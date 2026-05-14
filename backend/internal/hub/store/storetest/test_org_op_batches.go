package storetest

import (
	"testing"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testOrgOpBatches exercises the OrgOpBatchesStore surface. The bulk of
// the journal logic is covered indirectly via the manager-integration
// suite; the cases here focus on the raw SQL contracts that the
// integration suite doesn't run against real SQL backends.
func (s *Suite) testOrgOpBatches(t *testing.T) {
	// Regression: a fresh org has no rows, no compaction watermark, so
	// the CRDT manager's bootstrap path calls ListAfter with a zero
	// HLC and the full CRDTBatchPageLimit. Earlier the SQLite query
	// mixed positional `?` with sqlc.arg() and sqlc emitted numbered
	// placeholders that left LIMIT unbound; bind_parameter_count came
	// back as 6 but only 5 args were passed, so the driver returned
	// "missing argument with index 6" and the workspace failed to
	// load. The case must run against every backend so a future
	// generator change in any of them surfaces immediately.
	t.Run("list after zero watermark on empty journal", func(t *testing.T) {
		st := s.NewStore(t)
		orgID := SeedOrg(t, st, "opbatch-empty-org", false)

		rows, err := st.OrgOpBatches().ListAfter(ctx, store.ListOrgOpBatchesAfterParams{
			OrgID:             orgID,
			AfterPhysicalMs:   0,
			AfterLogical:      0,
			AfterOriginClient: "",
			Limit:             store.CRDTBatchPageLimit,
		})
		require.NoError(t, err)
		assert.Empty(t, rows)
	})
}
