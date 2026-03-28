package service

import (
	"encoding/json"
	"log/slog"
	"maps"
	"sort"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"google.golang.org/protobuf/encoding/protojson"
)

func parseExtraSettings(raw string) map[string]string {
	if raw == "" {
		return map[string]string{}
	}
	var parsed map[string]string
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		slog.Warn("invalid agent extra_settings payload; using empty object", "error", err)
		return map[string]string{}
	}
	if parsed == nil {
		return map[string]string{}
	}
	for k, v := range parsed {
		if v == "" {
			delete(parsed, k)
		}
	}
	return parsed
}

func marshalExtraSettings(settings map[string]string) string {
	if len(settings) == 0 {
		return "{}"
	}
	// Filter out empty values before marshaling.
	// json.Marshal sorts map keys automatically.
	filtered := make(map[string]string, len(settings))
	for k, v := range settings {
		if v != "" {
			filtered[k] = v
		}
	}
	if len(filtered) == 0 {
		return "{}"
	}
	data, err := json.Marshal(filtered)
	if err != nil {
		slog.Error("failed to marshal extra_settings; using empty object", "error", err)
		return "{}"
	}
	return string(data)
}

func mergeExtraSettings(current, incoming map[string]string) map[string]string {
	merged := maps.Clone(current)
	if merged == nil {
		merged = map[string]string{}
	}
	for k, v := range incoming {
		if v == "" {
			delete(merged, k)
			continue
		}
		merged[k] = v
	}
	return merged
}

func sortedExtraSettingKeys(mapsToMerge ...map[string]string) []string {
	keys := make(map[string]struct{})
	for _, settings := range mapsToMerge {
		for key := range settings {
			keys[key] = struct{}{}
		}
	}
	if len(keys) == 0 {
		return nil
	}
	out := make([]string, 0, len(keys))
	for key := range keys {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

// resolveCodexExtras fills in Codex-specific defaults for any missing extra
// settings keys. It mutates the input map and returns it.
func resolveCodexExtras(settings map[string]string, provider leapmuxv1.AgentProvider) map[string]string {
	if provider != leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX {
		return settings
	}
	if settings[agent.CodexExtraSandboxPolicy] == "" {
		settings[agent.CodexExtraSandboxPolicy] = agent.CodexDefaultSandboxPolicy
	}
	if settings[agent.CodexExtraNetworkAccess] == "" {
		settings[agent.CodexExtraNetworkAccess] = agent.CodexDefaultNetworkAccess
	}
	if settings[agent.CodexExtraCollaborationMode] == "" {
		settings[agent.CodexExtraCollaborationMode] = agent.CodexDefaultCollaborationMode
	}
	if settings[agent.CodexExtraServiceTier] == "" {
		settings[agent.CodexExtraServiceTier] = agent.CodexDefaultServiceTier
	}
	return settings
}

// loadExtraSettings parses a JSON extra_settings string from the DB and fills
// in provider-specific defaults.
func loadExtraSettings(raw string, provider leapmuxv1.AgentProvider) map[string]string {
	return resolveCodexExtras(parseExtraSettings(raw), provider)
}

// marshalAvailableModels serializes a slice of AvailableModel protos to a JSON
// array string suitable for DB storage.
func marshalAvailableModels(models []*leapmuxv1.AvailableModel) string {
	if len(models) == 0 {
		return "[]"
	}
	items := make([]json.RawMessage, len(models))
	for i, m := range models {
		b, err := protojson.Marshal(m)
		if err != nil {
			slog.Error("failed to marshal AvailableModel", "error", err)
			return "[]"
		}
		items[i] = b
	}
	data, _ := json.Marshal(items)
	return string(data)
}

// unmarshalAvailableModels deserializes a JSON array string (from DB) into a
// slice of AvailableModel protos.
func unmarshalAvailableModels(raw string) []*leapmuxv1.AvailableModel {
	if raw == "" || raw == "[]" {
		return nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		slog.Warn("invalid available_models JSON", "error", err)
		return nil
	}
	models := make([]*leapmuxv1.AvailableModel, 0, len(items))
	for _, item := range items {
		var m leapmuxv1.AvailableModel
		if err := protojson.Unmarshal(item, &m); err != nil {
			slog.Warn("failed to unmarshal AvailableModel", "error", err)
			continue
		}
		models = append(models, &m)
	}
	return models
}

// marshalAvailableOptionGroups serializes a slice of AvailableOptionGroup
// protos to a JSON array string suitable for DB storage.
func marshalAvailableOptionGroups(groups []*leapmuxv1.AvailableOptionGroup) string {
	if len(groups) == 0 {
		return "[]"
	}
	items := make([]json.RawMessage, len(groups))
	for i, g := range groups {
		b, err := protojson.Marshal(g)
		if err != nil {
			slog.Error("failed to marshal AvailableOptionGroup", "error", err)
			return "[]"
		}
		items[i] = b
	}
	data, _ := json.Marshal(items)
	return string(data)
}

// unmarshalAvailableOptionGroups deserializes a JSON array string (from DB)
// into a slice of AvailableOptionGroup protos.
func unmarshalAvailableOptionGroups(raw string) []*leapmuxv1.AvailableOptionGroup {
	if raw == "" || raw == "[]" {
		return nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		slog.Warn("invalid available_option_groups JSON", "error", err)
		return nil
	}
	groups := make([]*leapmuxv1.AvailableOptionGroup, 0, len(items))
	for _, item := range items {
		var g leapmuxv1.AvailableOptionGroup
		if err := protojson.Unmarshal(item, &g); err != nil {
			slog.Warn("failed to unmarshal AvailableOptionGroup", "error", err)
			continue
		}
		groups = append(groups, &g)
	}
	return groups
}
