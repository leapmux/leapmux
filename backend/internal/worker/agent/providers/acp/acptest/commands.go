package acptest

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// AdvertiseCommands delivers to agent an available_commands_update that lists
// commands, as the runtime sends it, through the agent's own output handler. The
// frame states no session, which an agent with no session accepts.
func AdvertiseCommands(t *testing.T, agent interface{ HandleOutput([]byte) }, commands ...string) {
	t.Helper()
	values := make([]map[string]string, len(commands))
	for i, command := range commands {
		values[i] = map[string]string{"name": command}
	}
	frame, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  "session/update",
		"params": map[string]any{
			"update": map[string]any{
				"sessionUpdate":     "available_commands_update",
				"availableCommands": values,
			},
		},
	})
	require.NoError(t, err)
	agent.HandleOutput(frame)
}
