package opencode

import (
	"reflect"
	"strings"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNativeControlAnswerTagsMatchTheContract(t *testing.T) {
	t.Parallel()
	for name, expected := range map[string]string{"Answer": contracts.OpenCodeNativeAnswerFieldAnswer, "Cancel": contracts.OpenCodeNativeAnswerFieldCancel, "Decision": contracts.OpenCodeNativeAnswerFieldDecision} {
		field, found := reflect.TypeFor[openCodeControlAnswer]().FieldByName(name)
		require.True(t, found)
		assert.Equal(t, expected, strings.Split(field.Tag.Get("json"), ",")[0])
	}
}
