package atomicfile_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/atomicfile"
)

func TestWriteFile_WritesTheContentAtTheRequestedMode(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "creds.json")
	require.NoError(t, atomicfile.WriteFile(path, []byte(`{"a":1}`), 0o600))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, `{"a":1}`, string(data))

	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(),
			"a credential file must not be readable by another account")
	}
}

// TestWriteFile_LeavesNoTemporaryFileBehind pins the ordinary path: the
// rename is the only file that remains.
func TestWriteFile_LeavesNoTemporaryFileBehind(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "creds.json")
	require.NoError(t, atomicfile.WriteFile(path, []byte("one"), 0o600))
	require.NoError(t, atomicfile.WriteFile(path, []byte("two"), 0o600))

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "creds.json", entries[0].Name())
}

// TestWriteFile_NeverUsesTheNameDerivedFromTheDestination is the property
// the whole atomicity claim rests on, and the one this test can prove
// deterministically.
//
// A temporary name of "<path>.tmp" comes from the destination alone,
// so two processes that write the same file at the same instant open the
// SAME temporary file, interleave their bytes into it, and rename a mixed
// document onto the destination -- after which every reader reports a parse
// failure. Occupying that one name is what makes the difference visible: a
// writer that composed it would fail here.
func TestWriteFile_NeverUsesTheNameDerivedFromTheDestination(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "creds.json")
	require.NoError(t, os.Mkdir(path+".tmp", 0o700))

	require.NoError(t, atomicfile.WriteFile(path, []byte("one"), 0o600))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "one", string(data))
}

// TestWriteFile_ConcurrentWritersEachLandAWholeDocument exercises the same
// property under real contention. It cannot fail on demand -- an interleave
// needs a write that the kernel splits -- so read it as a smoke test of the
// unique name, not as the proof; the test above is the proof.
func TestWriteFile_ConcurrentWritersEachLandAWholeDocument(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "creds.json")
	first := strings.Repeat("a", 64*1024)
	second := strings.Repeat("b", 64*1024)

	var wg sync.WaitGroup
	for _, payload := range []string{first, second} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 20 {
				assert.NoError(t, atomicfile.WriteFile(path, []byte(payload), 0o600))
			}
		}()
	}
	wg.Wait()

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	got := string(data)
	assert.True(t, got == first || got == second,
		"the destination must hold exactly one writer's document, never a mixture")

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Len(t, entries, 1, "every temporary file must be gone")
}

// TestWriteFile_RemovesTheTemporaryFileWhenTheRenameFails pins the failure
// path. The temporary file holds the same secret the destination holds, so
// a write that could not finish must not leave it on the disk.
func TestWriteFile_RemovesTheTemporaryFileWhenTheRenameFails(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	// A DIRECTORY at the destination: the write succeeds and the rename
	// cannot replace it.
	path := filepath.Join(dir, "creds.json")
	require.NoError(t, os.Mkdir(path, 0o700))

	require.Error(t, atomicfile.WriteFile(path, []byte("secret"), 0o600))

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "creds.json", entries[0].Name(), "no temporary file may survive a failed write")
}

func TestWriteFile_ReportsAMissingDirectory(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "absent", "creds.json")
	assert.Error(t, atomicfile.WriteFile(path, []byte("x"), 0o600),
		"WriteFile matches os.WriteFile: the caller creates the directory")
}

// TestRemoveTempFiles_SweepsWhatACrashLeft is the case a caller cannot
// reach through WriteFile, because WriteFile cleans up after itself: a
// process killed between the write and the rename leaves a temporary file
// that holds a live secret under a name no listing of the destination
// shows.
func TestRemoveTempFiles_SweepsWhatACrashLeft(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "creds.json")
	require.NoError(t, os.WriteFile(path, []byte("live"), 0o600))
	require.NoError(t, os.WriteFile(path+".tmp1234", []byte("secret"), 0o600))
	require.NoError(t, os.WriteFile(path+".tmp5678", []byte("secret"), 0o600))
	// A different destination's leftovers, and an unrelated file: neither
	// belongs to this path.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "other.json.tmp99"), []byte("other"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("keep"), 0o600))

	require.NoError(t, atomicfile.RemoveTempFiles(path))

	var names []string
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, e := range entries {
		names = append(names, e.Name())
	}
	assert.ElementsMatch(t, []string{"creds.json", "other.json.tmp99", "notes.txt"}, names,
		"only this destination's leftovers may be removed")
}

func TestRemoveTempFiles_AcceptsAMissingDirectory(t *testing.T) {
	t.Parallel()

	assert.NoError(t, atomicfile.RemoveTempFiles(filepath.Join(t.TempDir(), "absent", "creds.json")))
}

func TestRemoveTempFiles_AcceptsADirectoryWithNothingToRemove(t *testing.T) {
	t.Parallel()

	assert.NoError(t, atomicfile.RemoveTempFiles(filepath.Join(t.TempDir(), "creds.json")))
}
