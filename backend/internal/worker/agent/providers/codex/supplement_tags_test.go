package codex

import (
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
