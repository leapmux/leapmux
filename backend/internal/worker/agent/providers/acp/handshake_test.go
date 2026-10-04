package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// handshakePeer is the fake agent of runHandshakeForTest. It records each
// request that the base wrote.
type handshakePeer struct {
	mu       sync.Mutex
	requests []agenttest.RecordedRequest
	// linesBefore holds, for each method, the lines that the peer writes before
	// its answer to that method: what an agent sends while it handles the
	// request. The peer only reads it.
	linesBefore map[string][]string
}

// recorded returns the requests that the peer read so far, in order.
func (p *handshakePeer) recorded() []agenttest.RecordedRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]agenttest.RecordedRequest(nil), p.requests...)
}

// runHandshakeForTest runs startACPHandshake against peer, with hooks applied
// as Start applies them. The peer reads each request from the stdin of the
// base, records it, writes the lines that peer.linesBefore holds for its
// method, and then writes the result that respond returns for that method to
// the stdout of the base.
//
// The fake process is an exec.Cmd that never starts. At the end of the test its
// stdout closes, and the reader then records the exit as a real exit does,
// because the Wait of a command that never started returns at once.
func runHandshakeForTest(t *testing.T, peer *handshakePeer, hooks Hooks, opts agent.Options, sessionConfig SessionConfig, respond func(method string) json.RawMessage) (*Base, *agenttest.ControlSink, *SessionResult) {
	t.Helper()
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	exited := make(chan struct{})
	b := &Base{}
	b.Process = providerkit.NewProcessFrom(providerkit.ProcessConfig{
		AgentID:     "test-agent",
		Cmd:         &exec.Cmd{},
		Stdin:       stdinWriter,
		Ctx:         ctx,
		Cancel:      cancel,
		ProcessDone: exited,
	})
	sink := &agenttest.ControlSink{}
	b.sink = agent.NewProviderServices(sink)
	b.applyHooks(hooks)

	go func() {
		scanner := bufio.NewScanner(stdinReader)
		for scanner.Scan() {
			var request struct {
				ID     int64          `json:"id"`
				Method string         `json:"method"`
				Params map[string]any `json:"params"`
			}
			if json.Unmarshal(scanner.Bytes(), &request) != nil {
				continue
			}
			peer.mu.Lock()
			peer.requests = append(peer.requests, agenttest.RecordedRequest{Method: request.Method, Params: request.Params, Raw: scanner.Text()})
			peer.mu.Unlock()
			for _, line := range peer.linesBefore[request.Method] {
				if _, err := stdoutWriter.Write([]byte(line + "\n")); err != nil {
					return
				}
			}
			reply, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": respond(request.Method)})
			if err != nil {
				return
			}
			if _, err := stdoutWriter.Write(append(reply, '\n')); err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		_ = stdinWriter.Close()
		_ = stdoutWriter.Close()
		// t.Context is already done when a cleanup runs, so the wait takes a
		// deadline of its own. Nothing asserts about its length.
		wait, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		select {
		case <-exited:
		case <-wait.Done():
			t.Error("the reader never recorded the exit of the fake process")
		}
	})

	initParams, err := acpStandardInitParams(hooks.ClientCapabilityMeta, !hooks.DisableHostTerminal, hooks.InitializeMeta)
	require.NoError(t, err)
	session, err := b.startACPHandshake(stdoutReader, io.NopCloser(strings.NewReader("")), opts, initParams, sessionConfig)
	require.NoError(t, err)
	return b, sink, session
}

// The handshake reads the initialize response once, before it opens the
// session: it hands the response to the provider, and it learns there whether
// the agent closes a session. A context clear sends session/close only when it
// did, so this is the one place that turns the close on.
func TestStartACPHandshake_ReadsTheInitializeResponse(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		response string
		closes   bool
	}{
		"a response that advertises session/close": {response: `{"protocolVersion":1,"agentCapabilities":{"sessionCapabilities":{"close":{}}}}`, closes: true},
		"a response that advertises no close":      {response: `{"protocolVersion":1,"agentCapabilities":{"sessionCapabilities":{}}}`},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			peer := &handshakePeer{}
			var readResponses []string
			var requestsAtRead []int
			hooks := Hooks{
				InitializeResponse: func(response []byte) {
					readResponses = append(readResponses, string(response))
					requestsAtRead = append(requestsAtRead, len(peer.recorded()))
				},
			}
			b, sink, session := runHandshakeForTest(t, peer, hooks, agent.Options{WorkingDir: "/work"}, acpDefaultSessionConfig, func(method string) json.RawMessage {
				if method == MethodInitialize {
					return json.RawMessage(tc.response)
				}
				return json.RawMessage(`{"sessionId":"session-9"}`)
			})

			require.Len(t, readResponses, 1, "the provider reads the initialize response once")
			assert.JSONEq(t, tc.response, readResponses[0])
			assert.Equal(t, []int{1}, requestsAtRead, "the provider reads the response before the session request goes out")
			b.Mu.Lock()
			closes := b.closesSessions
			b.Mu.Unlock()
			assert.Equal(t, tc.closes, closes)
			assert.Equal(t, "session-9", session.SessionID)
			assert.Equal(t, "session-9", b.CurrentSessionID())
			assert.Equal(t, "session-9", sink.LastSessionID())
			methods := make([]string, 0, 2)
			for _, request := range peer.recorded() {
				methods = append(methods, request.Method)
			}
			assert.Equal(t, []string{MethodInitialize, MethodSessionNew}, methods)
		})
	}
}

// A resumed session reopens through the resume method with the stored id, and
// the provider's hook adjusts that request as it adjusts session/new. A resume
// response that states no session id keeps the id that the resume asked for.
func TestStartACPHandshake_AResumeCarriesTheSessionParams(t *testing.T) {
	t.Parallel()
	var adjusted []string
	hooks := Hooks{
		SessionParams: func(method string, params map[string]any) {
			adjusted = append(adjusted, method)
			if method == MethodSessionResume {
				params["cwd"] = "/stored/cwd"
			}
		},
	}
	peer := &handshakePeer{}
	b, _, session := runHandshakeForTest(t, peer, hooks,
		agent.Options{WorkingDir: "/work", ResumeSessionID: "stored-1"},
		SessionConfig{NewMethod: MethodSessionNew, ResumeMethod: MethodSessionResume},
		func(method string) json.RawMessage {
			if method == MethodInitialize {
				return json.RawMessage(`{"protocolVersion":1}`)
			}
			return json.RawMessage(`{}`)
		})

	assert.Equal(t, []string{MethodSessionResume}, adjusted)
	requests := peer.recorded()
	require.Len(t, requests, 2)
	assert.Equal(t, MethodSessionResume, requests[1].Method)
	assert.Equal(t, "/stored/cwd", requests[1].Params["cwd"])
	assert.Equal(t, "stored-1", requests[1].Params["sessionId"])
	assert.Equal(t, "stored-1", session.SessionID)
	assert.Equal(t, "stored-1", b.CurrentSessionID())
	b.Mu.Lock()
	defer b.Mu.Unlock()
	assert.False(t, b.closesSessions, "an agent that advertises no close takes none")
}

// An agent can raise a control request while it opens the session, before its
// answer to session/new gives the session id. Grok Build does so for folder
// trust. The sink learns the session only after that answer, so a request
// stored under the session that the sink held then was stored under none. The
// worker then refused every answer to it as an answer for a different provider
// session, and its card never cleared. The request states its own session, so
// the base stores it under that session.
func TestStartACPHandshake_AControlRequestBeforeTheSessionAnswerKeepsItsSession(t *testing.T) {
	t.Parallel()
	peer := &handshakePeer{linesBefore: map[string][]string{
		MethodSessionNew: {`{"jsonrpc":"2.0","id":7,"method":"session/request_permission","params":{"sessionId":"session-9","toolCall":{"toolCallId":"call_1"}}}`},
	}}

	_, sink, session := runHandshakeForTest(t, peer, Hooks{}, agent.Options{WorkingDir: "/work"}, acpDefaultSessionConfig, func(method string) json.RawMessage {
		if method == MethodInitialize {
			return json.RawMessage(`{"protocolVersion":1}`)
		}
		return json.RawMessage(`{"sessionId":"session-9"}`)
	})

	require.Equal(t, "session-9", session.SessionID)
	published := sink.PublishedControls()
	require.Len(t, published, 1, "a request of the session that the handshake opens reaches the reader")
	assert.Equal(t, "session-9", published[0].AgentSessionID)
}
