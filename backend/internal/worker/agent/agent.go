package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/terminal"
)

// Control response behavior values (shared protocol between frontend and backend).
const (
	ControlBehaviorAllow = "allow"
	ControlBehaviorDeny  = "deny"
)

// OptionGroupKeyPermissionMode is the key used in AvailableOptionGroup
// to identify the permission-mode option group across all providers.
const OptionGroupKeyPermissionMode = "permissionMode"

// ExitHandler is called when an agent process exits.
// agentID identifies the agent, exitCode is the process exit code,
// and err is non-nil if the process exited with an error.
type ExitHandler func(agentID string, exitCode int, err error)

// Options configures a new ClaudeCodeAgent.
type Options struct {
	AgentID         string
	Model           string
	Effort          string // Effort (low, medium, high, max)
	WorkingDir      string
	ResumeSessionID string                  // If set, uses --resume to resume a previous session
	PermissionMode  string                  // Permission mode to set on startup (default, acceptEdits, plan, bypassPermissions)
	ExtraSettings   map[string]string       // Provider-specific persisted settings (e.g. Codex extras)
	StartupTimeout  time.Duration           // Timeout for the startup handshake (default: 30s)
	Shell           string                  // Default shell path (always set when using shell wrapper)
	LoginShell      bool                    // If true, use interactive+login shell flags
	HomeDir         string                  // User's home directory (for reading Claude Code settings)
	AgentProvider   leapmuxv1.AgentProvider // Coding agent provider (default: CLAUDE_CODE)
}

func (o Options) startupTimeout() time.Duration {
	if o.StartupTimeout > 0 {
		return o.StartupTimeout
	}
	return 30 * time.Second
}

// providerRegistration holds the factory function, default model list,
// option groups, and environment variable keys for a provider.
type providerRegistration struct {
	start         startFunc
	defaultModels []*leapmuxv1.AvailableModel
	optionGroups  []*leapmuxv1.AvailableOptionGroup
	envModelKey   string // e.g. "LEAPMUX_CLAUDE_DEFAULT_MODEL"
	envEffortKey  string // e.g. "LEAPMUX_CLAUDE_DEFAULT_EFFORT"
	binaryName    string // e.g. "claude", "codex"
}

// providerRegistry maps each AgentProvider to its registration.
// Providers register at package init time via registerProvider.
var providerRegistry = map[leapmuxv1.AgentProvider]providerRegistration{}

// registerProvider registers a provider's factory function, default model list,
// option groups, and environment variable keys for overriding defaults.
func registerProvider(
	provider leapmuxv1.AgentProvider,
	start startFunc,
	defaultModels []*leapmuxv1.AvailableModel,
	optionGroups []*leapmuxv1.AvailableOptionGroup,
	envModelKey, envEffortKey string,
	binaryName string,
) {
	providerRegistry[provider] = providerRegistration{
		start:         start,
		defaultModels: defaultModels,
		optionGroups:  optionGroups,
		envModelKey:   envModelKey,
		envEffortKey:  envEffortKey,
		binaryName:    binaryName,
	}
}

// DefaultModel returns the default model ID for a provider, checking the
// provider's environment variable first, then falling back to the model
// marked IsDefault in the registered model list.
func DefaultModel(provider leapmuxv1.AgentProvider) string {
	reg, ok := providerRegistry[provider]
	if !ok {
		return ""
	}
	if reg.envModelKey != "" {
		if env := os.Getenv(reg.envModelKey); env != "" {
			return env
		}
	}
	for _, m := range reg.defaultModels {
		if m.IsDefault {
			return m.Id
		}
	}
	if len(reg.defaultModels) > 0 {
		return reg.defaultModels[0].Id
	}
	return ""
}

// DefaultEffort returns the default effort ID for a provider, checking the
// provider's environment variable first, then falling back to the default
// effort of the default model.
func DefaultEffort(provider leapmuxv1.AgentProvider) string {
	reg, ok := providerRegistry[provider]
	if !ok {
		return ""
	}
	if reg.envEffortKey != "" {
		if env := os.Getenv(reg.envEffortKey); env != "" {
			return env
		}
	}
	defaultModelID := DefaultModel(provider)
	for _, m := range reg.defaultModels {
		if m.Id == defaultModelID && m.DefaultEffort != "" {
			return m.DefaultEffort
		}
	}
	return ""
}

// filterEnv returns a copy of environ with entries matching any of the
// given key names removed. Keys are matched case-insensitively by the
// portion before the first '='.
func filterEnv(environ []string, keys ...string) []string {
	filtered := make([]string, 0, len(environ))
	for _, entry := range environ {
		name, _, _ := strings.Cut(entry, "=")
		skip := false
		for _, k := range keys {
			if strings.EqualFold(name, k) {
				skip = true
				break
			}
		}
		if !skip {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}

// ListAvailableProviders returns providers whose binary is found in the
// user's shell environment. Checks run concurrently to minimize latency
// when login shells are used (each check reads shell profiles).
func ListAvailableProviders(ctx context.Context, shellPath string, useLoginShell bool) []leapmuxv1.AgentProvider {
	type check struct {
		provider leapmuxv1.AgentProvider
		binary   string
	}
	var checks []check
	for provider, reg := range providerRegistry {
		if reg.binaryName != "" {
			checks = append(checks, check{provider, reg.binaryName})
		}
	}

	found := make([]bool, len(checks))
	var wg sync.WaitGroup
	for i, c := range checks {
		wg.Add(1)
		go func(idx int, binary string) {
			defer wg.Done()
			found[idx] = checkBinaryAvailable(ctx, shellPath, useLoginShell, binary)
		}(i, c.binary)
	}
	wg.Wait()

	var result []leapmuxv1.AgentProvider
	for i, c := range checks {
		if found[i] {
			result = append(result, c.provider)
		}
	}
	// Always return at least one provider so the UI has something to show.
	if len(result) == 0 {
		result = append(result, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func checkBinaryAvailable(ctx context.Context, shellPath string, loginShell bool, binaryName string) bool {
	shellName := filepath.Base(shellPath)

	var inner, flag string
	switch {
	case terminal.IsPwsh(shellName):
		inner = fmt.Sprintf("if (Get-Command %s -ErrorAction SilentlyContinue) { exit 0 } else { exit 1 }", pwshQuote(binaryName))
		flag = "-Command"
	case shellName == "nu":
		inner = fmt.Sprintf("if (which %s | is-not-empty) { exit 0 } else { exit 1 }", nuQuote(binaryName))
		flag = "-c"
	case shellName == "tcsh" || shellName == "csh":
		inner = fmt.Sprintf("which %s >& /dev/null", posixQuote(binaryName))
		flag = "-c"
	default:
		inner = fmt.Sprintf("command -v %s >/dev/null 2>&1", posixQuote(binaryName))
		flag = "-c"
	}

	var args []string
	if loginShell {
		args = append(terminal.LoginShellArgs(shellPath), flag, inner)
	} else {
		args = []string{flag, inner}
	}

	cmd := exec.CommandContext(ctx, shellPath, args...)
	cmd.Dir = os.TempDir()
	return cmd.Run() == nil
}
