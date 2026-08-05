package crdt

import (
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// TestSendTo_ResumeSuppressThrough pins the live-path half of the
// register-time until/broadcast dual-delivery fix: when a subscriber's
// resumeSuppressThrough is at or above the batch's last-op HLC, sendTo must
// not call Send. Those frames are owned by the ResumeDelta journal scan.
func TestSendTo_ResumeSuppressThrough(t *testing.T) {
	at := &leapmuxv1.HLC{Physical: 100, Logical: 0, ClientId: "c"}
	batch := &leapmuxv1.OpBatch{
		BatchId: "b1",
		Ops: []*leapmuxv1.CrdtOp{{
			OpId:         "op1",
			CanonicalHlc: at,
			Body: &leapmuxv1.CrdtOp_SetNodeRegister{SetNodeRegister: &leapmuxv1.SetNodeRegisterOp{
				NodeId: "n1",
				Field:  &leapmuxv1.SetNodeRegisterOp_Position{Position: "p"},
			}},
		}},
	}
	ref := OpTarget(batch.GetOps()[0])
	res := ValidationResult{
		AffectedEntities: map[EntityRef]EntityWorkspaceTransition{
			ref: {Pre: "w1", Post: "w1"},
		},
	}

	var sends atomic.Int32
	sub := &Subscriber{
		Filter:                NewSubscriberFilter(map[string]bool{"w1": true}),
		resumeSuppressThrough: at,
		Send: func(*MarshaledEvent) error {
			sends.Add(1)
			return nil
		},
	}
	fan := &batchFanout{
		batch: batch,
		res:   res,
		refs:  orderedAffectedRefs(res.AffectedEntities),
		atHLC: at,
		materialized: func(EntityRef) *MarshaledEvent {
			return nil
		},
		removed: func(EntityRef) *MarshaledEvent {
			return nil
		},
	}
	fan.sendTo(sub)
	assert.Equal(t, int32(0), sends.Load(), "batches at or below resumeSuppressThrough must not be live-sent")

	// A strictly newer batch must still deliver. batchFanout.atHLC is the
	// subscriber-constant source of truth broadcastBatch hoists once, so the
	// test mirrors that contract and updates fan.atHLC alongside the batch.
	newer := &leapmuxv1.HLC{Physical: 101, Logical: 0, ClientId: "c"}
	batch.Ops[0].CanonicalHlc = newer
	fan.atHLC = newer
	fan.sendTo(sub)
	require.Greater(t, sends.Load(), int32(0), "batches above resumeSuppressThrough must still live-send")
}

// TestRegisterForFallback_SuppressGateIsTheBaselineGeneration is the FALLBACK
// arm's no-duplicate invariant, reduced to ONE comparison.
//
// The gate and the baseline are read from the same captured generation under
// one m.mu.RLock, so "every batch at or below the baseline is suppressed on the
// live path, and every batch above it is delivered" holds by construction --
// provided these two values are literally the same HLC. A future change that
// re-read live state for either one would open the straddle again, silently,
// and this is what catches it.
func TestRegisterForFallback_SuppressGateIsTheBaselineGeneration(t *testing.T) {
	m := NewManager(userid.MustNew("user-1"), nil, nil, nil, nil)
	m.state = NewState(m.owner.String())
	m.state.MaxHlc = &leapmuxv1.HLC{Physical: 77, Logical: 3, ClientId: "hub"}

	sub := &Subscriber{UserID: m.owner.String(), Send: func(*MarshaledEvent) error { return nil }}
	reg := m.registerForFallback(sub, fallbackCold)
	t.Cleanup(reg.unsub)

	require.NotNil(t, sub.resumeSuppressThrough)
	assert.Equal(t, 0, HLCCmp(sub.resumeSuppressThrough, reg.state.GetMaxHlc()),
		"the suppress gate must be the captured generation's max_hlc")
	assert.Equal(t, 0, HLCCmp(sub.resumeSuppressThrough, materializedFromState(reg.state, reg.filter).GetMaxHlc()),
		"...which is the same value the baseline advertises")
	assert.NotSame(t, sub.resumeSuppressThrough, reg.state.GetMaxHlc(),
		"and a CLONE, so a later commit mutating the field in place could not move the gate")
}

// TestRegisterForResume_SuppressGateIsTheCapturedGeneration is the RESUME arm's
// half of the same invariant, and the reason the two register windows are one
// function.
//
// The scan's `until` high-water and the live path's suppress gate must be the
// SAME point in time: a tail bounded above `until` while the gate sat below it
// would ship a batch twice, and the reverse would drop one. Two hand-mirrored
// windows made that a coincidence of two independent reads of m.state; sharing
// registerLocked makes it one field, which is what this asserts.
func TestRegisterForResume_SuppressGateIsTheCapturedGeneration(t *testing.T) {
	m := NewManager(userid.MustNew("user-1"), nil, nil, nil, nil)
	m.state = NewState(m.owner.String())
	m.state.MaxHlc = &leapmuxv1.HLC{Physical: 77, Logical: 3, ClientId: "hub"}

	sub := &Subscriber{UserID: m.owner.String(), Send: func(*MarshaledEvent) error { return nil }}
	reg := m.registerForResume(sub)
	t.Cleanup(reg.unsub)

	assert.Same(t, reg.maxHLC, sub.resumeSuppressThrough,
		"the scan's `until` and the live-path suppress gate must be ONE value, not two reads")
	assert.Equal(t, 0, HLCCmp(reg.maxHLC, reg.state.GetMaxHlc()),
		"...read from the generation captured under the same RLock")
	assert.NotSame(t, reg.maxHLC, reg.state.GetMaxHlc(),
		"and a CLONE, so a later commit mutating the field in place could not move the gate")
	require.NotNil(t, reg.unsub,
		"the register window owns the ONE unsub handle for this registration; a second makeUnsub "+
			"would be a fresh sync.Once, free to decrement the presence refcount again")
}
