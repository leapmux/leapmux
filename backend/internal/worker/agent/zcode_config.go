package agent

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// ZCode's credentials and model catalog live in the desktop application's own
// configuration file, `~/.zcode/v2/config.json`. The app-server holds none of its
// own: the desktop application pushes them with workspace/updateProviderRegistry
// before it creates a session, and without that push every turn fails with
// `provider_not_configured`.
//
// This file READS that configuration and translates it into the two payloads the
// app-server accepts. LeapMux never writes it: it is another application's file,
// and a user who changes a provider in ZCode must not find the change reverted.

// zcodeConfigRelPath is the configuration file's path under the user's home
// directory.
var zcodeConfigRelPath = []string{".zcode", "v2", "config.json"}

// zcodeConfigPath returns the configuration file's absolute path for a home
// directory, or "" when no home directory is known.
func zcodeConfigPath(homeDir string) string {
	if homeDir == "" {
		return ""
	}
	return filepath.Join(append([]string{homeDir}, zcodeConfigRelPath...)...)
}

// --- the on-disk shape ---

// zcodeConfigFile is the subset of ZCode's configuration LeapMux reads.
type zcodeConfigFile struct {
	Provider map[string]zcodeConfigProvider `json:"provider"`
}

type zcodeConfigProvider struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Source  string `json:"source"`
	Enabled *bool  `json:"enabled"`
	Options struct {
		APIKey  string `json:"apiKey"`
		BaseURL string `json:"baseURL"`
	} `json:"options"`
	Models map[string]zcodeConfigModel `json:"models"`
}

type zcodeConfigModel struct {
	Name      string `json:"name"`
	Reasoning *struct {
		Enabled        bool     `json:"enabled"`
		Variants       []string `json:"variants"`
		DefaultVariant string   `json:"defaultVariant"`
	} `json:"reasoning"`
	Limit *struct {
		Context int64 `json:"context"`
		Output  int64 `json:"output"`
	} `json:"limit"`
	Modalities *struct {
		Input  []string `json:"input"`
		Output []string `json:"output"`
	} `json:"modalities"`
	// ZCode is the desktop application's own block. `priority` RANKS the models,
	// lower first, and it is the only ordering ZCode's configuration states -- the
	// map that holds the models has no order of its own. It decides which model a
	// fresh session runs on, so it is read rather than ignored. See
	// zcodeModelOrder.
	ZCode *struct {
		Priority *float64 `json:"priority"`
	} `json:"zcode"`
}

// priority returns the model's rank and whether the configuration states one.
func (m zcodeConfigModel) priority() (float64, bool) {
	if m.ZCode == nil || m.ZCode.Priority == nil {
		return 0, false
	}
	return *m.ZCode.Priority, true
}

// --- the wire shape ---

// zcodeRegistryProvider is one entry of the workspace/updateProviderRegistry
// payload.
//
// The app-server validates it STRICTLY: an unknown field, a `kind` outside its
// enumeration, or an empty `models` array refuses the whole request, not just the
// offending provider. Every field below is therefore normalized before it is sent.
type zcodeRegistryProvider struct {
	ProviderID string               `json:"providerId"`
	Kind       string               `json:"kind"`
	APIFormat  string               `json:"apiFormat,omitempty"`
	BaseURL    string               `json:"baseURL,omitempty"`
	Label      string               `json:"label,omitempty"`
	Source     string               `json:"source,omitempty"`
	APIKey     *zcodeRegistryAPIKey `json:"apiKey,omitempty"`
	Models     []zcodeRegistryModel `json:"models"`
}

// zcodeRegistryAPIKey is the app-server's tagged union for a credential. `inline`
// is the only member LeapMux can produce: the alternatives name a keychain entry
// or an OAuth session that belongs to the desktop application.
type zcodeRegistryAPIKey struct {
	Source string `json:"source"`
	Value  string `json:"value"`
}

// zcodeAPIKeySourceInline is the discriminator of the union member that carries the
// key VALUE. The other members name a keychain entry, an environment variable, or a
// server-side key, none of which LeapMux can resolve on the app-server's behalf.
const zcodeAPIKeySourceInline = "inline"

type zcodeRegistryModel struct {
	ModelID         string                  `json:"modelId"`
	Label           string                  `json:"label,omitempty"`
	ContextWindow   int64                   `json:"contextWindow,omitempty"`
	MaxOutputTokens int64                   `json:"maxOutputTokens,omitempty"`
	Reasoning       *zcodeRegistryReasoning `json:"reasoning,omitempty"`
	SupportsImages  bool                    `json:"supportsImages,omitempty"`
	SupportsPdf     bool                    `json:"supportsPdf,omitempty"`
	SupportsVideo   bool                    `json:"supportsVideo,omitempty"`
}

type zcodeRegistryReasoning struct {
	Enabled      bool                  `json:"enabled"`
	Levels       []zcodeReasoningLevel `json:"levels,omitempty"`
	DefaultLevel string                `json:"defaultLevel,omitempty"`
}

type zcodeReasoningLevel struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// zcodeModelRef is the app-server's {providerId, modelId} pair.
type zcodeModelRef struct {
	ProviderID string `json:"providerId"`
	ModelID    string `json:"modelId"`
}

// zcodeRuntimeModel is the per-request overlay session/setModel takes.
//
// It exists because the registry push is workspace-scoped and asynchronous, while
// setModel must resolve a credential NOW: the overlay carries the provider (with
// its inline key) alongside the model, so pinning a model never races the registry.
type zcodeRuntimeModel struct {
	Revision    string                `json:"revision"`
	GeneratedAt int64                 `json:"generatedAt"`
	Model       zcodeModelRef         `json:"model"`
	Provider    zcodeRegistryProvider `json:"provider"`
	// ThoughtLevel rides along so a create or a model switch applies the level in
	// the SAME request. Omitted when empty, which keeps the app-server's own default.
	ThoughtLevel string `json:"thoughtLevel,omitempty"`
}

// zcodeCatalog is the parsed, translated view of ZCode's configuration: the
// registry payload plus the per-model capability facts LeapMux needs (thought
// levels, context window, accepted input modalities).
type zcodeCatalog struct {
	// Providers is the registry payload, ordered deterministically (see
	// buildZCodeCatalog) so a session's default provider does not depend on Go's
	// map iteration order.
	Providers []zcodeRegistryProvider
	// Models is every (provider, model) pair the registry carries, in the same
	// order, projected into LeapMux's catalog shape. The id is the composite
	// zcodeModelID.
	Models []*ModelInfo
	// modalities maps a composite model id to the input modalities its
	// configuration declares, lowercased. Absent for a model whose configuration
	// declares none, which the capability gate treats as "text only".
	modalities map[string][]string
	// refs maps a composite model id back to its {providerId, modelId} pair and the
	// provider entry that carries its credential.
	refs map[string]zcodeModelRef
}

// zcodeModelIDSeparator joins a provider id and a model id into the single string
// LeapMux's option groups carry.
//
// `/` is the separator the app-server itself uses when it spells a composite model
// (`session.updated.model` reports "builtin:zai-coding-plan/GLM-5.3"), and no
// provider id contains one -- so splitting on the FIRST separator is unambiguous
// and the two spellings agree.
const zcodeModelIDSeparator = "/"

// zcodeModelID builds the composite catalog id for a model.
func zcodeModelID(providerID, modelID string) string {
	if providerID == "" {
		return modelID
	}
	return providerID + zcodeModelIDSeparator + modelID
}

// splitZCodeModelID splits a composite catalog id. ok is false for a bare model id
// (no separator), which the caller resolves against the catalog instead.
func splitZCodeModelID(id string) (providerID, modelID string, ok bool) {
	providerID, modelID, ok = strings.Cut(id, zcodeModelIDSeparator)
	if !ok || providerID == "" || modelID == "" {
		return "", "", false
	}
	return providerID, modelID, true
}

// normalizeZCodeModelID canonicalizes a composite model id.
//
// It accepts a backslash-separated spelling as well, because a value that travels
// through a Windows shell or a hand-written configuration reaches us that way, and
// a model id that merely re-spells its separator must not read as a model SWITCH
// (which is what the settings-change notification compares).
func normalizeZCodeModelID(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return ""
	}
	return strings.ReplaceAll(model, `\`, zcodeModelIDSeparator)
}

// The provider `kind` values the app-server accepts. The field is REQUIRED and
// validated against exactly this set, so a configured kind outside it cannot be
// pushed at all.
const (
	zcodeKindAnthropic        = "anthropic"
	zcodeKindOpenAI           = "openai"
	zcodeKindOpenAICompatible = "openai-compatible"
)

// The provider `source` values the app-server accepts.
var zcodeRegistrySources = map[string]bool{
	"builtin": true, "models-dev": true, "custom": true,
	"user": true, "workspace": true, "ephemeral": true,
}

// zcodeProviderKind maps a configured provider `kind` onto the app-server's
// enumeration. ok is false for a kind outside it, and the caller then SKIPS that
// provider: the request is validated as a whole, so one unrepresentable provider
// would otherwise refuse every provider with it.
func zcodeProviderKind(kind string) (string, bool) {
	switch k := strings.ToLower(strings.TrimSpace(kind)); k {
	case zcodeKindAnthropic, zcodeKindOpenAI, zcodeKindOpenAICompatible:
		return k, true
	default:
		return "", false
	}
}

// zcodeRegistrySource maps a configured `source` onto the app-server's
// enumeration, falling back to "custom" for anything else. A fallback is safe here
// (unlike for `kind`) because the field only labels where the entry came from: it
// selects no request format and no credential.
func zcodeRegistrySource(source string) string {
	s := strings.ToLower(strings.TrimSpace(source))
	if zcodeRegistrySources[s] {
		return s
	}
	return "custom"
}

// zcodeAPIFormat maps a configured provider `kind` onto the app-server's
// `apiFormat`. An unrecognized kind yields "", which omits the field and lets the
// app-server infer it -- better than guessing wrong for a provider added later.
func zcodeAPIFormat(kind string) string {
	switch k, _ := zcodeProviderKind(kind); k {
	case zcodeKindAnthropic:
		return "anthropic-messages"
	case zcodeKindOpenAI, zcodeKindOpenAICompatible:
		return "openai-chat-completions"
	default:
		return ""
	}
}

// zcodeProviderSkip records why buildZCodeCatalog dropped one configured provider.
// The three skips have three different remedies, so the reason travels with the
// provider id rather than being guessed at by the caller.
type zcodeProviderSkip struct {
	ProviderID string
	Reason     string
}

// loadZCodeCatalog reads and translates ZCode's configuration.
//
// An absent file, an unreadable one, and a malformed one are all reported as
// errors: without a provider registry every turn fails with a message that identifies
// the app-server rather than the missing configuration, so the caller states the
// real cause at startup.
func loadZCodeCatalog(homeDir string) (zcodeCatalog, error) {
	path := zcodeConfigPath(homeDir)
	if path == "" {
		return zcodeCatalog{}, fmt.Errorf("no home directory to read ZCode's configuration from")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return zcodeCatalog{}, fmt.Errorf("ZCode is not configured: %s does not exist (sign in with the ZCode application once)", path)
		}
		return zcodeCatalog{}, fmt.Errorf("read %s: %w", path, err)
	}
	var cfg zcodeConfigFile
	if err := json.Unmarshal(data, &cfg); err != nil {
		return zcodeCatalog{}, fmt.Errorf("parse %s: %w", path, err)
	}
	catalog, skipped := buildZCodeCatalog(cfg)
	if len(catalog.Providers) == 0 {
		return catalog, fmt.Errorf("no ZCode model provider in %s is usable%s", path, zcodeSkipDetail(skipped))
	}
	return catalog, nil
}

// zcodeSkipDetail renders why each configured provider was dropped, as a suffix to the
// "no usable provider" error.
//
// The three skips have three different remedies -- add a key, add a model, use a
// provider kind the app-server supports -- so one message that always says "carries no
// API key" sends the user to re-enter a credential that is already correct. Returns ""
// for a configuration that lists no provider at all, where there is nothing to explain.
func zcodeSkipDetail(skipped []zcodeProviderSkip) string {
	if len(skipped) == 0 {
		return " (it lists no model provider)"
	}
	parts := make([]string, 0, len(skipped))
	for _, s := range skipped {
		parts = append(parts, fmt.Sprintf("%q %s", s.ProviderID, s.Reason))
	}
	return ": " + strings.Join(parts, "; ")
}

// buildZCodeCatalog translates a parsed configuration into the registry payload
// and LeapMux's model catalog.
//
// Only a provider that carries an API KEY is pushed: one without a credential
// cannot serve a turn, and the app-server picks the FIRST registry entry as a
// session's default -- so a keyless entry at the front would make every fresh
// session fail until the model is pinned.
//
// `enabled` is a PREFERENCE, not a filter. An enabled provider is pushed ahead of
// a disabled one, so the default lands on a provider the user actually chose, but a
// disabled provider that holds a key is still offered: ZCode disables a provider
// for reasons of its own (an inactive OAuth plan, an entitlement it could not
// verify), and dropping it would leave a user whose every provider is disabled with
// no models at all rather than a working choice.
func buildZCodeCatalog(cfg zcodeConfigFile) (zcodeCatalog, []zcodeProviderSkip) {
	type candidate struct {
		providerID string
		enabled    bool
		apiKey     string
		baseURL    string
		provider   zcodeConfigProvider
	}
	var candidates []candidate
	var skipped []zcodeProviderSkip
	for providerID, provider := range cfg.Provider {
		// Trim ONCE here, and carry the trimmed value onto the candidate. The emptiness
		// test and the pushed credential must be the same string: a key the test accepted
		// only after trimming, but that reached the registry with its padding, puts a
		// newline in an Authorization header and fails every turn upstream.
		apiKey := strings.TrimSpace(provider.Options.APIKey)
		if apiKey == "" {
			skipped = append(skipped, zcodeProviderSkip{providerID, "carries no API key"})
			continue
		}
		// A provider with no model cannot be pushed: the app-server requires at least
		// one entry in `models`. It could serve no turn anyway.
		if len(provider.Models) == 0 {
			skipped = append(skipped, zcodeProviderSkip{providerID, "lists no model"})
			continue
		}
		// A `kind` outside the app-server's enumeration cannot be represented, and the
		// registry is validated as a whole -- so one such provider would refuse them
		// all. Skip it and keep the rest working.
		if _, ok := zcodeProviderKind(provider.Kind); !ok {
			skipped = append(skipped, zcodeProviderSkip{providerID,
				fmt.Sprintf("uses the unsupported kind %q", provider.Kind)})
			continue
		}
		candidates = append(candidates, candidate{
			providerID: providerID,
			enabled:    provider.Enabled != nil && *provider.Enabled,
			apiKey:     apiKey,
			baseURL:    strings.TrimSpace(provider.Options.BaseURL),
			provider:   provider,
		})
	}
	// Enabled first, then by provider id. Deterministic in both keys, because a Go
	// map yields its entries in a different order on every run and the app-server
	// reads the first entry as the session default.
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].enabled != candidates[j].enabled {
			return candidates[i].enabled
		}
		return candidates[i].providerID < candidates[j].providerID
	})
	// A Go map also yields the SKIPS in a different order on every run, and they reach
	// the user inside an error string.
	sort.Slice(skipped, func(i, j int) bool { return skipped[i].ProviderID < skipped[j].ProviderID })

	// Which configured names more than one candidate carries. A name that names
	// two providers names neither, so zcodeProviderLabel replaces it with the
	// label the id composes -- see it for the CLI defect this repairs.
	nameCount := map[string]int{}
	for _, c := range candidates {
		if name := strings.TrimSpace(c.provider.Name); name != "" {
			nameCount[name]++
		}
	}

	catalog := zcodeCatalog{
		modalities: map[string][]string{},
		refs:       map[string]zcodeModelRef{},
	}
	for _, c := range candidates {
		kind, _ := zcodeProviderKind(c.provider.Kind)
		entry := zcodeRegistryProvider{
			ProviderID: c.providerID,
			Kind:       kind,
			APIFormat:  zcodeAPIFormat(c.provider.Kind),
			BaseURL:    c.baseURL,
			Label:      zcodeProviderLabel(c.providerID, c.provider.Name, nameCount[strings.TrimSpace(c.provider.Name)] > 1),
			Source:     zcodeRegistrySource(c.provider.Source),
			APIKey:     &zcodeRegistryAPIKey{Source: zcodeAPIKeySourceInline, Value: c.apiKey},
			// An empty slice, never nil: the app-server's schema requires the
			// `models` ARRAY, and a nil slice marshals as `null`, which its
			// validator refuses for the whole request.
			Models: []zcodeRegistryModel{},
		}
		for _, modelID := range zcodeModelOrder(c.provider.Models) {
			model := c.provider.Models[modelID]
			entry.Models = append(entry.Models, zcodeRegistryModelFor(modelID, model))
			composite := zcodeModelID(c.providerID, modelID)
			catalog.Models = append(catalog.Models, zcodeModelInfo(composite, entry.Label, modelID, model))
			catalog.refs[composite] = zcodeModelRef{ProviderID: c.providerID, ModelID: modelID}
			if model.Modalities != nil && len(model.Modalities.Input) > 0 {
				lowered := make([]string, 0, len(model.Modalities.Input))
				for _, m := range model.Modalities.Input {
					lowered = append(lowered, strings.ToLower(strings.TrimSpace(m)))
				}
				catalog.modalities[composite] = lowered
			}
		}
		catalog.Providers = append(catalog.Providers, entry)
	}
	markZCodeDefaultModel(catalog.Models)
	return catalog, skipped
}

// zcodeModelOrder ranks a provider's models the way ZCode itself ranks them:
// `zcode.priority` ascending, then by model id.
//
// The order is load-bearing twice over. It is the order of the `models` array in
// the registry push, and the app-server starts a session on the FIRST model of the
// FIRST provider -- so an alphabetical order would open every ZCode agent on
// whichever model sorts first, which for the shipped Z.ai plans is the weakest one.
// It is also the order of the catalog LeapMux shows, and markZCodeDefaultModel
// marks its first entry.
//
// A model that states no priority sorts LAST, behind every ranked one: an entry the
// user added by hand has no claim to be the default, and the id keeps the order
// stable among such models.
func zcodeModelOrder(models map[string]zcodeConfigModel) []string {
	// Ascending model id, so a payload built from a Go map is byte-stable across runs.
	ids := slices.Sorted(maps.Keys(models))
	sort.SliceStable(ids, func(i, j int) bool {
		left, leftOK := models[ids[i]].priority()
		right, rightOK := models[ids[j]].priority()
		if leftOK != rightOK {
			return leftOK
		}
		if leftOK && left != right {
			return left < right
		}
		return false
	})
	return ids
}

// zcodeModelName picks how one model READS: its configured `name`, or its id.
//
// The id wins when the name differs from it by CASE alone. ZCode ships
// GLM-5-Turbo with `"name": "glm-5-turbo"`, while the id is the spelling the CLI
// itself uses everywhere the model is named -- the config key, the composite id
// in `session.updated.model`, and its own catalog. A lowercase copy of the id
// carries nothing the id does not, so it is not a name.
//
// A name that differs by more than case DOES carry something the id cannot, so
// it wins. That is the same rule zcodeEffortTier applies to a level's label.
//
// The "no name given" half routes through nameOrID, which every other naming
// helper in this file already uses, so this states ONE added rule on top of the
// shared one rather than a second answer to the same question.
func zcodeModelName(modelID string, model zcodeConfigModel) string {
	name := nameOrID(model.Name, modelID)
	if strings.EqualFold(name, modelID) {
		return modelID
	}
	return name
}

// zcodeBuiltinPrefix marks a provider the ZCode CLI created itself. The CLI
// composes such an id as `builtin:<family>[-<kind>]`.
const zcodeBuiltinPrefix = "builtin:"

// zcodeProviderFamilies and zcodeProviderPlans decompose a built-in provider id.
//
// The CLI's own ids are that cross product (`gs` in its bundle: the Z.ai and
// BigModel families crossed with the API-key, Coding Plan and Start Plan kinds),
// so LeapMux states the two axes rather than the six products they make. A
// seventh built-in the CLI adds -- `builtin:zai-pro-plan` -- is then named
// correctly with no change here, where a table of six literals would have let it
// fall through to the CLI's own name.
//
// Neither axis is derivable from its id: "Z.ai" is not a case fold of `zai`, and
// "API Key" is not a fold of the empty suffix.
var (
	zcodeProviderFamilies = map[string]string{
		"zai":      "Z.ai",
		"bigmodel": "BigModel",
	}
	zcodeProviderPlans = map[string]string{
		"":             "API Key",
		"coding-plan":  "Coding Plan",
		"start-plan":   "Start Plan",
		"pro-plan":     "Pro Plan",
		"lite-plan":    "Lite Plan",
		"max-plan":     "Max Plan",
		"free-plan":    "Free Plan",
		"api-key":      "API Key",
		"subscription": "Subscription",
	}
)

// zcodeBuiltinProviderLabel composes the display label for a provider the CLI
// created, or "" for an id it did not.
//
// An unknown FAMILY returns "": LeapMux has nothing to call it, so the CLI's own
// name is still the best answer. An unknown PLAN is title-cased from its own
// suffix, which reads correctly for anything the CLI is likely to add.
func zcodeBuiltinProviderLabel(providerID string) string {
	rest, ok := strings.CutPrefix(providerID, zcodeBuiltinPrefix)
	if !ok {
		return ""
	}
	family, plan, _ := strings.Cut(rest, "-")
	familyLabel, known := zcodeProviderFamilies[family]
	if !known {
		return ""
	}
	planLabel, known := zcodeProviderPlans[plan]
	if !known {
		planLabel = titleCaseHyphenated(plan)
	}
	return familyLabel + " - " + planLabel
}

// titleCaseHyphenated turns `pro-plan` into `Pro Plan`.
func titleCaseHyphenated(s string) string {
	words := strings.Split(s, "-")
	for i, w := range words {
		words[i] = capitalizeFirst(w)
	}
	return strings.Join(words, " ")
}

// zcodeProviderLabel returns the display label for one provider, correcting the
// CLI's own built-ins where their names are unusable.
//
// The CLI writes a built-in's name WRONG in two ways. An installation logged
// into both Z.ai plans holds `builtin:zai-coding-plan` and
// `builtin:zai-start-plan` under the SAME name, "Z.ai - Coding Plan"; and the
// BigModel start plan arrives as "BigModel- Coding Plan", which is simply
// incorrect. A model list that took those verbatim offered rows that read
// identically and resumed different plans.
//
// `collides` says another enabled provider carries this same name, which is the
// first of those two. The composed label ALSO wins when the id's own plan
// disagrees with the configured name, which is the second: the CLI names the
// plan in the id, and an id that says `start-plan` beside a name that says
// "Coding Plan" cannot both be right, and the id is the one the CLI resolves by.
//
// Otherwise the configured name stands. A user who renames `builtin:zai` to
// "Work account" in their own `config.json` means it, and LeapMux never writes
// that file -- so discarding the rename would leave a user with no way to tell
// two accounts apart and no message saying why.
//
// ONE label serves both the registry push and LeapMux's catalog. The registry's
// copy is display-only -- the app-server resolves a model by the composite
// `providerID/modelID`, never by a label -- so a second, uncorrected spelling
// would only be a copy that can disagree with what the user sees.
func zcodeProviderLabel(providerID, configuredName string, collides bool) string {
	builtin := zcodeBuiltinProviderLabel(providerID)
	if builtin == "" {
		return nameOrID(configuredName, providerID)
	}
	name := strings.TrimSpace(configuredName)
	if name == "" || collides || zcodeNameContradictsPlan(providerID, name) {
		return builtin
	}
	return name
}

// zcodeNameContradictsPlan reports that a built-in's configured name claims a
// DIFFERENT plan from the one its id states.
//
// The check is on the plan words alone, so a genuine rename ("Work account")
// keeps its name: it claims no plan at all. It catches exactly the CLI's own
// defect, where `builtin:bigmodel-start-plan` arrives named "BigModel- Coding
// Plan".
func zcodeNameContradictsPlan(providerID, configuredName string) bool {
	rest, ok := strings.CutPrefix(providerID, zcodeBuiltinPrefix)
	if !ok {
		return false
	}
	_, plan, _ := strings.Cut(rest, "-")
	mine := zcodeProviderPlans[plan]
	lower := strings.ToLower(configuredName)
	for suffix, label := range zcodeProviderPlans {
		if suffix == plan || label == mine {
			continue
		}
		if strings.Contains(lower, strings.ToLower(label)) {
			return true
		}
	}
	return false
}

// zcodeRegistryModelFor translates one configured model into its registry entry.
func zcodeRegistryModelFor(modelID string, model zcodeConfigModel) zcodeRegistryModel {
	entry := zcodeRegistryModel{
		ModelID: modelID,
		Label:   zcodeModelName(modelID, model),
	}
	if model.Limit != nil {
		entry.ContextWindow = model.Limit.Context
		entry.MaxOutputTokens = model.Limit.Output
	}
	if r := model.Reasoning; r != nil && r.Enabled && len(r.Variants) > 0 {
		levels := make([]zcodeReasoningLevel, 0, len(r.Variants))
		for _, v := range r.Variants {
			levels = append(levels, zcodeReasoningLevel{Value: v, Label: v})
		}
		entry.Reasoning = &zcodeRegistryReasoning{
			Enabled:      true,
			Levels:       levels,
			DefaultLevel: r.DefaultVariant,
		}
	}
	// The support flags are what the app-server's own attachment gate reads, so
	// declaring them from the configured modalities keeps its refusal and LeapMux's
	// refusal (zcodeModelAcceptsAttachment) in agreement.
	if model.Modalities != nil {
		for _, m := range model.Modalities.Input {
			switch strings.ToLower(strings.TrimSpace(m)) {
			case zcodeModalityImage:
				entry.SupportsImages = true
			case zcodeModalityPDF:
				entry.SupportsPdf = true
			case zcodeModalityVideo:
				entry.SupportsVideo = true
			}
		}
	}
	return entry
}

// ZCode input modalities a model's configuration can declare.
const (
	zcodeModalityText  = "text"
	zcodeModalityImage = "image"
	zcodeModalityPDF   = "pdf"
	zcodeModalityVideo = "video"
)

// zcodeModelDisplayName joins a model's name to the provider that serves it.
//
// A provider label is never empty in practice -- zcodeProviderLabel falls back
// to the provider id -- so the guard is for a caller that has no provider to
// label at all, and it produces the bare model name rather than empty brackets.
func zcodeModelDisplayName(modelName, providerLabel string) string {
	if providerLabel == "" {
		return modelName
	}
	return fmt.Sprintf("%s (%s)", modelName, providerLabel)
}

// zcodeModelInfo projects one configured model into LeapMux's catalog entry.
//
// The thought levels come from the model's own `reasoning.variants`: they are
// MODEL-dependent (GLM-5.3 offers low/high/max, GLM-5-Turbo offers enabled/off),
// so a hardcoded list would offer a level the app-server refuses.
func zcodeModelInfo(composite, providerLabel, modelID string, model zcodeConfigModel) *ModelInfo {
	info := &ModelInfo{
		Id: composite,
		// The provider is PART OF THE NAME, not a description beside it. The same
		// model id is offered by more than one ZCode provider -- a coding plan, a
		// start plan and a plain API key all reach GLM-5.3-Flash -- and a
		// description renders as a tooltip, so the list showed two or three rows
		// that read identically and resumed different plans. Which one a row spends
		// is the first thing the reader needs, so it goes where a reader sees it.
		//
		// Every row carries it, including an installation that configured one
		// provider: which provider a model runs on is a fact about the model, not
		// something that becomes true once a second provider exists, and a suffix
		// that appears the day a user adds one reads as a different model.
		DisplayName: zcodeModelDisplayName(zcodeModelName(modelID, model), providerLabel),
	}
	if model.Limit != nil {
		info.ContextWindow = model.Limit.Context
	}
	if r := model.Reasoning; r != nil && r.Enabled && len(r.Variants) > 0 {
		efforts := make([]*EffortInfo, 0, len(r.Variants))
		for _, v := range r.Variants {
			efforts = append(efforts, zcodeEffortTier(v, "", ""))
		}
		// Auto first -- LeapMux's sentinel for "do not send setThoughtLevel at
		// all", so a user can keep whatever default the app-server resolves --
		// then the variants strongest first, because `config.json` states them in
		// no order at all: ZCode ships GLM-5.3 as `["low", "max", "high"]`, and a
		// menu built in that order led with the weakest level.
		info.SupportedEfforts = zcodeEffortsWithAuto(efforts)
		info.DefaultEffort = r.DefaultVariant
		if info.DefaultEffort == "" {
			info.DefaultEffort = EffortAuto
		}
	}
	return info
}

// markZCodeDefaultModel flags the catalog's default model.
//
// The configuration specifies no default, so the FIRST entry is used -- which is the
// same model the app-server itself starts a session on. The providers are ordered
// enabled-first and their models by ZCode's own `zcode.priority` (see
// zcodeModelOrder), so the choice is stable and lands on the best model of a
// provider the user enabled.
func markZCodeDefaultModel(models []*ModelInfo) {
	for _, m := range models {
		if m != nil {
			m.IsDefault = true
			return
		}
	}
}

// registryPayload builds the workspace/updateProviderRegistry params.
//
// The providers, the revision, and the timestamp sit INSIDE a `registry` object:
// a payload that puts them at the top level is refused by the app-server's
// validator with an "unrecognized keys" report.
func (c zcodeCatalog) registryPayload(workspace zcodeWorkspace, revision string, generatedAt int64) map[string]any {
	return map[string]any{
		"workspace": workspace,
		"registry": map[string]any{
			"providers":   c.Providers,
			"generatedAt": generatedAt,
			"revision":    revision,
		},
	}
}

// runtimeModelFor builds the session/setModel overlay for a composite model id, or
// ok=false when the catalog does not hold it.
func (c zcodeCatalog) runtimeModelFor(modelID, revision string, generatedAt int64) (zcodeRuntimeModel, bool) {
	ref, ok := c.refs[modelID]
	if !ok {
		return zcodeRuntimeModel{}, false
	}
	for _, provider := range c.Providers {
		if provider.ProviderID != ref.ProviderID {
			continue
		}
		return zcodeRuntimeModel{
			Revision:    revision,
			GeneratedAt: generatedAt,
			Model:       ref,
			Provider:    provider,
		}, true
	}
	return zcodeRuntimeModel{}, false
}

// hasInlineAPIKey reports whether the catalog carries a usable inline credential
// for a provider id.
//
// It answers the app-server's interaction/requestProviderRuntimeHeaders honestly:
// LeapMux mints no OAuth header, so the only authorization it can claim is the key
// it already pushed with the registry. An empty provider id asks about ANY
// provider, which is what a request that gives none means.
func (c zcodeCatalog) hasInlineAPIKey(providerID string) bool {
	for _, provider := range c.Providers {
		if providerID != "" && provider.ProviderID != providerID {
			continue
		}
		if provider.APIKey != nil && provider.APIKey.Source == zcodeAPIKeySourceInline && provider.APIKey.Value != "" {
			return true
		}
	}
	return false
}

// resolveModelID maps whatever spelling a caller supplied onto a catalog id.
//
// A composite id is matched exactly. A BARE model id (what a user types as
// `--model GLM-5.3`) is resolved against the catalog, preferring the first
// provider that offers it -- which is the enabled one, by the catalog's ordering.
// Resolution needs the catalog and therefore cannot live in the pure
// setModelIDNormalizer hook, which is why the two are separate.
func (c zcodeCatalog) resolveModelID(model string) (string, bool) {
	model = normalizeZCodeModelID(model)
	if model == "" {
		return "", false
	}
	if _, ok := c.refs[model]; ok {
		return model, true
	}
	if _, _, composite := splitZCodeModelID(model); composite {
		return "", false
	}
	for _, m := range c.Models {
		if _, modelID, ok := splitZCodeModelID(m.GetId()); ok && modelID == model {
			return m.GetId(), true
		}
	}
	return "", false
}

// defaultThoughtLevel returns the thought level ZCode's configuration declares for a
// model, or "" when it declares none.
//
// It is what LeapMux's Auto sentinel resolves to. Auto cannot mean "send nothing":
// a session that is told no level runs on the app-server's own fallback, which is
// the LOWEST level rather than the model's default -- so "Use ZCode's default
// thought level for the model" would silently deliver the weakest one.
func (c zcodeCatalog) defaultThoughtLevel(modelID string) string {
	modelID = normalizeZCodeModelID(modelID)
	for _, m := range c.Models {
		if m.GetId() != modelID {
			continue
		}
		if level := m.DefaultEffort; level != "" && level != EffortAuto {
			return level
		}
		return ""
	}
	return ""
}

// acceptsInputModality reports whether a model's configuration declares an input
// modality. A model that declares none is treated as TEXT ONLY, which is what the
// app-server assumes for it too.
func (c zcodeCatalog) acceptsInputModality(modelID, modality string) bool {
	declared, ok := c.modalities[normalizeZCodeModelID(modelID)]
	if !ok {
		return modality == zcodeModalityText
	}
	for _, m := range declared {
		if m == modality {
			return true
		}
	}
	return false
}

// zcodeWorkspace is the app-server's workspace identity. Both fields carry the
// same path: `workspaceKey` is the app-server's own index key, and the desktop
// application sets it to the path.
type zcodeWorkspace struct {
	WorkspacePath string `json:"workspacePath"`
	WorkspaceKey  string `json:"workspaceKey"`
}

func zcodeWorkspaceFor(dir string) zcodeWorkspace {
	return zcodeWorkspace{WorkspacePath: dir, WorkspaceKey: dir}
}
