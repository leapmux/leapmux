//go:build unix

package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

func newReasonixAgentForRPC(t *testing.T) (*ReasonixAgent, func() []recordedRequest) {
	return newACPAgentForRPC(t,
		func() *ReasonixAgent { return &ReasonixAgent{} },
		func(a *ReasonixAgent) *acpBase { return &a.acpBase },
	)
}

const reasonixSessionFixture = `{"sessionId":"reasonix-new","models":{"currentModelId":"deepseek-flash","availableModels":[{"modelId":"deepseek-flash","name":"Flash"},{"modelId":"deepseek-pro","name":"Pro"}]},"modes":{"currentModeId":"normal","availableModes":[{"id":"normal","name":"Normal"},{"id":"plan","name":"Plan"},{"id":"goal","name":"Goal"}]},"configOptions":[{"id":"model","name":"Model","category":"model","type":"select","currentValue":"deepseek-flash","options":[{"value":"deepseek-flash","name":"Flash"},{"value":"deepseek-pro","name":"Pro"}]},{"id":"effort","name":"Effort","category":"thought_level","type":"select","currentValue":"max","options":[{"value":"high","name":"High"},{"value":"max","name":"Max"}]},{"id":"tool_approval","name":"Tool Approval","category":"tool_approval","type":"select","currentValue":"ask","options":[{"value":"ask","name":"Ask"},{"value":"auto","name":"Auto"},{"value":"yolo","name":"Yolo"}]}]}`

// installFakeReasonixCLI puts a fake `reasonix` on PATH. The launcher records the
// argv it was invoked with to argsFile so a test can assert the startup `--model`
// flag, then exec's the helper process that speaks ACP.
func installFakeReasonixCLI(t *testing.T, argsFile string) {
	installFakeACPCLI(t, fakeACPCLISpec{
		binary:    "reasonix",
		helperRun: "TestHelperProcessReasonixCLI",
		wantEnv:   "GO_WANT_HELPER_PROCESS_REASONIX",
		argsFile:  argsFile,
	})
}

// TestHelperProcessReasonixCLI supplies the advertised Reasonix session settings.
func TestHelperProcessReasonixCLI(*testing.T) {
	runFakeACPServer("GO_WANT_HELPER_PROCESS_REASONIX", func(method string) (string, bool, bool) {
		switch method {
		case acpMethodInitialize:
			return `{"protocolVersion":1,"agentCapabilities":{"loadSession":true,"promptCapabilities":{"image":false,"audio":false,"embeddedContext":true}}}`, false, true
		case acpMethodSessionNew:
			return reasonixSessionFixture, false, true
		case acpMethodSessionSetModel, acpMethodSessionSetMode:
			return `{}`, false, true
		case acpMethodSessionSetConfigOption:
			return reasonixSessionFixture, false, true
		case acpMethodSessionLoad, acpMethodSessionPrompt:
			return `{}`, false, true
		default:
			return "", false, false
		}
	})
}

func TestStartReasonix_NewSessionHandshakePassesModelFlag(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "args.txt")
	installFakeReasonixCLI(t, argsFile)

	provider, err := StartReasonix(context.Background(), Options{
		AgentID:       "reasonix-new",
		Options:       map[string]string{OptionIDModel: "deepseek-flash"},
		WorkingDir:    t.TempDir(),
		Shell:         testutil.TestShell(),
		LoginShell:    false,
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_REASONIX,
	}, &testSink{})
	require.NoError(t, err)

	agent := provider.(*ReasonixAgent)
	t.Cleanup(func() {
		agent.Stop()
		_ = agent.Wait()
	})

	assert.Equal(t, "reasonix-new", agent.sessionID)
	assert.Equal(t, "deepseek-flash", agent.model)
	current := CurrentOptions(agent.OptionGroups())
	assert.Equal(t, "normal", current[OptionIDPermissionMode])
	assert.Equal(t, "max", current[OptionIDEffort])
	assert.Equal(t, "ask", current["tool_approval"])

	// The launch flag selects the initial model.
	recorded, err := os.ReadFile(argsFile)
	require.NoError(t, err)
	assert.Contains(t, string(recorded), "acp --model deepseek-flash")
}

func TestReasonixAppliesAdvertisedSettingsWithoutRestart(t *testing.T) {
	values := map[string]string{"model": "deepseek-flash", "effort": "max", "tool_approval": "ask"}
	var fixture struct {
		ConfigOptions []map[string]any `json:"configOptions"`
	}
	require.NoError(t, json.Unmarshal([]byte(reasonixSessionFixture), &fixture))
	a, requests := newACPAgentForRPCWithRequestResponder(t,
		func() *ReasonixAgent { return &ReasonixAgent{} },
		func(a *ReasonixAgent) *acpBase { return &a.acpBase },
		func(req recordedRequest) jsonrpcResponsePayload {
			if req.Method == acpMethodSessionSetConfigOption {
				id, _ := req.Params["configId"].(string)
				value, _ := req.Params["value"].(string)
				values[id] = value
				for _, option := range fixture.ConfigOptions {
					option["currentValue"] = values[option["id"].(string)]
				}
				data, err := json.Marshal(fixture)
				require.NoError(t, err)
				return jsonrpcResponsePayload{Result: data}
			}
			return jsonrpcResponsePayload{Result: json.RawMessage(`{}`)}
		},
	)
	a.sink = &testSink{}
	a.modeChannel = modeChannelPermissionMode
	handshake, err := parseACPSessionResult(json.RawMessage(reasonixSessionFixture))
	require.NoError(t, err)
	a.applyHandshakeModels(handshake)
	a.applyHandshakeMode(handshake, "normal")
	a.handleACPConfigOptionUpdate(json.RawMessage(reasonixSessionFixture))
	result := a.UpdateSettings(map[string]string{OptionIDModel: "deepseek-pro", OptionIDEffort: "high", OptionIDPermissionMode: "plan", "tool_approval": "yolo"})
	assert.True(t, result.AppliedLive)
	current := CurrentOptions(a.OptionGroups())
	assert.Equal(t, "deepseek-pro", current[OptionIDModel])
	assert.Equal(t, "high", current[OptionIDEffort])
	assert.Equal(t, "plan", current[OptionIDPermissionMode])
	assert.Equal(t, "yolo", current["tool_approval"])
	assert.NotEmpty(t, requests())
}

func TestReasonixTaskCompletionClosesRegistry(t *testing.T) {
	installFakeReasonixCLI(t, filepath.Join(t.TempDir(), "args.txt"))
	sink := &testSink{}
	provider, err := StartReasonix(t.Context(), Options{AgentID: "reasonix-new", WorkingDir: t.TempDir(), Shell: testutil.TestShell(), AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_REASONIX}, sink)
	require.NoError(t, err)
	a := provider.(*ReasonixAgent)
	t.Cleanup(func() { a.Stop(); _ = a.Wait() })
	for _, tt := range []struct {
		name, status, output string
		background           bool
		want                 bgtask.Status
	}{
		{name: "completed", status: "completed", output: "Subagent reference: sa_example\nSubagent outcome: status=completed retryable=false\n\nFinal answer:\nReport", want: bgtask.StatusCompleted},
		{name: "partial", status: "completed", output: "Subagent reference: sa_example\nSubagent outcome: status=partial retryable=true error_code=max_steps", want: bgtask.StatusCompleted},
		{name: "native failure", status: "completed", output: "Subagent reference (failed): sa_example\nSubagent outcome: status=failed retryable=false error_code=subagent_error", want: bgtask.StatusFailed},
		{name: "native cancellation", status: "completed", output: "Subagent reference: sa_example\nSubagent outcome: status=cancelled retryable=false", want: bgtask.StatusStopped},
		{name: "transport failure", status: "failed", output: "Unavailable", want: bgtask.StatusFailed},
		{name: "transport cancellation", status: "cancelled", want: bgtask.StatusStopped},
		{name: "background acknowledgement", status: "completed", background: true, output: "Started background task \"job-1\" (Inspect sample).", want: bgtask.StatusRunning},
		{name: "background failure", status: "failed", background: true, output: "Cannot start", want: bgtask.StatusFailed},
		{name: "progress", status: "in_progress", output: "Working", want: bgtask.StatusRunning},
		{name: "unstructured result", status: "completed", output: "Report\nSubagent outcome: status=failed retryable=false", want: bgtask.StatusCompleted},
	} {
		t.Run(tt.name, func(t *testing.T) {
			request, err := json.Marshal(map[string]any{
				"sessionUpdate": "tool_call", "toolCallId": tt.name, "title": "use_capability", "kind": "other", "status": "pending",
				"rawInput": map[string]any{"action": "call", "capability_id": "tool:task", "arguments": map[string]any{"description": "Inspect sample", "prompt": "Read sample.py", "run_in_background": tt.background}},
			})
			require.NoError(t, err)
			a.handleACPUpdate(request, nil)
			update, err := json.Marshal(map[string]any{
				"sessionUpdate": "tool_call_update", "toolCallId": tt.name, "status": tt.status,
				"content": []any{map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": tt.output}}},
			})
			require.NoError(t, err)
			a.handleACPUpdate(update, nil)
			found := false
			for _, task := range sink.BackgroundTasks() {
				if task.RowKey == tt.name {
					found = true
					assert.Equal(t, tt.want, task.Status)
				}
			}
			require.True(t, found)
		})
	}
}

func TestReasonixRecoversTruncatedToolResults(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("REASONIX_HOME", dir)
	installFakeReasonixCLI(t, filepath.Join(t.TempDir(), "args.txt"))
	full := strings.Repeat("reader result line\n", 1000)
	stored, err := json.Marshal(map[string]any{
		"role": "tool", "id": "native-result", "tool_call_id": "read-call", "name": "read_file",
		"tool_run_state": "completed", "content": full,
	})
	require.NoError(t, err)
	writeFixtureFile(t, filepath.Join(dir, "sessions", "reasonix-new.jsonl"), string(stored)+"\n")
	sink := &testSink{}
	provider, err := StartReasonix(t.Context(), Options{
		AgentID: "reasonix-new", WorkingDir: t.TempDir(), Shell: testutil.TestShell(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_REASONIX,
	}, sink)
	require.NoError(t, err)
	a := provider.(*ReasonixAgent)
	t.Cleanup(func() {
		a.Stop()
		_ = a.Wait()
	})
	original, err := json.Marshal(map[string]any{
		"sessionUpdate": "tool_call_update", "toolCallId": "read-call", "status": "completed",
		"content": []any{map[string]any{"type": "content", "content": map[string]any{
			"type": "text", "text": full[:8000] + "\n…(11000 more chars truncated)",
		}}},
	})
	require.NoError(t, err)
	require.NoError(t, a.sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, MessageContent{Original: original}, SpanInfo{SpanID: "read-call", Closing: true}))
	require.NoError(t, a.sink.PersistTurnEnd(MessageContent{Original: []byte(`{"stopReason":"end_turn"}`)}, SpanInfo{}))
	var result *testSinkMessage
	for _, message := range sink.Messages() {
		if message.SpanID == "read-call" {
			result = &message
			break
		}
	}
	require.NotNil(t, result)
	assert.Equal(t, original, result.Content)
	assert.Equal(t, 1000, strings.Count(string(result.SupplementalContent), "reader result line"))
}

func TestStartReasonix_DefaultsModelFlagWhenUnset(t *testing.T) {
	// Clear the env override so the catalog default (deepseek-flash) applies.
	t.Setenv("LEAPMUX_REASONIX_DEFAULT_MODEL", "")
	argsFile := filepath.Join(t.TempDir(), "args.txt")
	installFakeReasonixCLI(t, argsFile)

	provider, err := StartReasonix(context.Background(), Options{
		AgentID:       "reasonix-default",
		WorkingDir:    t.TempDir(),
		Shell:         testutil.TestShell(),
		LoginShell:    false,
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_REASONIX,
	}, &testSink{})
	require.NoError(t, err)

	agent := provider.(*ReasonixAgent)
	t.Cleanup(func() {
		agent.Stop()
		_ = agent.Wait()
	})

	// An unset model is pinned to the provider default and always passed as
	// --model, so LeapMux controls the model rather than reasonix.toml's
	// default_model, and the stored model is never empty.
	assert.Equal(t, "deepseek-flash", agent.model)
	recorded, err := os.ReadFile(argsFile)
	require.NoError(t, err)
	assert.Equal(t, "acp --model deepseek-flash", strings.TrimSpace(string(recorded)))
}

func TestStartReasonix_LoadSessionUsesResumeID(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "args.txt")
	installFakeReasonixCLI(t, argsFile)

	provider, err := StartReasonix(context.Background(), Options{
		AgentID:         "reasonix-load",
		Options:         map[string]string{OptionIDModel: "deepseek-pro"},
		WorkingDir:      t.TempDir(),
		ResumeSessionID: "reasonix-resume",
		Shell:           testutil.TestShell(),
		LoginShell:      false,
		AgentProvider:   leapmuxv1.AgentProvider_AGENT_PROVIDER_REASONIX,
	}, &testSink{})
	require.NoError(t, err)

	agent := provider.(*ReasonixAgent)
	t.Cleanup(func() {
		agent.Stop()
		_ = agent.Wait()
	})

	// session/load returns no sessionId, so the handshake keeps the resume id.
	assert.Equal(t, "reasonix-resume", agent.sessionID)
	assert.Equal(t, "deepseek-pro", agent.model)
}

func TestReasonixCancelSessionSendsACPMethod(t *testing.T) {
	agent, requests := newReasonixAgentForRPC(t)

	require.NoError(t, agent.cancelSession())
	testutil.AssertEventually(t, func() bool {
		recorded := requests()
		return len(recorded) == 1 && recorded[0].Method == acpMethodSessionCancel
	}, "expected session/cancel notification to be recorded")
}

func TestReasonixAvailableOptionGroupsIsNil(t *testing.T) {
	agent := &ReasonixAgent{}
	assert.Nil(t, agent.OptionGroups())
}

func TestReasonixAppliesConfigOptionModelUpdate(t *testing.T) {
	agent, _ := newReasonixAgentForRPC(t)
	agent.sink = &testSink{}
	agent.model = "deepseek-flash"
	agent.handleACPConfigOptionUpdate(json.RawMessage(
		`{"configOptions":[{"id":"model","currentValue":"deepseek-pro","options":[{"value":"deepseek-flash"},{"value":"deepseek-pro"}]}]}`,
	))
	assert.Equal(t, "deepseek-pro", agent.model)
}

func TestReasonixModelCatalog(t *testing.T) {
	// The static catalog mirrors Reasonix's built-in provider entries; the id is
	// the bare provider-entry name its `--model` flag accepts.
	ids := make([]string, 0, len(reasonixAvailableModels))
	defaults := 0
	for _, m := range reasonixAvailableModels {
		ids = append(ids, m.GetId())
		if m.IsDefault {
			defaults++
		}
	}
	assert.Equal(t, []string{"deepseek-flash", "deepseek-pro", "mimo-pro", "mimo-flash"}, ids)
	assert.Equal(t, 1, defaults, "exactly one model is the default")
	assert.True(t, reasonixAvailableModels[0].IsDefault, "deepseek-flash is the default")
}

func TestDefaultModel_ReasonixDefaultsToDeepseekFlash(t *testing.T) {
	t.Setenv("LEAPMUX_REASONIX_DEFAULT_MODEL", "")
	assert.Equal(t, "deepseek-flash", DefaultModel(leapmuxv1.AgentProvider_AGENT_PROVIDER_REASONIX))
}

func TestDefaultModel_ReasonixUsesEnvOverride(t *testing.T) {
	t.Setenv("LEAPMUX_REASONIX_DEFAULT_MODEL", "deepseek-pro")
	assert.Equal(t, "deepseek-pro", DefaultModel(leapmuxv1.AgentProvider_AGENT_PROVIDER_REASONIX))
}
