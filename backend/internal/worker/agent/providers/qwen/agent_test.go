package qwen

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/quartz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp/acptest"
)

// qwenTestSession is the session id that the test peer attaches.
const qwenTestSession = "session-1"

// newQwenAgent builds a Qwen agent over a fake peer that answers each request
// through respond, with Qwen's own hooks, a recording sink and clock.
func newQwenAgent(t *testing.T, clock quartz.Clock, respond func(agenttest.RecordedRequest) agenttest.RPCReply) (*Agent, *agenttest.ControlSink, func() []agenttest.RecordedRequest) {
	t.Helper()
	if respond == nil {
		respond = func(agenttest.RecordedRequest) agenttest.RPCReply {
			return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
		}
	}
	if clock == nil {
		clock = quartz.NewReal()
	}
	a, requests := acptest.NewAgentForRPCWithRequestResponder(t,
		func() *Agent { return &Agent{} },
		func(a *Agent) *acp.Base { return &a.Base },
		respond,
	)
	sink := &agenttest.ControlSink{}
	a.SetSinkForTest(agent.NewProviderServices(sink))
	*a.HooksForTest() = a.configure(clock)
	t.Cleanup(a.stopAllBackgroundTranscripts)
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

// sessionUpdate encodes one ACP session update of the test session.
func sessionUpdate(t *testing.T, update map[string]any) []byte {
	t.Helper()
	return frame(t, map[string]any{
		"method": "session/update",
		"params": map[string]any{"sessionId": qwenTestSession, "update": update},
	})
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

// requestsFor returns the recorded frames of one method.
func requestsFor(requests []agenttest.RecordedRequest, method string) []agenttest.RecordedRequest {
	var out []agenttest.RecordedRequest
	for _, request := range requests {
		if request.Method == method {
			out = append(out, request)
		}
	}
	return out
}

// rawLines joins the frames that the agent wrote, one per line.
func rawLines(requests []agenttest.RecordedRequest) string {
	var out string
	for _, request := range requests {
		out += request.Raw + "\n"
	}
	return out
}

// drain asks the agent for the steered input, as Qwen does after a tool batch,
// and returns the reply.
func drain(t *testing.T, a *Agent, requests func() []agenttest.RecordedRequest, id int, sessionID string) map[string]any {
	t.Helper()
	return drainWithParams(t, a, requests, id, map[string]any{"sessionId": sessionID, "todoStopGuardWatchQueuedPrompt": true})
}

// drainWithParams is drain with the params that the request states.
func drainWithParams(t *testing.T, a *Agent, requests func() []agenttest.RecordedRequest, id int, params map[string]any) map[string]any {
	t.Helper()
	a.HandleOutput(frame(t, map[string]any{"id": id, "method": qwenDrainMethod, "params": params}))
	syncPeer(t, a)
	results := agenttest.JSONRPCResultsByID(t, rawLines(requests()))
	raw, ok := results[jsonNumber(id)]
	require.True(t, ok, "the agent answers every drain")
	var reply map[string]any
	require.NoError(t, json.Unmarshal([]byte(raw), &reply))
	return reply
}

func jsonNumber(id int) string {
	data, _ := json.Marshal(id)
	return string(data)
}

func TestQwenSteerInputIsDrainedByTheNextToolBatch(t *testing.T) {
	t.Parallel()
	a, _, requests := newQwenAgent(t, nil, nil)
	a.SetPromptActiveForTest(true)

	require.NoError(t, a.SteerInput("also mention steering", nil))
	require.NoError(t, a.SteerInput("and the tests", []*leapmuxv1.Attachment{{
		Filename: "notes.txt", MimeType: "text/plain", Data: []byte("line 4"),
	}}))

	reply := drain(t, a, requests, 7, qwenTestSession)
	assert.Equal(t, false, reply["hasQueuedPrompt"])
	items, ok := reply["items"].([]any)
	require.True(t, ok)
	require.Len(t, items, 2)
	first := items[0].(map[string]any)
	assert.Equal(t, "also mention steering", first["displayText"])
	assert.Equal(t, []any{map[string]any{"type": "text", "text": "also mention steering"}}, first["content"])
	second := items[1].(map[string]any)
	assert.Len(t, second["content"], 2, "the attachment rides as a block of its own")

	again := drain(t, a, requests, 8, qwenTestSession)
	assert.Empty(t, again["items"], "a drained message is not sent twice")
}

func TestQwenDrainTakesAtMostTenItems(t *testing.T) {
	t.Parallel()
	a, _, requests := newQwenAgent(t, nil, nil)
	a.SetPromptActiveForTest(true)
	for i := range 12 {
		require.NoError(t, a.SteerInput(string(rune('a'+i)), nil))
	}

	assert.Len(t, drain(t, a, requests, 1, qwenTestSession)["items"], 10, "Qwen reads ten items and drops the rest")
	assert.Len(t, drain(t, a, requests, 2, qwenTestSession)["items"], 2, "the rest wait for the next drain")
}

func TestQwenDrainOfAnotherSessionIsEmpty(t *testing.T) {
	t.Parallel()
	a, _, requests := newQwenAgent(t, nil, nil)
	a.SetPromptActiveForTest(true)
	require.NoError(t, a.SteerInput("kept", nil))

	assert.Empty(t, drain(t, a, requests, 1, "other-session")["items"])
	assert.Len(t, drain(t, a, requests, 2, qwenTestSession)["items"], 1, "the input waits for its own session")
}

func TestQwenSteerInputRefusesAnIdleSession(t *testing.T) {
	t.Parallel()
	a, _, _ := newQwenAgent(t, nil, nil)
	assert.ErrorIs(t, a.SteerInput("late", nil), agent.ErrNoActiveTurn)
	assert.True(t, a.SupportsSteering(), "Qwen steers by its own pull, which no handshake advertises")
}

func TestQwenFollowUpPromptSendsWhatNoDrainTook(t *testing.T) {
	t.Parallel()
	a, _, _ := newQwenAgent(t, nil, nil)
	a.SetPromptActiveForTest(true)
	require.NoError(t, a.SteerInput("first", nil))
	require.NoError(t, a.SteerInput("second", []*leapmuxv1.Attachment{{Filename: "a.png", MimeType: "image/png", Data: []byte{1}}}))

	content, attachments, ok := a.followUpPrompt(false)
	require.True(t, ok)
	assert.Equal(t, "first\n\nsecond", content)
	assert.Len(t, attachments, 1)

	_, _, ok = a.followUpPrompt(false)
	assert.False(t, ok, "the input goes once")
}

func TestQwenStoppedTurnDropsItsInputAndSaysSo(t *testing.T) {
	t.Parallel()
	a, sink, _ := newQwenAgent(t, nil, nil)
	a.SetPromptActiveForTest(true)
	require.NoError(t, a.SteerInput("never read", nil))
	require.NoError(t, a.SteerInput("never read either", nil))

	_, _, ok := a.followUpPrompt(true)
	assert.False(t, ok, "a stopped turn starts nothing")
	notifications := sink.Notifications()
	require.Len(t, notifications, 1)
	assert.Equal(t, contracts.NotificationTypeAgentStatus, notifications[0][contracts.NotificationFieldType])
	assert.Equal(t, "2 messages sent during the stopped turn did not reach Qwen Code", notifications[0][contracts.NotificationFieldText])

	_, _, ok = a.followUpPrompt(false)
	assert.False(t, ok, "the dropped input stays dropped")
}

func TestQwenFollowUpPromptWithNothingQueuedIsSilent(t *testing.T) {
	t.Parallel()
	a, sink, _ := newQwenAgent(t, nil, nil)
	_, _, ok := a.followUpPrompt(true)
	assert.False(t, ok)
	assert.Empty(t, sink.Notifications())
}

func TestQwenJoinSteerItemsSkipsBlankText(t *testing.T) {
	t.Parallel()
	_, _, ok := joinSteerItems([]steerItem{{content: "  "}})
	assert.False(t, ok, "a blank message with no attachment carries nothing")
	content, attachments, ok := joinSteerItems([]steerItem{{content: " ", attachments: []*leapmuxv1.Attachment{{Filename: "a"}}}})
	assert.True(t, ok)
	assert.Empty(t, content)
	assert.Len(t, attachments, 1)
	assert.Equal(t, "1 message", pluralMessages(1))
}

func TestQwenCompactContextSendsTheCompressCommand(t *testing.T) {
	t.Parallel()
	a, _, requests := newQwenAgent(t, nil, nil)

	require.NoError(t, a.CompactContext())

	testutil.RequireEventually(t, func() bool { return len(requestsFor(requests(), acp.MethodSessionPrompt)) == 1 })
	blocks := requestsFor(requests(), acp.MethodSessionPrompt)[0].Params["prompt"].([]any)
	require.Len(t, blocks, 1)
	assert.Equal(t, qwenCompressCommand, blocks[0].(map[string]any)["text"])
}

func TestQwenClearDropsTheSessionState(t *testing.T) {
	t.Parallel()
	a, _, _ := newQwenAgent(t, nil, nil)
	a.SetPromptActiveForTest(true)
	require.NoError(t, a.SteerInput("for the old session", nil))
	a.tools.remember("call-1", contracts.QwenToolAgent, nil)
	// A foreground spawn that runs, and a background child whose transcript
	// the agent reads.
	a.HandleOutput(rawUpdate(t, foregroundSpawnFrames[0]))
	a.HandleOutput(rawUpdate(t, `{"sessionUpdate":"tool_call","toolCallId":"call_686a7e3e21","status":"pending","title":"Agent","kind":"other","rawInput":{},"_meta":{"toolName":"agent"}}`))
	a.HandleOutput(backgroundLaunchResult(t, subagentTranscriptPath(t)))
	a.stateMu.Lock()
	require.Len(t, a.children.spawns, 1)
	tail := a.children.tails["call_686a7e3e21"]
	a.stateMu.Unlock()
	require.NotNil(t, tail)

	a.clearProviderState()

	_, _, ok := a.followUpPrompt(false)
	assert.False(t, ok, "input for the old session never reaches the new one")
	a.stateMu.Lock()
	assert.Empty(t, a.tools.byCall)
	assert.Empty(t, a.children.spawns, "no spawn of the old session stays open")
	assert.Empty(t, a.children.tails)
	a.stateMu.Unlock()
	select {
	case <-tail.stopped:
	default:
		t.Fatal("the reader of the old session's background transcript still runs")
	}
}

func TestQwenSteerQueueTake(t *testing.T) {
	t.Parallel()
	var q steerQueue
	assert.Empty(t, q.take(0), "an empty queue gives nothing")
	assert.Empty(t, q.take(qwenMaxDrainItems))

	a, b, c, d := steerItem{content: "a"}, steerItem{content: "b"}, steerItem{content: "c"}, steerItem{content: "d"}
	q.items = []steerItem{a, b, c}
	assert.Equal(t, []steerItem{a, b, c}, q.take(-1), "a negative limit takes every item")
	assert.Empty(t, q.items)

	q.items = []steerItem{a, b, c}
	taken := q.take(2)
	q.items = append(q.items, d)
	assert.Equal(t, []steerItem{a, b}, taken, "an item that is queued after the take does not change what the take returned")
	assert.Equal(t, []steerItem{c, d}, q.take(qwenMaxDrainItems), "a limit above the length takes what is left, oldest first")
	assert.Empty(t, q.items)
}

func TestQwenJoinSteerItemsKeepsTheOrder(t *testing.T) {
	t.Parallel()
	first := &leapmuxv1.Attachment{Filename: "1.txt"}
	second := &leapmuxv1.Attachment{Filename: "2.txt"}
	third := &leapmuxv1.Attachment{Filename: "3.txt"}

	content, attachments, ok := joinSteerItems([]steerItem{
		{content: "a", attachments: []*leapmuxv1.Attachment{first}},
		{content: " \n ", attachments: []*leapmuxv1.Attachment{second}},
		{content: "b", attachments: []*leapmuxv1.Attachment{third}},
	})

	require.True(t, ok)
	assert.Equal(t, "a\n\nb", content, "a blank message adds no empty paragraph")
	assert.Equal(t, []*leapmuxv1.Attachment{first, second, third}, attachments, "the attachment of a blank message still goes, in order")
}

// The worker's goroutines steer while the reader answers Qwen's drains. Each
// message reaches Qwen exactly once, through a drain or the follow-up prompt,
// and the messages of one sender keep their order.
func TestQwenConcurrentSteersReachQwenOnceInOrder(t *testing.T) {
	t.Parallel()
	a, _, requests := newQwenAgent(t, nil, nil)
	a.SetPromptActiveForTest(true)
	const senders, perSender, drains = 4, 25, 12

	var wg sync.WaitGroup
	start := make(chan struct{})
	for sender := range senders {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := range perSender {
				assert.NoError(t, a.SteerInput(fmt.Sprintf("s%d-m%02d", sender, i), nil))
			}
		}()
	}
	close(start)
	for id := 1; id <= drains; id++ {
		a.HandleOutput(frame(t, map[string]any{"id": id, "method": qwenDrainMethod, "params": map[string]any{"sessionId": qwenTestSession}}))
	}
	wg.Wait()
	syncPeer(t, a)

	var got []string
	results := agenttest.JSONRPCResultsByID(t, rawLines(requests()))
	for id := 1; id <= drains; id++ {
		raw, ok := results[jsonNumber(id)]
		require.True(t, ok, "the agent answers drain %d", id)
		var reply struct {
			Items []struct {
				DisplayText string `json:"displayText"`
			} `json:"items"`
		}
		require.NoError(t, json.Unmarshal([]byte(raw), &reply))
		assert.LessOrEqual(t, len(reply.Items), qwenMaxDrainItems)
		for _, item := range reply.Items {
			got = append(got, item.DisplayText)
		}
	}
	if rest, _, ok := a.followUpPrompt(false); ok {
		got = append(got, strings.Split(rest, "\n\n")...)
	}

	require.Len(t, got, senders*perSender, "no message is lost or sent twice")
	for sender := range senders {
		prefix := fmt.Sprintf("s%d-", sender)
		var mine []string
		for _, text := range got {
			if strings.HasPrefix(text, prefix) {
				mine = append(mine, text)
			}
		}
		want := make([]string, perSender)
		for i := range want {
			want[i] = fmt.Sprintf("s%d-m%02d", sender, i)
		}
		assert.Equal(t, want, mine, "the messages of sender %d keep their order", sender)
	}
}

// decodeAssembledText reads one assembled-message row: its kind and its text.
func decodeAssembledText(content []byte) (kind, text string, ok bool) {
	var envelope map[string]string
	if json.Unmarshal(content, &envelope) != nil || envelope[contracts.AssembledMessageFieldType] != contracts.AssembledMessageType {
		return "", "", false
	}
	return envelope[contracts.AssembledMessageFieldKind], envelope[contracts.AssembledMessageFieldText], true
}
