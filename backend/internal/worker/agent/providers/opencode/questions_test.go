package opencode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/opencode/opencodetest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// bridgeFakeDaemon starts a question bridge against daemon, with sink as its
// services.
func bridgeFakeDaemon(t *testing.T, daemon *opencodetest.FakeDaemon, sink agent.ControlServices) (*openCodeQuestions, context.Context) {
	t.Helper()
	questions := &openCodeQuestions{}
	questions.Configure(sink)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	questions.beginAt(ctx, "agent-1", daemon.Server.URL)
	return questions, ctx
}

func TestOpenCodeQuestions_PublishesAnAskedQuestion(t *testing.T) {
	t.Parallel()
	daemon := opencodetest.NewFakeDaemon(t, EventRoute, QuestionRoot)
	sink := &agenttest.ControlSink{}
	bridgeFakeDaemon(t, daemon, sink)
	daemon.Send(opencodetest.AskedEvent(opencodetest.FakeQuestion))

	var published []agenttest.ControlRequestRecord
	require.Eventually(t, func() bool {
		published = sink.PublishedControls()
		return len(published) == 1
	}, 3*time.Second, 5*time.Millisecond, "the question reaches the reader")
	assert.Equal(t, "opencode-question:que_1", published[0].RequestID)
	// The payload is the shape the browser plugin reads: the event type beside the
	// question's own properties.
	assert.JSONEq(t, `{"type":"`+contracts.OpenCodeEventQuestionAsked+`","properties":`+opencodetest.FakeQuestion+`}`, string(published[0].Payload))
}

// The stored request must carry NO top-level `id`. restoreControlResponseID reads a
// request that has one as a JSON-RPC request and withholds every answer whose id
// does not match it -- and the event envelope's `evt_...` id never would. Publishing
// the envelope unchanged therefore makes each answer unsendable, with the reader
// seeing only a refusal.
func TestOpenCodeQuestions_PublishedPayloadDoesNotWithholdTheAnswer(t *testing.T) {
	t.Parallel()
	daemon := opencodetest.NewFakeDaemon(t, EventRoute, QuestionRoot)
	sink := &agenttest.ControlSink{}
	bridgeFakeDaemon(t, daemon, sink)
	daemon.Send(opencodetest.AskedEvent(opencodetest.FakeQuestion))

	var published []agenttest.ControlRequestRecord
	require.Eventually(t, func() bool {
		published = sink.PublishedControls()
		return len(published) == 1
	}, 3*time.Second, 5*time.Millisecond, "the question reaches the reader")

	answer := []byte(`{"jsonrpc":"2.0","id":"opencode-question:que_1","result":{"answers":[["Inspect"]]}}`)
	resolved := acp.Provider{}.ResolveControlResponse(agent.ControlResponseContext{
		RequestID:       published[0].RequestID,
		RequestPayload:  published[0].Payload,
		ResponseContent: answer,
	})
	assert.False(t, resolved.Withhold, "the answer reaches the daemon")
	assert.Equal(t, answer, resolved.Content)
}

func TestOpenCodeQuestions_AnswersThroughTheDaemonRoute(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		frame  string
		action string
		body   string
	}{
		{"an answer", `{"jsonrpc":"2.0","id":"opencode-question:que_1","result":{"answers":[["Inspect"]]}}`, "reply", `{"answers":[["Inspect"]]}`},
		{"an empty answer", `{"jsonrpc":"2.0","id":"opencode-question:que_1","result":{"answers":[[]]}}`, "reply", `{"answers":[[]]}`},
		{"a rejection", `{"jsonrpc":"2.0","id":"opencode-question:que_1","result":{"rejected":true}}`, "reject", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			daemon := opencodetest.NewFakeDaemon(t, EventRoute, QuestionRoot)
			sink := &agenttest.ControlSink{}
			questions, ctx := bridgeFakeDaemon(t, daemon, sink)
			daemon.Send(opencodetest.AskedEvent(opencodetest.FakeQuestion))
			require.Eventually(t, func() bool {
				return len(sink.PublishedControls()) == 1
			}, 3*time.Second, 5*time.Millisecond, "the question reaches the reader")

			handled, err := questions.answer(ctx, []byte(tc.frame))
			require.NoError(t, err)
			assert.True(t, handled, "the bridge takes the frame instead of the stdio stream")
			answers := daemon.Answers()
			require.Len(t, answers, 1)
			assert.Equal(t, "que_1", answers[0].QuestionID)
			assert.Equal(t, tc.action, answers[0].Action)
			if tc.body == "" {
				assert.Empty(t, answers[0].Body)
			} else {
				assert.JSONEq(t, tc.body, answers[0].Body)
			}
		})
	}
}

// Every other control answer belongs to the ACP stream. A bridge that claimed one
// would swallow every permission decision the same agent makes.
func TestOpenCodeQuestions_LeavesEveryOtherFrameToTheStream(t *testing.T) {
	t.Parallel()
	daemon := opencodetest.NewFakeDaemon(t, EventRoute, QuestionRoot)
	questions, ctx := bridgeFakeDaemon(t, daemon, &agenttest.ControlSink{})
	for _, frame := range []string{
		`{"jsonrpc":"2.0","id":7,"result":{"outcome":{"optionId":"once"}}}`,
		`{"jsonrpc":"2.0","id":"opencode-question:que_unknown","result":{"answers":[[]]}}`,
		`{"jsonrpc":"2.0","method":"session/prompt","params":{}}`,
		`not json at all`,
	} {
		handled, err := questions.answer(ctx, []byte(frame))
		assert.False(t, handled, frame)
		assert.NoError(t, err, frame)
	}
	assert.Empty(t, daemon.Answers())
}

// An answer the daemon refuses must reach the reader as a failure. Reporting success
// would retire a card whose question the daemon still blocks on.
func TestOpenCodeQuestions_ReportsARefusedAnswer(t *testing.T) {
	t.Parallel()
	daemon := opencodetest.NewFakeDaemon(t, EventRoute, QuestionRoot)
	sink := &agenttest.ControlSink{}
	questions, ctx := bridgeFakeDaemon(t, daemon, sink)
	daemon.Send(opencodetest.AskedEvent(opencodetest.FakeQuestion))
	require.Eventually(t, func() bool {
		return len(sink.PublishedControls()) == 1
	}, 3*time.Second, 5*time.Millisecond, "the question reaches the reader")
	daemon.Mu.Lock()
	daemon.FailNext = 1
	daemon.Mu.Unlock()

	handled, err := questions.answer(ctx, []byte(`{"jsonrpc":"2.0","id":"opencode-question:que_1","result":{"answers":[["Inspect"]]}}`))
	assert.True(t, handled)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "404")
}

// A frame this build cannot read is still the bridge's, so it must not fall through
// to the stdio stream, where it would be written as a response to nothing.
func TestOpenCodeQuestions_RefusesAnUnreadableAnswer(t *testing.T) {
	t.Parallel()
	daemon := opencodetest.NewFakeDaemon(t, EventRoute, QuestionRoot)
	sink := &agenttest.ControlSink{}
	questions, ctx := bridgeFakeDaemon(t, daemon, sink)
	daemon.Send(opencodetest.AskedEvent(opencodetest.FakeQuestion))
	require.Eventually(t, func() bool {
		return len(sink.PublishedControls()) == 1
	}, 3*time.Second, 5*time.Millisecond, "the question reaches the reader")

	handled, err := questions.answer(ctx, []byte(`{"jsonrpc":"2.0","id":"opencode-question:que_1","result":{}}`))
	assert.True(t, handled)
	require.Error(t, err)
	assert.Empty(t, daemon.Answers())
}

func TestOpenCodeQuestions_WithdrawsASettledQuestion(t *testing.T) {
	t.Parallel()
	for _, event := range []string{contracts.OpenCodeEventQuestionReplied, contracts.OpenCodeEventQuestionRejected} {
		t.Run(event, func(t *testing.T) {
			t.Parallel()
			daemon := opencodetest.NewFakeDaemon(t, EventRoute, QuestionRoot)
			sink := &agenttest.ControlSink{}
			bridgeFakeDaemon(t, daemon, sink)
			daemon.Send(opencodetest.AskedEvent(opencodetest.FakeQuestion))
			require.Eventually(t, func() bool {
				return len(sink.PublishedControls()) == 1
			}, 3*time.Second, 5*time.Millisecond, "the question reaches the reader")

			daemon.Send("data: {\"id\":\"evt_2\",\"type\":\"" + event + "\",\"properties\":{\"sessionID\":\"ses_9\",\"requestID\":\"que_1\"}}\n\n")
			require.Eventually(t, func() bool {
				return len(sink.CanceledControls()) == 1
			}, 3*time.Second, 5*time.Millisecond, "the card is retired")
			assert.Equal(t, []string{"opencode-question:que_1"}, sink.CanceledControls())
		})
	}
}

// A question the daemon settled once must not retire a second card. The answer path
// and the event path both reach the record, and only the one that takes it acts.
func TestOpenCodeQuestions_WithdrawsOnlyOnce(t *testing.T) {
	t.Parallel()
	daemon := opencodetest.NewFakeDaemon(t, EventRoute, QuestionRoot)
	sink := &agenttest.ControlSink{}
	questions, ctx := bridgeFakeDaemon(t, daemon, sink)
	daemon.Send(opencodetest.AskedEvent(opencodetest.FakeQuestion))
	require.Eventually(t, func() bool {
		return len(sink.PublishedControls()) == 1
	}, 3*time.Second, 5*time.Millisecond, "the question reaches the reader")

	handled, err := questions.answer(ctx, []byte(`{"jsonrpc":"2.0","id":"opencode-question:que_1","result":{"answers":[["Inspect"]]}}`))
	require.NoError(t, err)
	require.True(t, handled)
	daemon.Send("data: {\"id\":\"evt_2\",\"type\":\"" + contracts.OpenCodeEventQuestionReplied + "\",\"properties\":{\"requestID\":\"que_1\"}}\n\n")

	require.Eventually(t, func() bool {
		return len(daemon.Answers()) == 1
	}, 3*time.Second, 5*time.Millisecond, "the daemon has the answer")
	assert.Empty(t, sink.CanceledControls(), "the answered card is retired by the service, not twice by the bridge")
}

// A question raised before LeapMux connected raises no event on the new connection.
// The pending list is the only thing that carries it, on the first connection as
// well as after a reconnect.
func TestOpenCodeQuestions_PublishesAQuestionRaisedBeforeItConnected(t *testing.T) {
	t.Parallel()
	daemon := opencodetest.NewFakeDaemon(t, EventRoute, QuestionRoot)
	daemon.SetPending(opencodetest.FakeQuestion)
	sink := &agenttest.ControlSink{}
	bridgeFakeDaemon(t, daemon, sink)

	var published []agenttest.ControlRequestRecord
	require.Eventually(t, func() bool {
		published = sink.PublishedControls()
		return len(published) == 1
	}, 3*time.Second, 5*time.Millisecond, "the waiting question reaches the reader")
	assert.Equal(t, "opencode-question:que_1", published[0].RequestID)
}

// The list the daemon holds is AUTHORITATIVE on every connection, in both directions.
//
// A question answered in ANOTHER client while the event stream was down raises its
// `question.replied` on a connection LeapMux no longer holds, so nothing withdrew the
// card: the reader kept a live question card for a question the agent had moved past,
// and a click on one of its options reached a daemon that refuses it.
func TestOpenCodeQuestions_RetiresACardTheDaemonNoLongerHolds(t *testing.T) {
	t.Parallel()
	daemon := opencodetest.NewFakeDaemon(t, EventRoute, QuestionRoot)
	daemon.SetPending(opencodetest.FakeQuestion)
	sink := &agenttest.ControlSink{}
	questions, ctx := bridgeFakeDaemon(t, daemon, sink)
	require.Eventually(t, func() bool {
		return len(sink.PublishedControls()) == 1
	}, 3*time.Second, 5*time.Millisecond, "the waiting question reaches the reader")

	// The daemon still holds it, so a second list re-announces the card and retires
	// nothing.
	questions.publishPending(ctx)
	assert.Empty(t, sink.CanceledControls())

	// Another client answered it while the stream was down, so the daemon's list no
	// longer carries it.
	daemon.SetPending()
	questions.publishPending(ctx)
	assert.Equal(t, []string{"opencode-question:que_1"}, sink.CanceledControls())
}

// A list the bridge could not READ retires nothing. A failed read is not a statement
// that the daemon holds no question, and retiring on one would drop every live card
// each time the daemon was busy.
func TestOpenCodeQuestions_KeepsEveryCardWhenTheListIsUnreadable(t *testing.T) {
	t.Parallel()
	daemon := opencodetest.NewFakeDaemon(t, EventRoute, QuestionRoot)
	daemon.SetPending(opencodetest.FakeQuestion)
	sink := &agenttest.ControlSink{}
	questions, ctx := bridgeFakeDaemon(t, daemon, sink)
	require.Eventually(t, func() bool {
		return len(sink.PublishedControls()) == 1
	}, 3*time.Second, 5*time.Millisecond, "the waiting question reaches the reader")

	questions.mu.Lock()
	questions.baseURL = "http://" + net.JoinHostPort(openCodeQuestionHost, "1")
	questions.mu.Unlock()
	questions.publishPending(ctx)
	assert.Empty(t, sink.CanceledControls())
}

// A reader who will never see the question must not leave the turn blocked on it.
func TestOpenCodeQuestions_RejectsAQuestionItCannotPublish(t *testing.T) {
	t.Parallel()
	daemon := opencodetest.NewFakeDaemon(t, EventRoute, QuestionRoot)
	sink := &agenttest.ControlSink{PublicationError: assert.AnError}
	bridgeFakeDaemon(t, daemon, sink)
	daemon.Send(opencodetest.AskedEvent(opencodetest.FakeQuestion))

	var answers []opencodetest.FakeAnswer
	require.Eventually(t, func() bool {
		answers = daemon.Answers()
		return len(answers) == 1
	}, 3*time.Second, 5*time.Millisecond, "the daemon is released")
	assert.Equal(t, "que_1", answers[0].QuestionID)
	assert.Equal(t, "reject", answers[0].Action)
}

// The daemon dies with the agent, so a card it left behind is retired here or never.
func TestOpenCodeQuestions_RetiresEveryCardWhenTheAgentStops(t *testing.T) {
	t.Parallel()
	daemon := opencodetest.NewFakeDaemon(t, EventRoute, QuestionRoot)
	sink := &agenttest.ControlSink{}
	questions := &openCodeQuestions{}
	questions.Configure(sink)
	ctx, cancel := context.WithCancel(context.Background())
	questions.beginAt(ctx, "agent-1", daemon.Server.URL)
	daemon.Send(opencodetest.AskedEvent(opencodetest.FakeQuestion))
	require.Eventually(t, func() bool {
		return len(sink.PublishedControls()) == 1
	}, 3*time.Second, 5*time.Millisecond, "the question reaches the reader")

	cancel()
	require.Eventually(t, func() bool {
		return len(sink.CanceledControls()) == 1
	}, 3*time.Second, 5*time.Millisecond, "the card is retired")
	assert.Equal(t, []string{"opencode-question:que_1"}, sink.CanceledControls())
}

// A payload that CONTAINS newlines travels as one `data:` line for each of them,
// and the standard rejoins them with a newline. Indented JSON is that payload. A
// reader that took each line for a whole event would parse none of them, and a
// reader that dropped the joining newline would concatenate two tokens.
func TestOpenCodeQuestions_ReadsAMultiLineEvent(t *testing.T) {
	t.Parallel()
	daemon := opencodetest.NewFakeDaemon(t, EventRoute, QuestionRoot)
	sink := &agenttest.ControlSink{}
	bridgeFakeDaemon(t, daemon, sink)
	whole := `{"id":"evt_1","type":"` + contracts.OpenCodeEventQuestionAsked + `","properties":` + opencodetest.FakeQuestion + `}`
	var indented bytes.Buffer
	require.NoError(t, json.Indent(&indented, []byte(whole), "", "  "))
	var event strings.Builder
	for _, line := range strings.Split(indented.String(), "\n") {
		event.WriteString("data:" + line + "\n")
	}
	event.WriteString("\n")
	daemon.Send(event.String())

	require.Eventually(t, func() bool {
		return len(sink.PublishedControls()) == 1
	}, 3*time.Second, 5*time.Millisecond, "the indented question reaches the reader")
}

// The stream can end in the middle of an event. That event is discarded, not
// handled half read: the reconnect that follows restates the pending list, so
// the question is not lost with it. A whole event before the cut still counts.
func TestOpenCodeQuestions_DiscardsAnEventThatTheStreamCutOff(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	questions := &openCodeQuestions{}
	questions.Configure(sink)
	second := strings.ReplaceAll(opencodetest.FakeQuestion, "que_1", "que_2")
	cut := strings.TrimSuffix(opencodetest.AskedEvent(second), "\n\n")

	err := questions.readEvents(context.Background(), strings.NewReader(opencodetest.AskedEvent(opencodetest.FakeQuestion)+cut))

	require.NoError(t, err, "a stream that ends is no failure")
	published := sink.PublishedControls()
	require.Len(t, published, 1)
	assert.Equal(t, "opencode-question:que_1", published[0].RequestID)
}

// An event larger than the limit fails the stream rather than growing the
// buffer without limit. The bridge then reconnects, and the pending list
// restates what the stream dropped.
func TestOpenCodeQuestions_RefusesAnEventOverTheLimit(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	questions := &openCodeQuestions{}
	questions.Configure(sink)
	huge := "data: " + strings.Repeat("x", openCodeQuestionMaxEvent+1) + "\n\n"

	err := questions.readEvents(context.Background(), strings.NewReader(huge))

	require.Error(t, err)
	assert.Empty(t, sink.PublishedControls())
}

// A bridge nobody configured must take no frame. Every agent test that builds a bare
// agent reaches SendRawInput through it, and a bridge that claimed a frame there
// would answer a daemon that does not exist.
func TestOpenCodeQuestions_ZeroValueTakesNothing(t *testing.T) {
	t.Parallel()
	var questions openCodeQuestions
	handled, err := questions.answer(context.Background(), []byte(`{"jsonrpc":"2.0","id":"opencode-question:que_1","result":{"answers":[[]]}}`))
	assert.False(t, handled)
	assert.NoError(t, err)
}

// The daemon binds no server without these, and every question then stays
// unanswerable with nothing in the transcript to say why. The port is 0 because the
// daemon chooses it: a port chosen here is released before the daemon binds it, and
// whatever takes it in between kills the session rather than just the questions.
func TestOpenCodeACPArgs_LetTheDaemonChooseItsPort(t *testing.T) {
	t.Parallel()
	assert.Equal(t, []string{"acp", "--hostname", "127.0.0.1", "--port", "0"}, ACPArgs())
}

func TestOpenCodeQuestionControlID_SeparatesTheStdioRows(t *testing.T) {
	t.Parallel()
	// The ACP stream keys its own rows under "jsonrpc:", so neither can match the other.
	assert.Equal(t, "opencode-question:que_1", openCodeQuestionControlID("que_1"))
	identity, valid := agent.NewControlRequestIdentity(json.RawMessage(`7`))
	require.True(t, valid)
	assert.NotEqual(t, identity.Key, openCodeQuestionControlID("que_1"))
}

// Discovery against the REAL operating system, with this test process as the
// subject. It exercises whatever gopsutil does on the platform the suite runs on --
// `lsof` on macOS, /proc on Linux, the IP helper API on Windows -- which no fake can
// stand in for. The daemon states its port nowhere, so this walk is the only thing
// between a running server and an unanswerable question.
func TestOpenCodeQuestions_DiscoversTheServerOfARunningProcess(t *testing.T) {
	// NOT parallel, and it cannot be. The subject process is this one, so every
	// question server any other test starts is an equally correct answer to the
	// search. A sequential test runs before any parallel test resumes, which is what
	// keeps this test's own daemon the only one alive. In production the subject is
	// the launched daemon, which serves one.
	daemon := opencodetest.NewFakeDaemon(t, EventRoute, QuestionRoot)
	questions := &openCodeQuestions{}
	questions.Configure(&agenttest.ControlSink{})

	base, err := questions.discover(context.Background(), int32(os.Getpid()))
	require.NoError(t, err)
	assert.Equal(t, daemon.Server.URL, base)
}

// The subject process holds other listening sockets, and one of them answers this
// path with something else. Only the question list identifies the daemon, so a
// candidate that fails that check must be passed over rather than answered to.
func TestOpenCodeQuestions_SkipsAPortThatIsNotTheDaemon(t *testing.T) {
	// Sequential for the reason the search above states.
	for _, decoy := range []http.HandlerFunc{
		func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "no", http.StatusNotFound) },
		func(w http.ResponseWriter, _ *http.Request) { _, _ = fmt.Fprint(w, `{"not":"an array"}`) },
		func(w http.ResponseWriter, _ *http.Request) { _, _ = fmt.Fprint(w, `<html>hello</html>`) },
	} {
		other := httptest.NewServer(decoy)
		t.Cleanup(other.Close)
	}
	daemon := opencodetest.NewFakeDaemon(t, EventRoute, QuestionRoot)
	questions := &openCodeQuestions{}
	questions.Configure(&agenttest.ControlSink{})

	base, err := questions.discover(context.Background(), int32(os.Getpid()))
	require.NoError(t, err)
	assert.Equal(t, daemon.Server.URL, base)
}

// A process with no question server must give up rather than block the goroutine for
// the life of the session. The reader loses questions and keeps the session.
func TestOpenCodeQuestions_GivesUpWhenNoServerAnswers(t *testing.T) {
	t.Parallel()
	questions := &openCodeQuestions{}
	questions.Configure(&agenttest.ControlSink{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := questions.discover(ctx, int32(os.Getpid()))
	require.Error(t, err)
}

// The launched process is the shell. A POSIX shell EXECs the daemon, so the pid is
// already the daemon's; PowerShell starts it as a child instead. The walk must
// therefore include the process ITSELF, or every POSIX platform finds nothing.
func TestOpenCodeDescendants_IncludesTheProcessItself(t *testing.T) {
	t.Parallel()
	found := openCodeDescendants(context.Background(), int32(os.Getpid()))
	require.NotEmpty(t, found)
	assert.Equal(t, int32(os.Getpid()), found[0])
}

// The per-process socket read, which is what the search asks FIRST.
//
// Every platform whose shell can `exec` leaves the daemon AS the launched process, so
// this answers and the process-table walk never runs. Reading the table first cost one
// `Ppid` syscall for every process on the host, on each of the 80 searches a fixed
// 250 ms poll ran over the timeout.
func TestOpenCodePidPorts_ReadsTheProcessOwnListeningSockets(t *testing.T) {
	// Sequential for the reason the search tests above state: the subject process is
	// this one, so another test's server is an equally correct answer.
	daemon := opencodetest.NewFakeDaemon(t, EventRoute, QuestionRoot)
	_, portText, err := net.SplitHostPort(strings.TrimPrefix(daemon.Server.URL, "http://"))
	require.NoError(t, err)
	port, err := strconv.Atoi(portText)
	require.NoError(t, err)

	assert.Contains(t, openCodePidPorts(context.Background(), int32(os.Getpid())), port)
}

// A process that holds no socket -- or that this user may not read -- answers an empty
// list rather than an error, because a daemon that has not bound yet is the normal
// case on the first search.
func TestOpenCodePidPorts_TolerateAnAbsentProcess(t *testing.T) {
	t.Parallel()
	assert.Empty(t, openCodePidPorts(context.Background(), 1<<30))
}

// An unreadable or absent process answers the process alone, never an error: a
// daemon that has not bound yet is the normal case on the first poll.
func TestOpenCodeDescendants_TolerateAnAbsentProcess(t *testing.T) {
	t.Parallel()
	// A pid this high is not in use; the walk must still answer.
	assert.Equal(t, []int32{1 << 30}, openCodeDescendants(context.Background(), 1<<30))
}

func TestOpenCodeAnswersAQuestionThroughTheBridge(t *testing.T) {
	t.Parallel()
	a := &Agent{}
	opencodetest.AssertAnswersAQuestionThroughTheBridge(t, a, EventRoute, QuestionRoot,
		func(ctx context.Context, sink agent.ControlServices, baseURL string) {
			a.SetContextForTest(ctx)
			a.BeginQuestionsForTest(ctx, sink, "agent-1", baseURL)
		})
}

func TestOpenCodeRcMarkerNeverReachesTheDaemon(t *testing.T) {
	t.Parallel()
	opencodetest.AssertRcMarkerNeverReachesTheDaemon(t, "opencode", "OPENCODE_CLIENT", openCodeQuestionToolEnv, ACPArgs())
}
