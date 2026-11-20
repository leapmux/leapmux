package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/coder/quartz"
	"github.com/leapmux/leapmux/internal/util/testutil"
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
	// linesAfter holds, for each method, the lines that the peer writes right
	// after its answer to that method: what an agent sends once it handled the
	// request. The peer only reads it.
	linesAfter map[string][]string
}

// recorded returns the requests that the peer read so far, in order.
func (p *handshakePeer) recorded() []agenttest.RecordedRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]agenttest.RecordedRequest(nil), p.requests...)
}

// handshakeRig is the fake process that startACPHandshake talks to. The peer
// reads each request from the stdin of the base and records it. Then it writes
// these items to the stdout of the base, in this order:
//
//   - The lines that peer.linesBefore holds for the method of the request.
//   - The result that respond returns for that method.
//   - The lines that peer.linesAfter holds for that method.
//
// The fake process is an exec.Cmd that never starts. At the end of the test its
// stdout closes, and the reader then records the exit as a real exit does,
// because the Wait of a command that never started returns at once.
type handshakeRig struct {
	base   *Base
	sink   *agenttest.ControlSink
	hooks  Hooks
	stdout io.ReadCloser
}

// newHandshakeRig builds the fake process and starts its peer. A nil clock
// selects the real clock of the process. A test that passes a mock clock can
// trap the timer of a request and stop the handshake goroutine at that point.
func newHandshakeRig(t *testing.T, peer *handshakePeer, hooks Hooks, clock quartz.Clock, respond func(method string) json.RawMessage) *handshakeRig {
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
		Clock:       clock,
	})
	sink := &agenttest.ControlSink{}
	b.sink = agent.NewProviderServices(sink)
	b.applyHooks(hooks)

	go func() {
		defer func() { _ = stdoutWriter.Close() }()
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
			for _, line := range peer.linesAfter[request.Method] {
				if _, err := stdoutWriter.Write([]byte(line + "\n")); err != nil {
					return
				}
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
	return &handshakeRig{base: b, sink: sink, hooks: hooks, stdout: stdoutReader}
}

// handshake runs startACPHandshake against the peer, with the hooks applied as
// Start applies them.
func (r *handshakeRig) handshake(opts agent.Options, sessionConfig SessionConfig) (*SessionResult, error) {
	initParams, err := acpStandardInitParams(&r.hooks)
	if err != nil {
		return nil, err
	}
	return r.base.startACPHandshake(r.stdout, io.NopCloser(strings.NewReader("")), opts, initParams, sessionConfig)
}

// runHandshakeForTest runs startACPHandshake against peer, on a rig with the
// real clock. See handshakeRig for what the peer writes.
func runHandshakeForTest(t *testing.T, peer *handshakePeer, hooks Hooks, opts agent.Options, sessionConfig SessionConfig, respond func(method string) json.RawMessage) (*Base, *agenttest.ControlSink, *SessionResult) {
	t.Helper()
	rig := newHandshakeRig(t, peer, hooks, nil, respond)
	session, err := rig.handshake(opts, sessionConfig)
	require.NoError(t, err)
	return rig.base, rig.sink, session
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

func TestACPStopClosesAdmissionAndRunsPrepareStopOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var hooks atomic.Int32
		entered, release := make(chan struct{}), make(chan struct{})
		peer := &handshakePeer{}
		rig := newHandshakeRig(t, peer, Hooks{
			PrepareStop: func(snapshot ACPStopSnapshot) {
				if hooks.Add(1) == 1 {
					close(entered)
				}
				<-release
			},
			AdvertisedSteerMethod: func([]byte) string { return "session/steer" },
		}, nil, func(method string) json.RawMessage {
			if method == MethodSessionNew {
				return json.RawMessage(`{"sessionId":"session"}`)
			}
			return json.RawMessage(`{"protocolVersion":1}`)
		})
		_, err := rig.handshake(agent.Options{}, SessionConfig{NewMethod: MethodSessionNew})
		require.NoError(t, err)
		child := ChildSessionTarget{RootSessionID: "session", SessionID: "child", RowKey: "child-row"}
		require.True(t, rig.base.AttachChildSession(child))
		rig.base.Mu.Lock()
		rig.base.promptActive = true
		rig.base.Mu.Unlock()

		var stops sync.WaitGroup
		stops.Go(rig.base.Stop)
		<-entered
		stops.Go(rig.base.Stop)
		synctest.Wait()
		assert.Equal(t, int32(1), hooks.Load())
		_, err = rig.base.SteerAdvertisedResult("late steer", nil)
		assert.Error(t, err)
		_, err = rig.base.ClearContext()
		assert.Error(t, err)
		assert.Error(t, rig.base.SendInput("late root prompt", nil))
		assert.ErrorIs(t, rig.base.SendChildPromptDetached(child, "late child prompt", nil, func(json.RawMessage, error) {}), agent.ErrChildRouteNotReady)
		assert.Error(t, rig.base.SendPromptDetached("session", "late detached prompt", nil, func(json.RawMessage, error) {}))
		assert.Len(t, peer.recorded(), 2, "no late operation can reach the ACP peer")

		close(release)
		stops.Wait()
	})
}

func TestACPStopBypassesAnUnstableSessionSnapshot(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		snapshots := make(chan ACPStopSnapshot, 1)
		rig := newHandshakeRig(t, &handshakePeer{}, Hooks{
			PrepareStop: func(snapshot ACPStopSnapshot) { snapshots <- snapshot },
		}, nil, func(method string) json.RawMessage {
			if method == MethodSessionNew {
				return json.RawMessage(`{"sessionId":"session"}`)
			}
			return json.RawMessage(`{"protocolVersion":1}`)
		})
		_, err := rig.handshake(agent.Options{}, SessionConfig{NewMethod: MethodSessionNew})
		require.NoError(t, err)
		rig.base.sessionMu.Lock()
		defer rig.base.sessionMu.Unlock()
		done := make(chan struct{})
		go func() {
			rig.base.Stop()
			close(done)
		}()
		synctest.Wait()
		select {
		case snapshot := <-snapshots:
			assert.Empty(t, snapshot.SessionID)
			assert.Nil(t, snapshot.Cancel)
		default:
			t.Error("the pre-stop hook waited for the session lock")
		}
		select {
		case <-done:
		default:
			t.Error("ordinary teardown waited for the session lock")
		}
	})
}

func TestACPStopCancellationDoesNotWaitForStdin(t *testing.T) {
	for _, full := range []bool{false, true} {
		t.Run(fmt.Sprintf("queue full=%t", full), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				promptReply := make(chan struct{})
				releasePrompt := sync.OnceFunc(func() { close(promptReply) })
				defer releasePrompt()
				attributed, releaseHook := make(chan bool, 1), make(chan struct{})
				resumeHook := sync.OnceFunc(func() { close(releaseHook) })
				defer resumeHook()
				var rig *handshakeRig
				rig = newHandshakeRig(t, &handshakePeer{}, Hooks{
					PrepareStop: func(snapshot ACPStopSnapshot) {
						snapshot.Cancel()
						snapshot.Cancel()
						attributed <- rig.base.acpInterruptRequested()
						<-releaseHook
					},
				}, nil, func(method string) json.RawMessage {
					switch method {
					case MethodSessionNew:
						return json.RawMessage(`{"sessionId":"session"}`)
					case MethodSessionPrompt:
						<-promptReply
						return json.RawMessage(`{"stopReason":"cancelled"}`)
					default:
						return json.RawMessage(`{"protocolVersion":1}`)
					}
				})
				_, err := rig.handshake(agent.Options{}, SessionConfig{NewMethod: MethodSessionNew})
				require.NoError(t, err)
				require.NoError(t, rig.base.SendInput("hold the native read", nil))
				blocked, err := rig.base.EnqueueStdin([]byte("{}\n"))
				require.NoError(t, err)
				synctest.Wait()
				if full {
					for {
						if _, err := rig.base.EnqueueStdin([]byte("{}\n")); err != nil {
							break
						}
					}
				}
				finished := make(chan struct{})
				go func() {
					rig.base.Stop()
					close(finished)
				}()
				synctest.Wait()
				select {
				case recorded := <-attributed:
					assert.True(t, recorded)
				default:
					t.Error("queueing the captured cancellation blocked behind the stdin writer")
				}
				resumeHook()
				synctest.Wait()
				select {
				case err := <-blocked:
					assert.Error(t, err, "ordinary teardown closes the blocked stdin writer")
				default:
					t.Error("ordinary teardown did not close stdin")
				}
				releasePrompt()
				<-finished
			})
		})
	}
}

func TestACPStoppedRootPromptDoesNotWaitForTheSessionLock(t *testing.T) {
	peer := &handshakePeer{}
	rig := newHandshakeRig(t, peer, Hooks{PrepareStop: func(ACPStopSnapshot) {}}, nil, func(method string) json.RawMessage {
		if method == MethodSessionNew {
			return json.RawMessage(`{"sessionId":"session"}`)
		}
		return json.RawMessage(`{"protocolVersion":1}`)
	})
	_, err := rig.handshake(agent.Options{}, SessionConfig{NewMethod: MethodSessionNew})
	require.NoError(t, err)
	b := rig.base
	b.sessionMu.Lock()
	release := sync.OnceFunc(b.sessionMu.Unlock)
	defer release()
	// Reproduce the interval between Stop claiming teardown and closing the
	// separate callback admission. The active session still owns its lock.
	b.Mu.Lock()
	b.stopDone = make(chan struct{})
	b.Mu.Unlock()
	done := make(chan error, 1)
	go func() { done <- b.SendInput("late root prompt", nil) }()
	select {
	case err := <-done:
		require.Error(t, err)
	case <-testutil.DeadlineContext(t).Done():
		t.Error("a root prompt entering after the stop claim waited for the session lock")
		release()
		assert.Error(t, <-done)
	}
	assert.Len(t, peer.recorded(), 2, "the late prompt sends no native RPC")
}

func TestACPStopKeepsTheSessionSnapshotWhileOutputOwnsTheTurnLock(t *testing.T) {
	snapshots := make(chan ACPStopSnapshot, 1)
	resumeStop := make(chan struct{})
	releaseStop := sync.OnceFunc(func() { close(resumeStop) })
	defer releaseStop()
	rig := newHandshakeRig(t, &handshakePeer{}, Hooks{
		PrepareStop: func(snapshot ACPStopSnapshot) {
			snapshots <- snapshot
			<-resumeStop
		},
	}, nil, func(method string) json.RawMessage {
		if method == MethodSessionNew {
			return json.RawMessage(`{"sessionId":"session"}`)
		}
		return json.RawMessage(`{"protocolVersion":1}`)
	})
	_, err := rig.handshake(agent.Options{}, SessionConfig{NewMethod: MethodSessionNew})
	require.NoError(t, err)
	b := rig.base
	b.Mu.Lock()
	b.promptActive = true
	b.Mu.Unlock()
	b.turnMu.Lock()
	releaseOutput := sync.OnceFunc(b.turnMu.Unlock)
	defer releaseOutput()
	done := make(chan struct{})
	go func() {
		b.Stop()
		close(done)
	}()
	ctx := testutil.DeadlineContext(t)
	select {
	case snapshot := <-snapshots:
		assert.Equal(t, "session", snapshot.SessionID)
		assert.True(t, snapshot.PromptActive)
		assert.Nil(t, snapshot.Cancel)
	case <-ctx.Done():
		t.Error("turn output delayed the pre-stop snapshot")
	}
	releaseOutput()
	releaseStop()
	select {
	case <-done:
	case <-ctx.Done():
		t.Error("the ACP stop did not finish")
	}
}
