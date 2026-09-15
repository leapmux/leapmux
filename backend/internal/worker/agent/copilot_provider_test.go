package agent

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/todoevents"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// copilotStoredFrame builds one persisted native frame.
func copilotStoredFrame(t *testing.T, eventType string, data any) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "method": "session.event",
		"params": map[string]any{
			"sessionId": "session-1",
			"event":     map[string]any{"id": "event-1", "type": eventType, "data": data},
		},
	})
	require.NoError(t, err)
	return raw
}

func copilotPlugin(t *testing.T) Provider {
	t.Helper()
	plugin := ProviderFor(leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT)
	require.IsType(t, copilotProvider{}, plugin)
	return plugin
}

func TestCopilotExtractTodoEventReadsTheChecklist(t *testing.T) {
	t.Parallel()
	checklist := "- [x] Read the code\n- [ ] Write the test\n  * [ ] Nested item\n\nnot a list item\n"
	event, ok := copilotPlugin(t).ExtractTodoEvent("", copilotStoredFrame(t, contracts.CopilotEventToolStarted, map[string]any{
		"toolCallId": "tool-1", "toolName": contracts.CopilotToolUpdateTodo,
		"arguments": map[string]any{"todos": checklist},
	}), nil)
	require.True(t, ok)
	assert.Equal(t, todoevents.KindSnapshot, event.Kind)
	require.Len(t, event.Snapshot, 3)
	assert.Equal(t, "Read the code", event.Snapshot[0].Content)
	assert.Equal(t, todoevents.StatusCompleted, event.Snapshot[0].Status)
	assert.Equal(t, "Write the test", event.Snapshot[1].Content)
	assert.Equal(t, todoevents.StatusPending, event.Snapshot[1].Status)
	assert.Equal(t, "Nested item", event.Snapshot[2].Content)
}

// An empty checklist is a real value: it says the list is now empty, which is what
// clears the sidebar. It must not be read as "this message changes nothing".
func TestCopilotExtractTodoEventAcceptsAnEmptyChecklist(t *testing.T) {
	t.Parallel()
	event, ok := copilotPlugin(t).ExtractTodoEvent("", copilotStoredFrame(t, contracts.CopilotEventToolStarted, map[string]any{
		"toolCallId": "tool-1", "toolName": contracts.CopilotToolUpdateTodo,
		"arguments": map[string]any{"todos": ""},
	}), nil)
	require.True(t, ok)
	assert.Empty(t, event.Snapshot)
}

// A marker outside the task-list syntax keeps its item pending. Claiming a state the
// runtime never stated would show work as finished that is not.
func TestCopilotExtractTodoEventKeepsAnUnreadMarkerPending(t *testing.T) {
	t.Parallel()
	event, ok := copilotPlugin(t).ExtractTodoEvent("", copilotStoredFrame(t, contracts.CopilotEventToolStarted, map[string]any{
		"toolCallId": "tool-1", "toolName": contracts.CopilotToolUpdateTodo,
		"arguments": map[string]any{"todos": "- [~] Half done\n- [X] Finished\n- [] Malformed\n- Plain line"},
	}), nil)
	require.True(t, ok)
	require.Len(t, event.Snapshot, 2)
	assert.Equal(t, "Half done", event.Snapshot[0].Content)
	assert.Equal(t, todoevents.StatusPending, event.Snapshot[0].Status)
	assert.Equal(t, "Finished", event.Snapshot[1].Content)
	assert.Equal(t, todoevents.StatusCompleted, event.Snapshot[1].Status)
}

func TestCopilotExtractTodoEventIgnoresEveryOtherMessage(t *testing.T) {
	t.Parallel()
	plugin := copilotPlugin(t)
	for name, content := range map[string][]byte{
		"another tool": copilotStoredFrame(t, contracts.CopilotEventToolStarted, map[string]any{
			"toolCallId": "tool-1", "toolName": contracts.CopilotToolView, "arguments": map[string]any{"path": "/x"},
		}),
		"another event": copilotStoredFrame(t, contracts.CopilotEventAssistantMessage, map[string]any{
			"messageId": "message-1", "content": "no list here",
		}),
		// A frame that states the event name at the top level but is not a session
		// event: the method check rejects it, which the byte search cannot.
		"another method": []byte(`{"jsonrpc":"2.0","method":"session.other","params":{"sessionId":"session-1",` +
			`"event":{"type":"tool.execution_start","data":{"toolName":"update_todo","arguments":{"todos":"- [ ] x"}}}}}`),
		"not a frame": []byte(`{"content":"plain user message"}`),
		"not json":    []byte(`tool.execution_start update_todo`),
	} {
		_, ok := plugin.ExtractTodoEvent("", content, nil)
		assert.False(t, ok, name)
	}
}

// The permission-mode axis carries manual, assisted and allow-all, and the runtime
// rejects every other word -- including the `default` its slash command accepts. A
// mode pushed after an approved plan would travel down that axis.
func TestCopilotPlanModePermissionModeStatesNoMode(t *testing.T) {
	t.Parallel()
	plugin := copilotPlugin(t)
	for _, kind := range []PlanModeControlKind{
		PlanModeControlNone, PlanModeControlEnter, PlanModeControlExit, PlanModeControlPrompt,
	} {
		assert.Empty(t, plugin.PlanModePermissionMode(kind), kind)
	}
	assert.Empty(t, plugin.PlanApprovalOptions(contracts.CopilotPermissionModeAllowAll))
}

func TestCopilotIsInterruptRecognizesTheNativeAbort(t *testing.T) {
	t.Parallel()
	plugin := copilotPlugin(t)
	assert.True(t, plugin.IsInterrupt(`{"jsonrpc":"2.0","id":1,"method":"session.abort","params":{"sessionId":"s"}}`))
	assert.False(t, plugin.IsInterrupt(`{"jsonrpc":"2.0","id":1,"method":"session.send","params":{}}`))
	assert.False(t, plugin.IsInterrupt(`{"method":"session/cancel"}`), "the Agent Client Protocol frame is not Copilot's")
	assert.False(t, plugin.IsInterrupt("not json"))
}

// The option groups a not-running agent reports must name the same axes and values
// the runtime accepts, so a stored value validates before the process starts.
func TestCopilotStaticOptionGroupsMatchTheNativeAxes(t *testing.T) {
	t.Parallel()
	groups := AvailableOptionGroupsForProvider(leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT)
	for id, want := range map[string][]string{
		contracts.CopilotOptionSessionMode: {contracts.CopilotModeInteractive, contracts.CopilotModePlan, contracts.CopilotModeAutopilot},
		OptionIDPermissionMode:             {contracts.CopilotPermissionModeManual, contracts.CopilotPermissionModeAssisted, contracts.CopilotPermissionModeAllowAll},
	} {
		group := findOptionGroup(groups, id)
		require.NotNil(t, group, id)
		values := make([]string, 0, len(group.GetOptions()))
		for _, option := range group.GetOptions() {
			values = append(values, option.GetId())
		}
		assert.Equal(t, want, values, id)
	}
}

func findOptionGroup(groups []*leapmuxv1.AvailableOptionGroup, id string) *leapmuxv1.AvailableOptionGroup {
	for _, group := range groups {
		if group.GetId() == id {
			return group
		}
	}
	return nil
}

func TestCopilotDefaultModelUsesTheEnvironmentOverride(t *testing.T) {
	t.Setenv("LEAPMUX_COPILOT_DEFAULT_MODEL", "gpt-5.4-mini")
	assert.Equal(t, "gpt-5.4-mini", DefaultModel(leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT))
}

func TestCopilotSessionConfigRequestsTheEventsLeapMuxReads(t *testing.T) {
	t.Parallel()
	config := newCopilotSessionConfig(Options{WorkingDir: "/project"}, "session-1", false)
	raw, err := json.Marshal(config)
	require.NoError(t, err)
	var fields map[string]any
	require.NoError(t, json.Unmarshal(raw, &fields))
	// The subagent stream is what replaces the CLI's own session-store files, and the
	// permission and elicitation requests are what reach the browser as controls.
	for _, field := range []string{"includeSubAgentStreamingEvents", "requestPermission", "requestElicitation", "streaming"} {
		assert.Equal(t, true, fields[field], field)
	}
	// The question and the plan decision arrive as EVENTS instead, because the
	// runtime's own callbacks omit the native request ID. See CP-004.
	for _, field := range []string{"requestUserInput", "requestExitPlanMode"} {
		assert.Equal(t, false, fields[field], field)
	}
}

func TestCopilotControlIDSeparatesKindsAndSessions(t *testing.T) {
	t.Parallel()
	seen := map[string]string{}
	for _, test := range []struct{ session, kind, request string }{
		{"session-1", "permission", "1"},
		{"session-1", "question", "1"},
		{"session-2", "permission", "1"},
		{"session-1", "permission", "2"},
		{"session-1", "permission", ""},
	} {
		id := copilotControlID(test.session, test.kind, test.request)
		label := fmt.Sprintf("%s/%s/%s", test.session, test.kind, test.request)
		assert.NotEmpty(t, id, label)
		if previous, exists := seen[id]; exists {
			t.Fatalf("%s and %s share one control id", previous, label)
		}
		seen[id] = label
	}
	assert.Equal(t, copilotControlID("session-1", "permission", "1"), copilotControlID("session-1", "permission", "1"),
		"the same request keeps one identity across announcements")
}
