//go:build unix

package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// claudeInterruptRig wires an Agent to an in-memory pipe
// pair so the test can capture the interrupt control_request and
// hand the matching control_response back through stdout. Claude's
// sendControlAndWait blocks until the agent responds; without the
// echo this test would deadlock.
//
// The rig also runs the agent's own output handling for every line the pending-
// control handler does not consume, exactly as readOutputLoop does. That is what
// lets a case state the ORDER the CLI speaks in: `beforeAck` writes lines that
// reach the reader before the acknowledgement does.
type claudeInterruptRig struct {
	agent    *Agent
	sink     *outputTestSink
	captured func() []recordedClaudeControl
}

func newClaudeInterruptRig(t *testing.T) *claudeInterruptRig {
	t.Helper()
	return newClaudeInterruptRigWithPreamble(t, nil)
}

// newClaudeInterruptRigWithPreamble is newClaudeInterruptRig whose responder
// writes `beforeAck` to stdout before it answers the control request it captured.
func newClaudeInterruptRigWithPreamble(t *testing.T, beforeAck []string) *claudeInterruptRig {
	t.Helper()
	return newClaudeInterruptRigResponding(t, beforeAck, claudeControlSuccessResponse)
}

// newClaudeInterruptRigWithControlError is newClaudeInterruptRig whose fake CLI
// answers every control request with the failure shape Claude Code emits,
// carrying errMsg.
func newClaudeInterruptRigWithControlError(t *testing.T, errMsg string) *claudeInterruptRig {
	t.Helper()
	return newClaudeInterruptRigResponding(t, nil, func(rec recordedClaudeControl) map[string]any {
		return map[string]any{
			"type": "control_response",
			"response": map[string]any{
				"subtype":    "error",
				"request_id": rec.RequestID,
				"error":      errMsg,
			},
		}
	})
}

// claudeControlSuccessResponse builds the control_response shape that
// terminates the pending wait inside sendControlAndWait with a success.
func claudeControlSuccessResponse(rec recordedClaudeControl) map[string]any {
	return map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype":    "success",
			"request_id": rec.RequestID,
			"response":   map[string]any{},
		},
	}
}

// newClaudeInterruptRigResponding builds the rig the type above describes.
// respond constructs the control_response the fake CLI returns for each
// captured control request; beforeAck lines reach the reader first.
func newClaudeInterruptRigResponding(t *testing.T, beforeAck []string, respond func(recordedClaudeControl) map[string]any) *claudeInterruptRig {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	stdinReader, stdinWriter, err := os.Pipe()
	require.NoError(t, err)
	stdoutReader, stdoutWriter, err := os.Pipe()
	require.NoError(t, err)

	sink := &outputTestSink{}
	a := &Agent{
		Process: providerkit.NewProcessFrom(providerkit.ProcessConfig{
			AgentID:      "test-agent",
			ProviderName: "claude",
			Stdin:        stdinWriter,
			Ctx:          ctx,
			Cancel:       cancel,
			ProcessDone:  make(chan struct{}),
			StderrDone:   make(chan struct{}),
			APITimeout:   2 * time.Second,
		}),
		sink:           agent.NewProviderServices(sink),
		pendingControl: make(map[string]chan<- claudeCodeControlResult),
	}
	a.SkipStderr()

	var (
		mu       sync.Mutex
		captured []recordedClaudeControl
	)

	// Read agent stdin → echo a control_response back through stdout.
	go func() {
		scanner := bufio.NewScanner(stdinReader)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			var rec recordedClaudeControl
			if err := json.Unmarshal(scanner.Bytes(), &rec); err != nil {
				continue
			}
			if rec.Type != "control_request" {
				continue
			}
			mu.Lock()
			captured = append(captured, rec)
			mu.Unlock()

			// Whatever the CLI says before it answers. An abort that ends the
			// turn on its way to the acknowledgement is written here.
			for _, line := range beforeAck {
				if _, err := stdoutWriter.Write(append([]byte(line), '\n')); err != nil {
					return
				}
			}

			b, _ := json.Marshal(respond(rec))
			b = append(b, '\n')
			if _, err := stdoutWriter.Write(b); err != nil {
				return
			}
		}
	}()

	// Drive the agent's read loop from the fake stdout. Mirrors
	// piTestRig — we don't call Process.ReadOutput because it
	// ends with cmd.Wait() which would nil-deref without an
	// exec.Cmd.
	go func() {
		scanner := bufio.NewScanner(stdoutReader)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := providerkit.ParseLine(append([]byte(nil), scanner.Bytes()...))
			if a.handlePendingControlResponse(line) {
				continue
			}
			a.handleClaudeOutput(line.Raw, line.Type)
		}
	}()

	t.Cleanup(func() {
		cancel()
		_ = stdinWriter.Close()
		_ = stdoutWriter.Close()
		_ = stdinReader.Close()
		_ = stdoutReader.Close()
	})

	return &claudeInterruptRig{
		agent: a,
		sink:  sink,
		captured: func() []recordedClaudeControl {
			mu.Lock()
			defer mu.Unlock()
			out := make([]recordedClaudeControl, len(captured))
			copy(out, captured)
			return out
		},
	}
}

func TestClaudeCodeAgent_Interrupt_SendsControlRequest(t *testing.T) {
	t.Parallel()

	rig := newClaudeInterruptRig(t)

	require.NoError(t, rig.agent.Interrupt())

	captured := rig.captured()
	require.Len(t, captured, 1)
	rec := captured[0]
	assert.Equal(t, "control_request", rec.Type)
	assert.NotEmpty(t, rec.RequestID, "request_id must be populated for response correlation")

	var inner struct {
		Subtype string `json:"subtype"`
	}
	require.NoError(t, json.Unmarshal(rec.Request, &inner))
	assert.Equal(t, "interrupt", inner.Subtype,
		"Claude Code interrupt must use the {subtype:'interrupt'} control_request")
}

// The note that tells an interrupted turn from a failed one is taken BEFORE the
// request goes out, so the `result` of the aborted turn cannot overtake it.
//
// The reader goroutine hands the acknowledgement to the waiting Interrupt and
// reads the next line at once. Taking the note after that wait raced the
// takeInterruptRequest that spends it, and lost whenever the reader won -- the
// stopped turn then read as the failure its `error_during_execution` subtype
// claims, in the danger colour, for a stop the reader asked for.
//
// This case pins the losing order: the abort's `result` reaches the reader
// before the acknowledgement does.
func TestClaudeCodeAgent_Interrupt_TheAbortedTurnStatesTheStop(t *testing.T) {
	t.Parallel()

	rig := newClaudeInterruptRigWithPreamble(t, []string{
		`{"type":"result","subtype":"error_during_execution","is_error":true,"duration_ms":12000}`,
	})
	rig.agent.Mu.Lock()
	rig.agent.turnActive = true
	rig.agent.Mu.Unlock()

	require.NoError(t, rig.agent.Interrupt())

	require.Eventually(t, func() bool { return len(rig.sink.Messages()) > 0 }, time.Second, 5*time.Millisecond,
		"the aborted turn stored no result")
	messages := rig.sink.Messages()
	assert.Equal(t, agent.MessageCompletionInterrupted, messages[len(messages)-1].Completion,
		"the turn end states the stop the reader asked for, not the subtype's failure")
}

// The CLI can end the turn before it reads the stop. The `result` then states a
// finished turn (the probe in output_test.go shows this order with Claude Code 2.1.289),
// and the divider must not read "Turn interrupted" for it. This case sends the stop
// through Interrupt and hands the finished frame to the reader before the acknowledgement.
func TestClaudeCodeAgent_Interrupt_ATurnThatFinishedFirstIsNotInterrupted(t *testing.T) {
	t.Parallel()

	rig := newClaudeInterruptRigWithPreamble(t, []string{claudeResultFinished})
	rig.agent.Mu.Lock()
	rig.agent.turnActive = true
	rig.agent.Mu.Unlock()

	require.NoError(t, rig.agent.Interrupt())

	require.Eventually(t, func() bool { return len(rig.sink.Messages()) > 0 }, time.Second, 5*time.Millisecond,
		"the finished turn stored no result")
	messages := rig.sink.Messages()
	assert.Empty(t, messages[len(messages)-1].Completion,
		"the turn finished before the stop took effect, so the turn end keeps its outcome")
}

// A turn that ends with no stop behind it keeps its own outcome. The note is
// taken only while a turn runs, because Claude Code acknowledges an interrupt
// sent outside one and sends no `result` for it.
func TestClaudeCodeAgent_Interrupt_OutsideATurnMarksNothing(t *testing.T) {
	t.Parallel()

	rig := newClaudeInterruptRigWithPreamble(t, nil)

	require.NoError(t, rig.agent.Interrupt())
	rig.agent.HandleOutput([]byte(`{"type":"result","subtype":"error_during_execution","is_error":true}`))

	messages := rig.sink.Messages()
	require.NotEmpty(t, messages)
	assert.Empty(t, messages[len(messages)-1].Completion,
		"no turn was running, so the next turn's failure stays a failure")
}

func TestClaudeCodeAgent_Interrupt_AfterStopErrors(t *testing.T) {
	t.Parallel()

	rig := newClaudeInterruptRig(t)
	// Mark the agent stopped without driving the cmd lifecycle.
	rig.agent.SetStoppedForTest(true)

	err := rig.agent.Interrupt()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stopped")
}

// InterruptChild addresses the CLI's task registry by the registry row key,
// which IS the task_id. The child is registered the way a live run is: a
// task_started event puts it in the task index.
func TestClaudeCodeAgent_InterruptChild_SendsStopTaskRequest(t *testing.T) {
	t.Parallel()

	rig := newClaudeInterruptRig(t)
	rig.agent.HandleOutput([]byte(`{"type":"system","subtype":"task_started","task_id":"task-1","tool_use_id":"spawn-1","task_type":"local_agent","description":"Reviewer","prompt":"Inspect."}`))

	require.NoError(t, rig.agent.InterruptChild("task-1"))

	captured := rig.captured()
	require.Len(t, captured, 1)
	rec := captured[0]
	assert.Equal(t, "control_request", rec.Type)
	assert.NotEmpty(t, rec.RequestID, "request_id must be populated for response correlation")

	var inner struct {
		Subtype string `json:"subtype"`
		TaskID  string `json:"task_id"`
	}
	require.NoError(t, json.Unmarshal(rec.Request, &inner))
	assert.Equal(t, "stop_task", inner.Subtype,
		"Claude Code stops a subagent with the {subtype:'stop_task'} control_request")
	assert.Equal(t, "task-1", inner.TaskID,
		"stop_task names the CLI's own task id, which the registry row key carries")
}

// A stop_task the CLI refuses surfaces to the caller unchanged. It is a
// provider-side failure, not a missing route and not a missing capability, so
// the two sentinels must not mask the reason.
func TestClaudeCodeAgent_InterruptChild_TheStopTaskErrorSurfaces(t *testing.T) {
	t.Parallel()

	rig := newClaudeInterruptRigWithControlError(t, "no such task")
	rig.agent.HandleOutput([]byte(`{"type":"system","subtype":"task_started","task_id":"task-2","tool_use_id":"spawn-2","task_type":"local_agent","description":"Reviewer","prompt":"Inspect."}`))

	err := rig.agent.InterruptChild("task-2")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no such task")
	assert.NotErrorIs(t, err, agent.ErrChildRouteNotReady)
	assert.NotErrorIs(t, err, agent.ErrChildOperationUnsupported)

	rig.agent.HandleOutput([]byte(`{"type":"system","subtype":"task_notification","task_id":"task-2","tool_use_id":"spawn-2","status":"stopped"}`))
	tasks := rig.sink.BackgroundTasks()
	require.Len(t, tasks, 1)
	assert.Equal(t, bgtask.StatusStopped, tasks[0].Status,
		"a rejected stop_task must not change a later stop into a user interrupt")
}

// A stop WE asked for is a user interrupt, not a plain stop: the closing row
// reports StatusInterrupted and the divider reads "Subagent interrupted". A
// `stopped` notification that no interrupt asked for keeps the plain stop
// word, so the two readings stay apart.
func TestClaudeCodeAgent_InterruptChild_TheClosingRowSaysInterrupted(t *testing.T) {
	t.Parallel()

	rig := newClaudeInterruptRig(t)
	rig.agent.HandleOutput([]byte(`{"type":"system","subtype":"task_started","task_id":"task-3","tool_use_id":"spawn-3","task_type":"local_agent","description":"Reviewer","prompt":"Inspect."}`))
	require.NoError(t, rig.agent.InterruptChild("task-3"))

	rig.agent.HandleOutput([]byte(`{"type":"system","subtype":"task_notification","task_id":"task-3","tool_use_id":"spawn-3","status":"stopped"}`))

	tasks := rig.sink.BackgroundTasks()
	require.Len(t, tasks, 1)
	assert.Equal(t, bgtask.StatusInterrupted, tasks[0].Status,
		"a stop InterruptChild asked for closes as interrupted, not a plain stop")
}

// The CLI can close a subagent before it reads stop_task. It still acknowledges the
// request with a success (a probe of Claude Code 2.1.289 sent stop_task right after
// the child's answer, and the CLI answered `success` and closed the task with
// `status: completed`). The stop did not take effect, so the mark that InterruptChild
// set must not outlive that closing notification. A stale mark would turn a later
// `stopped` notification of a restarted run into "Subagent interrupted", although
// nobody asked LeapMux for that stop.
func TestClaudeCodeAgent_InterruptChild_AStopThatLostTheRaceLeavesNoMark(t *testing.T) {
	t.Parallel()

	rig := newClaudeInterruptRig(t)
	a := rig.agent
	a.HandleOutput([]byte(`{"type":"system","subtype":"task_started","task_id":"task-1","tool_use_id":"tu-spawn","task_type":"local_agent","description":"Explore the parser","prompt":"Find every caller."}`))
	require.NoError(t, a.InterruptChild("task-1"))
	a.HandleOutput([]byte(`{"type":"system","subtype":"task_notification","task_id":"task-1","tool_use_id":"tu-spawn","status":"completed","summary":"done"}`))
	_, status, found, _ := rig.sink.LookupBackgroundTask("task-1")
	require.True(t, found)
	require.Equal(t, bgtask.StatusCompleted, status, "the child finished before the stop took effect")

	// The parent restarts the child, and the child stops by itself.
	sendMessageTo(a, "tu-send", "task-1")
	restartTaskStarted(a, "tu-send", "Keep going.")
	a.HandleOutput([]byte(`{"type":"system","subtype":"task_notification","task_id":"task-1","tool_use_id":"tu-send","status":"stopped"}`))

	_, status, found, _ = rig.sink.LookupBackgroundTask("task-1")
	require.True(t, found)
	assert.Equal(t, bgtask.StatusStopped, status,
		"the first run ended before the stop, so its mark ended with it and this stop is a plain stop")
}

// A forwarded child `result` follows the same rule as the root's: the child turn
// end is interrupted only when the frame states that the stop took effect.
//
// Claude Code 2.1.289 forwards no `result` of a subagent (the probes of a
// foreground child, a background child, and a stopped child showed a
// `task_notification` and nothing else). The path serves a CLI that does forward
// one, and the frames below carry the shapes of the root's `result`.
func TestClaudeCodeAgent_InterruptChild_AChildResultThatFinishedFirstIsNotInterrupted(t *testing.T) {
	t.Parallel()

	rig := newClaudeInterruptRig(t)
	rig.agent.HandleOutput([]byte(`{"type":"system","subtype":"task_started","task_id":"task-r1","tool_use_id":"spawn-r1","task_type":"local_agent","description":"Reviewer","prompt":"Inspect."}`))
	require.NoError(t, rig.agent.InterruptChild("task-r1"))

	rig.agent.HandleOutput([]byte(`{"type":"result","parent_tool_use_id":"spawn-r1","subtype":"success","is_error":false,"terminal_reason":"completed","result":"done"}`))

	messages := rig.sink.Child("child-of-spawn-r1").Messages()
	require.NotEmpty(t, messages)
	assert.Empty(t, messages[len(messages)-1].Completion,
		"the child finished before the stop took effect, so its turn end keeps its outcome")
	tasks := rig.sink.BackgroundTasks()
	require.Len(t, tasks, 1)
	assert.Equal(t, bgtask.StatusCompleted, tasks[0].Status)
}

func TestClaudeCodeAgent_InterruptChild_AChildResultThatStatesTheAbortIsInterrupted(t *testing.T) {
	t.Parallel()

	rig := newClaudeInterruptRig(t)
	rig.agent.HandleOutput([]byte(`{"type":"system","subtype":"task_started","task_id":"task-r2","tool_use_id":"spawn-r2","task_type":"local_agent","description":"Reviewer","prompt":"Inspect."}`))
	require.NoError(t, rig.agent.InterruptChild("task-r2"))

	rig.agent.HandleOutput([]byte(`{"type":"result","parent_tool_use_id":"spawn-r2","subtype":"error_during_execution","is_error":true,"terminal_reason":"aborted_streaming","errors":["[ede_diagnostic] result_type=user last_content_type=n/a stop_reason=null"]}`))

	messages := rig.sink.Child("child-of-spawn-r2").Messages()
	require.NotEmpty(t, messages)
	assert.Equal(t, agent.MessageCompletionInterrupted, messages[len(messages)-1].Completion)
	tasks := rig.sink.BackgroundTasks()
	require.Len(t, tasks, 1)
	assert.Equal(t, bgtask.StatusInterrupted, tasks[0].Status)
}

func TestClaudeCodeAgent_InterruptChild_NotificationBeforeAckSaysInterrupted(t *testing.T) {
	t.Parallel()

	rig := newClaudeInterruptRigWithPreamble(t, []string{
		`{"type":"system","subtype":"task_notification","task_id":"task-5","tool_use_id":"spawn-5","status":"stopped"}`,
	})
	rig.agent.HandleOutput([]byte(`{"type":"system","subtype":"task_started","task_id":"task-5","tool_use_id":"spawn-5","task_type":"local_agent","description":"Reviewer","prompt":"Inspect."}`))

	require.NoError(t, rig.agent.InterruptChild("task-5"))

	tasks := rig.sink.BackgroundTasks()
	require.Len(t, tasks, 1)
	assert.Equal(t, bgtask.StatusInterrupted, tasks[0].Status,
		"the CLI can close the child before it acknowledges stop_task")
}

func TestClaudeCodeAgent_AStoppedRowWithoutAnInterruptSaysStopped(t *testing.T) {
	t.Parallel()

	rig := newClaudeInterruptRig(t)
	rig.agent.HandleOutput([]byte(`{"type":"system","subtype":"task_started","task_id":"task-4","tool_use_id":"spawn-4","task_type":"local_agent","description":"Reviewer","prompt":"Inspect."}`))

	rig.agent.HandleOutput([]byte(`{"type":"system","subtype":"task_notification","task_id":"task-4","tool_use_id":"spawn-4","status":"stopped"}`))

	tasks := rig.sink.BackgroundTasks()
	require.Len(t, tasks, 1)
	assert.Equal(t, bgtask.StatusStopped, tasks[0].Status,
		"a stop nobody asked for keeps the plain stop word")
}

func TestInterrupt_ClaudeWireFormatMatchesProviderClassifier(t *testing.T) {
	t.Parallel()

	// Reconstruct the exact frame Agent.Interrupt produces
	// (sendControlAndWait formats it identically).
	frame := fmt.Sprintf(`{"type":"control_request","request_id":"%s","request":%s}`,
		"req-1", `{"subtype":"interrupt"}`)
	assert.True(t, claudeProvider{}.IsInterrupt(frame),
		"claudeProvider.IsInterrupt must recognise the frame Agent.Interrupt emits")
}

// recordedClaudeControl is a single control_request frame the
// claude interrupt rig captured from the agent's stdin.
type recordedClaudeControl struct {
	Type      string          `json:"type"`
	RequestID string          `json:"request_id"`
	Request   json.RawMessage `json:"request"`
}
