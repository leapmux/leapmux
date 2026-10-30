package kilo

import (
	"reflect"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The browser writes this envelope from the generated contract table, and a Go
// struct tag cannot hold a constant. Without this test a rename in
// contracts/native-questions.json would move the browser and leave the worker
// decoding the old name: `Answers` stays nil, `reply` never runs, and the daemon's
// question tool blocks for the rest of the session with nothing logged.
func TestKiloAnswerTagsMatchTheContract(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		field string
		want  string
	}{
		{"Answers", contracts.NativeQuestionAnswerFieldAnswers},
		{"Rejected", contracts.NativeQuestionAnswerFieldRejected},
	} {
		typ := reflect.TypeOf(kiloAnswerResult{})
		field, found := typ.FieldByName(tt.field)
		require.True(t, found, "kiloAnswerResult has no field %s", tt.field)
		assert.Equal(t, tt.want, field.Tag.Get("json"), "kiloAnswerResult.%s must carry the contract field name", tt.field)
	}
}
