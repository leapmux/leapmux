package cline

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/coder/quartz"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/agentdir/agentdirtest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// rig is one agent connected to a fake hub, with no daemon process behind it.
// The tests drive the hub's commands and events, which is everything the
// provider does after the daemon published its record; start_unix_test.go
// covers the launch.
type rig struct {
	agent *Agent
	hub   *fakeHub
	sink  *agenttest.ControlSink
}

// rigConfig states what a rig starts with.
type rigConfig struct {
	opts      agent.Options
	selection providerSelection
	// noSession connects the agent and opens no session.
	noSession bool
	// sink replaces the recording sink.
	sink *agenttest.ControlSink
	// services wraps the recording sink, so a test can make one service fail.
	// nil hands the agent the recording sink itself.
	services func(*agenttest.ControlSink) agent.ServiceFacets
}

// testProvider and testModel are the provider and the model of every rig's
// Cline settings, unless a test states others.
const (
	testProvider = "openai-native"
	testModel    = "gpt-5.6"
)

// exitOnClose is the stdin of a fake daemon process: closing it ends the
// process, as the real daemon's wrapper ends when its group is stopped.
type exitOnClose struct {
	once sync.Once
	done chan struct{}
}

func (e *exitOnClose) Write(p []byte) (int, error) { return len(p), nil }

func (e *exitOnClose) Close() error {
	e.once.Do(func() { close(e.done) })
	return nil
}

// newRig starts a fake hub, connects an agent to it, and opens a session.
func newRig(t *testing.T, configure ...func(*rigConfig)) *rig {
	t.Helper()
	hub, server := newFakeHubServer(t)
	cfg := rigConfig{
		opts: agent.Options{
			AgentID:    "cline-agent",
			WorkingDir: t.TempDir(),
			APITimeout: 30 * time.Second,
		},
		selection: providerSelection{Provider: testProvider, Model: testModel},
	}
	for _, apply := range configure {
		apply(&cfg)
	}
	sink := cfg.sink
	if sink == nil {
		sink = &agenttest.ControlSink{}
	}
	var services agent.ServiceFacets = sink
	if cfg.services != nil {
		services = cfg.services(sink)
	}
	a := newTestAgent(t, server.URL, cfg.opts, cfg.selection, services)
	r := &rig{agent: a, hub: hub, sink: sink}
	if cfg.noSession {
		return r
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sessionID, err := a.openSession(ctx, a.settings, cfg.opts.ResumeSessionID)
	require.NoError(t, err)
	r.waitSubscribed(t, sessionID)
	return r
}

// waitSubscribed waits until the hub holds the subscription of sessionID. The
// subscribe frame has no reply, so an event the test emits at once could reach
// the hub before it.
func (r *rig) waitSubscribed(t *testing.T, sessionID string) {
	t.Helper()
	waitFor(t, func() bool {
		r.hub.mu.Lock()
		defer r.hub.mu.Unlock()
		return r.hub.subscriptions[sessionID]
	}, "the hub holds the session's subscription")
}

// newTestAgent connects an agent with a fake process to the hub at url.
func newTestAgent(t *testing.T, url string, opts agent.Options, selection providerSelection, services agent.ServiceFacets) *Agent {
	t.Helper()
	record := fakeRecord(url)
	endpoint, path, err := hubEndpoint(record)
	require.NoError(t, err)
	stdin := &exitOnClose{done: make(chan struct{})}
	procCtx, procCancel := context.WithCancel(context.Background())
	lifeCtx, cancel := context.WithCancel(context.Background())
	a := &Agent{
		Process: func() *providerkit.Process {
			p := providerkit.NewProcessFrom(providerkit.ProcessConfig{
				AgentID: opts.AgentID, ProviderName: "cline", Ctx: procCtx, Cancel: procCancel,
				Stdin: stdin, APITimeout: opts.APITimeout, ProcessDone: stdin.done,
			})
			// No stderr pipe feeds the fake process, so nothing drains one.
			p.SkipStderr()
			return &p
		}(),
		sink:          agent.NewModelProgressResetSink(agent.NewProviderServices(services)),
		opts:          opts,
		dir:           agentdirtest.NewDir(t, agentDirSpec()),
		workspaceRoot: opts.WorkingDir,
		record:        record,
		endpoint:      endpoint,
		clientID:      "leapmux-" + opts.AgentID,
		selection:     selection,
		clock:         quartz.NewReal(),
		sendWait:      defaultSendWait,
		ctx:           lifeCtx,
		cancel:        cancel,
		stopped:       make(chan struct{}),
		out:           newOutputState(),
	}
	a.settings = a.launchSettings(opts)
	a.hub = newHubClient(endpoint, path, record.AuthToken, a.clientID, opts.AgentID, a.registration(), a.handleEvent, a.clock)
	a.subscribe = a.hub.subscribe
	a.hub.backoff = 10 * time.Millisecond
	require.NoError(t, a.hub.start(lifeCtx, 30*time.Second))
	t.Cleanup(func() {
		a.Stop()
		_ = a.Wait()
		procCancel()
	})
	return a
}

// noChildSink is a recording sink that cannot create a child transcript, as a
// database that refuses the row does.
type noChildSink struct {
	*agenttest.ControlSink
}

func (noChildSink) EnsureChildAgent(string, string, string) (string, error) {
	return "", errors.New("the database refuses the child")
}

// withNoChild makes every child transcript of the rig fail.
func withNoChild(c *rigConfig) {
	c.services = func(sink *agenttest.ControlSink) agent.ServiceFacets { return noChildSink{sink} }
}

// sessionID returns the agent's current session.
func (r *rig) sessionID() string { return r.agent.currentSession() }

// exit ends the fake daemon process as a crash would: nothing asked for it.
func (r *rig) exit() {
	_ = r.agent.Process.StdinForTest().Close()
}

// emit sends one event of the current session down the hub's socket.
func (r *rig) emit(event string, payload any) {
	r.hub.emit(r.sessionID(), event, payload)
}

// feed dispatches one event of the current session through HandleOutput, as
// the dispatcher would.
func (r *rig) feed(t *testing.T, event string, payload any) {
	t.Helper()
	r.agent.HandleOutput(eventEnvelope(t, r.sessionID(), event, payload))
}

// eventEnvelope renders one event envelope as the daemon sends it.
func eventEnvelope(t *testing.T, sessionID, event string, payload any) []byte {
	t.Helper()
	data, err := json.Marshal(map[string]any{
		"version":   hubProtocolVersion,
		"event":     event,
		"eventId":   "hevt_test",
		"sessionId": sessionID,
		"timestamp": time.Now().UnixMilli(),
		"payload":   payload,
	})
	require.NoError(t, err)
	return data
}

// startTurn sends a message and waits until the agent holds the turn. It
// returns the request id of the send.
func (r *rig) startTurn(t *testing.T, text string) string {
	t.Helper()
	require.NoError(t, r.agent.SendInput(text, nil))
	command, ok := r.hub.waitCommand(commandSessionSendInput)
	require.True(t, ok, "the send reaches the hub")
	return command.RequestID
}

// endRun ends the running turn with a run.completed event and the send's
// reply, as the daemon ends a turn.
func (r *rig) endRun(t *testing.T, requestID, reason string) {
	t.Helper()
	event := contracts.ClineEventRunCompleted
	switch reason {
	case contracts.ClineRunReasonAborted:
		event = contracts.ClineEventRunAborted
	case contracts.ClineRunReasonError, contracts.ClineRunReasonMistakeLimit:
		event = contracts.ClineEventRunFailed
	}
	r.emit(event, map[string]any{"reason": reason, "result": map[string]any{"text": "done", "messages": []any{map[string]any{"role": "user"}}}})
	r.hub.reply(requestID, fakeReply{Payload: map[string]any{"result": map[string]any{"text": "done"}}})
	waitFor(t, func() bool { return !r.turnActive() }, "the run's end ends the turn")
}

// turnActive reports whether the agent holds a turn.
func (r *rig) turnActive() bool {
	r.agent.Mu.Lock()
	defer r.agent.Mu.Unlock()
	return r.agent.turn.active
}

// waitFor polls cond until it holds. The deadline is generous and never
// asserted: a condition that never holds fails the test, however long that
// took.
func waitFor(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	require.Eventually(t, cond, 30*time.Second, 2*time.Millisecond, msg)
}

// options builds an option map.
func options(pairs ...string) optionmap.Map {
	out := optionmap.Map{}
	for i := 0; i+1 < len(pairs); i += 2 {
		out[pairs[i]] = pairs[i+1]
	}
	return out
}

// decode decodes one JSON document into a map.
func decode(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var out map[string]any
	require.NoError(t, json.Unmarshal(data, &out))
	return out
}

// rowEvents returns the `event` of each persisted AGENT row of a sink, in
// order.
func rowEvents(t *testing.T, sink *agenttest.Sink) []string {
	t.Helper()
	var events []string
	for _, message := range sink.Messages() {
		var envelope struct {
			Event string `json:"event"`
		}
		if json.Unmarshal(message.Content, &envelope) == nil && envelope.Event != "" {
			events = append(events, envelope.Event)
		}
	}
	return events
}

// payloadOf returns the payload of one persisted row.
func payloadOf(t *testing.T, message agenttest.Message) map[string]any {
	t.Helper()
	envelope := decode(t, message.Content)
	payload, _ := envelope["payload"].(map[string]any)
	return payload
}
