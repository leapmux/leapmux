package amp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/quartz"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/agentdir"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// Agent is one Amp session. It SUPERVISES a sequence of `amp` processes rather
// than owning one: Amp exits after an interrupt and after any server error, and
// the thread goes on in the next process (`amp threads continue`). The LeapMux
// agent ends only when Stop runs.
//
// It starts no conversation process until the first message. Amp creates its server thread
// when the process starts, so an agent that sends nothing leaves nothing in the
// user's account, and a mode change before the first message needs no restart.
type Agent struct {
	agentID string
	sink    agent.ProviderServices
	steers  providerkit.SteerLedger
	launch  launchConfig
	bridge  *permissionBridge
	// dir is the agent's private directory, whose path launch.stateDir holds.
	// Stop removes it.
	dir   *agentdir.Dir
	clock quartz.Clock

	// ctx lives as long as the agent, and every process derives from it. Stop
	// cancels it.
	ctx    context.Context
	cancel context.CancelFunc

	// sendMu serializes the operations that start a process or write a user line:
	// input, steering, and the resume after an unplanned exit. It is never held
	// while a turn runs.
	sendMu sync.Mutex

	// startFn starts one process. Production leaves it nil, which selects
	// startProcess. A test installs a starter of fake processes.
	startFn func(threadID string, config threadConfiguration) (*ampProcess, error)
	// stderrFn reads the stderr of a process that exited. Production leaves it
	// nil, which reads the process's own stderr. A test installs a reader.
	stderrFn func(proc *ampProcess) string
	// exportFn supplies native history in tests. Production runs the export command.
	exportFn func(context.Context, string) ([]byte, error)

	// userEnvMu guards the three fields below. The first process start that
	// reads the user's shell settles them.
	userEnvMu sync.Mutex
	// userEnvRead is true once the shell's values were read.
	userEnvRead bool
	userGetenv  func(string) string
	userHome    string

	// settingsMu serializes setting changes through their persistence. Take it before mu.
	settingsMu sync.Mutex

	// mu guards every field below it.
	mu sync.Mutex
	providerkit.TurnSeq

	// proc is the running `amp` process, or nil between processes.
	proc *ampProcess
	// threadID is Amp's thread, the resume handle. Empty until the first
	// process states it in its init line.
	threadID         string
	nativeSteers     []*ampSteerSubmission
	reconciling      bool
	importedMessages map[string]bool
	// agentMode is the mode the thread starts with. modeLocked is true once the
	// thread received its first message, after which Amp keeps the mode.
	agentMode string
	// fastMode is true, false, or empty when native resume metadata is unavailable.
	fastMode   string
	modeLocked bool
	// permissionMode decides each permission request when it arrives.
	permissionMode string
	turn           turnState
	stopped        bool
	// tools holds the tool calls that started and did not end, by tool-use id.
	tools map[string]*openTool
	// shells holds the registry row of each background command of the running
	// process, by PID. See subagent.go.
	shells    map[int]string
	nextOrder uint64
	// contextUsage is the last context-usage broadcast, so an unchanged reading
	// is not broadcast again.
	contextUsage map[string]any
	// lastStderr is the stderr of the last process that exited.
	lastStderr string

	discard atomic.Bool
	// done closes when Stop finishes.
	done     chan struct{}
	stopOnce sync.Once
}

// Compile-time checks of the optional interfaces this agent implements.
// Manager.SupportsSteering answers false, with no build error, for a provider
// that stops satisfying InputSteerer, so the assertion makes that regression a
// compile error.
var (
	_ agent.Agent        = (*Agent)(nil)
	_ agent.InputSteerer = (*Agent)(nil)
)

// turnState is the turn the agent owes the user a reply for. Guarded by mu.
//
// Amp prints no turn start and no per-turn result, so the worker arms the turn
// when it writes a user line and ends it at the assistant message whose stop
// reason is end_turn, or at the process's `result` line.
type turnState struct {
	active    bool
	startedAt time.Time
	// interruptRequested records that the user stopped the turn, so the exit
	// that the interrupt causes reads as an interruption and not as an error. A
	// `result` that states a failure of Amp's own outranks it (see
	// handleResultLine). The note ends with the turn: the signal itself is
	// ampProcess.interruptSignalled.
	interruptRequested bool
	// assistantMessages counts the assistant lines of the turn, which is what
	// Amp's own `num_turns` counts.
	assistantMessages int
	// lastText is the text of the turn's last assistant message, the `result`
	// that Amp states when a process ends after one turn.
	lastText string
	// toolUses counts the tool results of the turn. The turn-end row carries it
	// together with the calls that the turn outlived (see endTurn).
	toolUses int
}

// openTool is one tool call that started and did not end.
type openTool struct {
	id    string
	name  string
	input json.RawMessage
	// row is the persisted opening row. The worker closes a call that its turn
	// outlived with this row.
	row      []byte
	order    uint64
	subagent bool
	// matched is true once a permission request claimed this call. A second
	// request for an identical call then takes the next one.
	matched bool
}

// now reads the agent's clock.
func (a *Agent) now() time.Time { return a.clock.Now() }

// AgentID returns the agent's id.
func (a *Agent) AgentID() string { return a.agentID }

// IsStopped reports whether Stop ran.
func (a *Agent) IsStopped() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.stopped
}

// DiscardOutput drops every line the running process prints from now on. The
// service calls it before a stop that a restart follows.
func (a *Agent) DiscardOutput() { a.discard.Store(true) }

func (a *Agent) discarding() bool { return a.discard.Load() }

// Wait blocks until the agent ends. It ends when Stop runs: the agent replaces
// an `amp` process that exits, and does not report the exit here.
func (a *Agent) Wait() error {
	<-a.done
	return nil
}

// Stderr returns the stderr of the running process, or of the last one that
// exited.
func (a *Agent) Stderr() string {
	a.mu.Lock()
	proc, last := a.proc, a.lastStderr
	a.mu.Unlock()
	if proc != nil {
		return proc.Stderr()
	}
	return last
}

// HandleOutput handles one line as if the running process printed it. Tests
// drive the agent through it.
func (a *Agent) HandleOutput(content []byte) {
	a.mu.Lock()
	proc := a.proc
	a.mu.Unlock()
	a.handleLine(proc, providerkit.ParseLine(content))
}

// SendInput starts a turn with one user message.
func (a *Agent) SendInput(content string, attachments []*leapmuxv1.Attachment) error {
	return a.sendInput(nil, content, attachments, false)
}

// SendInputForSession starts a turn with one user message, when the thread it
// states is still the current one.
func (a *Agent) SendInputForSession(sessionID, content string, attachments []*leapmuxv1.Attachment) error {
	return a.sendInput(&sessionID, content, attachments, false)
}

// SupportsSteering reports true: Amp takes a steering line during any turn.
func (a *Agent) SupportsSteering() bool { return true }

// SteerRowAtEcho records the queue row when Amp echoes the prompt.
func (*Agent) SteerRowAtEcho() bool { return true }

// SteerSurvivesTurnEnd keeps a late steer in Amp's native queue.
func (*Agent) SteerSurvivesTurnEnd() bool { return true }

var _ agent.SteerEchoReporter = (*Agent)(nil)
var _ agent.SteerSurvivor = (*Agent)(nil)

// SteerInput adds a message to the running turn. Amp queues it on its server
// and inserts it at the turn's next interruption point, after the running tool.
func (a *Agent) SteerInput(content string, attachments []*leapmuxv1.Attachment) error {
	return a.sendInput(nil, content, attachments, true)
}

// sendInput writes one user line.
//
// A plain line during a turn would go to Amp's own server queue and become the
// next turn, out of LeapMux's sight, so the agent refuses a new message while a
// turn runs: the LeapMux input queue owns queueing. A steering line needs the
// turn.
//
// A new turn starts a process when none runs: a new thread for an agent with no
// thread yet, else `amp threads continue`. The agent arms the turn BEFORE the
// write, so the queue holds the next message behind this one from the moment
// it leaves.
func (a *Agent) sendInput(expected *string, content string, attachments []*leapmuxv1.Attachment, steer bool) error {
	line, texts, err := buildUserLine(content, attachments, steer)
	if err != nil {
		return err
	}

	a.sendMu.Lock()
	defer a.sendMu.Unlock()

	a.mu.Lock()
	if err := providerkit.CheckInputSession(expected, a.threadID); err != nil {
		a.mu.Unlock()
		return err
	}
	if a.stopped {
		a.mu.Unlock()
		return errAgentStopped
	}
	active := a.turn.active
	proc := a.proc
	a.mu.Unlock()

	if steer {
		if !active || proc == nil || proc.ending() {
			return agent.ErrNoActiveTurn
		}
		baseline, err := a.steerBaseline()
		if err != nil {
			return err
		}
		key := ampEchoKey(texts)
		ticket, err := a.rememberSteer(proc, key, baseline)
		if err != nil {
			return err
		}
		if err := proc.writeLine(line); err != nil {
			a.steers.Forget(ticket)
			a.forgetSteer(ticket)
			a.steers.Settle(a.sink)
			return err
		}
		return nil
	}
	if active {
		return agent.ErrAgentBusy
	}

	proc, err = a.processForTurn(proc)
	if err != nil {
		return err
	}
	a.armTurn()
	if err := proc.writeLine(line); err != nil {
		a.disarmTurn()
		return err
	}
	a.lockMode(proc)
	return nil
}

// processForTurn returns the process a new turn writes to, starting one when
// none runs. The caller holds sendMu.
//
// A process that already printed its `result`, or that an interrupt signalled,
// is on its way out, and Amp's server refuses a second executor on the same
// thread ("already open and active in another Amp"). So the new process starts
// only once the old one is gone.
func (a *Agent) processForTurn(proc *ampProcess) (*ampProcess, error) {
	if proc != nil && !proc.ending() {
		// A new turn in a running process: start checks the workspace for a
		// new process, and this checks it for the turn.
		if err := a.checkWorkspace(); err != nil {
			return nil, err
		}
		return proc, nil
	}
	if proc != nil {
		if err := a.awaitExit(proc); err != nil {
			return nil, err
		}
	}
	a.mu.Lock()
	threadID, config := a.threadID, threadConfiguration{mode: a.agentMode, fast: a.fastMode == "true"}
	active := a.turn.active || a.reconciling
	a.mu.Unlock()
	if active {
		return nil, agent.ErrAgentBusy
	}
	return a.start(threadID, config)
}

// start starts one process for threadID, or for a new thread in mode when
// threadID is empty. The caller holds sendMu.
//
// It checks the workspace first, because a process loads the workspace's
// settings and plugins as soon as it starts (see checkWorkspace).
func (a *Agent) start(threadID string, config threadConfiguration) (*ampProcess, error) {
	if err := a.checkUncertainThread(threadID); err != nil {
		return nil, err
	}
	if err := a.checkWorkspace(); err != nil {
		return nil, err
	}
	if a.startFn != nil {
		return a.startFn(threadID, config)
	}
	return a.startProcess(threadID, config)
}

// awaitExit waits for a process that is on its way out, and for its exit
// handler, and stops the process when it does not go within the API timeout.
func (a *Agent) awaitExit(proc *ampProcess) error {
	timer := a.clock.NewTimer(a.launch.opts.EffectiveAPITimeout(), "amp", "await-exit")
	defer timer.Stop()
	select {
	case <-proc.handled:
		return nil
	case <-timer.C:
		slog.Warn("amp process did not exit, so the agent stops it", "agent_id", a.agentID)
		proc.Stop()
	case <-a.ctx.Done():
		return errAgentStopped
	}
	select {
	case <-proc.handled:
		return nil
	case <-a.ctx.Done():
		return errAgentStopped
	}
}

// armTurn marks a new turn active and publishes it.
func (a *Agent) armTurn() {
	now := a.now()
	a.mu.Lock()
	a.turn = turnState{active: true, startedAt: now}
	a.mu.Unlock()
	a.sink.ReportProgress(agent.ResetModelProgress())
	a.PublishTurnActive()
}

// disarmTurn releases a turn whose line never reached Amp.
func (a *Agent) disarmTurn() {
	a.mu.Lock()
	a.turn = turnState{}
	a.mu.Unlock()
	a.PublishTurnActive()
}

// lockMode records that the thread received its first message. Amp keeps the
// mode and speed from here on, so both groups turn read-only, and the settings view
// learns it at once.
//
// The mode locks at the value that proc started the thread in. A change that
// landed while proc started does not reach the thread, so it must not show.
func (a *Agent) lockMode(proc *ampProcess) {
	a.settingsMu.Lock()
	defer a.settingsMu.Unlock()
	a.mu.Lock()
	changed := !a.modeLocked
	a.modeLocked = true
	if changed && proc.config.mode != "" {
		a.agentMode = proc.config.mode
		a.fastMode = strconv.FormatBool(proc.config.fast)
	}
	threadID := a.threadID
	a.mu.Unlock()
	if changed {
		a.sink.PersistSettingsRefresh(agent.CurrentOptions(a.OptionGroups()))
		a.sink.BroadcastStatusActive(threadID)
	}
}

// PublishTurnActive republishes the turn flag. A turn always takes steering.
func (a *Agent) PublishTurnActive() agent.TurnState {
	a.mu.Lock()
	state := agent.TurnState{
		Active:    a.turn.active || a.reconciling,
		Steerable: a.turn.active && !a.reconciling && a.threadID != "" && a.proc != nil && !a.proc.ending(),
	}
	seq := a.NextTurnSeq()
	a.mu.Unlock()
	a.sink.SetTurnState(state, seq)
	return state
}

// Interrupt stops the running turn.
//
// Amp takes no interrupt line: SIGINT makes the CLI send its cancel to the
// server, print an error `result`, and EXIT. The next message resumes the
// thread in a new process. The agent stops a process that ignores the signal
// after the API timeout, and on a platform that cannot send the signal
// (Windows) it stops the process at once. A stop cannot tell Amp's server to
// cancel, so the server can finish the turn with no executor attached.
// The exit handler reads native history to recover pending steers and their replies.
//
// It does nothing when no turn runs.
func (a *Agent) Interrupt(stop agent.StopContext) error {
	a.mu.Lock()
	if a.stopped {
		a.mu.Unlock()
		return errAgentStopped
	}
	if !a.turn.active {
		a.mu.Unlock()
		return nil
	}
	a.turn.interruptRequested = true
	proc := a.proc
	a.mu.Unlock()

	// Amp withdraws nothing it leased, and the helpers it started die with it,
	// so the banners of this turn go now rather than when the exit arrives.
	a.bridge.cancelAll(errTurnInterrupted)
	if proc == nil {
		// No process runs, so nothing can end the turn by itself: the exit
		// handler releases the process only after it ends the turn, and the
		// resume after an error starts a process with no turn.
		return nil
	}
	proc.markEnding()
	if err := proc.signalInterrupt(); err != nil {
		slog.Info("amp interrupt signal failed, so the agent stops the process", "agent_id", a.agentID, "error", err)
		go proc.Stop()
		return nil
	}
	go a.stopIfStillRunning(proc)
	return nil
}

// stopIfStillRunning stops a process that an interrupt signalled and that did
// not exit within the API timeout.
func (a *Agent) stopIfStillRunning(proc *ampProcess) {
	timer := a.clock.NewTimer(a.launch.opts.EffectiveAPITimeout(), "amp", "interrupt-escalation")
	defer timer.Stop()
	select {
	case <-proc.ProcessDone():
	case <-a.ctx.Done():
	case <-timer.C:
		slog.Warn("amp ignored the interrupt, so the agent stops the process", "agent_id", a.agentID)
		proc.Stop()
	}
}

// ClearContext refuses: one Amp process serves one thread, and the service
// restarts the agent with no resume handle, which starts a new thread at the
// next message.
func (a *Agent) ClearContext() (string, error) { return "", agent.ErrContextClearUnsupported }

// Stop ends the agent: it withdraws every permission request, interrupts and
// stops the running process, closes whatever the process left open, and
// removes the agent's own directory.
func (a *Agent) Stop() {
	a.stopOnce.Do(a.stop)
	<-a.done
}

func (a *Agent) stop() {
	a.mu.Lock()
	a.stopped = true
	// Amp answers the SIGINT below with an error `result`, which the reader
	// handles before the stop ends the turn itself. The flag makes that result
	// end the turn as interrupted, as Interrupt does.
	if a.turn.active {
		a.turn.interruptRequested = true
	}
	proc := a.proc
	a.mu.Unlock()

	a.cancel()
	a.bridge.close(errAgentStopped)
	if proc != nil {
		proc.markEnding()
		proc.NoteIntentionalStop()
		// SIGINT first, so Amp sends its cancel and the server stops the turn;
		// Process.Stop then closes stdin and reaps the process group.
		if err := proc.signalInterrupt(); err != nil {
			slog.Debug("amp stop signal failed", "agent_id", a.agentID, "error", err)
		}
		proc.Stop()
	}
	// A turn that no `result` ended -- a kill ended the process, or none ran --
	// ends as an interruption.
	if proc != nil {
		<-proc.handled
	}
	a.finishTurn(agent.MessageCompletionInterrupted, nil)
	// A process exit closes these rows also. Stop covers an agent that started no process.
	a.closeAllShellRows(bgtask.StatusStopped)
	a.sink.ReportProgress(agent.ResetProgress())
	providerkit.RemoveAgentDir("amp", a.agentID, a.dir)
	close(a.done)
}

// errAgentStopped refuses work for an agent that Stop ended.
var errAgentStopped = errors.New("agent is stopped")

// errTurnInterrupted answers a permission request whose turn the user stopped.
var errTurnInterrupted = errors.New("the user interrupted the turn")

// errProcessExited answers a permission request whose Amp process exited.
var errProcessExited = errors.New("the Amp process exited")

// errTurnEnded answers a permission request that outlived its turn.
var errTurnEnded = errors.New("the turn ended")

// exitStderr reads the stderr of a process that exited. The read can wait for
// the stderr drain (see providerkit.Process.Stderr).
func (a *Agent) exitStderr(proc *ampProcess) string {
	if a.stderrFn != nil {
		return a.stderrFn(proc)
	}
	return proc.Stderr()
}

// describeExit states why a process ended, for a transcript row: the exit code,
// and stderr, what the process wrote to its stderr.
func describeExit(proc *ampProcess, stderr string) string {
	message := proc.ProcessExitError().Error()
	if stderr := trimStderr(stderr); stderr != "" {
		message = fmt.Sprintf("%s: %s", message, stderr)
	}
	return message
}
