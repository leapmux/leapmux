package agent

import (
	"reflect"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The browser writes this envelope from the generated contract table, and a Go
// struct tag cannot hold a constant. Without this test a rename in
// contracts/opencode-protocol.json would move the browser and leave the worker
// decoding the old name: `Answers` stays nil, `reply` never runs, and the daemon's
// question tool blocks for the rest of the session with nothing logged.
func TestOpenCodeAnswerTagsMatchTheContract(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		field string
		want  string
	}{
		{"Answers", contracts.OpenCodeAnswerFieldAnswers},
		{"Rejected", contracts.OpenCodeAnswerFieldRejected},
	} {
		typ := reflect.TypeOf(openCodeAnswerResult{})
		field, found := typ.FieldByName(tt.field)
		require.True(t, found, "openCodeAnswerResult has no field %s", tt.field)
		assert.Equal(t, tt.want, field.Tag.Get("json"), "openCodeAnswerResult.%s must carry the contract field name", tt.field)
	}
}
