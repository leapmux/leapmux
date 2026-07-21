package postgres

import (
	"io"
	"io/fs"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStripCollateCLeavesNoCollation guards the CockroachDB migration path
// against a silent strip miss: stripCollateC matches ONE exact byte sequence
// (` COLLATE "C"`), so a future column hand-typed as `collate "C"`, with
// different spacing, or line-leading would escape the strip and fail the CRDB
// migration at startup ("invalid locale C") -- an outage no Postgres-only test
// can catch, because the strip only runs on the CRDB detection path. This test
// runs the real transform over the real embedded migrations and asserts no
// COLLATE survives in any spelling, so the invisible "hand-type exactly this
// byte sequence" rule becomes a mechanically checked one.
func TestStripCollateCLeavesNoCollation(t *testing.T) {
	sub, err := fs.Sub(migrations, "db/migrations")
	require.NoError(t, err)

	entries, err := fs.ReadDir(sub, ".")
	require.NoError(t, err)
	require.NotEmpty(t, entries, "embedded migrations must not be empty")

	anyCollate := regexp.MustCompile(`(?i)collate`)
	strippedTotal := 0
	for _, e := range entries {
		raw, err := fs.ReadFile(sub, e.Name())
		require.NoError(t, err)
		stripped := stripCollateC(raw)
		strippedTotal += len(raw) - len(stripped)
		assert.NotRegexp(t, anyCollate, string(stripped),
			"%s: a COLLATE clause survived stripCollateC -- its spelling does not match the exact ' COLLATE \"C\"' byte sequence the strip removes, and CockroachDB will reject the migration at startup",
			e.Name())
	}
	// The strip must actually have removed something: a zero delta means the
	// embed path or the strip literal broke and the assertion above passed
	// vacuously.
	assert.Positive(t, strippedTotal, "stripCollateC removed nothing from the embedded migrations")
}

// TestTransformFSServesTransformedContentConsistently pins the transformFS
// wrapper contract on the real embedded migrations: the Open path and the
// fs.ReadFile fast path (fs.ReadFileFS) must serve the SAME transformed bytes,
// and Stat().Size() must equal what Read serves -- the inner (pre-transform)
// FileInfo would over-report, and a consumer sizing a read from Stat would
// read a wrong-length view of the migration.
func TestTransformFSServesTransformedContentConsistently(t *testing.T) {
	sub, err := fs.Sub(migrations, "db/migrations")
	require.NoError(t, err)
	tfs := transformFS{inner: sub, transform: stripCollateC}

	entries, err := fs.ReadDir(tfs, ".")
	require.NoError(t, err)
	require.NotEmpty(t, entries)

	for _, e := range entries {
		viaReadFile, err := fs.ReadFile(tfs, e.Name())
		require.NoError(t, err)

		f, err := tfs.Open(e.Name())
		require.NoError(t, err)
		viaOpen, err := io.ReadAll(f)
		require.NoError(t, err)
		info, err := f.Stat()
		require.NoError(t, err)
		require.NoError(t, f.Close())

		assert.Equal(t, viaOpen, viaReadFile,
			"%s: the ReadFile fast path and the Open path must serve identical transformed bytes", e.Name())
		assert.Equal(t, int64(len(viaOpen)), info.Size(),
			"%s: Stat().Size() must report the transformed length Read serves, not the inner file's", e.Name())

		raw, err := fs.ReadFile(sub, e.Name())
		require.NoError(t, err)
		assert.NotEqual(t, raw, viaOpen,
			"%s: the transform must have been applied on both paths", e.Name())
	}
}
