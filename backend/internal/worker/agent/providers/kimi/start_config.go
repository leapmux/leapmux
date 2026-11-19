package kimi

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// The native server emits startup diagnostics before it accepts subscriptions.
// Its read-only validator supplies the same diagnostics without opening a session.
func (a *Agent) persistStartupConfigDiagnostics(ctx context.Context, opts agent.Options, spec launch.Spec) {
	output, err := providerkit.RunCLI(ctx, providerkit.CLIRun{
		Locator: launch.Custom(func(context.Context, string, bool) (launch.Spec, launch.Resolution) { return spec, launch.Found }),
		Label:   "Kimi Code", Shell: opts.Shell, LoginShell: opts.LoginShell,
		Args: []string{"doctor", "config"}, WorkingDir: opts.WorkingDir,
		SetEnv: []string{kimiNoAutoUpdateEnv}, Timeout: opts.EffectiveStartupTimeout(), MaxOutput: 1 << 20,
		Env: func(env []string) []string { return providerkit.FinalizeAgentEnv(env, opts) },
	})
	if err != nil {
		slog.Warn("read Kimi startup configuration diagnostics", "agent_id", a.AgentID(), "error", err)
		return
	}
	var warnings []map[string]string
	configResult := false
	for _, line := range strings.Split(string(output), "\n") {
		if strings.HasPrefix(line, "  ") && configResult {
			if message := strings.TrimSpace(line); message != "" {
				warnings = append(warnings, map[string]string{"message": message})
			}
		} else {
			configResult = strings.HasPrefix(line, "OK config.toml ")
		}
	}
	if len(warnings) == 0 {
		return
	}
	payload, err := json.Marshal(map[string]any{"type": contracts.KimiEventConfigWarning, "warnings": warnings})
	if err != nil {
		slog.Error("encode Kimi startup configuration diagnostics", "agent_id", a.AgentID(), "error", err)
		return
	}
	a.dispatchMu.Lock()
	defer a.finishDispatch()
	a.handleGlobalFrame(kimiFrame{
		Type: contracts.KimiEventConfigWarning, SessionID: kimiGlobalSessionID,
		Timestamp: a.clock.Now().Format(time.RFC3339Nano), Payload: payload,
	})
}
