package kiro

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp/acptest"
)

// kiroTestSession is the session id that the test peer attaches.
const kiroTestSession = "session-1"

// newKiroAgent builds a Kiro agent over a fake peer that answers each request
// through respond, with Kiro's own hooks and a recording sink.
func newKiroAgent(t *testing.T, opts agent.Options, respond func(agenttest.RecordedRequest) agenttest.RPCReply) (*Agent, *agenttest.ControlSink, func() []agenttest.RecordedRequest) {
	t.Helper()
	if respond == nil {
		respond = func(agenttest.RecordedRequest) agenttest.RPCReply {
			return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
		}
	}
	a, requests := acptest.NewAgentForRPCWithRequestResponder(t,
		func() *Agent { return &Agent{} },
		func(a *Agent) *acp.Base { return &a.Base },
		respond,
	)
	sink := &agenttest.ControlSink{}
	a.SetSinkForTest(agent.NewProviderServices(sink))
	a.WireTurnActiveForTest()
	*a.HooksForTest() = a.configure(opts)
	return a, sink, requests
}

// frame encodes one JSON-RPC message that the agent reads.
func frame(t *testing.T, message map[string]any) []byte {
	t.Helper()
	message["jsonrpc"] = "2.0"
	data, err := json.Marshal(message)
	require.NoError(t, err)
	return data
}

// sessionUpdate encodes one standard ACP session update.
func sessionUpdate(t *testing.T, sessionID string, update map[string]any) []byte {
	t.Helper()
	return frame(t, map[string]any{
		"method": "session/update",
		"params": map[string]any{"sessionId": sessionID, "update": update},
	})
}

// infoUpdate encodes one session_info_update of the main session, with its
// `_meta.kiro` fields.
func infoUpdate(t *testing.T, kiro map[string]any) []byte {
	t.Helper()
	return sessionUpdate(t, kiroTestSession, infoUpdateObject(kiro))
}

// infoUpdateObject is the update object of one session_info_update.
func infoUpdateObject(kiro map[string]any) map[string]any {
	return map[string]any{
		"sessionUpdate": "session_info_update",
		"_meta":         map[string]any{"kiro": kiro},
	}
}

// messageChunk encodes one agent_message_chunk of the main session.
func messageChunk(t *testing.T, text string, kiro map[string]any) []byte {
	t.Helper()
	update := map[string]any{
		"sessionUpdate": "agent_message_chunk",
		"content":       map[string]any{"type": "text", "text": text},
	}
	if kiro != nil {
		update["_meta"] = map[string]any{"kiro": kiro}
	}
	return sessionUpdate(t, kiroTestSession, update)
}

// syncPeer returns once the peer recorded every line that the agent wrote
// before the call. The peer reads the lines in order and answers this request
// only after it recorded each line before it, so a test that asserts that a
// line is ABSENT does not pass only because the peer did not read it yet.
func syncPeer(t *testing.T, a *Agent) {
	t.Helper()
	_, err := a.SendRequest("test/sync", nil, 30*time.Second)
	require.NoError(t, err)
}

// requestsFor returns the recorded requests of one method.
func requestsFor(requests []agenttest.RecordedRequest, method string) []agenttest.RecordedRequest {
	var out []agenttest.RecordedRequest
	for _, request := range requests {
		if request.Method == method {
			out = append(out, request)
		}
	}
	return out
}

// statusTexts returns the text of every agent status notification.
func statusTexts(sink *agenttest.ControlSink) []string {
	var out []string
	for _, notification := range sink.Notifications() {
		if notification[contracts.NotificationFieldType] == contracts.NotificationTypeAgentStatus {
			text, _ := notification[contracts.NotificationFieldText].(string)
			out = append(out, text)
		}
	}
	return out
}

// errorTexts returns the error of every agent error notification.
func errorTexts(sink *agenttest.ControlSink) []string {
	var out []string
	for _, notification := range sink.Notifications() {
		if notification[contracts.NotificationFieldType] == contracts.NotificationTypeAgentError {
			text, _ := notification[contracts.NotificationFieldError].(string)
			out = append(out, text)
		}
	}
	return out
}

// countNotifications counts the notifications of one type.
func countNotifications(sink *agenttest.ControlSink, notificationType string) int {
	count := 0
	for _, notification := range sink.Notifications() {
		if notification[contracts.NotificationFieldType] == notificationType {
			count++
		}
	}
	return count
}

func TestKiroSteerInputSendsASessionSteer(t *testing.T) {
	t.Parallel()
	a, _, requests := newKiroAgent(t, agent.Options{}, func(req agenttest.RecordedRequest) agenttest.RPCReply {
		if req.Method == kiroSteerMethod {
			return agenttest.RPCReply{Result: json.RawMessage(`{"queued":true,"messageId":"steer-1"}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	a.SetPromptActiveForTest(true)

	require.NoError(t, a.SteerInput("also mention bananas", nil))

	sent := requestsFor(requests(), kiroSteerMethod)
	require.Len(t, sent, 1)
	assert.Equal(t, kiroTestSession, sent[0].Params["sessionId"])
	assert.Equal(t, "also mention bananas", sent[0].Params["message"])
}

func TestKiroSteerInputRefusesAnIdleSession(t *testing.T) {
	t.Parallel()
	a, _, requests := newKiroAgent(t, agent.Options{}, nil)

	err := a.SteerInput("late", nil)

	assert.ErrorIs(t, err, agent.ErrNoActiveTurn)
	syncPeer(t, a)
	assert.Empty(t, requestsFor(requests(), kiroSteerMethod), "no steer reaches Kiro with no turn to read it")
}

func TestKiroSteerInputRefusesAnAttachmentAsUnsupported(t *testing.T) {
	t.Parallel()
	a, _, requests := newKiroAgent(t, agent.Options{}, nil)
	a.SetPromptActiveForTest(true)

	err := a.SteerInput("look", []*leapmuxv1.Attachment{{Filename: "shot.png", MimeType: "image/png", Data: []byte{0x89, 'P', 'N', 'G'}}})

	assert.ErrorIs(t, err, agent.ErrSteeringUnsupported, "the worker then keeps the message, attachment and all, for the next turn")
	syncPeer(t, a)
	assert.Empty(t, requestsFor(requests(), kiroSteerMethod))
}

func TestKiroSteerInputReadsADroppedSteerAsAnEndedTurn(t *testing.T) {
	t.Parallel()
	a, _, _ := newKiroAgent(t, agent.Options{}, func(req agenttest.RecordedRequest) agenttest.RPCReply {
		if req.Method == kiroSteerMethod {
			return agenttest.RPCReply{Result: json.RawMessage(`{"queued":false,"messageId":"steer-1","dropped":"epoch_changed"}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	a.SetPromptActiveForTest(true)

	err := a.SteerInput("too late", nil)

	assert.ErrorIs(t, err, agent.ErrNoActiveTurn)
	assert.Contains(t, err.Error(), "epoch_changed")
}

func TestKiroSteerInputReportsAnUnreadableReply(t *testing.T) {
	t.Parallel()
	a, _, _ := newKiroAgent(t, agent.Options{}, func(req agenttest.RecordedRequest) agenttest.RPCReply {
		if req.Method == kiroSteerMethod {
			return agenttest.RPCReply{Result: json.RawMessage(`"queued"`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	a.SetPromptActiveForTest(true)

	err := a.SteerInput("hello", nil)

	require.ErrorContains(t, err, "read the Kiro steer reply")
	assert.NotErrorIs(t, err, agent.ErrNoActiveTurn, "a reply that LeapMux cannot read is not a turn that ended")
}

// An empty attachment list carries no attachment, so the steer goes out as text.
func TestKiroSteerInputWithAnEmptyAttachmentListSteers(t *testing.T) {
	t.Parallel()
	a, _, requests := newKiroAgent(t, agent.Options{}, func(req agenttest.RecordedRequest) agenttest.RPCReply {
		if req.Method == kiroSteerMethod {
			return agenttest.RPCReply{Result: json.RawMessage(`{"queued":true,"messageId":"steer-1"}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	a.SetPromptActiveForTest(true)

	require.NoError(t, a.SteerInput("text only", []*leapmuxv1.Attachment{}))

	require.Len(t, requestsFor(requests(), kiroSteerMethod), 1)
}

// Kiro steers through a method that its initialize response does not
// advertise, so the provider states the route itself. The published turn
// reads the same answer, and the queue steers a message into it rather than
// holding it back.
func TestKiroTurnIsSteerable(t *testing.T) {
	t.Parallel()
	a, _, _ := newKiroAgent(t, agent.Options{}, nil)
	a.SetPromptActiveForTest(true)

	assert.True(t, a.SupportsSteering())
	state := a.PublishTurnActive()
	assert.True(t, state.Active)
	assert.True(t, state.Steerable)
}

func TestKiroSteerInputReportsAFailedRequest(t *testing.T) {
	t.Parallel()
	a, _, _ := newKiroAgent(t, agent.Options{}, func(req agenttest.RecordedRequest) agenttest.RPCReply {
		if req.Method == kiroSteerMethod {
			return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32603,"message":"Internal error"}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	a.SetPromptActiveForTest(true)

	err := a.SteerInput("hello", nil)

	require.Error(t, err)
	assert.False(t, errors.Is(err, agent.ErrNoActiveTurn), "a failed request is not a turn that ended")
}

// compactResponder answers the compaction with reply, after it pushes the
// updates of summarize to the agent, as Kiro does before its answer.
func compactResponder(a **Agent, t *testing.T, reply string, summarize bool) func(agenttest.RecordedRequest) agenttest.RPCReply {
	return func(req agenttest.RecordedRequest) agenttest.RPCReply {
		if req.Method != kiroCompactMethod {
			return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
		}
		if summarize {
			(*a).HandleOutput(infoUpdate(t, map[string]any{
				"kind":          kiroKindSummarizationDone,
				"summarization": map[string]any{"status": "success", "summary": map[string]any{"conversationSummary": "short", "truncated": false}},
			}))
		}
		return agenttest.RPCReply{Result: json.RawMessage(reply)}
	}
}

// waitForIdle waits until the compaction released its turn.
func waitForIdle(t *testing.T, a *Agent) {
	t.Helper()
	require.Eventually(t, func() bool { return !a.PromptActive() }, 30*time.Second, time.Millisecond)
}

func TestKiroCompactContextHoldsATurnUntilTheAnswer(t *testing.T) {
	t.Parallel()
	var a *Agent
	var sink *agenttest.ControlSink
	var requests func() []agenttest.RecordedRequest
	a, sink, requests = newKiroAgent(t, agent.Options{}, compactResponder(&a, t, `{"success":true}`, true))

	require.NoError(t, a.CompactContext())
	waitForIdle(t, a)

	sent := requestsFor(requests(), kiroCompactMethod)
	require.Len(t, sent, 1)
	assert.Equal(t, kiroTestSession, sent[0].Params["sessionId"])
	assert.Equal(t, 1, countNotifications(sink, contracts.NotificationTypeCompacting), "the compaction states its start")
	assert.Equal(t, []string{"Context compacted"}, statusTexts(sink), "Kiro's own report states the result, and the answer adds nothing")
	actives := sink.TurnActives()
	require.NotEmpty(t, actives)
	assert.Contains(t, actives, true, "the queue sees the compaction as a turn")
	assert.False(t, actives[len(actives)-1], "the answer ends that turn")
	for _, message := range sink.Messages() {
		assert.False(t, message.TurnEnd, "a compaction is no conversation turn, so it draws no divider")
	}
}

func TestKiroCompactContextStatesAnEmptyConversation(t *testing.T) {
	t.Parallel()
	var a *Agent
	var sink *agenttest.ControlSink
	a, sink, _ = newKiroAgent(t, agent.Options{}, compactResponder(&a, t, `{"success":true}`, false))

	require.NoError(t, a.CompactContext())
	waitForIdle(t, a)

	assert.Equal(t, []string{"The conversation had nothing to compact"}, statusTexts(sink))
}

func TestKiroCompactContextStatesARefusal(t *testing.T) {
	t.Parallel()
	var a *Agent
	var sink *agenttest.ControlSink
	a, sink, _ = newKiroAgent(t, agent.Options{}, compactResponder(&a, t, `{"success":false}`, false))

	require.NoError(t, a.CompactContext())
	waitForIdle(t, a)

	assert.Equal(t, []string{"Kiro did not compact the context"}, statusTexts(sink))
}

func TestKiroCompactContextStatesAFailedRequest(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, func(req agenttest.RecordedRequest) agenttest.RPCReply {
		if req.Method == kiroCompactMethod {
			return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32000,"message":"Session 'x' not found"}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})

	require.NoError(t, a.CompactContext())
	waitForIdle(t, a)

	errs := errorTexts(sink)
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "Kiro could not compact the context")
	assert.Contains(t, errs[0], "not found")
}

// heldCompaction answers the compaction only when release closes, after the
// test returns at the latest, as a Kiro whose summary call hangs does. Every
// other request takes an empty result, and session/new opens "session-2".
func heldCompaction(t *testing.T, release <-chan struct{}, reply string) func(agenttest.RecordedRequest) agenttest.RPCReply {
	t.Helper()
	return func(req agenttest.RecordedRequest) agenttest.RPCReply {
		switch req.Method {
		case kiroCompactMethod:
			<-release
			return agenttest.RPCReply{Result: json.RawMessage(reply)}
		case acp.MethodSessionNew:
			return agenttest.RPCReply{Result: json.RawMessage(`{"sessionId":"session-2"}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	}
}

// releaseOnce closes release when the test ends, unless the test closed it.
func releaseOnce(t *testing.T) (release chan struct{}, closeRelease func()) {
	t.Helper()
	release = make(chan struct{})
	var once sync.Once
	closeRelease = func() { once.Do(func() { close(release) }) }
	t.Cleanup(closeRelease)
	return release, closeRelease
}

// The reader can stop a compaction that Kiro never answers: the turn that it
// held ends, with no divider, and the late answer changes nothing.
func TestKiroInterruptEndsAPendingCompaction(t *testing.T) {
	t.Parallel()
	release, closeRelease := releaseOnce(t)
	a, sink, requests := newKiroAgent(t, agent.Options{}, heldCompaction(t, release, `{"success":false}`))
	require.NoError(t, a.CompactContext())
	require.True(t, a.PromptActive())

	require.NoError(t, a.Interrupt())

	assert.False(t, a.PromptActive(), "the stop releases the turn that the compaction held")
	assert.Equal(t, []string{"Stopped waiting for the context compaction"}, statusTexts(sink))
	for _, message := range sink.Messages() {
		assert.False(t, message.TurnEnd, "a compaction draws no divider")
	}

	// Kiro starts a turn of its own, and then answers the compaction.
	require.True(t, a.BeginAgentTurn())
	closeRelease()
	require.Eventually(t, func() bool { return len(requestsFor(requests(), kiroCompactMethod)) == 1 }, 30*time.Second, time.Millisecond)
	syncPeer(t, a)
	assert.True(t, a.AgentTurnActive(), "the late answer ends no later turn")
	assert.Equal(t, []string{"Stopped waiting for the context compaction"}, statusTexts(sink), "the late answer states nothing")
}

// A context clear opens a new session, and the compaction of the old one
// belongs to no turn of it. A new compaction can start at once.
func TestKiroClearContextDropsAPendingCompaction(t *testing.T) {
	t.Parallel()
	release, closeRelease := releaseOnce(t)
	a, sink, _ := newKiroAgent(t, agent.Options{}, heldCompaction(t, release, `{"success":false}`))
	require.NoError(t, a.CompactContext())

	a.stateMu.Lock()
	started := a.compaction.id
	a.stateMu.Unlock()
	require.NotZero(t, started)
	a.clearProviderState()

	a.stateMu.Lock()
	assert.Zero(t, a.compaction.id, "the clear drops the compaction of the old session")
	a.stateMu.Unlock()
	a.finishCompaction(started, json.RawMessage(`{"success":false}`), nil)
	assert.Empty(t, statusTexts(sink), "the answer of a dropped compaction states nothing")
	closeRelease()
}

// The ids of the compactions never repeat. So the late answer of a compaction
// that a stop dropped finds the next compaction under another id, and it
// neither ends that compaction's turn nor states its result.
func TestKiroLateAnswerOfAStoppedCompactionLeavesTheNextOneAlone(t *testing.T) {
	t.Parallel()
	release, closeRelease := releaseOnce(t)
	var compactions atomic.Int32
	a, sink, _ := newKiroAgent(t, agent.Options{}, func(req agenttest.RecordedRequest) agenttest.RPCReply {
		if req.Method != kiroCompactMethod {
			return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
		}
		if compactions.Add(1) == 1 {
			// The first compaction hangs until the test releases it, and Kiro then
			// refuses it.
			<-release
			return agenttest.RPCReply{Result: json.RawMessage(`{"success":false}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{"success":true}`)}
	})
	require.NoError(t, a.CompactContext())
	require.NoError(t, a.Interrupt())
	require.NoError(t, a.CompactContext(), "the stop released the turn, so a new compaction starts")

	closeRelease()
	waitForIdle(t, a)

	assert.Equal(t, []string{
		"Stopped waiting for the context compaction",
		"The conversation had nothing to compact",
	}, statusTexts(sink), "each answer reaches its own compaction only")
}

// A context clear opens a new session, and everything that the ids of the old
// session keyed goes with it. The compaction counter stays, so an id never
// repeats across the clear.
func TestKiroClearProviderStateForgetsWhatTheOldSessionKeyed(t *testing.T) {
	t.Parallel()
	a, _, _ := newKiroAgent(t, agent.Options{}, nil)
	a.HandleOutput(turnStart(t))
	a.HandleOutput(spawnFrame(t, "tool_call", "pending", nil))
	a.HandleOutput(userInputRequest(t, 3, "t_q"))
	a.HandleOutput(toolCall(t, "run_command_t_sh", "in_progress"))
	a.HandleOutput(contentChunk(t, kiroTestSession, "run_command_t_sh", "out"))
	a.stateMu.Lock()
	require.True(t, a.turns.agentTurn)
	require.NotEmpty(t, a.children.rowBySubtask)
	require.NotEmpty(t, a.controls.byToolCall)
	require.NotEmpty(t, a.output.running)
	a.compaction.lastID = 7
	a.stateMu.Unlock()

	a.clearProviderState()

	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	assert.Equal(t, turnState{}, a.turns)
	assert.Equal(t, childState{}, a.children)
	assert.Equal(t, controlIndex{}, a.controls)
	assert.Equal(t, toolOutputState{}, a.output)
	assert.Equal(t, compactionState{lastID: 7}, a.compaction, "the counter survives the clear")
}

// While Kiro compacts, the compaction holds the turn, so a second one is
// refused as busy and sends nothing.
func TestKiroCompactContextRefusesASecondCompaction(t *testing.T) {
	t.Parallel()
	release, _ := releaseOnce(t)
	a, _, requests := newKiroAgent(t, agent.Options{}, heldCompaction(t, release, `{"success":true}`))
	require.NoError(t, a.CompactContext())

	assert.ErrorIs(t, a.CompactContext(), agent.ErrAgentBusy)
	require.Eventually(t, func() bool { return len(requestsFor(requests(), kiroCompactMethod)) == 1 }, 30*time.Second, time.Millisecond)
}

// Two callers can pass the busy check before either one records its
// compaction. The record is one step under the lock, so exactly one
// compaction starts and exactly one request reaches Kiro.
func TestKiroCompactContextStartsOneCompactionForConcurrentCalls(t *testing.T) {
	t.Parallel()
	release, closeRelease := releaseOnce(t)
	a, _, requests := newKiroAgent(t, agent.Options{}, heldCompaction(t, release, `{"success":true}`))
	const callers = 8
	errs := make([]error, callers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs[i] = a.CompactContext()
		}()
	}
	close(start)
	wg.Wait()

	started := 0
	for _, err := range errs {
		if err == nil {
			started++
			continue
		}
		// A caller that lost the race finds the turn of the winner, or the
		// compaction that the winner recorded.
		assert.True(t, errors.Is(err, agent.ErrAgentBusy) || strings.Contains(err.Error(), "already pending"), "unexpected refusal: %v", err)
	}
	assert.Equal(t, 1, started, "exactly one compaction starts")

	closeRelease()
	waitForIdle(t, a)
	syncPeer(t, a)
	assert.Len(t, requestsFor(requests(), kiroCompactMethod), 1, "exactly one request reaches Kiro")
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	assert.Zero(t, a.compaction.id, "the answer ends the one compaction")
}

// A compaction whose request cannot reach Kiro waits for no answer: the turn
// that it opened ends at once, with no divider, and no compaction stays
// recorded to refuse the next one.
func TestKiroCompactContextThatCannotBeSentReleasesItsTurn(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)
	a.SetStdinForTest(agenttest.FailingStdin{})

	require.Error(t, a.CompactContext())

	assert.False(t, a.PromptActive(), "the turn that the compaction opened ends")
	actives := sink.TurnActives()
	require.NotEmpty(t, actives)
	assert.False(t, actives[len(actives)-1], "the queue sees the turn end")
	assert.Empty(t, turnEnds(sink.Messages()), "a compaction draws no divider")
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	assert.Zero(t, a.compaction.id, "no compaction waits for an answer that cannot come")
}

// A stop with no compaction pending leaves a turn that Kiro started to its own
// end marker, and states nothing of a compaction.
func TestKiroInterruptWithoutACompactionLeavesKirosTurnToItsMarker(t *testing.T) {
	t.Parallel()
	a, sink, _ := newKiroAgent(t, agent.Options{}, nil)
	a.HandleOutput(turnStart(t))
	require.True(t, a.AgentTurnActive())

	require.NoError(t, a.Interrupt())

	assert.True(t, a.AgentTurnActive(), "the stop ends no turn by itself")
	assert.Empty(t, statusTexts(sink), "no compaction waited, so the stop states nothing of one")
	a.HandleOutput(turnEnd(t, "cancelled"))
	assert.False(t, a.PromptActive())
	assert.Len(t, turnEnds(sink.Messages()), 1, "Kiro's marker ends its turn with a divider")
}

// A compaction holds the turn of the queue. Kiro can start a turn of its own
// while it compacts, and the end marker of that turn must not release the turn
// that the compaction holds: the queue would then send a prompt into a
// compaction that runs.
func TestKiroTurnOfKirosOwnDuringACompactionKeepsTheCompactionTurn(t *testing.T) {
	t.Parallel()
	release, closeRelease := releaseOnce(t)
	a, _, _ := newKiroAgent(t, agent.Options{}, heldCompaction(t, release, `{"success":false}`))
	require.NoError(t, a.CompactContext())

	a.HandleOutput(turnStart(t))
	a.HandleOutput(messageChunk(t, "A workflow finished.", agentInitiated))
	a.HandleOutput(turnEnd(t, "end_turn"))

	assert.True(t, a.PromptActive(), "only the answer of the compaction ends the turn that it holds")
	closeRelease()
	waitForIdle(t, a)
}

func TestKiroCompactContextStatesAnUnreadableReply(t *testing.T) {
	t.Parallel()
	var a *Agent
	var sink *agenttest.ControlSink
	a, sink, _ = newKiroAgent(t, agent.Options{}, compactResponder(&a, t, `"done"`, false))

	require.NoError(t, a.CompactContext())
	waitForIdle(t, a)

	errs := errorTexts(sink)
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], "Kiro answered the compaction with an unreadable reply")
}

func TestKiroCompactContextRefusesARunningTurn(t *testing.T) {
	t.Parallel()
	a, sink, requests := newKiroAgent(t, agent.Options{}, nil)
	a.SetPromptActiveForTest(true)

	assert.ErrorIs(t, a.CompactContext(), agent.ErrAgentBusy)

	syncPeer(t, a)
	assert.Empty(t, requestsFor(requests(), kiroCompactMethod))
	assert.Zero(t, countNotifications(sink, contracts.NotificationTypeCompacting))
}
