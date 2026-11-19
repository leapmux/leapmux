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

func TestDiracTaskletMetadataPreservesReadAndCloseFailures(t *testing.T) {
	t.Parallel()
	for _, populated := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing store", true: "readable store"}[populated], func(t *testing.T) {
			t.Parallel()
			directory := t.TempDir()
			if populated {
				writeDiracTaskletStore(t, directory)
			}
			root, err := sessionstore.OpenArchiveRoot(directory)
			require.NoError(t, err)
			closeErr := errors.New("injected native tasklet root close failure")
			metadata, err := readDiracTaskletMetadataFromRoot(diracCloseFaultRoot{ArchiveRoot: root, closeErr: closeErr}, "dirac-session")
			require.ErrorIs(t, err, closeErr)
			if !populated {
				require.ErrorIs(t, err, os.ErrNotExist)
			}
			assert.Nil(t, metadata, "a close failure cannot return admitted native metadata")
		})
	}
}

func TestDiracTaskletMetadataRefusesSymlinkAncestors(t *testing.T) {
	t.Parallel()
	for _, ancestor := range []string{"data", "data/tasks", "data/tasks/native-root", "data/tasks/native-root/tasklets", "data/tasks/native-root/tasklets/Witten", "data/acp-session-runtime-configs"} {
		t.Run(ancestor, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			writeDiracTaskletStore(t, root)
			path := filepath.Join(root, filepath.FromSlash(ancestor))
			target := path + "-real"
			require.NoError(t, os.Rename(path, target))
			if err := os.Symlink(target, path); err != nil {
				t.Skipf("symlinks are unavailable: %v", err)
			}
			metadata, err := readDiracTaskletMetadata(root, "dirac-session")
			require.Error(t, err, "even an in-root link cannot supply native tasklet data")
			assert.Nil(t, metadata)
		})
	}
}

func TestDiracTaskletMetadataRefusesItsRootAsAChild(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeDiracTaskletStore(t, root)
	mutateDiracTaskletRecord(t, root, func(record map[string]any) { record["conversationUlid"] = "dirac-session" })
	metadata, err := readDiracTaskletMetadata(root, "dirac-session")
	require.Error(t, err)
	assert.Nil(t, metadata)
}
