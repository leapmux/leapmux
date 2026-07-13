package crdt

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// denyForeignWorkerChecker allows everything except one worker id, so a test can
// prove validateWorkerRefs rejected an op PURELY because it named that id (and
// not because some other rule fired first).
type denyForeignWorkerChecker struct{ denied string }

func (c denyForeignWorkerChecker) CanAccessWorkspace(context.Context, string, string, string) (bool, error) {
	return true, nil
}

func (c denyForeignWorkerChecker) CanUseWorker(_ context.Context, _, workerID, _ string) (bool, error) {
	return workerID != c.denied, nil
}

// TestValidateWorkerRefs_GatesEveryWorkerIdField is the tripwire for the
// "every worker_id-bearing CRDT op field is gated by validateWorkerRefs" invariant.
// validateWorkerRefs today hard-codes a type-switch on *OrgOp_SetTabRegister +
// *SetTabRegisterOp_WorkerId because that is the only worker_id reference the
// protocol carries. A future CRDT op that introduces ANOTHER worker_id reference
// (a tab rebind, a presence pin, ...) must extend that switch -- without it, a
// client could name a worker it may not reach and produce state pointing at one.
// This test enumerates every worker_id field across the OrgOp body messages via
// protoreflect (the proto is the source of truth) and asserts validateWorkerRefs
// rejects each one when the principal cannot use the named worker. Adding a new
// worker_id field without extending the switch reddens THIS test, pointing the
// author at exactly the gate they missed.
//
// Scope: the scan is one level deep -- the worker_id field directly on an OrgOp
// body message (the SetTabRegisterOp.worker_id shape). A worker_id nested inside
// a sub-message of an op would not be caught, but CRDT ops carry their worker
// reference as a direct register write, so that shape is the one that matters.
func TestValidateWorkerRefs_GatesEveryWorkerIdField(t *testing.T) {
	orgOpDesc := (&leapmuxv1.OrgOp{}).ProtoReflect().Descriptor()
	bodyOneof := orgOpDesc.Oneofs().ByName("body")
	require.NotNil(t, bodyOneof, "OrgOp must have a `body` oneof")

	type workerIDSite struct {
		armName   protoreflect.Name
		msgName   string
		fieldName protoreflect.Name
	}
	var sites []workerIDSite
	for i := 0; i < bodyOneof.Fields().Len(); i++ {
		armField := bodyOneof.Fields().Get(i)
		innerMsg := armField.Message()
		// Fields() lists both direct fields and oneof members, so this finds
		// worker_id whether it is a plain field or a oneof register (the
		// SetTabRegisterOp.field.worker_id shape).
		for j := 0; j < innerMsg.Fields().Len(); j++ {
			f := innerMsg.Fields().Get(j)
			if f.TextName() == "worker_id" {
				sites = append(sites, workerIDSite{armField.Name(), string(innerMsg.FullName()), f.Name()})
			}
		}
	}
	require.NotEmpty(t, sites,
		"expected at least one worker_id field across OrgOp body messages (SetTabRegisterOp.worker_id)")

	for _, s := range sites {
		t.Run(string(s.msgName)+"."+string(s.fieldName), func(t *testing.T) {
			op := buildOpWithWorkerID(t, s.armName, s.fieldName, "foreign-worker")
			reason, _, err := validateWorkerRefs(
				context.Background(),
				[]*leapmuxv1.OrgOp{op},
				"principal", "org",
				denyForeignWorkerChecker{denied: "foreign-worker"},
			)
			require.NoError(t, err, "a deny result must not surface as a transient lookup error")
			assert.Equal(t, leapmuxv1.BatchRejectionReason_BATCH_REJECTION_INVALID_WORKER_REF, reason,
				"validateWorkerRefs must gate %s.%s: a worker_id the principal cannot use must be rejected. "+
					"If this is failing because you ADDED a worker_id field, extend validateWorkerRefs's switch to cover it.",
				s.msgName, s.fieldName)
		})
	}
}

// buildOpWithWorkerID constructs an OrgOp whose body is the `armName` oneof case,
// with that arm's inner message's `fieldName` (a worker_id field) set to workerID.
// Built from the proto descriptor via dynamicpb (so it works for worker_id fields
// that do not yet have a Go typed accessor), then marshaled across the
// dynamic/typed boundary into a real *leapmuxv1.OrgOp that validateWorkerRefs
// consumes.
func buildOpWithWorkerID(t *testing.T, armName, fieldName protoreflect.Name, workerID string) *leapmuxv1.OrgOp {
	t.Helper()
	orgOpDesc := (&leapmuxv1.OrgOp{}).ProtoReflect().Descriptor()
	armField := orgOpDesc.Oneofs().ByName("body").Fields().ByName(armName)
	require.NotNil(t, armField, "OrgOp.body has no oneof case %q", armName)

	inner := dynamicpb.NewMessage(armField.Message())
	workerField := inner.Descriptor().Fields().ByName(fieldName)
	require.NotNil(t, workerField, "%s has no field %q", armField.Message().FullName(), fieldName)
	// Set on a oneof-member field clears the competing case and selects this one.
	inner.Set(workerField, protoreflect.ValueOfString(workerID))

	dynOp := dynamicpb.NewMessage(orgOpDesc)
	dynOp.Set(armField, protoreflect.ValueOfMessage(inner))
	dynOp.Set(orgOpDesc.Fields().ByName("op_id"), protoreflect.ValueOfString("op-"+string(armName)))

	data, err := proto.Marshal(dynOp)
	require.NoError(t, err)
	typed := &leapmuxv1.OrgOp{}
	require.NoError(t, proto.Unmarshal(data, typed))
	return typed
}

// TestValidateWorkerRefs_AllowsAccessibleWorker pins the other half of the gate
// for the field the tripwire enumerates: a worker_id the principal CAN use is
// accepted, so the tripwire's rejection is specifically about the access check
// and not a blanket reject-all.
func TestValidateWorkerRefs_AllowsAccessibleWorker(t *testing.T) {
	op := &leapmuxv1.OrgOp{
		OpId: "op-allow",
		Body: &leapmuxv1.OrgOp_SetTabRegister{
			SetTabRegister: &leapmuxv1.SetTabRegisterOp{
				Field: &leapmuxv1.SetTabRegisterOp_WorkerId{WorkerId: "own-worker"},
			},
		},
	}
	reason, _, err := validateWorkerRefs(
		context.Background(), []*leapmuxv1.OrgOp{op}, "principal", "org",
		denyForeignWorkerChecker{denied: "foreign-worker"},
	)
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.BatchRejectionReason_BATCH_REJECTION_UNSPECIFIED, reason,
		"a worker_id the principal may use must pass the gate")
}
