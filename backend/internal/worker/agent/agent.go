package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/config"
	"github.com/leapmux/leapmux/internal/worker/terminal"
	"github.com/leapmux/leapmux/util/procutil"
)

// Control response behavior values (shared protocol between frontend and backend).
const (
	ControlBehaviorAllow = "allow"
	ControlBehaviorDeny  = "deny"
)

// Tool name constants used in control requests.
const (
	ToolNameAskUserQuestion     = "AskUserQuestion"
	ToolNameEnterPlanMode       = "EnterPlanMode"
	ToolNameExitPlanMode        = "ExitPlanMode"
	ToolNameCodexPlanModePrompt = "CodexPlanModePrompt"
)

// OptionGroupKeyPermissionMode is the key used in AvailableOptionGroup
// to identify the permission-mode option group across all providers.
const OptionGroupKeyPermissionMode = "permissionMode"

// OptionGroupKeyPrimaryAgent is the key used in AvailableOptionGroup
// to identify the primary-agent option group across providers that
// support agent selection (e.g. Kilo, OpenCode).
const OptionGroupKeyPrimaryAgent = "primaryAgent"

// DefaultAPITimeout is the fallback timeout for JSON-RPC requests to the
// agent process, used when no configured value is provided.
const DefaultAPITimeout = time.Duration(config.DefaultAPITimeoutSeconds) * time.Second

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
	APITimeout      time.Duration           // Timeout for JSON-RPC requests (default: 10s)
	Shell           string                  // Default shell path (always set when using shell wrapper)
	LoginShell      bool                    // If true, use interactive+login shell flags
	HomeDir         string                  // User's home directory (for reading Claude Code settings)
	AgentProvider   leapmuxv1.AgentProvider // Coding agent provider (default: CLAUDE_CODE)
}

func (o Options) startupTimeout() time.Duration {
	if o.StartupTimeout > 0 {
		return o.StartupTimeout
	}
	return time.Duration(config.DefaultAgentStartupTimeoutSeconds) * time.Second
}

func (o Options) apiTimeout() time.Duration {
	if o.APITimeout > 0 {
		return o.APITimeout
	}
	return DefaultAPITimeout
}

// providerRegistration holds the factory function, default model list,
// option groups, and environment variable keys for a provider.
type providerRegistration struct {
	start         startFunc
	defaultModels []*leapmuxv1.AvailableModel
	optionGroups  []*leapmuxv1.AvailableOptionGroup
	envModelKey   string   // e.g. "LEAPMUX_CLAUDE_DEFAULT_MODEL"
	envEffortKey  string   // e.g. "LEAPMUX_CLAUDE_DEFAULT_EFFORT"
	binaryNames   []string // preferred first; e.g. {"codex", "codex-x86_64-pc-windows-msvc"}
}

// providerRegistry maps each AgentProvider to its registration.
// Providers register at package init time via registerProvider.
var providerRegistry = map[leapmuxv1.AgentProvider]providerRegistration{}

// registerProvider registers a provider's factory function, default model list,
// option groups, and environment variable keys for overriding defaults.
// binaryNames lists the executable names to probe (first entry is preferred).
func registerProvider(
	provider leapmuxv1.AgentProvider,
	start startFunc,
	defaultModels []*leapmuxv1.AvailableModel,
	optionGroups []*leapmuxv1.AvailableOptionGroup,
	envModelKey, envEffortKey string,
	binaryNames ...string,
) {
	providerRegistry[provider] = providerRegistration{
		start:         start,
		defaultModels: defaultModels,
		optionGroups:  optionGroups,
		envModelKey:   envModelKey,
		envEffortKey:  envEffortKey,
		binaryNames:   binaryNames,
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

// DefaultEffortForModel returns the default effort ID for the given model
// within a provider. It checks the provider's environment variable first,
// then the model's DefaultEffort from the registry. If modelID is empty
// or unknown, it falls back to the default model's DefaultEffort.
func DefaultEffortForModel(provider leapmuxv1.AgentProvider, modelID string) string {
	reg, ok := providerRegistry[provider]
	if !ok {
		return ""
	}
	if reg.envEffortKey != "" {
		if env := os.Getenv(reg.envEffortKey); env != "" {
			return env
		}
	}
	lookup := func(id string) string {
		for _, m := range reg.defaultModels {
			if m.Id == id && m.DefaultEffort != "" {
				return m.DefaultEffort
			}
		}
		return ""
	}
	if modelID != "" {
		if e := lookup(modelID); e != "" {
			return e
		}
	}
	return lookup(DefaultModel(provider))
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
		binaries []string
	}
	var checks []check
	for provider, reg := range providerRegistry {
		if len(reg.binaryNames) > 0 {
			checks = append(checks, check{provider, reg.binaryNames})
		}
	}

	found := make([]bool, len(checks))
	var wg sync.WaitGroup
	for i, c := range checks {
		wg.Add(1)
		go func(idx int, binaries []string) {
			defer wg.Done()
			for _, b := range binaries {
				if checkBinaryAvailable(ctx, shellPath, useLoginShell, b) {
					found[idx] = true
					return
				}
			}
		}(i, c.binaries)
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

// resolveBinaryName returns the first binary from candidates that is
// available in the user's shell environment. If none are found, the first
// candidate is returned so that invocation produces a meaningful
// "command not found" error rather than silently picking an alias.
func resolveBinaryName(ctx context.Context, shellPath string, loginShell bool, candidates []string) string {
	for _, c := range candidates {
		if checkBinaryAvailable(ctx, shellPath, loginShell, c) {
			return c
		}
	}
	return candidates[0]
}

// binaryAvailabilityCache memoizes the result of a login-shell binary probe.
// Each probe spawns a (possibly login) shell that sources user profiles —
// commonly hundreds of milliseconds — so repeat calls from
// ListAvailableProviders and resolveBinaryName share results for the
// worker's lifetime. Installed binaries don't appear or disappear within
// a session, so no TTL is needed.
var (
	binaryAvailabilityCache   sync.Map // binaryAvailabilityKey -> bool
	binaryAvailabilitySingles sync.Map // binaryAvailabilityKey -> *sync.Once
)

type binaryAvailabilityKey struct {
	shellPath  string
	loginShell bool
	binaryName string
}

func checkBinaryAvailable(ctx context.Context, shellPath string, loginShell bool, binaryName string) bool {
	key := binaryAvailabilityKey{shellPath, loginShell, binaryName}
	if v, ok := binaryAvailabilityCache.Load(key); ok {
		return v.(bool)
	}
	onceAny, _ := binaryAvailabilitySingles.LoadOrStore(key, &sync.Once{})
	onceAny.(*sync.Once).Do(func() {
		binaryAvailabilityCache.Store(key, probeBinary(ctx, shellPath, loginShell, binaryName))
	})
	v, _ := binaryAvailabilityCache.Load(key)
	return v.(bool)
}

func probeBinary(ctx context.Context, shellPath string, loginShell bool, binaryName string) bool {
	shellName := terminal.ShellBaseName(shellPath)

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
	procutil.HideConsoleWindow(cmd)
	return cmd.Run() == nil
}
