package deepseekharness

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/envutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

var _ agent.StartFunc = Start

// Start chooses the full native Web profile and opens its Remote stream before input.
func Start(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (agent.Agent, error) {
	ctx, cancel := context.WithCancel(ctx)
	registration := Registration()
	spec, err := providerkit.ResolveLaunch(ctx, opts, registration)
	if err != nil {
		cancel()
		return nil, err
	}
	if opts.AgentDirs == nil {
		cancel()
		return nil, fmt.Errorf("DeepSeek Harness requires the Worker's private Agent directories")
	}
	directory, err := opts.AgentDirs.New(ctx, agentDirSpec())
	if err != nil {
		cancel()
		return nil, err
	}
	imageReceipts, err := prepareImageHook(directory.Path())
	if err != nil {
		cancel()
		return nil, errors.Join(err, directory.Close())
	}
	pinnedEnv := []string{"TMPDIR=" + imageReceipts.Temporary, "TMP=" + imageReceipts.Temporary, "TEMP=" + imageReceipts.Temporary}
	cmd, delimiter, meta := launch.Wrap(ctx, launch.WrapSpec{Shell: opts.Shell, LoginShell: opts.LoginShell, Launch: spec, BaseArgs: []string{"--profile", "web", "--patch", imageReceipts.Overlay, "--host", "127.0.0.1", "--port", "0", "--no-open"}, WorkingDir: opts.WorkingDir, SetEnv: pinnedEnv})
	cmd.Env = envutil.PinEnv(providerkit.FinalizeAgentEnv(cmd.Environ(), opts), pinnedEnv...)
	pipes, err := providerkit.SetupProcessPipes(cmd, cancel)
	if err != nil {
		return nil, errors.Join(err, directory.Close())
	}
	stdout, stderr := pipes.Stdout(), pipes.Stderr()
	a := newAgent(agent.NewModelProgressResetSink(sink), opts.WorkingDir)
	a.Process = providerkit.NewProcess(opts, providerkit.ProcessLaunch{ProviderName: "dsh", ShutdownGrace: registration.ShutdownGrace, PreambleDelimiter: delimiter, PreambleMetaPrefix: meta}, pipes, ctx, cancel)
	a.directory = directory
	a.imageReceipts = imageReceipts
	a.sink = agent.NewModelProgressResetSink(newToolTranscript(ctx, sink, a))
	a.rpc.timeout = opts.EffectiveAPITimeout()
	if err := a.StartCmd(); err != nil {
		return nil, errors.Join(err, directory.Close())
	}
	a.DrainStderr(stderr)
	listen := providerkit.NewListenWaiter(nativeListenPattern)
	go a.ReadLines(agent.NewStdoutScanner(stdout), func(line []byte) {
		listen.Observe(line)
		if !nativeListenPattern.Match(line) {
			slog.Debug("DeepSeek Harness stdout", "agent_id", a.AgentID(), "line", string(line))
		}
	})
	fail := func(phase string, err error) (agent.Agent, error) {
		a.Stop()
		_ = a.Wait()
		return nil, a.FormatStartupError(phase, err)
	}
	address, err := listen.Wait(ctx, a.ProcessDone(), opts.EffectiveStartupTimeout())
	if err != nil {
		return fail("listen", err)
	}
	startup, startupCancel := context.WithTimeout(ctx, opts.EffectiveStartupTimeout())
	defer startupCancel()
	a.rpc.endpoint, err = authenticateNative(startup, address)
	if err != nil {
		return fail("authenticate", err)
	}
	var nativeCatalog nativeModelCatalog
	if err := a.rpc.call(startup, "session/modelCatalog", nil, &nativeCatalog); err != nil {
		return fail("model catalog", err)
	}
	a.catalog, err = convertModelCatalog(nativeCatalog)
	if err != nil {
		return fail("model catalog", err)
	}
	a.selection = a.catalog.defaultSelection
	if err := a.openConnection(startup); err != nil {
		return fail("Remote stream", err)
	}
	select {
	case <-a.ready:
	case <-a.ProcessDone():
		return fail("Remote stream", providerkit.ErrServerExited)
	case <-startup.Done():
		return fail("Remote stream", startup.Err())
	}
	sessionID, err := a.createSession(startup, opts.ResumeSessionID)
	if err != nil {
		return fail("Session", err)
	}
	a.Mu.Lock()
	a.sessionID = sessionID
	a.Mu.Unlock()
	if err := a.followSession(sessionAddress{Kind: "session", SessionID: sessionID}, ""); err != nil {
		return fail("Session stream", err)
	}
	a.Mu.Lock()
	var opening <-chan struct{}
	for _, stream := range a.streams {
		if stream.sessionID == sessionID {
			opening = stream.ready
			break
		}
	}
	a.Mu.Unlock()
	if opening == nil {
		return fail("Session stream", fmt.Errorf("DeepSeek Harness has no Session stream"))
	}
	select {
	case <-opening:
	case <-a.ProcessDone():
		return fail("Session stream", providerkit.ErrServerExited)
	case <-startup.Done():
		return fail("Session stream", startup.Err())
	}
	a.Mu.Lock()
	current := a.selection
	mode, permissions := a.mode, a.permissions
	a.Mu.Unlock()
	selection, err := a.catalog.resolve(current, opts.Model(), opts.Effort())
	if err != nil {
		return fail("settings", err)
	}
	if value := opts.PermissionMode(); value != "" {
		mode = value
	}
	if value := opts.Get(contracts.DeepseekHarnessOptionPermissions); value != "" {
		permissions = value
	}
	if err := a.applySessionSettings(sessionID, selection, mode, permissions); err != nil {
		return fail("settings", err)
	}
	a.Mu.Lock()
	a.selection, a.mode, a.permissions = selection, mode, permissions
	a.Mu.Unlock()
	a.sink.UpdateSessionID(sessionID)
	a.sink.PersistSettingsRefresh(agent.CurrentOptions(a.OptionGroups()))
	a.sink.BroadcastStatusActive(sessionID)
	return a, nil
}
