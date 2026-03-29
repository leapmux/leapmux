package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
)

// thirdPartyProviderEnvVars are the env vars that indicate a third-party LLM
// provider is in use, meaning --model/--effort args should not be passed.
var thirdPartyProviderEnvVars = []string{
	"CLAUDE_CODE_USE_BEDROCK",
	"CLAUDE_CODE_USE_FOUNDRY",
	"CLAUDE_CODE_USE_VERTEX",
}

// detectThirdPartyProvider checks whether any third-party LLM provider env
// vars are configured, either in Claude Code settings files or in the process
// environment. Settings are merged in priority order per Claude Code docs:
// User (lowest) < Project < Local < Managed (highest).
func detectThirdPartyProvider(homeDir, workingDir string) bool {
	// Merge env vars from settings files (lowest to highest priority).
	merged := make(map[string]string)

	// 1. User settings (lowest priority).
	mergeSettingsEnv(merged, filepath.Join(homeDir, ".claude", "settings.json"))

	// 2. Project settings.
	mergeSettingsEnv(merged, filepath.Join(workingDir, ".claude", "settings.json"))

	// 3. Local project settings.
	mergeSettingsEnv(merged, filepath.Join(workingDir, ".claude", "settings.local.json"))

	// 4. Managed settings (highest priority).
	if path := managedSettingsPath(); path != "" {
		mergeSettingsEnv(merged, path)
	}

	// Check merged settings for third-party provider vars.
	for _, key := range thirdPartyProviderEnvVars {
		if v, ok := merged[key]; ok && v != "" {
			return true
		}
	}

	// Also check process environment as fallback (someone may have
	// exported these vars in their shell profile).
	for _, key := range thirdPartyProviderEnvVars {
		if os.Getenv(key) != "" {
			return true
		}
	}

	return false
}

// mergeSettingsEnv reads a Claude Code settings JSON file and merges its "env"
// entries into dst. Later calls override earlier values for the same key.
func mergeSettingsEnv(dst map[string]string, path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return // File doesn't exist or unreadable — skip silently.
	}

	var settings struct {
		Env map[string]string `json:"env"`
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		return // Malformed JSON — skip silently.
	}

	for k, v := range settings.Env {
		dst[k] = v
	}
}

// managedSettingsPath returns the OS-specific path for managed settings.
func managedSettingsPath() string {
	switch runtime.GOOS {
	case "darwin":
		return "/Library/Application Support/ClaudeCode/managed-settings.json"
	case "linux":
		return "/etc/claude-code/managed-settings.json"
	case "windows":
		return `C:\Program Files\ClaudeCode\managed-settings.json`
	default:
		return ""
	}
}
