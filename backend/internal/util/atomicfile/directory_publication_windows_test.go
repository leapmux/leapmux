//go:build windows

package atomicfile

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// plantRecord plants a record with the stated phase inside dir, for a
// subtree the test publishes by hand.
func plantRecord(t *testing.T, dir, target string, phase publicationPhase) {
	t.Helper()
	rec := &publicationRecord{
		AccountID:       currentAccountID(),
		Directory:       dir,
		TargetDirectory: target,
		Phase:           phase,
	}
	require.NoError(t, writeRecord(recordPathAt(dir), rec))
}

// TestUnconfirmedWindowsPublicationFailsClosed pins the fail-closed
// rule: a prepared record a fresh invocation finds on an existing
// subtree is reported as ErrDirectoryPublicationUncertain, the subtree
// is neither renamed nor removed nor guessed at, and no credential or
// application state may follow. A committed record is plain reusable
// storage.
func TestUnconfirmedWindowsPublicationFailsClosed(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "leapmux")
	target := filepath.Join(root, "worker")
	require.NoError(t, os.MkdirAll(target, 0o700))
	plantRecord(t, root, target, phasePrepared)
	before, err := os.ReadFile(recordPathAt(root))
	require.NoError(t, err)

	err = MkdirAllDurable(filepath.Join(target, "cache"))
	require.ErrorIs(t, err, ErrDirectoryPublicationUncertain)
	assert.Contains(t, err.Error(), root, "the error identifies the unconfirmed prefix")

	// The subtree is untouched: no rename, no removal, no deeper
	// creation, and the record's bytes are unchanged.
	after, readErr := os.ReadFile(recordPathAt(root))
	require.NoError(t, readErr)
	assert.Equal(t, string(before), string(after))
	assert.NoDirExists(t, filepath.Join(target, "cache"))

	// A committed record is safe to reuse: the deeper creation goes
	// ahead and publishes its own part.
	plantRecord(t, root, target, phaseCommitted)
	require.NoError(t, MkdirAllDurable(filepath.Join(target, "cache")))
	assert.DirExists(t, filepath.Join(target, "cache"))
}
