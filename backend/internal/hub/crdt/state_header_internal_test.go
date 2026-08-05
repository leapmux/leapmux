package crdt

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// TestStateGenerationBuildersCarryEveryUserCrdtStateField is a TRIPWIRE on the
// hand-maintained header field list.
//
// Two of the three generation builders spell the header out by hand:
// nextStateGeneration (via newStateHeader) and CloneStateForBatch. Only
// CloneState uses proto.Clone and so tracks new fields automatically. A field
// added to UserCrdtState in the proto is therefore silently DROPPED on every
// epoch bump and every commit unless someone remembers to extend newStateHeader
// — and the loss is invisible: maybeCompact deep-clones the truncated
// generation and persists it, so the field is gone from disk with every test
// still green.
//
// Driving the check off the DESCRIPTOR rather than a restated field list is the
// whole point: a list here would need the same maintenance it exists to police.
// Same idea as validate_tab_tripwire_internal_test.go and journal_shape_test.go.
func TestStateGenerationBuildersCarryEveryUserCrdtStateField(t *testing.T) {
	src := &leapmuxv1.UserCrdtState{}
	populateEveryField(t, src.ProtoReflect(), 2)

	// Anti-vacuity guard. If populateEveryField ever fails to set a field, the
	// round-trip assertions below would pass on a field neither side carries —
	// the tripwire would be green precisely when it stopped working.
	fields := src.ProtoReflect().Descriptor().Fields()
	for i := range fields.Len() {
		f := fields.Get(i)
		require.True(t, src.ProtoReflect().Has(f),
			"fixture: populateEveryField left %s unset, so this test would not notice it being dropped", f.Name())
	}

	assert.True(t, proto.Equal(src, nextStateGeneration(src)),
		"nextStateGeneration dropped a UserCrdtState field; add it to newStateHeader")
	assert.True(t, proto.Equal(src, CloneStateForBatch(src, nil)),
		"CloneStateForBatch dropped a UserCrdtState field; add it to newStateHeader")
}

// populateEveryField sets every field of `m` to a non-zero value, recursing into
// message fields until `depth` is exhausted.
//
// It fails the test on a field kind it does not know how to set, so the day
// UserCrdtState grows one this test says so instead of quietly skipping it —
// which would reopen exactly the hole it exists to close.
func populateEveryField(t *testing.T, m protoreflect.Message, depth int) {
	t.Helper()
	fields := m.Descriptor().Fields()
	for i := range fields.Len() {
		f := fields.Get(i)
		switch {
		case f.IsMap():
			mp := m.Mutable(f).Map()
			key := protoreflect.ValueOfString("k").MapKey()
			val := mp.NewValue()
			if f.MapValue().Kind() == protoreflect.MessageKind && depth > 0 {
				populateEveryField(t, val.Message(), depth-1)
			}
			mp.Set(key, val)
		case f.IsList():
			lst := m.Mutable(f).List()
			lst.Append(scalarValue(t, f, lst.NewElement()))
		case f.Kind() == protoreflect.MessageKind:
			// Mutable() establishes presence even at depth 0, so a nested
			// message still counts as SET for the anti-vacuity guard.
			msg := m.Mutable(f).Message()
			if depth > 0 {
				populateEveryField(t, msg, depth-1)
			}
		default:
			m.Set(f, scalarValue(t, f, m.NewField(f)))
		}
	}
}

// scalarValue returns a non-zero value for `f`. `zero` is the field's own
// freshly-created value, used only for kinds that need one (enums, messages).
func scalarValue(t *testing.T, f protoreflect.FieldDescriptor, zero protoreflect.Value) protoreflect.Value {
	t.Helper()
	switch f.Kind() {
	case protoreflect.StringKind:
		return protoreflect.ValueOfString("x")
	case protoreflect.BoolKind:
		return protoreflect.ValueOfBool(true)
	case protoreflect.BytesKind:
		return protoreflect.ValueOfBytes([]byte{1})
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		return protoreflect.ValueOfInt32(7)
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		return protoreflect.ValueOfUint32(7)
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		return protoreflect.ValueOfInt64(7)
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		return protoreflect.ValueOfUint64(7)
	case protoreflect.FloatKind:
		return protoreflect.ValueOfFloat32(1.5)
	case protoreflect.DoubleKind:
		return protoreflect.ValueOfFloat64(1.5)
	case protoreflect.EnumKind:
		return protoreflect.ValueOfEnum(1)
	case protoreflect.MessageKind, protoreflect.GroupKind:
		return zero
	default:
		t.Fatalf("populateEveryField: unhandled kind %v for field %s -- extend it rather than skipping, "+
			"or this tripwire silently stops covering that field", f.Kind(), f.Name())
		return zero
	}
}
