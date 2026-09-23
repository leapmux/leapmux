package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"sort"
	"sync"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionids"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
)

// StartFunc starts one agent of a provider.
type StartFunc func(ctx context.Context, opts Options, sink ProviderServices) (Agent, error)

// Registration is everything the worker knows about one provider before any of
// its agents runs: how to find and start its program, its wire-format plugin, its
// static catalog, and its option policy. Each provider states one Registration,
// and NewRegistry collects them; nothing mutates a Registration after that.
type Registration struct {
	// Provider is the enum value the Registration answers for.
	Provider leapmuxv1.AgentProvider
	// Plugin is the provider's stateless wire-format plugin.
	Plugin Provider
	// Start starts one agent of the provider.
	Start StartFunc
	// Locator finds the provider's program, for both the availability scan and the
	// launch, so the two never disagree about which program runs.
	Locator launch.Locator
	// DefaultModels is the static model catalog served before a running agent
	// reports its own. nil for a provider whose catalog is always discovered.
	DefaultModels []*ModelInfo
	// OptionGroups are the static option-group templates (the secondary
	// permission-mode or primary-agent axis, Codex's sandbox and network axes).
	OptionGroups []*leapmuxv1.AvailableOptionGroup
	// ModelSubGroups builds the model-dependent sub_groups carried on each model
	// option (nil selects EffortSubGroups; Claude supplies its own to also emit the
	// per-model extended-thinking group). Used by the manager's static fallback
	// so a restarting agent's groups still carry every model's dependent groups.
	ModelSubGroups ModelSubGroupsFunc
	// NormalizeModelID canonicalizes a model id into the provider's alias space (e.g.
	// Claude's "claude-opus-4-8" -> "opus[1m]", Cursor's "default[]" -> "auto"). nil leaves
	// the id unchanged. Registry.NormalizeModelID (the offline-label path) reads it, and the
	// provider hands the same function to its live agent, so the two can't drift.
	NormalizeModelID func(string) string
	// AdditionalOptionIDs lists the option-group ids this provider can carry BEYOND the
	// universal "model" axis and the static OptionGroups templates (the secondary
	// permission-mode/primary-agent axis): the well-known "effort" axis where the
	// provider has one, Codex's sandbox/network/collaboration/service-tier options,
	// Pi's pi_provider, and the server-driven ACP config options each family exposes
	// (Goose's thinking_effort/provider, Reasonix's tool_approval).
	// Together with "model" and OptionGroups they form KnownOptionIDs -- the static
	// allowlist UpdateAgentSettings validates an incoming options map against, so a
	// foreign axis the provider can't apply is dropped instead of persisting a
	// phantom key and emitting a misleading settings_changed notification.
	AdditionalOptionIDs []string
	// PersistedOnlyOptionIDs lists option ids the provider persists but NEVER
	// surfaces as a group -- Pi's pi_provider (the underlying LLM provider behind a model
	// id). They are a SUBSET of the known ids (folded into KnownOptionIDs below) but, unlike
	// every other axis, their absence from a confirmed catalog is by design, not orphaning:
	// confirmedOptions preserves them from the base instead of reconciling them away.
	PersistedOnlyOptionIDs []string
	// ProviderOptionDefaults seeds provider-specific option values (id->default) into a fresh
	// agent's launch options beyond model/effort -- e.g. Codex's sandbox / network /
	// collaboration / service-tier defaults. resolveProviderDefaults stamps these
	// uniformly for every provider, so a new provider declares its seeds here rather
	// than the service layer growing a per-provider branch.
	ProviderOptionDefaults map[string]string
	// PermissionDefaults holds this provider's two permission-policy answers in one
	// place, so a reader sees both at once and neither can be changed alone.
	PermissionDefaults PermissionDefaults
	// ManagesEffort marks a provider whose effort tiers depend on the MODEL although
	// DefaultModels carries none to read them from. Native Copilot is the one: its
	// account decides which models exist, so it reads the catalog -- and each model's
	// reasoning-effort list -- from the open session. Without this flag
	// Registry.ManagesEffort answers from DefaultModels alone, and a nil catalog reads
	// as "this provider has no per-model effort".
	ManagesEffort bool
	// FixedPermissionModes marks a provider whose permission-mode enum LeapMux
	// states itself, completely, so ValidateLaunchOptions may reject a value the
	// static group omits. An ACP provider DISCOVERS its modes from the daemon, and
	// its static group is only a seed, so validating against that seed would refuse
	// a mode the daemon really offers.
	//
	// It is a SEPARATE flag from ManagesEffort on purpose. The two answered one
	// question for as long as the same providers happened to give the same answer,
	// and the moment native Copilot set ManagesEffort -- for its model-dependent
	// effort catalog alone -- it silently gained permission-mode validation
	// authority as well. NewRegistry refuses the flag on a provider with no static
	// permission-mode group, because there would be nothing to validate against.
	FixedPermissionModes bool
	// EnvModelKey names the operator's default-model variable, e.g.
	// "LEAPMUX_CLAUDE_DEFAULT_MODEL". "" for a provider that honors none.
	EnvModelKey string
	// EnvEffortKey names the operator's default-effort variable, e.g.
	// "LEAPMUX_CLAUDE_DEFAULT_EFFORT". "" for a provider that honors none.
	EnvEffortKey string
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
	// resume handle. It is a map because a provider may stamp more than one axis; every
	// provider that declares one today states its permission mode alone.
	NewSession map[string]string
	// Fallback is the permission mode for a session that carries no stored one -- a
	// resume, a relaunch, or a row written before the axis existed. "" means the provider
	// has no permission-mode axis at all, and the option is left unset.
	Fallback string
}

// DefaultModel returns the provider's default model id: the operator's override
// in EnvModelKey when it is set, else the catalog entry marked IsDefault, else
// the first catalog entry. "" for a provider with neither.
func (r Registration) DefaultModel() string {
	if env := r.defaultModelEnvOverride(); env != "" {
		return env
	}
	for _, m := range r.DefaultModels {
		if m.IsDefault {
			return m.Id
		}
	}
	if len(r.DefaultModels) > 0 {
		return r.DefaultModels[0].Id
	}
	return ""
}

// defaultModelEnvOverride returns the value of EnvModelKey, or "" when the
// provider honors no such variable or the operator did not set it.
func (r Registration) defaultModelEnvOverride() string {
	if r.EnvModelKey == "" {
		return ""
	}
	return os.Getenv(r.EnvModelKey)
}

// Registry holds one Registration for each provider the worker supports. It is
// immutable: NewRegistry builds it, and every method only reads it, so one
// Registry serves every goroutine without a lock.
//
// The Manager that starts agents carries the Registry every other caller reads,
// so the wiring is a constructor argument and not package state. A caller cannot
// read a registry nobody filled.
type Registry struct {
	byProvider map[leapmuxv1.AgentProvider]Registration
	// providers lists the registered providers in enum order.
	providers []leapmuxv1.AgentProvider
}

// NewRegistry builds a Registry from one Registration for each provider. It
// refuses a Registration that could not serve a provider:
//
//   - a Provider that is UNSPECIFIED or not an AgentProvider value;
//   - a second Registration for the same Provider;
//   - a nil Plugin or a nil Start;
//   - a Locator that states no way, or two ways, to find the program;
//   - FixedPermissionModes without a static permission-mode group.
//
// A nil ModelSubGroups selects EffortSubGroups.
func NewRegistry(regs ...Registration) (*Registry, error) {
	r := &Registry{byProvider: make(map[leapmuxv1.AgentProvider]Registration, len(regs))}
	var errs []error
	for _, reg := range regs {
		if err := validateRegistration(reg); err != nil {
			errs = append(errs, err)
			continue
		}
		if _, dup := r.byProvider[reg.Provider]; dup {
			errs = append(errs, fmt.Errorf("provider %v: registered twice", reg.Provider))
			continue
		}
		if reg.ModelSubGroups == nil {
			reg.ModelSubGroups = EffortSubGroups
		}
		r.byProvider[reg.Provider] = reg
		r.providers = append(r.providers, reg.Provider)
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	slices.Sort(r.providers)
	return r, nil
}

func validateRegistration(reg Registration) error {
	if _, known := leapmuxv1.AgentProvider_name[int32(reg.Provider)]; !known || reg.Provider == leapmuxv1.AgentProvider_AGENT_PROVIDER_UNSPECIFIED {
		return fmt.Errorf("provider %v: not a registrable provider", reg.Provider)
	}
	var errs []error
	if reg.Plugin == nil {
		errs = append(errs, errors.New("nil Plugin"))
	}
	if reg.Start == nil {
		errs = append(errs, errors.New("nil Start"))
	}
	if !reg.Locator.Valid() {
		errs = append(errs, errors.New("the Locator states no single way to find the program"))
	}
	if reg.FixedPermissionModes && optionids.GroupByID(reg.OptionGroups, OptionIDPermissionMode) == nil {
		errs = append(errs, errors.New("FixedPermissionModes with no static permission-mode group"))
	}
	if len(errs) > 0 {
		return fmt.Errorf("provider %v: %w", reg.Provider, errors.Join(errs...))
	}
	return nil
}

// Providers returns the registered providers in enum order. The caller owns the
// returned slice.
func (r *Registry) Providers() []leapmuxv1.AgentProvider {
	return slices.Clone(r.providers)
}

// Registration returns the provider's Registration and whether it has one.
func (r *Registry) Registration(provider leapmuxv1.AgentProvider) (Registration, bool) {
	reg, ok := r.byProvider[provider]
	return reg, ok
}

// Plugin returns the provider's wire-format plugin. A provider with no
// Registration -- only UNSPECIFIED reaches here, because a request that omits the
// field is resolved through ProviderOrDefault first -- gets ProviderDefaults,
// which answers every question with the neutral default.
func (r *Registry) Plugin(provider leapmuxv1.AgentProvider) Provider {
	if reg, ok := r.byProvider[provider]; ok {
		return reg.Plugin
	}
	return ProviderDefaults{}
}

// IsInterrupt reports whether content is an interrupt frame in the wire format
// used by provider. Unknown providers and unparseable payloads both return false.
func (r *Registry) IsInterrupt(provider leapmuxv1.AgentProvider, content string) bool {
	return r.Plugin(provider).IsInterrupt(content)
}

// PermissionModeStoredSentinel is the literal an OLDER agents.options row can carry for a
// provider that never had a mode named "default". It is a cross-provider DB value, so it
// is spelled here and NOT as contracts.ClaudeModeDefault: that constant means Claude
// Code's own Default mode, and reading it while deciding about a Codex or ZCode row would
// claim Claude's vocabulary governs a provider that never used it. The two strings
// coincide, and that coincidence is not a shared meaning.
const PermissionModeStoredSentinel = "default"

// PermissionModeOrDefault normalizes an empty permission mode to the
// provider-native default. It also treats the historical DB schema default
// "default" as unset for providers whose native default is different.
func (r *Registry) PermissionModeOrDefault(provider leapmuxv1.AgentProvider, mode string) string {
	defaultMode := r.FallbackPermissionMode(provider)
	if mode == "" {
		return defaultMode
	}
	if mode == PermissionModeStoredSentinel && defaultMode != "" && defaultMode != PermissionModeStoredSentinel {
		return defaultMode
	}
	return mode
}

// HasFixedPermissionModes reports whether ValidateLaunchOptions may reject a
// permission mode for this provider. See Registration.FixedPermissionModes.
func (r *Registry) HasFixedPermissionModes(provider leapmuxv1.AgentProvider) bool {
	return r.byProvider[provider].FixedPermissionModes
}

// PersistedOnlyOptionIDs returns the set of option ids the provider persists but never
// surfaces as a group, so confirmedOptions preserves them from the base rather than
// reconciling them away when the running agent's catalog omits them.
func (r *Registry) PersistedOnlyOptionIDs(provider leapmuxv1.AgentProvider) map[string]bool {
	ids := map[string]bool{}
	for _, id := range r.byProvider[provider].PersistedOnlyOptionIDs {
		ids[id] = true
	}
	return ids
}

// NewAgentOptionDefaults returns the safe option values for a new session.
// The returned map is shared and read-only.
func (r *Registry) NewAgentOptionDefaults(provider leapmuxv1.AgentProvider) map[string]string {
	return r.byProvider[provider].PermissionDefaults.NewSession
}

// FallbackPermissionMode returns the mode a session with no stored one takes, or "" for a
// provider with no permission-mode axis.
func (r *Registry) FallbackPermissionMode(provider leapmuxv1.AgentProvider) string {
	return r.byProvider[provider].PermissionDefaults.Fallback
}

// ProviderOptionDefaults returns the provider-specific seed option values (id->default)
// for a fresh agent, or nil when the provider declares none. resolveProviderDefaults
// stamps these uniformly so the service layer carries no per-provider branch.
//
// The returned map is the Registration's own (shared across every agent of this provider)
// and is READ-ONLY: callers must not mutate it (resolveProviderDefaults only ranges over it).
// Unlike KnownOptionIDs, which builds a fresh map, this hands out the live map to avoid an
// allocation on every loadOptions; a mutating caller would corrupt the defaults for all
// subsequent agents.
func (r *Registry) ProviderOptionDefaults(provider leapmuxv1.AgentProvider) map[string]string {
	return r.byProvider[provider].ProviderOptionDefaults
}

// KnownOptionIDs returns the complete static allowlist of option-group ids a provider
// can legitimately carry in its options map: the universal "model" axis, the static
// OptionGroups templates (the secondary permission-mode/primary-agent axis), the
// provider's declared AdditionalOptionIDs (effort where applicable, Codex options, the ACP
// server config options), and its PersistedOnlyOptionIDs (Pi's pi_provider). It is the
// not-running floor UpdateAgentSettings validates against; for a running or previously-run
// agent the caller additionally unions in the live/persisted catalog, so a newly
// server-reported config option is accepted even before it is added here. An unknown provider
// yields just {"model"}.
func (r *Registry) KnownOptionIDs(provider leapmuxv1.AgentProvider) map[string]bool {
	ids := map[string]bool{OptionIDModel: true}
	reg, ok := r.byProvider[provider]
	if !ok {
		return ids
	}
	for _, g := range reg.OptionGroups {
		ids[g.GetId()] = true
	}
	for _, id := range reg.AdditionalOptionIDs {
		ids[id] = true
	}
	for _, id := range reg.PersistedOnlyOptionIDs {
		ids[id] = true
	}
	return ids
}

// DefaultModelEnvOverride returns the value of the provider's
// LEAPMUX_*_DEFAULT_MODEL environment variable, or "" if unset. It is the
// explicit operator override that takes precedence over both a CLI-reported
// default and the static catalog's preferred model (see defaultModelIDForList).
func (r *Registry) DefaultModelEnvOverride(provider leapmuxv1.AgentProvider) string {
	return r.byProvider[provider].defaultModelEnvOverride()
}

// DefaultModel returns the default model ID for a provider, checking the
// provider's environment variable first, then falling back to the model
// marked IsDefault in the registered model list. "" for an unknown provider.
func (r *Registry) DefaultModel(provider leapmuxv1.AgentProvider) string {
	return r.byProvider[provider].DefaultModel()
}

// NormalizeModelID canonicalizes a provider's model id into the alias space the
// provider stores and compares against, so two spellings of the same model -- e.g.
// the CLI's fully-qualified "claude-opus-4-8[1m]" and the alias "opus[1m]" -- compare
// equal. Providers without an alias space return the id unchanged. Used by the
// settings-change notification so a model that merely re-normalizes (not a user
// switch) isn't reported as a change.
func (r *Registry) NormalizeModelID(provider leapmuxv1.AgentProvider, model string) string {
	if fn := r.byProvider[provider].NormalizeModelID; fn != nil {
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
func (r *Registry) EffortEnvOverride(provider leapmuxv1.AgentProvider) string {
	key := r.byProvider[provider].EnvEffortKey
	if key == "" {
		return ""
	}
	return os.Getenv(key)
}

// ManagesEffort reports whether leapmux owns a model-dependent effort
// default for this provider -- i.e. its effort tiers belong to the model. A provider
// with a STATIC catalog states them in DefaultModels (Claude, Codex, Pi). Native
// Copilot reads its catalog from the open session, so it has no static entry to state
// them in and sets Registration.ManagesEffort instead. For all of them,
// resolveProviderDefaults stamps an effort default into the launch options, and
// providerHasModelDependentGroups rebuilds the effort tiers on a model change.
//
// An ACP provider's reasoning axis, where it has one (OpenCode's and Kilo's reasoning
// effort, Goose's thinking effort), is a server-driven config option that does NOT
// depend on the model; leapmux must not stamp a default for one, or it would collide
// with the server's own value (that config option's id is OptionIDEffort) and store an
// inert key.
func (r *Registry) ManagesEffort(provider leapmuxv1.AgentProvider) bool {
	reg, ok := r.byProvider[provider]
	if !ok {
		return false
	}
	if reg.ManagesEffort {
		return true
	}
	for _, m := range reg.DefaultModels {
		if m != nil && len(m.SupportedEfforts) > 0 {
			return true
		}
	}
	return false
}

// StaticOptionGroups returns the provider's static option-group templates, or nil
// for an unknown provider. The slice is the Registration's own and is READ-ONLY.
func (r *Registry) StaticOptionGroups(provider leapmuxv1.AgentProvider) []*leapmuxv1.AvailableOptionGroup {
	return r.byProvider[provider].OptionGroups
}

// ListAvailable returns providers whose program is found in the user's shell
// environment. Checks run concurrently to minimize latency when login shells are
// used (each check reads shell profiles).
//
// The second return is false when the scan did not finish under ctx — at
// least one probe was cut short, so the list is NOT evidence of absence.
// The caller must treat that as a retryable failure, not as "none
// installed": probes that DID complete are cached, so a retry only
// re-runs what was cut short.
func (r *Registry) ListAvailable(ctx context.Context, shellPath string, useLoginShell bool) ([]leapmuxv1.AgentProvider, bool) {
	providers := r.providers
	found := make([]bool, len(providers))
	// settled[i] reports whether every probe this check ran ESTABLISHED
	// something. A check that never found its binary and whose last probe
	// proved nothing is not evidence of absence.
	settled := make([]bool, len(providers))
	var wg sync.WaitGroup
	for i, provider := range providers {
		wg.Add(1)
		go func(idx int, l launch.Locator) {
			defer wg.Done()
			res := l.Available(ctx, shellPath, useLoginShell)
			found[idx] = res == launch.Found
			settled[idx] = res != launch.Unknown
		}(i, r.byProvider[provider].Locator)
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
	for i := range providers {
		if !found[i] && !settled[i] {
			return nil, false
		}
	}

	var result []leapmuxv1.AgentProvider
	for i, provider := range providers {
		if found[i] {
			result = append(result, provider)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result, true
}
