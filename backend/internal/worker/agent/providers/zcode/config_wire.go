package zcode

import "strings"

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

type zcodeAccountProviderConfig struct {
	BuiltinModelIDs []string `json:"builtinModelIds,omitempty"`
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
	accountType := strings.TrimSpace(rule.Config.Access.AccountType)
	mode := strings.TrimSpace(rule.Config.Access.Mode)
	if accountType == "" || mode == "" || strings.TrimSpace(rule.ProviderID) == "" {
		return ""
	}
	suffix := "coding-plan"
	if mode == "start-plan" {
		suffix = "start-plan"
	}
	return "builtin:" + accountType + "-" + suffix
}

func (c zcodeCatalog) selectAccountProviderRule(legacyProviderID string, rules []zcodeBuiltinProviderRule) (zcodeBuiltinProviderRule, bool) {
	for _, rule := range rules {
		if legacyProviderIDForAccountRule(rule) != legacyProviderID {
			continue
		}
		mode := strings.TrimSpace(rule.Config.Access.Mode)
		if mode == "start-plan" {
			return rule, true
		}
		selected := strings.TrimSpace(c.accountPlanKinds[rule.Config.Access.AccountType])
		if selected == "" {
			selected = "individual-coding-plan"
		}
		if mode == selected {
			return rule, true
		}
	}
	return zcodeBuiltinProviderRule{}, false
}

// accountProviderPayload derives every legacy/current relationship from installed rules.
func (c *zcodeCatalog) accountProviderPayload(builtinPath, revision string) (zcodeAccountProviderPayload, error) {
	builtin, basedOn, err := loadZCodeBuiltinProviderFile(builtinPath)
	if err != nil {
		return zcodeAccountProviderPayload{}, err
	}
	c.accountProviderIDs = make(map[string]string)
	c.legacyProviderIDs = make(map[string]string)
	payload := zcodeAccountProviderPayload{
		Revision:                    revision,
		BasedOnZCodeBuiltinRevision: basedOn,
		Providers:                   map[string]zcodeAccountProviderConfig{},
		States:                      map[string]zcodeAccountProviderState{},
	}
	for _, provider := range c.Providers {
		rule, ok := c.selectAccountProviderRule(provider.ProviderID, builtin.Config.ProviderConfigRules.ProviderRules)
		if !ok || provider.APIKey == nil || provider.APIKey.Value == "" {
			continue
		}
		accountID := strings.TrimSpace(rule.ProviderID)
		if accountID == "" {
			continue
		}
		c.accountProviderIDs[provider.ProviderID] = accountID
		c.legacyProviderIDs[accountID] = provider.ProviderID
		entry := zcodeAccountProviderConfig{}
		entry.Access.Type = strings.TrimSpace(rule.Config.Access.Type)
		// Entitled means the account HOLDS the plan -- every provider that reaches
		// this loop carries a credential -- not that it is the selected connection
		// kind. The app-server gates model executability on entitled AND current,
		// and mapping enabled here silently dropped every model of a plan the user
		// did not select as the active connection.
		entry.Access.Entitled = true
		isStartPlan := strings.TrimSpace(rule.Config.Access.Mode) == "start-plan"
		// For Start Plan, push the model list the entitlement API reports so the
		// app-server's registry carries the models the purchased plan actually
		// grants (which can differ from the generic builtinModelIds). When the
		// fetch failed or returned nothing, leave the field unset so the
		// app-server keeps its own builtinModelIds -- the static fallback.
		if isStartPlan {
			if models := c.startPlanModels[provider.ProviderID]; len(models) > 0 {
				entry.BuiltinModelIDs = append([]string(nil), models...)
			}
		}
		payload.Providers[accountID] = entry
		// Start Plan is classified "ordinary" by the app-server
		// (isStartPlanModelProviderId), so it is always usable alongside the
		// selected coding-plan connection and must report current. A coding-plan
		// provider reports current only when it IS the selected connection, which
		// keeps the app-server's one-current-account-plan invariant.
		payload.States[accountID] = zcodeAccountProviderState{
			Availability: "available",
			Entitled:     true,
			Current:      isStartPlan || provider.Enabled,
		}
	}
	return payload, nil
}

func (c zcodeCatalog) inlineAPIKey(providerID string) (string, bool) {
	if legacyID := c.legacyProviderIDs[providerID]; legacyID != "" {
		providerID = legacyID
	}
	for _, provider := range c.Providers {
		if providerID != "" && provider.ProviderID != providerID {
			continue
		}
		if provider.APIKey != nil && provider.APIKey.Source == zcodeAPIKeySourceInline && provider.APIKey.Value != "" {
			return provider.APIKey.Value, true
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
