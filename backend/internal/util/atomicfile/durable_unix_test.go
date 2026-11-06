//go:build unix

package atomicfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordedFlushes extracts the directory flush paths a recording seam
// observed, in order.
func recordedFlushes(rec *recordingOps) []string {
	var flushes []string
	for _, c := range rec.calls {
		if strings.HasPrefix(c, "syncDir:") {
			flushes = append(flushes, strings.TrimPrefix(c, "syncDir:"))
		}
	}
	return flushes
}

// TestDurableParentCreation pins the order a new subtree's publication
// goes through: the staged directories flush from the leaves to the
// staging root, the staging root publishes without replacing anything,
// the parent flushes, and only then does the committed record replace
// the prepared one.
func TestDurableParentCreation(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "existing", "deep", "leaf")
	require.NoError(t, os.Mkdir(filepath.Join(base, "existing"), 0o700))

	recOps := &recordingOps{}
	withOps(t, recOps)

	var publishFrom, publishTo string
	prevPublish := publishExclusive
	publishExclusive = func(oldpath, newpath string) error {
		publishFrom, publishTo = oldpath, newpath
		return prevPublish(oldpath, newpath)
	}
	t.Cleanup(func() { publishExclusive = prevPublish })

	require.NoError(t, MkdirAllDurable(target))

	// The destination tree exists, exactly once, and no staging tree is
	// left behind.
	info, err := os.Stat(target)
	require.NoError(t, err)
	assert.True(t, info.IsDir())
	entries, err := os.ReadDir(filepath.Join(base, "existing"))
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "deep", entries[0].Name())

	// The publish renamed THIS invocation's staging root onto the first
	// missing prefix.
	assert.Equal(t, filepath.Join(base, "existing", "deep"), publishTo)
	assert.Contains(t, filepath.Base(publishFrom), stagePrefix)

	// The committed record sits at the first new directory with the
	// provenance of this publication.
	rec, err := loadRecordAt(recordPathAt(filepath.Join(base, "existing", "deep")))
	require.NoError(t, err)
	assert.Equal(t, phaseCommitted, rec.Phase)
	assert.Equal(t, filepath.Join(base, "existing", "deep"), rec.Directory)
	assert.Equal(t, target, rec.TargetDirectory)
	assert.Equal(t, currentAccountID(), rec.AccountID)

	// The order of directory flushes: the staged subtree from its leaves
	// to the staging root, then the parent, then the committed record's
	// own publication of the new directory.
	flushes := recordedFlushes(recOps)
	require.GreaterOrEqual(t, len(flushes), 3, "leaves, staging root, and the parent all flush")
	assert.Equal(t, filepath.Join(base, "existing"), flushes[len(flushes)-2],
		"the parent flushes after the staged subtree")
	assert.Equal(t, filepath.Join(base, "existing", "deep"), flushes[len(flushes)-1],
		"the committed record's publication flushes the new directory last")
	// Every flush before the parent targeted the staging tree.
	for _, f := range flushes[:len(flushes)-2] {
		assert.Contains(t, f, stagePrefix, "only the staging tree flushes before the parent")
	}
}

// TestRetryPublishesSurvivingAncestor reproduces the interrupted first
// command: the staged prefix became visible but the parent never
// flushed, so the record stays prepared. The retry, from fresh command
// state, repairs the recorded parent and commits the record BEFORE any
// pending state or registration exists, and a pre-existing unrelated
// chain needs no ancestor flush at all.
func TestRetryPublishesSurvivingAncestor(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "leapmux")
	target := filepath.Join(root, "worker", "cache")

	// The pending destination and the registration stand-ins: nothing
	// may exist until the publication confirms.
	pendingPath := filepath.Join(target, "pending-registration.json")
	registrationCalls := 0
	attempt := func(dir string) error {
		if err := MkdirAllDurable(dir); err != nil {
			return err
		}
		registrationCalls++
		return nil
	}

	// Fail exactly the parent flush of the first missing prefix, so the
	// staging tree publishes and the record stays prepared.
	lockOpsForTest(t)
	swapOps(&targetFailingOps{failPath: base})

	err := attempt(target)
	require.Error(t, err, "the interrupted publication reports its failure")
	assert.Zero(t, registrationCalls, "no registration happens while publication is unconfirmed")
	assert.NoFileExists(t, pendingPath)

	// The surviving ancestor carries its prepared record.
	prepared, loadErr := loadRecordAt(recordPathAt(root))
	require.NoError(t, loadErr)
	require.Equal(t, phasePrepared, prepared.Phase)

	// The retry, with the failure gone, repairs the ancestor first and
	// publishes the rest.
	swapOps(defaultFileOps{})
	require.NoError(t, attempt(target))
	assert.Equal(t, 1, registrationCalls)

	repaired, loadErr := loadRecordAt(recordPathAt(root))
	require.NoError(t, loadErr)
	assert.Equal(t, phaseCommitted, repaired.Phase)
	assert.Equal(t, prepared.AccountID, repaired.AccountID)
	assert.Equal(t, prepared.Directory, repaired.Directory)
	assert.Equal(t, prepared.TargetDirectory, repaired.TargetDirectory,
		"the repair keeps the original provenance")

	// A pre-existing unrelated chain: creating a directory inside an
	// externally provisioned tree flushes only what this helper created.
	unrelated := filepath.Join(base, "external", "provisioned", "state")
	require.NoError(t, os.MkdirAll(filepath.Join(base, "external", "provisioned"), 0o700))
	recOps := &recordingOps{}
	swapOps(recOps)
	require.NoError(t, MkdirAllDurable(unrelated))
	flushes := recordedFlushes(recOps)
	require.GreaterOrEqual(t, len(flushes), 3,
		"the staging root, the parent, and the committed record all flush")
	assert.Equal(t, filepath.Join(base, "external", "provisioned", "state"), flushes[len(flushes)-1],
		"the committed record's publication flushes the new directory last")
	assert.Equal(t, filepath.Join(base, "external", "provisioned"), flushes[len(flushes)-2],
		"the parent of what this helper created flushes after the staging tree")
	for _, path := range flushes[:len(flushes)-2] {
		assert.Contains(t, path, stagePrefix, "every earlier flush targets this invocation's staging tree")
	}
	for _, path := range flushes {
		assert.NotEqual(t, filepath.Join(base, "external"), path,
			"a pre-existing unrelated ancestor is never read or flushed")
	}
}

// targetFailingOps fails the directory flush of exactly one path, the
// one an interrupted first publication dies on, and forwards the rest.
type targetFailingOps struct {
	defaultFileOps
	failPath   string
	failedOnce bool
}

func (f *targetFailingOps) syncDir(path string) error {
	if !f.failedOnce && path == f.failPath {
		f.failedOnce = true
		return os.ErrPermission
	}
	return f.defaultFileOps.syncDir(path)
}
