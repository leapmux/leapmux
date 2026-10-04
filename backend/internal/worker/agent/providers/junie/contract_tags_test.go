package junie

import (
	"reflect"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// JSON tags cannot use generated constants. Pin every output file path tag to its contract.
func TestJunieOutputFilePathTagsMatchTheContract(t *testing.T) {
	t.Parallel()
	for _, group := range []struct {
		typeOf reflect.Type
		fields map[string]string
	}{
		{typeOf: reflect.TypeFor[junieOutputFilePath](), fields: map[string]string{
			"SessionID": contracts.JunieOutputFilePathFieldSessionID, "CallID": contracts.JunieOutputFilePathFieldToolCallID, "TaskID": contracts.JunieOutputFilePathFieldTaskID,
			"Command": contracts.JunieOutputFilePathFieldCommand, "WorkingDirectory": contracts.JunieOutputFilePathFieldWorkingDirectory, "Path": contracts.JunieOutputFilePathFieldPath,
			"ExitCode": contracts.JunieOutputFilePathFieldExitCode,
		}},
		{typeOf: reflect.TypeFor[junieTerminalExit](), fields: map[string]string{
			"CallID": contracts.JunieTerminalMetaTerminalID, "ExitCode": contracts.JunieTerminalMetaExitCode, "Signal": contracts.JunieTerminalMetaSignal,
		}},
	} {
		for field, tag := range group.fields {
			member, exists := group.typeOf.FieldByName(field)
			require.True(t, exists, field)
			assert.Equal(t, tag, member.Tag.Get("json"), field)
		}
	}
}
