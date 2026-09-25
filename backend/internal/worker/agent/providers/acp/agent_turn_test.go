package acp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp/acptest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// agentTurnEndFrame is a provider's own end of a turn it started, which the
// base stores as the turn-end row unchanged.
const agentTurnEndFrame = `{"jsonrpc":"2.0","method":"_vendor/turn_ended","params":{"sessionId":"session-1","reason":"end_turn"}}`

func TestAgentTurn_BeginOpensATurnThatRefusesInput(t *testing.T) {
	t.Parallel()
	out := &agenttest.Stdin{}
	b, sink := newACPTurnBase(t, out)

	require.True(t, b.BeginAgentTurn())
	assert.True(t, b.AgentTurnActive())
	assert.Equal(t, []bool{true}, sink.TurnActives(), "the agent turn is published like a prompt")

	// The worker queues a message behind a turn the agent started, exactly as
	// behind one LeapMux started: a second session/prompt would reach the agent
	// while it works.
	err := b.SendInput("later", nil)
	assert.ErrorIs(t, err, agent.ErrAgentBusy)
	assert.Empty(t, out.String(), "a refused input writes nothing")
}

func TestAgentTurn_AdmitOpensATurnWhenNoTurnRuns(t *testing.T) {
	t.Parallel()
	out := &agenttest.Stdin{}
	b, sink := newACPTurnBase(t, out)

	require.True(t, b.AdmitAgentTurn())
	assert.True(t, b.AgentTurnActive())
	assert.Equal(t, []bool{true}, sink.TurnActives(), "the admitted turn is published like a prompt")
	assert.False(t, b.AdmitAgentTurn(), "a second admission waits for the first turn")

	b.EndAgentTurn(json.RawMessage(agentTurnEndFrame))
	assert.True(t, b.AdmitAgentTurn(), "an ended turn admits the next one")
}

// An admission, unlike a turn that the agent states, never waits behind a
// prompt: the agent asked first, so it can ask again once the prompt ended.
func TestAgentTurn_AdmitRefusesWhileAPromptRuns(t *testing.T) {
	t.Parallel()
	out := &agenttest.Stdin{}
	b, sink := newACPTurnBase(t, out)
	require.NoError(t, b.SendInput("hello", nil))

	assert.False(t, b.AdmitAgentTurn())
	assert.False(t, b.AgentTurnActive())

	b.HandleJSONRPCResponseForTest(promptResponse(1))
	testutil.RequireEventually(t, func() bool { return !b.PromptActive() })
	assert.Equal(t, []bool{true, false}, sink.TurnActives(), "no agent turn waited behind the prompt")
	assert.True(t, b.AdmitAgentTurn())
}

// turnEndContents returns the content of each turn-end row, in order.
func turnEndContents(sink *agenttest.Sink) []string {
	var ends []string
	for _, message := range sink.Messages() {
		if message.TurnEnd {
			ends = append(ends, string(message.Content))
		}
	}
	return ends
}

// promptResponse is the session/prompt response of request id.
func promptResponse(id int) *providerkit.ParsedLine {
	return providerkit.ParseLine([]byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{"stopReason":"end_turn"}}`, id)))
}

func TestAgentTurn_BeginInsideAPromptWaitsForThePromptToEnd(t *testing.T) {
	t.Parallel()
	out := &agenttest.Stdin{}
	b, sink := newACPTurnBase(t, out)
	require.NoError(t, b.SendInput("/goal ship it", nil))

	// The agent starts its turn before the base processed the prompt's end.
	require.True(t, b.BeginAgentTurn(), "the agent turn waits for the prompt")
	assert.False(t, b.AgentTurnActive(), "it runs only once the prompt ended")
	assert.False(t, b.BeginAgentTurn(), "a second agent turn joins the waiting one")

	b.HandleJSONRPCResponseForTest(promptResponse(1))
	testutil.RequireEventually(t, func() bool { return b.AgentTurnActive() })
	assert.True(t, b.PromptActive(), "the agent turn holds the busy state")
	assert.Equal(t, []bool{true}, sink.TurnActives(), "no idle state is published between the two turns")
	assert.Len(t, turnEndContents(sink), 1, "the prompt wrote its own divider")

	b.EndAgentTurn(json.RawMessage(agentTurnEndFrame))
	assert.False(t, b.PromptActive())
	assert.Equal(t, []bool{true, false}, sink.TurnActives())
	ends := turnEndContents(sink)
	require.Len(t, ends, 2)
	assert.JSONEq(t, agentTurnEndFrame, ends[1], "the agent turn ends with its own frame")
}

func TestAgentTurn_AQueuedTurnThatEndsFirstEndsAfterThePrompt(t *testing.T) {
	t.Parallel()
	out := &agenttest.Stdin{}
	b, sink := newACPTurnBase(t, out)
	require.NoError(t, b.SendInput("hi", nil))
	require.True(t, b.BeginAgentTurn())

	// The whole agent turn went by before the prompt's end was processed.
	b.EndAgentTurn(json.RawMessage(agentTurnEndFrame))
	assert.Empty(t, turnEndContents(sink), "the prompt's end comes first")

	b.HandleJSONRPCResponseForTest(promptResponse(1))
	testutil.RequireEventually(t, func() bool { return !b.PromptActive() })
	ends := turnEndContents(sink)
	require.Len(t, ends, 2, "each turn writes its divider, in order")
	assert.JSONEq(t, `{"stopReason":"end_turn"}`, ends[0])
	assert.JSONEq(t, agentTurnEndFrame, ends[1])
	assert.Equal(t, []bool{true, false}, sink.TurnActives())
}

func TestAgentTurn_AQueuedTurnDefersTheFollowUpPrompt(t *testing.T) {
	t.Parallel()
	out := &agenttest.Stdin{}
	b, sink := newACPTurnBase(t, out)
	follow := &followUpBase{pending: []string{"also check the tests"}}
	b.hooks.FollowUpPrompt = follow.take
	require.NoError(t, b.SendInput("hi", nil))
	require.True(t, b.BeginAgentTurn())

	b.HandleJSONRPCResponseForTest(promptResponse(1))
	testutil.RequireEventually(t, func() bool { return b.AgentTurnActive() })
	// A prompt now would cut the agent's own turn short, so the input waits.
	assert.Equal(t, 1, strings.Count(out.String(), `"method":"session/prompt"`))

	b.EndAgentTurn(json.RawMessage(agentTurnEndFrame))
	testutil.RequireEventually(t, func() bool { return strings.Count(out.String(), `"method":"session/prompt"`) == 2 })
	assert.Contains(t, out.String(), "also check the tests")
	assert.True(t, b.PromptActive())
	assert.Equal(t, []bool{true}, sink.TurnActives(), "the turns follow one another with no idle state")
}

func TestAgentTurn_AFailedPromptHandsOverToTheQueuedTurn(t *testing.T) {
	t.Parallel()
	out := &agenttest.Stdin{}
	b, sink := newACPTurnBase(t, out)
	require.NoError(t, b.SendInput("/compress", nil))
	require.True(t, b.BeginAgentTurn())

	b.HandleJSONRPCResponseForTest(providerkit.ParseLine([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"Internal error"}}`)))
	testutil.RequireEventually(t, func() bool { return b.AgentTurnActive() })
	assert.True(t, b.PromptActive())
	require.Len(t, sink.LeapMuxNotifications(), 1, "the failure is still reported")

	b.EndAgentTurn(json.RawMessage(agentTurnEndFrame))
	assert.False(t, b.PromptActive())
}

func TestPromptParams_TheHookAdjustsEachPrompt(t *testing.T) {
	t.Parallel()
	out := &agenttest.Stdin{}
	b, _ := newACPTurnBase(t, out)
	b.hooks.PromptParams = func(params map[string]any) {
		params["_meta"] = map[string]any{"promptId": "p-1"}
	}

	require.NoError(t, b.SendInput("hi", nil))

	var frame struct {
		Params struct {
			SessionID string         `json:"sessionId"`
			Meta      map[string]any `json:"_meta"`
		} `json:"params"`
	}
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(out.String())), &frame))
	assert.Equal(t, "session-1", frame.Params.SessionID, "the hook keeps the base's own params")
	assert.Equal(t, map[string]any{"promptId": "p-1"}, frame.Params.Meta)
}

func TestAgentTurn_EndPersistsTheFrameAndClearsTheTurn(t *testing.T) {
	t.Parallel()
	out := &agenttest.Stdin{}
	b, sink := newACPTurnBase(t, out)
	require.True(t, b.BeginAgentTurn())

	b.HandleOutput(acptest.Chunk("session-1", "agent_message_chunk", "Woke up."))
	b.main().handleToolCall(json.RawMessage(`{"sessionUpdate":"tool_call","toolCallId":"call-1","title":"Read","kind":"read","status":"completed"}`))
	b.EndAgentTurn(json.RawMessage(agentTurnEndFrame))

	assert.False(t, b.AgentTurnActive())
	assert.False(t, b.PromptActive())
	assert.Equal(t, []string{"turn_active:true", "turn_end", "reset_spans", "turn_active:false"}, sink.TurnLifecycle(),
		"the turn end precedes the clear, as it does for a prompt")
	messages := sink.Messages()
	require.NotEmpty(t, messages)
	end := messages[len(messages)-1]
	require.True(t, end.TurnEnd)
	assert.JSONEq(t, agentTurnEndFrame, string(end.Content), "the agent's own frame is the row")
	assert.JSONEq(t, `{"`+contracts.MessageMetadataFieldToolUses+`":1}`, string(end.Metadata), "the turn counts its tool call")
	assert.Equal(t, []string{"text:Woke up."}, assembledTexts(t, messages[:len(messages)-1]))
}

func TestAgentTurn_EndWithNoAgentTurnIsANoOp(t *testing.T) {
	t.Parallel()
	out := &agenttest.Stdin{}
	b, sink := newACPTurnBase(t, out)

	b.EndAgentTurn(json.RawMessage(agentTurnEndFrame))

	assert.Equal(t, 0, sink.MessageCount())
	assert.Empty(t, sink.TurnActives())
}

func TestAgentTurn_EndWithoutRowStoresTheOutputAndWritesNoDivider(t *testing.T) {
	t.Parallel()
	out := &agenttest.Stdin{}
	b, sink := newACPTurnBase(t, out)
	require.True(t, b.BeginAgentTurn())

	b.HandleOutput(acptest.Chunk("session-1", "agent_message_chunk", "Compacting."))
	b.main().handleToolCall(json.RawMessage(`{"sessionUpdate":"tool_call","toolCallId":"call-1","title":"Read","kind":"read","status":"pending"}`))
	b.EndAgentTurnWithoutRow()

	assert.False(t, b.AgentTurnActive())
	assert.False(t, b.PromptActive(), "the queue can release the next message")
	assert.Equal(t, []bool{true, false}, sink.TurnActives())
	assert.Empty(t, turnEndContents(sink), "work that is no conversation turn draws no divider")
	assert.Equal(t, []string{"text:Compacting."}, assembledTexts(t, sink.Messages()), "the text is stored as a turn end stores it")
	var closing *agenttest.Message
	for _, message := range sink.Messages() {
		if message.SpanID == "call-1" && message.Closing {
			closing = &message
		}
	}
	require.NotNil(t, closing, "the tool call that the work left open is stored")
	assert.Equal(t, agent.MessageCompletionError, closing.Completion)
}

func TestAgentTurn_EndWithoutRowDropsAQueuedTurn(t *testing.T) {
	t.Parallel()
	out := &agenttest.Stdin{}
	b, sink := newACPTurnBase(t, out)
	require.NoError(t, b.SendInput("hi", nil))
	require.True(t, b.BeginAgentTurn())

	// The work ended before the prompt ahead of it did.
	b.EndAgentTurnWithoutRow()
	b.HandleJSONRPCResponseForTest(promptResponse(1))
	testutil.RequireEventually(t, func() bool { return !b.PromptActive() })

	assert.False(t, b.AgentTurnActive(), "no turn takes the busy state over")
	assert.Equal(t, []bool{true, false}, sink.TurnActives())
	assert.Len(t, turnEndContents(sink), 1, "only the prompt writes its divider")
}

// The queued turn set a boundary on the prompt's output. A turn that never runs
// takes nothing after that boundary, so the prompt's end stores that output,
// counts the prompt's tools, and leaves nothing for the next turn.
func TestAgentTurn_EndWithoutRowGivesTheOutputOfAQueuedTurnBackToThePrompt(t *testing.T) {
	t.Parallel()
	out := &agenttest.Stdin{}
	b, sink := newACPTurnBase(t, out)
	require.NoError(t, b.SendInput("hi", nil))
	b.HandleOutput(acptest.Chunk("session-1", "agent_message_chunk", "Before."))
	b.HandleOutput(toolCallFrame(`{"sessionUpdate":"tool_call","toolCallId":"prompt-tool","title":"Read","kind":"read","status":"completed"}`))
	require.True(t, b.BeginAgentTurn())
	b.HandleOutput(acptest.Chunk("session-1", "agent_message_chunk", "After."))
	b.HandleOutput(toolCallFrame(`{"sessionUpdate":"tool_call","toolCallId":"late-tool","title":"Build","kind":"execute","status":"in_progress"}`))

	b.EndAgentTurnWithoutRow()
	b.HandleJSONRPCResponseForTest(promptResponse(1))
	testutil.RequireEventually(t, func() bool { return !b.PromptActive() })

	assert.Equal(t, []string{"text:Before.", "text:After."}, assembledTexts(t, sink.Messages()),
		"the prompt's end stores the output after the boundary")
	assert.Equal(t, []agent.MessageCompletion{agent.MessageCompletionError}, closingRows(sink, "late-tool"),
		"the prompt's end closes the tool that no turn will finish")
	var toolUses []string
	for _, message := range sink.Messages() {
		if message.TurnEnd {
			toolUses = append(toolUses, string(message.Metadata))
		}
	}
	require.Len(t, toolUses, 1)
	assert.JSONEq(t, `{"`+contracts.MessageMetadataFieldToolUses+`":2}`, toolUses[0], "the prompt counts both tools")
	assert.Empty(t, b.TurnAssistantTextForTest().String(), "nothing waits for the next turn")
}

// The prompt's end can drain its own part of the output before the work
// ends: the drain took the boundary, and the queued turn has not taken the
// busy state yet. The work's output then remains, and its end stores it.
func TestAgentTurn_EndWithoutRowAfterThePromptDrainStoresTheOutputOfTheWork(t *testing.T) {
	t.Parallel()
	out := &agenttest.Stdin{}
	b, sink := newACPTurnBase(t, out)
	require.NoError(t, b.SendInput("hi", nil))
	require.True(t, b.BeginAgentTurn())
	b.HandleOutput(acptest.Chunk("session-1", "agent_message_chunk", "Work."))
	turn, bounded := b.drainPromptTurn()
	require.True(t, bounded)
	require.Empty(t, turn.assistantText)

	b.EndAgentTurnWithoutRow()

	assert.Equal(t, []string{"text:Work."}, assembledTexts(t, sink.Messages()))
	assert.Empty(t, b.TurnAssistantTextForTest().String())
}

func TestAgentTurn_EndWithoutRowWithNoAgentTurnIsANoOp(t *testing.T) {
	t.Parallel()
	out := &agenttest.Stdin{}
	b, sink := newACPTurnBase(t, out)

	b.EndAgentTurnWithoutRow()

	assert.Equal(t, 0, sink.MessageCount())
	assert.Empty(t, sink.TurnActives())
}

func TestAgentTurn_StopEndsTheAgentTurn(t *testing.T) {
	t.Parallel()
	out := &agenttest.Stdin{}
	b, _ := newACPTurnBase(t, out)
	require.True(t, b.BeginAgentTurn())

	b.clearActivePrompt()

	assert.False(t, b.AgentTurnActive(), "the turn fields go together")
	assert.True(t, b.BeginAgentTurn(), "a later agent turn can begin")
}

// followUpBase is a turn base whose provider holds input for the next prompt.
type followUpBase struct {
	mu          sync.Mutex
	pending     []string
	attachments []*leapmuxv1.Attachment
	stoppedAt   []bool
}

func (f *followUpBase) take(stopped bool) (string, []*leapmuxv1.Attachment, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stoppedAt = append(f.stoppedAt, stopped)
	if len(f.pending) == 0 || stopped {
		return "", nil, false
	}
	content := strings.Join(f.pending, "\n")
	attachments := f.attachments
	f.pending, f.attachments = nil, nil
	return content, attachments, true
}

func TestFollowUpPrompt_LeftoverInputStartsTheNextPromptWithoutAnIdleGap(t *testing.T) {
	t.Parallel()
	out := &agenttest.Stdin{}
	b, sink := newACPTurnBase(t, out)
	follow := &followUpBase{
		pending:     []string{"also check the tests"},
		attachments: []*leapmuxv1.Attachment{{Filename: "notes.txt", MimeType: "text/plain", Data: []byte("see line 4")}},
	}
	b.hooks.FollowUpPrompt = follow.take

	require.NoError(t, b.SendInput("hi", nil))
	b.HandleJSONRPCResponseForTest(providerkit.ParseLine([]byte(`{"jsonrpc":"2.0","id":1,"result":{"stopReason":"end_turn"}}`)))

	testutil.RequireEventually(t, func() bool { return strings.Count(out.String(), `"method":"session/prompt"`) == 2 })
	assert.Contains(t, out.String(), `also check the tests`)
	assert.Contains(t, out.String(), `see line 4`, "the attachment of the leftover input reaches the next prompt")
	assert.True(t, b.PromptActive())
	assert.Equal(t, []bool{true}, sink.TurnActives(), "no idle state is published between the two prompts")

	b.HandleJSONRPCResponseForTest(providerkit.ParseLine([]byte(`{"jsonrpc":"2.0","id":2,"result":{"stopReason":"end_turn"}}`)))
	testutil.RequireEventually(t, func() bool { return len(sink.TurnActives()) == 2 })
	assert.Equal(t, []bool{true, false}, sink.TurnActives())
	follow.mu.Lock()
	defer follow.mu.Unlock()
	assert.Equal(t, []bool{false, false}, follow.stoppedAt)
}

func TestFollowUpPrompt_AStoppedTurnStartsNoPrompt(t *testing.T) {
	t.Parallel()
	out := &agenttest.Stdin{}
	b, sink := newACPTurnBase(t, out)
	follow := &followUpBase{pending: []string{"kept"}}
	b.hooks.FollowUpPrompt = follow.take

	require.NoError(t, b.SendInput("hi", nil))
	b.noteACPInterruptRequested()
	b.HandleJSONRPCResponseForTest(providerkit.ParseLine([]byte(`{"jsonrpc":"2.0","id":1,"result":{"stopReason":"cancelled"}}`)))

	testutil.RequireEventually(t, func() bool { return len(sink.TurnActives()) == 2 })
	assert.Equal(t, 1, strings.Count(out.String(), `"method":"session/prompt"`))
	follow.mu.Lock()
	defer follow.mu.Unlock()
	assert.Equal(t, []bool{true}, follow.stoppedAt, "the provider learns the turn was stopped, and decides what becomes of its input")
	assert.Equal(t, []string{"kept"}, follow.pending, "the base took nothing from a stopped turn")
}

func TestFollowUpPrompt_AStoppedAgentStartsAndReportsNothing(t *testing.T) {
	t.Parallel()
	out := &agenttest.Stdin{}
	b, sink := newACPTurnBase(t, out)
	b.hooks.FollowUpPrompt = func(bool) (string, []*leapmuxv1.Attachment, bool) { return "lost", nil, true }
	require.True(t, b.BeginAgentTurn())
	b.SetStoppedForTest(true)

	b.EndAgentTurn(json.RawMessage(agentTurnEndFrame))

	assert.False(t, b.PromptActive())
	last, _ := sink.LastTurnActive()
	assert.False(t, last)
	assert.NotContains(t, out.String(), `"method":"session/prompt"`)
	assert.Empty(t, sink.LeapMuxNotifications(), "a stopped agent starts nothing and reports nothing")
}

// brokenStdin refuses every write, as the stdin of a process that exited does.
type brokenStdin struct{}

func (brokenStdin) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
func (brokenStdin) Close() error              { return nil }

func TestFollowUpPrompt_AFailedSendEndsTheTurnAndStatesTheFailure(t *testing.T) {
	t.Parallel()
	b, sink := newACPTurnBase(t, brokenStdin{})
	b.hooks.FollowUpPrompt = func(bool) (string, []*leapmuxv1.Attachment, bool) { return "lost", nil, true }
	require.True(t, b.BeginAgentTurn())

	b.EndAgentTurn(json.RawMessage(agentTurnEndFrame))

	// The follow-up prompt goes out on its own goroutine, so its failure lands
	// after EndAgentTurn returns.
	testutil.RequireEventually(t, func() bool { return !b.PromptActive() }, "a follow-up that never started leaves the agent idle")
	last, _ := sink.LastTurnActive()
	assert.False(t, last)
	notifications := sink.LeapMuxNotifications()
	require.Len(t, notifications, 1, "the reader learns that the message never reached the agent")
	assert.Equal(t, contracts.NotificationTypeAgentError, notifications[0][contracts.NotificationFieldType])
}

func TestSteersByOwnRoute_TheTurnIsSteerable(t *testing.T) {
	t.Parallel()
	out := &agenttest.Stdin{}
	b, sink := newACPTurnBase(t, out)
	assert.False(t, b.SupportsSteering(), "no method and no route")

	b.hooks.SteersByOwnRoute = true
	assert.True(t, b.SupportsSteering())
	require.True(t, b.BeginAgentTurn())
	state := b.PublishTurnActive()
	assert.True(t, state.Steerable, "SupportsSteering and the published flag read one answer")
	for _, kind := range sink.TurnKinds() {
		assert.Equal(t, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, kind, "each published turn is steerable")
	}
}

// A provider ends its own turn on the reader goroutine. ClearContext holds the
// session lock for the whole session/new round trip, whose response only that
// reader delivers, so the end must not wait for the session lock.
func TestAgentTurn_EndDoesNotWaitForTheSessionLock(t *testing.T) {
	t.Parallel()
	out := &agenttest.Stdin{}
	b, sink := newACPTurnBase(t, out)
	require.True(t, b.BeginAgentTurn())
	b.SessionMuForTest().Lock()
	defer b.SessionMuForTest().Unlock()

	ended := make(chan struct{})
	go func() {
		defer close(ended)
		b.EndAgentTurn(json.RawMessage(agentTurnEndFrame))
	}()

	select {
	case <-ended:
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("the end of an agent turn waited for the session lock")
	}
	assert.False(t, b.PromptActive())
	assert.Len(t, turnEndContents(sink), 1)
}

// A provider ends its own turn on the reader goroutine, and only that goroutine
// drains the agent's stdout. The follow-up prompt waits for its stdin write, and
// an agent that is blocked on its own stdout reads no stdin. So the end of the
// turn must not wait for that write: both sides would wait for each other.
func TestAgentTurn_EndDoesNotWaitForTheFollowUpWrite(t *testing.T) {
	t.Parallel()
	pipeReader, pipeWriter := io.Pipe() // An agent that reads no stdin now.
	t.Cleanup(func() { _ = pipeReader.Close() })
	b, _ := newACPTurnBase(t, pipeWriter)
	b.hooks.FollowUpPrompt = func(bool) (string, []*leapmuxv1.Attachment, bool) {
		return "typed during the turn", nil, true
	}
	require.True(t, b.BeginAgentTurn())

	ended := make(chan struct{})
	go func() {
		defer close(ended)
		b.EndAgentTurn(json.RawMessage(agentTurnEndFrame))
	}()
	select {
	case <-ended:
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("the end of an agent turn waited for the stdin write of the follow-up prompt")
	}
	assert.True(t, b.PromptActive(), "the follow-up prompt holds the busy state while its write waits")

	// The agent reads again, and the follow-up prompt arrives.
	line := make(chan string, 1)
	go func() {
		text, _ := bufio.NewReader(pipeReader).ReadString('\n')
		line <- text
	}()
	select {
	case text := <-line:
		assert.Contains(t, text, `"method":"session/prompt"`)
		assert.Contains(t, text, "typed during the turn")
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("the follow-up prompt never reached the agent")
	}
}

// toolCallFrame is a session/update that opens or updates one tool call of
// session-1.
func toolCallFrame(update string) []byte {
	return []byte(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"session-1","update":` + update + `}}`)
}

// closingRows returns the completion of each closing row of spanID, in order.
func closingRows(sink *agenttest.Sink, spanID string) []agent.MessageCompletion {
	var completions []agent.MessageCompletion
	for _, message := range sink.Messages() {
		if message.SpanID == spanID && message.Closing {
			completions = append(completions, message.Completion)
		}
	}
	return completions
}

// An agent can start its turn before the base processed the end of the prompt
// before it: Qwen Code starts the first round of a goal so. The output that the
// agent turn streams in that window belongs to the agent turn. The prompt's end
// closes only what the PROMPT left open, and its text stays apart from the
// agent turn's text.
func TestAgentTurn_AQueuedTurnKeepsItsOwnOutputAtThePromptEnd(t *testing.T) {
	t.Parallel()
	out := &agenttest.Stdin{}
	b, sink := newACPTurnBase(t, out)
	require.NoError(t, b.SendInput("/goal ship it", nil))
	b.HandleOutput(acptest.Chunk("session-1", "agent_message_chunk", "Goal set."))
	b.HandleOutput(toolCallFrame(`{"sessionUpdate":"tool_call","toolCallId":"prompt-tool","title":"Plan","kind":"think","status":"in_progress"}`))

	require.True(t, b.BeginAgentTurn(), "the goal round waits for the prompt")
	b.HandleOutput(acptest.Chunk("session-1", "agent_message_chunk", "Working on the goal."))
	b.HandleOutput(toolCallFrame(`{"sessionUpdate":"tool_call","toolCallId":"goal-tool","title":"Build","kind":"execute","status":"in_progress"}`))

	b.HandleJSONRPCResponseForTest(promptResponse(1))
	testutil.RequireEventually(t, func() bool { return b.AgentTurnActive() })

	assert.Equal(t, []agent.MessageCompletion{agent.MessageCompletionError}, closingRows(sink, "prompt-tool"),
		"the prompt's end closes the tool that the prompt left open")
	assert.Empty(t, closingRows(sink, "goal-tool"), "the goal round's tool still runs")

	b.HandleOutput(toolCallFrame(`{"sessionUpdate":"tool_call_update","toolCallId":"goal-tool","status":"completed","content":[{"type":"content","content":{"type":"text","text":"ok"}}]}`))
	b.EndAgentTurn(json.RawMessage(agentTurnEndFrame))

	// A tool that finished states its own status in its frame, so its closing row
	// sets no completion.
	assert.Equal(t, []agent.MessageCompletion{""}, closingRows(sink, "goal-tool"),
		"the goal round's tool closes once, with its own status")
	messages := sink.Messages()
	assert.Equal(t, []string{"text:Goal set.", "text:Working on the goal."}, assembledTexts(t, messages),
		"the prompt's text and the goal round's text are separate rows")
	var toolUses []string
	for _, message := range messages {
		if message.TurnEnd {
			toolUses = append(toolUses, string(message.Metadata))
		}
	}
	require.Len(t, toolUses, 2, "each turn writes its divider")
	assert.JSONEq(t, `{"`+contracts.MessageMetadataFieldToolUses+`":1}`, toolUses[0], "the prompt counts its own tool")
	assert.JSONEq(t, `{"`+contracts.MessageMetadataFieldToolUses+`":1}`, toolUses[1], "the goal round counts its own tool")
}

// A tool call that the prompt left open can still end after the agent started
// its own turn. It counts for the prompt, and the prompt's end does not close it
// a second time.
func TestAgentTurn_APromptToolThatEndsAfterTheBoundaryCountsForThePrompt(t *testing.T) {
	t.Parallel()
	out := &agenttest.Stdin{}
	b, sink := newACPTurnBase(t, out)
	require.NoError(t, b.SendInput("hi", nil))
	b.HandleOutput(toolCallFrame(`{"sessionUpdate":"tool_call","toolCallId":"prompt-tool","title":"Read","kind":"read","status":"in_progress"}`))
	require.True(t, b.BeginAgentTurn())

	b.HandleOutput(toolCallFrame(`{"sessionUpdate":"tool_call_update","toolCallId":"prompt-tool","status":"completed"}`))
	b.HandleJSONRPCResponseForTest(promptResponse(1))
	testutil.RequireEventually(t, func() bool { return b.AgentTurnActive() })
	b.EndAgentTurn(json.RawMessage(agentTurnEndFrame))

	assert.Equal(t, []agent.MessageCompletion{""}, closingRows(sink, "prompt-tool"), "the tool closes once, with its own status")
	var toolUses []string
	for _, message := range sink.Messages() {
		if message.TurnEnd {
			toolUses = append(toolUses, string(message.Metadata))
		}
	}
	require.Len(t, toolUses, 2)
	assert.JSONEq(t, `{"`+contracts.MessageMetadataFieldToolUses+`":1}`, toolUses[0], "the prompt counts its own tool")
	assert.JSONEq(t, `{"`+contracts.MessageMetadataFieldToolUses+`":0}`, toolUses[1], "the agent turn used no tool")
}

// A prompt that fails while an agent turn waits behind it closes only its own
// open tool calls. The agent turn keeps its output and runs on.
func TestAgentTurn_AFailedPromptKeepsTheQueuedTurnsOutput(t *testing.T) {
	t.Parallel()
	out := &agenttest.Stdin{}
	b, sink := newACPTurnBase(t, out)
	require.NoError(t, b.SendInput("/compress", nil))
	b.HandleOutput(toolCallFrame(`{"sessionUpdate":"tool_call","toolCallId":"prompt-tool","title":"Plan","kind":"think","status":"in_progress"}`))
	require.True(t, b.BeginAgentTurn())
	b.HandleOutput(toolCallFrame(`{"sessionUpdate":"tool_call","toolCallId":"goal-tool","title":"Build","kind":"execute","status":"in_progress"}`))

	b.HandleJSONRPCResponseForTest(providerkit.ParseLine([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"Internal error"}}`)))
	testutil.RequireEventually(t, func() bool { return b.AgentTurnActive() })

	assert.Equal(t, []agent.MessageCompletion{agent.MessageCompletionError}, closingRows(sink, "prompt-tool"))
	assert.Empty(t, closingRows(sink, "goal-tool"), "the agent turn's tool still runs")
}

// newExitedACPTurnBase is newACPTurnBase over a process that already exited,
// so Stop and Wait run their whole sequence and return at once. No prompt
// request may be in flight on it: its response wait sees the exit and fails the
// prompt. A test sets promptActive instead.
func newExitedACPTurnBase(t *testing.T) (*Base, *agenttest.Sink) {
	t.Helper()
	out := &agenttest.Stdin{}
	b, sink := newACPTurnBase(t, out)
	exited := make(chan struct{})
	close(exited)
	b.Process = providerkit.NewProcessFrom(providerkit.ProcessConfig{
		AgentID:     "test-agent",
		Stdin:       out,
		Ctx:         t.Context(),
		ProcessDone: exited,
	})
	return b, sink
}

// openAllTurns opens a prompt with a tool call, an agent turn queued behind it
// with a tool call of its own, and a child conversation with a tool call and
// some text.
func openAllTurns(t *testing.T, b *Base) {
	t.Helper()
	b.Mu.Lock()
	b.promptActive = true
	b.Mu.Unlock()
	b.HandleOutput(toolCallFrame(`{"sessionUpdate":"tool_call","toolCallId":"prompt-tool","title":"Plan","kind":"think","status":"in_progress"}`))
	require.True(t, b.BeginAgentTurn())
	b.HandleOutput(toolCallFrame(`{"sessionUpdate":"tool_call","toolCallId":"goal-tool","title":"Build","kind":"execute","status":"in_progress"}`))
	b.ApplySubagentObservation(&SubagentObservation{RowKey: "call-bg", ChildAgentKey: "call-bg", Status: bgtask.StatusRunning})
	require.True(t, b.FeedChildUpdate("call-bg", json.RawMessage(`{"sessionUpdate":"tool_call","toolCallId":"child-tool","title":"Run","kind":"execute","status":"in_progress"}`)))
	require.True(t, b.FeedChildUpdate("call-bg", json.RawMessage(`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"cut off"}}`)))
}

// childTextCompletion returns the completion of the one assembled text row of
// the child transcript.
func childTextCompletion(t *testing.T, child *agenttest.Sink) agent.MessageCompletion {
	t.Helper()
	var completions []agent.MessageCompletion
	for _, message := range child.Messages() {
		if _, _, completion, ok := decodeAssembled(message.Content); ok {
			completions = append(completions, completion)
		}
	}
	require.Len(t, completions, 1)
	return completions[0]
}

// Stop ends the prompt, the agent turn that waits behind it, and every child
// conversation, so every open tool call of the three closes as a stop.
func TestAgentTurn_StopClosesTheToolsOfBothTurns(t *testing.T) {
	t.Parallel()
	b, sink := newExitedACPTurnBase(t)
	openAllTurns(t, b)

	b.Stop()

	interrupted := []agent.MessageCompletion{agent.MessageCompletionInterrupted}
	assert.Equal(t, interrupted, closingRows(sink, "prompt-tool"))
	assert.Equal(t, interrupted, closingRows(sink, "goal-tool"))
	child := sink.Child("child-of-call-bg")
	assert.Equal(t, interrupted, closingRows(child, "child-tool"))
	assert.Equal(t, agent.MessageCompletionInterrupted, childTextCompletion(t, child))
	assert.False(t, b.PromptActive())
	assert.False(t, b.AgentTurnActive())
	last, published := sink.LastTurnActive()
	require.True(t, published)
	assert.False(t, last, "a stopped agent is idle")
}

// A process that exits by itself ends both turns as a failure, because nobody
// stopped them. Each child conversation ends as stopped: the process that fed it
// is gone, whatever the cause.
func TestAgentTurn_AProcessExitEndsBothTurnsAsAFailure(t *testing.T) {
	t.Parallel()
	b, sink := newExitedACPTurnBase(t)
	openAllTurns(t, b)

	require.NoError(t, b.Wait())

	failed := []agent.MessageCompletion{agent.MessageCompletionError}
	assert.Equal(t, failed, closingRows(sink, "prompt-tool"))
	assert.Equal(t, failed, closingRows(sink, "goal-tool"))
	child := sink.Child("child-of-call-bg")
	assert.Equal(t, []agent.MessageCompletion{agent.MessageCompletionInterrupted}, closingRows(child, "child-tool"))
	assert.Equal(t, agent.MessageCompletionInterrupted, childTextCompletion(t, child))
}

// Work that the reader stopped ends its open tool calls as a stop, not as a
// failure, and the live progress resets.
func TestAgentTurn_EndWithoutRowAfterAStopStoresTheOpenToolAsInterrupted(t *testing.T) {
	t.Parallel()
	out := &agenttest.Stdin{}
	b, sink := newACPTurnBase(t, out)
	require.True(t, b.BeginAgentTurn())
	b.HandleOutput(toolCallFrame(`{"sessionUpdate":"tool_call","toolCallId":"call-1","title":"Compact","kind":"other","status":"in_progress"}`))
	b.noteACPInterruptRequested()

	b.EndAgentTurnWithoutRow()

	assert.Equal(t, []agent.MessageCompletion{agent.MessageCompletionInterrupted}, closingRows(sink, "call-1"))
	assert.Empty(t, turnEndContents(sink))
	assert.False(t, b.PromptActive())
	assert.False(t, b.InterruptRequestedForTest(), "the stop ends with the work that it stopped")
	updates := sink.ProgressUpdates()
	require.NotEmpty(t, updates)
	assert.Equal(t, agent.ResetProgress(), updates[len(updates)-1], "the live progress of the work resets")
}

// A provider can hand over the end frame of a queued turn in the buffer of the
// line that the reader read. The base keeps its own copy until the prompt ends,
// so a reader that reuses the buffer does not rewrite the row.
func TestAgentTurn_AQueuedTurnKeepsACopyOfItsEndFrame(t *testing.T) {
	t.Parallel()
	out := &agenttest.Stdin{}
	b, sink := newACPTurnBase(t, out)
	require.NoError(t, b.SendInput("hi", nil))
	require.True(t, b.BeginAgentTurn())
	frame := []byte(agentTurnEndFrame)

	b.EndAgentTurn(frame)
	copy(frame, bytes.Repeat([]byte("x"), len(frame)))

	b.HandleJSONRPCResponseForTest(promptResponse(1))
	testutil.RequireEventually(t, func() bool { return !b.PromptActive() })
	ends := turnEndContents(sink)
	require.Len(t, ends, 2)
	assert.JSONEq(t, agentTurnEndFrame, ends[1])
}

// The boundary stores the prompt's thought at once, so the thought of the agent
// turn that follows starts a row of its own.
func TestAgentTurn_TheBoundaryStoresThePromptsThought(t *testing.T) {
	t.Parallel()
	out := &agenttest.Stdin{}
	b, sink := newACPTurnBase(t, out)
	require.NoError(t, b.SendInput("/goal ship it", nil))
	b.HandleOutput(acptest.Chunk("session-1", "agent_thought_chunk", "Prompt thinks."))

	require.True(t, b.BeginAgentTurn())
	assert.Equal(t, []string{"thought:Prompt thinks."}, assembledTexts(t, sink.Messages()), "the boundary stores the prompt's thought at once")

	b.HandleOutput(acptest.Chunk("session-1", "agent_thought_chunk", "Goal thinks."))
	b.HandleJSONRPCResponseForTest(promptResponse(1))
	testutil.RequireEventually(t, func() bool { return b.AgentTurnActive() })
	assert.Equal(t, []string{"thought:Prompt thinks."}, assembledTexts(t, sink.Messages()), "the prompt's end takes nothing of the agent turn")
	b.EndAgentTurn(json.RawMessage(agentTurnEndFrame))

	assert.Equal(t, []string{"thought:Prompt thinks.", "thought:Goal thinks."}, assembledTexts(t, sink.Messages()))
}

// A context clear replaces the session, and the agent turn that waited behind
// the prompt of the old session belongs to that session. The swap drops it: no
// turn stays busy, a late end of the old turn writes nothing, and the next
// agent turn runs at once.
func TestAgentTurn_ASessionSwapDropsAQueuedTurn(t *testing.T) {
	t.Parallel()
	a, _ := newTestAgentForRPCWithResponder(t, func(method string) agenttest.RPCReply {
		if method == MethodSessionNew {
			return agenttest.RPCReply{Result: json.RawMessage(`{"sessionId":"session-2"}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	sink := &agenttest.Sink{}
	a.sink = agent.NewProviderServices(sink)
	a.Mu.Lock()
	a.promptActive = true
	a.Mu.Unlock()
	require.True(t, a.BeginAgentTurn())

	_, err := a.ClearContext()
	require.NoError(t, err)

	assert.False(t, a.PromptActive())
	assert.False(t, a.AgentTurnActive())
	a.EndAgentTurn(json.RawMessage(agentTurnEndFrame))
	assert.Empty(t, turnEndContents(sink), "the end of the dropped turn writes no divider")
	require.True(t, a.BeginAgentTurn())
	assert.True(t, a.AgentTurnActive(), "the next agent turn runs at once, with no prompt ahead of it")
}

// A prompt that fails ends like any other: the input that the provider accepted
// for it and never read becomes the next prompt, after the failure note.
func TestFollowUpPrompt_AFailedPromptStillSendsTheInputThatItNeverRead(t *testing.T) {
	t.Parallel()
	out := &agenttest.Stdin{}
	b, sink := newACPTurnBase(t, out)
	follow := &followUpBase{pending: []string{"also check the tests"}}
	b.hooks.FollowUpPrompt = follow.take
	require.NoError(t, b.SendInput("hi", nil))

	b.HandleJSONRPCResponseForTest(providerkit.ParseLine([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"Internal error"}}`)))

	testutil.RequireEventually(t, func() bool { return strings.Count(out.String(), `"method":"session/prompt"`) == 2 })
	assert.Contains(t, out.String(), "also check the tests")
	require.Len(t, sink.LeapMuxNotifications(), 1, "the failure is still reported")
	assert.True(t, b.PromptActive())
	assert.Equal(t, []bool{true}, sink.TurnActives(), "no idle state is published between the two prompts")
	follow.mu.Lock()
	defer follow.mu.Unlock()
	assert.Equal(t, []bool{false}, follow.stoppedAt)
}

// Hooks.PromptParams adjusts each session/prompt, the follow-up prompt that the
// base starts by itself included.
func TestPromptParams_TheHookAdjustsTheFollowUpPrompt(t *testing.T) {
	t.Parallel()
	out := &agenttest.Stdin{}
	b, _ := newACPTurnBase(t, out)
	b.hooks.PromptParams = func(params map[string]any) {
		params["_meta"] = map[string]any{"promptId": "follow-up"}
	}
	b.hooks.FollowUpPrompt = (&followUpBase{pending: []string{"typed during the turn"}}).take
	require.True(t, b.BeginAgentTurn())

	b.EndAgentTurn(json.RawMessage(agentTurnEndFrame))

	testutil.RequireEventually(t, func() bool { return strings.Contains(out.String(), `"method":"session/prompt"`) })
	var frame struct {
		Params struct {
			Prompt []struct {
				Text string `json:"text"`
			} `json:"prompt"`
			Meta map[string]any `json:"_meta"`
		} `json:"params"`
	}
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(out.String())), &frame))
	require.Len(t, frame.Params.Prompt, 1)
	assert.Equal(t, "typed during the turn", frame.Params.Prompt[0].Text)
	assert.Equal(t, map[string]any{"promptId": "follow-up"}, frame.Params.Meta)
}

// A follow-up prompt belongs to the session whose turn ended, and to a running
// agent. When a context clear replaced that session first, or the agent
// stopped, the prompt starts nowhere: the turn ends, and the reader learns that
// the message never reached the agent.
func TestFollowUpPrompt_AStaleTargetStartsNoPrompt(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		sessionID string
		stopped   bool
	}{
		"a session that a clear replaced": {sessionID: "previous-session"},
		"an agent that stopped":           {sessionID: "session-1", stopped: true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			out := &agenttest.Stdin{}
			b, sink := newACPTurnBase(t, out)
			b.Mu.Lock()
			b.promptActive = true
			b.Mu.Unlock()
			b.SetStoppedForTest(tc.stopped)

			b.sendFollowUpPrompt(tc.sessionID, "lost", nil)

			assert.Empty(t, out.String(), "no prompt reaches the agent")
			assert.False(t, b.PromptActive())
			notifications := sink.LeapMuxNotifications()
			require.Len(t, notifications, 1)
			assert.Equal(t, contracts.NotificationTypeAgentError, notifications[0][contracts.NotificationFieldType])
			assert.Contains(t, notifications[0][contracts.NotificationFieldError], "no longer active")
		})
	}
}

// One agent turn at most waits behind a prompt, so a second boundary before
// the prompt's end changes nothing: the agent turn keeps its text, and the
// prompt keeps the tools of the first boundary.
func TestACPTurnOutput_ASecondBoundaryChangesNothing(t *testing.T) {
	t.Parallel()
	var output acpTurnOutput
	output.appendAssistant("prompt text")
	output.rememberIncompleteTool("prompt-tool", map[string]json.RawMessage{"toolCallId": json.RawMessage(`"prompt-tool"`)},
		json.RawMessage(`{"sessionUpdate":"tool_call","toolCallId":"prompt-tool","status":"pending"}`))
	output.completeTool("done-tool")

	output.turnMu.Lock()
	text, thought := output.markPromptBoundaryLocked()
	output.turnMu.Unlock()
	assert.Equal(t, "prompt text", text)
	assert.Empty(t, thought)

	output.appendAssistant("agent text")
	output.rememberIncompleteTool("agent-tool", map[string]json.RawMessage{"toolCallId": json.RawMessage(`"agent-tool"`)},
		json.RawMessage(`{"sessionUpdate":"tool_call","toolCallId":"agent-tool","status":"pending"}`))
	output.turnMu.Lock()
	text, thought = output.markPromptBoundaryLocked()
	output.turnMu.Unlock()
	assert.Empty(t, text, "a second boundary takes no text of the agent turn")
	assert.Empty(t, thought)

	prompt, bounded := output.drainPromptTurn()
	require.True(t, bounded)
	assert.Empty(t, prompt.assistantText)
	assert.Equal(t, 1, prompt.completedToolUses, "the prompt keeps the count of the first boundary")
	require.Len(t, prompt.incompleteTools, 1)
	assert.Equal(t, "prompt-tool", prompt.incompleteTools[0].toolCallID, "the prompt keeps the tools of the first boundary")

	rest := output.drainTurn()
	assert.Equal(t, "agent text", rest.assistantText)
	assert.Zero(t, rest.completedToolUses)
	require.Len(t, rest.incompleteTools, 1)
	assert.Equal(t, "agent-tool", rest.incompleteTools[0].toolCallID)
}

// An agent that the worker restarts discards its output. A prompt that fails
// then, with an agent turn queued behind it, closes no tool call: the output of
// a process that is going away writes no row.
func TestAgentTurn_AFailedPromptOfADiscardingAgentWritesNoRow(t *testing.T) {
	t.Parallel()
	out := &agenttest.Stdin{}
	b, sink := newACPTurnBase(t, out)
	require.NoError(t, b.SendInput("hi", nil))
	b.HandleOutput(toolCallFrame(`{"sessionUpdate":"tool_call","toolCallId":"prompt-tool","title":"Plan","kind":"think","status":"in_progress"}`))
	require.True(t, b.BeginAgentTurn())
	b.DiscardOutput()

	b.HandleJSONRPCResponseForTest(providerkit.ParseLine([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"Internal error"}}`)))
	testutil.RequireEventually(t, func() bool { return b.AgentTurnActive() })

	assert.Empty(t, closingRows(sink, "prompt-tool"))
}
