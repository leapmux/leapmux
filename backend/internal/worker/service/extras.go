package service

import (
	"encoding/json"
	"log/slog"
	"maps"
	"sort"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
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
