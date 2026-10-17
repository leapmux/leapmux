package codex

import (
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The `initialize` capabilities decide which frames Codex ever writes. This
// pins the exact opt-out list: a method removed from it starts writing raw rows
// again, and a method added to it silently loses a frame the transcript reads.
func TestCodexInitializeParamsCarryTheOptOutList(t *testing.T) {
	t.Parallel()

	params := codexInitializeParams()
	require.Equal(t, "leapmux", params["clientInfo"].(map[string]string)["name"])

	capabilities, ok := params["capabilities"].(map[string]interface{})
	require.True(t, ok, "initialize must state its capabilities")
	assert.Equal(t, true, capabilities["experimentalApi"],
		"the app-server notification API stays experimental and must be opted into")

	optedOut, ok := capabilities["optOutNotificationMethods"].([]string)
	require.True(t, ok, "initialize must carry the opt-out list")
	assert.Equal(t, []string{
		"thread/reverted",
		"turn/diff/updated",
		contracts.CodexMethodTurnModerationMetadata,
		contracts.CodexMethodAccountUpdated,
		"account/gatewayOAuth/changed",
		contracts.CodexMethodThreadStarted,
		contracts.CodexMethodThreadStatusChanged,
		"thread/readState/changed",
		"thread/queue/changed",
		"thread/environment/connected",
		"thread/environment/disconnected",
		contracts.CodexMethodSkillsChanged,
		contracts.CodexMethodRemoteControlStatusChanged,
		contracts.CodexMethodHookStarted,
		"rawResponse/completed",
		"item/fileChange/patchUpdated",
		"model/rerouted",
	}, optedOut)
}
