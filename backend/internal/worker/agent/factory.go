package agent

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/agentlabels"
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
const DefaultAPITimeout = config.DefaultAPITimeout

// EffortAuto is the LeapMux-side sentinel meaning "let the CLI pick its own
// default reasoning effort". When an agent's Effort is this value, the
// provider layer omits the CLI flag / wire field entirely so older CLIs
// that don't recognize newer effort names (e.g. "xhigh") still work.
const EffortAuto = contracts.EffortAuto

// DefaultModelSentinel is the model id that means "let the agent select the
// account's default model" -- the model-side analogue of EffortAuto above. Claude
// Code reports the sentinel in its own model list. LeapMux stores the sentinel for
// a new Codex session, and Codex replaces it with the concrete model that the
// thread/start lifecycle response reports.
const DefaultModelSentinel = contracts.DefaultModelSentinel

// UsesAccountDefaultModel reports whether the model lets the agent select the
// account default. An empty value and DefaultModelSentinel have this meaning.
//
// Every site that puts a model on the wire must call this. One two-clause check
// keeps the omit-the-model decision the same at every site, so a forgotten
// sentinel clause becomes a missing call rather than a silent wrong branch.
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
// agentID identifies the agent, exitCode is the process exit code, err is
// non-nil if the process exited with an error, and stopped is true when the
// exit was driven by an explicit Stop (a user interrupt, a relaunch, or a
// shutdown) rather than a crash. The background-task registry uses stopped to
// label rows 'stopped' vs 'interrupted'.
//
// The Manager keeps the exiting provider registered until this handler returns.
// This lets the handler pause durable input before the slot permits a restart.
//
// A handler must NEVER acquire the agent's lifecycle lock, directly or through
// a Manager method that takes it (SendInput, SendChildInput, RestartAgent,
// StopAndWaitAgent). It runs on the exit goroutine, and a lifecycle caller is
// normally waiting for that goroutine to finish while it holds that very lock,
// so an acquire here deadlocks the agent for the life of the process. Do the
// work that needs the lock AFTER the lifecycle call returns, the way the
// plan-execution path does.
type ExitHandler func(agentID string, exitCode int, err error, stopped bool)

// Options configures one agent process start.
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
	Options optionmap.Map
	// NewSessionDefaultOptionIDs records which option values came from the
	// safe defaults for a new session. A provider can fall back when its local
	// CLI does not support one of these defaults. Explicit values are absent.
	NewSessionDefaultOptionIDs map[string]bool
	StartupTimeout             time.Duration           // Timeout for the startup handshake (default: 5m)
	APITimeout                 time.Duration           // Timeout for JSON-RPC requests (default: 10s)
	Shell                      string                  // Default shell path (always set when using shell wrapper)
	LoginShell                 bool                    // If true, use interactive+login shell flags
	HomeDir                    string                  // User's home directory (reads Claude Code settings; expands `~` when the Pi rule CHECKS a resume handle -- Pi expands it again itself)
	AgentProvider              leapmuxv1.AgentProvider // Coding agent provider (default: CLAUDE_CODE)
	// ExtraEnv is appended verbatim to the spawned process's
	// environment after the provider-specific env-var setup. The
	// service.Service populates this with LEAPMUX_CONTROL_* so the
	// running agent can drive the worker via the leapmux control CLI.
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
	return config.DefaultAgentStartupTimeout
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
	// permissionDefaults holds this provider's two permission-policy answers in one
	// place, so a reader sees both at once and neither can be changed alone.
	permissionDefaults PermissionDefaults
	envModelKey        string   // e.g. "LEAPMUX_CLAUDE_DEFAULT_MODEL"
	envEffortKey       string   // e.g. "LEAPMUX_CLAUDE_DEFAULT_EFFORT"
	binaryNames        []string // preferred first; e.g. {"codex", "codex-x86_64-pc-windows-msvc"}
	// launchResolver replaces the binaryNames probe for a provider whose program is
	// not a bare name the login shell can resolve (see launchResolverFunc). When set,
	// binaryNames is unused for both availability and launch.
	launchResolver launchResolverFunc
}

// launchSpec says how to invoke a provider's agent program.
//
// It exists for a provider that a bare PATH name cannot describe: ZCode ships no
// executable of its own, only a `zcode.cjs` script inside its desktop bundle, which
// a Node interpreter has to be handed.
type launchSpec struct {
	// Program is the shell word that starts the process: a bare name the shell
	// resolves through PATH, or an absolute path. buildShellWrappedCommand quotes it.
	Program string
	// PrefixArgs precede the provider's own BaseArgs -- the script path an
	// interpreter must receive before the script's own arguments.
	PrefixArgs []string
	// Env are extra KEY=VALUE entries the launched program needs, pinned onto the
	// spawned environment (ELECTRON_RUN_AS_NODE=1 for ZCode's Electron-as-Node
	// fallback). Empty for a plain interpreter.
	Env []string
}

// launchResolution is the three-state answer a launch resolver gives. It is the
// launch-side counterpart of probeResult, and it exists for the same reason:
// ListAvailableProviders must distinguish "the probe ran and the provider is not
// installed" from "no probe could run", or a broken shell reports an authoritative
// empty provider list that never retries. The two stay separate types because they
// answer different questions -- "is this program present" against "how do I start this
// provider" -- and a resolver that returned probeYes would say nothing about the spec.
//
// launchUnknown is the ZERO value on purpose: a resolver that falls off the end of
// its own logic then reports "nothing established", which is retryable, rather than
// an authoritative absence.
type launchResolution int

const (
	// launchUnknown: no probe established anything. The caller must treat the scan
	// as incomplete and retryable.
	launchUnknown launchResolution = iota
	// launchFound: the returned launchSpec is usable.
	launchFound
	// launchMissing: every probe ran and the provider is not usable on this machine.
	launchMissing
)

// launchResolverFunc discovers how to launch a provider. The shell path and
// login-shell flag are the same values probeBinary uses, so a resolver that wants
// the PATH answer can delegate to checkBinaryAvailable.
//
// A resolver MUST return launchUnknown whenever it could not establish an answer --
// a probe killed by the context, a shell that would not start, an interpreter it
// could not run. Reporting launchMissing there would freeze a transient failure as
// "not installed" for the worker's lifetime.
type launchResolverFunc func(ctx context.Context, shellPath string, loginShell bool) (launchSpec, launchResolution)

// registerLaunchResolver installs a provider's launch resolver. Called from the
// provider's init() after registerAgentFactory, beside setModelSubGroups and the
// other entry mutators.
func registerLaunchResolver(provider leapmuxv1.AgentProvider, fn launchResolverFunc) {
	mutateFactoryEntry(provider, func(e *agentFactoryEntry) { e.launchResolver = fn })
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

// PermissionDefaults is a provider's complete permission-policy declaration: what a NEW
// session asks for, and what a session that stored nothing falls back to.
//
// The two live together because they are one policy read at two moments, and because
// keeping them apart let them contradict each other: Goose declared `smart_approve` as
// its new-session mode in one file and `auto` -- the very mode its bypass shortcut
// selects -- as its fallback in another, so every RESUMED Goose session opened with
// permission prompts disabled. One struct in one call puts both under the reader's eye.
//
// They stay two FIELDS because they are genuinely two values: Claude asks for Auto Mode
// but falls back to Default, since a CLI that cannot enter Auto must still start.
type PermissionDefaults struct {
	// NewSession is the option id->value set stamped only into a session opened WITHOUT a
	// resume handle. A provider may name several ids: Copilot sets both of its axes.
	NewSession map[string]string
	// Fallback is the permission mode for a session that carries no stored one -- a
	// resume, a relaunch, or a row written before the axis existed. "" means the provider
	// has no permission-mode axis at all, and the option is left unset.
	Fallback string
}

// setPermissionDefaults declares a provider's permission policy. Called from the
// provider's own init(), so the values live in that provider's file.
func setPermissionDefaults(provider leapmuxv1.AgentProvider, defaults PermissionDefaults) {
	mutateFactoryEntry(provider, func(e *agentFactoryEntry) { e.permissionDefaults = defaults })
}

// NewAgentOptionDefaults returns the safe option values for a new session.
// The returned map is shared and read-only.
func NewAgentOptionDefaults(provider leapmuxv1.AgentProvider) map[string]string {
	return agentFactoryRegistry[provider].permissionDefaults.NewSession
}

// FallbackPermissionMode returns the mode a session with no stored one takes, or "" for a
// provider with no permission-mode axis.
func FallbackPermissionMode(provider leapmuxv1.AgentProvider) string {
	return agentFactoryRegistry[provider].permissionDefaults.Fallback
}

// addStaticOptionGroups appends provider-owned groups to the static catalog.
// A live provider must also include each group in its settings snapshot.
func addStaticOptionGroups(provider leapmuxv1.AgentProvider, groups ...*leapmuxv1.AvailableOptionGroup) {
	mutateFactoryEntry(provider, func(e *agentFactoryEntry) {
		e.optionGroups = append(e.optionGroups, groups...)
	})
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
// default and the static catalog's preferred model (see defaultModelIDForList).
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
// only way LeapMux injects a concrete effort level at agent-open time; when
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
		// Guard nil entries: callers (modelOptionGroup, the effort resolver) already
		// treat the slice as possibly nil-bearing, so this must too.
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
//
// The second return is false when the scan did not finish under ctx — at
// least one probe was cut short, so the list is NOT evidence of absence.
// The caller must treat that as a retryable failure, not as "none
// installed": probes that DID complete are cached, so a retry only
// re-runs what was cut short.
func ListAvailableProviders(ctx context.Context, shellPath string, useLoginShell bool) ([]leapmuxv1.AgentProvider, bool) {
	type check struct {
		provider leapmuxv1.AgentProvider
		binaries []string
		// resolver, when non-nil, replaces the binaries probe. It answers with the
		// same three states, so the settled bookkeeping below is identical.
		resolver launchResolverFunc
	}
	var checks []check
	for provider, reg := range agentFactoryRegistry {
		switch {
		case reg.launchResolver != nil:
			checks = append(checks, check{provider: provider, resolver: reg.launchResolver})
		case len(reg.binaryNames) > 0:
			checks = append(checks, check{provider: provider, binaries: reg.binaryNames})
		}
	}

	found := make([]bool, len(checks))
	// settled[i] reports whether every probe this check ran ESTABLISHED
	// something. A check that never found its binary and whose last probe
	// proved nothing is not evidence of absence.
	settled := make([]bool, len(checks))
	var wg sync.WaitGroup
	for i, c := range checks {
		wg.Add(1)
		go func(idx int, c check) {
			defer wg.Done()
			settled[idx] = true
			if c.resolver != nil {
				_, res := c.resolver(ctx, shellPath, useLoginShell)
				found[idx] = res == launchFound
				settled[idx] = res != launchUnknown
				return
			}
			for _, b := range c.binaries {
				res := checkBinaryAvailable(ctx, shellPath, useLoginShell, b)
				if res == probeYes {
					found[idx] = true
					return
				}
				if !res.settled() {
					settled[idx] = false
				}
			}
		}(i, c)
	}
	wg.Wait()

	// The scan is complete only when every probe it ran answered.
	//
	// An expired ctx is ONE way a probe proves nothing: exec.CommandContext
	// killed its shell, and every cache write below would freeze a killed
	// probe as "absent" for the worker's lifetime. But it is not the only
	// way — probeBinary also reports "inconclusive" for a $SHELL that
	// cannot start, a missing interpreter, EACCES, a fork failure under
	// load, and a login profile that exits non-zero before the probe runs.
	// Reading ctx alone made all of those answer with an AUTHORITATIVE
	// empty list, so a user with a broken shell saw "no agent providers
	// installed" with no retry, although every CLI was installed.
	if ctx.Err() != nil {
		return nil, false
	}
	for i := range checks {
		if !found[i] && !settled[i] {
			return nil, false
		}
	}

	var result []leapmuxv1.AgentProvider
	for i, c := range checks {
		if found[i] {
			result = append(result, c.provider)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result, true
}

// resolveProviderLaunch resolves how to start a provider's program at spawn time.
//
// EVERY provider goes through it, and it reads the SAME registry entry the availability
// scan reads -- so the two can never disagree about which program a provider runs, and a
// provider cannot acquire a launch path of its own that the scan knows nothing about.
// A provider with no resolver takes the binaryNames path: resolveBinaryName picks the
// first candidate that probes present, and falls back to candidates[0] so the failure
// surfaces as "command not found" from the shell.
//
// An error is returned only for a resolver that reports launchMissing or launchUnknown.
// Both are startup failures the user must see; they differ for the availability scan,
// not here, where neither one can start a process.
func resolveProviderLaunch(
	ctx context.Context,
	shellPath string,
	loginShell bool,
	provider leapmuxv1.AgentProvider,
) (launchSpec, error) {
	entry := agentFactoryRegistry[provider]
	if fn := entry.launchResolver; fn != nil {
		spec, res := fn(ctx, shellPath, loginShell)
		switch res {
		case launchFound:
			return spec, nil
		case launchMissing:
			return launchSpec{}, fmt.Errorf("%s is not installed on this machine", agentlabels.DisplayName(provider))
		default:
			return launchSpec{}, fmt.Errorf("could not determine how to launch %s (the probe did not complete)", agentlabels.DisplayName(provider))
		}
	}
	if len(entry.binaryNames) == 0 {
		return launchSpec{}, fmt.Errorf("no binary candidates registered for %s", agentlabels.DisplayName(provider))
	}
	return launchSpec{Program: resolveBinaryName(ctx, shellPath, loginShell, entry.binaryNames)}, nil
}

// resolveBinaryName returns the first binary from candidates that is
// available in the user's shell environment. If none are found, the first
// candidate is returned so that invocation produces a meaningful
// "command not found" error rather than silently picking an alias.
func resolveBinaryName(ctx context.Context, shellPath string, loginShell bool, candidates []string) string {
	for _, c := range candidates {
		if checkBinaryAvailable(ctx, shellPath, loginShell, c) == probeYes {
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
// a session, so no TTL is needed — but only a probe that RAN TO COMPLETION
// establishes anything: one killed by an expired context proves neither
// presence nor absence, and caching its result would freeze a load-induced
// timeout as "not installed" for the rest of the session (the new-agent
// button then stays disabled no matter how idle the machine becomes).
var (
	binaryAvailabilityCache   sync.Map // binaryAvailabilityKey -> bool
	binaryAvailabilityMutexes sync.Map // binaryAvailabilityKey -> *sync.Mutex
)

type binaryAvailabilityKey struct {
	shellPath  string
	loginShell bool
	binaryName string
}

// binaryFlagUnsupportedCache remembers, for this worker process, that one binary
// rejected one launch flag. A capability belongs to the INSTALLED CLI, not to the agent
// that happened to discover it: without this, a second tab repeats the failed spawn, and
// every restart of the same tab forgets and fails again -- which turns a stored option
// into a tab that can never start.
//
// Only a NEGATIVE result is stored, and only from a launch the CLI itself refused, so
// there is nothing to invalidate: a flag a binary accepts is never recorded, and an
// upgraded CLI is a new worker process.
var binaryFlagUnsupportedCache sync.Map // binaryFlagKey -> struct{}

type binaryFlagKey struct {
	binaryAvailabilityKey
	flag string
}

// MarkBinaryFlagUnsupported records that binaryName rejected flag under this shell.
func MarkBinaryFlagUnsupported(shellPath string, loginShell bool, binaryName, flag string) {
	binaryFlagUnsupportedCache.Store(
		binaryFlagKey{binaryAvailabilityKey{shellPath, loginShell, binaryName}, flag}, struct{}{})
}

// BinaryFlagUnsupported reports whether a previous launch in this worker process proved
// that binaryName rejects flag under this shell.
func BinaryFlagUnsupported(shellPath string, loginShell bool, binaryName, flag string) bool {
	_, found := binaryFlagUnsupportedCache.Load(
		binaryFlagKey{binaryAvailabilityKey{shellPath, loginShell, binaryName}, flag})
	return found
}

// checkBinaryAvailable answers whether one binary resolves, and whether
// that answer ESTABLISHES anything.
//
// The second result is not optional bookkeeping: a caller that reports an
// authoritative "not installed" needs to know the difference between a
// shell that ran and said no, and a shell that never ran at all. A cached
// answer is conclusive by construction, because only a conclusive probe
// is ever cached.
func checkBinaryAvailable(ctx context.Context, shellPath string, loginShell bool, binaryName string) probeResult {
	key := binaryAvailabilityKey{shellPath, loginShell, binaryName}
	if v, ok := binaryAvailabilityCache.Load(key); ok {
		return v.(probeResult)
	}
	// Single-flight per key. A mutex rather than the previous sync.Once:
	// once an attempt ends without caching (deadline-killed probe), the
	// next caller must be able to probe again, and a spent Once cannot.
	muAny, _ := binaryAvailabilityMutexes.LoadOrStore(key, &sync.Mutex{})
	mu := muAny.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()
	if v, ok := binaryAvailabilityCache.Load(key); ok {
		return v.(probeResult)
	}
	res := probeBinary(ctx, shellPath, loginShell, binaryName)
	// An unknown result is this call's best answer but never a cached one — the next
	// caller re-probes with its own deadline.
	if res.settled() {
		binaryAvailabilityCache.Store(key, res)
	}
	return res
}

// probeResult is the answer every environment probe in this package gives: does the
// thing exist, and did the probe ESTABLISH anything at all?
//
// It replaces the `(found, conclusive bool)` pair those probes used to return. The pair
// could spell `(true, false)` -- "it works, but nothing was established" -- which means
// nothing, and a caller that read only the first boolean silently treated an
// unestablished answer as a real one. The enum cannot express the contradiction.
//
// probeUnknown is the ZERO value on purpose, for the same reason launchUnknown is: a
// probe that falls off the end of its own logic then reports "nothing established",
// which is retryable, rather than an authoritative absence.
type probeResult int

const (
	// probeUnknown: the probe established nothing. The caller must treat the answer as
	// incomplete and must not cache it.
	probeUnknown probeResult = iota
	// probeYes: the thing is present and usable.
	probeYes
	// probeNo: the probe ran and the thing is not present.
	probeNo
)

// settled reports whether the probe established anything, so a caller can tell an
// authoritative absence from a probe that never ran.
func (r probeResult) settled() bool { return r != probeUnknown }

// probeReachedPresent / probeReachedAbsent are printed by the inner
// command so a login profile that exits before the probe cannot be
// cached as "binary absent". Exit status alone cannot tell those apart:
// both are ExitError.
const (
	probeReachedPresent = "__LEAPMUX_PROBE_REACHED__present"
	probeReachedAbsent  = "__LEAPMUX_PROBE_REACHED__absent"
)

// probeBinary asks the shell whether binaryName resolves, and reports
// whether the answer ESTABLISHES anything.
//
// The two are not the same, and conflating them is what froze a broken
// environment as "not installed" for the worker's lifetime. Presence is
// the inner command's marker on stdout, not the shell's exit status:
//
//   - the inner command printed probeReachedPresent or Absent — conclusive;
//   - the shell could not START at all (a $SHELL that is not executable, a
//     missing interpreter, EACCES, fork failure under load) — proves
//     nothing about the binary;
//   - a login profile exited before the inner command — no marker, likewise;
//   - ctx expired and CommandContext killed the process — likewise.
//
// $SHELL reaches here unvalidated (terminal.ResolveDefaultShell does no
// LookPath), so the start-failure case is reachable, not theoretical.
func probeBinary(ctx context.Context, shellPath string, loginShell bool, binaryName string) probeResult {
	shellName := terminal.ShellBaseName(shellPath)
	quoted := posixQuote(binaryName)

	var inner, flag string
	switch {
	case terminal.IsPwsh(shellName):
		inner = fmt.Sprintf(
			"if (Get-Command %s -ErrorAction SilentlyContinue) { Write-Output '%s' } else { Write-Output '%s' }",
			pwshQuote(binaryName), probeReachedPresent, probeReachedAbsent,
		)
		flag = "-Command"
	case shellName == "nu":
		inner = fmt.Sprintf(
			"if (which %s | is-not-empty) { echo '%s' } else { echo '%s' }",
			nuQuote(binaryName), probeReachedPresent, probeReachedAbsent,
		)
		flag = "-c"
	case shellName == "tcsh" || shellName == "csh":
		inner = fmt.Sprintf(
			"which %s >& /dev/null && printf '%%s\\n' '%s' || printf '%%s\\n' '%s'",
			quoted, probeReachedPresent, probeReachedAbsent,
		)
		flag = "-c"
	default:
		inner = fmt.Sprintf(
			"if command -v %s >/dev/null 2>&1; then printf '%%s\\n' '%s'; else printf '%%s\\n' '%s'; fi",
			quoted, probeReachedPresent, probeReachedAbsent,
		)
		flag = "-c"
	}

	args := terminal.CommandArgs(shellPath, loginShell, flag, inner)

	cmd := exec.CommandContext(ctx, shellPath, args...)
	cmd.Dir = os.TempDir()
	procutil.HideConsoleWindow(cmd)
	procutil.DetachFromTerminal(cmd)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	_ = cmd.Run()
	if ctx.Err() != nil {
		return probeUnknown
	}
	out := stdout.String()
	if strings.Contains(out, probeReachedPresent) {
		return probeYes
	}
	if strings.Contains(out, probeReachedAbsent) {
		return probeNo
	}
	return probeUnknown
}
