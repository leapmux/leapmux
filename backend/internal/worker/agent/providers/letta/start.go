package letta

import (
	"context"
	"errors"
	"fmt"

	"github.com/coder/quartz"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

var _ agent.StartFunc = Start

// Start launches `letta server --listen`, waits for the WebSocket ready line,
// and opens the agent's conversation.
func Start(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (agent.Agent, error) {
	spec, err := providerkit.ResolveLaunch(ctx, opts, Registration())
	if err != nil {
		return nil, err
	}
	return startServer(ctx, opts, sink, spec)
}

// startServer runs the App Server and completes the protocol_v2 handshake.
func startServer(ctx context.Context, opts agent.Options, sink agent.ProviderServices, spec launch.Spec) (agent.Agent, error) {
	ctx, cancel := context.WithCancel(ctx)
	cmd, preambleDelimiter, metaPrefix := launch.Wrap(ctx, launch.WrapSpec{
		Shell:      opts.Shell,
		LoginShell: opts.LoginShell,
		Launch:     spec,
		BaseArgs:   lettaServerArgs,
		WorkingDir: opts.WorkingDir,
	})
	cmd.Env = providerkit.FinalizeAgentEnv(cmd.Environ(), opts)
	stdin, stdout, stderrPipe, err := providerkit.SetupProcessPipes(cmd, cancel)
	if err != nil {
		cancel()
		return nil, err
	}

	a := &Agent{
		Process:    providerkit.NewProcess(opts, "letta", cmd, stdin, ctx, cancel, preambleDelimiter, metaPrefix),
		sink:       agent.NewModelProgressResetSink(sink),
		workingDir: opts.WorkingDir,
		clock:      quartz.NewReal(),
	}
	if err := a.StartCmd(cmd, cancel); err != nil {
		cancel()
		return nil, err
	}
	a.DrainStderr(stderrPipe)

	ready := newLettaReadyReader()
	go a.ReadLines(agent.NewStdoutScanner(stdout), func(line []byte) {
		if ready.observe(line) {
			return
		}
		a.handleFrame(line)
	})

	cleanup := func() {
		a.Stop()
		_ = a.Wait()
	}
	timeout := opts.EffectiveStartupTimeout()
	url, err := ready.waiter.Wait(ctx, a.ProcessDone(), timeout)
	if err != nil {
		cleanup()
		if err == providerkit.ErrServerExited {
			return nil, a.FormatStartupError("server start", fmt.Errorf("%w; the program named `letta` must be Letta Code 0.33 or later", err))
		}
		return nil, a.FormatStartupError("server start", err)
	}
	a.wsURL = url

	conn, err := dial(ctx, url)
	if err != nil {
		cleanup()
		return nil, a.FormatStartupError("websocket connect", err)
	}
	a.ws = conn
	go a.readLoop(ctx, conn)

	if err := a.openConversation(opts); err != nil {
		cleanup()
		return nil, a.FormatStartupError("runtime_start", err)
	}
	return a, nil
}

// lettaReadyReader observes the WebSocket ready line.
type lettaReadyReader struct {
	waiter *providerkit.ListenWaiter
}

// newLettaReadyReader builds the ready-line reader.
func newLettaReadyReader() *lettaReadyReader {
	return &lettaReadyReader{
		waiter: providerkit.NewListenWaiter(lettaWebSocketLine),
	}
}

// observe feeds one stdout line to the waiter.
func (r *lettaReadyReader) observe(line []byte) bool {
	return r.waiter.Observe(line)
}

// openConversation sends runtime_start and WAITS for the response that states
// the runtime identity.
//
// The wait is load-bearing. An `input` frame sent before the identity exists
// carries an empty runtime scope, and the App Server drops that input without
// an error: no `input_accepted`, no `loop_error`, nothing. Start must not
// return an agent before then, because the worker dispatches queued user input
// the moment it does -- 98ms in the E2E, 365ms before the response arrived.
func (a *Agent) openConversation(opts agent.Options) error {
	mode := lettaModeFor(opts.PermissionMode())
	// runtime_start puts its fields at the top level, not under a payload, and
	// the discriminator is `type`, not `kind`. A fresh agent is created through
	// `create_agent`; `agent_id` alone fails with 401 because the agent does not
	// exist yet. The response carries the real agent and conversation ids.
	model := opts.Model()
	if model == "" {
		model = defaultModels[0].Id
	}
	// Install the waiter BEFORE the command leaves: adoptRuntime runs on the
	// readLoop goroutine and can settle the moment the response arrives.
	waiter := newRuntimeWaiter()
	a.Mu.Lock()
	a.runtimeReady = waiter
	a.settings.permissionMode = mode
	a.settings.model = opts.Model()
	a.settings.reasoningLevel = opts.Effort()
	a.Mu.Unlock()

	cmd := newLettaCommand("runtime_start", "rs-1")
	cmd.CreateAgent = map[string]any{
		"body": map[string]any{
			"name":   "leapmux-agent",
			"model":  model,
			"system": "You are a helpful assistant.",
		},
	}
	cmd.CreateConversation = map[string]any{}
	cmd.Mode = mode
	if err := a.sendCommand(cmd); err != nil {
		return err
	}
	if err := waiter.wait(a.Context(), a.ProcessDone(), opts.EffectiveStartupTimeout()); err != nil {
		return err
	}
	a.Mu.Lock()
	agentID := a.agentID
	conversationID := a.conversationID
	a.Mu.Unlock()
	if agentID == "" || conversationID == "" {
		return errors.New("runtime_start named no agent and conversation")
	}
	return nil
}

// lettaModeFor maps LeapMux's permission mode onto runtime_start.mode.
func lettaModeFor(mode string) string {
	switch mode {
	case "acceptEdits":
		return "acceptEdits"
	case "unrestricted":
		return "unrestricted"
	case "strict":
		return "strict"
	default:
		return "standard"
	}
}
