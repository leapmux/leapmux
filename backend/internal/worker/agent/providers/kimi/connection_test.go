package kimi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/quartz"
	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

func TestKimiMalformedNativeResponseReleasesOnlyItsCorrelatedWaiter(t *testing.T) {
	t.Parallel()
	for _, code := range []string{"", "null", `"bad"`} {
		t.Run("code="+code, func(t *testing.T) {
			t.Parallel()
			ctx := testutil.DeadlineContext(t)
			releaseSecond := make(chan struct{})
			release := sync.OnceFunc(func() { close(releaseSecond) })
			defer release()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				connection, err := websocket.Accept(w, request, nil)
				if !assert.NoError(t, err) {
					return
				}
				defer connection.CloseNow() //nolint:errcheck // The client can close first.
				ids := make(map[string]string)
				for range 2 {
					_, raw, err := connection.Read(ctx)
					if !assert.NoError(t, err) {
						return
					}
					var command struct {
						RequestID string `json:"request_id"`
						SessionID string `json:"session_id"`
					}
					if !assert.NoError(t, json.Unmarshal(raw, &command)) {
						return
					}
					ids[command.SessionID] = command.RequestID
				}
				response := map[string]any{"type": "response", "request_id": ids["session_1"]}
				if code != "" {
					response["code"] = json.RawMessage(code)
				}
				raw, err := json.Marshal(response)
				if !assert.NoError(t, err) || !assert.NoError(t, connection.Write(ctx, websocket.MessageText, raw)) {
					return
				}
				if !assert.NoError(t, connection.Write(ctx, websocket.MessageText, []byte(`{"type":"turns","session_id":"session_1","agent_id":"main","event_created_at":"2026-11-18T00:00:00Z","turns":[],"has_more":false}`))) {
					return
				}
				select {
				case <-releaseSecond:
				case <-ctx.Done():
					return
				}
				raw, err = json.Marshal(map[string]any{"type": "response", "request_id": ids["session_2"], "code": 0})
				if !assert.NoError(t, err) || !assert.NoError(t, connection.Write(ctx, websocket.MessageText, raw)) {
					return
				}
				_, _, _ = connection.Read(ctx)
			}))
			t.Cleanup(server.Close)
			endpoint, err := providerkit.NewHTTPEndpoint(server.URL, nil)
			require.NoError(t, err)
			t.Cleanup(endpoint.Close)
			observed := make(chan int, 1)
			var stream *kimiStream
			stream = newKimiStream(endpoint, "malformed-response", quartz.NewReal(), func(frame kimiFrame) {
				if frame.Type == "turns" {
					stream.mu.Lock()
					pending := len(stream.acks)
					stream.mu.Unlock()
					observed <- pending
				}
			}, nil)
			t.Cleanup(func() { stream.close(); stream.wait() })
			require.NoError(t, stream.start(ctx, 30*time.Second))
			requestCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			done := make(chan error, 1)
			go func() { err := stream.subscribe(requestCtx, "session_1"); done <- err }()
			second := make(chan error, 1)
			go func() { err := stream.subscribe(ctx, "session_2"); second <- err }()
			select {
			case pending := <-observed:
				assert.Equal(t, 1, pending, "only the other request remains pending before following data dispatches")
			case <-ctx.Done():
				t.Fatal("the following conversation frame did not reach dispatch")
			}
			cancel()
			select {
			case err := <-done:
				require.ErrorIs(t, err, agent.ErrDeliveryUncertain)
			case <-ctx.Done():
				t.Fatal("the refused subscription did not finish")
			}
			release()
			select {
			case err := <-second:
				require.NoError(t, err, "the malformed response cannot refuse another request")
			case <-ctx.Done():
				t.Fatal("the independently acknowledged subscription did not finish")
			}
		})
	}
}

// kimiReconnect records one native subscription recovery callback.
type kimiReconnect struct {
	sessionID string
}

// kimiStreamRig is one event stream connected to a fake server.
type kimiStreamRig struct {
	fake   *fakeKap
	stream *kimiStream

	mu         sync.Mutex
	frames     []kimiFrame
	reconnects []kimiReconnect
}

type kimiBlockedResponseContext struct {
	context.Context
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (ctx *kimiBlockedResponseContext) Done() <-chan struct{} {
	ctx.once.Do(func() { close(ctx.entered); <-ctx.release })
	return ctx.Context.Done()
}

func TestKimiSubscriptionBarrierPrecedesSnapshotsWhileTheCallerCannotResume(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		connection, err := websocket.Accept(w, request, nil)
		if !assert.NoError(t, err) {
			return
		}
		defer connection.CloseNow() //nolint:errcheck // The client can close first.
		_, raw, err := connection.Read(ctx)
		if !assert.NoError(t, err) {
			return
		}
		var command struct {
			RequestID string `json:"request_id"`
		}
		if !assert.NoError(t, json.Unmarshal(raw, &command)) {
			return
		}
		response, err := json.Marshal(map[string]any{"type": "response", "request_id": command.RequestID, "code": 0})
		if !assert.NoError(t, err) {
			return
		}
		if !assert.NoError(t, connection.Write(ctx, websocket.MessageText, response)) {
			return
		}
		if !assert.NoError(t, connection.Write(ctx, websocket.MessageText, []byte(`{"type":"turns","session_id":"session_1","agent_id":"main","event_created_at":"2026-11-18T00:00:00Z","turns":[],"has_more":false}`))) {
			return
		}
		_, _, _ = connection.Read(ctx)
	}))
	t.Cleanup(server.Close)
	endpoint, err := providerkit.NewHTTPEndpoint(server.URL, nil)
	require.NoError(t, err)
	t.Cleanup(endpoint.Close)
	var applied atomic.Bool
	arrived := make(chan bool, 1)
	stream := newKimiStream(endpoint, "barrier-agent", quartz.NewReal(), func(frame kimiFrame) {
		if frame.Type == "turns" {
			arrived <- applied.Load()
		}
	}, nil)
	t.Cleanup(func() { stream.close(); stream.wait() })
	stream.sessions["session_1"] = kimiSubscription{}
	stream.recovering = func(context.Context, string) func(bool) {
		return func(accepted bool) { applied.Store(accepted) }
	}
	require.NoError(t, stream.start(ctx, 30*time.Second))
	blocked := &kimiBlockedResponseContext{Context: ctx, entered: make(chan struct{}), release: make(chan struct{})}
	done := make(chan struct{})
	defer func() {
		close(blocked.release)
		select {
		case <-done:
		case <-ctx.Done():
			t.Error("the released subscription caller did not finish")
		}
	}()
	go func() { stream.resubscribeAll(blocked); close(done) }()
	select {
	case <-blocked.entered:
	case <-ctx.Done():
		t.Fatal("the subscription caller did not reach its response wait")
	}
	select {
	case ready := <-arrived:
		assert.True(t, ready, "the native response must order its barrier before following snapshot data")
	case <-ctx.Done():
		t.Fatal("the native snapshot did not reach the dispatcher")
	}
}

func TestKimiStreamUsesCurrentFlatSubscription(t *testing.T) {
	t.Parallel()
	requests := make(chan map[string]json.RawMessage, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		connection, err := websocket.Accept(w, request, nil)
		if !assert.NoError(t, err) {
			return
		}
		defer connection.CloseNow() //nolint:errcheck // The peer can close first.
		_, raw, err := connection.Read(t.Context())
		if !assert.NoError(t, err) {
			return
		}
		var frame map[string]json.RawMessage
		if !assert.NoError(t, json.Unmarshal(raw, &frame)) {
			return
		}
		requests <- frame
		var kind, id, sessionID string
		_ = json.Unmarshal(frame["type"], &kind)
		_ = json.Unmarshal(frame["request_id"], &id)
		_ = json.Unmarshal(frame["session_id"], &sessionID)
		if kind != "subscribe" || id == "" || sessionID != "session_1" {
			return
		}
		reply, err := json.Marshal(map[string]any{"type": "response", "request_id": id, "code": 0})
		if assert.NoError(t, err) {
			assert.NoError(t, connection.Write(t.Context(), websocket.MessageText, reply))
		}
	}))
	t.Cleanup(server.Close)
	endpoint, err := providerkit.NewHTTPEndpoint(server.URL, nil)
	require.NoError(t, err)
	t.Cleanup(endpoint.Close)
	stream := newKimiStream(endpoint, "test-agent", testutil.NewQuartzMock(t), func(kimiFrame) {}, nil)
	t.Cleanup(func() { stream.close(); stream.wait() })
	require.NoError(t, stream.start(t.Context(), 30*time.Second))
	err = stream.subscribe(t.Context(), "session_1")
	assert.NoError(t, err, "the current native server acknowledges a flat subscription")
	frame := <-requests
	assert.JSONEq(t, `"subscribe"`, string(frame["type"]))
	assert.JSONEq(t, `"session_1"`, string(frame["session_id"]))
	assert.NotEmpty(t, frame["request_id"])
	assert.NotContains(t, frame, "payload")
	assert.NotContains(t, frame, "id")
}

func TestKimiFrameRejectsIncompleteResponses(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		`{"type":"response","request_id":"r1"}`,
		`{"type":"response","request_id":"r1","code":null}`,
		`{"type":"response","code":0}`,
		`{"type":"response","request_id":"","code":0}`,
	} {
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			var frame kimiFrame
			require.Error(t, json.Unmarshal([]byte(raw), &frame))
		})
	}
}

func TestKimiStreamUsesFlatMainAgentSubscription(t *testing.T) {
	t.Parallel()
	rig := newKimiStreamRig(t)
	rig.fake.setSubscribeCodeFor(func(sub fakeKapSubscribe) int { return kimiCodeOK })
	err := rig.stream.subscribe(t.Context(), "session_1")
	require.NoError(t, err)
	frames := rig.fake.subscribeFrames()
	require.Len(t, frames, 1)
	assert.Equal(t, "subscribe", frames[0].WireType)
	assert.NotEmpty(t, frames[0].RequestID)
	assert.Equal(t, "session_1", frames[0].SessionID)
	assertKimiNativeSubscriptionCommand(t, frames[0])
}

func TestKimiStreamReportsNativeSubscriptionRefusals(t *testing.T) {
	t.Parallel()
	for _, code := range []int{kimiCodeSessionNotFound, 40300, 40001, -32603} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			t.Parallel()
			rig := newKimiStreamRig(t)
			rig.fake.setSubscribeCodeFor(func(fakeKapSubscribe) int { return code })
			err := rig.stream.subscribe(t.Context(), "session_1")
			if code == kimiCodeSessionNotFound {
				require.ErrorIs(t, err, errKimiSessionUnavailable)
			} else {
				require.ErrorContains(t, err, strconv.Itoa(code))
			}
			_, tracked := rig.subscription("session_1")
			assert.False(t, tracked, "a refused new subscription cannot return on reconnect")
		})
	}
}

func TestKimiStreamRestoresEveryDesiredNativeAgent(t *testing.T) {
	t.Parallel()
	rig := newKimiStreamRig(t)
	rig.fake.store("session_1", fakeKapSession{})
	require.NoError(t, rig.stream.subscribe(t.Context(), "session_1"))
	require.NoError(t, rig.stream.subscribeAgents(t.Context(), "session_1", []string{"agent-1"}))
	rig.stream.resubscribeAll(t.Context())
	frames := rig.fake.subscribeFrames()
	require.Len(t, frames, 3)
	assert.Equal(t, "session_1", frames[2].SessionID)
	assert.ElementsMatch(t, []string{"main", "agent-1"}, frames[2].AgentIDs)
	var command map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(frames[2].Payload, &command))
	assert.NotContains(t, command, "transcript")
	assert.NotContains(t, command, "transcript_since")
	assert.NotContains(t, command, "since")
	assert.NotContains(t, command, "payload")
	assert.NotEmpty(t, command["request_id"])
}

func newKimiStreamRig(t *testing.T) *kimiStreamRig {
	t.Helper()
	return newKimiStreamRigOnClock(t, quartz.NewReal(), time.Millisecond)
}

// newKimiStreamRigOnClock starts a stream whose timers run on clock, and that
// waits backoff before its first reconnect attempt.
func newKimiStreamRigOnClock(t *testing.T, clock quartz.Clock, backoff time.Duration) *kimiStreamRig {
	t.Helper()
	fake, server := newFakeKap(t)
	endpoint, err := providerkit.NewHTTPEndpoint(server.URL, providerkit.BearerAuth(fakeKapToken))
	require.NoError(t, err)
	t.Cleanup(endpoint.Close)
	rig := &kimiStreamRig{fake: fake}
	rig.stream = newKimiStream(endpoint, "test-agent", clock, func(frame kimiFrame) {
		rig.mu.Lock()
		rig.frames = append(rig.frames, frame)
		rig.mu.Unlock()
	}, func(_ context.Context, sessionID string) {
		rig.mu.Lock()
		rig.reconnects = append(rig.reconnects, kimiReconnect{sessionID: sessionID})
		rig.mu.Unlock()
	})
	rig.stream.backoff = backoff
	ctx, cancel := context.WithCancel(context.Background())
	// The cleanups run in reverse: close, then wait, then cancel. A stream that
	// its close did not end fails the test in the wait.
	t.Cleanup(cancel)
	t.Cleanup(rig.stream.wait)
	t.Cleanup(rig.stream.close)
	require.NoError(t, rig.stream.start(ctx, 10*time.Second))
	// The fake records a socket after the handshake, in its own goroutine, so
	// the dial can return first. A push before the record finds no socket.
	waitFor(t, func() bool { return fake.connectionCount() == 1 }, "the fake records the stream's socket")
	return rig
}

func (r *kimiStreamRig) received() []kimiFrame {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]kimiFrame(nil), r.frames...)
}

// conversationFrames excludes the native state snapshots sent at subscription.
func (r *kimiStreamRig) conversationFrames() []kimiFrame {
	var frames []kimiFrame
	for _, frame := range r.received() {
		if frame.Type != contracts.KimiEventAgent && frame.Type != contracts.KimiEventUsage && frame.Type != contracts.KimiEventGoal && frame.Type != "turns" {
			frames = append(frames, frame)
		}
	}
	return frames
}

func (r *kimiStreamRig) reconnected() []kimiReconnect {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]kimiReconnect(nil), r.reconnects...)
}

func (r *kimiStreamRig) subscription(sessionID string) (kimiSubscription, bool) {
	r.stream.mu.Lock()
	defer r.stream.mu.Unlock()
	cursor, ok := r.stream.sessions[sessionID]
	return cursor, ok
}

func assertKimiNativeSubscriptionCommand(t *testing.T, subscription fakeKapSubscribe) {
	t.Helper()
	var command map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(subscription.Payload, &command))
	assert.JSONEq(t, `"subscribe"`, string(command["type"]))
	assert.JSONEq(t, strconv.Quote(subscription.SessionID), string(command["session_id"]))
	assert.NotEmpty(t, command["request_id"])
	fields := 3
	if len(subscription.AgentIDs) > 0 {
		fields++
		expected, err := json.Marshal(subscription.AgentIDs)
		require.NoError(t, err)
		assert.JSONEq(t, string(expected), string(command["agent_ids"]))
	}
	assert.Len(t, command, fields, "the command contains only the current native subscription fields")
}

func TestKimiReconnectConfirmationCannotOutliveItsConnection(t *testing.T) {
	t.Parallel()
	rig := newKimiStreamRig(t)
	rig.fake.store("session_1", fakeKapSession{})
	ctx := testutil.DeadlineContext(t)
	require.NoError(t, rig.stream.subscribe(ctx, "session_1"))
	confirmations := make(chan context.Context, 2)
	rig.stream.recovering = func(context.Context, string) func(bool) { return func(bool) {} }
	rig.stream.confirmControls = func(connectionContext context.Context, _ string) {
		confirmations <- connectionContext
		<-connectionContext.Done()
	}
	nextConfirmation := func() context.Context {
		t.Helper()
		select {
		case current := <-confirmations:
			return current
		case <-ctx.Done():
			t.Fatal("the replacement connection did not reach replay confirmation")
			return nil
		}
	}
	rig.fake.closeConnections()
	first := nextConfirmation()
	require.NoError(t, first.Err())
	rig.fake.closeConnections()
	second := nextConfirmation()
	assert.ErrorIs(t, first.Err(), context.Canceled, "a replacement socket cannot confirm the previous socket's replay")
	assert.NoError(t, second.Err(), "the new connection retains its own live confirmation context")
}

func TestKimiFakeStateReplayStillSuppliesTheInitialConversationPage(t *testing.T) {
	t.Parallel()
	const state = `{"type":"task.list","session_id":"session_1","agent_id":"main","tasks":[]}`
	const started = `{"type":"turn.started","session_id":"session_1","agent_id":"main","event_created_at":"2026-11-18T00:00:00Z","turn":{"turn_id":"turn-1","ordinal":0,"kind":"agent","status":"running","started_at":"2026-11-18T00:00:00Z","steps":[]}}`
	const page = `{"type":"turns","session_id":"session_1","agent_id":"main","event_created_at":"2026-11-18T00:00:00Z","turns":[],"has_more":false}`
	for _, tc := range []struct {
		name         string
		replay       []string
		taskLists    int
		starts       int
		explicitPage bool
	}{
		{name: "empty replay"},
		{name: "state only", replay: []string{state}, taskLists: 1},
		{name: "live event", replay: []string{started}, starts: 1},
		{name: "state and live event", replay: []string{state, started}, taskLists: 1, starts: 1},
		{name: "explicit page", replay: []string{state, page, started}, taskLists: 1, starts: 1, explicitPage: true},
		{name: "foreign agent", replay: []string{strings.ReplaceAll(page, `"main"`, `"other-agent"`)}},
		{name: "foreign session", replay: []string{strings.ReplaceAll(page, `"session_1"`, `"other-session"`)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rig := newKimiStreamRig(t)
			replay := make([]any, len(tc.replay))
			for i, raw := range tc.replay {
				replay[i] = json.RawMessage(raw)
			}
			rig.fake.store("session_1", fakeKapSession{ReplayFrames: replay})
			ctx := testutil.DeadlineContext(t)
			require.NoError(t, rig.stream.subscribe(ctx, "session_1"))
			observed := make(chan []kimiFrame, 1)
			_, err := rig.stream.requestFrame(ctx, kimiFrameRequest{
				operation: "native replay barrier",
				build: func(id string) any {
					return map[string]any{"type": "task.unwatch", "request_id": id, "session_id": "session_1", "agent_id": "main", "task_id": "unused-task"}
				},
				beforeDispatch: func(kimiFrame) {
					rig.stream.queueResponseBarrier(func() { observed <- rig.received() })
				},
			})
			require.NoError(t, err)
			var frames []kimiFrame
			select {
			case frames = <-observed:
			case <-ctx.Done():
				t.Fatal("the response after subscription replay did not reach dispatch")
			}
			pages, taskLists, starts := 0, 0, 0
			for _, frame := range frames {
				switch frame.Type {
				case contracts.KimiEventTaskList:
					taskLists++
				case contracts.KimiFrameTurnStarted:
					starts++
					assert.Equal(t, 1, pages, "the initial page precedes live conversation events")
				case contracts.KimiFrameTurns:
					page, err := decodeKimiConversationFrame(frame.Raw)
					require.NoError(t, err)
					assert.Equal(t, "session_1", page.SessionID)
					assert.Equal(t, "main", page.AgentID)
					require.NotNil(t, page.HasMore)
					assert.False(t, *page.HasMore)
					if tc.explicitPage {
						assert.JSONEq(t, tc.replay[1], string(frame.Raw), "an explicit native page remains unchanged")
					}
					pages++
				}
			}
			assert.Equal(t, tc.taskLists, taskLists, "native state replay remains visible")
			assert.Equal(t, tc.starts, starts, "native live events remain visible")
			assert.Equal(t, 1, pages, "state replay cannot replace the required conversation page")
		})
	}
}

func TestKimiStreamAnswersEveryPing(t *testing.T) {
	t.Parallel()
	rig := newKimiStreamRig(t)

	// The native heartbeat carries no nonce. Every identical ping needs a pong.
	for range 3 {
		rig.fake.push(t, map[string]any{"type": kimiFramePing})
	}
	waitFor(t, func() bool { return len(rig.fake.pongFrames()) == 3 }, "every native ping gets its own pong")
	for _, raw := range rig.fake.pongFrames() {
		assert.JSONEq(t, `{"type":"pong"}`, string(raw))
	}
	assert.Empty(t, rig.conversationFrames(), "heartbeat frames cannot enter conversation output")
}

func TestKimiStreamDeliversEventsInOrder(t *testing.T) {
	t.Parallel()
	rig := newKimiStreamRig(t)
	rig.fake.store("session_1", fakeKapSession{})
	err := rig.stream.subscribe(t.Context(), "session_1")
	require.NoError(t, err)
	for index := 1; index <= 21; index++ {
		if index == 21 {
			rig.fake.push(t, "not a frame")
		}
		rig.fake.push(t, map[string]any{"type": "item.delta", "session_id": "session_1", "agent_id": "main", "event_created_at": "2026-11-18T00:00:00Z", "turn_id": "turn-1", "item_id": "answer:1", "field": "text", "delta": strconv.Itoa(index)})
	}
	waitFor(t, func() bool { return len(rig.conversationFrames()) == 21 }, "every native item delta arrives")
	for index, received := range rig.conversationFrames() {
		frame, err := decodeKimiConversationFrame(received.Raw)
		require.NoError(t, err)
		require.NotNil(t, frame.Delta)
		assert.Equal(t, strconv.Itoa(index+1), *frame.Delta)
		assert.Equal(t, "answer:1", frame.ItemID)
	}
	subscription, tracked := rig.subscription("session_1")
	require.True(t, tracked)
	assert.Equal(t, []string{"main"}, subscription.Agents)
	assert.Equal(t, []string{"main"}, subscription.Agents)
}

func TestKimiStreamSubscribe(t *testing.T) {
	t.Parallel()

	t.Run("tracks an accepted session until its transcript arrives", func(t *testing.T) {
		t.Parallel()
		rig := newKimiStreamRig(t)
		rig.fake.store("session_1", fakeKapSession{})
		err := rig.stream.subscribe(t.Context(), "session_1")
		require.NoError(t, err)
		cursor, tracked := rig.subscription("session_1")
		assert.True(t, tracked)
		assert.Equal(t, []string{"main"}, cursor.Agents)
		frames := rig.fake.subscribeFrames()
		require.Len(t, frames, 1)
		assertKimiNativeSubscriptionCommand(t, frames[0])
	})

	t.Run("reports a session the server has not loaded", func(t *testing.T) {
		t.Parallel()
		rig := newKimiStreamRig(t)
		err := rig.stream.subscribe(context.Background(), "session_unknown")
		require.ErrorIs(t, err, errKimiSessionUnavailable)
		_, tracked := rig.subscription("session_unknown")
		assert.False(t, tracked, "a session the server refused is not re-subscribed on a reconnect")
	})

	t.Run("reports a refused subscribe", func(t *testing.T) {
		t.Parallel()
		rig := newKimiStreamRig(t)
		rig.fake.setSubscribeCodeFor(func(fakeKapSubscribe) int { return 40300 })
		err := rig.stream.subscribe(context.Background(), "session_1")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "40300")
	})

	t.Run("returns when its context ends", func(t *testing.T) {
		t.Parallel()
		rig := newKimiStreamRig(t)
		release := make(chan struct{})
		rig.fake.setSubscribeCodeFor(func(sub fakeKapSubscribe) int {
			<-release
			return kimiCodeOK
		})
		t.Cleanup(func() { close(release) })
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := rig.stream.subscribe(ctx, "session_1")
		require.ErrorIs(t, err, context.Canceled)
	})

	// coder/websocket closes the whole connection when the context of a write
	// ends, and the stream carries every session of the agent. So a subscribe
	// whose caller gave up must write nothing. The library picks at random
	// whether such a write takes its lock, so one call proves little.
	t.Run("sends nothing and keeps the connection when its context ended", func(t *testing.T) {
		t.Parallel()
		rig := newKimiStreamRig(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		for range 50 {
			err := rig.stream.subscribe(ctx, "session_1")
			require.ErrorIs(t, err, context.Canceled)
		}
		assert.Empty(t, rig.fake.subscribeFrames(), "no subscribe of a caller that gave up reaches the server")

		rig.fake.store("session_2", fakeKapSession{})
		err := rig.stream.subscribe(context.Background(), "session_2")
		require.NoError(t, err)
		assert.Equal(t, 1, rig.fake.connectionCount(), "the stream keeps its first connection")
	})

	t.Run("fails when the socket closes before the ack", func(t *testing.T) {
		t.Parallel()
		rig := newKimiStreamRig(t)
		release := make(chan struct{})
		rig.fake.setSubscribeCodeFor(func(sub fakeKapSubscribe) int {
			<-release
			return kimiCodeOK
		})
		done := make(chan error, 1)
		go func() {
			err := rig.stream.subscribe(context.Background(), "session_1")
			done <- err
		}()
		waitFor(t, func() bool { return len(rig.fake.subscribeFrames()) == 1 }, "the subscribe reaches the server")
		rig.fake.closeConnections()
		err := <-done
		close(release)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "closed before the subscribe response")
	})
}

func TestKimiStreamStateCannotCreateOrChangeDesiredSubscriptions(t *testing.T) {
	t.Parallel()
	stream := newKimiStream(nil, "test-agent", quartz.NewReal(), nil, nil)
	stream.sessions["session_1"] = kimiSubscription{Agents: []string{"main", "agent-1"}}
	for _, sessionID := range []string{"session_1", "foreign", ""} {
		raw, err := json.Marshal(map[string]any{"type": "agent", "session_id": sessionID, "agent_id": "main", "event_created_at": "2026-11-18T00:00:00Z", "status": "idle", "compacting": false, "plan_mode": false, "swarm_mode": false, "tower_mode": false})
		require.NoError(t, err)
		var frame kimiFrame
		require.NoError(t, json.Unmarshal(raw, &frame))
		stream.observeSessionState(frame)
	}
	assert.Equal(t, map[string]kimiSubscription{"session_1": {Agents: []string{"main", "agent-1"}}}, stream.sessions)
}

func TestKimiStreamReconnectsAndRestoresSubscriptions(t *testing.T) {
	t.Parallel()
	rig := newKimiStreamRig(t)
	rig.fake.store("session_1", fakeKapSession{})
	err := rig.stream.subscribe(t.Context(), "session_1")
	require.NoError(t, err)
	push := func(text string) {
		rig.fake.push(t, map[string]any{"type": "item.delta", "session_id": "session_1", "agent_id": "main", "event_created_at": "2026-11-18T00:00:00Z", "turn_id": "turn-1", "item_id": "answer:1", "field": "text", "delta": text})
	}
	push("before reconnect")
	waitFor(t, func() bool { return len(rig.conversationFrames()) == 1 }, "the first native event arrives")
	rig.fake.closeConnections()
	waitFor(t, func() bool { return len(rig.fake.subscribeFrames()) == 2 }, "the stream restores its exact subscription on a new socket")
	subscription := rig.fake.subscribeFrames()[1]
	assert.Equal(t, "session_1", subscription.SessionID)
	assert.Equal(t, []string{"main"}, subscription.AgentIDs)
	assertKimiNativeSubscriptionCommand(t, subscription)
	waitFor(t, func() bool { return len(rig.reconnected()) == 1 }, "the subscription acknowledgement reaches its handler")
	assert.Equal(t, []kimiReconnect{{sessionID: "session_1"}}, rig.reconnected(), "the new protocol restores pages rather than a durable event cursor")
	push("after reconnect")
	waitFor(t, func() bool { return len(rig.conversationFrames()) == 2 }, "native events flow on the new socket")
	last, err := decodeKimiConversationFrame(rig.conversationFrames()[1].Raw)
	require.NoError(t, err)
	require.NotNil(t, last.Delta)
	assert.Equal(t, "after reconnect", *last.Delta)
}

func TestKimiStreamRefusalSettlesItsRecoveryBarrierOnce(t *testing.T) {
	t.Parallel()
	rig := newKimiStreamRig(t)
	rig.fake.store("session_1", fakeKapSession{})
	require.NoError(t, rig.stream.subscribe(t.Context(), "session_1"))
	applied := make(chan bool, 2)
	rig.stream.recovering = func(context.Context, string) func(bool) { return func(accepted bool) { applied <- accepted } }
	rig.fake.setSubscribeCodeFor(func(fakeKapSubscribe) int { return 40300 })
	rig.stream.resubscribeAll(t.Context())
	ctx := testutil.DeadlineContext(t)
	select {
	case accepted := <-applied:
		assert.False(t, accepted)
	case <-ctx.Done():
		t.Fatal("the refused native response did not settle recovery")
	}
	rig.stream.queueResponseBarrier(func() { applied <- true })
	select {
	case marker := <-applied:
		assert.True(t, marker, "recovery settles exactly once before the following barrier")
	case <-ctx.Done():
		t.Fatal("the following dispatch barrier did not run")
	}
}

func TestKimiStreamReconnectsWithoutPriorConversation(t *testing.T) {
	t.Parallel()
	rig := newKimiStreamRig(t)
	rig.fake.store("session_1", fakeKapSession{})
	// No conversation has arrived; reconnect still requests fresh native pages.
	rig.fake.setSubscribeCodeFor(func(sub fakeKapSubscribe) int { return kimiCodeOK })
	err := rig.stream.subscribe(context.Background(), "session_1")
	require.NoError(t, err)

	rig.fake.closeConnections()
	waitFor(t, func() bool { return len(rig.reconnected()) == 1 }, "the stream re-subscribes on a new socket")
	assertKimiNativeSubscriptionCommand(t, rig.fake.subscribeFrames()[1])
	assert.Equal(t, []kimiReconnect{{sessionID: "session_1"}}, rig.reconnected(),
		"the server confirms the renewed native subscription")
}

func TestKimiStreamResyncsWhatItCannotReplay(t *testing.T) {
	t.Parallel()
	rig := newKimiStreamRig(t)
	rig.fake.store("session_1", fakeKapSession{})
	rig.fake.store("session_2", fakeKapSession{})
	err := rig.stream.subscribe(context.Background(), "session_1")
	require.NoError(t, err)
	err = rig.stream.subscribe(context.Background(), "session_2")
	require.NoError(t, err)

	rig.fake.setSubscribeCodeFor(func(sub fakeKapSubscribe) int {
		if sub.SessionID == "session_2" {
			return kimiCodeSessionNotFound
		}
		return kimiCodeOK
	})
	rig.fake.closeConnections()
	waitFor(t, func() bool { return len(rig.reconnected()) == 2 }, "both sessions resync")
	assert.Equal(t, []kimiReconnect{{sessionID: "session_1"}, {sessionID: "session_2"}}, rig.reconnected(),
		"each renewed or unavailable session reaches its recovery handler once")
}

// kimiGoroutineStacks returns the stack of every goroutine in the process.
func kimiGoroutineStacks() string {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			return string(buf[:n])
		}
		buf = make([]byte, 2*len(buf))
	}
}

// kimiStreamLoops counts the goroutines in the process that run the reader
// loop or the dispatcher loop of an event stream.
func kimiStreamLoops() (readers, dispatchers int) {
	stacks := kimiGoroutineStacks()
	return strings.Count(stacks, "kimi.(*kimiStream).runReader("), strings.Count(stacks, "kimi.(*kimiStream).runDispatcher(")
}

// waitForKimiStreamLoops waits until at most readers readers and dispatchers
// dispatchers run. A goroutine that closes its done channel from a deferred call
// is still on its stack for a moment after, so the count can lag the close.
func waitForKimiStreamLoops(t *testing.T, readers, dispatchers int, msg string) {
	t.Helper()
	waitFor(t, func() bool {
		r, d := kimiStreamLoops()
		return r <= readers && d <= dispatchers
	}, msg)
}

// waitForStreamWait waits until stream.wait returns, which it does once every
// goroutine of the stream returned.
func waitForStreamWait(t *testing.T, stream *kimiStream) {
	t.Helper()
	waited := make(chan struct{})
	go func() {
		stream.wait()
		close(waited)
	}()
	waitFor(t, func() bool {
		select {
		case <-waited:
			return true
		default:
			return false
		}
	}, "wait returns once the goroutines of the stream return")
}

// TestKimiStreamCloseEndsItsGoroutines observes the goroutines themselves, so
// a close that only sets a flag does not pass it.
//
// It counts goroutines in the whole process, so it does not call t.Parallel:
// the streams of a parallel test would change the count. The parallel tests of
// the package start only after every sequential test ends.
func TestKimiStreamCloseEndsItsGoroutines(t *testing.T) {
	t.Run("while the reader reads", func(t *testing.T) {
		readers, dispatchers := kimiStreamLoops()
		rig := newKimiStreamRig(t)
		// start returns before its two goroutines run, and a goroutine that has not
		// run yet shows its closure on its stack rather than the loop. Wait until
		// both loops run, then check that each runs once.
		waitFor(t, func() bool {
			r, d := kimiStreamLoops()
			return r > readers && d > dispatchers
		}, "the stream starts its reader and its dispatcher")
		r, d := kimiStreamLoops()
		require.Equal(t, readers+1, r, "the stream runs one reader")
		require.Equal(t, dispatchers+1, d, "the stream runs one dispatcher")

		rig.stream.close()
		rig.stream.close()
		waitForStreamWait(t, rig.stream)
		waitForKimiStreamLoops(t, readers, dispatchers, "the reader and the dispatcher return once the stream closes")
		require.Error(t, rig.stream.write(map[string]any{"type": "noop"}), "a closed stream writes nothing")
		// An unsubscribe after the close only forgets the session.
		rig.stream.unsubscribe("session_1")
	})

	t.Run("while the reader waits to reconnect", func(t *testing.T) {
		readers, dispatchers := kimiStreamLoops()
		ctx := testutil.DeadlineContext(t)
		// The mock clock never advances, so only the close can end the wait.
		clock := testutil.NewQuartzMock(t)
		armed := clock.Trap().NewTimer(kimiStreamReconnectTimerTag)
		defer armed.Close()
		rig := newKimiStreamRigOnClock(t, clock, kimiStreamFirstBackoff)
		rig.fake.closeConnections()
		assert.Equal(t, kimiStreamFirstBackoff, testutil.WaitForTimer(t, ctx, armed), "the reader waits before it dials again")

		rig.stream.close()
		waitForStreamWait(t, rig.stream)
		waitForKimiStreamLoops(t, readers, dispatchers, "the close ends the wait, and both goroutines return")
		assert.Equal(t, 1, rig.fake.socketDialCount(), "the closed stream never dials again")
	})
}

func TestKimiStreamUnsubscribe(t *testing.T) {
	t.Parallel()
	rig := newKimiStreamRig(t)
	rig.fake.store("session_1", fakeKapSession{})
	err := rig.stream.subscribe(context.Background(), "session_1")
	require.NoError(t, err)

	rig.stream.unsubscribe("session_1")
	waitFor(t, func() bool { return len(rig.fake.unsubscribeFrames()) == 1 }, "the unsubscribe reaches the server")
	assert.Equal(t, [][]string{{"session_1"}}, rig.fake.unsubscribeFrames())
	_, tracked := rig.subscription("session_1")
	assert.False(t, tracked, "a reconnect does not re-subscribe it")
}

func TestKimiStreamStartFailsForARefusedSocket(t *testing.T) {
	t.Parallel()
	_, server := newFakeKap(t)
	endpoint, err := providerkit.NewHTTPEndpoint(server.URL, providerkit.BearerAuth("wrong-token"))
	require.NoError(t, err)
	t.Cleanup(endpoint.Close)
	stream := newKimiStream(endpoint, "test-agent", quartz.NewReal(), func(kimiFrame) {}, nil)
	err = stream.start(context.Background(), 10*time.Second)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "open the Kimi event stream")
}

func TestKimiStreamStartAfterCloseFails(t *testing.T) {
	t.Parallel()
	fake, server := newFakeKap(t)
	endpoint, err := providerkit.NewHTTPEndpoint(server.URL, providerkit.BearerAuth(fakeKapToken))
	require.NoError(t, err)
	t.Cleanup(endpoint.Close)
	stream := newKimiStream(endpoint, "test-agent", quartz.NewReal(), func(kimiFrame) {}, nil)

	stream.close()
	require.ErrorIs(t, stream.start(context.Background(), 10*time.Second), errKimiStreamClosed)
	stream.wait()
	assert.Zero(t, fake.socketDialCount(), "a stream that its close ended dials nothing")
}

// A server that restarts refuses the socket for a moment. The reader keeps
// dialing until a dial succeeds, and the re-subscribe then runs as usual.
func TestKimiStreamKeepsDialingAfterAFailedReconnect(t *testing.T) {
	t.Parallel()
	rig := newKimiStreamRig(t)
	rig.fake.store("session_1", fakeKapSession{})
	err := rig.stream.subscribe(context.Background(), "session_1")
	require.NoError(t, err)

	rig.fake.refuseNextSockets(3)
	rig.fake.closeConnections()
	waitFor(t, func() bool { return len(rig.reconnected()) == 1 }, "the stream re-subscribes once a dial succeeds")
	assert.Equal(t, 5, rig.fake.socketDialCount(), "the first socket, three refused dials, and the one that succeeded")
	assert.Equal(t, []kimiReconnect{{sessionID: "session_1"}}, rig.reconnected())
}

// Each refused dial doubles the wait before the next one, up to the cap. The
// test reads the delay that the reader ASKS the clock for, so the host's timer
// granularity cannot blur two steps.
func TestKimiStreamReconnectBackoffDoublesUpToItsCap(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	clock := testutil.NewQuartzMock(t)
	armed := clock.Trap().NewTimer(kimiStreamReconnectTimerTag)
	defer armed.Close()
	rig := newKimiStreamRigOnClock(t, clock, kimiStreamFirstBackoff)

	want := []time.Duration{
		250 * time.Millisecond, 500 * time.Millisecond, time.Second, 2 * time.Second, 4 * time.Second,
		kimiStreamMaxBackoff, kimiStreamMaxBackoff, kimiStreamMaxBackoff,
	}
	rig.fake.refuseNextSockets(len(want) - 1)
	rig.fake.closeConnections()
	for i, delay := range want {
		require.Equal(t, delay, testutil.WaitForTimer(t, ctx, armed), "the wait before dial %d", i+1)
		clock.Advance(delay).MustWait(ctx)
	}
	waitFor(t, func() bool { return rig.fake.connectionCount() == 1 }, "the last dial opens a socket")
	assert.Equal(t, 1+len(want), rig.fake.socketDialCount(), "the first socket, the refused dials, and the one that succeeded")
}

// The server pings every 10 seconds, so a connection that sends nothing for the
// read timeout is dead. The reader drops it and reconnects, and the new socket
// delivers events.
func TestKimiStreamReconnectsAConnectionThatFallsSilent(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	clock := testutil.NewQuartzMock(t)
	reads := clock.Trap().AfterFunc(kimiStreamReadTimerTag)
	defer reads.Close()
	armed := clock.Trap().NewTimer(kimiStreamReconnectTimerTag)
	defer armed.Close()
	rig := newKimiStreamRigOnClock(t, clock, kimiStreamFirstBackoff)

	// Native sockets send no hello. The first read waits on the silent socket.
	call := reads.MustWait(ctx)
	call.MustRelease(ctx)
	reads.Close()
	require.Equal(t, kimiStreamReadTimeout, call.Duration)

	clock.Advance(kimiStreamReadTimeout - time.Nanosecond).MustWait(ctx)
	left, pending := clock.Peek()
	require.True(t, pending, "the read still waits")
	assert.Equal(t, time.Nanosecond, left, "a silence one nanosecond short of the timeout keeps the socket")
	assert.Equal(t, 1, rig.fake.socketDialCount())

	clock.Advance(time.Nanosecond).MustWait(ctx)
	assert.Equal(t, kimiStreamFirstBackoff, testutil.WaitForTimer(t, ctx, armed), "the silent socket is dropped, and the reader waits to reconnect")
	clock.Advance(kimiStreamFirstBackoff).MustWait(ctx)
	waitFor(t, func() bool { return rig.fake.connectionCount() == 2 }, "the reader opens a new socket")
	assert.Equal(t, 2, rig.fake.socketDialCount())

	rig.fake.push(t, json.RawMessage(kimiConversationTestFrame(t, "session_1", "main", `{"type":"turn.ended","turn":{"turn_id":"turn-1","ordinal":0,"kind":"agent","status":"ended","outcome":"completed","started_at":"2026-11-18T00:00:00Z","steps":[]}}`)))
	waitFor(t, func() bool { return len(rig.conversationFrames()) == 1 }, "the new socket delivers events")
}

// A read that receives no frame for the read timeout fails with
// errKimiStreamSilent. A read whose caller's context ends fails with the
// context's error instead, so the log states the real cause.
func TestKimiStreamRead(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	clock := testutil.NewQuartzMock(t)
	_, server := newFakeKap(t)
	endpoint, err := providerkit.NewHTTPEndpoint(server.URL, providerkit.BearerAuth(fakeKapToken))
	require.NoError(t, err)
	t.Cleanup(endpoint.Close)
	stream := newKimiStream(endpoint, "test-agent", clock, func(kimiFrame) {}, nil)

	// Native sockets send no hello. Trap the first read's timer before starting it.
	readAsync := func(t *testing.T, ctx context.Context) <-chan error {
		t.Helper()
		conn, err := stream.dial(ctx, 10*time.Second)
		require.NoError(t, err)
		t.Cleanup(func() { _ = conn.CloseNow() })

		armed := clock.Trap().AfterFunc(kimiStreamReadTimerTag)
		t.Cleanup(armed.Close)
		result := make(chan error, 1)
		go func() {
			_, err := stream.read(ctx, conn)
			result <- err
		}()
		call := armed.MustWait(ctx)
		// Release before the assertion: a call left trapped blocks the read, and a
		// failed test would then hang rather than fail.
		call.MustRelease(ctx)
		armed.Close()
		require.Equal(t, kimiStreamReadTimeout, call.Duration)
		return result
	}
	awaitResult := func(t *testing.T, result <-chan error) error {
		t.Helper()
		select {
		case err := <-result:
			return err
		case <-ctx.Done():
			require.FailNow(t, "the read never returned")
			return nil
		}
	}

	t.Run("a silent socket", func(t *testing.T) {
		result := readAsync(t, ctx)
		clock.Advance(kimiStreamReadTimeout).MustWait(ctx)
		require.ErrorIs(t, awaitResult(t, result), errKimiStreamSilent)
		_, pending := clock.Peek()
		assert.False(t, pending, "the failed read leaves no timer behind")
	})

	t.Run("a caller that gives up", func(t *testing.T) {
		readCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		result := readAsync(t, readCtx)
		cancel()
		err := awaitResult(t, result)
		require.Error(t, err)
		assert.NotErrorIs(t, err, errKimiStreamSilent)
		_, pending := clock.Peek()
		assert.False(t, pending, "the ended read leaves no timer behind")
	})
}

func TestKimiStreamResubscribeAll(t *testing.T) {
	t.Parallel()

	subscribed := func(t *testing.T, ids ...string) *kimiStreamRig {
		t.Helper()
		rig := newKimiStreamRig(t)
		for _, id := range ids {
			rig.fake.store(id, fakeKapSession{})
			err := rig.stream.subscribe(context.Background(), id)
			require.NoError(t, err)
		}
		return rig
	}

	t.Run("reports nothing when the server refuses the re-subscribe", func(t *testing.T) {
		t.Parallel()
		rig := subscribed(t, "session_1")
		rig.fake.setSubscribeCodeFor(func(fakeKapSubscribe) int { return 40300 })
		rig.stream.resubscribeAll(context.Background())
		assert.Len(t, rig.fake.subscribeFrames(), 2, "the re-subscribe reached the server")
		assert.Empty(t, rig.reconnected(), "no session was re-subscribed, so none is reported")
	})

	t.Run("reports a native unavailable session", func(t *testing.T) {
		t.Parallel()
		rig := subscribed(t, "session_1")
		rig.fake.setSubscribeCodeFor(func(fakeKapSubscribe) int { return kimiCodeSessionNotFound })
		rig.stream.resubscribeAll(t.Context())
		assert.Equal(t, []kimiReconnect{{sessionID: "session_1"}}, rig.reconnected())
	})

	t.Run("re-subscribes each tracked session in id order", func(t *testing.T) {
		t.Parallel()
		rig := subscribed(t, "session_b", "session_a")
		rig.stream.resubscribeAll(context.Background())
		frames := rig.fake.subscribeFrames()
		require.Len(t, frames, 4)
		assert.Equal(t, "session_a", frames[2].SessionID)
		assert.Equal(t, "session_b", frames[3].SessionID)
		assert.Equal(t, []kimiReconnect{{sessionID: "session_a"}, {sessionID: "session_b"}}, rig.reconnected())
	})

	t.Run("sends nothing when no session is tracked", func(t *testing.T) {
		t.Parallel()
		rig := newKimiStreamRig(t)
		rig.stream.resubscribeAll(context.Background())
		assert.Empty(t, rig.fake.subscribeFrames())
		assert.Empty(t, rig.reconnected())
	})

	t.Run("runs no handler when the stream has none", func(t *testing.T) {
		t.Parallel()
		rig := subscribed(t, "session_1")
		rig.stream.reconnected = nil
		assert.NotPanics(t, func() { rig.stream.resubscribeAll(context.Background()) })
		assert.Len(t, rig.fake.subscribeFrames(), 2)
	})
}

// An ack whose payload does not decode fails its own subscribe. The real ack
// that arrives after it finds no subscribe waiting, and the stream reads on.
func TestKimiStreamLateResponseCannotAnswerAnotherSubscription(t *testing.T) {
	t.Parallel()
	rig := newKimiStreamRig(t)
	release := make(chan struct{})
	releaseFirst := sync.OnceFunc(func() { close(release) })
	defer releaseFirst()
	entered := make(chan struct{})
	rig.fake.setSubscribeCodeFor(func(sub fakeKapSubscribe) int {
		if sub.SessionID == "session_1" {
			close(entered)
			<-release
		}
		if sub.SessionID == "session_2" {
			return 40300
		}
		return kimiCodeOK
	})
	ctx := testutil.DeadlineContext(t)
	firstCtx, cancelFirst := context.WithCancel(ctx)
	defer cancelFirst()
	first := make(chan error, 1)
	go func() { first <- rig.stream.subscribe(firstCtx, "session_1") }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("the first native subscription did not arrive")
	}
	cancelFirst()
	require.ErrorIs(t, <-first, agent.ErrDeliveryUncertain)
	second := make(chan error, 1)
	go func() { second <- rig.stream.subscribe(ctx, "session_2") }()
	waitFor(t, func() bool {
		rig.stream.mu.Lock()
		defer rig.stream.mu.Unlock()
		_, pending := rig.stream.acks["2"]
		return pending
	}, "the second request owns its waiter before the first response is released")
	releaseFirst()
	select {
	case err := <-second:
		require.ErrorContains(t, err, "40300")
	case <-ctx.Done():
		t.Fatal("the second native response did not arrive")
	}
	require.NoError(t, rig.stream.subscribe(ctx, "session_3"))
	assert.Empty(t, rig.conversationFrames(), "native responses never become conversation events")
}

func TestKimiStreamRoutesConcurrentSubscribes(t *testing.T) {
	t.Parallel()
	rig := newKimiStreamRig(t)
	const count = 16
	ids := make([]string, count)
	for i := range ids {
		ids[i] = "session_" + strconv.Itoa(i)
		rig.fake.store(ids[i], fakeKapSession{})
	}

	rig.fake.setSubscribeCodeFor(func(sub fakeKapSubscribe) int {
		index, err := strconv.Atoi(strings.TrimPrefix(sub.SessionID, "session_"))
		if err != nil {
			return 49999
		}
		if index == 0 {
			return kimiCodeOK
		}
		return 40000 + index
	})
	errs := make([]error, count)
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Go(func() {
			errs[i] = rig.stream.subscribe(context.Background(), id)
		})
	}
	wg.Wait()
	for i, id := range ids {
		if i == 0 {
			require.NoError(t, errs[i], id)
		} else {
			require.ErrorContains(t, errs[i], strconv.Itoa(40000+i), id)
		}
		_, tracked := rig.subscription(id)
		assert.Equal(t, i == 0, tracked, id)
	}
}

// A subscribe that fails leaves no session behind that it added: a reconnect
// would re-subscribe it, and the server would stream a session that no agent
// drives. A session that was tracked before the call stays tracked.
func TestKimiStreamSubscribeThatFailsTracksNoNewSession(t *testing.T) {
	t.Parallel()
	rig := newKimiStreamRig(t)
	rig.fake.store("session_1", fakeKapSession{})
	err := rig.stream.subscribe(context.Background(), "session_1")
	require.NoError(t, err)

	rig.fake.setSubscribeCodeFor(func(fakeKapSubscribe) int { return 40300 })
	rig.fake.store("session_2", fakeKapSession{})
	err = rig.stream.subscribe(context.Background(), "session_2")
	require.Error(t, err)
	_, tracked := rig.subscription("session_2")
	assert.False(t, tracked, "the session that the failed subscribe added is not tracked")

	err = rig.stream.subscribe(context.Background(), "session_1")
	require.Error(t, err)
	_, tracked = rig.subscription("session_1")
	assert.True(t, tracked, "a failed subscribe of a tracked session keeps it for the next reconnect")

	// A resync subscribes the session that the agent drives again. A server that
	// still does not find it must not remove it from the next reconnect.
	rig.fake.setSubscribeCodeFor(func(sub fakeKapSubscribe) int { return kimiCodeSessionNotFound })
	err = rig.stream.subscribe(context.Background(), "session_1")
	require.ErrorIs(t, err, errKimiSessionUnavailable)
	_, tracked = rig.subscription("session_1")
	assert.True(t, tracked, "a tracked session that the server did not find stays tracked")
}

func TestKimiStreamDropsCachedStateWhileDisconnected(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	clock := testutil.NewQuartzMock(t)
	reconnectTimer := clock.Trap().NewTimer(kimiStreamReconnectTimerTag)
	defer reconnectTimer.Close()
	rig := newKimiStreamRigOnClock(t, clock, kimiStreamFirstBackoff)
	rig.fake.store("session_1", fakeKapSession{Model: "kimi-k2", Permission: "manual"})
	err := rig.stream.subscribe(ctx, "session_1")
	require.NoError(t, err)
	waitFor(t, func() bool {
		_, exists := rig.stream.sessionStatus("session_1")
		return exists
	}, "the native state frame arrives after its acknowledgement")
	rig.fake.closeConnections()
	assert.Equal(t, kimiStreamFirstBackoff, testutil.WaitForTimer(t, ctx, reconnectTimer))
	_, exists := rig.stream.sessionStatus("session_1")
	assert.False(t, exists, "disconnected state must not satisfy a settings confirmation")
}
