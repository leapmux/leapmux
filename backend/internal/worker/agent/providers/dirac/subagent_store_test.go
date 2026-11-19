package dirac

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/sessionstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type diracSwapFileOpener struct {
	*os.Root
	beforeOpen func()
}

func (opener *diracSwapFileOpener) Open(path string) (*os.File, error) {
	if opener.beforeOpen != nil {
		beforeOpen := opener.beforeOpen
		opener.beforeOpen = nil
		beforeOpen()
	}
	return opener.Root.Open(path)
}

type diracCloseFaultRoot struct {
	sessionstore.ArchiveRoot
	closeErr error
}

func (root diracCloseFaultRoot) Close() error {
	return errors.Join(root.ArchiveRoot.Close(), root.closeErr)
}

func TestReadDiracRegularFileRejectsSwapBetweenStatAndOpen(t *testing.T) {
	for _, tc := range []struct {
		name    string
		symlink bool
	}{
		{name: "external symlink", symlink: true},
		{name: "different regular file"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			path := filepath.Join(root, "tasklet.json")
			require.NoError(t, os.WriteFile(path, []byte("original archive"), 0o644))
			archiveRoot, err := os.OpenRoot(root)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, archiveRoot.Close()) })
			swapped := false
			opener := &diracSwapFileOpener{Root: archiveRoot, beforeOpen: func() {
				swapped = true
				require.NoError(t, os.Rename(path, path+"-prior"))
				if tc.symlink {
					outside := filepath.Join(t.TempDir(), "outside.json")
					require.NoError(t, os.WriteFile(outside, []byte("outside archive"), 0o644))
					if err := os.Symlink(outside, path); err != nil {
						t.Skipf("symlinks are unavailable: %v", err)
					}
					return
				}
				require.NoError(t, os.WriteFile(path, []byte("different archive"), 0o644))
			}}
			data, err := readDiracRegularFile(opener, "tasklet.json", diracTaskHistoryLimit)
			assert.True(t, swapped, "the test swaps the file after Lstat")
			require.Error(t, err, "the reader must refuse a file that changed after Lstat")
			assert.Empty(t, data)
		})
	}
}
