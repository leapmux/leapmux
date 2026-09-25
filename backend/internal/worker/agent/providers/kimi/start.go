package kimi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/coder/quartz"

	"github.com/leapmux/leapmux/generated/contracts"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

var _ agent.StartFunc = Start

// kimiVersionProbeTimeout limits `kimi --version`, which starts a login shell
// and a Node process and prints one line.
const kimiVersionProbeTimeout = 60 * time.Second

// Start launches `kimi web`, connects to it, and opens the agent's session.
func Start(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (agent.Agent, error) {
	spec, err := providerkit.ResolveLaunch(ctx, opts, Registration())
	if err != nil {
		return nil, err
	}
	timeout := opts.EffectiveStartupTimeout()
	if _, err := probeKimiVersion(ctx, opts, spec, min(timeout, kimiVersionProbeTimeout)); err != nil {
		return nil, err
	}
	return startServer(ctx, opts, sink, spec)
}

// startServer runs the server the version check approved.
func startServer(ctx context.Context, opts agent.Options, sink agent.ProviderServices, spec launch.Spec) (agent.Agent, error) {
	ctx, cancel := context.WithCancel(ctx)
	cmd, preambleDelimiter, metaPrefix := launch.Wrap(ctx, launch.WrapSpec{
		Shell:      opts.Shell,
		LoginShell: opts.LoginShell,
		Launch:     spec,
		BaseArgs:   kimiServerArgs,
		WorkingDir: opts.WorkingDir,
	})
	cmd.Env = providerkit.FinalizeAgentEnv(cmd.Environ(), opts)
	stdin, stdout, stderrPipe, err := providerkit.SetupProcessPipes(cmd, cancel)
	if err != nil {
		return nil, err
	}

	a := &Agent{
		Process:          providerkit.NewProcess(opts, "kimi", cmd, stdin, ctx, cancel, preambleDelimiter, metaPrefix),
		sink:             agent.NewModelProgressResetSink(sink),
		workingDir:       opts.WorkingDir,
		clock:            quartz.NewReal(),
		descendantGroups: kimiDescendantGroups,
	}
	if err := a.StartCmd(cmd, cancel); err != nil {
		return nil, err
	}
	a.DrainStderr(stderrPipe)

	ready := newKimiReadyReader(a.AgentID())
	go a.ReadLines(agent.NewStdoutScanner(stdout), ready.observe)

	cleanup := func() {
		a.Stop()
		_ = a.Wait()
	}
	timeout := opts.EffectiveStartupTimeout()
	address, err := ready.waiter.Wait(ctx, a.ProcessDone(), timeout)
	if err != nil {
		cleanup()
		if errors.Is(err, providerkit.ErrServerExited) {
			return nil, a.FormatStartupError("server start", fmt.Errorf("%w; the program named `kimi` must be Kimi Code 2.0 or later", err))
		}
		return nil, a.FormatStartupError("server start", err)
	}
	if err := a.connect(ctx, address, ready.token(), opts, timeout); err != nil {
		cleanup()
		return nil, a.FormatStartupError("server connection", err)
	}
	if err := a.openStartupSession(opts, timeout); err != nil {
		cleanup()
		if opts.ResumeSessionID != "" {
			return nil, providerkit.ResumeFailedError(opts.ResumeSessionID, err)
		}
		return nil, a.FormatStartupError("session open", err)
	}

	a.Mu.Lock()
	sessionID := a.sessionID
	a.Mu.Unlock()
	a.sink.UpdateSessionID(sessionID)
	a.sink.PersistSettingsRefresh(agent.CurrentOptions(a.OptionGroups()))
	a.sink.BroadcastStatusActive(sessionID)
	return a, nil
}

// connect builds the REST client and the event stream, and reads what the
// server offers.
func (a *Agent) connect(ctx context.Context, address, token string, opts agent.Options, timeout time.Duration) error {
	if token == "" {
		return errors.New("the Kimi Code server stated no access token")
	}
	endpoint, err := providerkit.NewHTTPEndpoint(address, providerkit.BearerAuth(token))
	if err != nil {
		return err
	}
	a.endpoint = endpoint
	a.api = &kimiClient{endpoint: endpoint, timeout: opts.EffectiveAPITimeout()}

	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var meta struct {
		ServerVersion string `json:"server_version"`
		Features      []struct {
			Name  string `json:"name"`
			State string `json:"state"`
		} `json:"features"`
	}
	if err := a.api.get(reqCtx, kimiRouteMeta, &meta); err != nil {
		return fmt.Errorf("read the server description: %w", err)
	}
	if _, err := checkKimiVersion(kimiVersionFromServer, meta.ServerVersion); err != nil {
		return err
	}
	a.features = make(map[string]bool, len(meta.Features))
	for _, feature := range meta.Features {
		if strings.EqualFold(feature.State, "active") {
			a.features[feature.Name] = true
		}
	}

	a.stream = newKimiStream(endpoint, a.AgentID(), a.clock, a.handleFrame, a.resyncSession)
	if err := a.stream.start(a.Context(), timeout); err != nil {
		return err
	}
	catalog, err := a.loadKimiCatalog(reqCtx)
	if err != nil {
		return fmt.Errorf("read the model catalog: %w", err)
	}
	a.Mu.Lock()
	a.catalog = catalog
	a.Mu.Unlock()
	return nil
}

// errKimiNoModel refuses a start with no model to bind: the server binds none to
// a new session by itself, and the first prompt would fail.
var errKimiNoModel = errors.New("the Kimi Code server reports no model; run `kimi login`, or add a model to the `config.toml` in your Kimi Code data directory")

// openStartupSession opens the session the launch asks for: the stored one it
// resumes, or a new one with the launch's settings.
func (a *Agent) openStartupSession(opts agent.Options, timeout time.Duration) error {
	a.Mu.Lock()
	catalog := a.catalog
	a.Mu.Unlock()
	wanted := kimiSettingsFromOptions(kimiSettings{permission: contracts.KimiDefaultMode, effort: agent.EffortAuto}, opts.Options)
	ctx, cancel := context.WithTimeout(a.Context(), timeout)
	defer cancel()
	a.Mu.Lock()
	a.attachedAt = a.clock.Now()
	a.Mu.Unlock()

	if opts.ResumeSessionID != "" {
		// A resumed session keeps the model it ran on unless the launch asks for
		// another one the catalog lists.
		if !catalog.has(wanted.model) {
			wanted.model = ""
		}
		a.Mu.Lock()
		a.sessionID = opts.ResumeSessionID
		a.settings.effort = wanted.effort
		a.Mu.Unlock()
		status, err := a.resumeSession(ctx, opts.ResumeSessionID, wanted)
		if err != nil {
			a.Mu.Lock()
			a.sessionID = ""
			a.Mu.Unlock()
			return err
		}
		a.applyStatus(status)
		a.readGoal(ctx, opts.ResumeSessionID)
		return nil
	}

	wanted.model = catalog.launchModel(opts.Model())
	if wanted.model == "" {
		return errKimiNoModel
	}
	a.Mu.Lock()
	a.settings = wanted
	a.Mu.Unlock()
	sessionID, err := a.createSession(ctx, wanted)
	if err != nil {
		return err
	}
	a.Mu.Lock()
	a.sessionID = sessionID
	a.Mu.Unlock()
	status, err := a.readStatus(ctx, sessionID)
	if err != nil {
		return err
	}
	a.applyStatus(status)
	return nil
}

// kimiReadyReader reads the server's stdout: the ready line, which states the
// address and the token, and the log lines, which go to the worker's log.
type kimiReadyReader struct {
	agentID string
	waiter  *providerkit.ListenWaiter

	mu          sync.Mutex
	accessToken string
}

func newKimiReadyReader(agentID string) *kimiReadyReader {
	return &kimiReadyReader{agentID: agentID, waiter: providerkit.NewListenWaiter(kimiAddressLine)}
}

// observe reads one stdout line. The token is recorded BEFORE the waiter sees
// the line, so a caller that Wait released always reads it.
func (r *kimiReadyReader) observe(line []byte) {
	if match := kimiReadyLine.FindSubmatch(line); match != nil {
		r.mu.Lock()
		if r.accessToken == "" {
			r.accessToken = string(match[2])
		}
		r.mu.Unlock()
		r.waiter.Observe(line)
		return
	}
	slog.Debug("kimi server output", "agent_id", r.agentID, "line", string(line))
}

func (r *kimiReadyReader) token() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.accessToken
}
