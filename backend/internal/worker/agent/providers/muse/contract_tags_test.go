package muse

import (
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMuseContractTagsKeepTheInstalledWireSpelling(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		typeInfo reflect.Type
		fields   map[string]string
	}{
		{"frame", reflect.TypeFor[frame](), map[string]string{"JSONRPC": "jsonrpc", "ID": "id", "Method": "method", "Params": "params"}},
		{"position", reflect.TypeFor[nativePosition](), map[string]string{"ID": "id", "Sequence": "sequence"}},
		{"stream", reflect.TypeFor[nativeStream](), map[string]string{"Kind": "kind", "ID": "id"}},
		{"source range", reflect.TypeFor[nativeSourceRange](), map[string]string{"Stream": "stream", "First": "first", "Last": "last"}},
		{"item params", reflect.TypeFor[itemParams](), map[string]string{"SessionID": "sessionId", "ViewCursor": "viewCursor", "SourceRange": "sourceRange", "Item": "item"}},
		{"item", reflect.TypeFor[nativeItem](), map[string]string{"ID": "itemId", "Kind": "kind", "TurnID": "turnId", "Revision": "revision", "Status": "status", "Tool": "tool", "CallID": "callId", "Args": "args", "VisibleOutput": "visibleOutput", "OutputReference": "outputRef", "PatchReference": "patchRef", "ModelVisibleContent": "modelVisibleContent", "Children": "children"}},
		{"control", reflect.TypeFor[controlParams](), map[string]string{"SessionID": "sessionId", "ApprovalID": "approvalId", "UserInputID": "userInputId", "Requirement": "currentRequirementId", "Choices": "availableChoices", "Questions": "questions"}},
		{"turn completion", reflect.TypeFor[turnCompletion](), map[string]string{"SessionID": "sessionId", "TurnID": "turnId", "Outcome": "terminal", "Reason": "reason", "Error": "error"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			for fieldName, wireName := range tc.fields {
				field, exists := tc.typeInfo.FieldByName(fieldName)
				require.True(t, exists, fieldName)
				assert.Equal(t, wireName, strings.Split(field.Tag.Get("json"), ",")[0], fieldName)
			}
		})
	}
}
