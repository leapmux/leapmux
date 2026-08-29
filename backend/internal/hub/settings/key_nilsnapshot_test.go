package settings

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A nil *Snapshot is a value this codebase PRODUCES, not a caller's mistake: a
// component wired with no settings manager has nothing to snapshot and returns
// nil to say so. Of must answer with the default there, exactly as it does for
// a key the snapshot never carried.
//
// Before this, Of dereferenced s.values and the nil crashed the process. It
// went unnoticed because a fully wired hub never reaches it -- only a test
// harness or a partially wired build does, so the panic showed up as an
// unrelated handler failing.
func TestKeyOf_NilSnapshotReadsTheDefault(t *testing.T) {
	t.Parallel()

	flag := NewKey[bool]("nil_snapshot.flag").WithDefault(true)
	assert.True(t, flag.Of(nil), "a nil snapshot must read the key's default")

	// A key whose default is the ZERO value still answers, rather than being
	// indistinguishable from a panic that a recover swallowed.
	off := NewKey[bool]("nil_snapshot.off")
	assert.False(t, off.Of(nil))

	// A composite default is COPIED, so a caller that mutates what it reads
	// cannot poison the next read -- the same guarantee the populated path
	// gives.
	type doc struct {
		Items []string `json:"items"`
	}
	composite := NewKey[doc]("nil_snapshot.doc").WithDefault(doc{Items: []string{"a"}})
	first := composite.Of(nil)
	require.Equal(t, []string{"a"}, first.Items)
	first.Items[0] = "mutated"
	assert.Equal(t, []string{"a"}, composite.Of(nil).Items,
		"the default must be copied, not shared, on the nil-snapshot path too")
}
