package junie

import (
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
)

// TestStructTagsMatchTheContract pins the JSON tags of the hand-written struct
// that reads a `spawn_subagent` raw input to contracts/junie-protocol.json.
//
// The browser reads the same keys from the generated constants. Without this pin
// a renamed tag fails SILENTLY: the subagent mapping decodes into empty fields
// and a spawn call loses its registry row, and nothing states that a key went
// unread.
func TestStructTagsMatchTheContract(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name  string
		value any
		field string
		want  string
	}{
		{"spawn agent", junieSpawnInput{}, "Agent", contracts.JunieSpawnFieldAgent},
		{"spawn handle", junieSpawnInput{}, "Handle", contracts.JunieSpawnFieldHandle},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			structField, ok := reflect.TypeOf(tt.value).FieldByName(tt.field)
			require.Truef(t, ok, "%s has no field %s", reflect.TypeOf(tt.value), tt.field)
			tag := structField.Tag.Get("json")
			name, _, _ := strings.Cut(tag, ",")
			assert.Equal(t, tt.want, name)
		})
	}
}
