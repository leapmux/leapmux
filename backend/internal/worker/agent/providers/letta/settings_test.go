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
	"github.com/leapmux/leapmux/internal/worker/bgtask"
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
	requestID, ok := command["request_id"].(string)
	require.True(t, ok)
	require.True(t, a.rpcReplies.IsPendingForTest(requestID))
	a.ws.close()
	ctx := testutil.DeadlineContext(t)
	select {
	case <-fake.readDone:
	case <-ctx.Done():
		t.Fatal("the native reader did not stop")
	}
	applied := waitModeResult(t, result)
	assert.False(t, applied.AppliedLive)
	assert.Empty(t, applied.ConfirmedOptions())
	assert.False(t, a.rpcReplies.IsPendingForTest(requestID))
	assert.Equal(t, "initial-model", a.SettingsSnapshot().ConfirmedOptions()[agent.OptionIDModel])
}

func TestModeSyncReplyCannotConfirmAnotherRuntime(t *testing.T) {
	t.Parallel()
	fake, a := openedAgent(t)
	a.settings.permissionMode = contracts.LettaModeStandard
	result, snapshot := beginNativeMode(t, fake, a, contracts.LettaModeUnrestricted)
	fake.replies <- []byte(fmt.Sprintf(`{"type":"sync_response","request_id":%q,"runtime":{"agent_id":"foreign","conversation_id":"foreign"},"success":true}`, snapshot["request_id"]))
	assert.Equal(t, contracts.LettaModeStandard, waitModeResult(t, result).ConfirmedOptions()[agent.OptionIDPermissionMode])
}

func TestLettaDeviceStatusTracksBackgroundProcesses(t *testing.T) {
	t.Parallel()
	a, sink := newLettaChildTestAgent(t)
	currentRuntime := map[string]any{"agent_id": "agent-local-1", "conversation_id": "local-conv-1"}

	// Frame with no current_permission_mode still tracks background_processes.
	openFrame, err := json.Marshal(map[string]any{
		"type":    "update_device_status",
		"runtime": currentRuntime,
		"device_status": map[string]any{
			"background_processes": []any{
				map[string]any{"process_id": "bash-1", "kind": "bash", "command": "npm run dev", "status": "running"},
				map[string]any{"process_id": "mon-1", "kind": "monitor", "description": "Watch build", "status": "running"},
			},
		},
	})
	require.NoError(t, err)
	a.HandleOutput(openFrame)

	tasks := sink.BackgroundTasks()
	require.Len(t, tasks, 2)
	byTitle := map[string]bgtask.Item{}
	for _, task := range tasks {
		byTitle[task.Title] = task
	}
	bashRow, ok := byTitle["npm run dev"]
	require.True(t, ok)
	assert.Equal(t, bgtask.KindShell, bashRow.Kind)
	assert.True(t, bashRow.TitleIsCommand)
	assert.Equal(t, bgtask.StatusRunning, bashRow.Status)

	monRow, ok := byTitle["Watch build"]
	require.True(t, ok)
	assert.Equal(t, bgtask.KindShell, monRow.Kind)
	assert.False(t, monRow.TitleIsCommand)
	assert.Equal(t, bgtask.StatusRunning, monRow.Status)

	// Omitted and null background_processes must NOT evict tracked shells.
	a.HandleOutput([]byte(lettaLiveDeviceStatus))
	nullFrame, err := json.Marshal(map[string]any{
		"type":          "update_device_status",
		"runtime":       currentRuntime,
		"device_status": map[string]any{"current_permission_mode": "standard", "background_processes": nil},
	})
	require.NoError(t, err)
	a.HandleOutput(nullFrame)

	// Foreign runtime empty array must NOT evict tracked shells.
	foreignFrame, err := json.Marshal(map[string]any{
		"type":          "update_device_status",
		"runtime":       map[string]any{"agent_id": "agent-other", "conversation_id": "local-conv-1"},
		"device_status": map[string]any{"background_processes": []any{}},
	})
	require.NoError(t, err)
	a.HandleOutput(foreignFrame)

	row, ok := sink.BackgroundTask(bashRow.RowKey)
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusRunning, row.Status)
	row, ok = sink.BackgroundTask(monRow.RowKey)
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusRunning, row.Status)

	// Explicit finished status closes bash-1 as succeeded, and omitting mon-1 closes it with unknown outcome.
	closeFrame, err := json.Marshal(map[string]any{
		"type":    "update_device_status",
		"runtime": currentRuntime,
		"device_status": map[string]any{
			"background_processes": []any{
				map[string]any{"process_id": "bash-1", "kind": "bash", "command": "npm run dev", "status": "completed", "exit_code": 0},
			},
		},
	})
	require.NoError(t, err)
	a.HandleOutput(closeFrame)

	row, ok = sink.BackgroundTask(bashRow.RowKey)
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusSucceeded, row.Status)
	row, ok = sink.BackgroundTask(monRow.RowKey)
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusEndedWithUnknownOutcome, row.Status)
}
