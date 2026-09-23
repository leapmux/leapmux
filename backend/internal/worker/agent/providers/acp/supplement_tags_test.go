package acp

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/stretchr/testify/assert"
)

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
	supplement := NewToolSupplement(original)
	assert.Len(t, supplement, len(contracts.ACPSupplementIdentityKeys))
	for _, key := range contracts.ACPSupplementIdentityKeys {
		assert.Equal(t, original[key], supplement[key], "identity key %s", key)
	}
	assert.True(t, supplement.IdentityMatches(original))
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
	supplement := NewToolSupplement(frame("tool_call_update", "call-1", "completed"))
	assert.True(t, supplement.IdentityMatches(frame("tool_call_update", "call-1", "completed")))
	assert.False(t, supplement.IdentityMatches(frame("tool_call_update", "call-2", "completed")), "another call")
	assert.False(t, supplement.IdentityMatches(frame("tool_call", "call-1", "completed")), "another update kind")
	assert.False(t, supplement.IdentityMatches(frame("tool_call_update", "call-1", "pending")), "an earlier status")
	assert.False(t, supplement.IdentityMatches(frame("tool_call_update", "call-1", "")), "a frame that states no status")

	noStatus := NewToolSupplement(frame("tool_call", "call-1", ""))
	assert.True(t, noStatus.IdentityMatches(frame("tool_call", "call-1", "")))
	assert.False(t, noStatus.IdentityMatches(frame("tool_call", "call-1", "pending")), "a frame that gained a status")
}
