package codex

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// codexCatalogMaxPages limits discovery from a server that never ends pagination.
const codexCatalogMaxPages = 20

type codexCatalogPage[T any] struct {
	Data       []T     `json:"data"`
	NextCursor *string `json:"nextCursor"`
}

// queryCodexCatalogPages returns only complete catalogs within one deadline.
func queryCodexCatalogPages[T any](a *Agent, method string, params map[string]any, timeout time.Duration) ([]T, error) {
	var deadline time.Time
	if timeout > 0 {
		deadline = a.Clock().Now().Add(timeout)
	}
	var entries []T
	cursor := ""
	seen := make(map[string]struct{})
	for range codexCatalogMaxPages {
		remaining := timeout
		if !deadline.IsZero() {
			remaining = deadline.Sub(a.Clock().Now())
			if remaining <= 0 {
				return nil, fmt.Errorf("%s exceeded the catalog deadline", method)
			}
		}
		pageParams := maps.Clone(params)
		if pageParams == nil {
			pageParams = make(map[string]any)
		}
		if cursor != "" {
			pageParams["cursor"] = cursor
		}
		encoded, err := json.Marshal(pageParams)
		if err != nil {
			return nil, fmt.Errorf("marshal %s parameters: %w", method, err)
		}
		raw, err := a.SendRequest(method, encoded, remaining)
		if err != nil {
			return nil, err
		}
		var page codexCatalogPage[T]
		if err := json.Unmarshal(raw, &page); err != nil {
			return nil, fmt.Errorf("decode %s catalog: %w", method, err)
		}
		if page.Data == nil {
			return nil, fmt.Errorf("%s catalog has no data array", method)
		}
		entries = append(entries, page.Data...)
		if page.NextCursor == nil || *page.NextCursor == "" {
			return entries, nil
		}
		cursor = *page.NextCursor
		if _, repeated := seen[cursor]; repeated {
			return nil, fmt.Errorf("%s repeated its pagination cursor", method)
		}
		seen[cursor] = struct{}{}
	}
	return nil, fmt.Errorf("%s exceeded the %d-page catalog limit", method, codexCatalogMaxPages)
}

type codexModelCatalogEntry struct {
	ID                        string `json:"id"`
	Model                     string `json:"model"`
	DisplayName               string `json:"displayName"`
	IsDefault                 bool   `json:"isDefault"`
	Hidden                    bool   `json:"hidden"`
	Description               string `json:"description"`
	DefaultReasoningEffort    string `json:"defaultReasoningEffort"`
	SupportedReasoningEfforts []struct {
		ReasoningEffort string `json:"reasoningEffort"`
		Description     string `json:"description"`
	} `json:"supportedReasoningEfforts"`
}

// queryAvailableModels reads the complete model catalog and converts its entries.
func (a *Agent) queryAvailableModels(timeout time.Duration) []*agent.ModelInfo {
	rawModels, err := queryCodexCatalogPages[codexModelCatalogEntry](a, "model/list", nil, timeout)
	if err != nil {
		slog.Warn("codex model/list failed", "agent_id", a.AgentID(), "error", err)
		return nil
	}

	// Build a lookup from default models so we can fill in missing metadata.
	defaultsByID := make(map[string]*agent.ModelInfo, len(codexDefaultModels))
	for _, d := range codexDefaultModels {
		defaultsByID[d.Id] = d
	}

	var models []*agent.ModelInfo
	for _, m := range rawModels {
		if m.Hidden {
			continue
		}
		id := m.Model
		if id == "" {
			id = m.ID
		}
		// Reverse effort order so highest appears first, and split
		// the server description into a short label + tooltip. Prepend
		// the LeapMux-side "auto" sentinel so users can pick it from
		// the UI even though the CLI never reports it.
		raw := m.SupportedReasoningEfforts
		efforts := make([]*agent.EffortInfo, 0, len(raw)+1)
		efforts = append(efforts, codexAutoEffort())
		for i := len(raw) - 1; i >= 0; i-- {
			e := raw[i]
			efforts = append(efforts, &agent.EffortInfo{
				Id:          e.ReasoningEffort,
				Name:        providerkit.EffortLabel(e.ReasoningEffort),
				Description: e.Description,
			})
		}

		// Prefer our curated metadata over the API's, which often
		// returns the raw model ID (e.g. "gpt-5.4" instead of "GPT-5.4").
		var displayName string
		var description string
		var contextWindow int64
		if d, ok := defaultsByID[id]; ok {
			displayName = d.DisplayName
			description = d.Description
			contextWindow = d.ContextWindow
		}
		if description == "" {
			description = m.Description
		}
		if displayName == "" {
			displayName = m.DisplayName
		}
		if displayName == "" {
			displayName = codexModelDisplayName(id)
		}

		models = append(models, &agent.ModelInfo{
			Id:               id,
			DisplayName:      displayName,
			Description:      description,
			IsDefault:        m.IsDefault,
			DefaultEffort:    m.DefaultReasoningEffort,
			SupportedEfforts: efforts,
			ContextWindow:    contextWindow,
		})
	}
	return models
}

// codexPermissionProfileCatalog distinguishes an unavailable catalog from a known empty catalog.
type codexPermissionProfileCatalog struct {
	available bool
	options   []*leapmuxv1.AvailableOption
}

// selectableOptions supplies one fallback rule for validation and display.
func (catalog codexPermissionProfileCatalog) selectableOptions() []*leapmuxv1.AvailableOption {
	if !catalog.available {
		return codexBuiltinProfiles
	}
	return catalog.options
}

type codexPermissionCatalog struct {
	profiles     codexPermissionProfileCatalog
	refusedModes map[string]struct{}
}

// discoverPermissions learns which permission profiles and modes Codex allows, keeps
// them for the live option groups, and settles the agent's permission settings
// against them. Call it before thread/start: a stored profile that config.toml no
// longer defines, or a mode that the requirements refuse, would make thread/start
// fail.
func (a *Agent) discoverPermissions(cwd string, timeout time.Duration) codexPermissionSettings {
	catalog := a.queryPermissionCatalog(cwd, timeout)
	a.Mu.Lock()
	defer a.Mu.Unlock()
	a.permissionProfiles = catalog.profiles
	a.refusedModes = catalog.refusedModes
	a.permissionMode = settledPermissionMode(a.permissionMode, catalog.refusedModes)
	a.permissionProfile = settledPermissionProfile(a.permissionProfile, catalog.profiles)
	return a.permissionSettingsLocked()
}

// queryPermissionCatalog runs the independent startup queries concurrently.
// Each query uses one timeout budget. A failed query loses only its own information.
func (a *Agent) queryPermissionCatalog(cwd string, timeout time.Duration) codexPermissionCatalog {
	var (
		wg           sync.WaitGroup
		profiles     codexPermissionProfileCatalog
		requirements codexApprovalRequirements
		guardianOff  bool
	)
	wg.Add(3)
	go func() {
		defer wg.Done()
		profiles = a.queryPermissionProfiles(cwd, timeout)
	}()
	go func() {
		defer wg.Done()
		requirements = a.queryApprovalRequirements(timeout)
	}()
	go func() {
		defer wg.Done()
		guardianOff = a.queryGuardianApprovalDisabled(cwd, timeout)
	}()
	wg.Wait()
	return codexPermissionCatalog{profiles: profiles, refusedModes: codexRefusedModes(requirements, guardianOff)}
}

type codexPermissionProfileEntry struct {
	ID          string  `json:"id"`
	Description *string `json:"description"`
	Allowed     bool    `json:"allowed"`
}

// queryPermissionProfiles returns a known catalog only after every page succeeds.
func (a *Agent) queryPermissionProfiles(cwd string, timeout time.Duration) codexPermissionProfileCatalog {
	entries, err := queryCodexCatalogPages[codexPermissionProfileEntry](a, "permissionProfile/list", map[string]any{"cwd": cwd}, timeout)
	if err != nil {
		slog.Warn("codex permissionProfile/list failed", "agent_id", a.AgentID(), "error", err)
		return codexPermissionProfileCatalog{}
	}
	options := make([]*leapmuxv1.AvailableOption, 0, len(entries))
	seen := make(map[string]struct{})
	for _, entry := range entries {
		if !entry.Allowed || entry.ID == "" {
			continue
		}
		if _, duplicate := seen[entry.ID]; duplicate {
			continue
		}
		seen[entry.ID] = struct{}{}
		description := ""
		if entry.Description != nil {
			description = *entry.Description
		}
		options = append(options, codexPermissionProfileOption(entry.ID, description))
	}
	return codexPermissionProfileCatalog{available: true, options: options}
}

// codexPermissionProfileOption returns the picker option for a discovered profile. A
// built-in profile keeps the name and the description of the static seed, so it
// reads the same before and after discovery. A profile from config.toml is named by
// its id and described by Codex.
func codexPermissionProfileOption(id, description string) *leapmuxv1.AvailableOption {
	if i := slices.IndexFunc(codexBuiltinProfiles, func(o *leapmuxv1.AvailableOption) bool { return o.GetId() == id }); i >= 0 {
		return codexBuiltinProfiles[i]
	}
	if strings.TrimSpace(description) == "" {
		description = "A permission profile from config.toml."
	}
	return &leapmuxv1.AvailableOption{Id: id, Name: id, Description: description}
}

// codexApprovalRequirements holds the managed requirements that restrict the
// permission modes. A nil list allows every value.
type codexApprovalRequirements struct {
	allowedPolicies  map[string]struct{}
	allowedReviewers map[string]struct{}
	// autoReviewOff is true when a requirement pins the auto-review feature off.
	autoReviewOff bool
}

// queryApprovalRequirements reads configRequirements/read. A failure allows every
// value, because Codex still refuses a forbidden one on the request itself.
func (a *Agent) queryApprovalRequirements(timeout time.Duration) codexApprovalRequirements {
	resp, err := a.SendRequest("configRequirements/read", json.RawMessage(`{}`), timeout)
	if err != nil {
		slog.Warn("codex configRequirements/read failed", "agent_id", a.AgentID(), "error", err)
		return codexApprovalRequirements{}
	}
	requirements, err := parseCodexApprovalRequirements(resp)
	if err != nil {
		slog.Warn("codex configRequirements/read unmarshal failed", "agent_id", a.AgentID(), "error", err)
		return codexApprovalRequirements{}
	}
	return requirements
}

// parseCodexApprovalRequirements reads the requirement lists that restrict the
// permission modes. A granular approval policy is an object, which no LeapMux mode
// selects, so the policy list ignores it.
func parseCodexApprovalRequirements(raw json.RawMessage) (codexApprovalRequirements, error) {
	var response struct {
		Requirements *struct {
			AllowedApprovalPolicies   []json.RawMessage `json:"allowedApprovalPolicies"`
			AllowedApprovalsReviewers []string          `json:"allowedApprovalsReviewers"`
			FeatureRequirements       map[string]bool   `json:"featureRequirements"`
		} `json:"requirements"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return codexApprovalRequirements{}, err
	}
	var requirements codexApprovalRequirements
	if response.Requirements == nil {
		return requirements, nil
	}
	if policies := response.Requirements.AllowedApprovalPolicies; policies != nil {
		requirements.allowedPolicies = make(map[string]struct{}, len(policies))
		for _, raw := range policies {
			var policy string
			if json.Unmarshal(raw, &policy) == nil {
				requirements.allowedPolicies[policy] = struct{}{}
			}
		}
	}
	if reviewers := response.Requirements.AllowedApprovalsReviewers; reviewers != nil {
		requirements.allowedReviewers = make(map[string]struct{}, len(reviewers))
		for _, reviewer := range reviewers {
			requirements.allowedReviewers[reviewer] = struct{}{}
		}
	}
	if enabled, present := response.Requirements.FeatureRequirements[codexAutoReviewFeatureRequirement]; present && !enabled {
		requirements.autoReviewOff = true
	}
	return requirements, nil
}

// The two keys that turn the auto-review reviewer off. A managed requirement pins
// the feature through its own key, and config.toml turns it off through the
// feature's config key.
const (
	codexAutoReviewFeatureRequirement = "auto_review"
	codexGuardianApprovalFeature      = "guardian_approval"
)

// queryGuardianApprovalDisabled reports whether the effective config, as seen from
// cwd, turns the auto-review feature off. config/read reports a feature only when
// the user set it, so an absent key reads as on, which is the Codex default.
func (a *Agent) queryGuardianApprovalDisabled(cwd string, timeout time.Duration) bool {
	paramsJSON, err := json.Marshal(map[string]interface{}{"cwd": cwd})
	if err != nil {
		slog.Warn("codex marshal config/read params", "agent_id", a.AgentID(), "error", err)
		return false
	}
	resp, err := a.SendRequest("config/read", paramsJSON, timeout)
	if err != nil {
		slog.Warn("codex config/read failed", "agent_id", a.AgentID(), "error", err)
		return false
	}
	return codexGuardianApprovalDisabled(resp)
}

// codexGuardianApprovalDisabled reads `config.features.guardian_approval` from a
// config/read response. Only an explicit false turns the feature off.
func codexGuardianApprovalDisabled(raw json.RawMessage) bool {
	var response struct {
		Config struct {
			Features map[string]json.RawMessage `json:"features"`
		} `json:"config"`
	}
	if json.Unmarshal(raw, &response) != nil {
		return false
	}
	var enabled bool
	value, present := response.Config.Features[codexGuardianApprovalFeature]
	return present && json.Unmarshal(value, &enabled) == nil && !enabled
}

// codexRefusedModes returns the permission modes that the requirements or the
// config refuse. A mode is refused when the requirements refuse its approval policy
// or its reviewer, and Approve for Me is also refused when the auto-review feature
// is off.
func codexRefusedModes(requirements codexApprovalRequirements, guardianOff bool) map[string]struct{} {
	refused := make(map[string]struct{})
	for _, option := range codexPermissionModeOptions() {
		mode := option.GetId()
		policy, reviewer := codexApprovalWire(mode)
		_, policyAllowed := requirements.allowedPolicies[policy]
		_, reviewerAllowed := requirements.allowedReviewers[reviewer]
		switch {
		case requirements.allowedPolicies != nil && !policyAllowed,
			requirements.allowedReviewers != nil && !reviewerAllowed,
			reviewer == codexReviewerAutoReview && (requirements.autoReviewOff || guardianOff):
			refused[mode] = struct{}{}
		}
	}
	return refused
}

// codexPermissionModeOptions returns the permission modes of the static group, in
// its order.
func codexPermissionModeOptions() []*leapmuxv1.AvailableOption {
	for _, group := range codexStaticOptionGroups {
		if group.GetId() == agent.OptionIDPermissionMode {
			return group.GetOptions()
		}
	}
	return nil
}

// settledPermissionMode returns the mode that a launch uses: the requested mode when
// the requirements allow it, else the default mode, else the first allowed mode. It
// returns the requested mode when the requirements allow none, so Codex states the
// refusal itself.
func settledPermissionMode(requested string, refused map[string]struct{}) string {
	allowed := func(mode string) bool {
		_, isRefused := refused[mode]
		return !isRefused
	}
	if allowed(requested) {
		return requested
	}
	if allowed(DefaultPermissionMode) {
		return DefaultPermissionMode
	}
	for _, option := range codexPermissionModeOptions() {
		if allowed(option.GetId()) {
			return option.GetId()
		}
	}
	return requested
}

// settledPermissionProfile returns the profile that a launch uses: the requested
// profile when the discovered catalog offers it, else the default profile, else the
// first offered profile. An unavailable or empty catalog supplies no replacement.
// The requested profile then stays for Codex to validate.
func settledPermissionProfile(requested string, catalog codexPermissionProfileCatalog) string {
	profiles := catalog.options
	if !catalog.available || len(profiles) == 0 {
		return requested
	}
	offered := func(id string) bool {
		return slices.ContainsFunc(profiles, func(o *leapmuxv1.AvailableOption) bool { return o.GetId() == id })
	}
	if offered(requested) {
		return requested
	}
	if offered(contracts.CodexOptionDefaultPermissionProfile) {
		return contracts.CodexOptionDefaultPermissionProfile
	}
	return profiles[0].GetId()
}

// reconcileModelCatalog repairs the two gaps between what model/list reports and
// what the picker must offer. It runs once at startup, after applyThreadResult has
// settled a.model, and it is the Codex twin of Claude's ensureSettledModelListed.
//
// Gap one: model/list never reports the account-default sentinel, unlike the
// Claude CLI, which lists it itself. Without the row a user who picks a concrete
// model can never return to "let my account decide" for the life of the tab, and
// the option would appear before the first launch and then vanish. The sentinel
// leads the list, matching the static catalog's order.
//
// Gap two: the settled model can be one model/list omits -- a model the account
// retired between the thread resuming and this query, for instance. An unlisted
// current model leaves the picker with no selected row and no effort menu, so the
// static catalog's entry is inserted at its canonical slot.
//
// No-op on an empty live list: queryAvailableModels failed or the CLI reported
// nothing, and OptionGroups then falls back to the static catalog, which already
// carries both the sentinel and every shipped model. Appending here would replace
// that full fallback with a singleton.
func (a *Agent) reconcileModelCatalog() {
	if len(a.availableModels) == 0 {
		return
	}
	if agent.FindAvailableModel(a.availableModels, agent.DefaultModelSentinel) == nil {
		if sentinel := agent.FindAvailableModel(codexDefaultModels, agent.DefaultModelSentinel); sentinel != nil {
			a.availableModels = slices.Insert(a.availableModels, 0, sentinel)
		}
	}
	if agent.UsesAccountDefaultModel(a.model) || agent.FindAvailableModel(a.availableModels, a.model) != nil {
		return
	}
	entry := agent.FindAvailableModel(codexDefaultModels, a.model)
	if entry == nil {
		return
	}
	// Drop the settled model at its slot in the static catalog's order rather than
	// at the end, so a retired model does not sort below a newer one it outranks.
	// The inserted pointer is the shared static entry, which every consumer reads
	// and none mutates -- agent.ModelOptionGroup projects it into fresh protos.
	rank := codexCanonicalModelRank(a.model)
	insertAt := len(a.availableModels)
	for i, m := range a.availableModels {
		if codexCanonicalModelRank(m.GetId()) > rank {
			insertAt = i
			break
		}
	}
	a.availableModels = slices.Insert(a.availableModels, insertAt, entry)
}

// codexCanonicalModelRank returns modelID's index in codexDefaultModels, whose
// order is the canonical picker order (the sentinel first, then newest to oldest,
// then the retired models). A model the static catalog omits ranks last, so it
// sorts after every catalog-known model.
func codexCanonicalModelRank(modelID string) int {
	if i := slices.IndexFunc(codexDefaultModels, func(m *agent.ModelInfo) bool {
		return m.GetId() == modelID
	}); i >= 0 {
		return i
	}
	return len(codexDefaultModels)
}

// codexDefaultEfforts contains all effort levels in the Codex fallback catalog.
// The order matches the menu. Each model selects a supported window of it below.
//
// Every tier the live CLI reports must appear here, so the static fallback offers
// the same menu the running session does. codexEffortsDownFrom fails at startup on
// a tier this list omits, so a forgotten tier cannot shrink a menu in silence.
var codexDefaultEfforts = buildCodexDefaultEfforts()

// codexEffortIDs states membership. effortLadder supplies the order.
// Codex offers no `ultracode` rung and no separate `off` level.
var codexEffortIDs = map[string]bool{
	"ultra": true, "max": true, agent.EffortXHigh: true, agent.EffortHigh: true,
	"medium": true, "low": true,
}

func buildCodexDefaultEfforts() []*agent.EffortInfo {
	efforts := []*agent.EffortInfo{codexAutoEffort()}
	for _, id := range providerkit.EffortLadderIDs() {
		if codexEffortIDs[id] {
			efforts = append(efforts, providerkit.EffortTier(id))
		}
	}
	return efforts
}

// codexEffortsDownFrom returns auto followed by every tier from top down to the
// weakest one Codex offers. Each model states only its strongest tier, so a menu
// cannot skip a rung or fall out of ladder order: the window comes from
// codexDefaultEfforts, which effortLadder already orders.
//
// It panics on a tier codexDefaultEfforts omits. Every argument is a literal in
// this file and codexDefaultEfforts is derived at build time, so no runtime input
// reaches it -- the panic fires on the first `go test` of this package, never on a
// running worker. A silent filter instead shortened the menu with no diagnostic.
func codexEffortsDownFrom(top string) []*agent.EffortInfo {
	tiers := codexDefaultEfforts[1:]
	for i, tier := range tiers {
		if tier.Id == top {
			// The literal has capacity 1, so append allocates a new array and the
			// returned slice never aliases codexDefaultEfforts.
			return append([]*agent.EffortInfo{codexAutoEffort()}, tiers[i:]...)
		}
	}
	panic("codex: effort tier " + top + " is not in codexDefaultEfforts")
}

// codexAutoEffort is the LeapMux-side "auto" sentinel. The CLI never reports it,
// so both the live catalog and the static fallback prepend this one value rather
// than spelling the label and the description out twice.
func codexAutoEffort() *agent.EffortInfo {
	return &agent.EffortInfo{
		Id:          agent.EffortAuto,
		Name:        providerkit.EffortLabel(agent.EffortAuto),
		Description: "Let Codex decide the appropriate effort",
	}
}

var (
	codexEffortsFromUltra = codexEffortsDownFrom("ultra")
	codexEffortsFromMax   = codexEffortsDownFrom("max")
	codexEffortsFromXHigh = codexEffortsDownFrom(agent.EffortXHigh)
)

// codexDefaultModels is the static fallback model catalog. The selectable rows
// mirror what Codex 0.152.1 reports from model/list, in its order. model/list adds
// the account-specific models, such as Daybreak, at runtime.
//
// A model the current app server no longer lists stays here Hidden rather than
// leaving the file. queryAvailableModels reads this list for the Description and
// the ContextWindow that model/list never reports, and modelDependentGroups reads
// it for a stopped agent, so a session still pinned to a retired model keeps its
// effort tiers and its context meter. The picker skips a Hidden row; a lookup by
// id still finds it.
var codexDefaultModels = []*agent.ModelInfo{
	agent.AccountDefaultModelEntry("Use the account's default Codex model"),
	{Id: "gpt-5.6-sol", DisplayName: "GPT-5.6-Sol", Description: "Reliable agentic workhorse for everyday tasks", DefaultEffort: "low", SupportedEfforts: codexEffortsFromUltra, ContextWindow: 1_050_000},
	{Id: "gpt-5.6-terra", DisplayName: "GPT-5.6-Terra", Description: "Balanced agentic coding model for everyday work", DefaultEffort: "medium", SupportedEfforts: codexEffortsFromUltra, ContextWindow: 1_050_000},
	{Id: "gpt-5.6-luna", DisplayName: "GPT-5.6-Luna", Description: "Fast and affordable agentic coding model", DefaultEffort: "medium", SupportedEfforts: codexEffortsFromMax, ContextWindow: 1_050_000},
	{Id: "gpt-5.5", DisplayName: "GPT-5.5", Description: "Proven previous-generation model for coding and general work", DefaultEffort: "medium", SupportedEfforts: codexEffortsFromXHigh, ContextWindow: 1_050_000},
	{Id: "gpt-5.4", DisplayName: "GPT-5.4", Description: "Strong model for everyday coding", DefaultEffort: "medium", SupportedEfforts: codexEffortsFromXHigh, ContextWindow: 1_050_000},
	{Id: "gpt-5.4-mini", DisplayName: "GPT-5.4-Mini", Description: "Small, fast, and cost-efficient model for simpler coding tasks", DefaultEffort: "medium", SupportedEfforts: codexEffortsFromXHigh, ContextWindow: 400_000},
	{Id: "gpt-5.3-codex-spark", DisplayName: "GPT-5.3-Codex-Spark", Description: "Ultra-fast coding model", DefaultEffort: "high", SupportedEfforts: codexEffortsFromXHigh, ContextWindow: 128_000},
	// Retired below: Codex 0.152.1 lists none of these, so they carry their last
	// known metadata and stay out of the picker.
	{Id: "gpt-5.2", DisplayName: "GPT-5.2", Description: "Optimized for professional work and long-running agents", DefaultEffort: "high", SupportedEfforts: codexEffortsFromXHigh, ContextWindow: 256_000, Hidden: true},
	{Id: "gpt-5.3-codex", DisplayName: "GPT-5.3 Codex", Description: "Frontier Codex-optimized agentic coding model", DefaultEffort: "high", SupportedEfforts: codexEffortsFromXHigh, ContextWindow: 400_000, Hidden: true},
	{Id: "gpt-5.2-codex", DisplayName: "GPT-5.2 Codex", Description: "Frontier agentic coding model", DefaultEffort: "high", SupportedEfforts: codexEffortsFromXHigh, ContextWindow: 400_000, Hidden: true},
	{Id: "gpt-5.1-codex-max", DisplayName: "GPT-5.1 Codex Max", Description: "Codex-optimized model for deep and fast reasoning", DefaultEffort: "high", SupportedEfforts: codexEffortsFromXHigh, ContextWindow: 400_000, Hidden: true},
	{Id: "gpt-5.1-codex-mini", DisplayName: "GPT-5.1 Codex Mini", Description: "Optimized for Codex; cheaper, faster, but less capable", DefaultEffort: "high", SupportedEfforts: codexEffortsFromXHigh, ContextWindow: 400_000, Hidden: true},
}

// codexModelDisplayName generates a human-readable display name from a Codex
// model ID (e.g. "gpt-4.1-mini" → "GPT-4.1 Mini", "o4-mini" → "o4-mini"). It is
// the last fallback in queryAvailableModels: codexDefaultModels wins, then the
// name model/list reports, so this runs only for a model that neither supplies.
// Do not draw an example from codexDefaultModels -- the catalog spells the
// current models the way the CLI does ("GPT-5.4-Mini"), which this differs from.
func codexModelDisplayName(id string) string {
	prefix := ""
	rest := id
	if strings.HasPrefix(id, "gpt-") {
		prefix = "GPT-"
		rest = id[4:]
	}
	// Split remaining by hyphens, capitalize suffix parts.
	parts := strings.SplitN(rest, "-", 2)
	if len(parts) == 1 {
		return prefix + parts[0]
	}
	// Version part stays as-is, suffix parts get title-cased.
	suffixParts := strings.Split(parts[1], "-")
	for i, p := range suffixParts {
		if len(p) > 0 {
			suffixParts[i] = providerkit.CapitalizeFirst(p)
		}
	}
	return prefix + parts[0] + " " + strings.Join(suffixParts, " ")
}
