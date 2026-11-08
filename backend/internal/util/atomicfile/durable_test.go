package atomicfile

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// failingOps fails ONE operation the first time it runs and forwards
// everything else, so a test can pin which step a durable writer's error
// came from and what each failure leaves behind.
type failingOps struct {
	defaultFileOps
	failAt string
	failed bool
}

func (f *failingOps) maybe(op string) error {
	if !f.failed && op == f.failAt {
		f.failed = true
		return os.ErrPermission
	}
	return nil
}

func (f *failingOps) createTemp(dir, pattern string, mode os.FileMode) (*os.File, error) {
	if err := f.maybe("createTemp"); err != nil {
		return nil, err
	}
	return f.defaultFileOps.createTemp(dir, pattern, mode)
}

func (f *failingOps) chmod(file *os.File, mode os.FileMode) error {
	if err := f.maybe("chmod"); err != nil {
		return err
	}
	return f.defaultFileOps.chmod(file, mode)
}

func (f *failingOps) write(file *os.File, data []byte) error {
	if err := f.maybe("write"); err != nil {
		return err
	}
	return f.defaultFileOps.write(file, data)
}

func (f *failingOps) sync(file *os.File) error {
	if err := f.maybe("sync"); err != nil {
		return err
	}
	return f.defaultFileOps.sync(file)
}

func (f *failingOps) close(file *os.File) error {
	return errors.Join(f.defaultFileOps.close(file), f.maybe("close"))
}

func (f *failingOps) rename(oldpath, newpath string) error {
	if err := f.maybe("rename"); err != nil {
		return err
	}
	return f.defaultFileOps.rename(oldpath, newpath)
}

func (f *failingOps) remove(path string) error {
	if err := f.maybe("remove"); err != nil {
		return err
	}
	return f.defaultFileOps.remove(path)
}

func (f *failingOps) syncDir(path string) error {
	if err := f.maybe("syncDir"); err != nil {
		return err
	}
	return f.defaultFileOps.syncDir(path)
}

// opsLock serializes seam swaps: the package var is process-global, so
// two swapping tests that ran at once would observe each other's
// substitution. Tests that never swap the seam stay free to run in
// parallel with anything.
var opsLock sync.Mutex

// withOps takes the seam for one test body. It holds opsLock until the
// test's cleanup, so a parallel test that swaps the seam waits rather
// than racing, and its cleanup restores the ORIGINAL seam whatever the
// test swapped in afterwards.
func withOps(t *testing.T, replacement fileOps) {
	t.Helper()
	opsLock.Lock()
	original := ops
	ops = replacement
	t.Cleanup(func() {
		ops = original
		opsLock.Unlock()
	})
}

// TestDurablePublicationFailures injects a failure at each publication
// stage separately. Every stage reports its error, and the destination
// never holds a partial document: before the rename it keeps the old
// bytes, after it it holds the new ones whole. A caller that keeps its
// recovery material until success can always retry safely.
func TestDurablePublicationFailures(t *testing.T) {
	stages := []string{"sync", "rename"}
	if runtime.GOOS != "windows" {
		stages = append(stages, "syncDir")
	}
	for _, stage := range stages {
		t.Run("failure at "+stage, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "creds.json")
			require.NoError(t, WriteFile(path, []byte("old"), 0o600))

			failing := &failingOps{failAt: stage}
			withOps(t, failing)
			injectDurableReplacementFailure(t, stage)

			err := WriteFileDurable(path, []byte("new"), 0o600)
			require.ErrorIs(t, err, os.ErrPermission, "the %s failure must surface", stage)

			data, readErr := os.ReadFile(path)
			require.NoError(t, readErr)
			assert.Contains(t, []string{"old", "new"}, string(data),
				"the destination holds one whole document, never a mixture")

			// A failed write must not leave its temporary file behind:
			// it holds the same secret the destination holds.
			entries, listErr := os.ReadDir(dir)
			require.NoError(t, listErr)
			for _, e := range entries {
				assert.False(t, len(e.Name()) > len("creds.json") &&
					e.Name()[:len("creds.json")] == "creds.json" && e.Name() != "creds.json",
					"no temporary file may survive a failed write: %s", e.Name())
			}
		})
	}
}

// A durable write into a missing directory fails like WriteFile does:
// the caller creates the directory, durably, through MkdirAllDurable.
func TestWriteFileDurable_ReportsAMissingDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent", "creds.json")
	assert.Error(t, WriteFileDurable(path, []byte("x"), 0o600))
}
