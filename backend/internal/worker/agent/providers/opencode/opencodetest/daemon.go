package opencodetest

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// FakeDaemon answers the two routes the question bridge reads, and lets a
// test push events onto the stream the way the daemon does.
type FakeDaemon struct {
	Server *httptest.Server

	Mu       sync.Mutex
	events   chan string
	pending  []string
	replies  []FakeAnswer
	FailNext int
}

type FakeAnswer struct {
	QuestionID string
	Action     string
	Body       string
}

// NewFakeDaemon starts a daemon that serves eventRoute and questionRoot,
// the two routes of the family's question bridge.
func NewFakeDaemon(t *testing.T, eventRoute, questionRoot string) *FakeDaemon {
	t.Helper()
	daemon := &FakeDaemon{events: make(chan string, 8)}
	mux := http.NewServeMux()
	mux.HandleFunc(eventRoute, func(w http.ResponseWriter, r *http.Request) {
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
	mux.HandleFunc(questionRoot, func(w http.ResponseWriter, _ *http.Request) {
		daemon.Mu.Lock()
		pending := "[" + strings.Join(daemon.pending, ",") + "]"
		daemon.Mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, pending)
	})
	mux.HandleFunc(questionRoot+"/", func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, questionRoot), "/"), "/")
		require.Len(t, parts, 2, "the route is /question/{id}/{action}")
		body, _ := io.ReadAll(r.Body)
		daemon.Mu.Lock()
		fail := daemon.FailNext > 0
		if fail {
			daemon.FailNext--
		} else {
			daemon.replies = append(daemon.replies, FakeAnswer{QuestionID: parts[0], Action: parts[1], Body: string(body)})
		}
		daemon.Mu.Unlock()
		if fail {
			http.Error(w, "the question is gone", http.StatusNotFound)
			return
		}
		_, _ = fmt.Fprint(w, `true`)
	})
	daemon.Server = httptest.NewServer(mux)
	t.Cleanup(daemon.Server.Close)
	return daemon
}

func (d *FakeDaemon) Send(event string) { d.events <- event }

func (d *FakeDaemon) Answers() []FakeAnswer {
	d.Mu.Lock()
	defer d.Mu.Unlock()
	return append([]FakeAnswer(nil), d.replies...)
}

func (d *FakeDaemon) SetPending(questions ...string) {
	d.Mu.Lock()
	defer d.Mu.Unlock()
	d.pending = questions
}

const FakeQuestion = `{"id":"que_1","sessionID":"ses_9","questions":[{"question":"Which one?","header":"Task","options":[{"label":"Inspect","description":"Look only"}]}]}`

func AskedEvent(properties string) string {
	return "data: {\"id\":\"evt_1\",\"type\":\"" + contracts.OpenCodeEventQuestionAsked + "\",\"properties\":" + properties + "}\n\n"
}

// AssertRcMarkerNeverReachesTheDaemon states what a family daemon's `*_CLIENT`
// marker is, and what it is NOT.
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
func AssertRcMarkerNeverReachesTheDaemon(t *testing.T, binary, marker, flag string, baseArgs []string) {
	t.Helper()
	cmd, _, _ := launch.Wrap(context.Background(), launch.WrapSpec{
		Shell:        "/bin/sh",
		Launch:       launch.Spec{Program: binary},
		StripEnvKeys: []string{marker},
		BaseArgs:     baseArgs,
	})
	require.Len(t, cmd.Args, 3) // sh -c <inner>
	inner := cmd.Args[2]

	assert.Contains(t, inner, "unset "+marker+" && ",
		"the marker must be removed before the daemon starts")
	// The flag beside it is NOT stripped: the daemon has to read it.
	assert.NotContains(t, inner, "unset "+flag)
}

// AssertAnswersAQuestionThroughTheBridge pins that agent answers a question
// through the bridge, not through the stdio stream. begin starts the agent's
// bridge under ctx against the daemon at baseURL, with sink as its services.
//
// OpenCode and Kilo embed the family base, which embeds the ACP base, and BOTH
// define SendRawInput. The shallower one wins, so the family base's interception
// is what a caller reaches. An embedding that put the ACP base first would
// reverse that in silence: the answer would be written to stdin as a response to
// a request the daemon never made, and the question would block for the whole
// turn.
func AssertAnswersAQuestionThroughTheBridge(t *testing.T, ag agent.Agent, eventRoute, questionRoot string,
	begin func(ctx context.Context, sink agent.ControlServices, baseURL string),
) {
	t.Helper()
	daemon := NewFakeDaemon(t, eventRoute, questionRoot)
	sink := &agenttest.ControlSink{}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	begin(ctx, agent.NewProviderServices(sink), daemon.Server.URL)
	daemon.Send(AskedEvent(FakeQuestion))
	require.Eventually(t, func() bool {
		return len(sink.PublishedControls()) == 1
	}, 3*time.Second, 5*time.Millisecond, "the question reaches the reader")

	// Through the interface, which is how the worker service calls it. The stdin
	// writer is nil, so a frame that fell through to the stream fails rather than
	// passing unnoticed.
	require.NoError(t, ag.SendRawInput([]byte(`{"jsonrpc":"2.0","id":"opencode-question:que_1","result":{"answers":[["Inspect"]]}}`)))
	answers := daemon.Answers()
	require.Len(t, answers, 1)
	assert.Equal(t, "reply", answers[0].Action)
	assert.JSONEq(t, `{"answers":[["Inspect"]]}`, answers[0].Body)
}
