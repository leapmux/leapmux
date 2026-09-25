package amp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"syscall"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/leapmux/leapmux/util/procutil"
)

// ampIdentityEnvKeys are the variables Amp sets on every command it runs. The
// login shell removes them after the user's profile ran, for the reason
// providerkit.FinalizeAgentEnv gives: AMP_THREAD_ID makes a new thread a child
// of the thread it states.
var ampIdentityEnvKeys = []string{envThreadID, "AMP_CURRENT_THREAD_ID", "AGENT_THREAD_ID"}

// launchConfig is what every process of one agent starts from.
type launchConfig struct {
	opts agent.Options
	spec launch.Spec
	// stateDir is the path of the directory that the worker owns for this agent
	// (Agent.dir): the generated settings file and the helper spec live in it.
	// newAgent sets it from that directory.
	stateDir string
	// helperEnv is the entry that points Amp's delegate rule at the helper spec.
	helperEnv string
	// helperProgram is the executable Amp's delegate rule starts.
	helperProgram string
	// getenv and home locate the user's own Amp settings file when shellEnv is
	// nil or its probe establishes nothing.
	getenv func(string) string
	home   string
	// shellEnv reads variables in the environment that the user's shell sets
	// up, where Amp itself reads them. nil reads getenv and home alone.
	shellEnv func(ctx context.Context, names []string) (map[string]string, launch.ProbeResult)
}

// ampProcess is one `amp` process.
type ampProcess struct {
	providerkit.Process
	// resumed states that the process continues an existing thread.
	resumed bool
	// mode is the agent mode that the process started its new thread in. It is
	// empty for a resume, because Amp ignores --mode there.
	mode string
	// exiting is true once the process is on its way out: it printed its
	// `result`, an interrupt signalled it, or Stop runs. No new line goes to it.
	exiting atomic.Bool
	// sawInit is true once the process printed its init line, which is when the
	// thread it serves is known to exist.
	sawInit atomic.Bool
	// sawResult is true once the process printed its `result` line, which ends
	// whatever turn ran.
	sawResult atomic.Bool
	// resumeAfterExit asks the exit handler to continue the thread in a new
	// process at once, because an error ended a turn that was in progress.
	resumeAfterExit atomic.Bool
	// handled closes when the exit handler returns. A new process waits for it,
	// so the handler cannot end the new process's turn.
	handled chan struct{}
}

func (p *ampProcess) ending() bool { return p.exiting.Load() || p.IsStopped() }

func (p *ampProcess) markEnding() { p.exiting.Store(true) }

// writeLine writes one JSON line to Amp's stdin.
func (p *ampProcess) writeLine(line []byte) error {
	if err := p.SendRawInput(line); err != nil {
		return fmt.Errorf("deliver the message to amp: %w", err)
	}
	return nil
}

// signalInterrupt sends SIGINT to the process group, which makes Amp send its
// cancel to the server and exit. Windows has no such signal for a process, and
// the call fails there.
func (p *ampProcess) signalInterrupt() error {
	return procutil.SignalProcessGroup(p.Cmd(), syscall.SIGINT)
}

// launchArgs builds Amp's arguments.
//
//   - `--execute --stream-json --stream-json-input` is the one machine interface
//     that carries a whole conversation over stdin. `--stream-json-thinking`
//     adds the thinking blocks, which Amp drops without it.
//   - `--settings-file` is the settings file that the worker generates for this
//     agent. Amp still merges the workspace's `.amp/settings.json` into it.
//   - `--no-ide` keeps Amp from attaching the open IDE file to each message,
//     `--no-notifications` and `--no-color` keep the output clean, and
//     `--no-remote-control-terminal` keeps ampcode.com out of the terminal.
//   - A new thread takes `--mode`, because Amp fixes the mode when the thread
//     gets its first message. It also takes `--no-archive-after-execute`:
//     execute mode archives a thread it created, and `threads continue` refuses
//     an archived thread, so without it no thread could resume.
//   - `threads continue <id>` resumes a thread. Amp ignores `--mode` there, and
//     it archives only a thread it created.
//   - No `--visibility`: a new thread takes the default visibility of the
//     user's Amp account, the same default as a thread that the user starts
//     with `amp` directly. An account of a team workspace can make that default
//     visible to the team, and the choice stays with the account.
func launchArgs(threadID, mode, settingsPath string) []string {
	var args []string
	if threadID != "" {
		args = append(args, "threads", "continue", threadID)
	}
	args = append(args,
		"--execute", "--stream-json", "--stream-json-input", "--stream-json-thinking",
		"--settings-file", settingsPath,
		"--no-ide", "--no-notifications", "--no-color", "--no-remote-control-terminal",
	)
	if threadID == "" {
		args = append(args, "--mode", mode, "--no-archive-after-execute")
	}
	return args
}

// processEnv is the environment that the shell of one `amp` process starts
// with: the worker's own, less every agent's identity markers and any helper
// spec that it inherited.
func (c launchConfig) processEnv(inherited []string) []string {
	return providerkit.FinalizeAgentEnv(inherited, c.opts)
}

// setEnv is what the shell sets after the user's profile, just before `amp`
// starts: the helper spec that Amp's delegate rule reads, and the update-check
// switch. A profile export cannot replace either (launch.WrapSpec.SetEnv). A
// replaced helper spec would send each permission decision elsewhere.
func (c launchConfig) setEnv() []string {
	return []string{c.helperEnv, envSkipUpdateCheck + "=1"}
}

// startProcess writes the settings file and starts one `amp` process, a new
// thread in mode when threadID is empty and a resume otherwise. The caller holds
// sendMu.
func (a *Agent) startProcess(threadID, mode string) (*ampProcess, error) {
	settingsPath, err := a.launch.writeSettings(a.userSettingsFile())
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(a.ctx)
	cmd, preambleDelimiter, metaPrefix := launch.Wrap(ctx, launch.WrapSpec{
		Shell:        a.launch.opts.Shell,
		LoginShell:   a.launch.opts.LoginShell,
		Launch:       a.launch.spec,
		StripEnvKeys: ampIdentityEnvKeys,
		SetEnv:       a.launch.setEnv(),
		BaseArgs:     launchArgs(threadID, mode, settingsPath),
		WorkingDir:   a.launch.opts.WorkingDir,
	})
	cmd.Env = a.launch.processEnv(cmd.Environ())

	stdin, stdout, stderr, err := providerkit.SetupProcessPipes(cmd, cancel)
	if err != nil {
		return nil, err
	}
	proc := &ampProcess{
		Process: providerkit.NewProcess(a.launch.opts, "amp", cmd, stdin, ctx, cancel, preambleDelimiter, metaPrefix),
		resumed: threadID != "",
		handled: make(chan struct{}),
	}
	if threadID == "" {
		proc.mode = mode
	}
	if err := proc.StartCmd(cmd, cancel); err != nil {
		return nil, err
	}
	proc.DrainStderr(stderr)

	// The reader starts first: it is what closes the exit channel, which a
	// refused adoption waits on when it stops the process.
	go proc.ReadOutput(agent.NewStdoutScanner(stdout), func(*providerkit.ParsedLine) bool { return false },
		func(line *providerkit.ParsedLine) { a.handleLine(proc, line) })
	if err := a.adopt(proc); err != nil {
		return nil, err
	}
	return proc, nil
}

// adopt makes proc the running process and starts watching for its exit. It
// stops proc and refuses it when Stop already ran.
func (a *Agent) adopt(proc *ampProcess) error {
	a.mu.Lock()
	if a.stopped {
		a.mu.Unlock()
		proc.markEnding()
		proc.Stop()
		return errAgentStopped
	}
	a.proc = proc
	a.mu.Unlock()
	go a.superviseExit(proc)
	return nil
}

// superviseExit waits for one process to exit and handles the exit.
//
// It marks the process ending FIRST. A message that reads the process from
// now on then waits for the exit handler (see processForTurn), and cannot start
// a new process while the handler still acts on the agent's state.
func (a *Agent) superviseExit(proc *ampProcess) {
	<-proc.ProcessDone()
	proc.markEnding()
	a.handleProcessExit(proc)
}

// handleProcessExit handles the end of one process. It runs after the reader
// handled the process's last line, because the reader closes the exit channel
// only once stdout ended.
//
// The process stays the agent's process until the LAST step. processForTurn
// waits for proc.handled when it finds an ending process, so a new process
// starts only after the handler returns. The permission requests that the
// handler refuses, the shell rows that it closes and the turn that it ends are
// therefore never the next process's.
//
// A turn that no `result` ended -- the process crashed, it failed to start, or a
// platform stop replaced the interrupt signal -- ends here, as an interruption
// when the user asked for one and as an error otherwise. An error that ended a
// turn resumes the thread in a new process at once. After every other exit, the
// next message starts the new process. Nothing retries: the agent reports a
// resume that fails, and the next message tries again.
func (a *Agent) handleProcessExit(proc *ampProcess) {
	defer close(proc.handled)
	stderr := a.exitStderr(proc)
	a.mu.Lock()
	a.lastStderr = stderr
	stopped := a.stopped
	threadID := a.threadID
	a.mu.Unlock()
	defer a.releaseProcess(proc)

	// A helper still waiting belongs to the process that just exited, and no
	// answer can reach it through that process.
	a.bridge.cancelAll(errProcessExited)
	// Amp stops every command that it started when it shuts down in order, and
	// it prints its `result` on that path. The agent's Stop asks for that
	// shutdown too. A crash skips it, so a background command can outlive Amp,
	// and its row must not claim a stop.
	if proc.sawResult.Load() || stopped {
		a.closeAllShellRows(bgtask.StatusStopped)
	} else {
		a.closeAllShellRows(bgtask.StatusInterrupted)
	}
	if stopped || a.discarding() {
		return
	}

	// Computed here from the stderr read above, and not inside the callback,
	// because endTurn runs the callback under the agent's lock.
	message := exitMessage(proc, threadID, stderr)
	ended, turnFailed := false, false
	a.endTurn(func(turn turnState) (agent.MessageCompletion, []byte) {
		ended = true
		if turn.interruptRequested {
			return agent.MessageCompletionInterrupted, errorResultLine(threadID, interruptedMessage)
		}
		turnFailed = true
		return agent.MessageCompletionError, errorResultLine(threadID, message)
	})
	resume := proc.resumesThreadAfterExit(turnFailed)
	if !ended && !proc.sawResult.Load() && !proc.IntentionalStopRequested() {
		// An idle process died with no `result`. Nothing awaits a reply, so the
		// transcript states the failure and the next message starts a process.
		a.sink.PersistLeapMuxNotification(map[string]any{
			contracts.NotificationFieldType:  contracts.NotificationTypeAgentError,
			contracts.NotificationFieldError: message,
		})
	}
	if resume && threadID != "" {
		// On its own goroutine: it takes sendMu, and a message that waits for
		// this handler to return holds sendMu already.
		go a.resumeAfterError(proc)
	}
}

// resumesThreadAfterExit reports whether the exit of p continues the thread in
// a new process at once. An error that ended a turn does so: the error came from
// the `result` line (resumeAfterExit) or from the exit itself (turnFailed).
//
// A resume that failed before its init line does not: its thread did not
// open, so the same command would fail again and repeat the same error. The
// next message tries again, and the error already gives the `/clear` hint.
func (p *ampProcess) resumesThreadAfterExit(turnFailed bool) bool {
	if p.resumed && !p.sawInit.Load() {
		return false
	}
	return turnFailed || p.resumeAfterExit.Load()
}

// releaseProcess stops treating proc as the agent's process. It is the last
// step of the exit handler.
func (a *Agent) releaseProcess(proc *ampProcess) {
	a.mu.Lock()
	if a.proc == proc {
		a.proc = nil
	}
	a.mu.Unlock()
}

// resumeAfterError continues the thread in a new process after an error ended a
// turn, so the thread has an executor again before the next message.
//
// It runs once for each such exit and never loops: an exit of the resumed
// process that ends no turn starts nothing.
//
// It waits for the exit handler of exited to return first, because the handler
// releases the process as its last step, and a start before that would find
// the process still in place.
func (a *Agent) resumeAfterError(exited *ampProcess) {
	select {
	case <-exited.handled:
	case <-a.ctx.Done():
		return
	}
	a.sendMu.Lock()
	defer a.sendMu.Unlock()
	a.mu.Lock()
	skip := a.stopped || a.proc != nil || a.threadID == "" || a.turn.active
	threadID, mode := a.threadID, a.agentMode
	a.mu.Unlock()
	if skip {
		return
	}
	if _, err := a.start(threadID, mode); err != nil {
		// Stop can overtake the start: the start then fails because the agent
		// stopped, and a stopped agent reports nothing.
		if a.IsStopped() || errors.Is(err, errAgentStopped) || errors.Is(err, context.Canceled) {
			return
		}
		slog.Warn("amp resume after an error failed", "agent_id", a.agentID, "thread_id", threadID, "error", err)
		a.sink.PersistLeapMuxNotification(map[string]any{
			contracts.NotificationFieldType:  contracts.NotificationTypeAgentError,
			contracts.NotificationFieldError: fmt.Sprintf("could not resume the Amp thread %s: %v", threadID, err),
		})
	}
}

// interruptedMessage is the error of the `result` that ends an interrupted turn
// whose process printed none. It is Amp's own wording for the same event.
const interruptedMessage = "User cancelled (SIGINT/SIGTERM)"

// exitMessage states why a process ended with no `result`. A resumed process
// that failed before its init line could not reopen its thread, and a fresh
// thread is one command away.
func exitMessage(proc *ampProcess, threadID, stderr string) string {
	message := describeExit(proc, stderr)
	if proc.resumed && !proc.sawInit.Load() {
		return providerkit.ResumeFailedError(threadID, errors.New(message)).Error()
	}
	return message
}

// trimStderr keeps the part of Amp's stderr that states an error. Amp prints
// `Error: <message>` and, for an unexpected error, a hint about its command
// palette that means nothing outside its terminal UI.
func trimStderr(stderr string) string {
	lines := strings.Split(strings.TrimSpace(stderr), "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.Contains(line, "command palette") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}
