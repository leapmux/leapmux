package cline

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/coder/quartz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
)

// newTestHubClient connects a hub client to a new fake hub, and records every
// event it dispatches.
func newTestHubClient(t *testing.T) (*hubClient, *fakeHub, *eventRecorder) {
	t.Helper()
	hub, server := newFakeHubServer(t)
	record := fakeRecord(server.URL)
	endpoint, path, err := hubEndpoint(record)
	require.NoError(t, err)
	recorder := &eventRecorder{}
	client := newHubClient(endpoint, path, record.AuthToken, "client-1", "agent-1",
		map[string]any{"clientId": "client-1", "clientType": clientType}, recorder.record, quartz.NewReal())
	client.backoff = 5 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		client.close()
		client.wait()
		cancel()
	})
	require.NoError(t, client.start(ctx, 30*time.Second))
	return client, hub, recorder
}

// eventRecorder records the events a client dispatches.
type eventRecorder struct {
	mu     sync.Mutex
	events []hubEvent
}

func (r *eventRecorder) record(event hubEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
}

func (r *eventRecorder) names() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	names := make([]string, 0, len(r.events))
	for _, event := range r.events {
		names = append(names, event.Event)
	}
	return names
}

func (r *eventRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.events)
}

func TestHubClientRegistersFirst(t *testing.T) {
	t.Parallel()
	_, hub, _ := newTestHubClient(t)
	commands := hub.commandsNamed(commandClientRegister)
	require.Len(t, commands, 1)
	assert.Equal(t, "client-1", commands[0].ClientID)
	assert.Equal(t, clientType, commands[0].str("clientType"))
}

func TestHubClientCommandReturnsThePayload(t *testing.T) {
	t.Parallel()
	client, hub, _ := newTestHubClient(t)
	hub.handle("session.get", func(command fakeCommand) fakeReply {
		return fakeReply{Payload: map[string]any{"echo": command.str("sessionId")}}
	})
	reply, err := client.command(context.Background(), "session.get", "sess-9", map[string]any{"sessionId": "sess-9"})
	require.NoError(t, err)
	assert.JSONEq(t, `{"echo":"sess-9"}`, string(reply))
	command := hub.commandsNamed("session.get")[0]
	assert.Equal(t, "sess-9", command.SessionID, "the envelope states the session")
	assert.Equal(t, "client-1", command.ClientID, "every command states the registered client")
}

// coder/websocket closes the whole connection when the context of a write
// ends, and the connection carries the agent's session. So a command whose
// caller gave up must write nothing. The library picks at random whether such a
// write takes its lock, so one call proves little.
func TestHubClientSendsNothingForACallerThatGaveUp(t *testing.T) {
	t.Parallel()
	client, hub, _ := newTestHubClient(t)
	hub.handle("session.get", func(fakeCommand) fakeReply {
		return fakeReply{Payload: map[string]any{}}
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for range 50 {
		_, err := client.command(ctx, "session.get", "sess-1", nil)
		require.ErrorIs(t, err, context.Canceled)
		require.ErrorIs(t, client.subscribe(ctx, "sess-1"), context.Canceled)
	}
	assert.Empty(t, hub.commandsNamed("session.get"), "no command of a caller that gave up reaches the hub")
	assert.Empty(t, hub.subscribeFrames(), "no subscribe of a caller that gave up reaches the hub")

	_, err := client.command(context.Background(), "session.get", "sess-1", nil)
	require.NoError(t, err)
	assert.Len(t, hub.commandsNamed(commandClientRegister), 1, "the client keeps its first connection")
}

func TestHubClientReportsARefusedCommand(t *testing.T) {
	t.Parallel()
	client, hub, _ := newTestHubClient(t)
	hub.handle("session.get", func(fakeCommand) fakeReply {
		return fakeReply{Code: "session_not_found", Message: "Unknown session: x"}
	})
	_, err := client.command(context.Background(), "session.get", "x", nil)
	var refused *HubCommandError
	require.ErrorAs(t, err, &refused)
	assert.Equal(t, "session_not_found", refused.Code)
	code, ok := hubErrorCode(err)
	assert.True(t, ok)
	assert.Equal(t, "session_not_found", code)
	_, ok = hubErrorCode(errors.New("other"))
	assert.False(t, ok, "only a refusal carries a code")
}

func TestHubClientDeliversOnlySubscribedEvents(t *testing.T) {
	t.Parallel()
	client, hub, recorder := newTestHubClient(t)
	hub.emit("sess-1", "iteration.started", map[string]any{})
	require.NoError(t, client.subscribe(context.Background(), "sess-1"))
	waitFor(t, func() bool { return len(hub.subscribeFrames()) == 1 }, "the subscription arrives")
	hub.emit("sess-2", "iteration.started", map[string]any{})
	hub.emit("sess-1", "assistant.delta", map[string]any{"text": "a"})
	waitFor(t, func() bool { return recorder.count() == 1 }, "the subscribed event arrives")
	assert.Equal(t, []string{"assistant.delta"}, recorder.names(), "an event before the subscription or of another session stays out")
	assert.Nil(t, hub.subscribeFrames()[0].SinceSequence, "a first subscription is live-only")
}

func TestHubClientReplaysWhatAReconnectMissed(t *testing.T) {
	t.Parallel()
	client, hub, recorder := newTestHubClient(t)
	require.NoError(t, client.subscribe(context.Background(), "sess-1"))
	waitFor(t, func() bool { return len(hub.subscribeFrames()) == 1 }, "the subscription arrives")
	last := hub.emit("sess-1", "assistant.delta", map[string]any{"text": "a"})
	waitFor(t, func() bool { return recorder.count() == 1 }, "the first event arrives")

	hub.drop()
	// The event lands in the log while no connection is open.
	hub.emit("sess-1", "assistant.delta", map[string]any{"text": "b"})
	waitFor(t, func() bool { return recorder.count() == 2 }, "the reconnect replays the missed event")
	frames := hub.subscribeFrames()
	require.Len(t, frames, 2)
	require.NotNil(t, frames[1].SinceSequence, "the reconnect subscribes from its cursor")
	assert.Equal(t, last, *frames[1].SinceSequence)
	assert.Len(t, hub.commandsNamed(commandClientRegister), 2, "the reconnect registers again")
}

func TestHubClientFailsACommandWhoseConnectionFails(t *testing.T) {
	t.Parallel()
	client, hub, _ := newTestHubClient(t)
	hub.handle("session.get", func(fakeCommand) fakeReply { return fakeReply{Hold: true} })
	done := make(chan error, 1)
	go func() {
		_, err := client.command(context.Background(), "session.get", "", nil)
		done <- err
	}()
	_, ok := hub.waitCommand("session.get")
	require.True(t, ok)
	hub.drop()
	select {
	case err := <-done:
		assert.ErrorIs(t, err, errHubConnectionLost)
	case <-time.After(30 * time.Second):
		t.Fatal("the command waited on a lost connection")
	}
}

func TestHubClientSendRunsPrepareBeforeTheFrameLeaves(t *testing.T) {
	t.Parallel()
	client, hub, _ := newTestHubClient(t)
	var mu sync.Mutex
	prepared := ""
	hub.handle("probe", func(command fakeCommand) fakeReply {
		// The command reaches the hub only after the prepare recorded its id.
		mu.Lock()
		defer mu.Unlock()
		assert.Equal(t, command.RequestID, prepared)
		return fakeReply{}
	})
	requestID, replies, err := client.send(context.Background(), "probe", "", nil, func(id string) {
		mu.Lock()
		defer mu.Unlock()
		prepared = id
	})
	require.NoError(t, err)
	mu.Lock()
	assert.Equal(t, requestID, prepared)
	mu.Unlock()
	_, err = client.await(context.Background(), "probe", replies)
	require.NoError(t, err)
}

func TestHubClientRefusesCommandsAfterClose(t *testing.T) {
	t.Parallel()
	client, _, _ := newTestHubClient(t)
	client.close()
	client.close()
	_, err := client.command(context.Background(), "session.get", "", nil)
	assert.ErrorIs(t, err, errHubClosed)
}

func TestHubClientRefusesATokenTheHubRejects(t *testing.T) {
	t.Parallel()
	_, server := newFakeHubServer(t)
	record := fakeRecord(server.URL)
	record.AuthToken = "wrong"
	endpoint, path, err := hubEndpoint(record)
	require.NoError(t, err)
	client := newHubClient(endpoint, path, record.AuthToken, "c", "a", map[string]any{}, func(hubEvent) {}, quartz.NewReal())
	err = client.start(context.Background(), 30*time.Second)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "open the Cline hub connection")
}

func TestHubClientDropsAnEventItCannotRead(t *testing.T) {
	t.Parallel()
	client := &hubClient{}
	_, ok := client.decodeEvent(json.RawMessage(`{"payload":{}}`))
	assert.False(t, ok, "an event with no name is dropped")
	_, ok = client.decodeEvent(json.RawMessage(`not json`))
	assert.False(t, ok)
	event, ok := client.decodeEvent(json.RawMessage(`{"event":"x","sessionId":"s","sequence":4}`))
	require.True(t, ok)
	assert.Equal(t, "x", event.Event)
	assert.Zero(t, client.lastSequence, "an event of a session the client does not follow moves no cursor")
}

// A subscribe frame that cannot leave changes nothing: the stream still follows
// the session that it followed. So the reconnect subscribes that session again,
// from its cursor, and not the session that the caller failed to get. A client
// that moved to the failed session would stop the events of the session that
// the agent still drives, and a turn of that session would never end.
func TestHubClientKeepsItsSessionWhenASubscribeFails(t *testing.T) {
	t.Parallel()
	client, hub, recorder := newTestHubClient(t)
	require.NoError(t, client.subscribe(context.Background(), "sess-1"))
	waitFor(t, func() bool { return len(hub.subscribeFrames()) == 1 }, "the subscription arrives")
	last := hub.emit("sess-1", "assistant.delta", map[string]any{"text": "a"})
	waitFor(t, func() bool { return recorder.count() == 1 }, "the first event arrives")

	// The reconnect waits until the test admits it, so the subscribe below
	// meets the closed connection.
	hub.setRefuseUpgrades(true)
	client.mu.Lock()
	conn := client.conn
	client.mu.Unlock()
	require.NoError(t, conn.CloseNow())
	require.Error(t, client.subscribe(context.Background(), "sess-2"), "no frame leaves a closed connection")
	client.mu.Lock()
	leaving := client.leaving
	client.mu.Unlock()
	assert.Empty(t, leaving, "a subscribe that ended moves the stream no more")

	hub.setRefuseUpgrades(false)
	waitFor(t, func() bool { return len(hub.subscribeFrames()) == 2 }, "the reconnect subscribes again")
	frames := hub.subscribeFrames()
	assert.Equal(t, "sess-1", frames[1].SessionID, "the stream follows the session that it followed")
	require.NotNil(t, frames[1].SinceSequence, "the replay starts from the cursor of that session")
	assert.Equal(t, last, *frames[1].SinceSequence)
}

// A reconnect before the first event holds no cursor, and a replay from the
// start would bring back every earlier run of the session, so it subscribes
// live. A client that follows no session subscribes nothing.
func TestHubClientReconnectsLiveWithoutACursor(t *testing.T) {
	t.Parallel()
	t.Run("a session with no event yet", func(t *testing.T) {
		t.Parallel()
		client, hub, _ := newTestHubClient(t)
		require.NoError(t, client.subscribe(context.Background(), "sess-1"))
		waitFor(t, func() bool { return len(hub.subscribeFrames()) == 1 }, "the subscription arrives")
		hub.drop()
		waitFor(t, func() bool { return len(hub.subscribeFrames()) == 2 }, "the reconnect subscribes again")
		frame := hub.subscribeFrames()[1]
		assert.Equal(t, "sess-1", frame.SessionID)
		assert.Nil(t, frame.SinceSequence, "no cursor, no replay")
	})
	t.Run("no session", func(t *testing.T) {
		t.Parallel()
		client, hub, _ := newTestHubClient(t)
		client.resume(context.Background())
		require.Len(t, hub.commandsNamed(commandClientRegister), 2, "the resume registers again")
		// The hub reads the frames of one connection in order, so a subscribe
		// that the resume wrote reaches it before this command.
		_, err := client.command(context.Background(), "probe", "", nil)
		require.NoError(t, err)
		assert.Empty(t, hub.subscribeFrames(), "a client that follows no session subscribes nothing")
	})
}

// The cursor moves forward alone: a replay can repeat an older event, and an
// event without a sequence states nothing about the log.
func TestHubClientCursorMovesForwardAlone(t *testing.T) {
	t.Parallel()
	client := &hubClient{session: "s"}
	for _, tc := range []struct {
		raw  string
		want int64
	}{
		{`{"event":"x","sessionId":"s","sequence":5}`, 5},
		{`{"event":"x","sessionId":"s","sequence":3}`, 5},
		{`{"event":"x","sessionId":"s"}`, 5},
		{`{"event":"x","sessionId":"s","sequence":-1}`, 5},
		{`{"event":"x","sessionId":"other","sequence":9}`, 5},
		{`{"event":"x","sessionId":"s","sequence":7}`, 7},
	} {
		_, ok := client.decodeEvent(json.RawMessage(tc.raw))
		require.True(t, ok, tc.raw)
		assert.Equal(t, tc.want, client.lastSequence, tc.raw)
	}
}

// While a subscribe moves the stream, the events of the session that it leaves
// still move that session's cursor, and a subscribe that fails restores the
// cursor with them: a replay after a reconnect would otherwise bring back
// events that the agent already has.
func TestHubClientCursorFollowsTheSessionThatASubscribeLeaves(t *testing.T) {
	t.Parallel()
	client := &hubClient{session: "new", leaving: "old", leavingSequence: 5}
	for _, raw := range []string{
		`{"event":"x","sessionId":"old","sequence":7}`,
		`{"event":"x","sessionId":"old","sequence":6}`,
		`{"event":"x","sessionId":"new","sequence":3}`,
		`{"event":"x","sessionId":"","sequence":9}`,
	} {
		_, ok := client.decodeEvent(json.RawMessage(raw))
		require.True(t, ok, raw)
	}
	assert.EqualValues(t, 7, client.leavingSequence, "the session that the subscribe leaves keeps its cursor")
	assert.EqualValues(t, 3, client.lastSequence, "the session that the subscribe asks for starts its own")

	idle := &hubClient{session: "s"}
	_, ok := idle.decodeEvent(json.RawMessage(`{"event":"x","sessionId":"","sequence":9}`))
	require.True(t, ok)
	assert.Zero(t, idle.leavingSequence, "no subscribe in flight, no second cursor")
	assert.Zero(t, idle.lastSequence)
}

// A frame that the client cannot read, a frame of an unknown kind, and a reply
// that no command waits for leave the connection as it is: the next event
// arrives on it, and nothing reconnects.
func TestHubClientSkipsAFrameItCannotUse(t *testing.T) {
	t.Parallel()
	client, hub, recorder := newTestHubClient(t)
	require.NoError(t, client.subscribe(context.Background(), "sess-1"))
	waitFor(t, func() bool { return len(hub.subscribeFrames()) == 1 }, "the subscription arrives")
	for _, frame := range []string{
		`not json`,
		`{"kind":"mystery","envelope":{}}`,
		`{"kind":"reply","envelope":{"requestId":"leapmux_999","ok":true,"payload":{}}}`,
		`{"kind":"reply","envelope":"not a reply"}`,
		`{"kind":"event","envelope":{"payload":{}}}`,
	} {
		require.NoError(t, hub.sendRaw([]byte(frame)), frame)
	}
	hub.emit("sess-1", "assistant.delta", map[string]any{"text": "a"})
	waitFor(t, func() bool { return recorder.count() == 1 }, "the event after the frames arrives")
	assert.Equal(t, []string{"assistant.delta"}, recorder.names())
	assert.Len(t, hub.commandsNamed(commandClientRegister), 1, "the client keeps its connection")
}

// A daemon that refuses the registration cannot run the agent, so the start
// fails and the client closes.
func TestHubClientStartFailsWhenTheHubRefusesTheRegistration(t *testing.T) {
	t.Parallel()
	hub, server := newFakeHubServer(t)
	hub.handle(commandClientRegister, func(fakeCommand) fakeReply {
		return fakeReply{Code: "protocol_error", Message: "unsupported client"}
	})
	record := fakeRecord(server.URL)
	endpoint, path, err := hubEndpoint(record)
	require.NoError(t, err)
	client := newHubClient(endpoint, path, record.AuthToken, "c", "a", map[string]any{"clientId": "c"}, func(hubEvent) {}, quartz.NewReal())
	err = client.start(context.Background(), 30*time.Second)
	var refused *HubCommandError
	require.ErrorAs(t, err, &refused)
	assert.Contains(t, err.Error(), "register with the Cline hub")
	assert.Equal(t, "protocol_error", refused.Code)
	client.wait()
	_, err = client.command(context.Background(), "session.get", "", nil)
	assert.ErrorIs(t, err, errHubClosed, "the client that failed its start is closed")
}

func TestHubClientStartRefusesAClosedClient(t *testing.T) {
	t.Parallel()
	hub, server := newFakeHubServer(t)
	record := fakeRecord(server.URL)
	endpoint, path, err := hubEndpoint(record)
	require.NoError(t, err)
	client := newHubClient(endpoint, path, record.AuthToken, "c", "a", map[string]any{}, func(hubEvent) {}, quartz.NewReal())
	client.close()
	require.ErrorIs(t, client.start(context.Background(), 30*time.Second), errHubClosed)
	assert.Empty(t, hub.commandsNamed(commandClientRegister), "a closed client opens no connection")
}

// A close fails each command that waits for its reply, so no caller waits for a
// reply that cannot arrive.
func TestHubClientCloseFailsACommandThatWaits(t *testing.T) {
	t.Parallel()
	client, hub, _ := newTestHubClient(t)
	hub.handle("session.get", func(fakeCommand) fakeReply { return fakeReply{Hold: true} })
	done := make(chan error, 1)
	go func() {
		_, err := client.command(context.Background(), "session.get", "", nil)
		done <- err
	}()
	_, ok := hub.waitCommand("session.get")
	require.True(t, ok)
	client.close()
	ctx := testutil.DeadlineContext(t)
	select {
	case err := <-done:
		assert.ErrorIs(t, err, errHubConnectionLost)
	case <-ctx.Done():
		t.Fatal("the command waited on a closed client")
	}
}

// A payload that does not encode never leaves, and it leaves no reply channel
// behind.
func TestHubClientRefusesAPayloadThatDoesNotEncode(t *testing.T) {
	t.Parallel()
	client, hub, _ := newTestHubClient(t)
	_, err := client.command(context.Background(), "probe", "", map[string]any{"value": make(chan int)})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "encode the Cline hub command probe")
	client.mu.Lock()
	pending := len(client.pending)
	client.mu.Unlock()
	assert.Zero(t, pending)
	assert.Empty(t, hub.commandsNamed("probe"))
}
