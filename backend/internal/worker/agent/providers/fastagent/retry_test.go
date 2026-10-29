package fastagent

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp/acptest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// The chunk texts and error responses of .tmp/retry-research/fastagent.md and
// .tmp/probe/fastagent/wire-*.log, fast-agent 0.10.47 against a fake model,
// copied verbatim.
const (
	fastagentAnthropic529Text  = "I hit an internal error while calling the model: anthropic request failed for model 'claude-sonnet-4-5' (status=529): Error code: 529 - {'type': 'error', 'error': {'type': 'overloaded_error', 'message': 'Overloaded'}}. See fast-agent-error for additional details."
	fastagentAnthropic500Text  = "I hit an internal error while calling the model: anthropic request failed for model 'claude-sonnet-4-5' (status=500): Error code: 500 - {'type': 'error', 'error': {'type': 'api_error', 'message': 'Internal server error'}}. See fast-agent-error for additional details."
	fastagentAnthropic429Text  = "I hit an internal error while calling the model: anthropic request failed for model 'claude-sonnet-4-5' (status=429): Error code: 429 - {'type': 'error', 'error': {'type': 'rate_limit_error', 'message': \"This request would exceed your account's rate limit. Please try again later.\"}}. See fast-agent-error for additional details."
	fastagentAnthropic401Text  = "I hit an internal error while calling the model: anthropic request failed for model 'claude-sonnet-4-5' (status=401): Error code: 401 - {'type': 'error', 'error': {'type': 'authentication_error', 'message': 'invalid x-api-key'}}. See fast-agent-error for additional details."
	fastagentOpenAI500Text     = "I hit an internal error while calling the model: generic request failed for model 'fake-model' (code: server_error) (status=500): Error code: 500 - {'error': {'message': 'The server had an error while processing your request.', 'type': 'server_error', 'param': None, 'code': 'server_error'}}. See fast-agent-error for additional details."
	fastagentOpenAI503Text     = "I hit an internal error while calling the model: generic request failed for model 'fake-model' (status=503): Error code: 503 - {'error': {'message': 'The engine is currently overloaded, please try again later.', 'type': 'server_error', 'param': None, 'code': None}}. See fast-agent-error for additional details."
	fastagentOpenAI429Text     = "I hit an internal error while calling the model: generic request failed for model 'fake-model' (code: rate_limit_exceeded) (status=429): Error code: 429 - {'error': {'message': 'Rate limit reached for requests. Please try again in 1s.', 'type': 'requests', 'param': None, 'code': 'rate_limit_exceeded'}}. See fast-agent-error for additional details."
	fastagentQuotaText         = "I hit an internal error while calling the model: generic request failed for model 'fake-model' (code: insufficient_quota) (status=429): Error code: 429 - {'error': {'message': 'You exceeded your current quota, please check your plan and billing details.', 'type': 'insufficient_quota', 'param': None, 'code': 'insufficient_quo... See fast-agent-error for additional details."
	fastagentOpenAI401Text     = "I hit an internal error while calling the model: generic request failed for model 'fake-model' (code: invalid_api_key) (status=401): Error code: 401 - {'error': {'message': 'Incorrect API key provided: fake.', 'type': 'invalid_request_error', 'param': None, 'code': 'invalid_api_key'}}. See fast-agent-error for additional details."
	fastagentPartialText       = "Hel"
	fastagentSuccessText       = "Hello from fake."
	fastagentStreamDropError   = `{"code":-32603,"message":"Internal error","data":{"details":"peer closed connection without sending complete message body (incomplete chunked read)"}}`
	fastagentBillingError      = `{"code":-32603,"message":"Internal error","data":{"details":"Error code: 400 - {'type': 'error', 'error': {'type': 'invalid_request_error', 'message': 'Your credit balance is too low to access the Anthropic API. Please go to Plans & Billing to upgrade or purchase credits.'}}"}}`
	fastagentContextError      = `{"code":-32603,"message":"Internal error","data":{"details":"Error code: 400 - {'type': 'error', 'error': {'type': 'invalid_request_error', 'message': 'prompt is too long: 250000 tokens > 200000 maximum'}}"}}`
	fastagentBareStringError   = "{\"code\":-32603,\"message\":\"Internal error\",\"data\":\"Could not resolve authentication method. Expected one of api_key, auth_token, or credentials to be set. Or for one of the `X-Api-Key` or `Authorization` headers to be explicitly omitted\"}"
	fastagentParseFailureError = `{"code":-32603,"message":"Internal error","data":{"details":"Failed to parse response as JSON. Raw response: [DONE]"}}`
	fastagentGoogle503Error    = `{"code":-32000,"message":"Authentication required","data":{"methodId":"fast-agent-ai-secrets","message":"Google API Error: 503","details":"The engine is currently overloaded, please try again later.","configFile":"fast-agent.secrets.yaml","docsUrl":"https://fast-agent.ai/ref/config_file/","recommendedCommands":["fast-agent check","fast-agent model doctor","fast-agent model setup"],"description":"Configure provider keys in fast-agent.secrets.yaml or environment variables. For interactive setup, run ` + "`fast-agent model setup`" + ` in a terminal.","envVars":["GOOGLE_API_KEY"],"provider":"Google"}}`
	fastagentGoogle500Error    = `{"code":-32000,"message":"Authentication required","data":{"methodId":"fast-agent-ai-secrets","message":"Google API Error: 500","details":"The server had an error while processing your request.","configFile":"fast-agent.secrets.yaml","docsUrl":"https://fast-agent.ai/ref/config_file/","recommendedCommands":["fast-agent check","fast-agent model doctor","fast-agent model setup"],"description":"Configure provider keys in fast-agent.secrets.yaml or environment variables. For interactive setup, run ` + "`fast-agent model setup`" + ` in a terminal.","envVars":["GOOGLE_API_KEY"],"provider":"Google"}}`
	fastagentGoogle429Error    = `{"code":-32000,"message":"Authentication required","data":{"methodId":"fast-agent-ai-secrets","message":"Google API Error: 429","details":"Rate limit reached for requests. Please try again in 1s.","configFile":"fast-agent.secrets.yaml","docsUrl":"https://fast-agent.ai/ref/config_file/","recommendedCommands":["fast-agent check","fast-agent model doctor","fast-agent model setup"],"description":"Configure provider keys in fast-agent.secrets.yaml or environment variables. For interactive setup, run ` + "`fast-agent model setup`" + ` in a terminal.","envVars":["GOOGLE_API_KEY"],"provider":"Google"}}`
	fastagentGoogle401Error    = `{"code":-32000,"message":"Authentication required","data":{"methodId":"fast-agent-ai-secrets","message":"Google API Error: 401","details":"Incorrect API key provided: fake.","configFile":"fast-agent.secrets.yaml","docsUrl":"https://fast-agent.ai/ref/config_file/","recommendedCommands":["fast-agent check","fast-agent model doctor","fast-agent model setup"],"description":"Configure provider keys in fast-agent.secrets.yaml or environment variables. For interactive setup, run ` + "`fast-agent model setup`" + ` in a terminal.","envVars":["GOOGLE_API_KEY"],"provider":"Google"}}`
	fastagentGoogle400Error    = `{"code":-32000,"message":"Authentication required","data":{"methodId":"fast-agent-ai-secrets","message":"Google API Error: 400","details":"This model's maximum context length is 128000 tokens. However, your messages resulted in 250000 tokens.","configFile":"fast-agent.secrets.yaml","docsUrl":"https://fast-agent.ai/ref/config_file/","recommendedCommands":["fast-agent check","fast-agent model doctor","fast-agent model setup"],"description":"Configure provider keys in fast-agent.secrets.yaml or environment variables. For interactive setup, run ` + "`fast-agent model setup`" + ` in a terminal.","envVars":["GOOGLE_API_KEY"],"provider":"Google"}}`

	// fastagentConnectionErrorText is the agent message of a refused
	// connection. No wire log holds it, so it is copied from the session
	// history that fast-agent wrote in the probe run
	// (.tmp/probe/fastagent/fahome/sessions/2610280953-ZoH1lJ/history_agent.json).
	fastagentConnectionErrorText = "I hit an internal error while calling the model: generic request failed for model 'fake-model': Connection error. See fast-agent-error for additional details."
)

// Constructed, because no probe run captured them.
const (
	// fastagentTimeoutText follows build_stream_failure_response in
	// fast-agent 0.10.47 (fast_agent/llm/provider/error_utils.py): an SDK
	// error with no HTTP status states its message after the head, as the
	// refused connection does, and the agent adds no second period.
	fastagentTimeoutText = "I hit an internal error while calling the model: anthropic request failed for model 'claude-sonnet-4-5': Request timed out. See fast-agent-error for additional details."
	// fastagentBareStreamDropError is the -32603 shape with str(exc) as data
	// itself, which fast-agent sends when str(exc) is JSON, with a stream text.
	fastagentBareStreamDropError = `{"code":-32603,"message":"Internal error","data":"peer closed connection without sending complete message body (incomplete chunked read)"}`
	// fastagentNoCredentialError is a -32000 that states no Google status.
	fastagentNoCredentialError = `{"code":-32000,"message":"Authentication required"}`
	// fastagentUnknownCodeFailure is a JSON-RPC code that the verdict does
	// not read.
	fastagentUnknownCodeFailure = `{"code":-32602,"message":"Invalid params"}`
)

var fastagentObservedAt = time.Date(2026, 10, 28, 9, 49, 0, 0, time.UTC)

func fastagentResponseError(t *testing.T, raw string) error {
	t.Helper()
	var response struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(raw), &response))
	return &providerkit.JSONRPCResponseError{Code: response.Code, Message: response.Message, Data: response.Data}
}

func TestFastagentTurnFailureClassifiesEveryCapture(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		text string
		err  string
		want providerkit.TurnFailureKind
	}{
		"an Anthropic 529":         {text: fastagentAnthropic529Text, want: providerkit.TurnFailureTransient},
		"an Anthropic 500":         {text: fastagentAnthropic500Text, want: providerkit.TurnFailureTransient},
		"an OpenAI 500":            {text: fastagentOpenAI500Text, want: providerkit.TurnFailureTransient},
		"an OpenAI 503":            {text: fastagentOpenAI503Text, want: providerkit.TurnFailureTransient},
		"an OpenAI per-minute 429": {text: fastagentOpenAI429Text, want: providerkit.TurnFailureRateLimited},
		"an Anthropic account limit takes the vendor": {text: fastagentAnthropic429Text, want: providerkit.TurnFailureUsageLimited},
		"an exceeded quota is a billing block":        {text: fastagentQuotaText, want: providerkit.TurnFailureBillingBlocked},
		"an Anthropic 401":                            {text: fastagentAnthropic401Text, want: providerkit.TurnFailureNotRetryable},
		"an OpenAI 401":                               {text: fastagentOpenAI401Text, want: providerkit.TurnFailureNotRetryable},
		"a refused connection":                        {text: fastagentConnectionErrorText, want: providerkit.TurnFailureTransient},
		"a timed-out request":                         {text: fastagentTimeoutText, want: providerkit.TurnFailureTransient},
		"a refusal after streamed text":               {text: fastagentPartialText, want: providerkit.TurnFailureNotRetryable},
		"a successful answer":                         {text: fastagentSuccessText, want: providerkit.TurnFailureNotRetryable},
		"no chunk at all":                             {want: providerkit.TurnFailureNotRetryable},
		"a dropped stream":                            {err: fastagentStreamDropError, want: providerkit.TurnFailureTransient},
		"a dropped stream as bare data":               {err: fastagentBareStreamDropError, want: providerkit.TurnFailureTransient},
		"a low credit balance":                        {err: fastagentBillingError, want: providerkit.TurnFailureBillingBlocked},
		"a prompt that is too long":                   {err: fastagentContextError, want: providerkit.TurnFailureNotRetryable},
		"a missing credential as bare data":           {err: fastagentBareStringError, want: providerkit.TurnFailureNotRetryable},
		"an unparseable stream":                       {err: fastagentParseFailureError, want: providerkit.TurnFailureNotRetryable},
		"a Google 503":                                {err: fastagentGoogle503Error, want: providerkit.TurnFailureTransient},
		"a Google 500":                                {err: fastagentGoogle500Error, want: providerkit.TurnFailureTransient},
		"a Google 429":                                {err: fastagentGoogle429Error, want: providerkit.TurnFailureRateLimited},
		"a Google 401":                                {err: fastagentGoogle401Error, want: providerkit.TurnFailureNotRetryable},
		"a Google 400":                                {err: fastagentGoogle400Error, want: providerkit.TurnFailureNotRetryable},
		"a credential that no Google error states":    {err: fastagentNoCredentialError, want: providerkit.TurnFailureNotRetryable},
		"another JSON-RPC code":                       {err: fastagentUnknownCodeFailure, want: providerkit.TurnFailureNotRetryable},
		// A chunk is the record of a failure only when the prompt succeeded.
		"an error chunk before a failed prompt": {text: fastagentAnthropic529Text, err: fastagentNoCredentialError, want: providerkit.TurnFailureNotRetryable},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var err error
			if tc.err != "" {
				err = fastagentResponseError(t, tc.err)
			}
			raw := json.RawMessage(`{"sessionUpdate":"agent_message_chunk"}`)
			verdict := fastagentTurnFailure(err, false, tc.text, raw, fastagentObservedAt)
			assert.Equal(t, tc.want, verdict.Failure.Kind)
			if tc.want == providerkit.TurnFailureNotRetryable || tc.want == providerkit.TurnFailureBillingBlocked {
				return
			}
			if tc.err == "" {
				assert.JSONEq(t, string(raw), string(verdict.Payload), "a chunk failure carries the chunk's raw update")
				return
			}
			assert.JSONEq(t, tc.err, string(verdict.Payload), "a prompt error carries the error response")
		})
	}
}

func TestFastagentTurnFailureGivesNotRetryableAtAStop(t *testing.T) {
	t.Parallel()
	assert.Equal(t, providerkit.TurnFailureNotRetryable, fastagentTurnFailure(nil, true, fastagentAnthropic529Text, nil, fastagentObservedAt).Failure.Kind)
	stopped := fastagentTurnFailure(fastagentResponseError(t, fastagentStreamDropError), true, "", nil, fastagentObservedAt)
	assert.Equal(t, providerkit.TurnFailureNotRetryable, stopped.Failure.Kind)
}

// A wrapped error still reveals its JSON-RPC response.
func TestFastagentTurnFailureUnwrapsTheResponseError(t *testing.T) {
	t.Parallel()
	err := fmt.Errorf("session/prompt: %w", fastagentResponseError(t, fastagentGoogle503Error))
	assert.Equal(t, providerkit.TurnFailureTransient, fastagentTurnFailure(err, false, "", nil, fastagentObservedAt).Failure.Kind)
}

// fastagentUpdate builds the params of one session/update of sessionID.
func fastagentUpdate(sessionID string, update map[string]any) json.RawMessage {
	raw, err := json.Marshal(map[string]any{"sessionId": sessionID, "update": update})
	if err != nil {
		panic(err)
	}
	return raw
}

func fastagentChunk(kind, text string) map[string]any {
	return map[string]any{"sessionUpdate": kind, "content": map[string]any{"type": "text", "text": text}}
}

// newFastagentRetryAgent builds a fast-agent with its production hooks over a
// fake peer. Before the peer answers a session/prompt, it dispatches each
// update of duringPrompt, as fast-agent streams a turn before its reply.
func newFastagentRetryAgent(t *testing.T, reply agenttest.RPCReply, duringPrompt ...func(*Agent) json.RawMessage) (*Agent, *agenttest.Sink) {
	t.Helper()
	clock := testutil.NewQuartzMock(t)
	clock.Set(fastagentObservedAt)
	sink := &agenttest.Sink{}
	services := agent.NewProviderServices(sink)
	a := &Agent{home: t.TempDir(), clock: clock}
	acptest.NewAgentForRPCWithResponder(t,
		func() *Agent { return a },
		func(a *Agent) *acp.Base { return &a.Base },
		func(method string) agenttest.RPCReply {
			if method != acp.MethodSessionPrompt {
				return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
			}
			for _, update := range duringPrompt {
				a.HandleSessionUpdateForTest(update(a))
			}
			return reply
		},
	)
	a.SetSinkForTest(services)
	*a.HooksForTest() = a.hooks("", services)
	return a, sink
}

// runFastagentPrompt sends one prompt and waits until its reply ended the turn.
func runFastagentPrompt(t *testing.T, a *Agent) {
	t.Helper()
	require.NoError(t, a.SendInput("Say hello.", nil))
	testutil.RequireEventually(t, func() bool { return !a.PromptActive() })
}

// fastagentRetryTimeline splits the timeline at its last row. It returns that
// row's operation and every retry operation after it, and fails when a retry
// operation precedes the row.
func fastagentRetryTimeline(t *testing.T, timeline []agenttest.TimelineEntry) (lastRow string, retries []string) {
	t.Helper()
	for _, entry := range timeline {
		switch entry.Op {
		case "message", "turn_end", "notification", "leapmux_notification":
			require.Empty(t, retries, "a retry operation precedes the row %q", entry.Op)
			lastRow = entry.Op
		default:
			retries = append(retries, entry.Op)
		}
	}
	return lastRow, retries
}

func mainChunk(kind, text string) func(*Agent) json.RawMessage {
	return func(a *Agent) json.RawMessage {
		return fastagentUpdate(a.CurrentSessionID(), fastagentChunk(kind, text))
	}
}

func TestFastagentPromptEndAppliesOneVerdictAfterTheTurnEndRow(t *testing.T) {
	t.Parallel()
	refusal := agenttest.RPCReply{Result: json.RawMessage(`{"stopReason":"refusal"}`)}
	for name, tc := range map[string]struct {
		reply        agenttest.RPCReply
		duringPrompt []func(*Agent) json.RawMessage
		wantRow      string
		wantRetries  []string
	}{
		"a 529 message schedules a server-error retry": {
			reply: refusal, duringPrompt: []func(*Agent) json.RawMessage{mainChunk("agent_message_chunk", fastagentAnthropic529Text)},
			wantRow: "turn_end", wantRetries: []string{"schedule_server_error"},
		},
		"a 401 message cancels the server-error retry": {
			reply: refusal, duringPrompt: []func(*Agent) json.RawMessage{mainChunk("agent_message_chunk", fastagentAnthropic401Text)},
			wantRow: "turn_end", wantRetries: []string{"cancel_server_error"},
		},
		"a successful answer cancels the server-error retry": {
			reply:        agenttest.RPCReply{Result: json.RawMessage(`{"stopReason":"end_turn"}`)},
			duringPrompt: []func(*Agent) json.RawMessage{mainChunk("agent_message_chunk", fastagentSuccessText)},
			wantRow:      "turn_end", wantRetries: []string{"cancel_server_error"},
		},
		"a dropped stream schedules a server-error retry": {
			reply:   agenttest.RPCReply{Error: json.RawMessage(fastagentStreamDropError)},
			wantRow: "leapmux_notification", wantRetries: []string{"schedule_server_error"},
		},
		"a low credit balance cancels both retries": {
			reply:   agenttest.RPCReply{Error: json.RawMessage(fastagentBillingError)},
			wantRow: "leapmux_notification", wantRetries: []string{"cancel_usage_limit", "cancel_server_error"},
		},
		"a Google 503 schedules a server-error retry": {
			reply:   agenttest.RPCReply{Error: json.RawMessage(fastagentGoogle503Error)},
			wantRow: "leapmux_notification", wantRetries: []string{"schedule_server_error"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a, sink := newFastagentRetryAgent(t, tc.reply, tc.duringPrompt...)
			runFastagentPrompt(t, a)
			lastRow, retries := fastagentRetryTimeline(t, sink.Timeline())
			assert.Equal(t, tc.wantRow, lastRow)
			assert.Equal(t, tc.wantRetries, retries)
		})
	}
}

// The verdict reads the last agent message chunk of the MAIN session. A
// thought, a user chunk, a chunk of another session, and a chunk that a
// subagent tag routes to the child's transcript each arrive after the error
// chunk, and none of them replaces it.
func TestFastagentKeepsTheMainErrorChunkAcrossLaterChildAndThoughtChunks(t *testing.T) {
	t.Parallel()
	for name, later := range map[string]func(*Agent) json.RawMessage{
		"a thought":    mainChunk("agent_thought_chunk", "thinking about it"),
		"a user chunk": mainChunk("user_message_chunk", "Say hello."),
		"another session": func(*Agent) json.RawMessage {
			return fastagentUpdate("session-other", fastagentChunk("agent_message_chunk", "child answer"))
		},
		"a subagent chunk": func(a *Agent) json.RawMessage {
			update := fastagentChunk("agent_message_chunk", "child answer")
			update["_meta"] = map[string]any{"parentToolCallId": "call-1"}
			return fastagentUpdate(a.CurrentSessionID(), update)
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a, sink := newFastagentRetryAgent(t, agenttest.RPCReply{Result: json.RawMessage(`{"stopReason":"refusal"}`)},
				mainChunk("agent_message_chunk", fastagentAnthropic529Text), later)
			spawn := a.observeSubagentCall("call-1", "subagent", json.RawMessage(`{"message":"Count files","label":"Count files"}`))
			require.NotNil(t, spawn)
			a.ApplySubagentObservation(spawn)
			runFastagentPrompt(t, a)
			_, retries := fastagentRetryTimeline(t, sink.Timeline())
			assert.Equal(t, []string{"schedule_server_error"}, retries)
			require.Equal(t, 1, sink.AutoScheduleCount())
			var payload struct {
				Content struct {
					Text string `json:"text"`
				} `json:"content"`
			}
			require.NoError(t, json.Unmarshal(sink.LastAutoSchedule().SourcePayload, &payload))
			assert.Equal(t, fastagentAnthropic529Text, payload.Content.Text, "the payload is the main error chunk")
		})
	}
}

// An error chunk of an earlier prompt does not decide the next prompt:
// PromptPrepared clears it.
func TestFastagentEarlierPromptChunkDecidesNothing(t *testing.T) {
	t.Parallel()
	a, sink := newFastagentRetryAgent(t, agenttest.RPCReply{Result: json.RawMessage(`{"stopReason":"end_turn"}`)})
	a.HandleSessionUpdateForTest(fastagentUpdate(a.CurrentSessionID(), fastagentChunk("agent_message_chunk", fastagentAnthropic529Text)))
	runFastagentPrompt(t, a)
	_, retries := fastagentRetryTimeline(t, sink.Timeline())
	assert.Equal(t, []string{"cancel_server_error"}, retries)
}

// The model report of a native local command answers an audio query. The
// session update handler consumes that report first, so it never becomes the
// failure record of the prompt. Other text during the query still does.
func TestFastagentAudioQueryReportIsNoFailureRecord(t *testing.T) {
	t.Parallel()
	a, _ := newFastagentRetryAgent(t, agenttest.RPCReply{Result: json.RawMessage(`{"stopReason":"end_turn"}`)})
	session := a.CurrentSessionID()
	a.audioQueryMu.Lock()
	a.audioQuery = &fastagentAudioQuery{sessionID: session}
	a.audioQueryMu.Unlock()
	handle := a.HooksForTest().SessionUpdateHandler
	report := json.RawMessage(`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"# model\n\nprovider: anthropic"}}`)

	assert.True(t, handle(session, nil, report), "the audio query consumes the model report")
	a.Mu.Lock()
	assert.Empty(t, a.lastChunkText, "the report is no failure record")
	a.Mu.Unlock()

	failure := json.RawMessage(`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"` + fastagentAnthropic529Text + `"}}`)
	assert.False(t, handle(session, nil, failure), "other text keeps its transcript route")
	a.Mu.Lock()
	defer a.Mu.Unlock()
	assert.Equal(t, fastagentAnthropic529Text, a.lastChunkText)
}

// The observer claims no update and records nothing from a malformed or empty
// chunk.
func TestFastagentObserverClaimsNothingAndSkipsEmptyChunks(t *testing.T) {
	t.Parallel()
	a, _ := newFastagentRetryAgent(t, agenttest.RPCReply{Result: json.RawMessage(`{"stopReason":"end_turn"}`)})
	session := a.CurrentSessionID()
	assert.False(t, a.observeFastagentSessionUpdate(session, nil, json.RawMessage(`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"`+fastagentSuccessText+`"}}`)))
	assert.False(t, a.observeFastagentSessionUpdate(session, nil, json.RawMessage(`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":""}}`)))
	assert.False(t, a.observeFastagentSessionUpdate(session, nil, json.RawMessage(`{"sessionUpdate":"agent_message_chunk","content":"broken"}`)))
	assert.False(t, a.observeFastagentSessionUpdate(session, nil, json.RawMessage(`{"sessionUpdate":"agent_message_chunk","content":{"type":"image","data":"AAAA"}}`)))
	// The base hands this hook the updates of an attached child session too.
	assert.False(t, a.observeFastagentSessionUpdate("session-child", nil, json.RawMessage(`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"child answer"}}`)))
	a.Mu.Lock()
	defer a.Mu.Unlock()
	assert.Equal(t, fastagentSuccessText, a.lastChunkText)
}
