package agent

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSupplementTagsMatchTheContract pins the struct tags a HAND-WRITTEN struct still
// carries for a contracted key.
//
// Most supplement structs are generated from their contract now
// (contracts/<domain>.json `structs`), so their tags come from the same table the
// browser reads and there is no literal left to drift. This test holds the ones that
// stay hand-written because they carry fields a generated struct cannot: Codex's
// collab item mixes two contracted keys with three of its own.
//
// Without it a renamed field fails SILENTLY -- the resolve answers the ORIGINAL frame
// when a key it expects is absent, so a row draws with its joined output missing and
// nothing anywhere states that a supplement was dropped.
func TestSupplementTagsMatchTheContract(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name  string
		value any
		field string
		want  string
	}{
		{"codex receiver threads", codexCollabAgentToolCall{}, "ReceiverThreadIds", contracts.CodexCollabItemReceiverThreadIDs},
		{"codex agent states", codexCollabAgentToolCall{}, "AgentsStates", contracts.CodexCollabItemAgentsStates},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			typ := reflect.TypeOf(tt.value)
			field, found := typ.FieldByName(tt.field)
			require.True(t, found, "%s has no field %s", typ.Name(), tt.field)
			assert.Equal(t, tt.want, field.Tag.Get("json"), "%s.%s must carry the contract field name", typ.Name(), tt.field)
		})
	}
}

// The ACP supplement is a map rather than a struct, so its keys have no tags to pin.
// This states the same invariant one level up: the envelope a fresh supplement starts
// with holds exactly the identity keys the contract declares, and no other one.
func TestNewACPToolSupplementCopiesTheContractIdentityKeys(t *testing.T) {
	t.Parallel()
	original := map[string]json.RawMessage{
		contracts.ACPSupplementIdentitySessionUpdate: json.RawMessage(`"tool_call"`),
		contracts.ACPSupplementIdentityToolCallID:    json.RawMessage(`"call-1"`),
		contracts.ACPSupplementIdentityStatus:        json.RawMessage(`"pending"`),
		contracts.ACPSupplementRequestTitle:          json.RawMessage(`"Read"`),
		"content":                                    json.RawMessage(`[]`),
	}
	supplement := newACPToolSupplement(original)
	assert.Len(t, supplement, len(contracts.ACPSupplementIdentityKeys))
	for _, key := range contracts.ACPSupplementIdentityKeys {
		assert.Equal(t, original[key], supplement[key], "identity key %s", key)
	}
	assert.True(t, supplement.identityMatches(original))
}

// A supplement stored beside one frame must never reach another. Each case moves ONE
// identity key, because a row that took another call's output is the failure the
// identity check exists to stop.
func TestACPToolSupplementIdentityRefusesAnotherFrame(t *testing.T) {
	t.Parallel()
	frame := func(update, id, status string) map[string]json.RawMessage {
		fields := map[string]json.RawMessage{
			contracts.ACPSupplementIdentitySessionUpdate: json.RawMessage(`"` + update + `"`),
			contracts.ACPSupplementIdentityToolCallID:    json.RawMessage(`"` + id + `"`),
		}
		if status != "" {
			fields[contracts.ACPSupplementIdentityStatus] = json.RawMessage(`"` + status + `"`)
		}
		return fields
	}
	supplement := newACPToolSupplement(frame("tool_call_update", "call-1", "completed"))
	assert.True(t, supplement.identityMatches(frame("tool_call_update", "call-1", "completed")))
	assert.False(t, supplement.identityMatches(frame("tool_call_update", "call-2", "completed")), "another call")
	assert.False(t, supplement.identityMatches(frame("tool_call", "call-1", "completed")), "another update kind")
	assert.False(t, supplement.identityMatches(frame("tool_call_update", "call-1", "pending")), "an earlier status")
	assert.False(t, supplement.identityMatches(frame("tool_call_update", "call-1", "")), "a frame that states no status")

	noStatus := newACPToolSupplement(frame("tool_call", "call-1", ""))
	assert.True(t, noStatus.identityMatches(frame("tool_call", "call-1", "")))
	assert.False(t, noStatus.identityMatches(frame("tool_call", "call-1", "pending")), "a frame that gained a status")
}
