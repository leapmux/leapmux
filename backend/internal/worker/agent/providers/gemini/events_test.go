package gemini

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp/acptest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGeminiModeTextCannotChangeNativePermissionState(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	services := agent.NewProviderServices(sink)
	agent := &Agent{}
	agent.SetPromptActiveForTest(true)
	agent.SetSinkForTest(services)
	agent.HooksForTest().ModeChannel = acp.ModeChannelPermissionMode
	agent.SetPermissionModeForTest(contracts.GeminiModeDefault)
	agent.SetAvailableModesForTest(geminiModes())
	consumed := agent.handleSessionUpdate("native-session", services, json.RawMessage(`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"[MODE_UPDATE] yolo"}}`))
	assert.False(t, consumed, "native model text must remain conversation content")
	assert.Equal(t, contracts.GeminiModeDefault, agent.PermissionModeForTest())
	assert.Zero(t, sink.SessionInfoCount())
}

func TestGeminiRecoveredModeCannotReplaceALaterAcknowledgedSetter(t *testing.T) {
	t.Parallel()
	a := &Agent{}
	a.SetPromptActiveForTest(true)
	a.SetSinkForTest(agent.NewProviderServices(&agenttest.Sink{}))
	a.SetSessionIDForTest("root")
	a.SetAvailableModesForTest(geminiModes())
	a.HooksForTest().ModeChannel = acp.ModeChannelPermissionMode
	a.SetPermissionModeForTest(contracts.GeminiModePlan)
	a.HooksForTest().ModeSetter = func(mode string, acknowledged func(string)) error {
		require.True(t, a.observeModeSetterReply("root", mode, json.RawMessage(`{}`), nil, acknowledged))
		return nil
	}
	a.handleSessionUpdate("root", nil, json.RawMessage(`{"sessionUpdate":"tool_call","toolCallId":"exit_plan_mode__call","status":"in_progress"}`))
	a.handleSessionUpdate("root", nil, json.RawMessage(`{"sessionUpdate":"tool_call_update","toolCallId":"exit_plan_mode__call","status":"completed"}`))
	result := a.UpdateSettings(optionmap.Map{agent.OptionIDPermissionMode: contracts.GeminiModeYolo})
	require.Equal(t, contracts.GeminiModeYolo, result.ConfirmedOptions()[agent.OptionIDPermissionMode])
	a.observeNativeMode("root", json.RawMessage(`{"id":"exit_plan_mode__call","name":"exit_plan_mode","status":"success","resultDisplay":"Plan approved: /native/plan.md","result":[{"functionResponse":{"response":{"output":"Plan approved. Switching to Default mode."}}}]}`))
	assert.Equal(t, contracts.GeminiModeYolo, a.PermissionModeForTest(), "the later native setter remains authoritative")
}

func TestGeminiModeChangeRequiresItsExactCompletedNativeTool(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	services := agent.NewProviderServices(sink)
	a := &Agent{}
	a.SetPromptActiveForTest(true)
	a.SetSinkForTest(services)
	a.SetSessionIDForTest("session-root")
	a.HooksForTest().ModeChannel = acp.ModeChannelPermissionMode
	a.SetPermissionModeForTest(contracts.GeminiModeDefault)
	a.SetAvailableModesForTest(geminiModes())
	const start = `{"sessionUpdate":"tool_call","toolCallId":"enter_plan_mode__model-call","status":"in_progress"}`
	const success = `{"id":"enter_plan_mode__model-call","name":"enter_plan_mode","status":"success","result":[{"functionResponse":{"response":{"output":"Switching to Plan mode."}}}]}`
	assert.False(t, a.handleSessionUpdate("session-root", services, json.RawMessage(start)))
	assert.True(t, a.handleSessionUpdate("session-root", services, json.RawMessage(`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"[MODE_UPDATE] plan"}}`)))
	assert.Equal(t, contracts.GeminiModeDefault, a.PermissionModeForTest(), "text alone cannot change the native mode")
	a.observeNativeMode("session-foreign", json.RawMessage(success))
	assert.Equal(t, contracts.GeminiModeDefault, a.PermissionModeForTest())
	a.observeNativeMode("session-root", json.RawMessage(success))
	assert.Equal(t, contracts.GeminiModePlan, a.PermissionModeForTest())
	count := sink.SessionInfoCount()
	a.observeNativeMode("session-root", json.RawMessage(success))
	assert.Equal(t, count, sink.SessionInfoCount(), "a duplicate native record cannot replay the setting change")
}

// Gemini states the content of each tool call as an array of content blocks,
// as the Agent Client Protocol does (acpSession.ts runTool), and the array can
// be empty. The mode bookkeeping must read a tool call of that shape. Before
// this test, the hook decoded the content as ONE text block, the decode of
// every real tool call failed, and no native mode call was ever tracked: the
// `[MODE_UPDATE] plan` text of Gemini's own plan mode reached the transcript,
// and the completed record could not change the permission mode.
func TestGeminiModeChangeReadsTheToolCallShapeThatGeminiSends(t *testing.T) {
	t.Parallel()
	for name, start := range map[string]string{
		"an empty content array": `{"sessionUpdate":"tool_call","toolCallId":"enter_plan_mode__model-call","status":"in_progress","title":"Enter Plan Mode","content":[],"locations":[],"kind":"other"}`,
		"a content block":        `{"sessionUpdate":"tool_call","toolCallId":"enter_plan_mode__model-call","status":"pending","title":"Enter Plan Mode","content":[{"type":"content","content":{"type":"text","text":"Plan the change first."}}],"locations":[],"kind":"other"}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a := &Agent{}
			a.SetPromptActiveForTest(true)
			a.SetSinkForTest(agent.NewProviderServices(&agenttest.Sink{}))
			a.SetSessionIDForTest("session-root")
			a.HooksForTest().ModeChannel = acp.ModeChannelPermissionMode
			a.SetPermissionModeForTest(contracts.GeminiModeDefault)
			a.SetAvailableModesForTest(geminiModes())

			assert.False(t, a.handleSessionUpdate("session-root", nil, json.RawMessage(start)), "the tool call stays conversation")
			assert.True(t, a.handleSessionUpdate("session-root", nil, json.RawMessage(`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"[MODE_UPDATE] plan"}}`)),
				"the mode text of a tracked call is no conversation")
			a.observeNativeMode("session-root", json.RawMessage(`{"id":"enter_plan_mode__model-call","name":"enter_plan_mode","status":"success","result":[{"functionResponse":{"response":{"output":"Switching to Plan mode."}}}]}`))
			assert.Equal(t, contracts.GeminiModePlan, a.PermissionModeForTest())
		})
	}
}

func TestGeminiRejectsFailedUntrackedAndMalformedModeRecords(t *testing.T) {
	t.Parallel()
	for _, record := range []string{
		`null`, `{}`, `{`,
		`{"id":"enter_plan_mode__another-call","name":"enter_plan_mode","status":"success","result":[{"functionResponse":{"response":{"output":"Switching to Plan mode."}}}]}`,
		`{"id":"enter_plan_mode__call","name":"enter_plan_mode","status":"error","result":[{"functionResponse":{"response":{"output":"Switching to Plan mode."}}}]}`,
		`{"id":"enter_plan_mode__call","name":"enter_plan_mode","status":"success","result":[{"functionResponse":{"response":{"output":"User cancelled entering Plan Mode."}}}]}`,
		`{"id":"enter_plan_mode__call","name":"other","status":"success","result":[{"functionResponse":{"response":{"output":"Switching to Plan mode."}}}]}`,
	} {
		a := &Agent{}
		a.SetPromptActiveForTest(true)
		a.SetSessionIDForTest("root")
		a.SetPermissionModeForTest(contracts.GeminiModeDefault)
		a.handleSessionUpdate("root", nil, json.RawMessage(`{"sessionUpdate":"tool_call","toolCallId":"enter_plan_mode__call"}`))
		a.observeNativeMode("root", json.RawMessage(record))
		assert.Equal(t, contracts.GeminiModeDefault, a.PermissionModeForTest(), record)
	}
}

func TestGeminiContextResetRemovesPendingModeCalls(t *testing.T) {
	t.Parallel()
	a := &Agent{}
	a.SetPromptActiveForTest(true)
	a.handleSessionUpdate("root", nil, json.RawMessage(`{"sessionUpdate":"tool_call","toolCallId":"enter_plan_mode__call"}`))
	a.resetNativeModes()
	assert.False(t, a.handleSessionUpdate("root", nil, json.RawMessage(`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"[MODE_UPDATE] plan"}}`)))
}

// Gemini CLI 0.62.0 wrote the frames below, byte for byte, when a probe loaded
// an older session with session/load and then sent one prompt. Gemini starts
// the replay of the history and does not await it
// (packages/cli/src/acp/acpSessionManager.ts, `session.streamHistory(messages)`,
// upstream issue 28775), so only the first replay frame arrives before the
// session/load reply. The rest arrives after the reply, while no prompt runs.
//
// geminiReplayContext keeps the shape of the first replay frame but drops the
// local paths that the probe machine wrote into it.
const (
	geminiReplaySessionID = "dcbb182b-5b75-4693-9a82-9000afab4419"
	geminiInitializeReply = `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":1,"authMethods":[{"id":"oauth-personal","name":"Log in with Google","description":"Log in with your Google account"},{"id":"gemini-api-key","name":"Gemini API key","description":"Use an API key with Gemini Developer API","_meta":{"api-key":{"provider":"google"}}},{"id":"vertex-ai","name":"Vertex AI","description":"Use an API key with Vertex AI GenAI API"},{"id":"gateway","name":"AI API Gateway","description":"Use a custom AI API Gateway","_meta":{"gateway":{"protocol":"google","restartRequired":"false"}}}],"agentInfo":{"name":"gemini-cli","title":"Gemini CLI","version":"0.62.0"},"agentCapabilities":{"loadSession":true,"promptCapabilities":{"image":true,"audio":true,"embeddedContext":true},"mcpCapabilities":{"http":true,"sse":true}}}}`
	geminiReplayContext   = `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"dcbb182b-5b75-4693-9a82-9000afab4419","update":{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":"<session_context>\nThis is the Gemini CLI. We are setting up the context for our chat.\nToday's date is Saturday, October 3, 2026 (formatted according to the user's locale).\nMy operating system is: darwin\n</session_context>"}}}}`
	geminiLoadReply       = `{"jsonrpc":"2.0","id":2,"result":{"modes":{"availableModes":[{"id":"default","name":"Default","description":"Prompts for approval"},{"id":"autoEdit","name":"Auto Edit","description":"Auto-approves edit tools"},{"id":"yolo","name":"YOLO","description":"Auto-approves all tools"},{"id":"plan","name":"Plan","description":"Read-only mode"}],"currentModeId":"default"},"models":{"availableModels":[{"modelId":"auto","name":"Auto","description":"Let Gemini CLI decide the best model for the task: gemini-3.1-pro-preview, gemini-3.8-flash"},{"modelId":"gemini-3.1-pro-preview-customtools","name":"gemini-3.1-pro-preview"},{"modelId":"gemini-3-flash-preview","name":"gemini-3-flash-preview"},{"modelId":"gemini-2.5-pro","name":"gemini-2.5-pro"},{"modelId":"gemini-3.8-flash","name":"gemini-3.8-flash"},{"modelId":"gemini-3.5-flash-lite","name":"gemini-3.5-flash-lite"}],"currentModelId":"gemini-2.5-pro"}}}`
	// The replay after the reply: the old prompt, the old answer, and the
	// command list that a timer sends.
	geminiReplayPrompt   = `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"dcbb182b-5b75-4693-9a82-9000afab4419","update":{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":"LEAPMUX_GEMINI_PROBE"}}}}`
	geminiReplayAnswer   = `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"dcbb182b-5b75-4693-9a82-9000afab4419","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"GEMINI_MOCK_REPLY"}}}}`
	geminiReplayCommands = `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"dcbb182b-5b75-4693-9a82-9000afab4419","update":{"sessionUpdate":"available_commands_update","availableCommands":[{"name":"memory","description":"Manage memory."},{"name":"memory show","description":"Shows the current memory contents."},{"name":"memory refresh","description":"Refreshes the memory from the source."},{"name":"memory list","description":"Lists the paths of the GEMINI.md files in use."},{"name":"memory inbox","description":"Lists memory items extracted from past sessions that are pending review."},{"name":"extensions","description":"Manage extensions."},{"name":"extensions list","description":"Lists all installed extensions."},{"name":"extensions explore","description":"Explore available extensions."},{"name":"extensions enable","description":"Enable an extension."},{"name":"extensions disable","description":"Disable an extension."},{"name":"extensions install","description":"Install an extension from a git repo or local path."},{"name":"extensions link","description":"Link an extension from a local path."},{"name":"extensions uninstall","description":"Uninstall an extension."},{"name":"extensions restart","description":"Restart an extension."},{"name":"extensions update","description":"Update an extension."},{"name":"init","description":"Analyzes the project and creates a tailored GEMINI.md file"},{"name":"restore","description":"Restore to a previous checkpoint, or list available checkpoints to restore. This will reset the conversation and file history to the state it was in when the checkpoint was created"},{"name":"restore list","description":"Lists all available checkpoints."},{"name":"about","description":"Show version and environment info"},{"name":"help","description":"Show available commands"}]}}}`
	// The answer to the new prompt, and the reply that ends that prompt.
	geminiNewAnswer   = `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"dcbb182b-5b75-4693-9a82-9000afab4419","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"OLDER_NATIVE_RESUME_CONFIRMED"}}}}`
	geminiPromptReply = `{"jsonrpc":"2.0","id":3,"result":{"stopReason":"end_turn","_meta":{"quota":{"token_count":{"input_tokens":1,"output_tokens":1},"model_usage":[{"model":"gemini-2.5-pro","token_count":{"input_tokens":1,"output_tokens":1}}]}}}}`
	// geminiUsageUpdate is a usage frame that Gemini CLI 0.62.0 wrote in a
	// second probe of the same kind.
	geminiUsageUpdate = `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"dcbb182b-5b75-4693-9a82-9000afab4419","update":{"sessionUpdate":"usage_update","used":2,"size":1048576}}}`
)

// geminiFrameParams returns the params of one session/update frame: the part
// that the reader hands to the dispatcher.
func geminiFrameParams(t *testing.T, frame string) json.RawMessage {
	t.Helper()
	var line struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	require.NoError(t, json.Unmarshal([]byte(frame), &line))
	require.Equal(t, "session/update", line.Method)
	return line.Params
}

// geminiReplayedUpdate puts one update of the replayed session into the params
// of a session/update notification.
func geminiReplayedUpdate(update string) json.RawMessage {
	return json.RawMessage(`{"sessionId":"` + geminiReplaySessionID + `","update":` + update + `}`)
}

// Updates in the shapes that Gemini sends. The thought, the tool call and the
// answer have the shapes of its history replay (acpSession.ts streamHistory).
// Gemini sends no tool_call_update and no plan in the replay, but each is
// conversation content that a turn draws, so each is a case too.
const (
	geminiThoughtUpdate        = `{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"**Reading the file**\nThe turn reads the file first."}}`
	geminiToolCallUpdate       = `{"sessionUpdate":"tool_call","toolCallId":"read_file__replayed","status":"completed","title":"ReadFile","content":[{"type":"content","content":{"type":"text","text":"file contents"}}],"kind":"read"}`
	geminiFailedToolCall       = `{"sessionUpdate":"tool_call","toolCallId":"run_shell_command__replayed","status":"failed","title":"Shell","content":[],"kind":"execute"}`
	geminiToolCallUpdateUpdate = `{"sessionUpdate":"tool_call_update","toolCallId":"run_shell_command__updated","status":"completed","content":[{"type":"content","content":{"type":"text","text":"command output"}}]}`
	geminiPlanUpdate           = `{"sessionUpdate":"plan","entries":[{"content":"Read the file","priority":"medium","status":"completed"}]}`
	geminiModeText             = `{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"[MODE_UPDATE] yolo"}}`
	geminiReplayedModeTool     = `{"sessionUpdate":"tool_call","toolCallId":"enter_plan_mode__replayed","status":"completed","title":"Enter Plan Mode","content":[],"kind":"other"}`
)

// newGeminiPeerAgent builds a Gemini agent that serves the replayed session,
// with the session-update hook and the mode channel that Start installs. Its
// fake peer answers each request. Before it answers a session/prompt, it
// dispatches duringPrompt, as Gemini streams a turn before its reply.
func newGeminiPeerAgent(t *testing.T, duringPrompt ...json.RawMessage) (*Agent, *agenttest.Sink) {
	t.Helper()
	sink := &agenttest.Sink{}
	a := &Agent{}
	a.SetSinkForTest(agent.NewProviderServices(sink))
	a.HooksForTest().SessionUpdateHandler = a.handleSessionUpdate
	a.HooksForTest().ModeChannel = acp.ModeChannelPermissionMode
	a.SetAvailableModesForTest(geminiModes())
	a.SetPermissionModeForTest(contracts.GeminiModeDefault)
	a.SetModelForTest("gemini-2.5-pro")
	acptest.NewAgentForRPCWithResponder(t, func() *Agent { return a }, func(a *Agent) *acp.Base { return &a.Base }, func(method string) agenttest.RPCReply {
		if method != acp.MethodSessionPrompt {
			return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
		}
		for _, params := range duringPrompt {
			a.HandleSessionUpdateForTest(params)
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{"stopReason":"end_turn"}`)}
	})
	a.SetSessionIDForTest(geminiReplaySessionID)
	return a, sink
}

// runGeminiPrompt sends one prompt and waits until its reply ended the turn.
func runGeminiPrompt(t *testing.T, a *Agent) {
	t.Helper()
	require.NoError(t, a.SendInput("Continue the resumed session.", nil))
	testutil.RequireEventually(t, func() bool { return !a.PromptActive() })
}

// geminiRows describes each stored row in order: an assembled text as `text:`
// or `thought:` and its text, a turn end as `turn_end:` and its tool count, and
// any other row as its sessionUpdate, with `:` and its tool call ID when it
// states one.
func geminiRows(t *testing.T, messages []agenttest.Message) []string {
	t.Helper()
	var rows []string
	for _, message := range messages {
		if message.TurnEnd {
			var metadata map[string]int
			require.NoError(t, json.Unmarshal(message.Metadata, &metadata))
			rows = append(rows, fmt.Sprintf("turn_end:%d", metadata[contracts.MessageMetadataFieldToolUses]))
			continue
		}
		var assembled map[string]string
		if json.Unmarshal(message.Content, &assembled) == nil && assembled[contracts.AssembledMessageFieldType] == contracts.AssembledMessageType {
			prefix := "text:"
			if assembled[contracts.AssembledMessageFieldKind] == contracts.AssembledMessageKindReasoning {
				prefix = "thought:"
			}
			rows = append(rows, prefix+assembled[contracts.AssembledMessageFieldText])
			continue
		}
		var update struct {
			SessionUpdate string `json:"sessionUpdate"`
			ToolCallID    string `json:"toolCallId"`
		}
		require.NoError(t, json.Unmarshal(message.Content, &update))
		row := update.SessionUpdate
		if update.ToolCallID != "" {
			row += ":" + update.ToolCallID
		}
		rows = append(rows, row)
	}
	return rows
}

// Gemini replays the history of a loaded session after the session/load reply,
// while no prompt runs, and nothing else of the conversation reaches LeapMux
// outside a prompt. The Worker copied the stored transcript into the resumed
// tab, so a replayed update must store nothing. Before this rule, the replayed
// answer joined the first answer of the next prompt in ONE row, and a replayed
// thought, tool call or plan stored a second copy beside the copied transcript.
func TestGeminiIdleConversationUpdatesStoreNothing(t *testing.T) {
	t.Parallel()
	for name, idle := range map[string][]json.RawMessage{
		"the replay of Gemini CLI 0.62.0": {
			geminiFrameParams(t, geminiReplayPrompt), geminiFrameParams(t, geminiReplayAnswer), geminiFrameParams(t, geminiReplayCommands),
		},
		"a replayed thought":                       {geminiReplayedUpdate(geminiThoughtUpdate)},
		"a replayed completed tool call":           {geminiReplayedUpdate(geminiToolCallUpdate)},
		"a replayed failed tool call":              {geminiReplayedUpdate(geminiFailedToolCall)},
		"a completed tool call update":             {geminiReplayedUpdate(geminiToolCallUpdateUpdate)},
		"a plan":                                   {geminiReplayedUpdate(geminiPlanUpdate)},
		"mode text that no tracked tool call owns": {geminiReplayedUpdate(geminiModeText)},
		"a replayed mode tool call":                {geminiReplayedUpdate(geminiReplayedModeTool)},
		"a thought, a tool call and an answer in order": {
			geminiReplayedUpdate(geminiThoughtUpdate), geminiReplayedUpdate(geminiToolCallUpdate), geminiFrameParams(t, geminiReplayAnswer),
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a, sink := newGeminiPeerAgent(t, geminiFrameParams(t, geminiNewAnswer))
			for _, params := range idle {
				a.HandleSessionUpdateForTest(params)
			}
			assert.Empty(t, geminiRows(t, sink.Messages()), "an idle update stores nothing")

			runGeminiPrompt(t, a)

			assert.Equal(t, []string{"text:OLDER_NATIVE_RESUME_CONFIRMED", "turn_end:0"}, geminiRows(t, sink.Messages()),
				"the prompt stores exactly its own answer and counts no replayed tool")
			a.modeMu.Lock()
			defer a.modeMu.Unlock()
			assert.Empty(t, a.modeCalls, "a replayed mode tool call is no call of the running turn")
		})
	}
}

// The same updates are the conversation of a turn while a prompt runs.
func TestGeminiConversationUpdatesDuringAPromptStillRender(t *testing.T) {
	t.Parallel()
	answer := geminiFrameParams(t, geminiNewAnswer)
	for name, tc := range map[string]struct {
		during []json.RawMessage
		rows   []string
	}{
		"an answer": {
			during: []json.RawMessage{answer},
			rows:   []string{"text:OLDER_NATIVE_RESUME_CONFIRMED", "turn_end:0"},
		},
		"a thought": {
			during: []json.RawMessage{geminiReplayedUpdate(geminiThoughtUpdate), answer},
			rows:   []string{"thought:**Reading the file**\nThe turn reads the file first.", "text:OLDER_NATIVE_RESUME_CONFIRMED", "turn_end:0"},
		},
		"a completed tool call": {
			during: []json.RawMessage{geminiReplayedUpdate(geminiToolCallUpdate), answer},
			rows:   []string{"tool_call:read_file__replayed", "text:OLDER_NATIVE_RESUME_CONFIRMED", "turn_end:1"},
		},
		"a completed tool call update": {
			during: []json.RawMessage{geminiReplayedUpdate(geminiToolCallUpdateUpdate), answer},
			rows:   []string{"tool_call_update:run_shell_command__updated", "text:OLDER_NATIVE_RESUME_CONFIRMED", "turn_end:1"},
		},
		"a plan": {
			during: []json.RawMessage{geminiReplayedUpdate(geminiPlanUpdate), answer},
			rows:   []string{"plan", "text:OLDER_NATIVE_RESUME_CONFIRMED", "turn_end:0"},
		},
		"mode text that no tracked tool call owns": {
			during: []json.RawMessage{geminiReplayedUpdate(geminiModeText)},
			rows:   []string{"text:[MODE_UPDATE] yolo", "turn_end:0"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a, sink := newGeminiPeerAgent(t, tc.during...)
			runGeminiPrompt(t, a)
			assert.Equal(t, tc.rows, geminiRows(t, sink.Messages()))
		})
	}
}

// When the model enters plan mode by itself, Gemini sends the tool call, the
// `[MODE_UPDATE] plan` text of the mode change, and the completed tool call, in
// the shapes of acpSession.ts runTool. The mode text is no answer of the model.
func TestGeminiOwnPlanModeTextStaysOutOfTheTranscript(t *testing.T) {
	t.Parallel()
	a, sink := newGeminiPeerAgent(t,
		geminiReplayedUpdate(`{"sessionUpdate":"tool_call","toolCallId":"enter_plan_mode__model-call","status":"in_progress","title":"Enter Plan Mode","content":[],"locations":[],"kind":"other"}`),
		geminiReplayedUpdate(`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"[MODE_UPDATE] plan"}}`),
		geminiReplayedUpdate(`{"sessionUpdate":"tool_call_update","toolCallId":"enter_plan_mode__model-call","status":"completed","title":"Enter Plan Mode","content":[],"locations":[],"kind":"other"}`),
		geminiFrameParams(t, geminiNewAnswer),
	)
	runGeminiPrompt(t, a)
	assert.Equal(t, []string{
		"tool_call:enter_plan_mode__model-call",
		"tool_call_update:enter_plan_mode__model-call",
		"text:OLDER_NATIVE_RESUME_CONFIRMED",
		"turn_end:1",
	}, geminiRows(t, sink.Messages()))
}

// An idle update that changes the state of the session is no conversation, and
// it still applies.
func TestGeminiIdleSessionStateUpdatesStillApply(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		update json.RawMessage
		// rows is what the idle update stores before the prompt.
		rows  []string
		check func(t *testing.T, a *Agent, sink *agenttest.Sink)
	}{
		"the command list": {
			update: geminiFrameParams(t, geminiReplayCommands),
			check: func(t *testing.T, a *Agent, _ *agenttest.Sink) {
				assert.True(t, a.HasAvailableCommand("memory"))
			},
		},
		"a mode update": {
			update: geminiReplayedUpdate(`{"sessionUpdate":"current_mode_update","currentModeId":"plan"}`),
			rows:   []string{"current_mode_update"},
			check: func(t *testing.T, a *Agent, _ *agenttest.Sink) {
				assert.Equal(t, contracts.GeminiModePlan, a.PermissionModeForTest())
			},
		},
		"a config option update": {
			update: geminiReplayedUpdate(`{"sessionUpdate":"config_option_update","configOptions":[{"id":"model","currentValue":"gemini-3.8-flash","options":[{"value":"gemini-2.5-pro"},{"value":"gemini-3.8-flash"}]}]}`),
			check: func(t *testing.T, a *Agent, _ *agenttest.Sink) {
				assert.Equal(t, "gemini-3.8-flash", a.ModelForTest())
			},
		},
		"a usage update": {
			update: geminiFrameParams(t, geminiUsageUpdate),
			check: func(t *testing.T, _ *Agent, sink *agenttest.Sink) {
				require.Equal(t, 1, sink.SessionInfoCount())
				usage, ok := sink.LastSessionInfo()[contracts.SessionInfoKeyContextUsage].(map[string]any)
				require.True(t, ok, "the usage update reports the context usage")
				assert.EqualValues(t, 1048576, usage[contracts.ContextUsageFieldContextWindow])
			},
		},
		"a session info update": {
			update: geminiReplayedUpdate(`{"sessionUpdate":"session_info_update","title":"Old title","updatedAt":"2026-10-03T00:00:00Z"}`),
			check:  func(*testing.T, *Agent, *agenttest.Sink) {},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a, sink := newGeminiPeerAgent(t, geminiFrameParams(t, geminiNewAnswer))
			a.HandleSessionUpdateForTest(tc.update)
			tc.check(t, a, sink)
			assert.Equal(t, tc.rows, geminiRows(t, sink.Messages()))

			runGeminiPrompt(t, a)

			assert.Equal(t, append(append([]string(nil), tc.rows...), "text:OLDER_NATIVE_RESUME_CONFIRMED", "turn_end:0"), geminiRows(t, sink.Messages()))
		})
	}
}

// The hook consumes an idle conversation update of the session that the agent
// serves, and nothing else. A state update, an update of another session, and
// each update while a prompt runs reach the base.
func TestGeminiSessionUpdateHandlerConsumesOnlyIdleConversation(t *testing.T) {
	t.Parallel()
	conversation := []string{
		`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"GEMINI_MOCK_REPLY"}}`,
		geminiThoughtUpdate, geminiToolCallUpdate, geminiFailedToolCall, geminiToolCallUpdateUpdate, geminiPlanUpdate, geminiModeText, geminiReplayedModeTool,
	}
	state := []string{
		`{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":"LEAPMUX_GEMINI_PROBE"}}`,
		`{"sessionUpdate":"available_commands_update","availableCommands":[{"name":"memory","description":"Manage memory."}]}`,
		`{"sessionUpdate":"current_mode_update","currentModeId":"plan"}`,
		`{"sessionUpdate":"config_option_update","configOptions":[]}`,
		`{"sessionUpdate":"usage_update","used":2,"size":1048576}`,
		`{"sessionUpdate":"session_info_update","title":"Old title"}`,
	}
	for _, update := range conversation {
		idle := &Agent{}
		idle.SetSessionIDForTest(geminiReplaySessionID)
		assert.True(t, idle.handleSessionUpdate(geminiReplaySessionID, nil, json.RawMessage(update)), "an idle update of the current session: %s", update)
		assert.False(t, idle.handleSessionUpdate("another-session", nil, json.RawMessage(update)), "an idle update of another session: %s", update)

		active := &Agent{}
		active.SetSessionIDForTest(geminiReplaySessionID)
		active.SetPromptActiveForTest(true)
		assert.False(t, active.handleSessionUpdate(geminiReplaySessionID, nil, json.RawMessage(update)), "an update while a prompt runs: %s", update)
	}
	for _, update := range state {
		idle := &Agent{}
		idle.SetSessionIDForTest(geminiReplaySessionID)
		assert.False(t, idle.handleSessionUpdate(geminiReplaySessionID, nil, json.RawMessage(update)), "an idle state update: %s", update)
	}
}
