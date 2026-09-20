package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeOpenCodeDaemon answers the two routes the question bridge reads, and lets a
// test push events onto the stream the way the daemon does.
type fakeOpenCodeDaemon struct {
	server *httptest.Server

	mu       sync.Mutex
	events   chan string
	pending  []string
	replies  []fakeOpenCodeAnswer
	failNext int
}

type fakeOpenCodeAnswer struct {
	QuestionID string
	Action     string
	Body       string
}

func newFakeOpenCodeDaemon(t *testing.T) *fakeOpenCodeDaemon {
	t.Helper()
	daemon := &fakeOpenCodeDaemon{events: make(chan string, 8)}
	mux := http.NewServeMux()
	mux.HandleFunc(openCodeEventRoute, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		require.True(t, ok, "the test server streams")
		flusher.Flush()
		for {
			select {
			case <-r.Context().Done():
				return
			case event := <-daemon.events:
				_, _ = fmt.Fprint(w, event)
				flusher.Flush()
			}
		}
	})
	mux.HandleFunc(openCodeQuestionRoot, func(w http.ResponseWriter, _ *http.Request) {
		daemon.mu.Lock()
		pending := "[" + strings.Join(daemon.pending, ",") + "]"
		daemon.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, pending)
	})
	mux.HandleFunc(openCodeQuestionRoot+"/", func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, openCodeQuestionRoot), "/"), "/")
		require.Len(t, parts, 2, "the route is /question/{id}/{action}")
		body, _ := io.ReadAll(r.Body)
		daemon.mu.Lock()
		fail := daemon.failNext > 0
		if fail {
			daemon.failNext--
		} else {
			daemon.replies = append(daemon.replies, fakeOpenCodeAnswer{QuestionID: parts[0], Action: parts[1], Body: string(body)})
		}
		daemon.mu.Unlock()
		if fail {
			http.Error(w, "the question is gone", http.StatusNotFound)
			return
		}
		_, _ = fmt.Fprint(w, `true`)
	})
	daemon.server = httptest.NewServer(mux)
	t.Cleanup(daemon.server.Close)
	return daemon
}

func (d *fakeOpenCodeDaemon) bridge(t *testing.T, sink ControlServices) (*openCodeQuestions, context.Context) {
	t.Helper()
	questions := &openCodeQuestions{}
	questions.configure(sink)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	questions.beginAt(ctx, "agent-1", d.server.URL)
	return questions, ctx
}

func (d *fakeOpenCodeDaemon) send(event string) { d.events <- event }

func (d *fakeOpenCodeDaemon) answers() []fakeOpenCodeAnswer {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]fakeOpenCodeAnswer(nil), d.replies...)
}

func (d *fakeOpenCodeDaemon) setPending(questions ...string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.pending = questions
}

const fakeOpenCodeQuestion = `{"id":"que_1","sessionID":"ses_9","questions":[{"question":"Which one?","header":"Task","options":[{"label":"Inspect","description":"Look only"}]}]}`

func openCodeAskedEvent(properties string) string {
	return "data: {\"id\":\"evt_1\",\"type\":\"" + contracts.OpenCodeEventQuestionAsked + "\",\"properties\":" + properties + "}\n\n"
}

func TestOpenCodeQuestions_PublishesAnAskedQuestion(t *testing.T) {
	t.Parallel()
	daemon := newFakeOpenCodeDaemon(t)
	sink := &recordingControlSink{}
	daemon.bridge(t, sink)
	daemon.send(openCodeAskedEvent(fakeOpenCodeQuestion))

	var published []controlRequestRecord
	require.Eventually(t, func() bool {
		published = sink.PublishedControls()
		return len(published) == 1
	}, 3*time.Second, 5*time.Millisecond, "the question reaches the reader")
	assert.Equal(t, "opencode-question:que_1", published[0].RequestID)
	// The payload is the shape the browser plugin reads: the event type beside the
	// question's own properties.
	assert.JSONEq(t, `{"type":"`+contracts.OpenCodeEventQuestionAsked+`","properties":`+fakeOpenCodeQuestion+`}`, string(published[0].Payload))
}

// The stored request must carry NO top-level `id`. restoreControlResponseID reads a
// request that has one as a JSON-RPC request and withholds every answer whose id
// does not match it -- and the event envelope's `evt_...` id never would. Publishing
// the envelope unchanged therefore makes each answer unsendable, with the reader
// seeing only a refusal.
func TestOpenCodeQuestions_PublishedPayloadDoesNotWithholdTheAnswer(t *testing.T) {
	t.Parallel()
	daemon := newFakeOpenCodeDaemon(t)
	sink := &recordingControlSink{}
	daemon.bridge(t, sink)
	daemon.send(openCodeAskedEvent(fakeOpenCodeQuestion))

	var published []controlRequestRecord
	require.Eventually(t, func() bool {
		published = sink.PublishedControls()
		return len(published) == 1
	}, 3*time.Second, 5*time.Millisecond, "the question reaches the reader")

	answer := []byte(`{"jsonrpc":"2.0","id":"opencode-question:que_1","result":{"answers":[["Inspect"]]}}`)
	resolved := acpProvider{}.ResolveControlResponse(ControlResponseContext{
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
			daemon := newFakeOpenCodeDaemon(t)
			sink := &recordingControlSink{}
			questions, ctx := daemon.bridge(t, sink)
			daemon.send(openCodeAskedEvent(fakeOpenCodeQuestion))
			require.Eventually(t, func() bool {
				return len(sink.PublishedControls()) == 1
			}, 3*time.Second, 5*time.Millisecond, "the question reaches the reader")

			handled, err := questions.answer(ctx, []byte(tc.frame))
			require.NoError(t, err)
			assert.True(t, handled, "the bridge takes the frame instead of the stdio stream")
			answers := daemon.answers()
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
	daemon := newFakeOpenCodeDaemon(t)
	questions, ctx := daemon.bridge(t, &recordingControlSink{})
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
	assert.Empty(t, daemon.answers())
}

// An answer the daemon refuses must reach the reader as a failure. Reporting success
// would retire a card whose question the daemon still blocks on.
func TestOpenCodeQuestions_ReportsARefusedAnswer(t *testing.T) {
	t.Parallel()
	daemon := newFakeOpenCodeDaemon(t)
	sink := &recordingControlSink{}
	questions, ctx := daemon.bridge(t, sink)
	daemon.send(openCodeAskedEvent(fakeOpenCodeQuestion))
	require.Eventually(t, func() bool {
		return len(sink.PublishedControls()) == 1
	}, 3*time.Second, 5*time.Millisecond, "the question reaches the reader")
	daemon.mu.Lock()
	daemon.failNext = 1
	daemon.mu.Unlock()

	handled, err := questions.answer(ctx, []byte(`{"jsonrpc":"2.0","id":"opencode-question:que_1","result":{"answers":[["Inspect"]]}}`))
	assert.True(t, handled)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "404")
}

// A frame this build cannot read is still the bridge's, so it must not fall through
// to the stdio stream, where it would be written as a response to nothing.
func TestOpenCodeQuestions_RefusesAnUnreadableAnswer(t *testing.T) {
	t.Parallel()
	daemon := newFakeOpenCodeDaemon(t)
	sink := &recordingControlSink{}
	questions, ctx := daemon.bridge(t, sink)
	daemon.send(openCodeAskedEvent(fakeOpenCodeQuestion))
	require.Eventually(t, func() bool {
		return len(sink.PublishedControls()) == 1
	}, 3*time.Second, 5*time.Millisecond, "the question reaches the reader")

	handled, err := questions.answer(ctx, []byte(`{"jsonrpc":"2.0","id":"opencode-question:que_1","result":{}}`))
	assert.True(t, handled)
	require.Error(t, err)
	assert.Empty(t, daemon.answers())
}

func TestOpenCodeQuestions_WithdrawsASettledQuestion(t *testing.T) {
	t.Parallel()
	for _, event := range []string{contracts.OpenCodeEventQuestionReplied, contracts.OpenCodeEventQuestionRejected} {
		t.Run(event, func(t *testing.T) {
			t.Parallel()
			daemon := newFakeOpenCodeDaemon(t)
			sink := &recordingControlSink{}
			daemon.bridge(t, sink)
			daemon.send(openCodeAskedEvent(fakeOpenCodeQuestion))
			require.Eventually(t, func() bool {
				return len(sink.PublishedControls()) == 1
			}, 3*time.Second, 5*time.Millisecond, "the question reaches the reader")

			daemon.send("data: {\"id\":\"evt_2\",\"type\":\"" + event + "\",\"properties\":{\"sessionID\":\"ses_9\",\"requestID\":\"que_1\"}}\n\n")
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
	daemon := newFakeOpenCodeDaemon(t)
	sink := &recordingControlSink{}
	questions, ctx := daemon.bridge(t, sink)
	daemon.send(openCodeAskedEvent(fakeOpenCodeQuestion))
	require.Eventually(t, func() bool {
		return len(sink.PublishedControls()) == 1
	}, 3*time.Second, 5*time.Millisecond, "the question reaches the reader")

	handled, err := questions.answer(ctx, []byte(`{"jsonrpc":"2.0","id":"opencode-question:que_1","result":{"answers":[["Inspect"]]}}`))
	require.NoError(t, err)
	require.True(t, handled)
	daemon.send("data: {\"id\":\"evt_2\",\"type\":\"" + contracts.OpenCodeEventQuestionReplied + "\",\"properties\":{\"requestID\":\"que_1\"}}\n\n")

	require.Eventually(t, func() bool {
		return len(daemon.answers()) == 1
	}, 3*time.Second, 5*time.Millisecond, "the daemon has the answer")
	assert.Empty(t, sink.CanceledControls(), "the answered card is retired by the service, not twice by the bridge")
}

// A question raised before LeapMux connected raises no event on the new connection.
// The pending list is the only thing that carries it, on the first connection as
// well as after a reconnect.
func TestOpenCodeQuestions_PublishesAQuestionRaisedBeforeItConnected(t *testing.T) {
	t.Parallel()
	daemon := newFakeOpenCodeDaemon(t)
	daemon.setPending(fakeOpenCodeQuestion)
	sink := &recordingControlSink{}
	daemon.bridge(t, sink)

	var published []controlRequestRecord
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
	daemon := newFakeOpenCodeDaemon(t)
	daemon.setPending(fakeOpenCodeQuestion)
	sink := &recordingControlSink{}
	questions, ctx := daemon.bridge(t, sink)
	require.Eventually(t, func() bool {
		return len(sink.PublishedControls()) == 1
	}, 3*time.Second, 5*time.Millisecond, "the waiting question reaches the reader")

	// The daemon still holds it, so a second list re-announces the card and retires
	// nothing.
	questions.publishPending(ctx)
	assert.Empty(t, sink.CanceledControls())

	// Another client answered it while the stream was down, so the daemon's list no
	// longer carries it.
	daemon.setPending()
	questions.publishPending(ctx)
	assert.Equal(t, []string{"opencode-question:que_1"}, sink.CanceledControls())
}

// A list the bridge could not READ retires nothing. A failed read is not a statement
// that the daemon holds no question, and retiring on one would drop every live card
// each time the daemon was busy.
func TestOpenCodeQuestions_KeepsEveryCardWhenTheListIsUnreadable(t *testing.T) {
	t.Parallel()
	daemon := newFakeOpenCodeDaemon(t)
	daemon.setPending(fakeOpenCodeQuestion)
	sink := &recordingControlSink{}
	questions, ctx := daemon.bridge(t, sink)
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
	daemon := newFakeOpenCodeDaemon(t)
	sink := &recordingControlSink{publicationError: assert.AnError}
	daemon.bridge(t, sink)
	daemon.send(openCodeAskedEvent(fakeOpenCodeQuestion))

	var answers []fakeOpenCodeAnswer
	require.Eventually(t, func() bool {
		answers = daemon.answers()
		return len(answers) == 1
	}, 3*time.Second, 5*time.Millisecond, "the daemon is released")
	assert.Equal(t, "que_1", answers[0].QuestionID)
	assert.Equal(t, "reject", answers[0].Action)
}

// The daemon dies with the agent, so a card it left behind is retired here or never.
func TestOpenCodeQuestions_RetiresEveryCardWhenTheAgentStops(t *testing.T) {
	t.Parallel()
	daemon := newFakeOpenCodeDaemon(t)
	sink := &recordingControlSink{}
	questions := &openCodeQuestions{}
	questions.configure(sink)
	ctx, cancel := context.WithCancel(context.Background())
	questions.beginAt(ctx, "agent-1", daemon.server.URL)
	daemon.send(openCodeAskedEvent(fakeOpenCodeQuestion))
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
	daemon := newFakeOpenCodeDaemon(t)
	sink := &recordingControlSink{}
	daemon.bridge(t, sink)
	whole := `{"id":"evt_1","type":"` + contracts.OpenCodeEventQuestionAsked + `","properties":` + fakeOpenCodeQuestion + `}`
	var indented bytes.Buffer
	require.NoError(t, json.Indent(&indented, []byte(whole), "", "  "))
	var event strings.Builder
	for _, line := range strings.Split(indented.String(), "\n") {
		event.WriteString("data:" + line + "\n")
	}
	event.WriteString("\n")
	daemon.send(event.String())

	require.Eventually(t, func() bool {
		return len(sink.PublishedControls()) == 1
	}, 3*time.Second, 5*time.Millisecond, "the indented question reaches the reader")
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
	assert.Equal(t, []string{"acp", "--hostname", "127.0.0.1", "--port", "0"}, openCodeACPArgs())
}

func TestOpenCodeQuestionControlID_SeparatesTheStdioRows(t *testing.T) {
	t.Parallel()
	// The ACP stream keys its own rows under "jsonrpc:", so neither can match the other.
	assert.Equal(t, "opencode-question:que_1", openCodeQuestionControlID("que_1"))
	identity, valid := newControlRequestIdentity(json.RawMessage(`7`))
	require.True(t, valid)
	assert.NotEqual(t, identity.key, openCodeQuestionControlID("que_1"))
}

// The agent must answer a question through the bridge, not through the stdio stream.
//
// Both OpenCodeAgent and KiloAgent embed openCodeFamilyBase, which embeds acpBase,
// and BOTH define SendRawInput. The shallower one wins, so the family base's
// interception is what a caller reaches. An embedding that put acpBase first would
// reverse that in silence: the answer would be written to stdin as a response to a
// request the daemon never made, and the question would block for the whole turn.
func TestOpenCodeFamily_AnswersAQuestionThroughTheBridge(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		build func() (Agent, *openCodeQuestions, *processBase)
	}{
		{"opencode", func() (Agent, *openCodeQuestions, *processBase) {
			a := &OpenCodeAgent{}
			return a, &a.questions, &a.processBase
		}},
		{"kilo", func() (Agent, *openCodeQuestions, *processBase) {
			a := &KiloAgent{}
			return a, &a.questions, &a.processBase
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			daemon := newFakeOpenCodeDaemon(t)
			sink := &recordingControlSink{}
			agent, questions, process := tc.build()
			questions.configure(sink)
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			process.ctx = ctx
			questions.beginAt(ctx, "agent-1", daemon.server.URL)
			daemon.send(openCodeAskedEvent(fakeOpenCodeQuestion))
			require.Eventually(t, func() bool {
				return len(sink.PublishedControls()) == 1
			}, 3*time.Second, 5*time.Millisecond, "the question reaches the reader")

			// Through the interface, which is how the worker service calls it. The
			// stdin writer is nil, so a frame that fell through to the stream fails
			// rather than passing unnoticed.
			require.NoError(t, agent.SendRawInput([]byte(`{"jsonrpc":"2.0","id":"opencode-question:que_1","result":{"answers":[["Inspect"]]}}`)))
			answers := daemon.answers()
			require.Len(t, answers, 1)
			assert.Equal(t, "reply", answers[0].Action)
			assert.JSONEq(t, `{"answers":[["Inspect"]]}`, answers[0].Body)
		})
	}
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
	daemon := newFakeOpenCodeDaemon(t)
	questions := &openCodeQuestions{}
	questions.configure(&recordingControlSink{})

	base, err := questions.discover(context.Background(), int32(os.Getpid()))
	require.NoError(t, err)
	assert.Equal(t, daemon.server.URL, base)
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
	daemon := newFakeOpenCodeDaemon(t)
	questions := &openCodeQuestions{}
	questions.configure(&recordingControlSink{})

	base, err := questions.discover(context.Background(), int32(os.Getpid()))
	require.NoError(t, err)
	assert.Equal(t, daemon.server.URL, base)
}

// A process with no question server must give up rather than block the goroutine for
// the life of the session. The reader loses questions and keeps the session.
func TestOpenCodeQuestions_GivesUpWhenNoServerAnswers(t *testing.T) {
	t.Parallel()
	questions := &openCodeQuestions{}
	questions.configure(&recordingControlSink{})
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
	daemon := newFakeOpenCodeDaemon(t)
	_, portText, err := net.SplitHostPort(strings.TrimPrefix(daemon.server.URL, "http://"))
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

// TestOpenCodeFamilyRcMarkerNeverReachesTheDaemon states what the `*_CLIENT` marker
// is, and what it is NOT.
//
// It is the rc-file signal, and it is the daemon's own client-identity config key at
// the same time -- so the wrapper UNSETS it in the inner command, right before the
// exec. The login shell sources the user's rc files with the marker set, which is the
// whole point of it, and the daemon then starts without it and falls back to its own
// default.
//
// So the marker is not "overridden" by `<cli> acp` assigning its own client name: it
// never reaches that process at all. It neither opens nor closes the question gate,
// which is why the pinned flag beside it is a separate mechanism and not a
// replacement for it.
func TestOpenCodeFamilyRcMarkerNeverReachesTheDaemon(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		binary string
		marker string
	}{
		{binary: "opencode", marker: "OPENCODE_CLIENT"},
		{binary: "kilo", marker: "KILO_CLIENT"},
	} {
		t.Run(tc.binary, func(t *testing.T) {
			t.Parallel()

			inner := buildPosixCommand(shellWrapSpec{
				Launch:       launchSpec{Program: tc.binary},
				StripEnvKeys: []string{tc.marker},
				BaseArgs:     openCodeACPArgs(),
			}, "__DELIM__", "__META__ ")

			assert.Contains(t, inner, "unset "+tc.marker+" && ",
				"the marker must be removed before the daemon starts")
			// The flag beside it is NOT stripped: the daemon has to read it.
			assert.NotContains(t, inner, "unset "+openCodeQuestionToolEnv)
			assert.NotContains(t, inner, "unset "+kiloQuestionToolEnv)
		})
	}
}
