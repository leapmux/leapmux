package atomicfile

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func openTestRoot(t *testing.T, dir string) *os.Root {
	t.Helper()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = root.Close() })
	return root
}

func TestWriteFileInRoot_RootLevelAndNestedOverwrite(t *testing.T) {
	dir := t.TempDir()
	root := openTestRoot(t, dir)

	// Root-level create and overwrite at requested modes.
	require.NoError(t, WriteFileInRoot(root, "creds.json", []byte(`{"v":1}`), 0o600))
	data, err := root.ReadFile("creds.json")
	require.NoError(t, err)
	assert.Equal(t, `{"v":1}`, string(data))
	if runtime.GOOS != "windows" {
		info, statErr := root.Stat("creds.json")
		require.NoError(t, statErr)
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	}

	require.NoError(t, WriteFileInRoot(root, "creds.json", []byte(`{"v":2}`), 0o644))
	data, err = root.ReadFile("creds.json")
	require.NoError(t, err)
	assert.Equal(t, `{"v":2}`, string(data))
	if runtime.GOOS != "windows" {
		info, statErr := root.Stat("creds.json")
		require.NoError(t, statErr)
		assert.Equal(t, os.FileMode(0o644), info.Mode().Perm())
	}

	// Empty payload overwrite.
	require.NoError(t, WriteFileInRoot(root, "creds.json", nil, 0o600))
	data, err = root.ReadFile("creds.json")
	require.NoError(t, err)
	assert.Empty(t, data)

	// Nested directory create and overwrite using forward-slash relative path.
	require.NoError(t, root.MkdirAll(filepath.Join("nested", "sub"), 0o700))
	nestedRel := "nested/sub/state.json"
	require.NoError(t, WriteFileInRoot(root, nestedRel, []byte("first"), 0o600))
	require.NoError(t, WriteFileInRoot(root, nestedRel, []byte("second"), 0o640))

	data, err = root.ReadFile(nestedRel)
	require.NoError(t, err)
	assert.Equal(t, "second", string(data))
	if runtime.GOOS != "windows" {
		info, statErr := root.Stat(nestedRel)
		require.NoError(t, statErr)
		assert.Equal(t, os.FileMode(0o640), info.Mode().Perm())
	}

	rootEntries, err := os.ReadDir(dir)
	require.NoError(t, err)
	var rootNames []string
	for _, e := range rootEntries {
		rootNames = append(rootNames, e.Name())
	}
	assert.ElementsMatch(t, []string{"creds.json", "nested"}, rootNames,
		"no temporary file may remain at root level")

	subEntries, err := os.ReadDir(filepath.Join(dir, "nested", "sub"))
	require.NoError(t, err)
	require.Len(t, subEntries, 1, "no temporary file may remain in nested directory")
	assert.Equal(t, "state.json", subEntries[0].Name())
}

func TestWriteFileInRoot_NeverUsesTheNameDerivedFromTheDestination(t *testing.T) {
	dir := t.TempDir()
	root := openTestRoot(t, dir)

	require.NoError(t, root.Mkdir("creds.json.tmp", 0o700))
	require.NoError(t, WriteFileInRoot(root, "creds.json", []byte("one"), 0o600))

	data, err := root.ReadFile("creds.json")
	require.NoError(t, err)
	assert.Equal(t, "one", string(data))
}

func TestWriteFileInRoot_ConcurrentWritersAndReadersSeeWholeDocuments(t *testing.T) {
	dir := t.TempDir()
	root := openTestRoot(t, dir)

	first := strings.Repeat("a", 64*1024)
	second := strings.Repeat("b", 64*1024)
	require.NoError(t, WriteFileInRoot(root, "creds.json", []byte(first), 0o600))

	var wg sync.WaitGroup
	for _, payload := range []string{first, second} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 25 {
				assert.NoError(t, WriteFileInRoot(root, "creds.json", []byte(payload), 0o600))
			}
		}()
	}

	// Concurrent readers observe only complete documents, never partial writes.
	stopReaders := make(chan struct{})
	var readersWG sync.WaitGroup
	readersWG.Add(1)
	go func() {
		defer readersWG.Done()
		for {
			select {
			case <-stopReaders:
				return
			default:
			}
			data, err := root.ReadFile("creds.json")
			if err != nil {
				if runtime.GOOS == "windows" {
					// Windows can transiently reject an open while a rename
					// replaces the destination; every read that succeeds must
					// still observe a complete document.
					continue
				}
				assert.NoError(t, err)
				return
			}
			got := string(data)
			if got != first && got != second {
				assert.Failf(t, "observed partial or mixed document", "len=%d", len(got))
				return
			}
		}
	}()

	wg.Wait()
	close(stopReaders)
	readersWG.Wait()

	data, err := root.ReadFile("creds.json")
	require.NoError(t, err)
	got := string(data)
	assert.True(t, got == first || got == second,
		"the destination must hold exactly one writer's document")

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1, "every temporary file must be gone")
	assert.Equal(t, "creds.json", entries[0].Name())
}

func TestWriteFileInRoot_RejectsInvalidInputs(t *testing.T) {
	assert.Error(t, WriteFileInRoot(nil, "creds.json", []byte("x"), 0o600))

	dir := t.TempDir()
	root := openTestRoot(t, dir)
	absPath := filepath.Join(t.TempDir(), "escape.txt")

	cases := []struct {
		name string
		rel  string
	}{
		{name: "empty", rel: ""},
		{name: "dot", rel: "."},
		{name: "dot-slash-dot", rel: "./."},
		{name: "dotdot", rel: ".."},
		{name: "parent-escape", rel: "../escape"},
		{name: "nested-parent-collapse", rel: "sub/.."},
		{name: "nested-parent-traversal", rel: "sub/../file.txt"},
		{name: "trailing-slash", rel: "sub/"},
		{name: "trailing-dot", rel: "sub/."},
		{name: "absolute", rel: absPath},
		{name: "missing-parent-dir", rel: "absent/creds.json"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := WriteFileInRoot(root, tc.rel, []byte("secret"), 0o600)
			assert.Error(t, err)
		})
	}

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, entries, "rejected writes must leave the root untouched")
	assert.NoFileExists(t, absPath)
}

func TestWriteFileInRoot_SymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks requires elevated privileges on Windows")
	}

	t.Run("directory symlink escaping root is rejected", func(t *testing.T) {
		rootDir := t.TempDir()
		outsideDir := t.TempDir()
		require.NoError(t, os.Symlink(outsideDir, filepath.Join(rootDir, "escape_dir")))

		root := openTestRoot(t, rootDir)
		err := WriteFileInRoot(root, "escape_dir/target.txt", []byte("secret"), 0o600)
		require.Error(t, err)

		outsideEntries, readErr := os.ReadDir(outsideDir)
		require.NoError(t, readErr)
		assert.Empty(t, outsideEntries, "no file or temp file may be created outside root")
	})

	t.Run("file symlink escaping root never modifies outside target", func(t *testing.T) {
		rootDir := t.TempDir()
		outsideDir := t.TempDir()
		outsideFile := filepath.Join(outsideDir, "outside.txt")
		require.NoError(t, os.WriteFile(outsideFile, []byte("outside-original"), 0o600))
		require.NoError(t, os.Symlink(outsideFile, filepath.Join(rootDir, "escape_file")))

		root := openTestRoot(t, rootDir)
		// renameat replaces the link entry inside root without following it, so
		// the outside target stays untouched and no temporary file remains.
		require.NoError(t, WriteFileInRoot(root, "escape_file", []byte("inside-only"), 0o600))

		outsideData, err := os.ReadFile(outsideFile)
		require.NoError(t, err)
		assert.Equal(t, "outside-original", string(outsideData),
			"outside symlink target must remain unmodified")

		info, err := root.Lstat("escape_file")
		require.NoError(t, err)
		assert.True(t, info.Mode().IsRegular(),
			"destination inside root must be a regular file, not a symlink")

		insideData, err := root.ReadFile("escape_file")
		require.NoError(t, err)
		assert.Equal(t, "inside-only", string(insideData))

		entries, err := os.ReadDir(rootDir)
		require.NoError(t, err)
		require.Len(t, entries, 1)
		assert.Equal(t, "escape_file", entries[0].Name())
	})
}

func TestWriteFileInRoot_CleansUpTemporaryFileOnFailure(t *testing.T) {
	t.Run("destination is a directory", func(t *testing.T) {
		dir := t.TempDir()
		root := openTestRoot(t, dir)
		require.NoError(t, root.Mkdir("creds.json", 0o700))

		require.Error(t, WriteFileInRoot(root, "creds.json", []byte("secret"), 0o600))

		entries, err := os.ReadDir(dir)
		require.NoError(t, err)
		require.Len(t, entries, 1)
		assert.Equal(t, "creds.json", entries[0].Name(),
			"no temporary file may survive a failed rename")
	})

	t.Run("sync failure removes temporary file", func(t *testing.T) {
		dir := t.TempDir()
		root := openTestRoot(t, dir)
		syncErr := errors.New("sync failed")
		withOps(t, &writerCleanupOps{primaryStage: "sync", primary: syncErr})

		err := WriteFileInRoot(root, "creds.json", []byte("secret"), 0o600)
		require.ErrorIs(t, err, syncErr)

		entries, readErr := os.ReadDir(dir)
		require.NoError(t, readErr)
		assert.Empty(t, entries, "sync failure must remove the temporary file inside root")
	})

	t.Run("read-only destination", func(t *testing.T) {
		dir := t.TempDir()
		root := openTestRoot(t, dir)
		require.NoError(t, WriteFileInRoot(root, "creds.json", []byte("one"), 0o600))
		require.NoError(t, root.Chmod("creds.json", 0o400))
		t.Cleanup(func() { _ = root.Chmod("creds.json", 0o600) })

		err := WriteFileInRoot(root, "creds.json", []byte("two"), 0o600)
		if runtime.GOOS == "windows" {
			require.Error(t, err, "Windows cannot replace a read-only file")
			data, readErr := root.ReadFile("creds.json")
			require.NoError(t, readErr)
			assert.Equal(t, "one", string(data))
		} else {
			require.NoError(t, err)
			data, readErr := root.ReadFile("creds.json")
			require.NoError(t, readErr)
			assert.Equal(t, "two", string(data))
		}

		entries, listErr := os.ReadDir(dir)
		require.NoError(t, listErr)
		require.Len(t, entries, 1, "no temporary file may survive")
		assert.Equal(t, "creds.json", entries[0].Name())
	})
}
