//go:build unix

package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const copilotNativeTaskStart = `{"type":"tool.execution_start","data":{"toolCallId":"native-task","toolName":"task","arguments":{"description":"Inspect sample","prompt":"Read sample.py","agent_type":"explore","mode":"sync"}}}`

func copilotNativeFixture(t *testing.T, events ...string) (*CopilotCLIAgent, *testSink) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("COPILOT_HOME", home)
	const sessionID = "d5c7d3e6-e251-44db-875b-34a248d1392a"
	directory := filepath.Join(home, "session-state", sessionID)
	require.NoError(t, os.MkdirAll(directory, 0o700))
	native := []byte(strings.Join(events, "\n") + "\n")
	require.NoError(t, os.WriteFile(filepath.Join(directory, "events.jsonl"), native, 0o600))
	a, _ := newCopilotAgentForRPC(t)
	a.sessionID = sessionID
	sink := &testSink{}
	a.sink = sink
	return a, sink
}

func TestCopilotNativeSubagentLaunchOpensNoSpan(t *testing.T) {
	a, sink := copilotNativeFixture(t, copilotNativeTaskStart)
	a.handleACPSessionUpdate(json.RawMessage(`{"update":{"sessionUpdate":"tool_call","toolCallId":"native-task","title":"Inspect sample","kind":"other","status":"pending","rawInput":{"description":"Inspect sample","prompt":"Read sample.py","agent_type":"explore","mode":"sync"}}}`), nil)
	assert.Empty(t, sink.OpenSpans())
	require.Len(t, sink.Messages(), 1)
	assert.True(t, sink.Messages()[0].NoSpan)
	require.Len(t, sink.BackgroundTasks(), 1)
	a.handleACPSessionUpdate(json.RawMessage(`{"update":{"sessionUpdate":"tool_call_update","toolCallId":"native-task","status":"completed","rawOutput":{"content":"Done"}}}`), nil)
	assert.Equal(t, bgtask.StatusCompleted, sink.BackgroundTasks()[0].Status)
}

func TestCopilotNativeToolUsesTheAgentWorkingDirectory(t *testing.T) {
	workingDir := t.TempDir()
	t.Setenv("COPILOT_HOME", "relative-copilot")
	a, _ := newCopilotAgentForRPC(t)
	a.workingDir = workingDir
	directory := filepath.Join(workingDir, "relative-copilot", "session-state", a.currentSessionID())
	require.NoError(t, os.MkdirAll(directory, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "events.jsonl"), []byte(copilotNativeTaskStart+"\n"), 0o600))
	record := a.nativeTool("native-task")
	require.NotNil(t, record)
	assert.Equal(t, "task", record.ToolName)
}

func TestCopilotChildToolsUseTheirOwnTranscript(t *testing.T) {
	a, sink := copilotNativeFixture(t, copilotNativeTaskStart,
		`{"type":"subagent.started","agentId":"native-child","data":{"toolCallId":"native-task"}}`,
		`{"type":"tool.execution_start","agentId":"native-child","data":{"toolCallId":"child-read","parentToolCallId":"native-task","toolName":"view","arguments":{"path":"/project/sample.py"}}}`)
	a.handleACPSessionUpdate(json.RawMessage(`{"update":{"sessionUpdate":"tool_call","toolCallId":"native-task","title":"Inspect sample","kind":"other","status":"pending"}}`), nil)
	request := `{"sessionUpdate":"tool_call","toolCallId":"child-read","title":"Viewing sample.py","kind":"read","status":"pending","rawInput":{"path":"/project/sample.py"},"_meta":{"github.com/copilot":{"agentId":"native-child"}}}`
	result := `{"sessionUpdate":"tool_call_update","toolCallId":"child-read","status":"completed","rawOutput":{"content":"answer = 41"},"_meta":{"github.com/copilot":{"agentId":"native-child"}}}`
	a.handleACPSessionUpdate(json.RawMessage(`{"update":`+request+`}`), nil)
	require.NoError(t, os.Remove(copilotToolStorePath(a.currentSessionID(), a.currentWorkingDir())))
	a.handleACPSessionUpdate(json.RawMessage(`{"update":`+result+`}`), nil)
	require.Len(t, sink.Messages(), 1, "child tools must not appear in the parent transcript")
	assert.Empty(t, sink.OpenSpans())
	sink.childSinkMu.Lock()
	child := sink.children["native-task"]
	sink.childSinkMu.Unlock()
	require.NotNil(t, child)
	messages := child.Messages()
	require.Len(t, messages, 3)
	assert.Contains(t, string(messages[0].Content), "Read sample.py")
	assert.Equal(t, request, string(messages[1].Content))
	assert.Equal(t, result, string(messages[2].Content))
	require.Len(t, child.OpenSpans(), 1)
	assert.Equal(t, "child-read", child.OpenSpans()[0].SpanID)
	require.Len(t, sink.BackgroundTasks(), 1)
	a.handleACPSessionUpdate(json.RawMessage(`{"update":{"sessionUpdate":"tool_call_update","toolCallId":"native-task","status":"completed","rawOutput":{"content":"Done"}}}`), nil)
	assert.Equal(t, bgtask.StatusCompleted, sink.BackgroundTasks()[0].Status)
	a.subagentMu.Lock()
	childCount := len(a.childTools)
	a.subagentMu.Unlock()
	assert.Zero(t, childCount, "completed children must release their protocol state")
}

func TestCopilotBackgroundLaunchDoesNotCompleteTheChild(t *testing.T) {
	a, sink := copilotNativeFixture(t, strings.Replace(copilotNativeTaskStart, `"mode":"sync"`, `"mode":"background"`, 1))
	a.handleACPSessionUpdate(json.RawMessage(`{"update":{"sessionUpdate":"tool_call","toolCallId":"native-task","title":"Inspect sample","kind":"other","status":"pending"}}`), nil)
	a.handleACPSessionUpdate(json.RawMessage(`{"update":{"sessionUpdate":"tool_call_update","toolCallId":"native-task","status":"completed","rawOutput":{"content":"Agent launched"}}}`), nil)
	require.Len(t, sink.BackgroundTasks(), 1)
	assert.Equal(t, bgtask.StatusRunning, sink.BackgroundTasks()[0].Status)
	file, err := os.OpenFile(copilotToolStorePath(a.currentSessionID(), a.currentWorkingDir()), os.O_APPEND|os.O_WRONLY, 0)
	require.NoError(t, err)
	_, err = file.WriteString("{\"type\":\"subagent.completed\",\"agentId\":\"native-child\",\"data\":{\"toolCallId\":\"native-task\"}}\n")
	require.NoError(t, err)
	require.NoError(t, file.Close())
	assert.Eventually(t, func() bool { return sink.BackgroundTasks()[0].Status == bgtask.StatusCompleted }, 3*time.Second, 10*time.Millisecond)
}

func TestCopilotBackgroundNativeOutcome(t *testing.T) {
	for _, test := range []struct {
		name, event string
		status      bgtask.Status
	}{
		{"cancelled", `{"type":"subagent.completed","agentId":"native-child","data":{"toolCallId":"native-task","cancelled":true}}`, bgtask.StatusStopped},
		{"failed", `{"type":"subagent.failed","agentId":"native-child","data":{"toolCallId":"native-task"}}`, bgtask.StatusFailed},
	} {
		for _, delayed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/delayed=%t", test.name, delayed), func(t *testing.T) {
				events := []string{strings.Replace(copilotNativeTaskStart, `"mode":"sync"`, `"mode":"background"`, 1)}
				if !delayed {
					events = append(events, test.event)
				}
				a, sink := copilotNativeFixture(t, events...)
				a.handleACPUpdate(json.RawMessage(`{"sessionUpdate":"tool_call","toolCallId":"native-task","title":"Inspect sample","kind":"other","status":"pending"}`), nil)
				a.handleACPUpdate(json.RawMessage(`{"sessionUpdate":"tool_call_update","toolCallId":"native-task","status":"completed"}`), nil)
				if delayed {
					file, err := os.OpenFile(copilotToolStorePath(a.currentSessionID(), a.currentWorkingDir()), os.O_APPEND|os.O_WRONLY, 0)
					require.NoError(t, err)
					_, err = file.WriteString(test.event + "\n")
					require.NoError(t, err)
					require.NoError(t, file.Close())
				}
				assert.Eventually(t, func() bool {
					return len(sink.BackgroundTasks()) == 1 && sink.BackgroundTasks()[0].Status == test.status
				}, 3*time.Second, 10*time.Millisecond)
			})
		}
	}
}

// Copilot uses its native tool identity. Other providers' argument shapes do not identify its subagents.
func TestCopilot_RejectsOtherProviderSubagentShapes(t *testing.T) {
	t.Parallel()

	agent, _ := newCopilotAgentForRPC(t)
	base := &agent.acpBase
	assert.NotNil(t, base.subagentFromToolCall)
	assert.NotNil(t, base.subagentFromToolCallUpdate)

	// --- Behavioral assertion: spawn-shaped payloads write nothing to the
	// registry when driven through the shared ACP session-update dispatcher. ---
	// Wire a fresh testSink so a registry write (UpsertBackgroundTask /
	// EnsureChildAgent) is observable via sink.BackgroundTasks().
	sink := &testSink{}
	base.sink = sink

	// A payload that WOULD be detected as a spawn by the OpenCode detector
	// ({description,prompt,subagent_type}) and Reasonix detector
	// ({description,prompt}) if Copilot had wired those hooks.
	opencodeSpawnShape := json.RawMessage(`{"sessionUpdate":"tool_call","toolCallId":"tc-opencode","title":"build feature","kind":"other","status":"in_progress","rawInput":{"description":"build feature","prompt":"do the thing","subagent_type":"build"}}`)

	// A payload that WOULD be detected as a spawn by the Goose detector
	// (_meta.goose.toolCall {delegate,summon}).
	gooseSpawnShape := json.RawMessage(`{"sessionUpdate":"tool_call","toolCallId":"tc-goose","title":"delegate","status":"in_progress","_meta":{"goose":{"toolCall":{"toolName":"delegate","extensionName":"summon"}}}}`)

	// These unrelated shapes have no matching native Copilot task event.
	require.NotPanics(t, func() {
		base.handleACPSessionUpdate(
			json.RawMessage(`{"update":`+string(opencodeSpawnShape)+`}`),
			nil, /* extra: no provider-specific session-update handler */
		)
	})
	require.NotPanics(t, func() {
		base.handleACPSessionUpdate(
			json.RawMessage(`{"update":`+string(gooseSpawnShape)+`}`),
			nil,
		)
	})

	// A final update with another provider's metadata also identifies no Copilot task.
	finalUpdate := json.RawMessage(`{"sessionUpdate":"tool_call_update","toolCallId":"tc-opencode","status":"completed","rawOutput":{"metadata":{"sessionId":"child-sess-1"}}}`)
	require.NotPanics(t, func() {
		base.handleACPSessionUpdate(
			json.RawMessage(`{"update":`+string(finalUpdate)+`}`),
			nil,
		)
	})

	// The registry must be empty: no UpsertBackgroundTask, no EnsureChildAgent.
	assert.Empty(t, sink.BackgroundTasks(),
		"Copilot must not write background-task registry rows for spawn-shaped payloads")
	// No child transcript sink was ever created either.
	sink.childSinkMu.Lock()
	assert.Empty(t, sink.children,
		"Copilot must not spawn child transcripts")
	sink.childSinkMu.Unlock()
}
