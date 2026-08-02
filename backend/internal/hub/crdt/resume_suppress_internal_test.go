package crdt

import (
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
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
