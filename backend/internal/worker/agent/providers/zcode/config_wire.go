package zcode

import (
	"errors"
	"strings"
)

// zcodeRegistryProvider is one strict legacy registry entry.
type zcodeRegistryProvider struct {
	ProviderID string               `json:"providerId"`
	Kind       string               `json:"kind"`
	APIFormat  string               `json:"apiFormat,omitempty"`
	BaseURL    string               `json:"baseURL,omitempty"`
	Label      string               `json:"label,omitempty"`
	Source     string               `json:"source,omitempty"`
	APIKey     *zcodeRegistryAPIKey `json:"apiKey,omitempty"`
	Models     []zcodeRegistryModel `json:"models"`
	Enabled    bool                 `json:"-"`
}

// zcodeRegistryAPIKey carries the inline credential that LeapMux can resolve.
type zcodeRegistryAPIKey struct {
	Source string `json:"source"`
	Value  string `json:"value"`
}

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

type zcodeModelRef struct {
	ProviderID string `json:"providerId"`
	ModelID    string `json:"modelId"`
}

// zcodeModelSelection is the model reference accepted by current ZCode builds.
// A reasoning model requires its level inside the selection.
type zcodeModelSelection struct {
	zcodeModelRef
	Options *zcodeModelSelectionOptions `json:"options,omitempty"`
}

type zcodeModelSelectionOptions struct {
	ReasoningLevel string `json:"reasoningLevel"`
}

// zcodeRuntimeModel prevents a legacy model switch from racing the registry push.
type zcodeRuntimeModel struct {
	Revision     string                `json:"revision"`
	GeneratedAt  int64                 `json:"generatedAt"`
	Model        zcodeModelRef         `json:"model"`
	Provider     zcodeRegistryProvider `json:"provider"`
	ThoughtLevel string                `json:"thoughtLevel,omitempty"`
}

// zcodeAccountProviderConfig is one provider entry of the account snapshot.
type zcodeAccountProviderConfig struct {
	// BuiltinModelIDs replaces the installed rule's model list when it is not nil.
	// omitzero keeps the difference that the app-server reads: a nil list omits the
	// field, so the rule's list stays, and an empty list offers no model.
	BuiltinModelIDs []string `json:"builtinModelIds,omitzero"`
	Access          struct {
		Type     string `json:"type"`
		Entitled bool   `json:"entitled"`
	} `json:"access"`
}

type zcodeAccountProviderState struct {
	Availability      string `json:"availability"`
	Entitled          bool   `json:"entitled"`
	Current           bool   `json:"current"`
	UnavailableReason string `json:"unavailableReason,omitempty"`
	// EffectiveAt is the time, in seconds since the epoch, at which a pending plan
	// takes effect.
	EffectiveAt *float64 `json:"effectiveAt,omitempty"`
}

// The availability values of an account state, as the app-server's schema states
// them.
const (
	zcodeAvailabilityAvailable   = "available"
	zcodeAvailabilityPending     = "pending"
	zcodeAvailabilityUnavailable = "unavailable"
	zcodeAvailabilityUnknown     = "unknown"
)

// The unavailableReason values that LeapMux reports, as the app-server's schema
// states them.
const (
	zcodeUnavailableNotEntitled      = "not-entitled"
	zcodeUnavailableCredentialFailed = "credential-failed"
)

// zcodeAccountEntitlement is what LeapMux knows about whether the account holds
// one plan, in the form that the account snapshot reports it.
type zcodeAccountEntitlement struct {
	// Availability is one of the zcodeAvailability* values.
	Availability string
	// UnavailableReason explains an unavailable plan.
	UnavailableReason string
	// EffectiveAt is the time at which a pending plan takes effect.
	EffectiveAt *float64
	// Models replaces the installed rule's model list when it is not nil. An empty
	// list offers no model.
	Models []string
}

// zcodeCodingPlanEntitlement is the entitlement of every coding plan. LeapMux has
// no entitlement check for a coding plan: the inline credential is the only
// evidence that it holds, so the plan is available and the installed rule keeps
// its model list.
var zcodeCodingPlanEntitlement = zcodeAccountEntitlement{Availability: zcodeAvailabilityAvailable}

// zcodeUnknownEntitlement is the entitlement of a plan that no check answered.
var zcodeUnknownEntitlement = zcodeAccountEntitlement{Availability: zcodeAvailabilityUnknown}

// accountEntry renders the entitlement as the snapshot's provider entry and state,
// as the ZCode desktop does: the plan is entitled only when it is available, and
// the state repeats that entitlement. The app-server offers a model only when its
// provider is entitled and current.
func (e zcodeAccountEntitlement) accountEntry(rule zcodeBuiltinProviderRule, current bool) (zcodeAccountProviderConfig, zcodeAccountProviderState) {
	availability := e.Availability
	if availability == "" {
		availability = zcodeAvailabilityUnknown
	}
	var entry zcodeAccountProviderConfig
	entry.Access.Type = strings.TrimSpace(rule.Config.Access.Type)
	entry.Access.Entitled = availability == zcodeAvailabilityAvailable
	if e.Models != nil {
		// A copy, so the payload never aliases the entitlement result.
		entry.BuiltinModelIDs = append(make([]string, 0, len(e.Models)), e.Models...)
	}
	state := zcodeAccountProviderState{
		Availability:      availability,
		Entitled:          entry.Access.Entitled,
		Current:           current,
		UnavailableReason: e.UnavailableReason,
		EffectiveAt:       e.EffectiveAt,
	}
	return entry, state
}

// zcodeAccountProviderPayload is the host-owned current account snapshot.
type zcodeAccountProviderPayload struct {
	Revision                    string                                `json:"revision"`
	BasedOnZCodeBuiltinRevision string                                `json:"basedOnZCodeBuiltinRevision"`
	Providers                   map[string]zcodeAccountProviderConfig `json:"providers"`
	States                      map[string]zcodeAccountProviderState  `json:"states"`
}

func (c zcodeCatalog) accountProviderID(providerID string) (string, bool) {
	accountID, ok := c.accountProviderIDs[providerID]
	return accountID, ok
}

func legacyProviderIDForAccountRule(rule zcodeBuiltinProviderRule) string {
	accountType := rule.accountType()
	if accountType == "" || rule.mode() == "" || strings.TrimSpace(rule.ProviderID) == "" {
		return ""
	}
	suffix := "coding-plan"
	if rule.isStartPlan() {
		suffix = zcodeStartPlanMode
	}
	return "builtin:" + accountType + "-" + suffix
}

// zcodeDefaultCodingPlanMode is the coding-plan mode of a family that setting.json
// selects no mode for.
const zcodeDefaultCodingPlanMode = "individual-coding-plan"

// selectedAccountRule reports whether LeapMux binds the account plan that rule
// describes: a family's Start Plan, or the coding-plan mode that setting.json
// selects for the family (the individual plan when it selects none). The other
// modes, such as off-peak, are not bound.
func (c zcodeCatalog) selectedAccountRule(rule zcodeBuiltinProviderRule) bool {
	if strings.TrimSpace(rule.Config.Access.Type) != zcodeAccountAccessType || legacyProviderIDForAccountRule(rule) == "" {
		return false
	}
	if rule.isStartPlan() {
		return true
	}
	selected := c.accountSelection.planKinds[rule.accountType()]
	if selected == "" {
		selected = zcodeDefaultCodingPlanMode
	}
	return rule.mode() == selected
}

// zcodeAccountBinding is an account plan that LeapMux pushes: an installed account
// rule, and the credentials that serve it.
type zcodeAccountBinding struct {
	rule      zcodeBuiltinProviderRule
	accountID string
	// legacyID is the provider id that config.json and a legacy model id use for
	// the plan, such as "builtin:zai-start-plan".
	legacyID string
	// provider is the plan's config.json provider, whose inline key serves when the
	// credential store holds no credential. nil when config.json lists none.
	provider *zcodeRegistryProvider
	// session is the family's sign-in state in the credential store.
	session zcodeAccountSession
	current bool
}

func (b zcodeAccountBinding) family() string { return b.rule.accountType() }

// configKey returns the inline key of the plan's config.json provider, or "".
func (b zcodeAccountBinding) configKey() string {
	if b.provider == nil {
		return ""
	}
	key, _ := b.provider.inlineAPIKey()
	return key
}

// startPlanCredential returns the credential that authorizes a Start Plan, as the
// desktop prefers them (resolveStartPlanAuthorization): the ZCode token of the
// credential store, or else the config.json key.
func (b zcodeAccountBinding) startPlanCredential() string {
	if b.session.zcodeJWT != "" {
		return b.session.zcodeJWT
	}
	return b.configKey()
}

// baseURL returns the endpoint of the plan's model requests: the one that the
// installed rule states, or else the one that config.json states.
func (b zcodeAccountBinding) baseURL() string {
	if baseURL := strings.TrimSpace(b.rule.Config.API.BaseURL); baseURL != "" {
		return baseURL
	}
	if b.provider != nil {
		return b.provider.BaseURL
	}
	return ""
}

// selectedWithoutFamily reports whether the plan is the selected connection when
// setting.json states no family: config.json enables its provider, or, where
// config.json lists none, the credential store holds the family's sign-in.
func (b zcodeAccountBinding) selectedWithoutFamily() bool {
	if b.provider != nil {
		return b.provider.Enabled
	}
	return b.session.accessToken != ""
}

// accountProviderBindings binds each selected account rule that a credential
// serves: the family's sign-in in the credential store, or the plan's config.json
// provider. A plan that neither serves is left out of the snapshot. The error
// reports unreadable credential material. Each binding retains independently
// readable credentials and can also use its config.json key.
func (c zcodeCatalog) accountProviderBindings(rules []zcodeBuiltinProviderRule, credentials zcodeCredentials) ([]zcodeAccountBinding, error) {
	sessions := map[string]zcodeAccountSession{}
	var errs []error
	var bindings []zcodeAccountBinding
	for _, rule := range rules {
		if !c.selectedAccountRule(rule) {
			continue
		}
		family := rule.accountType()
		session, read := sessions[family]
		if !read {
			var err error
			if session, _, err = credentials.session(family); err != nil {
				errs = append(errs, err)
			}
			sessions[family] = session
		}
		binding := zcodeAccountBinding{
			rule:      rule,
			accountID: strings.TrimSpace(rule.ProviderID),
			legacyID:  legacyProviderIDForAccountRule(rule),
			session:   session,
		}
		binding.provider = c.inlineProvider(binding.legacyID)
		credential := binding.configKey()
		if credential == "" {
			credential = session.accessToken
			if rule.isStartPlan() {
				credential = session.zcodeJWT
			}
		}
		if credential == "" {
			continue
		}
		bindings = append(bindings, binding)
	}
	c.markCurrentBindings(bindings)
	return bindings, errors.Join(errs...)
}

// inlineProvider returns the config.json provider of providerID that holds an
// inline credential, or nil.
func (c zcodeCatalog) inlineProvider(providerID string) *zcodeRegistryProvider {
	for _, provider := range c.Providers {
		if provider.ProviderID != providerID {
			continue
		}
		if _, ok := provider.inlineAPIKey(); ok {
			return &provider
		}
	}
	return nil
}

// markCurrentBindings marks the bindings that are the current connection, as the
// ZCode desktop decides it. Only a plan of the current provider family can be
// current. A Start Plan of that family is current when its credential exists,
// which every binding guarantees. A coding plan is current when it is the mode
// that setting.json selects for the family. The app-server requires exactly one
// current coding plan, and at most one is current by construction: one family,
// and one selected mode for each family.
//
// setting.json can state no family: the file can be missing, as on a machine where
// only the ZCode CLI runs. A Start Plan then stays current, because the app-server
// serves a Start Plan as an ordinary provider beside the current coding plan. A
// coding plan is current only when it is the one plan that selectedWithoutFamily
// selects: with two families selected, any choice could be wrong, and the
// app-server refuses every selection of a coding-plan model while two are
// current.
func (c zcodeCatalog) markCurrentBindings(bindings []zcodeAccountBinding) {
	domain := c.accountSelection.domain
	if domain != "" {
		for i := range bindings {
			binding := &bindings[i]
			binding.current = binding.family() == domain &&
				(binding.rule.isStartPlan() || c.accountSelection.planKinds[domain] == binding.rule.mode())
		}
		return
	}
	selected, count := -1, 0
	for i := range bindings {
		binding := &bindings[i]
		binding.current = binding.rule.isStartPlan()
		if !binding.current && binding.selectedWithoutFamily() {
			selected, count = i, count+1
		}
	}
	if count == 1 {
		bindings[selected].current = true
	}
}

// newZCodeAccountProviderPayload builds the account snapshot from the bindings. It
// records nothing: recordAccountBridge records the id bridge after the app-server
// accepts the snapshot.
//
// startPlans holds the entitlement of each Start Plan binding, keyed by its legacy
// provider id. A Start Plan with no entry has an unknown entitlement, so it is not
// entitled and the app-server offers none of its models: the ZCode desktop also
// fails closed when it cannot learn the entitlement.
func newZCodeAccountProviderPayload(bindings []zcodeAccountBinding, basedOn, revision string, startPlans map[string]zcodeAccountEntitlement) zcodeAccountProviderPayload {
	payload := zcodeAccountProviderPayload{
		Revision:                    revision,
		BasedOnZCodeBuiltinRevision: basedOn,
		Providers:                   map[string]zcodeAccountProviderConfig{},
		States:                      map[string]zcodeAccountProviderState{},
	}
	for _, binding := range bindings {
		entitlement := zcodeCodingPlanEntitlement
		if binding.rule.isStartPlan() {
			var ok bool
			if entitlement, ok = startPlans[binding.legacyID]; !ok {
				entitlement = zcodeUnknownEntitlement
			}
		}
		entry, state := entitlement.accountEntry(binding.rule, binding.current)
		payload.Providers[binding.accountID] = entry
		payload.States[binding.accountID] = state
	}
	return payload
}

// recordAccountBridge translates legacy provider IDs to account provider IDs.
// Readers and writers of a shared Agent catalog hold Agent.Mu.
func (c *zcodeCatalog) recordAccountBridge(bindings []zcodeAccountBinding) {
	c.accountProviderIDs = make(map[string]string, len(bindings))
	for _, binding := range bindings {
		c.accountProviderIDs[binding.legacyID] = binding.accountID
	}
}

// inlineAPIKey returns the provider's inline credential, the one kind of credential
// that LeapMux can resolve.
func (p zcodeRegistryProvider) inlineAPIKey() (string, bool) {
	if p.APIKey == nil || p.APIKey.Source != zcodeAPIKeySourceInline || p.APIKey.Value == "" {
		return "", false
	}
	return p.APIKey.Value, true
}

func (c zcodeCatalog) inlineAPIKey(providerID string) (string, bool) {
	for _, provider := range c.Providers {
		if providerID != "" && provider.ProviderID != providerID {
			continue
		}
		if key, ok := provider.inlineAPIKey(); ok {
			return key, true
		}
	}
	return "", false
}

// registryPayload nests the legacy registry fields under the strict wire envelope.
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

func (c zcodeCatalog) runtimeModelFor(modelID, revision string, generatedAt int64) (zcodeRuntimeModel, bool) {
	ref, ok := c.refs[modelID]
	if !ok {
		return zcodeRuntimeModel{}, false
	}
	for _, provider := range c.Providers {
		if provider.ProviderID == ref.ProviderID {
			return zcodeRuntimeModel{Revision: revision, GeneratedAt: generatedAt, Model: ref, Provider: provider}, true
		}
	}
	return zcodeRuntimeModel{}, false
}

type zcodeWorkspace struct {
	WorkspacePath string `json:"workspacePath"`
	WorkspaceKey  string `json:"workspaceKey"`
}

func zcodeWorkspaceFor(dir string) zcodeWorkspace {
	return zcodeWorkspace{WorkspacePath: dir, WorkspaceKey: dir}
}
