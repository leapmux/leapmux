//go:build unix

package atomicfile

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A private writable data directory beneath an execute-only ancestor
// must stay usable: the helper neither opens nor lists the ancestor for
// reading. With mode 0111 any read-open fails, so a helper that tried
// would fail loudly here; success is the proof it did not.
func TestExistingExecuteOnlyAncestorRemainsUsable(t *testing.T) {
	base := canonicalTempDir(t)
	executeOnly := filepath.Join(base, "mode-0111")
	beneath := filepath.Join(executeOnly, "data")
	require.NoError(t, os.MkdirAll(beneath, 0o700))
	require.NoError(t, os.Chmod(executeOnly, 0o111))
	t.Cleanup(func() { _ = os.Chmod(executeOnly, 0o700) })

	target := filepath.Join(beneath, "leapmux", "worker")
	require.NoError(t, MkdirAllDurable(target))
	assert.DirExists(t, target)

	// A file flush inside the data directory works under the same
	// constraint.
	require.NoError(t, WriteFileDurable(filepath.Join(target, "state.json"), []byte("{}"), 0o600))
}

// TestPublicationConcurrentInitializers races two creators of the same
// tree. The exclusive publish makes one the winner; the loser removes
// only its own unpublished staging tree, inspects the winner's record,
// and returns. The destination holds exactly one subtree, no staging
// tree survives, and the record ends committed with one provenance.
func TestPublicationConcurrentInitializers(t *testing.T) {
	base := canonicalTempDir(t)
	target := filepath.Join(base, "leapmux", "worker")

	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make([]error, 2)
	for i := range 2 {
		wg.Add(1)
		go func(slot int) {
			defer wg.Done()
			<-start
			results[slot] = MkdirAllDurable(target)
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range results {
		assert.NoError(t, err, "invocation %d", i)
	}
	assert.DirExists(t, target)

	entries, err := os.ReadDir(base)
	require.NoError(t, err)
	require.Len(t, entries, 1, "no staging tree survives the race")
	assert.Equal(t, "leapmux", entries[0].Name())

	rec, loadErr := loadRecordAt(recordPathAt(filepath.Join(base, "leapmux")))
	require.NoError(t, loadErr)
	assert.Equal(t, phaseCommitted, rec.Phase)
}

// TestPublicationPrePublishFailureCleansOwnStage injects a publish
// failure: the invocation removes only its own staging tree, the
// destination never appears, and another invocation can publish after
// the fault clears.
func TestPublicationPrePublishFailureCleansOwnStage(t *testing.T) {
	base := canonicalTempDir(t)
	target := filepath.Join(base, "leapmux", "worker")

	prev := publishExclusive
	var staged atomic.Bool
	publishExclusive = func(oldpath, newpath string) error {
		staged.Store(true)
		return os.ErrPermission
	}
	t.Cleanup(func() { publishExclusive = prev })

	require.Error(t, MkdirAllDurable(target))

	require.True(t, staged.Load(), "the injection point ran")
	assert.NoDirExists(t, filepath.Join(base, "leapmux"))
	entries, err := os.ReadDir(base)
	require.NoError(t, err)
	assert.Empty(t, entries, "the failed invocation removed its own staging tree")

	// The destination itself is never cleaned up once published: a
	// failure AFTER the publish leaves it and its record intact.
	publishExclusive = prev
	require.NoError(t, MkdirAllDurable(target))
	assert.DirExists(t, target)
}

// TestPublicationPostPublishFailureKeepsDestination fails the parent
// flush after a successful publish: the destination stays, the record
// stays prepared, and a later invocation repairs it.
func TestPublicationPostPublishFailureKeepsDestination(t *testing.T) {
	base := canonicalTempDir(t)
	target := filepath.Join(base, "leapmux", "worker")

	lockOpsForTest(t)
	swapOps(&targetFailingOps{failPath: base})
	require.Error(t, MkdirAllDurable(target))
	assert.DirExists(t, target, "a published destination is never removed by failure cleanup")

	rec, err := loadRecordAt(recordPathAt(filepath.Join(base, "leapmux")))
	require.NoError(t, err)
	assert.Equal(t, phasePrepared, rec.Phase)

	swapOps(defaultFileOps{})
	require.NoError(t, MkdirAllDurable(target))
	rec, err = loadRecordAt(recordPathAt(filepath.Join(base, "leapmux")))
	require.NoError(t, err)
	assert.Equal(t, phaseCommitted, rec.Phase)
}

// TestConcurrentRepairsAreIdempotent drives two repairs of the same
// prepared record at once. Each performs the parent barrier and writes
// the same committed record; the provenance never changes and the
// directory never moves.
func TestConcurrentRepairsAreIdempotent(t *testing.T) {
	base := canonicalTempDir(t)
	target := filepath.Join(base, "leapmux", "worker")

	// Arrive at a prepared record: publish, then fail the parent flush.
	withOps(t, &targetFailingOps{failPath: base})
	require.Error(t, MkdirAllDurable(target))
	swapOps(defaultFileOps{})

	before, err := os.ReadFile(recordPathAt(filepath.Join(base, "leapmux")))
	require.NoError(t, err)
	original, err := loadRecordAt(recordPathAt(filepath.Join(base, "leapmux")))
	require.NoError(t, err)

	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results <- MkdirAllDurable(target)
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	for err := range results {
		assert.NoError(t, err, "each concurrent repair must succeed")
	}

	after, err := os.ReadFile(recordPathAt(filepath.Join(base, "leapmux")))
	require.NoError(t, err)
	rec, loadErr := loadRecordAt(recordPathAt(filepath.Join(base, "leapmux")))
	require.NoError(t, loadErr)
	assert.Equal(t, phaseCommitted, rec.Phase)
	original.Phase = phaseCommitted
	assert.Equal(t, original, rec, "repair must preserve every provenance field")
	assert.NotEqual(t, string(before), string(after), "the repair rewrote the prepared record")
	assert.DirExists(t, target, "the directory itself never moves")
}
