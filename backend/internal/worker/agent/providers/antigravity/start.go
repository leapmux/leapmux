package antigravity

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/coder/quartz"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/agentdir"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

var _ agent.StartFunc = Start

const antigravityStopSignal = syscall.SIGTERM

func Start(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (agent.Agent, error) {
	ctx, cancel := context.WithCancel(ctx)
	registration := Registration()
	spec, err := providerkit.ResolveLaunch(ctx, opts, registration)
	if err != nil {
		cancel()
		return nil, err
	}
	directory, err := opts.AgentDirs.New(ctx, agentdir.Spec{Prefix: "antigravity"})
	if err != nil {
		cancel()
		return nil, err
	}
	logPath := filepath.Join(directory.Path(), "acp.log")
	cmd, delimiter, metaPrefix := launch.Wrap(ctx, launch.WrapSpec{
		Shell: opts.Shell, LoginShell: opts.LoginShell, Launch: spec,
		BaseArgs: []string{"acp", "--log-file=" + logPath}, WorkingDir: opts.WorkingDir,
	})
	pipes, err := providerkit.SetupProcessPipes(cmd, cancel)
	if err != nil {
		providerkit.RemoveAgentDir("antigravity", opts.AgentID, directory)
		cancel()
		return nil, err
	}
	a := newAgentState(sink, antigravityRPC{timeout: opts.EffectiveAPITimeout()}, opts.WorkingDir, quartz.NewReal())
	a.JSONRPCProcess = providerkit.JSONRPCProcess{Process: providerkit.NewProcess(opts, providerkit.ProcessLaunch{
		ProviderName: "agy", ShutdownGrace: registration.ShutdownGrace,
		PreambleDelimiter: delimiter, PreambleMetaPrefix: metaPrefix, StopSignal: antigravityStopSignal,
	}, pipes, ctx, cancel)}
	a.agentDir = directory
	a.logPath = logPath
	a.homeDir = opts.HomeDir
	if a.homeDir == "" {
		a.homeDir, _ = os.UserHomeDir()
	}
	a.model = opts.Model()
	if a.model == "" {
		a.model = agent.DefaultModelSentinel
	}
	a.effort = opts.Effort()
	if mode := opts.PermissionMode(); validAntigravityMode(mode) {
		a.mode = mode
	}
	if workspace, err := filepath.EvalSymlinks(opts.WorkingDir); err == nil {
		a.workspace = workspace
	}
	a.streamCtx, a.streamCancel = context.WithCancel(ctx)
	if err := a.StartCmd(); err != nil {
		providerkit.RemoveAgentDir("antigravity", opts.AgentID, directory)
		cancel()
		return nil, err
	}
	a.DrainStderr(pipes.Stderr())
	go a.ReadOutputLoop(agent.NewStdoutScanner(pipes.Stdout()), a.RefuseUnsupportedRequest)
	cleanup := func(stage string, cause error) (agent.Agent, error) {
		logTail := lastLogLines(logPath, 20)
		a.Stop()
		_ = a.Wait()
		if opts.ResumeSessionID != "" && cause != errAntigravitySignIn {
			return nil, cause
		}
		if logTail != "" {
			cause = fmt.Errorf("%w\nLast Antigravity log lines:\n%s", cause, logTail)
		}
		return nil, a.FormatStartupError(stage, cause)
	}
	if err := a.initializeACP(ctx); err != nil {
		return cleanup("initialize", err)
	}
	sessionID, err := a.openACPSession(ctx, opts.ResumeSessionID)
	if err != nil {
		return cleanup("session", err)
	}
	if err := a.discoverLSPort(ctx, opts.EffectiveStartupTimeout()); err != nil {
		return cleanup("language server", err)
	}
	if err := a.loadCatalog(); err != nil {
		return cleanup("model catalog", err)
	}
	a.Mu.Lock()
	root := a.installRoot(sessionID, opts.ResumeSessionID != "")
	a.Mu.Unlock()
	if opts.ResumeSessionID != "" {
		if err := a.seedStepCount(ctx, root); err != nil {
			return cleanup("resume step count", providerkit.ResumeFailedError(opts.ResumeSessionID, err))
		}
	}
	a.loadSlashCommands()
	a.startCascadeStream(root)
	if err := a.waitForRoot(ctx, root, opts.EffectiveStartupTimeout()); err != nil {
		return cleanup("event stream", err)
	}
	a.sink.UpdateSessionID(sessionID)
	a.sink.BroadcastStatusActive(sessionID)
	a.PublishTurnActive()
	return a, nil
}

func (a *Agent) seedStepCount(ctx context.Context, root *cascadeState) error {
	var err error
	for range 3 {
		attemptCtx, cancel := context.WithTimeout(ctx, a.rpc.timeout)
		var count int
		count, err = a.rpc.readStepCount(attemptCtx, root.id)
		cancel()
		if err == nil {
			a.Mu.Lock()
			root.seededCount = count
			a.Mu.Unlock()
			return nil
		}
		if isStreamNotFound(err) {
			break
		}
	}
	return err
}

func (a *Agent) waitForRoot(ctx context.Context, root *cascadeState, timeout time.Duration) error {
	timer := a.clock.NewTimer(timeout, antigravityStreamConnectTimerTag)
	defer timer.Stop(antigravityStreamConnectTimerTag)
	select {
	case <-root.connected:
		a.Mu.Lock()
		err := root.connectErr
		a.Mu.Unlock()
		return err
	case <-a.ProcessDone():
		return providerkit.ErrServerExited
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return fmt.Errorf("the Antigravity event stream did not connect within %s", timeout)
	}
}
