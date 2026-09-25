package ohmypi

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/coder/quartz"
	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/require"
)

// recordedCommand is one command the agent wrote to omp's stdin.
type recordedCommand struct {
	ID      string
	Type    string
	Payload map[string]any
	Raw     []byte
}

// rigReply is how the fake omp answers one command. A nil reply answers success
// with no data; Skip answers nothing at all.
type rigReply struct {
	Data    json.RawMessage
	Error   string
	Skip    bool
	Command string
	// Before holds event frames that omp sends before the response, as it sends
	// thinking_level_changed before it answers set_thinking_level.
	Before []string
}

// rig drives an Agent against a fake omp: it records every command the agent
// writes, answers each one through a responder, and feeds frames to the agent's
// read path in ONE order, as the real read loop does.
type rig struct {
	t      *testing.T
	agent  *Agent
	sink   *agenttest.ControlSink
	stdinR *os.File
	stdinW *os.File
	// clock is the agent's clock. No timer of the agent fires unless a test
	// advances it.
	clock *quartz.Mock

	mu        sync.Mutex
	commands  []recordedCommand
	responder func(recordedCommand) *rigReply

	// lines carries every frame to the one goroutine that dispatches them, so an
	// emitted event and a response keep the order they were sent in.
	lines chan rigLine
	done  chan struct{}
}

type rigLine struct {
	raw       []byte
	processed chan struct{}
}

// newRig builds an Agent with a fake omp behind it. The responder answers every
// command with success and no data until a test sets its own.
func newRig(t *testing.T) *rig {
	t.Helper()
	stdinR, stdinW, err := os.Pipe()
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	sink := &agenttest.ControlSink{}
	clock := testutil.NewQuartzMock(t)
	a := &Agent{
		Process: providerkit.NewProcessFrom(providerkit.ProcessConfig{
			AgentID:      "omp-test",
			ProviderName: "omp",
			Stdin:        stdinW,
			Ctx:          ctx,
			Cancel:       cancel,
			ProcessDone:  make(chan struct{}),
			StderrDone:   make(chan struct{}),
			APITimeout:   2 * time.Second,
			Clock:        clock,
		}),
		sink:          agent.NewProviderServices(sink),
		ready:         make(chan readyFrame, 1),
		model:         "mock/mock-model",
		thinkingLevel: "high",
		approvalMode:  "write",
		sessionID:     "01a0cf77-9ae4-72d8-9a42-665c431d3beb",
		sessionFile:   "/sessions/2026-09-23T18-11-57-284Z_01a0cf77-9ae4-72d8-9a42-665c431d3beb.jsonl",
	}
	a.SkipStderr()
	r := &rig{
		t: t, agent: a, sink: sink, stdinR: stdinR, stdinW: stdinW, clock: clock,
		lines: make(chan rigLine, 256), done: make(chan struct{}),
	}
	go r.dispatch()
	go r.readCommands()
	t.Cleanup(func() {
		cancel()
		_ = stdinW.Close()
		_ = stdinR.Close()
		close(r.done)
	})
	return r
}

// dispatch hands every frame to the agent's read path, one at a time.
func (r *rig) dispatch() {
	for {
		select {
		case line := <-r.lines:
			r.agent.HandleOutput(line.raw)
			if line.processed != nil {
				close(line.processed)
			}
		case <-r.done:
			return
		}
	}
}

// readCommands records each command the agent writes and answers it. It closes
// the fake process when stdin closes, as omp exits on EOF.
func (r *rig) readCommands() {
	scanner := bufio.NewScanner(r.stdinR)
	scanner.Buffer(make([]byte, 0, 64*1024), 4<<20)
	for scanner.Scan() {
		raw := append([]byte(nil), scanner.Bytes()...)
		var payload map[string]any
		if json.Unmarshal(raw, &payload) != nil {
			continue
		}
		id, _ := payload["id"].(string)
		kind, _ := payload["type"].(string)
		command := recordedCommand{ID: id, Type: kind, Payload: payload, Raw: raw}
		r.mu.Lock()
		r.commands = append(r.commands, command)
		responder := r.responder
		r.mu.Unlock()
		if id == "" || !answersWithResponse(kind) {
			// A dialog answer or a host-call refusal: omp answers nothing.
			continue
		}
		var reply *rigReply
		if responder != nil {
			reply = responder(command)
		}
		if reply != nil {
			for _, frame := range reply.Before {
				select {
				case r.lines <- rigLine{raw: []byte(frame)}:
				case <-r.done:
					return
				}
			}
			if reply.Skip {
				continue
			}
		}
		response := map[string]any{"type": "response", "id": id, "command": kind, "success": true}
		if reply != nil {
			if reply.Command != "" {
				response["command"] = reply.Command
			}
			if reply.Error != "" {
				response["success"] = false
				response["error"] = reply.Error
			}
			if len(reply.Data) > 0 {
				response["data"] = reply.Data
			}
		}
		encoded, _ := json.Marshal(response)
		select {
		case r.lines <- rigLine{raw: encoded}:
		case <-r.done:
			return
		}
	}
	r.agent.SimulateExitForTest()
}

// answersWithResponse reports whether omp answers a frame the agent writes with a
// `response` frame. A dialog answer and a host-call result carry the id of the
// frame they answer, and omp answers neither.
func answersWithResponse(kind string) bool {
	switch kind {
	case contracts.OhMyPiEventExtensionUIResponse, CommandHostToolResult, CommandHostURIResult:
		return false
	default:
		return true
	}
}

// respond sets how the fake omp answers each command.
func (r *rig) respond(responder func(recordedCommand) *rigReply) {
	r.mu.Lock()
	r.responder = responder
	r.mu.Unlock()
}

// emit feeds frames to the agent and waits until it processed each one.
func (r *rig) emit(frames ...string) {
	r.t.Helper()
	for _, frame := range frames {
		processed := make(chan struct{})
		r.lines <- rigLine{raw: []byte(frame), processed: processed}
		select {
		case <-processed:
		case <-time.After(10 * time.Second):
			r.t.Fatalf("the agent did not process the frame %s", frame)
		}
	}
}

// commandsOfType returns the commands of one type the agent wrote so far.
func (r *rig) commandsOfType(kind string) []recordedCommand {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []recordedCommand
	for _, command := range r.commands {
		if command.Type == kind {
			out = append(out, command)
		}
	}
	return out
}

// waitForCommand waits until the agent wrote `n` commands of one type.
func (r *rig) waitForCommand(kind string, n int) []recordedCommand {
	r.t.Helper()
	var got []recordedCommand
	require.Eventually(r.t, func() bool {
		got = r.commandsOfType(kind)
		return len(got) >= n
	}, 10*time.Second, time.Millisecond, "the agent wrote fewer than %d %q commands", n, kind)
	return got
}

// awaitStatsRead waits until no session-stats read runs. A run start and a run
// end start one, and its wait is a timer of the clock, so a test that moves the
// clock past that wait takes this step first.
func (r *rig) awaitStatsRead() {
	r.t.Helper()
	waitFor(r.t, func() bool {
		r.agent.Mu.Lock()
		defer r.agent.Mu.Unlock()
		return !r.agent.usage.statsRunning
	})
}

// waitFor waits until condition holds. The deadline is generous: nothing here
// sizes a window, it only bounds a wait for an event that is certain to happen.
func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	require.Eventually(t, condition, 10*time.Second, time.Millisecond)
}

// mustJSON encodes a value for a frame literal.
func mustJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	require.NoError(t, err)
	return string(encoded)
}

// persistedTypes lists the `type` of each message the sink persisted, in order.
func persistedTypes(messages []agenttest.Message) []string {
	out := make([]string, 0, len(messages))
	for _, message := range messages {
		var head struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(message.Content, &head)
		out = append(out, head.Type)
	}
	return out
}
