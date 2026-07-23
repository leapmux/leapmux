package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"sync"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/config"
	"github.com/leapmux/leapmux/internal/worker/terminal"
	"github.com/leapmux/leapmux/util/procutil"
)

// Control response behavior values (shared protocol between frontend and backend).
const (
	ControlBehaviorAllow = "allow"
	ControlBehaviorDeny  = "deny"
)

// ControlRejectedByUserMessage is the placeholder reject message the frontend emits when
// the user declines a control request WITHOUT typing a reason (buildDenyResponse in
// frontend utils/controlResponse.ts). The backend treats it as "no feedback" -- it is not
// shown as the user's answer -- so every deny-with-feedback path compares against this one
// constant instead of re-spelling the literal (which must stay in lockstep with the
// frontend producer).
const ControlRejectedByUserMessage = "Rejected by user."

// Tool name constants used in control requests.
const (
	ToolNameAskUserQuestion     = "AskUserQuestion"
	ToolNameEnterPlanMode       = "EnterPlanMode"
	ToolNameExitPlanMode        = "ExitPlanMode"
	ToolNameCodexPlanModePrompt = "CodexPlanModePrompt"
)

// The well-known option-group id constants (OptionIDPermissionMode,
// OptionIDPrimaryAgent, OptionIDModel, OptionIDEffort) live in options.go.

// DefaultAPITimeout is the fallback timeout for JSON-RPC requests to the
// agent process, used when no configured value is provided.
const DefaultAPITimeout = time.Duration(config.DefaultAPITimeoutSeconds) * time.Second

// EffortAuto is the Leapmux-side sentinel meaning "let the CLI pick its own
// default reasoning effort". When an agent's Effort is this value, the
// provider layer omits the CLI flag / wire field entirely so older CLIs
// that don't recognize newer effort names (e.g. "xhigh") still work.
const EffortAuto = "auto"

// DefaultModelSentinel is the model id meaning "let the CLI pick the account's
// own default model". Claude Code reports it as a distinct entry in the
// initialize response (displayName "Default (recommended)"); selecting it makes
// the provider omit --model at launch (and relaunch on a live switch) so the CLI
// resolves it to the concrete model, which get_settings then reports back -- the
// model-side analogue of EffortAuto.
const DefaultModelSentinel = "default"

// UsesAccountDefaultModel reports whether a (normalized) model id means "no concrete
// model -- let the CLI pick the account default": an empty id or the
// DefaultModelSentinel. Centralizing the two-clause check keeps the "omit --model"
// decision identical at every site and makes a forgotten sentinel clause a missing
// call rather than a silent wrong branch.
func UsesAccountDefaultModel(model string) bool {
	return model == "" || model == DefaultModelSentinel
}

// EffortUltracode is LeapMux's internal name for the CLI's xhigh+ultracode combo.
// At the provider wire boundary it maps to {effortLevel:"xhigh", ultracode:true};
// the CLI's --effort launch flag does not accept it.
const EffortUltracode = "ultracode"

// EffortXHigh is the "xhigh" effort level. It is also the launch/wire base for
// the ultracode combo (which layers the `ultracode` boolean on top of xhigh),
// so it is a load-bearing value shared by the encode path (ultracodeFlagSettings,
// buildModelEffortArgs) and the decode path (effortFromApplied). Naming it
// once keeps those sites from drifting to inconsistent literals.
const EffortXHigh = "xhigh"

// EffortHigh is the "high" effort level. It is the universal-safe fallback every
// model supports, so resolveEffort downgrades any unsupported
// effort to it. Like EffortXHigh it is load-bearing (the fallback target and the
// Sonnet/Haiku catalog default), so naming it once keeps those sites from
// drifting to inconsistent literals.
const EffortHigh = "high"

// ExitHandler is called when an agent process exits.
// agentID identifies the agent, exitCode is the process exit code,
// and err is non-nil if the process exited with an error.
type ExitHandler func(agentID string, exitCode int, err error)

// Options configures a new ClaudeCodeAgent.
type Options struct {
	AgentID         string
	WorkingDir      string
	ResumeSessionID string // If set, uses --resume to resume a previous session
	// Options is the COMPLETE resolved option set keyed by option-group id
	// (model, effort, permissionMode, primaryAgent, and every provider option).
	// It is the single source of truth -- there are no shadow scalar fields. Read
	// a specific axis via Model()/Effort()/PermissionMode()/Get(id). It is the same
	// optionmap.Map type the service layer (OptionMap) and the Agent interface use,
	// so a launch option set flows to/from those boundaries without a conversion.
	Options        optionmap.Map
	StartupTimeout time.Duration           // Timeout for the startup handshake (default: 5m)
	APITimeout     time.Duration           // Timeout for JSON-RPC requests (default: 10s)
	Shell          string                  // Default shell path (always set when using shell wrapper)
	LoginShell     bool                    // If true, use interactive+login shell flags
	HomeDir        string                  // User's home directory (for reading Claude Code settings)
	AgentProvider  leapmuxv1.AgentProvider // Coding agent provider (default: CLAUDE_CODE)
	// ExtraEnv is appended verbatim to the spawned process's
	// environment after the provider-specific env-var setup. The
	// service.Service populates this with LEAPMUX_REMOTE_* so the
	// running agent can drive the worker via the leapmux remote CLI.
	ExtraEnv []string
}

// Get returns the resolved value of an option-group id, or "" if absent. The
// Model/Effort/PermissionMode helpers are by-id readers, not assignable fields --
// the option map remains the single representation.
func (o Options) Get(id string) string   { return o.Options[id] }
func (o Options) Model() string          { return o.Options[OptionIDModel] }
func (o Options) Effort() string         { return o.Options[OptionIDEffort] }
func (o Options) PermissionMode() string { return o.Options[OptionIDPermissionMode] }

// FinalizeAgentEnv and the agent-harness env scrub it applies live in env.go.

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

// agentFactoryEntry holds the factory function, default model list,
// option groups, and environment variable keys for a provider.
type agentFactoryEntry struct {
	start         startFunc
	defaultModels []*ModelInfo
	optionGroups  []*leapmuxv1.AvailableOptionGroup
	// modelSubGroups builds the model-dependent sub_groups carried on each model
	// option (defaults to effortSubGroups; Claude overrides it to also emit the
	// per-model extended-thinking group). Used by the manager's static fallback
	// so a restarting agent's groups still carry every model's dependent groups.
	modelSubGroups modelSubGroupsFunc
	// modelIDNormalizer canonicalizes a model id into the provider's alias space (e.g.
	// Claude's "claude-opus-4-8" -> "opus[1m]", Cursor's "default[]" -> "auto"). nil leaves
	// the id unchanged. NormalizeModelID (the offline-label path) and the live agent's
	// acpBase.modelIDNormalizer both source it here, so the two can't drift.
	modelIDNormalizer func(string) string
	// additionalOptionIDs lists the option-group ids this provider can carry BEYOND the
	// universal "model" axis and the static optionGroups templates (the secondary
	// permission-mode/primary-agent axis): the well-known "effort" axis where the
	// provider has one, Codex's sandbox/network/collaboration/service-tier options,
	// Pi's pi_provider, and the server-driven ACP config options each family exposes
	// (Copilot's reasoning_effort/allow_all, Goose's thinking_effort/provider).
	// Together with "model" and optionGroups they form KnownOptionIDs -- the static
	// allowlist UpdateAgentSettings validates an incoming options map against, so a
	// foreign axis the provider can't apply is dropped instead of persisting a
	// phantom key and emitting a misleading settings_changed notification.
	additionalOptionIDs []string
	// persistedOnlyOptionIDs lists option ids the provider persists but NEVER
	// surfaces as a group -- Pi's pi_provider (the underlying LLM provider behind a model
	// id). They are a SUBSET of the known ids (folded into KnownOptionIDs below) but, unlike
	// every other axis, their absence from a confirmed catalog is by design, not orphaning:
	// confirmedOptions preserves them from the base instead of reconciling them away.
	persistedOnlyOptionIDs []string
	// providerOptionDefaults seeds provider-specific option values (id->default) into a fresh
	// agent's launch options beyond model/effort -- e.g. Codex's sandbox / network /
	// collaboration / service-tier defaults. resolveProviderDefaults stamps these
	// uniformly for every provider, so a new provider declares its seeds here rather
	// than the service layer growing a per-provider branch.
	providerOptionDefaults map[string]string
	envModelKey            string   // e.g. "LEAPMUX_CLAUDE_DEFAULT_MODEL"
	envEffortKey           string   // e.g. "LEAPMUX_CLAUDE_DEFAULT_EFFORT"
	binaryNames            []string // preferred first; e.g. {"codex", "codex-x86_64-pc-windows-msvc"}
}

// agentFactoryRegistry maps each AgentProvider to its registration.
// Providers register at package init time via registerAgentFactory.
var agentFactoryRegistry = map[leapmuxv1.AgentProvider]agentFactoryEntry{}

// registerAgentFactory registers a provider's factory function, default model list,
// option groups, and environment variable keys for overriding defaults.
// binaryNames lists the executable names to probe (first entry is preferred).
func registerAgentFactory(
	provider leapmuxv1.AgentProvider,
	start startFunc,
	defaultModels []*ModelInfo,
	optionGroups []*leapmuxv1.AvailableOptionGroup,
	envModelKey, envEffortKey string,
	binaryNames ...string,
) {
	agentFactoryRegistry[provider] = agentFactoryEntry{
		start:          start,
		defaultModels:  defaultModels,
		optionGroups:   optionGroups,
		modelSubGroups: effortSubGroups,
		envModelKey:    envModelKey,
		envEffortKey:   envEffortKey,
		binaryNames:    binaryNames,
	}
}

// mutateFactoryEntry applies fn to the provider's registry entry and writes it
// back. agentFactoryEntry is stored by value, so a read-modify-write that forgets
// the copy-back silently no-ops; routing every entry mutator through this helper
// makes the write-back mechanical rather than per-caller boilerplate.
func mutateFactoryEntry(provider leapmuxv1.AgentProvider, fn func(*agentFactoryEntry)) {
	e := agentFactoryRegistry[provider]
	fn(&e)
	agentFactoryRegistry[provider] = e
}

// setModelSubGroups overrides the default (effort-only) model sub_groups builder
// for a provider. Called from a provider's init() after registerAgentFactory so
// the manager's static fallback emits the provider's full per-model dependent
// groups (e.g. Claude's extended-thinking group alongside effort).
func setModelSubGroups(provider leapmuxv1.AgentProvider, fn modelSubGroupsFunc) {
	mutateFactoryEntry(provider, func(e *agentFactoryEntry) { e.modelSubGroups = fn })
}

// setAdditionalOptionIDs declares the provider-specific option-group ids a provider can
// carry beyond "model" and its static optionGroups (see agentFactoryEntry.additionalOptionIDs).
// Called from a provider's init() after registerAgentFactory; a provider with no additional
// axes (e.g. Cursor, Reasonix) need not call it.
func setAdditionalOptionIDs(provider leapmuxv1.AgentProvider, ids ...string) {
	mutateFactoryEntry(provider, func(e *agentFactoryEntry) { e.additionalOptionIDs = ids })
}

// registerPermissionModeConfigProvider registers a permission-mode ACP provider whose reasoning
// axis is a server-driven config option rather than the well-known "effort" id (Copilot's
// "reasoning_effort", Goose's "thinking_effort"). The two run different daemons but share the SAME
// registration shape: a permissionMode secondary channel with a per-daemon fallback mode list,
// dynamically-discovered models, NO env effort override (their reasoning axis is a config option,
// not the well-known effort id), and a set of server-driven config-option ids. Only the provider
// enum, Start function, fallback modes, env model key, binary name, and config-option ids vary --
// so each init() reduces to one call here, mirroring registerOpenCodeFamilyProvider, instead of
// two near-identical registration blocks that can drift.
func registerPermissionModeConfigProvider(
	provider leapmuxv1.AgentProvider,
	start startFunc,
	fallbackModes []*leapmuxv1.AvailableOption,
	envModelKey, binaryName string,
	configOptionIDs ...string,
) {
	registerAgentFactory(
		provider,
		start,
		nil, // models discovered dynamically from session/new
		staticSecondaryGroup(modeChannelPermissionMode, fallbackModes),
		envModelKey,
		"", // no well-known effort axis; reasoning is a server-driven config option
		binaryName,
	)
	setAdditionalOptionIDs(provider, configOptionIDs...)
}

// setPersistedOnlyOptionIDs declares option ids the provider persists but never
// surfaces as a group (see agentFactoryEntry.persistedOnlyOptionIDs). Called from a
// provider's init() after registerAgentFactory; the ids are also folded into KnownOptionIDs.
func setPersistedOnlyOptionIDs(provider leapmuxv1.AgentProvider, ids ...string) {
	mutateFactoryEntry(provider, func(e *agentFactoryEntry) { e.persistedOnlyOptionIDs = ids })
}

// PersistedOnlyOptionIDs returns the set of option ids the provider persists but never
// surfaces as a group, so confirmedOptions preserves them from the base rather than
// reconciling them away when the running agent's catalog omits them.
func PersistedOnlyOptionIDs(provider leapmuxv1.AgentProvider) map[string]bool {
	ids := map[string]bool{}
	for _, id := range agentFactoryRegistry[provider].persistedOnlyOptionIDs {
		ids[id] = true
	}
	return ids
}

// setProviderOptionDefaults declares the provider-specific seed option values (id->default) a
// fresh agent should launch with beyond model/effort. Called from a provider's init()
// after registerAgentFactory; a provider with none (most) need not call it.
func setProviderOptionDefaults(provider leapmuxv1.AgentProvider, defaults map[string]string) {
	mutateFactoryEntry(provider, func(e *agentFactoryEntry) { e.providerOptionDefaults = defaults })
}

// ProviderOptionDefaults returns the provider-specific seed option values (id->default)
// for a fresh agent, or nil when the provider declares none. resolveProviderDefaults
// stamps these uniformly so the service layer carries no per-provider branch.
//
// The returned map is the registry's own (shared across every agent of this provider) and is
// READ-ONLY: callers must not mutate it (resolveProviderDefaults only ranges over it). Unlike
// KnownOptionIDs, which builds a fresh map, this hands out the live registry map to avoid an
// allocation on every loadOptions; a mutating caller would corrupt the defaults for all
// subsequent agents.
func ProviderOptionDefaults(provider leapmuxv1.AgentProvider) map[string]string {
	return agentFactoryRegistry[provider].providerOptionDefaults
}

// KnownOptionIDs returns the complete static allowlist of option-group ids a provider
// can legitimately carry in its options map: the universal "model" axis, the static
// optionGroups templates (the secondary permission-mode/primary-agent axis), the
// provider's declared additionalOptionIDs (effort where applicable, Codex options, the ACP
// server config options), and its persistedOnlyOptionIDs (Pi's pi_provider). It is the
// not-running floor UpdateAgentSettings validates against; for a running or previously-run
// agent the caller additionally unions in the live/persisted catalog, so a newly
// server-reported config option is accepted even before it is added here. An unknown provider
// yields just {"model"}.
func KnownOptionIDs(provider leapmuxv1.AgentProvider) map[string]bool {
	ids := map[string]bool{OptionIDModel: true}
	reg, ok := agentFactoryRegistry[provider]
	if !ok {
		return ids
	}
	for _, g := range reg.optionGroups {
		ids[g.GetId()] = true
	}
	for _, id := range reg.additionalOptionIDs {
		ids[id] = true
	}
	for _, id := range reg.persistedOnlyOptionIDs {
		ids[id] = true
	}
	return ids
}

// setModelIDNormalizer registers a provider's model-id normalizer. Called from a
// provider's init() after registerAgentFactory; providers without one leave model ids
// unchanged. Both NormalizeModelID and the live agent (via modelIDNormalizerFor) read
// it, so the offline-label and live paths use the same function.
func setModelIDNormalizer(provider leapmuxv1.AgentProvider, fn func(string) string) {
	mutateFactoryEntry(provider, func(e *agentFactoryEntry) { e.modelIDNormalizer = fn })
}

// modelIDNormalizerFor returns the provider's registered model-id normalizer, or nil
// when it has none. Used to wire a live agent's normalizer from the same registry the
// offline NormalizeModelID reads.
func modelIDNormalizerFor(provider leapmuxv1.AgentProvider) func(string) string {
	return agentFactoryRegistry[provider].modelIDNormalizer
}

// DefaultModelEnvOverride returns the value of the provider's
// LEAPMUX_*_DEFAULT_MODEL environment variable, or "" if unset. It is the
// explicit operator override that takes precedence over both a CLI-reported
// default and the static catalog's preferred model (see withDefaultModelMarked).
func DefaultModelEnvOverride(provider leapmuxv1.AgentProvider) string {
	reg, ok := agentFactoryRegistry[provider]
	if !ok || reg.envModelKey == "" {
		return ""
	}
	return os.Getenv(reg.envModelKey)
}

// DefaultModel returns the default model ID for a provider, checking the
// provider's environment variable first, then falling back to the model
// marked IsDefault in the registered model list.
func DefaultModel(provider leapmuxv1.AgentProvider) string {
	reg, ok := agentFactoryRegistry[provider]
	if !ok {
		return ""
	}
	if env := DefaultModelEnvOverride(provider); env != "" {
		return env
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

// NormalizeModelID canonicalizes a provider's model id into the alias space the
// provider stores and compares against, so two spellings of the same model -- e.g.
// the CLI's fully-qualified "claude-opus-4-8[1m]" and the alias "opus[1m]" -- compare
// equal. Providers without an alias space return the id unchanged. Used by the
// settings-change notification so a model that merely re-normalizes (not a user
// switch) isn't reported as a change.
func NormalizeModelID(provider leapmuxv1.AgentProvider, model string) string {
	if fn := modelIDNormalizerFor(provider); fn != nil {
		return fn(model)
	}
	return model
}

// EffortEnvOverride returns the value of the provider's
// LEAPMUX_*_DEFAULT_EFFORT environment variable, or "" if unset. This is the
// only way Leapmux injects a concrete effort level at agent-open time; when
// the env var is unset, effort defaults to EffortAuto and the agent binary
// picks its own level. This avoids pinning users on newer effort names
// (e.g. "xhigh") that an older CLI binary may not recognize.
func EffortEnvOverride(provider leapmuxv1.AgentProvider) string {
	reg, ok := agentFactoryRegistry[provider]
	if !ok || reg.envEffortKey == "" {
		return ""
	}
	return os.Getenv(reg.envEffortKey)
}

// ProviderManagesEffort reports whether leapmux owns a model-dependent effort
// default for this provider -- i.e. its static catalog carries per-model effort
// tiers (Claude, Codex, Pi). For those, resolveProviderDefaults stamps an effort
// default into the launch options. ACP providers' effort, when they have one (e.g.
// OpenCode/Kilo/Copilot reasoning effort), is a server-driven config option
// surfaced as a option group; leapmux must NOT stamp a default for them, or
// it would shadow/collide with the server's own value (the "effort" config option's
// id is OptionIDEffort) and pollute the persisted options with an inert key.
func ProviderManagesEffort(provider leapmuxv1.AgentProvider) bool {
	reg, ok := agentFactoryRegistry[provider]
	if !ok {
		return false
	}
	for _, m := range reg.defaultModels {
		if m != nil && len(m.SupportedEfforts) > 0 {
			return true
		}
	}
	return false
}

// FindAvailableModel returns the AvailableModel with the given ID, or nil if
// none matches. Callers typically use this to resolve per-model metadata
// (e.g. DefaultEffort) from a catalog returned by the CLI.
func FindAvailableModel(models []*ModelInfo, id string) *ModelInfo {
	for _, m := range models {
		// Guard nil entries: callers (defaultModelForList, withDefaultModelMarked)
		// already treat the slice as possibly nil-bearing, so this must too.
		if m != nil && m.Id == id {
			return m
		}
	}
	return nil
}

// IsEffortAutoTransition reports whether a settings update is switching
// effort from a concrete value to EffortAuto. Both providers' UpdateSettings
// must handle this by requesting a restart, since apply-in-place paths do
// not accept "auto" as a live effortLevel / reasoning_effort value.
func IsEffortAutoTransition(newEffort, curEffort string) bool {
	return newEffort == EffortAuto && curEffort != EffortAuto
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
	for provider, reg := range agentFactoryRegistry {
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
