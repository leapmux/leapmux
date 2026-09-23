//go:build unix

package goose

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionids"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp/acptest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

func newGooseAgentForRPC(t *testing.T) (*Agent, func() []agenttest.RecordedRequest) {
	return acptest.NewAgentForRPC(t,
		func() *Agent {
			a := &Agent{}
			a.HooksForTest().ModeChannel = acp.ModeChannelPermissionMode
			return a
		},
		func(a *Agent) *acp.Base { return &a.Base },
	)
}

// gooseToolOutputTail reads the state that the production observation returns.
// It stays in this Unix-only test file because no production caller needs it.
func gooseToolOutputTail(a *Agent, toolCallID string) (string, bool) {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	state := a.gooseOutput[toolCallID]
	return state.tail, state.tailLostBytes
}

func TestGooseToolOutputProgressCountsSequencedMetadata(t *testing.T) {
	t.Parallel()

	a := &Agent{}
	update := acp.ToolCallUpdateEnvelope{ToolCallID: "tool-1", Meta: json.RawMessage(`{
		"toolNotification":{"type":"live_output","params":{"sequence":1,"truncated":false,
		"chunks":[{"stream":"stdout","output":"hello"},{"stream":"stderr","output":"error"}]}}
	}`)}
	out, ok := a.gooseToolOutput(update)
	require.True(t, ok)
	assert.Equal(t, int64(10), out.Total)
	assert.False(t, out.TotalIsMinimum)
	// ONE observation: the count and the text a row draws leave the same read.
	assert.Equal(t, "helloerror", out.Tail)

	_, ok = a.gooseToolOutput(update)
	assert.False(t, ok, "a repeated sequence must not count twice")
}

// Goose sends one live_output notification per CHUNK, so only this agent knows
// where the earlier ones ended. The running row draws everything the call has
// printed so far, which is what this joins.
func TestGooseToolOutputTailJoinsTheChunksItCounted(t *testing.T) {
	t.Parallel()

	a := &Agent{}
	tail, truncated := gooseToolOutputTail(a, "tool-1")
	assert.Equal(t, "", tail)
	assert.False(t, truncated)

	first := acp.ToolCallUpdateEnvelope{ToolCallID: "tool-1", Meta: json.RawMessage(`{
		"toolNotification":{"type":"live_output","params":{"sequence":1,"truncated":false,
		"chunks":[{"stream":"stdout","output":"first\n"}]}}
	}`)}
	second := acp.ToolCallUpdateEnvelope{ToolCallID: "tool-1", Meta: json.RawMessage(`{
		"toolNotification":{"type":"live_output","params":{"sequence":2,"truncated":true,
		"chunks":[{"stream":"stdout","output":"second\n"}]}}
	}`)}
	_, ok := a.gooseToolOutput(first)
	require.True(t, ok)
	tail, truncated = gooseToolOutputTail(a, "tool-1")
	assert.Equal(t, "first\n", tail)
	assert.False(t, truncated)

	_, ok = a.gooseToolOutput(second)
	require.True(t, ok)
	tail, truncated = gooseToolOutputTail(a, "tool-1")
	assert.Equal(t, "first\nsecond\n", tail)
	assert.True(t, truncated, "the provider said it dropped output before this chunk")

	// The call ends, and its live text ends with it.
	a.clearGooseToolOutput("tool-1")
	tail, _ = gooseToolOutputTail(a, "tool-1")
	assert.Equal(t, "", tail)
}

// The joined text cannot grow without limit for a command that prints for
// minutes. The cap keeps the END, which is what a reader watches.
func TestGooseToolOutputTailKeepsTheEndWhenItGrowsPastTheCap(t *testing.T) {
	t.Parallel()

	a := &Agent{}
	chunk := strings.Repeat("x", gooseLiveOutputLimit)
	for sequence, text := range []string{chunk, "tail-marker"} {
		update := acp.ToolCallUpdateEnvelope{ToolCallID: "tool-1", Meta: json.RawMessage(fmt.Sprintf(`{
			"toolNotification":{"type":"live_output","params":{"sequence":%d,"truncated":false,
			"chunks":[{"stream":"stdout","output":%q}]}}
		}`, sequence+1, text))}
		_, ok := a.gooseToolOutput(update)
		require.True(t, ok)
	}
	tail, truncated := gooseToolOutputTail(a, "tool-1")
	assert.LessOrEqual(t, len(tail), gooseLiveOutputLimit)
	assert.True(t, strings.HasSuffix(tail, "tail-marker"))
	assert.True(t, truncated)
}

func newGooseAgentForRPCWithResponder(t *testing.T, respond func(method string) agenttest.RPCReply) (*Agent, func() []agenttest.RecordedRequest) {
	return acptest.NewAgentForRPCWithResponder(t,
		func() *Agent {
			a := &Agent{}
			a.HooksForTest().ModeChannel = acp.ModeChannelPermissionMode
			return a
		},
		func(a *Agent) *acp.Base { return &a.Base },
		respond,
	)
}

func installFakeGooseCLI(t *testing.T, scenario string) {
	agenttest.InstallFakeCLI(t, agenttest.FakeCLI{
		Binary:    "goose",
		HelperRun: "TestHelperProcessGooseCLI",
		WantEnv:   "GO_WANT_HELPER_PROCESS_GOOSE",
		Env:       []string{"LEAPMUX_GOOSE_TEST_SCENARIO=" + scenario},
	})
}

func TestHelperProcessGooseCLI(*testing.T) {
	scenario := os.Getenv("LEAPMUX_GOOSE_TEST_SCENARIO")
	agenttest.ServeFakeJSONRPC("GO_WANT_HELPER_PROCESS_GOOSE", func(method string) (string, bool, bool) {
		switch method {
		case acp.MethodInitialize:
			return `{"protocolVersion":1,"agentCapabilities":{"loadSession":true}}`, false, true
		case acp.MethodSessionNew:
			if scenario == "new-refused" {
				return `{"code":-32000,"message":"workspace is not readable"}`, true, true
			}
			return `{"sessionId":"goose-new","models":{"currentModelId":"default-model","availableModels":[{"modelId":"default-model","name":"Default Model","description":"Default"},{"modelId":"fast-model","name":"Fast Model","description":"Fast"}]},"modes":{"currentModeId":"auto","availableModes":[{"id":"auto","name":"Auto"},{"id":"approve","name":"Approve"},{"id":"smart_approve","name":"Smart Approve"},{"id":"chat","name":"Chat"}]},"configOptions":[{"id":"mode","currentValue":"auto","options":[{"value":"auto","name":"Auto"},{"value":"approve","name":"Approve"},{"value":"smart_approve","name":"Smart Approve"},{"value":"chat","name":"Chat"}]},{"id":"model","currentValue":"default-model","options":[{"value":"default-model","name":"Default Model"},{"value":"fast-model","name":"Fast Model"}]}]}`, false, true
		case acp.MethodSessionLoad:
			if scenario == "load-refused" {
				return `{"code":-32000,"message":"session not found"}`, true, true
			}
			if scenario == "load" {
				return `{"models":{"currentModelId":"fast-model","availableModels":[{"modelId":"fast-model","name":"Fast Model"}]},"modes":{"currentModeId":"approve","availableModes":[{"id":"auto","name":"Auto"},{"id":"approve","name":"Approve"},{"id":"smart_approve","name":"Smart Approve"},{"id":"chat","name":"Chat"}]}}`, false, true
			}
			return "", false, false
		case acp.MethodSessionSetConfigOption, acp.MethodSessionSetModel, acp.MethodSessionSetMode, acp.MethodSessionPrompt:
			return `{}`, false, true
		default:
			return "", false, false
		}
	})
}

func TestStartGooseCLI_NewSessionHandshake(t *testing.T) {
	installFakeGooseCLI(t, "new")

	provider, err := Start(context.Background(), agent.Options{
		AgentID:       "goose-new",
		WorkingDir:    t.TempDir(),
		Shell:         testutil.TestShell(),
		LoginShell:    false,
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE,
	}, agent.NewProviderServices(&agenttest.Sink{}))
	require.NoError(t, err)

	a := provider.(*Agent)
	t.Cleanup(func() {
		a.Stop()
		_ = a.Wait()
	})

	assert.Equal(t, "goose-new", a.SessionIDForTest())
	assert.Equal(t, "default-model", a.ModelForTest())
	assert.Equal(t, contracts.GooseModeAuto, a.PermissionModeForTest())
	require.Len(t, a.AvailableModelsForTest(), 2)
	assert.Equal(t, "default-model", a.AvailableModelsForTest()[0].GetId())
	groups := a.OptionGroups()
	modeGroup := optionids.GroupByID(groups, agent.OptionIDPermissionMode)
	require.NotNil(t, modeGroup)
	// Verify mode names are capitalized (e.g. "smart_approve" → "Smart Approve").
	modeNames := make([]string, 0, len(modeGroup.GetOptions()))
	for _, opt := range modeGroup.GetOptions() {
		modeNames = append(modeNames, opt.GetName())
	}
	assert.Equal(t, []string{"Smart Approve", "Auto", "Approve", "Chat"}, modeNames)
}

func TestFallbackGooseModesPutSmartApproveFirst(t *testing.T) {
	t.Parallel()

	modes := fallbackGooseCLIModes()
	require.NotEmpty(t, modes)
	assert.Equal(t, contracts.GooseModeSmartApprove, modes[0].GetId())
	// The rest keep Goose's own order, so the fallback and a live catalog agree.
	rest := make([]string, 0, len(modes)-1)
	for _, mode := range modes[1:] {
		rest = append(rest, mode.GetId())
	}
	assert.Equal(t, []string{contracts.GooseModeAuto, contracts.GooseModeApprove, contracts.GooseModeChat}, rest)
}

// acp.OrderModesPreferredFirst is the one ordering rule every rebuilt permission list uses,
// so its edges decide what the picker shows and which option position 0 badges as the
// group default.
func TestOrderModesPreferredFirst(t *testing.T) {
	t.Parallel()

	ids := func(modes []*leapmuxv1.AvailableOption) []string {
		out := make([]string, 0, len(modes))
		for _, mode := range modes {
			out = append(out, mode.GetId())
		}
		return out
	}
	modes := func(values ...string) []*leapmuxv1.AvailableOption {
		out := make([]*leapmuxv1.AvailableOption, 0, len(values))
		for _, value := range values {
			out = append(out, &leapmuxv1.AvailableOption{Id: value})
		}
		return out
	}

	cases := []struct {
		name      string
		in        []*leapmuxv1.AvailableOption
		preferred string
		want      []string
	}{
		{"moves the preferred mode to the front", modes("a", "b", "c"), "c", []string{"c", "a", "b"}},
		{"leaves a list already led by it alone", modes("c", "a", "b"), "c", []string{"c", "a", "b"}},
		{"leaves a list that omits it alone", modes("a", "b"), "c", []string{"a", "b"}},
		{"orders nothing without a preferred mode", modes("a", "b"), "", []string{"a", "b"}},
		{"handles an empty list", modes(), "c", []string{}},
		// A server that reports the id twice must not rotate the others: both copies
		// land at the front and every other mode keeps its reported position.
		{"keeps the order under a duplicate", modes("c", "a", "c", "b"), "c", []string{"c", "c", "a", "b"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			acp.OrderModesPreferredFirst(tc.in, tc.preferred)
			assert.Equal(t, tc.want, ids(tc.in))
		})
	}
}

func TestStartGooseCLI_LoadSessionUsesResumeID(t *testing.T) {
	installFakeGooseCLI(t, "load")

	provider, err := Start(context.Background(), agent.Options{
		AgentID:         "goose-load",
		WorkingDir:      t.TempDir(),
		ResumeSessionID: "goose-resume",
		Shell:           testutil.TestShell(),
		LoginShell:      false,
		AgentProvider:   leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE,
	}, agent.NewProviderServices(&agenttest.Sink{}))
	require.NoError(t, err)

	agent := provider.(*Agent)
	t.Cleanup(func() {
		agent.Stop()
		_ = agent.Wait()
	})

	assert.Equal(t, "goose-resume", agent.SessionIDForTest())
	assert.Equal(t, "fast-model", agent.ModelForTest())
	assert.Equal(t, contracts.GooseModeApprove, agent.PermissionModeForTest())
}

// A resume the agent refuses fails the whole start, for every ACP provider:
// startACPAgent is the one handshake behind all of them. It used to answer a
// refused session/load with session/new, which opened an EMPTY session and
// reported success -- the user got a tab with no history and no report of why.
func TestStartGooseCLI_RefusedResumeFailsTheStart(t *testing.T) {
	installFakeGooseCLI(t, "load-refused")

	provider, err := Start(context.Background(), agent.Options{
		AgentID:         "goose-load-refused",
		WorkingDir:      t.TempDir(),
		ResumeSessionID: "goose-resume",
		Shell:           testutil.TestShell(),
		LoginShell:      false,
		AgentProvider:   leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE,
	}, agent.NewProviderServices(&agenttest.Sink{}))
	require.Error(t, err)
	assert.Nil(t, provider, "a start that fails hands back no agent to talk to")
	assert.Contains(t, err.Error(), "goose-resume", "the handle that failed is what the user has to replace")
	assert.Contains(t, err.Error(), "session not found", "the agent's own reason must reach the tab")
	assert.Contains(t, err.Error(), "/clear", "the failure must state the command that recovers the tab")
}

// A start that carries no resume handle must not report a resume failure. The
// wrap is keyed on the handle, which is also what picks session/load over
// session/new, so the two can never disagree.
func TestStartGooseCLI_RefusedNewSessionIsNotReportedAsAResume(t *testing.T) {
	installFakeGooseCLI(t, "new-refused")

	provider, err := Start(context.Background(), agent.Options{
		AgentID:       "goose-new-refused",
		WorkingDir:    t.TempDir(),
		Shell:         testutil.TestShell(),
		LoginShell:    false,
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE,
	}, agent.NewProviderServices(&agenttest.Sink{}))
	require.Error(t, err)
	assert.Nil(t, provider)
	assert.Contains(t, err.Error(), "workspace is not readable")
	assert.NotContains(t, err.Error(), "could not resume", "nothing was asked to resume")
	assert.NotContains(t, err.Error(), "/clear", "`/clear` restarts on a fresh session, which is what already failed")
}

func TestGooseUpdateSettingsSendsLiveACPRequests(t *testing.T) {
	a, requests := newGooseAgentForRPC(t)
	a.SetAvailableModesForTest([]*leapmuxv1.AvailableOption{
		{Id: contracts.GooseModeAuto, Name: "Auto"},
		{Id: contracts.GooseModeApprove, Name: "Approve"},
	})

	updated := a.UpdateSettings(map[string]string{
		agent.OptionIDModel:          "fast-model",
		agent.OptionIDPermissionMode: contracts.GooseModeApprove,
	})
	require.True(t, updated.AppliedLive)
	assert.Equal(t, "fast-model", a.ModelForTest())
	assert.Equal(t, contracts.GooseModeApprove, a.PermissionModeForTest())

	recorded := requests()
	require.Len(t, recorded, 2)
	assert.Equal(t, acp.MethodSessionSetConfigOption, recorded[0].Method)
	assert.Equal(t, acp.ConfigOptionIDModel, recorded[0].Params["configId"])
	assert.Equal(t, "fast-model", recorded[0].Params["value"])
	assert.Equal(t, acp.MethodSessionSetMode, recorded[1].Method)
	assert.Equal(t, contracts.GooseModeApprove, recorded[1].Params["modeId"])
}

func TestGooseCancelSessionSendsACPMethod(t *testing.T) {
	agent, requests := newGooseAgentForRPC(t)

	require.NoError(t, agent.CancelSessionForTest())
	testutil.AssertEventually(t, func() bool {
		recorded := requests()
		return len(recorded) == 1 && recorded[0].Method == acp.MethodSessionCancel
	}, "expected session/cancel notification to be recorded")
}

func TestGooseAvailableOptionGroupsFallsBack(t *testing.T) {
	// configure sets the channel and acp.Start seeds the static fallback list from the provider's
	// registration; OptionGroups serves that fallback before the session reports a
	// permission-mode catalog. This test wires the Base directly, so it sets
	// secondaryFallback itself.
	agent := &Agent{}
	agent.HooksForTest().ModeChannel = acp.ModeChannelPermissionMode
	agent.SetSecondaryFallbackForTest(fallbackGooseCLIModes())

	groups := agent.OptionGroups()
	require.Len(t, groups, 1)
	assert.Equal(t, "permissionMode", groups[0].GetId())
	assert.Equal(t, contracts.GooseModeSmartApprove, groups[0].GetOptions()[0].GetId())
}

func TestDefaultModel_GooseUsesEnvOverride(t *testing.T) {
	t.Setenv("LEAPMUX_GOOSE_DEFAULT_MODEL", "custom-model")
	assert.Equal(t, "custom-model", Registration().DefaultModel())
}

// Goose sends its live status and its usage totals ONLY to a client that asks
// for them, on a method of its own. Without the advertisement the counters
// stayed empty and every status line was lost.
func TestGooseSessionUpdateReadsUsageAndNotices(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := &Agent{}
	a.SetSinkForTest(agent.NewProviderServices(sink))
	a.Process = providerkit.NewProcessFrom(providerkit.ProcessConfig{AgentID: "goose-1"})

	claimed := a.handleGooseExtraMethod(providerkit.ParseLine([]byte(`{"jsonrpc":"2.0","method":"_goose/unstable/session/update","params":{
		"sessionId":"s1","update":{"sessionUpdate":"usage_update","used":1200,"contextLimit":200000,
		"accumulatedInputTokens":900,"accumulatedOutputTokens":300,"accumulatedCost":0.0125}}}`)))
	assert.True(t, claimed, "the handler claims its own method so no raw row is persisted")

	require.Positive(t, sink.SessionInfoCount())
	last := sink.LastSessionInfo()
	usage, ok := last[contracts.SessionInfoKeyContextUsage].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, int64(1200), usage[contracts.ContextUsageFieldContextTokens])
	assert.Equal(t, int64(200000), usage[contracts.ContextUsageFieldContextWindow])
	assert.Equal(t, 0.0125, last[contracts.SessionInfoKeyTotalCostUsd])
	// Every count the breakdown draws is present, and the two Goose does not measure
	// carry a ZERO. An ABSENT key blanks its row rather than showing that the
	// provider counted none, which is why this site goes through the shared
	// projection instead of spelling the keys itself.
	assert.Equal(t, int64(900), usage[contracts.ContextUsageFieldInputTokens])
	assert.Equal(t, int64(300), usage[contracts.ContextUsageFieldOutputTokens])
	assert.Equal(t, int64(0), usage[contracts.ContextUsageFieldCacheCreationInputTokens])
	assert.Equal(t, int64(0), usage[contracts.ContextUsageFieldCacheReadInputTokens])

	// A NOTICE is a sentence the reader must see; `progress` is live chrome that
	// Goose's own schema says must not become history.
	a.handleGooseExtraMethod(providerkit.ParseLine([]byte(`{"jsonrpc":"2.0","method":"_goose/unstable/session/update","params":{
		"sessionId":"s1","update":{"sessionUpdate":"status_message","status":{"type":"notice","message":"Switched provider"}}}}`)))
	a.handleGooseExtraMethod(providerkit.ParseLine([]byte(`{"jsonrpc":"2.0","method":"_goose/unstable/session/update","params":{
		"sessionId":"s1","update":{"sessionUpdate":"status_message","status":{"type":"progress","message":"Thinking"}}}}`)))

	notifications := sink.Notifications()
	require.Len(t, notifications, 1)
	assert.Equal(t, contracts.NotificationTypeAgentStatus, notifications[0]["type"])
	assert.Equal(t, "Switched provider", notifications[0]["text"])

	// A method this handler does not own reaches the shared dispatcher unchanged.
	assert.False(t, a.handleGooseExtraMethod(providerkit.ParseLine([]byte(`{"jsonrpc":"2.0","method":"session/update","params":{}}`))))
}

// The frames below are the ones Goose's own Rust tests assert
// (`crates/goose/src/acp/server/tool_notifications.rs`). All four notification
// types ride a `tool_call_update` whose status is `in_progress`, so the shared
// classifier hides the ROW -- which is why the text inside has to reach the reader
// through a hook instead.

// A progress sentence describes a call that STILL RUNS, so it takes the ephemeral
// tail the shell output uses: the row draws it while the call runs and drops it when
// the result lands. Persisting one would write a row per tick.
func TestGooseProgressNotificationReachesTheRunningRow(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := &Agent{}
	a.SetSinkForTest(agent.NewProviderServices(sink))
	claimed := a.observeGooseToolNotification(acp.ToolCallUpdateEnvelope{
		ToolCallID: "tool-1",
		Meta: json.RawMessage(`{"toolNotification":{"type":"progress","params":{
			"progressToken":"scan-repo","progress":3.0,"total":10.0,
			"message":"Scanned 3 of 10 directories"}}}`),
	})
	require.True(t, claimed)
	assert.Empty(t, sink.Messages(), "a progress tick is not a transcript row")
	require.NotEmpty(t, sink.ProgressUpdates())
	last := sink.ProgressUpdates()[len(sink.ProgressUpdates())-1]
	assert.Equal(t, agent.ProgressOutputTail, last.Operation)
	assert.Equal(t, "tool-1", last.ScopeID)
	assert.Equal(t, "Scanned 3 of 10 directories", last.Text)
}

// Goose sends the counts beside its own sentence. The sentence leads, because
// "Scanned 3 of 10 directories (3/10)" reads worse than the sentence alone -- so the
// counts stand in only when the runtime sent no sentence.
func TestGooseProgressFallsBackToTheCountsItSent(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		params string
		want   string
	}{
		{"a sentence wins", `{"progress":3.0,"total":10.0,"message":"Scanned the repository"}`, "Scanned the repository"},
		{"counts stand in", `{"progress":3.0,"total":10.0}`, "3 of 10"},
		{"a count with no total", `{"progress":7.0}`, "7"},
		{"a fractional count keeps its fraction", `{"progress":2.5}`, "2.5"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var meta gooseToolNotification
			require.NoError(t, json.Unmarshal([]byte(`{"type":"progress","params":`+tc.params+`}`), &meta))
			assert.Equal(t, tc.want, gooseProgressLine(meta))
		})
	}
}

// A progress notification that states NEITHER a sentence nor a count says nothing a
// reader can use, so it reaches no surface -- but it is still claimed, because an
// empty update must not reach the merge that folds an update into the stored row.
func TestGooseEmptyProgressIsClaimedAndReachesNoSurface(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := &Agent{}
	a.SetSinkForTest(agent.NewProviderServices(sink))
	claimed := a.observeGooseToolNotification(acp.ToolCallUpdateEnvelope{
		ToolCallID: "tool-1",
		Meta:       json.RawMessage(`{"toolNotification":{"type":"progress","params":{"progressToken":"t"}}}`),
	})
	assert.True(t, claimed)
	assert.Empty(t, sink.ProgressUpdates())
	assert.Empty(t, sink.Messages())
}

// A platform event is an extension announcing something that happened OUTSIDE this
// call, so it outlives the call and reaches the transcript as a notification.
func TestGoosePlatformEventBecomesANotification(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := &Agent{}
	a.SetSinkForTest(agent.NewProviderServices(sink))
	claimed := a.observeGooseToolNotification(acp.ToolCallUpdateEnvelope{
		ToolCallID: "tool-1",
		Meta: json.RawMessage(`{"toolNotification":{"type":"platform_event","params":{
			"extension":"apps","event_type":"app_created","app_name":"platform-event-repro"}}}`),
	})
	require.True(t, claimed)
	require.Len(t, sink.Notifications(), 1)
	assert.Equal(t, map[string]interface{}{
		"type": contracts.NotificationTypeAgentStatus, "text": "apps: app created",
	}, sink.Notifications()[0])
}

func TestGoosePlatformEventLineStatesWhatItCan(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ params, want string }{
		{`{"extension":"apps","event_type":"app_created"}`, "apps: app created"},
		{`{"event_type":"app_created"}`, "app created"},
		{`{"extension":"apps"}`, "apps sent an event"},
		// An extension's own field names are ITS vocabulary, so a line built from
		// them would state a word this code cannot explain.
		{`{"app_name":"only-its-own-field"}`, ""},
	} {
		t.Run(tc.params, func(t *testing.T) {
			t.Parallel()
			var meta gooseToolNotification
			require.NoError(t, json.Unmarshal([]byte(`{"type":"platform_event","params":`+tc.params+`}`), &meta))
			assert.Equal(t, tc.want, goosePlatformEventLine(meta))
		})
	}
}

// The two types this hook does NOT claim stay with the paths that own them:
// `live_output` reaches the byte counter and the tail, and `message` is a log line
// the runtime keeps for itself.
func TestGooseToolNotificationClaimsOnlyItsOwnTwoTypes(t *testing.T) {
	t.Parallel()

	sink := &agenttest.Sink{}
	a := &Agent{}
	a.SetSinkForTest(agent.NewProviderServices(sink))
	for _, meta := range []string{
		`{"toolNotification":{"type":"live_output","params":{"sequence":1,"chunks":[{"output":"x"}]}}}`,
		`{"toolNotification":{"type":"message","params":{"level":"info","logger":"goose"}}}`,
		`{"toolNotification":{"type":"a_type_from_a_later_release","params":{}}}`,
		`{}`,
	} {
		assert.False(t, a.observeGooseToolNotification(acp.ToolCallUpdateEnvelope{
			ToolCallID: "tool-1", Meta: json.RawMessage(meta),
		}), meta)
	}
	assert.Empty(t, sink.Messages())
}
