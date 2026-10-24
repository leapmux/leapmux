package letta

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

type lettaSettingsRecord struct {
	ID            string                     `json:"id"`
	AgentID       string                     `json:"agent_id"`
	Model         string                     `json:"model"`
	ModelSettings map[string]json.RawMessage `json:"model_settings"`
	LLMConfig     map[string]json.RawMessage `json:"llm_config"`
}

type lettaSettingsCommand struct {
	lettaCommand
	Body map[string]any `json:"body,omitempty"`
}

type lettaSettingsResponse struct {
	lettaReplyHeader
	Error         string                     `json:"error"`
	Backend       string                     `json:"backend"`
	Agent         *lettaSettingsRecord       `json:"agent"`
	Conversation  *lettaSettingsRecord       `json:"conversation"`
	Runtime       runtimeScope               `json:"runtime"`
	ModelHandle   string                     `json:"model_handle"`
	ModelSettings map[string]json.RawMessage `json:"model_settings"`
}

var errLettaSettingsRefused = errors.New("the Letta server refused the settings request")

func (a *Agent) catalogModel(id string) lettaModel {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	for _, model := range a.catalog.models {
		if model.id == id {
			return model
		}
	}
	return lettaModel{id: id, displayName: id}
}

func (a *Agent) applyNativeModel(model, effort string) error {
	scope := a.runtime()
	if scope.AgentID == "" || scope.ConversationID == "" {
		return errors.New("the Letta runtime identity is unavailable")
	}
	command := lettaSettingsCommand{lettaCommand: newLettaCommand("update_model", a.nextRequestID())}
	command.Runtime = &scope
	payload := map[string]any{"model_handle": model}
	if effort != "" && effort != agent.EffortAuto {
		for _, variant := range a.catalogModel(model).variants {
			if variant.effort == effort && variant.id != "" {
				payload["model_id"] = variant.id
				break
			}
		}
		if _, exists := payload["model_id"]; !exists {
			return fmt.Errorf("%w: model %q offers no %q effort preset", errLettaSettingsRefused, model, effort)
		}
	}
	command.Payload = payload
	response, err := a.requestNativeSettings(a.Context(), command)
	if err != nil {
		return err
	}
	if response.Runtime != scope || a.runtime() != scope {
		return errors.New("the Letta model update belongs to another runtime")
	}
	if response.ModelHandle == "" || response.ModelSettings == nil {
		return errors.New("the Letta model update contains no model settings")
	}
	return a.recordNativeModelSettings(&lettaSettingsRecord{Model: response.ModelHandle, ModelSettings: response.ModelSettings}, nil)
}

func (a *Agent) requestNativeSettings(ctx context.Context, command lettaSettingsCommand) (lettaSettingsResponse, error) {
	var response lettaSettingsResponse
	if err := a.awaitNativeReply(ctx, command, &response); err != nil {
		return response, err
	}
	if !*response.Success {
		return response, fmt.Errorf("%w: %s", errLettaSettingsRefused, response.Error)
	}
	return response, nil
}

func lettaRecordSettings(record *lettaSettingsRecord) map[string]json.RawMessage {
	if record == nil {
		return nil
	}
	if record.ModelSettings != nil {
		return record.ModelSettings
	}
	return record.LLMConfig
}

func lettaRecordModel(record *lettaSettingsRecord) string {
	if record == nil {
		return ""
	}
	if record.Model != "" {
		return record.Model
	}
	var model, provider string
	if json.Unmarshal(record.LLMConfig["model"], &model) != nil || model == "" {
		return ""
	}
	if json.Unmarshal(record.LLMConfig["model_endpoint_type"], &provider) == nil && provider != "" {
		return provider + "/" + model
	}
	return model
}

func lettaReasoningSettings(raw json.RawMessage) (map[string]json.RawMessage, error) {
	var reasoning map[string]json.RawMessage
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &reasoning); err != nil {
			return nil, fmt.Errorf("the Letta reasoning settings are invalid: %w", err)
		}
	}
	return reasoning, nil
}

// lettaReportedEffort follows the native nested, effort, then flat precedence.
// A null field lets the next native representation supply the value.
func lettaReportedEffort(settings map[string]json.RawMessage) (string, bool, error) {
	reasoning, err := lettaReasoningSettings(settings["reasoning"])
	if err != nil {
		return "", false, err
	}
	_, hasReasoning := settings["reasoning"]
	present := hasReasoning && reasoning == nil
	for _, raw := range []json.RawMessage{reasoning["reasoning_effort"], settings["effort"], settings["reasoning_effort"]} {
		if len(raw) == 0 {
			continue
		}
		present = true
		var value *string
		if err := json.Unmarshal(raw, &value); err != nil {
			return "", false, fmt.Errorf("the Letta effort setting is invalid: %w", err)
		}
		if value == nil {
			continue
		}
		if *value == "" {
			return agent.EffortAuto, true, nil
		}
		return *value, true, nil
	}
	return agent.EffortAuto, present, nil
}

// recordNativeModelSettings applies the native shallow merge of model_settings.
// Legacy llm_config supplies defaults only when canonical settings leave them unset.
func (a *Agent) recordNativeModelSettings(base, conversation *lettaSettingsRecord) error {
	model := lettaRecordModel(conversation)
	if model == "" {
		model = lettaRecordModel(base)
	}
	settings := make(map[string]json.RawMessage)
	if base != nil {
		maps.Copy(settings, base.ModelSettings)
	}
	overrides := lettaRecordSettings(conversation)
	maps.Copy(settings, overrides)
	effort, present, err := lettaReportedEffort(settings)
	if err != nil {
		return err
	}
	_, overridesReasoning := overrides["reasoning"]
	if !present && !overridesReasoning && (base == nil || base.ModelSettings == nil) {
		effort, _, err = lettaReportedEffort(lettaRecordSettings(base))
		if err != nil {
			return err
		}
	}
	catalogModel := a.catalogModel(model)
	provider, err := lettaSettingsProvider(catalogModel, overrides, lettaRecordSettings(base))
	if err != nil {
		return err
	}
	effort = lettaNativeEffort(model, provider, effort)
	if !slices.ContainsFunc(catalogModel.variants, func(variant lettaModelVariant) bool { return variant.effort == effort }) {
		for _, variant := range catalogModel.variants {
			if lettaNativeEffort(model, provider, variant.effort) == effort {
				effort = variant.effort
				break
			}
		}
	}
	a.Mu.Lock()
	if model != "" {
		a.settings.model = model
		a.settings.modelProvider = provider
	}
	a.settings.reasoningLevel = effort
	a.Mu.Unlock()
	return nil
}

func lettaSettingsProvider(model lettaModel, current, inherited map[string]json.RawMessage) (string, error) {
	prefix, _, _ := strings.Cut(model.id, "/")
	provider := "openai"
	switch prefix {
	case "openai", "openrouter", "openai-codex", "chatgpt_oauth", "anthropic", "lc-anthropic", "claude-pro-max", "minimax", "bedrock", "moonshot", "moonshotai", "zai", "xai", "google_ai", "google_vertex":
		provider = prefix
	}
	for _, variant := range model.variants {
		if variant.providerType != "" {
			provider = variant.providerType
			break
		}
	}
	for _, settings := range []map[string]json.RawMessage{inherited, current} {
		for _, key := range []string{"model_endpoint_type", "provider_type"} {
			raw, exists := settings[key]
			if !exists {
				continue
			}
			if err := json.Unmarshal(raw, &provider); err != nil {
				return "", fmt.Errorf("the Letta model provider is invalid: %w", err)
			}
		}
	}
	if provider == "" {
		return "", errors.New("the Letta model provider is empty")
	}
	switch provider {
	case "openai-codex":
		provider = "chatgpt_oauth"
	case "lc-anthropic", "claude-pro-max", "minimax":
		provider = "anthropic"
	case "moonshotai":
		provider = "moonshot"
	}
	return provider, nil
}

// lettaNativeEffort matches native supportsDistinctAnthropicXHighEffort and
// normalizeReasoningEffortForModel, including advertised aliases of OpenAI tiers.
func lettaNativeEffort(model, provider, effort string) string {
	switch provider {
	case "anthropic", "bedrock":
		if effort != "xhigh" {
			return effort
		}
		for _, name := range []string{"claude-fable-5", "claude-opus-5", "claude-sonnet-5", "claude-opus-4-7", "claude-opus-4-8"} {
			if strings.Contains(model, name) {
				return effort
			}
		}
		return "max"
	case "moonshot", "zai", "xai", "google_ai", "google_vertex":
		return effort
	}
	name := strings.ToLower(strings.TrimSpace(model))
	name = name[strings.LastIndex(name, "/")+1:]
	name = strings.TrimSuffix(name, "-fast")
	if effort == "minimal" {
		for _, prefix := range []string{"gpt-5.1", "gpt-5.2", "gpt-5.3", "gpt-5.4", "gpt-5.5", "gpt-5.6", "gpt-6-sol", "gpt-6-luna"} {
			if name == prefix || strings.HasPrefix(name, prefix+"-") {
				return "none"
			}
		}
	}
	if effort == "max" && strings.HasPrefix(name, "gpt-5.5") {
		return "xhigh"
	}
	return effort
}

// lettaEffortPatch preserves the settings object because native updates replace it.
func lettaEffortPatch(current, inherited map[string]json.RawMessage, model lettaModel, effort, backend string) (map[string]json.RawMessage, error) {
	patch := maps.Clone(current)
	if patch == nil {
		patch = make(map[string]json.RawMessage)
	}
	provider, err := lettaSettingsProvider(model, current, inherited)
	if err != nil {
		return nil, err
	}
	encodedProvider, err := json.Marshal(provider)
	if err != nil {
		return nil, err
	}
	patch["provider_type"] = encodedProvider
	var value any = lettaNativeEffort(model.id, provider, effort)
	if effort == "" || effort == agent.EffortAuto {
		value = nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	primaryField := "reasoning"
	switch provider {
	case "anthropic", "bedrock":
		primaryField = "effort"
	case "moonshot", "zai":
		primaryField = "reasoning_effort"
	}
	// All three properties can feed the local backend's effective effort.
	// Update existing aliases too, so an old property cannot defeat an Auto reset.
	for _, key := range []string{"effort", "reasoning_effort"} {
		_, currentHas := current[key]
		_, inheritedHas := inherited[key]
		if key == primaryField || currentHas || backend == "local" && inheritedHas {
			patch[key] = encoded
		}
	}
	raw, exists := current["reasoning"]
	if !exists {
		raw = inherited["reasoning"]
	}
	reasoning, err := lettaReasoningSettings(raw)
	if err != nil {
		return nil, err
	}
	_, nestedAlias := reasoning["reasoning_effort"]
	if primaryField == "reasoning" || nestedAlias {
		if reasoning == nil {
			reasoning = make(map[string]json.RawMessage)
		}
		reasoning["reasoning_effort"] = encoded
		patch["reasoning"], err = json.Marshal(reasoning)
		if err != nil {
			return nil, err
		}
	}
	if backend == "local" {
		// Native local records retain the selected alias beside canonical settings.
		var selected any = effort
		if value == nil {
			selected = nil
		}
		patch["reasoning_effort"], err = json.Marshal(selected)
		if err != nil {
			return nil, err
		}
	}
	return patch, nil
}

func (a *Agent) applyNativeEffort(effort string) error {
	scope := a.runtime()
	if scope.AgentID == "" || scope.ConversationID == "" {
		return errors.New("the Letta runtime identity is unavailable")
	}
	info, err := a.requestNativeSettings(a.Context(), lettaSettingsCommand{lettaCommand: newLettaCommand("app_server_info", a.nextRequestID())})
	if err != nil {
		return err
	}
	if info.Backend != "local" && info.Backend != "api" {
		return errors.New("the Letta server reported no supported settings backend")
	}
	readAgent := lettaSettingsCommand{lettaCommand: newLettaCommand("agent_retrieve", a.nextRequestID())}
	readAgent.AgentID = scope.AgentID
	base, err := a.requestNativeSettings(a.Context(), readAgent)
	if err != nil {
		return err
	}
	if base.Agent == nil || base.Agent.ID != scope.AgentID {
		return errors.New("the Letta settings reply identifies another agent")
	}
	var conversation *lettaSettingsRecord
	current := base.Agent
	if scope.ConversationID != "default" {
		read := lettaSettingsCommand{lettaCommand: newLettaCommand("conversation_retrieve", a.nextRequestID())}
		read.ConversationID = scope.ConversationID
		response, err := a.requestNativeSettings(a.Context(), read)
		if err != nil {
			return err
		}
		conversation = response.Conversation
		if conversation == nil || conversation.ID != scope.ConversationID || conversation.AgentID != scope.AgentID {
			return errors.New("the Letta settings reply identifies another conversation")
		}
		current = conversation
	}
	if a.runtime() != scope {
		return errors.New("the Letta runtime changed during the settings request")
	}
	if err := a.recordNativeModelSettings(base.Agent, conversation); err != nil {
		return err
	}
	model := lettaRecordModel(current)
	if model == "" {
		model = lettaRecordModel(base.Agent)
	}
	if model == "" {
		return errors.New("the Letta settings reply contains no model")
	}
	candidate := a.catalogModel(model)
	if effort != "" && effort != agent.EffortAuto && !slices.ContainsFunc(candidate.variants, func(variant lettaModelVariant) bool { return variant.effort == effort }) {
		return fmt.Errorf("%w: model %q offers no %q effort", errLettaSettingsRefused, model, effort)
	}
	patch, err := lettaEffortPatch(current.ModelSettings, lettaRecordSettings(base.Agent), candidate, effort, info.Backend)
	if err != nil {
		return err
	}
	updateType := "conversation_update"
	if conversation == nil {
		updateType = "agent_update"
	}
	write := lettaSettingsCommand{lettaCommand: newLettaCommand(updateType, a.nextRequestID()), Body: map[string]any{"model_settings": patch}}
	if conversation == nil {
		write.AgentID = scope.AgentID
	} else {
		write.ConversationID = scope.ConversationID
	}
	response, err := a.requestNativeSettings(a.Context(), write)
	if err != nil {
		return err
	}
	if a.runtime() != scope {
		return errors.New("the Letta runtime changed during the settings update")
	}
	confirmed := response.Conversation
	if conversation == nil {
		if response.Agent == nil || response.Agent.ID != scope.AgentID {
			return errors.New("the Letta settings update identifies another agent")
		}
		confirmed = response.Agent
	} else if response.Conversation == nil || response.Conversation.ID != scope.ConversationID || response.Conversation.AgentID != scope.AgentID {
		return errors.New("the Letta settings update identifies another conversation")
	}
	if _, present, err := lettaReportedEffort(lettaRecordSettings(confirmed)); err != nil || !present {
		return errors.Join(errors.New("the Letta settings update contains no valid effort"), err)
	}
	if conversation == nil {
		return a.recordNativeModelSettings(confirmed, nil)
	}
	return a.recordNativeModelSettings(base.Agent, confirmed)
}
