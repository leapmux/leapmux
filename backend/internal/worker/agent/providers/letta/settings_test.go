package letta

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

func beginNativeMode(t *testing.T, fake *fakeAppServer, a *Agent, mode string) (<-chan agent.SettingsApplyResult, map[string]any) {
	t.Helper()
	result := make(chan agent.SettingsApplyResult, 1)
	go func() { result <- a.UpdateSettings(map[string]string{agent.OptionIDPermissionMode: mode}) }()
	change := fake.nextCommand(t)
	require.Equal(t, "change_device_state", change["type"])
	require.Equal(t, map[string]any{"mode": mode}, change["payload"])
	snapshot := fake.nextCommand(t)
	require.Equal(t, "sync", snapshot["type"])
	require.Equal(t, false, snapshot["recover_approvals"])
	require.Equal(t, false, snapshot["resume_interrupted_turn"])
	require.Equal(t, true, snapshot["force_device_status"])
	return result, snapshot
}

func returnModeStatus(t *testing.T, fake *fakeAppServer, scope any, mode string) {
	t.Helper()
	data, err := json.Marshal(map[string]any{"type": "update_device_status", "runtime": scope, "device_status": map[string]string{"current_permission_mode": mode}})
	require.NoError(t, err)
	fake.replies <- data
}

func returnSettingsSync(t *testing.T, fake *fakeAppServer, snapshot map[string]any) {
	t.Helper()
	data, err := json.Marshal(map[string]any{"type": "sync_response", "request_id": snapshot["request_id"], "runtime": snapshot["runtime"], "success": true})
	require.NoError(t, err)
	fake.replies <- data
}

func waitModeResult(t *testing.T, result <-chan agent.SettingsApplyResult) agent.SettingsApplyResult {
	t.Helper()
	ctx := testutil.DeadlineContext(t)
	select {
	case value := <-result:
		return value
	case <-ctx.Done():
		t.Fatal("the native mode update did not finish")
		return agent.SettingsApplyResult{}
	}
}

func applyNativeMode(t *testing.T, fake *fakeAppServer, a *Agent, mode string) agent.SettingsApplyResult {
	t.Helper()
	result, snapshot := beginNativeMode(t, fake, a, mode)
	returnModeStatus(t, fake, snapshot["runtime"], mode)
	returnSettingsSync(t, fake, snapshot)
	return waitModeResult(t, result)
}

func TestPermissionModeRequiresTheNativeSnapshotOfItsRuntime(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"matching", "refused", "missing", "foreign"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			fake, a := openedAgent(t)
			a.settings.permissionMode = contracts.LettaModeStandard
			result, snapshot := beginNativeMode(t, fake, a, contracts.LettaModeUnrestricted)
			want := contracts.LettaModeStandard
			switch scenario {
			case "matching":
				want = contracts.LettaModeUnrestricted
				returnModeStatus(t, fake, snapshot["runtime"], want)
			case "refused":
				returnModeStatus(t, fake, snapshot["runtime"], want)
			case "foreign":
				returnModeStatus(t, fake, runtimeScope{AgentID: "foreign", ConversationID: "foreign"}, contracts.LettaModeUnrestricted)
			}
			returnSettingsSync(t, fake, snapshot)
			assert.Equal(t, want, waitModeResult(t, result).ConfirmedOptions()[agent.OptionIDPermissionMode])
		})
	}
}

func TestClosedSocketSettlesThePendingSettingsWaiter(t *testing.T) {
	t.Parallel()
	fake, a := openedAgent(t)
	a.settings.model = "initial-model"
	result := make(chan agent.SettingsApplyResult, 1)
	go func() { result <- a.UpdateSettings(map[string]string{agent.OptionIDModel: "new-model"}) }()
	command := fake.nextCommand(t)
	require.Equal(t, "update_model", command["type"])
	a.Mu.Lock()
	pending := a.modelPending
	a.Mu.Unlock()
	require.NotNil(t, pending)
	a.ws.close()
	ctx := testutil.DeadlineContext(t)
	select {
	case <-fake.readDone:
	case <-ctx.Done():
		t.Fatal("the native reader did not stop")
	}
	select {
	case <-pending.waiter.done:
	default:
		t.Fatal("the settings waiter survived its native reader")
	}
	assert.Equal(t, "initial-model", waitModeResult(t, result).ConfirmedOptions()[agent.OptionIDModel])
}

func TestModeSyncReplyCannotConfirmAnotherRuntime(t *testing.T) {
	t.Parallel()
	fake, a := openedAgent(t)
	a.settings.permissionMode = contracts.LettaModeStandard
	result, snapshot := beginNativeMode(t, fake, a, contracts.LettaModeUnrestricted)
	fake.replies <- []byte(fmt.Sprintf(`{"type":"sync_response","request_id":%q,"runtime":{"agent_id":"foreign","conversation_id":"foreign"},"success":true}`, snapshot["request_id"]))
	assert.Equal(t, contracts.LettaModeStandard, waitModeResult(t, result).ConfirmedOptions()[agent.OptionIDPermissionMode])
}
