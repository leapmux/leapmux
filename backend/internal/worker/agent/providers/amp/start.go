package amp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/coder/quartz"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/agentdir"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

var _ agent.StartFunc = Start

// helperSpecFileName is the helper spec inside the agent's directory.
const helperSpecFileName = "helper.json"

// Start prepares an Amp agent. It starts no `amp` process: the first message
// starts one (see Agent).
//
// What it does set up is what every process of the agent shares: the program
// Amp's delegate rule runs, a directory that the worker owns for this agent,
// the permission bridge, and the helper spec that points the program at the
// bridge. Start takes a resume handle as the thread to continue. Amp checks
// the thread when the first process starts, and a thread that it cannot find
// fails that turn with the reason.
func Start(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (agent.Agent, error) {
	spec, err := providerkit.ResolveLaunch(ctx, opts, Registration())
	if err != nil {
		return nil, err
	}
	program, err := helperProgram()
	if err != nil {
		return nil, err
	}
	// The directory is private to the owner. It holds the bridge's socket, the
	// bridge secret and a copy of the user's Amp settings (see agentDirSpec).
	dir, err := opts.AgentDirs.New(ctx, agentDirSpec())
	if err != nil {
		return nil, fmt.Errorf("prepare the Amp permission bridge: %w", err)
	}
	bridge, err := newPermissionBridge(dir.Path())
	if err != nil {
		removeAgentDir(opts.AgentID, dir)
		return nil, err
	}
	config, err := json.Marshal(helperConfig{Endpoint: bridge.endpoint(), Secret: bridge.secretText()})
	if err != nil {
		bridge.close(errAgentStopped)
		removeAgentDir(opts.AgentID, dir)
		return nil, fmt.Errorf("encode the Amp helper configuration: %w", err)
	}
	helperEnv, err := agent.WriteHelperSpec(filepath.Join(dir.Path(), helperSpecFileName), agent.HelperSpec{
		Provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_AMP.String(),
		Helper:   helperPermission,
		Config:   config,
	})
	if err != nil {
		bridge.close(errAgentStopped)
		removeAgentDir(opts.AgentID, dir)
		return nil, err
	}

	home := opts.HomeDir
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	a := newAgent(opts, agent.NewModelProgressResetSink(sink), launchConfig{
		opts:          opts,
		spec:          spec,
		helperEnv:     helperEnv,
		helperProgram: program,
		getenv:        os.Getenv,
		home:          home,
		shellEnv:      shellEnvOf(opts),
	}, bridge, dir, quartz.NewReal())

	if opts.ResumeSessionID != "" {
		a.sink.UpdateSessionID(opts.ResumeSessionID)
	}
	a.sink.BroadcastStatusActive(opts.ResumeSessionID)
	return a, nil
}

// shellEnvOf reads variables in the environment that the shell of opts sets
// up, the shell that starts every `amp` process of the agent.
func shellEnvOf(opts agent.Options) func(ctx context.Context, names []string) (map[string]string, launch.ProbeResult) {
	return func(ctx context.Context, names []string) (map[string]string, launch.ProbeResult) {
		return launch.ShellEnv(ctx, opts.Shell, opts.LoginShell, names)
	}
}

// newAgent builds an agent from its parts and starts serving its bridge. A
// resumed thread keeps the mode of its first message, so the mode group of a
// resumed agent stays read-only from the start.
//
// The agent owns dir, and its stop removes it. config takes the directory's
// path from dir, so each process and the stop use one directory.
func newAgent(opts agent.Options, sink agent.ProviderServices, config launchConfig, bridge *permissionBridge, dir *agentdir.Dir, clock quartz.Clock) *Agent {
	ctx, cancel := context.WithCancel(context.Background())
	config.stateDir = dir.Path()
	a := &Agent{
		agentID:        opts.AgentID,
		sink:           sink,
		launch:         config,
		bridge:         bridge,
		dir:            dir,
		clock:          clock,
		ctx:            ctx,
		cancel:         cancel,
		threadID:       opts.ResumeSessionID,
		agentMode:      launchAgentMode(opts),
		modeLocked:     opts.ResumeSessionID != "",
		permissionMode: launchPermissionMode(opts),
		tools:          make(map[string]*openTool),
		shells:         make(map[int]string),
		done:           make(chan struct{}),
	}
	bridge.serve(a.decidePermission, sink.CancelControlRequest)
	return a
}

// helperProgram is the program Amp's delegate rule runs: the executable of this
// worker, which runs the permission helper when it starts with no argument.
//
// Amp expands `~` and `%NAME%` in the program path before it starts it, and on
// Windows it then turns the path into a URI path (`/C:/...`) that no program
// has. A path that holds either character therefore cannot pass through, and
// the agent refuses to start rather than leave every tool call refused.
func helperProgram() (string, error) {
	program, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("find the LeapMux executable for the Amp permission helper: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(program); err == nil {
		program = resolved
	}
	return checkHelperProgram(program)
}

// checkHelperProgram refuses a program path that Amp's delegate rule would
// change before it starts the program.
func checkHelperProgram(program string) (string, error) {
	if strings.ContainsAny(program, "~%") {
		return "", fmt.Errorf("the LeapMux executable %s holds `~` or `%%`, which Amp's permission rule expands. Install LeapMux under a path without them", program)
	}
	return program, nil
}
