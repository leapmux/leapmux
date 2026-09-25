//go:build unix

package kiro

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionids"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
)

const (
	kiroFakeRequestLog = "KIRO_FAKE_REQUEST_LOG"
	// kiroFakeModelEnv gives the model that a new session of the fake runs.
	kiroFakeModelEnv = "KIRO_FAKE_SESSION_MODEL"
	// kiroFakePromptErrorEnv makes the fake fail each session/prompt, as Kiro
	// fails a prompt that the model service throttles: a display error and an
	// error end of the turn, then a JSON-RPC error with this message.
	kiroFakePromptErrorEnv = "KIRO_FAKE_PROMPT_ERROR"
	// kiroFakeHoldCompactionEnv makes the fake leave each compaction
	// unanswered, as a Kiro whose summary call hangs does.
	kiroFakeHoldCompactionEnv = "KIRO_FAKE_HOLD_COMPACTION"
	// kiroFakeGoalRunEnv makes the fake's store hold one paused goal run of its
	// session, as a Kiro does after a restart that interrupted the goal.
	kiroFakeGoalRunEnv = "KIRO_FAKE_GOAL_RUN"
)

// kiroFakeGoalRunID is the goal run that the fake's store holds.
const kiroFakeGoalRunID = "wf_fake_goal"

// kiroInitializeFixture is Kiro's initialize response, reduced to what the
// start reads.
const kiroInitializeFixture = `{"protocolVersion":1,"agentCapabilities":{"loadSession":true,"promptCapabilities":{"image":true,"embeddedContext":true},"mcpCapabilities":{"http":true,"sse":true},"sessionCapabilities":{"list":{},"delete":{},"fork":{}},"_meta":{"kiro":{"sessionList":true,"extensionMethods":["_kiro/session/compact","_kiro/workflow/list","_kiro/workflow/inspect"]}}},"authMethods":[{"id":"aws-builder-id","name":"AWS Builder ID"}]}`

// kiroFakeSessionID is the session that the fake opens.
const kiroFakeSessionID = "sess_c7e2951e"

// kiroFakeConfigOptions is Kiro's config options of the session, as the probe
// recorded them against the mock catalog, with model the current model.
//
// effort is the current value of the effort axis, and "" when Kiro does not
// report the axis. Kiro reports the effort axis of a model only after a client
// writes the model: session/new, session/load and every config_option_update
// before the write omit it, although the model's own metadata states that it
// has an effort.
func kiroFakeConfigOptions(model, effort string) []any {
	option := func(value, name, description string, meta map[string]any) map[string]any {
		out := map[string]any{"value": value, "name": name, "description": description}
		if meta != nil {
			out["_meta"] = map[string]any{"kiro": meta}
		}
		return out
	}
	options := []any{
		map[string]any{
			"type": "select", "id": "mode", "name": "Mode", "category": "mode", "currentValue": "vibe",
			"options": []any{
				option("vibe", "Default", "General coding assistance", nil),
				option("plan", "Plan", "Plan-only mode", nil),
				option("semantic_reviewer", "semantic_reviewer", "Behavioral code review", nil),
			},
		},
		map[string]any{
			"type": "select", "id": "model", "name": "Model", "category": "model", "currentValue": model,
			"options": []any{
				option("auto", "auto", "Mock auto model", map[string]any{"rateMultiplier": 1, "rateUnit": "Credit", "hasEffort": false}),
				option("mock-sonnet", "mock-sonnet", "Mock model with effort", map[string]any{"rateMultiplier": 1.3, "rateUnit": "Credit", "hasEffort": true}),
			},
		},
	}
	if effort != "" {
		options = append(options, map[string]any{
			"type": "select", "id": "effortLevel", "name": "Effort", "category": "thought_level", "currentValue": effort,
			"options": []any{option("low", "Low", "", nil), option("high", "High", "", nil), option("max", "Max", "", nil)},
		})
	}
	return append(options,
		map[string]any{
			"type": "select", "id": "autopilot", "name": "Autopilot", "currentValue": "on",
			"options": []any{
				option("on", "Autopilot", "Agent executes tools without confirmation", nil),
				option("off", "Supervised", "Agent asks for approval before file changes", nil),
			},
		},
		map[string]any{
			"type": "select", "id": "contentCollection", "name": "Content Collection", "currentValue": "enabled",
			"options": []any{option("enabled", "Enabled", "", nil), option("disabled", "Disabled", "", nil)},
		},
	)
}

// kiroFakeSession is Kiro's session/new or session/load response, which never
// states the effort axis.
func kiroFakeSession(model string) map[string]any {
	return map[string]any{
		"sessionId": kiroFakeSessionID,
		"modes": map[string]any{
			"currentModeId": "vibe",
			"availableModes": []any{
				map[string]any{"id": "vibe", "name": "Default", "description": "General coding assistance"},
				map[string]any{"id": "plan", "name": "Plan", "description": "Plan-only mode"},
				map[string]any{"id": "semantic_reviewer", "name": "semantic_reviewer", "description": "Behavioral code review"},
			},
		},
		"configOptions": kiroFakeConfigOptions(model, ""),
	}
}

// TestHelperProcessKiroCLI is the fake Kiro: it records each request line and
// answers the handshake and each config change.
func TestHelperProcessKiroCLI(*testing.T) {
	if os.Getenv(kiroFakeCLIEnv) != "1" {
		return
	}
	log, err := os.OpenFile(os.Getenv(kiroFakeRequestLog), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		os.Exit(2)
	}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	writer := bufio.NewWriter(os.Stdout)
	model := os.Getenv(kiroFakeModelEnv)
	if model == "" {
		model = "auto"
	}
	// effort is "" until a model write makes Kiro report the axis. Only
	// mock-sonnet has an effort, and a model write starts it on the model's
	// default.
	effort := ""
	for scanner.Scan() {
		_, _ = log.Write(append(append([]byte(nil), scanner.Bytes()...), '\n'))
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				ConfigID string `json:"configId"`
				Value    string `json:"value"`
			} `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &req) != nil || len(req.ID) == 0 {
			continue
		}
		var result any = map[string]any{}
		switch req.Method {
		case acp.MethodSessionPrompt:
			if message := os.Getenv(kiroFakePromptErrorEnv); message != "" {
				failFakePrompt(writer, req.ID, message)
				continue
			}
		case kiroCompactMethod:
			if os.Getenv(kiroFakeHoldCompactionEnv) != "" {
				continue
			}
		case acp.MethodInitialize:
			result = json.RawMessage(kiroInitializeFixture)
		case acp.MethodSessionNew, acp.MethodSessionLoad:
			effort = ""
			result = kiroFakeSession(model)
		case acp.MethodSessionSetConfigOption:
			switch req.Params.ConfigID {
			case "model":
				model = req.Params.Value
				effort = ""
				if model == "mock-sonnet" {
					effort = "high"
				}
			case contracts.KiroConfigEffortLevel:
				// Kiro does not validate a value, and an axis that it does not
				// report keeps no value.
				if effort != "" {
					effort = req.Params.Value
				}
			}
			result = map[string]any{"configOptions": kiroFakeConfigOptions(model, effort)}
		case kiroWorkflowListMethod:
			runs := []any{}
			if os.Getenv(kiroFakeGoalRunEnv) != "" {
				runs = append(runs, map[string]any{
					"workflowId": kiroFakeGoalRunID, "workflowName": kiroGoalWorkflowName, "status": kiroRunPaused,
					"updatedAt": "2026-09-24T04:00:00.000Z", "parentSessionId": kiroFakeSessionID,
				})
			}
			result = map[string]any{"runs": runs}
		case kiroWorkflowInspectMethod:
			result = map[string]any{"workflowId": kiroFakeGoalRunID, "state": map[string]any{
				"status": kiroRunPaused, "inputs": map[string]any{"prompt": "Ship the release"},
				"pauseReason": "Repeat 'goal-loop' reached maxIterations.",
			}}
		}
		encoded, err := json.Marshal(result)
		if err != nil {
			os.Exit(3)
		}
		_, _ = fmt.Fprintf(writer, `{"jsonrpc":"2.0","id":%s,"result":%s}`+"\n", req.ID, encoded)
		_ = writer.Flush()
	}
	_ = log.Close()
	os.Exit(0)
}

// kiroFakeDisplayError is the display error of a prompt that the fake fails, as
// the probe recorded Kiro's text for a throttled model call.
const kiroFakeDisplayError = "Too many requests, please wait before trying again. (Request ID: c791fc4e)"

// failFakePrompt writes Kiro's failure of one prompt, in the probed order: the
// turn starts, Kiro states the display error, the turn ends with the stop
// reason error, and the prompt answers a JSON-RPC error.
func failFakePrompt(writer *bufio.Writer, id json.RawMessage, message string) {
	update := func(kiro map[string]any) {
		encoded, _ := json.Marshal(map[string]any{
			"jsonrpc": "2.0", "method": "session/update",
			"params": map[string]any{"sessionId": kiroFakeSessionID, "update": map[string]any{
				"sessionUpdate": "session_info_update", "_meta": map[string]any{"kiro": kiro},
			}},
		})
		_, _ = writer.Write(append(encoded, '\n'))
	}
	update(map[string]any{"turnStart": true, "kind": kiroKindTurnStart})
	update(map[string]any{
		"displayError": map[string]any{"message": kiroFakeDisplayError, "errorType": "ServiceThrottleError", "retryErrorType": "THROTTLING"},
		"kind":         kiroKindDisplayError, "message": kiroFakeDisplayError,
	})
	update(map[string]any{"turnEnd": map[string]any{"stopReason": kiroStopError}, "kind": contracts.KiroKindTurnEnd, "stopReason": kiroStopError})
	failure, _ := json.Marshal(map[string]any{"code": -32000, "message": message, "data": map[string]any{"errorType": "ServiceThrottleError"}})
	_, _ = fmt.Fprintf(writer, `{"jsonrpc":"2.0","id":%s,"error":%s}`+"\n", id, failure)
	_ = writer.Flush()
}

// startFakeKiro starts the provider against the fake Kiro, whose new session
// runs the model "auto", and returns the agent, the requests that the fake
// recorded, and the launch arguments.
func startFakeKiro(t *testing.T, opts agent.Options) (*Agent, func() []agenttest.RecordedRequest, string) {
	t.Helper()
	return startFakeKiroOn(t, "auto", opts)
}

// startFakeKiroOn is startFakeKiro with the model that a new session of the
// fake runs.
func startFakeKiroOn(t *testing.T, sessionModel string, opts agent.Options) (*Agent, func() []agenttest.RecordedRequest, string) {
	t.Helper()
	a, _, requests, args := startFakeKiroWith(t, sessionModel, nil, opts)
	return a, requests, args
}

// startFakeKiroWith is startFakeKiroOn with more environment for the fake, and
// it returns the sink of the agent too.
func startFakeKiroWith(t *testing.T, sessionModel string, fakeEnv []string, opts agent.Options) (*Agent, *agenttest.Sink, func() []agenttest.RecordedRequest, string) {
	t.Helper()
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args.txt")
	logFile := filepath.Join(dir, "requests.jsonl")
	agenttest.InstallFakeCLI(t, agenttest.FakeCLI{
		Binary:    kiroBinary,
		HelperRun: "TestHelperProcessKiroCLI",
		WantEnv:   kiroFakeCLIEnv,
		ArgsFile:  argsFile,
		Env:       append([]string{kiroFakeRequestLog + "=" + logFile, kiroFakeModelEnv + "=" + sessionModel}, fakeEnv...),
	})
	opts.AgentID = "kiro-agent"
	opts.WorkingDir = t.TempDir()
	opts.Shell = testutil.TestShell()
	opts.AgentProvider = leapmuxv1.AgentProvider_AGENT_PROVIDER_KIRO
	sink := &agenttest.Sink{}
	provider, err := Start(context.Background(), opts, agent.NewProviderServices(sink))
	require.NoError(t, err)
	a := provider.(*Agent)
	t.Cleanup(func() {
		a.Stop()
		_ = a.Wait()
	})
	requests := func() []agenttest.RecordedRequest {
		data, err := os.ReadFile(logFile)
		require.NoError(t, err)
		var out []agenttest.RecordedRequest
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			var req struct {
				Method string         `json:"method"`
				Params map[string]any `json:"params"`
			}
			if json.Unmarshal([]byte(line), &req) == nil {
				out = append(out, agenttest.RecordedRequest{Method: req.Method, Params: req.Params, Raw: line})
			}
		}
		return out
	}
	args, err := os.ReadFile(argsFile)
	require.NoError(t, err)
	return a, sink, requests, strings.TrimSpace(string(args))
}

// agentErrorTexts returns the error of every agent error notification that a
// plain sink recorded.
func agentErrorTexts(sink *agenttest.Sink) []string {
	var out []string
	for _, notification := range sink.LeapMuxNotifications() {
		if notification[contracts.NotificationFieldType] == contracts.NotificationTypeAgentError {
			text, _ := notification[contracts.NotificationFieldError].(string)
			out = append(out, text)
		}
	}
	return out
}

// A Kiro process that exits during a compaction answers nothing. The request
// then fails, which drops the compaction and ends its turn, and a stopped
// agent states no error.
func TestStartKiroCompactionOfAStoppedAgentEndsQuietly(t *testing.T) {
	a, sink, _, _ := startFakeKiroWith(t, "auto", []string{kiroFakeHoldCompactionEnv + "=1"}, agent.Options{})
	require.NoError(t, a.CompactContext())
	require.True(t, a.PromptActive())

	a.Stop()
	_ = a.Wait()

	require.Eventually(t, func() bool {
		a.stateMu.Lock()
		defer a.stateMu.Unlock()
		return a.compaction.id == 0
	}, 30*time.Second, time.Millisecond)
	assert.False(t, a.PromptActive())
	assert.Empty(t, agentErrorTexts(sink))
}

// A prompt that the model service throttles ends with a JSON-RPC error that
// states the display error's text, as the probe showed. The transcript states
// the reason once. A prompt whose error states another text keeps the display
// error beside it.
func TestStartKiroStatesTheReasonOfAFailedPrompt(t *testing.T) {
	for _, tc := range []struct {
		name         string
		promptError  string
		wantMessages []string
	}{
		{
			name:         "the probed error",
			promptError:  kiroFakeDisplayError,
			wantMessages: []string{"prompt failed: json-rpc error -32000: " + kiroFakeDisplayError},
		},
		{
			name:         "another error",
			promptError:  "Internal error",
			wantMessages: []string{kiroFakeDisplayError, "prompt failed: json-rpc error -32000: Internal error"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, sink, _, _ := startFakeKiroWith(t, "auto", []string{kiroFakePromptErrorEnv + "=" + tc.promptError}, agent.Options{})

			require.NoError(t, a.SendInput("Say hello.", nil))
			require.Eventually(t, func() bool { return !a.PromptActive() }, 30*time.Second, 10*time.Millisecond)

			assert.Equal(t, tc.wantMessages, agentErrorTexts(sink))
		})
	}
}

func TestStartKiroRunsTheV3EngineWithTheCLIAuthentication(t *testing.T) {
	a, requests, args := startFakeKiro(t, agent.Options{})

	assert.Equal(t, "acp --agent-engine v3 --auth-method cli", args)
	assert.Equal(t, kiroFakeSessionID, a.SessionIDForTest())

	initialize := requestsFor(requests(), acp.MethodInitialize)
	require.Len(t, initialize, 1)
	capabilities := initialize[0].Params["clientCapabilities"].(map[string]any)
	assert.Equal(t, false, capabilities["terminal"], "Kiro runs its own shell commands")
	assert.Equal(t, map[string]any{"kiro": map[string]any{
		"userInput":             true,
		"hooks":                 map[string]any{"enabled": true, "v2": true},
		"streamingShellContent": true,
		"settings": map[string]any{
			"goal":      map[string]any{"enabled": true},
			"workflows": map[string]any{"enabled": true},
			"todoList":  map[string]any{"enabled": true},
		},
	}}, capabilities["_meta"])
}

func TestStartKiroStatesNoPresetForTheAskPolicy(t *testing.T) {
	_, requests, _ := startFakeKiro(t, agent.Options{})

	sessions := requestsFor(requests(), acp.MethodSessionNew)
	require.Len(t, sessions, 1)
	assert.NotContains(t, sessions[0].Params, "_meta", "Kiro's own rules decide")
}

func TestStartKiroStatesThePolicyPresetInTheSession(t *testing.T) {
	a, requests, _ := startFakeKiro(t, agent.Options{Options: map[string]string{contracts.KiroOptionPolicyPreset: contracts.KiroPolicyPresetAllowAll}})

	sessions := requestsFor(requests(), acp.MethodSessionNew)
	require.Len(t, sessions, 1)
	assert.Equal(t, map[string]any{"kiro": map[string]any{"policyPreset": []any{contracts.KiroPolicyPresetAllowAll}}}, sessions[0].Params["_meta"])
	assert.Equal(t, contracts.KiroPolicyPresetAllowAll, agent.CurrentOptions(a.OptionGroups())[contracts.KiroOptionPolicyPreset])
}

func TestStartKiroLoadsWithoutAReplay(t *testing.T) {
	_, requests, _ := startFakeKiro(t, agent.Options{ResumeSessionID: kiroFakeSessionID})

	assert.Empty(t, requestsFor(requests(), acp.MethodSessionNew))
	loads := requestsFor(requests(), acp.MethodSessionLoad)
	require.Len(t, loads, 1)
	assert.Equal(t, kiroFakeSessionID, loads[0].Params["sessionId"])
	assert.Equal(t, map[string]any{"kiro": map[string]any{"noReplay": true}}, loads[0].Params["_meta"],
		"LeapMux keeps its own transcript, and a replay would draw it twice")
}

// A resumed session can hold a goal run in Kiro's own store. The start finds
// it and restates the goal, so the goal card and its controls are back before
// any notification of the run arrives.
func TestStartKiroRestoresTheGoalRunOfAResumedSession(t *testing.T) {
	a, sink, requests, _ := startFakeKiroWith(t, "auto", []string{kiroFakeGoalRunEnv + "=1"}, agent.Options{ResumeSessionID: kiroFakeSessionID})

	require.Eventually(t, func() bool { return len(sink.Goals()) == 1 }, 30*time.Second, 10*time.Millisecond)
	goal, _ := sink.LastGoal()
	assert.Equal(t, kiroFakeGoalRunID, goal.NativeID)
	assert.Equal(t, "Ship the release", goal.Objective)
	assert.Equal(t, agent.GoalStatusPaused, goal.Status)
	assert.Equal(t, "Repeat 'goal-loop' reached maxIterations.", goal.StatusDetail)
	assert.True(t, goal.Snapshot)
	assert.Len(t, a.SupportedGoalActions(), 4, "the run's controls are back")
	lists := requestsFor(requests(), kiroWorkflowListMethod)
	require.Len(t, lists, 1)
	assert.Equal(t, map[string]any{"sessionId": kiroFakeSessionID}, lists[0].Params)
}

func TestStartKiroReadsTheHandshake(t *testing.T) {
	a, _, _ := startFakeKiro(t, agent.Options{})

	current := agent.CurrentOptions(a.OptionGroups())
	assert.Equal(t, contracts.KiroModeDefault, current[agent.OptionIDPermissionMode])
	assert.Equal(t, kiroPolicyAsk, current[contracts.KiroOptionPolicyPreset])
	assert.Equal(t, "on", current[kiroConfigAutopilot], "the base surfaces the autopilot axis")
	assert.Equal(t, "enabled", current[kiroConfigContentCollection])

	modes := optionids.GroupByID(a.OptionGroups(), agent.OptionIDPermissionMode)
	require.NotNil(t, modes)
	var modeIDs []string
	for _, option := range modes.GetOptions() {
		modeIDs = append(modeIDs, option.GetId())
	}
	assert.Equal(t, []string{"vibe", "plan", "semantic_reviewer"}, modeIDs, "the session's own list replaces the static one")

	descriptions := map[string]string{}
	for _, model := range a.AvailableModelsForTest() {
		descriptions[model.Id] = model.Description
	}
	assert.Equal(t, map[string]string{
		"auto":        "Mock auto model (1x credit rate)",
		"mock-sonnet": "Mock model with effort (1.3x credit rate)",
	}, descriptions)
}

// configWrites lists the session/set_config_option requests as "id=value", in
// the order that the fake received them.
func configWrites(requests []agenttest.RecordedRequest) []string {
	var configs []string
	for _, request := range requestsFor(requests, acp.MethodSessionSetConfigOption) {
		configs = append(configs, fmt.Sprintf("%v=%v", request.Params["configId"], request.Params["value"]))
	}
	return configs
}

func TestStartKiroAppliesTheRequestedModelAndItsEffort(t *testing.T) {
	a, requests, _ := startFakeKiro(t, agent.Options{Options: map[string]string{
		agent.OptionIDModel:  "mock-sonnet",
		agent.OptionIDEffort: "max",
	}})

	assert.Equal(t, []string{"model=mock-sonnet", contracts.KiroConfigEffortLevel + "=max"}, configWrites(requests()))
	assert.Equal(t, "mock-sonnet", a.ModelForTest())
	assert.Equal(t, "max", agent.CurrentOptions(a.OptionGroups())[contracts.KiroConfigEffortLevel])
}

// Kiro reports the effort axis of the session's model only after a client
// writes the model. The start therefore writes the model that the session
// already runs, and the requested effort then has an axis to apply to.
func TestStartKiroRevealsTheEffortOfTheSessionModel(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts agent.Options
	}{
		{name: "no model requested", opts: agent.Options{Options: map[string]string{agent.OptionIDEffort: "max"}}},
		{name: "the session model requested", opts: agent.Options{Options: map[string]string{
			agent.OptionIDModel:  "mock-sonnet",
			agent.OptionIDEffort: "max",
		}}},
		{name: "a resumed session", opts: agent.Options{ResumeSessionID: kiroFakeSessionID, Options: map[string]string{agent.OptionIDEffort: "max"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, requests, _ := startFakeKiroOn(t, "mock-sonnet", tc.opts)

			assert.Equal(t, []string{"model=mock-sonnet", contracts.KiroConfigEffortLevel + "=max"}, configWrites(requests()),
				"the model write comes first, so the effort axis exists when the effort applies")
			assert.Equal(t, "max", agent.CurrentOptions(a.OptionGroups())[contracts.KiroConfigEffortLevel])
		})
	}
}

// A context clear opens a new session, which omits the effort axis again. The
// reapply writes the model first, so the effort that the reader chose has its
// axis back when it applies.
func TestKiroClearContextKeepsTheChosenEffort(t *testing.T) {
	a, requests, _ := startFakeKiroOn(t, "mock-sonnet", agent.Options{Options: map[string]string{agent.OptionIDEffort: "max"}})
	before := len(configWrites(requests()))

	_, err := a.ClearContext()
	require.NoError(t, err)

	// The reapply writes each stored option again, in the order of its ids, after
	// the model.
	assert.Equal(t, []string{
		"model=mock-sonnet",
		kiroConfigAutopilot + "=on",
		kiroConfigContentCollection + "=enabled",
		contracts.KiroConfigEffortLevel + "=max",
	}, configWrites(requests())[before:])
	assert.Equal(t, "max", agent.CurrentOptions(a.OptionGroups())[contracts.KiroConfigEffortLevel],
		"the session/new response omits the axis, and the refresh keeps what the model write revealed")
}

func TestStartKiroKeepsKirosEffortWhenNoneIsRequested(t *testing.T) {
	a, requests, _ := startFakeKiroOn(t, "mock-sonnet", agent.Options{})

	assert.Equal(t, []string{"model=mock-sonnet"}, configWrites(requests()))
	assert.Equal(t, "high", agent.CurrentOptions(a.OptionGroups())[contracts.KiroConfigEffortLevel],
		"the effort axis shows Kiro's own default")
}

func TestStartKiroOnAModelWithNoEffortShowsNoEffortAxis(t *testing.T) {
	a, requests, _ := startFakeKiro(t, agent.Options{Options: map[string]string{agent.OptionIDEffort: "max"}})

	assert.Equal(t, []string{"model=auto"}, configWrites(requests()),
		"no effort axis exists, so no effort write goes out")
	assert.NotContains(t, agent.CurrentOptions(a.OptionGroups()), contracts.KiroConfigEffortLevel)
}
