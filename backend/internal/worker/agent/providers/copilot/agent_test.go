package copilot

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newCopilotOffReaderAgent builds an agent whose connection is present but stopped
// nowhere. offReader reads sessionMu and the stopped flag, and both resolve here.
func newCopilotOffReaderAgent() *Agent {
	return &Agent{
		copilotConnection: &copilotConnection{JSONRPCProcess: providerkit.JSONRPCProcess{
			Process: providerkit.NewProcessFrom(providerkit.ProcessConfig{Ctx: context.Background(), Stdin: agenttest.FailingStdin{}}),
		}},
		sink: agent.NewProviderServices(&agenttest.Sink{}), sessionID: "session",
	}
}

// A burst of change events must not start a request for each one. The runtime states
// that an axis moved and not what it became, so every request in the burst reads the
// same answer, and the order they finish in decides which answer stands.
func TestCopilotOffReaderKeepsOneRunOfAKeyInFlight(t *testing.T) {
	a := newCopilotOffReaderAgent()
	started := make(chan struct{})
	release := make(chan struct{})
	var runs atomic.Int64
	work := func() {
		if runs.Add(1) == 1 {
			close(started)
			<-release
		}
	}

	a.offReader(copilotReadSettings, work)
	<-started
	// Three more events arrive while the first read is in flight.
	for range 3 {
		a.offReader(copilotReadSettings, work)
	}
	assert.Equal(t, int64(1), runs.Load(), "a request that arrives mid-read starts no second read")
	close(release)

	// One more run follows, because a change that arrived mid-read can carry a state the
	// read already passed. Three requests still buy exactly one of them.
	require.Eventually(t, func() bool { return runs.Load() == 2 }, offReaderWait, time.Millisecond)
	// The runner deletes the entry after the work returns, so the count can reach
	// its end a moment before the entry goes.
	assert.Eventually(t, a.backgroundWork.Idle, offReaderWait, time.Millisecond, "a finished read leaves no entry behind")
}

// offReaderWait limits each wait for a background read. A wait that succeeds
// returns at once, so the limit is generous for a loaded machine.
const offReaderWait = 30 * time.Second

// A read that nobody interrupted runs exactly once.
func TestCopilotOffReaderRunsOnceForOneRequest(t *testing.T) {
	a := newCopilotOffReaderAgent()
	var runs atomic.Int64
	a.offReader(copilotReadGoal, func() { runs.Add(1) })
	require.Eventually(t, func() bool { return runs.Load() == 1 }, offReaderWait, time.Millisecond)
	assert.Eventually(t, a.backgroundWork.Idle, offReaderWait, time.Millisecond, "a finished read leaves no entry behind")
}

// The keys stand apart: an objective read and a settings read never wait for each other.
func TestCopilotOffReaderSeparatesItsKeys(t *testing.T) {
	a := newCopilotOffReaderAgent()
	goalRan := make(chan struct{})
	settingsRan := make(chan struct{})
	block := make(chan struct{})
	defer close(block)

	a.offReader(copilotReadGoal, func() {
		close(goalRan)
		<-block
	})
	<-goalRan
	a.offReader(copilotReadSettings, func() { close(settingsRan) })
	select {
	case <-settingsRan:
	case <-time.After(offReaderWait):
		t.Fatal("a settings read waited for the objective read")
	}
}

// A stopped process answers nothing, so the work never starts.
func TestCopilotOffReaderRefusesAStoppedProcess(t *testing.T) {
	a := newCopilotOffReaderAgent()
	a.SetStoppedForTest(true)
	var runs atomic.Int64
	a.offReader(copilotReadGoal, func() { runs.Add(1) })
	require.Eventually(t, a.backgroundWork.Idle, offReaderWait, time.Millisecond)
	assert.Zero(t, runs.Load())
}

// An inbound REQUEST needs an answer. The session configuration opts into
// requestPermission and requestElicitation, so the runtime can ask; without a reply it
// waits for its own timeout. The frame still reaches the transcript.
func TestCopilotAnswersAnUnsupportedRequest(t *testing.T) {
	sink := &agenttest.Sink{}
	written := &agenttest.Stdin{}
	a := &Agent{
		copilotConnection: &copilotConnection{JSONRPCProcess: providerkit.JSONRPCProcess{
			Process:      providerkit.NewProcessFrom(providerkit.ProcessConfig{Ctx: context.Background(), Stdin: agenttest.NopStdin(written)}),
			FrameMessage: frameCopilotJSON,
		}},
		sink: agent.NewProviderServices(sink), sessionID: "session",
	}

	raw := []byte(`{"jsonrpc":"2.0","id":9007199254740993,"method":"session/requestSomethingNew","params":{}}`)
	a.HandleOutput(raw)

	// The refusal is written on its OWN goroutine, so that a reply to a runtime
	// which is not draining its stdin cannot stall the read loop that must keep
	// draining the runtime's stdout -- and, here, cannot hold outputMu across the
	// write and leave Stop unable to close stdin and end the stall.
	var answer string
	require.Eventually(t, func() bool {
		answer = written.String()
		return strings.Contains(answer, `"code":-32601`)
	}, 2*time.Second, 5*time.Millisecond, "the unsupported request is answered")
	require.Contains(t, answer, `"code":-32601`)
	require.Contains(t, answer, "Method not supported: session/requestSomethingNew")
	assert.Contains(t, answer, `"id":9007199254740993`,
		"the identifier returns exactly as it arrived, above the range a float keeps")
	require.Len(t, sink.Messages(), 1, "the frame still reaches the transcript")
	assert.Equal(t, raw, sink.Messages()[0].Content)
}

// A NOTIFICATION carries no id and needs no answer. Writing one would put a response
// with a null identifier on the wire, which the runtime cannot route.
func TestCopilotAnswersNoNotification(t *testing.T) {
	written := &agenttest.Stdin{}
	a := &Agent{
		copilotConnection: &copilotConnection{JSONRPCProcess: providerkit.JSONRPCProcess{
			Process:      providerkit.NewProcessFrom(providerkit.ProcessConfig{Ctx: context.Background(), Stdin: agenttest.NopStdin(written)}),
			FrameMessage: frameCopilotJSON,
		}},
		sink: agent.NewProviderServices(&agenttest.Sink{}), sessionID: "session",
	}

	for _, raw := range []string{
		`{"jsonrpc":"2.0","method":"session/somethingHappened","params":{}}`,
		`{"jsonrpc":"2.0","id":null,"method":"session/somethingHappened","params":{}}`,
		`{"jsonrpc":"2.0","method":"session.lifecycle","params":{}}`,
	} {
		a.HandleOutput([]byte(raw))
	}
	// Never, not a bare Empty. RefuseUnsupportedRequest answers on its own goroutine,
	// so NOTHING is written on the calling goroutine for any input at all -- an Empty
	// assertion straight after a synchronous call is therefore true whether or not the
	// guard holds, and deleting the guard left this test green.
	require.Never(t, func() bool { return written.String() != "" },
		200*time.Millisecond, 5*time.Millisecond, "a notification draws no reply")

	// And the machinery is live rather than merely idle: one genuine unsupported
	// REQUEST is answered, exactly once, with no null identifier beside it.
	a.HandleOutput([]byte(`{"jsonrpc":"2.0","id":1,"method":"session/requestSomethingNew","params":{}}`))
	var answer string
	require.Eventually(t, func() bool {
		answer = written.String()
		return strings.Contains(answer, `"code":-32601`)
	}, 2*time.Second, 5*time.Millisecond, "the unsupported request is answered")
	assert.Equal(t, 1, strings.Count(answer, `"code":-32601`), "only the request draws a reply")
	assert.NotContains(t, answer, `"id":null`, "a reply with a null id cannot be routed")
}

// A response the correlator already declined must not draw an error reply: it carries
// no method, so it is nobody's request.
func TestCopilotAnswersNoOrphanResponse(t *testing.T) {
	written := &agenttest.Stdin{}
	a := &Agent{
		copilotConnection: &copilotConnection{JSONRPCProcess: providerkit.JSONRPCProcess{
			Process:      providerkit.NewProcessFrom(providerkit.ProcessConfig{Ctx: context.Background(), Stdin: agenttest.NopStdin(written)}),
			FrameMessage: frameCopilotJSON,
		}},
		sink: agent.NewProviderServices(&agenttest.Sink{}), sessionID: "session",
	}
	a.handleNativeOutput(&providerkit.ParsedLine{
		Raw: []byte(`{"jsonrpc":"2.0","id":7,"result":{}}`),
		ID:  json.RawMessage(`7`),
	})
	// Never, not a bare Empty: the reply travels on its own goroutine, so an Empty
	// assertion straight after this call cannot tell a held guard from an unscheduled
	// goroutine. See TestCopilotAnswersNoNotification.
	require.Never(t, func() bool { return written.String() != "" },
		200*time.Millisecond, 5*time.Millisecond, "a response is nobody's request and draws no reply")
}

type copilotSteerRequest struct {
	ID     int64           `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

type copilotSteerScript struct {
	removed        bool
	earlyEcho      bool
	echoOnWithdraw bool
	sendError      bool
	sendResult     json.RawMessage
	withdrawError  bool
	withdrawResult json.RawMessage
}

func newCopilotSteeringAgent(t *testing.T, script copilotSteerScript) (*Agent, *agenttest.Sink, *[]copilotSteerRequest) {
	t.Helper()
	a, sink := newNativeCopilotForEvents(t)
	a.active = true
	done := make(chan struct{})
	var calls []copilotSteerRequest
	writer := copilotRequestWriter(func(raw []byte) (int, error) {
		var request copilotSteerRequest
		if err := json.Unmarshal(raw, &request); err != nil {
			return 0, err
		}
		calls = append(calls, request)
		result := json.RawMessage(`{}`)
		switch request.Method {
		case "session.send":
			if script.sendError {
				a.Deliver(request.ID, []byte(`{"error":{"code":-32603,"message":"send refused"}}`))
				return len(raw), nil
			}
			if script.earlyEcho {
				a.HandleOutput(nativeCopilotEvent(t, "", "user.message", map[string]any{"content": providerkit.StringField(request.Params, "prompt")}))
			}
			result = json.RawMessage(`{"messageId":"native-steer"}`)
			if script.sendResult != nil {
				result = script.sendResult
			}
		case "session.queue.withdrawMessage":
			if script.echoOnWithdraw {
				a.HandleOutput(nativeCopilotEvent(t, "", "user.message", map[string]any{"content": providerkit.StringField(request.Params, "expectedPrompt")}))
			}
			if script.withdrawError {
				a.Deliver(request.ID, []byte(`{"error":{"code":-32603,"message":"withdrawal refused"}}`))
				return len(raw), nil
			}
			encoded, err := json.Marshal(map[string]bool{"removed": script.removed})
			if err != nil {
				return 0, err
			}
			result = encoded
			if script.withdrawResult != nil {
				result = script.withdrawResult
			}
		}
		a.Deliver(request.ID, append(append([]byte(`{"result":`), result...), '}'))
		return len(raw), nil
	})
	a.Process = providerkit.NewProcessFrom(providerkit.ProcessConfig{Ctx: t.Context(), Stdin: agenttest.NopStdin(writer), ProcessDone: done})
	t.Cleanup(func() { close(done); a.Process.Stop() })
	return a, sink, &calls
}

func TestSteerEchoAtUserMessage(t *testing.T) {
	for _, early := range []bool{false, true} {
		t.Run(fmt.Sprint(early), func(t *testing.T) {
			a, sink, _ := newCopilotSteeringAgent(t, copilotSteerScript{earlyEcho: early})
			require.NoError(t, a.SteerInput("change direction", nil))
			if !early {
				assert.Empty(t, sink.SteerEchoReports())
			}
			a.HandleOutput(nativeCopilotEvent(t, "", "user.message", map[string]any{"content": "change direction"}))
			assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeEchoed, Count: 1}}, sink.SteerEchoReports())
			assert.Empty(t, sink.Messages(), "the queue owns the user transcript row")
		})
	}
}

func TestInterruptWithdrawsAPendingSteerAndReportsTheDrop(t *testing.T) {
	a, sink, calls := newCopilotSteeringAgent(t, copilotSteerScript{removed: true})
	require.NoError(t, a.SteerInput("change direction", nil))
	require.NoError(t, a.Interrupt(agent.StopContext{}))
	require.Len(t, *calls, 3)
	assert.Equal(t, "session.queue.withdrawMessage", (*calls)[1].Method)
	assert.JSONEq(t, `{"sessionId":"session-1","messageId":"native-steer","expectedPrompt":"change direction"}`, string((*calls)[1].Params))
	assert.Equal(t, "session.abort", (*calls)[2].Method)
	assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeDropped, Count: 1}}, sink.SteerEchoReports())
}

func TestInterruptKeepsASteerThatTheRuntimeAlreadyClaimed(t *testing.T) {
	a, sink, calls := newCopilotSteeringAgent(t, copilotSteerScript{})
	require.NoError(t, a.SteerInput("change direction", nil))
	require.NoError(t, a.Interrupt(agent.StopContext{}))
	require.Len(t, *calls, 3)
	assert.Empty(t, sink.SteerEchoReports())
	a.HandleOutput(nativeCopilotEvent(t, "", "user.message", map[string]any{"content": "change direction"}))
	assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeEchoed, Count: 1}}, sink.SteerEchoReports())
}

func TestSteerDeclaresRowAtEchoAndSurvival(t *testing.T) {
	t.Parallel()
	a := &Agent{}
	echo, ok := any(a).(agent.SteerEchoReporter)
	require.True(t, ok)
	assert.True(t, echo.SteerRowAtEcho())
	survivor, ok := any(a).(agent.SteerSurvivor)
	require.True(t, ok)
	assert.True(t, survivor.SteerSurvivesTurnEnd())
}

func TestSteerContextReplacementReportsUnechoedDrops(t *testing.T) {
	a, sink, _ := newCopilotSteeringAgent(t, copilotSteerScript{})
	require.NoError(t, a.SteerInput("change direction", nil))
	a.sessionMu.Lock()
	a.forgetNativeSessionState("replacement")
	a.sessionMu.Unlock()
	assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeDropped, Count: 1}}, sink.SteerEchoReports())
	assert.Empty(t, a.steers.Pending())
}

func TestSteerIgnoresAMalformedOrChildEcho(t *testing.T) {
	a, sink, _ := newCopilotSteeringAgent(t, copilotSteerScript{})
	require.NoError(t, a.SteerInput("", nil))
	for _, data := range []map[string]any{{}, {"content": nil}, {"content": 7}} {
		a.HandleOutput(nativeCopilotEvent(t, "", "user.message", data))
	}
	a.HandleOutput(nativeCopilotEvent(t, "child", "user.message", map[string]any{"content": ""}))
	assert.Empty(t, sink.SteerEchoReports())
	a.HandleOutput(nativeCopilotEvent(t, "", "user.message", map[string]any{"content": ""}))
	assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeEchoed, Count: 1}}, sink.SteerEchoReports())
}

func TestSteerFailedRequestForgetsItsEcho(t *testing.T) {
	for _, script := range []copilotSteerScript{{sendError: true}, {sendResult: json.RawMessage(`{}`)}} {
		a, sink, _ := newCopilotSteeringAgent(t, script)
		require.Error(t, a.SteerInput("change direction", nil))
		a.HandleOutput(nativeCopilotEvent(t, "", "user.message", map[string]any{"content": "change direction"}))
		assert.Empty(t, sink.SteerEchoReports())
		assert.Empty(t, a.steers.Pending())
	}
}

func TestInterruptAbortsEvenWhenSteerWithdrawalFails(t *testing.T) {
	for _, script := range []copilotSteerScript{{withdrawError: true}, {withdrawResult: json.RawMessage(`{}`)}} {
		a, sink, calls := newCopilotSteeringAgent(t, script)
		require.NoError(t, a.SteerInput("change direction", nil))
		require.NoError(t, a.Interrupt(agent.StopContext{}))
		assert.Equal(t, "session.abort", (*calls)[len(*calls)-1].Method)
		assert.Empty(t, sink.SteerEchoReports())
		a.HandleOutput(nativeCopilotEvent(t, "", "user.message", map[string]any{"content": "change direction"}))
		assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeEchoed, Count: 1}}, sink.SteerEchoReports())
	}
}

func TestInterruptDoesNotDropASteerThatEchoedDuringWithdrawal(t *testing.T) {
	a, sink, _ := newCopilotSteeringAgent(t, copilotSteerScript{removed: true, echoOnWithdraw: true})
	require.NoError(t, a.SteerInput("change direction", nil))
	require.NoError(t, a.Interrupt(agent.StopContext{}))
	assert.Equal(t, []agenttest.SteerEchoReport{{Outcome: agent.SteerOutcomeEchoed, Count: 1}}, sink.SteerEchoReports())
}
