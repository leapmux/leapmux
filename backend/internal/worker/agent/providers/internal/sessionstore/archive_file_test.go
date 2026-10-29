package sessionstore

import (
	"bytes"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type archiveBytesReadFailure struct{ err error }

func (reader archiveBytesReadFailure) Read([]byte) (int, error) { return 0, reader.err }

func TestReadSizedArchiveBytesRequiresTheCompleteCheckedSize(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, content string
		size          int64
		valid         bool
	}{
		{name: "exact size", content: "safe", size: 4, valid: true},
		{name: "truncated", content: "saf", size: 4},
		{name: "one extra byte", content: "safer", size: 4},
		{name: "several extra bytes", content: "saferstill", size: 4},
		{name: "empty", valid: true},
		{name: "empty file gained a byte", content: "x"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			reader := bytes.NewReader([]byte(test.content))
			data, err := readSizedArchiveBytes(reader, test.size)
			assert.LessOrEqual(t, int64(len(test.content)-reader.Len()), test.size+1, "read no more than the checked size and its growth sentinel")
			if test.valid {
				require.NoError(t, err)
				assert.Equal(t, test.content, string(data))
			} else {
				require.ErrorContains(t, err, "complete checked bytes")
				assert.Nil(t, data)
			}
		})
	}
}

func TestReadSizedArchiveBytesPreservesReadErrorsWithoutData(t *testing.T) {
	t.Parallel()
	readError := errors.New("injected archive read failure")
	reader := io.MultiReader(bytes.NewReader([]byte("sa")), archiveBytesReadFailure{err: readError})
	data, err := readSizedArchiveBytes(reader, 4)
	require.ErrorIs(t, err, readError)
	assert.Nil(t, data)
}

func TestReadSizedArchiveBytesRejectsInvalidSizesBeforeReading(t *testing.T) {
	t.Parallel()
	for _, size := range []int64{-1, int64(^uint(0) >> 1), math.MaxInt64} {
		reader := bytes.NewReader([]byte("safe"))
		data, err := readSizedArchiveBytes(reader, size)
		require.Error(t, err)
		assert.Nil(t, data)
		assert.Equal(t, 4, reader.Len())
	}
}

type archiveFileTestRoot struct {
	*os.Root
	open func(string) (*os.File, error)
}

func (r archiveFileTestRoot) Open(name string) (*os.File, error) {
	if r.open != nil {
		return r.open(name)
	}
	return r.Root.Open(name)
}

func openArchiveFileTestRoot(t *testing.T, dir string) *os.Root {
	t.Helper()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	return root
}

func TestReadRegularFileAcceptsExactLimitAndEmptyFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "full.json"), []byte("valid"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "empty.json"), nil, 0o600))
	root := openArchiveFileTestRoot(t, dir)

	full, err := ReadRegularFile(root, "full.json", 5)
	require.NoError(t, err)
	assert.Equal(t, []byte("valid"), full)
	empty, err := ReadRegularFile(root, "empty.json", 0)
	require.NoError(t, err)
	assert.Empty(t, empty)
}

func TestReadRegularFileFirstLineKeepsTheReadLimitAndFileChecks(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "output")
	require.NoError(t, os.WriteFile(path, append([]byte("first\n"), bytes.Repeat([]byte("detail\n"), 10000)...), 0o600))
	root := openArchiveFileTestRoot(t, dir)
	line, err := ReadRegularFileFirstLine(root, "output", 6)
	require.NoError(t, err)
	assert.Equal(t, "first\n", string(line), "the complete file can exceed the line limit")
	for _, limit := range []int64{-1, 0, 5, math.MaxInt64} {
		line, err = ReadRegularFileFirstLine(root, "output", limit)
		require.Error(t, err)
		assert.Empty(t, line)
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "last"), []byte("last"), 0o600))
	line, err = ReadRegularFileFirstLine(root, "last", 4)
	require.NoError(t, err)
	assert.Equal(t, "last", string(line))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "empty"), nil, 0o600))
	line, err = ReadRegularFileFirstLine(root, "empty", 0)
	require.NoError(t, err)
	assert.Empty(t, line)
	changed := archiveFileTestRoot{Root: root, open: func(name string) (*os.File, error) {
		if err := os.Rename(path, path+".prior"); err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, []byte("first\n"), 0o600); err != nil {
			return nil, err
		}
		return root.Open(name)
	}}
	line, err = ReadRegularFileFirstLine(changed, "output", 6)
	require.ErrorContains(t, err, "changed before it opened")
	assert.Empty(t, line)
}

func TestReadRegularFileRejectsInvalidLimitAndFinalType(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "file.json"), []byte("data"), 0o600))
	require.NoError(t, os.Mkdir(filepath.Join(dir, "directory"), 0o700))
	root := openArchiveFileTestRoot(t, dir)

	for _, limit := range []int64{-1, math.MaxInt64} {
		data, err := ReadRegularFile(root, "file.json", limit)
		require.Error(t, err)
		assert.Empty(t, data)
	}
	data, err := ReadRegularFile(root, "file.json", 3)
	require.ErrorContains(t, err, "not a regular file within 3 bytes")
	require.ErrorIs(t, err, ErrArchiveFileModeOrSize)
	assert.Empty(t, data)
	data, err = ReadRegularFile(root, "directory", 100)
	require.ErrorIs(t, err, ErrArchiveFileModeOrSize)
	assert.Empty(t, data)
	data, err = ReadRegularFile(root, "missing.json", 100)
	require.ErrorIs(t, err, os.ErrNotExist)
	assert.Empty(t, data)
	t.Run("final symlink", func(t *testing.T) {
		if err := os.Symlink("file.json", filepath.Join(dir, "link.json")); err != nil {
			t.Skipf("the test filesystem cannot create a symlink: %v", err)
		}
		linkData, linkErr := ReadRegularFile(root, "link.json", 100)
		require.ErrorContains(t, linkErr, "symlink")
		assert.Empty(t, linkData)
	})
}

func TestReadRegularFileRejectsCheckedOpenSwapAndGrowth(t *testing.T) {
	for _, tc := range []struct {
		name    string
		symlink bool
		grow    bool
	}{
		{name: "regular replacement"},
		{name: "symlink replacement", symlink: true},
		{name: "file growth", grow: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, "archive.json")
			require.NoError(t, os.WriteFile(path, []byte("safe"), 0o600))
			root := openArchiveFileTestRoot(t, dir)
			swapped := false
			reader := archiveFileTestRoot{Root: root, open: func(name string) (*os.File, error) {
				swapped = true
				if tc.grow {
					require.NoError(t, os.WriteFile(path, []byte("unsafe"), 0o600))
				} else {
					require.NoError(t, os.Rename(path, path+".prior"))
					if tc.symlink {
						outside := filepath.Join(t.TempDir(), "outside.json")
						require.NoError(t, os.WriteFile(outside, []byte("unsafe"), 0o600))
						if err := os.Symlink(outside, path); err != nil {
							t.Skipf("the test filesystem cannot create a symlink: %v", err)
						}
					} else {
						require.NoError(t, os.WriteFile(path, []byte("evil"), 0o600))
					}
				}
				return root.Open(name)
			}}
			data, err := ReadRegularFile(reader, "archive.json", 4)
			assert.True(t, swapped)
			require.Error(t, err)
			assert.Empty(t, data)
		})
	}
}

func TestReadRegularFileReturnsOpenAndStatErrorsWithoutData(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "archive.json"), []byte("safe"), 0o600))
	root := openArchiveFileTestRoot(t, dir)
	openError := errors.New("injected open failure")
	data, err := ReadRegularFile(archiveFileTestRoot{Root: root, open: func(string) (*os.File, error) {
		return nil, openError
	}}, "archive.json", 4)
	require.ErrorIs(t, err, openError)
	assert.Empty(t, data)

	data, err = ReadRegularFile(archiveFileTestRoot{Root: root, open: func(name string) (*os.File, error) {
		file, openErr := root.Open(name)
		if openErr != nil {
			return nil, openErr
		}
		require.NoError(t, file.Close())
		return file, nil
	}}, "archive.json", 4)
	require.Error(t, err)
	assert.Empty(t, data)
}

func TestReadRegularFileJoinsStatAndCloseErrors(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "archive.json"), []byte("safe"), 0o600))
	root := openArchiveFileTestRoot(t, dir)
	reader := archiveFileTestRoot{Root: root, open: func(name string) (*os.File, error) {
		file, err := root.Open(name)
		require.NoError(t, err)
		require.NoError(t, file.Close())
		return file, nil
	}}

	data, err := ReadRegularFile(reader, "archive.json", 4)
	assert.Empty(t, data)
	var joined interface{ Unwrap() []error }
	require.ErrorAs(t, err, &joined)
	require.Len(t, joined.Unwrap(), 2)
	for _, cause := range joined.Unwrap() {
		if runtime.GOOS == "windows" && !errors.Is(cause, os.ErrClosed) {
			// A closed descriptor can answer an invalid-handle error instead.
			require.ErrorContains(t, cause, "handle is invalid")
			continue
		}
		require.ErrorIs(t, cause, os.ErrClosed)
	}
}

func TestReadRegularFileRejectsChangesAfterTheFileOpens(t *testing.T) {
	for _, scenario := range []string{"replacement", "removal", "truncate", "grow within limit", "same size write", "symlink"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, "archive.json")
			require.NoError(t, os.WriteFile(path, []byte("safe"), 0o600))
			root := openArchiveFileTestRoot(t, dir)
			reader := archiveFileTestRoot{Root: root, open: func(name string) (*os.File, error) {
				file, err := root.Open(name)
				if err != nil {
					return nil, err
				}
				switch scenario {
				case "replacement", "symlink":
					require.NoError(t, os.Rename(path, path+".prior"))
					if scenario == "symlink" {
						if err := os.Symlink("archive.json.prior", path); err != nil {
							require.NoError(t, file.Close())
							t.Skipf("the test filesystem cannot create a symlink: %v", err)
						}
					} else {
						require.NoError(t, os.WriteFile(path, []byte("evil"), 0o600))
					}
				case "removal":
					require.NoError(t, os.Remove(path))
				case "truncate":
					require.NoError(t, os.Truncate(path, 1))
				case "grow within limit":
					require.NoError(t, os.WriteFile(path, []byte("larger"), 0o600))
				case "same size write":
					require.NoError(t, os.WriteFile(path, []byte("evil"), 0o600))
					modified := time.Unix(1, 0)
					require.NoError(t, os.Chtimes(path, modified, modified))
				}
				return file, nil
			}}
			data, err := ReadRegularFile(reader, "archive.json", 10)
			require.Error(t, err)
			assert.Empty(t, data)
		})
	}
}
