package deepseekharness

import (
	"reflect"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Go JSON tags cannot hold generated constants. Keep the native decoder tags in the contract.
func TestSessionEventTagsMatchTheContract(t *testing.T) {
	t.Parallel()
	event := reflect.TypeFor[sessionEvent]()
	for _, tc := range []struct {
		field string
		want  string
	}{
		{field: "Type", want: contracts.DeepseekHarnessFieldType},
		{field: "Data", want: contracts.DeepseekHarnessFieldData},
	} {
		member, present := event.FieldByName(tc.field)
		require.True(t, present, tc.field)
		assert.Equal(t, tc.want, member.Tag.Get("json"), tc.field)
	}
	data := reflect.TypeFor[assistantMessageEventData]()
	message, present := data.FieldByName("Message")
	require.True(t, present)
	assert.Equal(t, contracts.DeepseekHarnessFieldMessage, message.Tag.Get("json"))
	content, present := message.Type.FieldByName("Content")
	require.True(t, present)
	assert.Equal(t, contracts.DeepseekHarnessFieldContent, content.Tag.Get("json"))
}
