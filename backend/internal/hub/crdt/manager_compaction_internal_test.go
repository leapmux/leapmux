package crdt

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// TestLaggedRetentionWatermark_ZerosLogicalForLagCase pins the invariant that
// the op_retention_watermark carries logical=0 whenever the lag moves
// physical-ms strictly earlier. The delete/resume boundary alignment depends
// on this: DeleteUserOpBatchesThrough keys on the batch's LAST canonical HLC
// (physical_ms, last_logical, origin_client) with a `<=` test, while
// decideResume uses a strict `>` test against the watermark. A floor at
// physical=F with logical=0 means a batch whose last op is at (F, logical>=1)
// survives deletion (last_logical < 0 is false) AND a cursor at that batch
// passes resume (> floor). If a future change carried max_hlc.logical into the
// floor, the journal would delete a batch the resume predicate still admits —
// a silent gap in the client's delta. This test makes the zero-logical
// invariant a checked property rather than a coincidence.
func TestLaggedRetentionWatermark_ZerosLogicalForLagCase(t *testing.T) {
	maxHlc := &leapmuxv1.HLC{Physical: 1_700_000_000_000, Logical: 7, ClientId: "hub-user1"}
	cw := &leapmuxv1.HLC{Physical: 1_700_000_000_000, Logical: 7, ClientId: "hub-user1"}
	// Positive lag: floor lands at an earlier physical-ms.
	rw := laggedRetentionWatermark(maxHlc, cw, time.Hour)
	assert.Equal(t, int64(0), rw.GetLogical(),
		"op_retention_watermark must zero logical when the lag moves physical-ms earlier")
	assert.Equal(t, int64(1_700_000_000_000-3_600_000), rw.GetPhysical(),
		"op_retention_watermark physical must be max_hlc.physical - ttl")
	assert.Equal(t, "hub-user1", rw.GetClientId(),
		"op_retention_watermark must carry max_hlc's client_id")
	assert.Less(t, HLCCmp(rw, cw), 0, "op_retention_watermark must stay ≤ compaction_watermark")

	// Zero TTL collapses to compaction_watermark verbatim (logical preserved) —
	// the floor IS the head, so it must carry the head's logical to make the
	// `> floor` keep-test drop every batch at or below the head.
	rw0 := laggedRetentionWatermark(maxHlc, cw, 0)
	assert.Equal(t, int64(7), rw0.GetLogical(),
		"op_retention_watermark must preserve logical when ttl=0 (floor == head)")
}
