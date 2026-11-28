package amp

import (
	"context"
	"fmt"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// createReplyThread makes the first input's native session available before
// dispatch. Amp's stream prints its init only after inference has started.
// The caller holds sendMu and has already checked the workspace.
func (a *Agent) createReplyThread(config threadConfiguration) (string, string, error) {
	var threadID, settingsPath string
	var err error
	if a.createThreadFn != nil {
		threadID, err = a.createThreadFn(a.ctx, config)
	} else {
		threadID, settingsPath, err = a.runCreateThread(config)
	}
	if err != nil {
		return "", "", err
	}
	a.settingsMu.Lock()
	defer a.settingsMu.Unlock()
	a.mu.Lock()
	if a.stopped {
		a.mu.Unlock()
		return "", "", errAgentStopped
	}
	a.threadID = threadID
	a.awaitThreadInit = true
	a.modeLocked = true
	a.agentMode, a.fastMode = config.mode, strconv.FormatBool(config.fast)
	a.mu.Unlock()
	a.sink.UpdateSessionID(threadID)
	a.sink.PersistSettingsRefresh(agent.CurrentOptions(a.OptionGroups()))
	a.sink.BroadcastStatusActive(threadID)
	return threadID, settingsPath, nil
}

func (a *Agent) runCreateThread(config threadConfiguration) (string, string, error) {
	settingsPath, err := a.launch.writeSettings(a.userSettingsFile())
	if err != nil {
		return "", "", err
	}
	args := []string{"threads", "new", "--mode", config.mode, "--settings-file", settingsPath}
	if config.fast {
		args = append(args, "--fast")
	}
	output, err := a.runCLI(a.ctx, args, a.launch.opts.EffectiveAPITimeout(), 8<<10)
	if err != nil {
		return "", "", fmt.Errorf("create the Amp thread: %w", err)
	}
	threadID, err := threadIDFromCreateOutput(output)
	if err != nil {
		return "", "", err
	}
	return threadID, settingsPath, nil
}

// runCLI runs one short-lived Amp CLI subcommand with the resolved launch spec
// and environment shared by conversation processes.
func (a *Agent) runCLI(ctx context.Context, args []string, timeout time.Duration, maxOutput int) ([]byte, error) {
	opts := a.launch.opts
	locator := ampLocator
	if a.launch.spec.Program != "" {
		spec := a.launch.spec
		locator = launch.Custom(func(context.Context, string, bool) (launch.Spec, launch.Resolution) {
			return spec, launch.Found
		})
	}
	return providerkit.RunCLI(ctx, providerkit.CLIRun{
		Locator: locator, Label: "Amp", Shell: opts.Shell, LoginShell: opts.LoginShell,
		Args: args, WorkingDir: opts.WorkingDir, StripEnvKeys: ampIdentityEnvKeys,
		SetEnv: a.launch.setEnv(), Env: a.launch.processEnv,
		Timeout: timeout, MaxOutput: maxOutput,
	})
}

func threadIDFromCreateOutput(output []byte) (string, error) {
	link, err := url.Parse(strings.TrimSpace(string(output)))
	if err == nil && link.Host != "" && link.User == nil && (link.Scheme == "https" || link.Scheme == "http") &&
		link.RawQuery == "" && !link.ForceQuery && link.Fragment == "" && path.Base(path.Dir(link.Path)) == "threads" {
		threadID := path.Base(link.Path)
		if raw, ok := strings.CutPrefix(threadID, "T-"); ok {
			if parsed, err := uuid.Parse(raw); err == nil && parsed != uuid.Nil && parsed.String() == raw {
				return threadID, nil
			}
		}
	}
	return "", fmt.Errorf("the Amp CLI returned no valid thread URL")
}
