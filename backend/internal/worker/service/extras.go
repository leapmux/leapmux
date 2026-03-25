package service

import (
	"encoding/json"
	"log/slog"
	"sort"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

const (
	extraSettingSandboxPolicy     = "sandbox_policy"
	extraSettingNetworkAccess     = "network_access"
	extraSettingCollaborationMode = "collaboration_mode"
	extraSettingServiceTier       = "service_tier"
)

func cloneExtraSettings(src map[string]string) map[string]string {
	if len(src) == 0 {
		return map[string]string{}
	}
	dst := make(map[string]string, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

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
	keys := make([]string, 0, len(settings))
	for k, v := range settings {
		if v != "" {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return "{}"
	}
	sort.Strings(keys)
	ordered := make(map[string]string, len(keys))
	for _, k := range keys {
		ordered[k] = settings[k]
	}
	data, err := json.Marshal(ordered)
	if err != nil {
		slog.Error("failed to marshal extra_settings; using empty object", "error", err)
		return "{}"
	}
	return string(data)
}

func mergeExtraSettings(current, incoming map[string]string) map[string]string {
	merged := cloneExtraSettings(current)
	for k, v := range incoming {
		if v == "" {
			delete(merged, k)
			continue
		}
		merged[k] = v
	}
	return merged
}

func extraSettingOrDefault(settings map[string]string, key, fallback string) string {
	if v := settings[key]; v != "" {
		return v
	}
	return fallback
}

func sandboxPolicyFromExtras(settings map[string]string) string {
	return extraSettingOrDefault(settings, extraSettingSandboxPolicy, agent.CodexDefaultSandboxPolicy)
}

func networkAccessFromExtras(settings map[string]string) string {
	return extraSettingOrDefault(settings, extraSettingNetworkAccess, agent.CodexDefaultNetworkAccess)
}

func collaborationModeFromExtras(settings map[string]string, provider leapmuxv1.AgentProvider) string {
	mode := settings[extraSettingCollaborationMode]
	if provider == leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX {
		return agent.StringOrDefault(mode, agent.CodexDefaultCollaborationMode)
	}
	return mode
}

func serviceTierFromExtras(settings map[string]string, provider leapmuxv1.AgentProvider) string {
	tier := settings[extraSettingServiceTier]
	if provider == leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX {
		return agent.StringOrDefault(tier, agent.CodexDefaultServiceTier)
	}
	return tier
}

func codexExtrasResolved(settings map[string]string, provider leapmuxv1.AgentProvider) map[string]string {
	if provider != leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX {
		return cloneExtraSettings(settings)
	}
	resolved := cloneExtraSettings(settings)
	if resolved[extraSettingSandboxPolicy] == "" {
		resolved[extraSettingSandboxPolicy] = agent.CodexDefaultSandboxPolicy
	}
	if resolved[extraSettingNetworkAccess] == "" {
		resolved[extraSettingNetworkAccess] = agent.CodexDefaultNetworkAccess
	}
	if resolved[extraSettingCollaborationMode] == "" {
		resolved[extraSettingCollaborationMode] = agent.CodexDefaultCollaborationMode
	}
	if resolved[extraSettingServiceTier] == "" {
		resolved[extraSettingServiceTier] = agent.CodexDefaultServiceTier
	}
	return resolved
}
