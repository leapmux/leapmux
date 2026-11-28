package agentenv

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSlicesHaveNoDuplicatesOrBlankKeys(t *testing.T) {
	t.Parallel()

	for name, keys := range map[string][]string{
		"HarnessDetectionMarkers": HarnessDetectionMarkers,
		"IdentityScrubKeys":       IdentityScrubKeys,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			require.NotEmpty(t, keys)

			seen := make(map[string]struct{}, len(keys))
			for _, key := range keys {
				assert.NotEmpty(t, key, "key must not be empty")
				assert.Equal(t, strings.TrimSpace(key), key, "key %q must not contain surrounding whitespace", key)
				_, duplicate := seen[key]
				assert.Falsef(t, duplicate, "%s contains duplicate key %q", name, key)
				seen[key] = struct{}{}
			}
		})
	}
}

func TestHarnessDetectionMarkersCoverSupportedHarnesses(t *testing.T) {
	t.Parallel()

	expected := []string{
		// Claude Code
		"CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT", "CLAUDE_CODE_SESSION_ID", "CLAUDE_CODE_SSE_PORT",
		// Codex
		"CODEX_CI", "CODEX_THREAD_ID", "CODEX_SANDBOX",
		// OpenCode
		"OPENCODE", "OPENCODE_CLIENT", "OPENCODE_RUN_ID",
		// Kilo
		"KILO_CLIENT", "KILO_RUN_ID",
		// MiMo Code
		"MIMOCODE", "MIMOCODE_CLIENT", "MIMOCODE_RUN_ID",
		// Gemini CLI, Antigravity, Cursor, Aider
		"GEMINI_CLI", "ANTIGRAVITY_AGENT", "CURSOR_AGENT", "AIDER",
		// Goose, Pi, Oh My Pi
		"GOOSE_TERMINAL", "PI_CODING_AGENT", "PI_SESSION_FILE",
		// Factory Droid, Letta Code
		"FACTORY_SESSION_PARENT_ID", "DROID_PARENT_SESSION_ID", "LETTA_AGENT_ID", "LETTA_CONVERSATION_ID",
		// Grok Build, Qwen Code, Kiro, Codewhale, Amp, Cline
		"GROK_SESSION_ID", "QWEN_CODE", "QWEN_CODE_SESSION_ID", "KIRO_SESSION_ID",
		"CODEWHALE_SANDBOX", "DEEPSEEK_SANDBOX", "CODEWHALE_SESSION_ID",
		"AMP_THREAD_ID", "AMP_CURRENT_THREAD_ID",
		"CLINE_SANDBOX", "CLINE_RUN_AS_HUB_DAEMON",
	}

	for _, key := range expected {
		assert.Containsf(t, HarnessDetectionMarkers, key, "HarnessDetectionMarkers must include %q", key)
	}
}

func TestHarnessDetectionMarkersExcludeGenericAndPreferenceKeys(t *testing.T) {
	t.Parallel()

	excluded := []string{
		// Generic trace, memory, transcript, and ambient agent keys.
		"TRACEPARENT", "TRACESTATE", "AI_AGENT", "AGENT",
		"AGENT_ID", "AGENT_SESSION_ID", "AGENT_THREAD_ID",
		"CONVERSATION_ID", "MEMORY_DIR", "TRANSCRIPT_PATH",
		// User-configurable effort overrides.
		"CLAUDE_EFFORT", "CLAUDE_CODE_EFFORT_LEVEL",
		// CI is checked separately by callers that want to include CI.
		"CI",
	}

	for _, key := range excluded {
		assert.NotContainsf(t, HarnessDetectionMarkers, key, "HarnessDetectionMarkers must exclude %q to avoid shell false positives", key)
	}
}

func TestIdentityScrubKeysPreservePrimaryRCMarkersAndUserConfig(t *testing.T) {
	t.Parallel()

	excluded := []string{
		// Primary rc-detection and entrypoint markers handled by individual providers.
		"CLAUDECODE", "CODEX_CI", "OPENCODE_CLIENT", "KILO_CLIENT",
		"MIMOCODE_CLIENT", "CLAUDE_CODE_ENTRYPOINT",
		// Standalone detection-only markers.
		"OPENCODE", "GEMINI_CLI", "ANTIGRAVITY_AGENT", "CURSOR_AGENT", "AIDER",
		// Auth, config, and directory variables that must survive scrubbing.
		"CLAUDE_CODE_OAUTH_TOKEN", "OPENAI_API_KEY", "CODEX_API_KEY",
		"CODEX_HOME", "OPENCODE_CONFIG", "PI_CODING_AGENT_DIR",
		"AMP_URL", "CLINE_DIR", "CLINE_DATA_DIR",
	}

	for _, key := range excluded {
		assert.NotContainsf(t, IdentityScrubKeys, key, "IdentityScrubKeys must exclude %q", key)
	}
}

func TestIdentityScrubKeysIncludeScrubOnlyAndSharedSessionKeys(t *testing.T) {
	t.Parallel()

	required := []string{
		"TRACEPARENT", "TRACESTATE", "AI_AGENT", "AGENT",
		"AGENT_ID", "AGENT_SESSION_ID", "AGENT_THREAD_ID",
		"CONVERSATION_ID", "MEMORY_DIR", "TRANSCRIPT_PATH",
		"CLAUDE_CODE_EFFORT_LEVEL", "CLAUDE_EFFORT",
		"CLAUDE_CODE_SESSION_ID", "CODEX_THREAD_ID", "OPENCODE_RUN_ID",
		"KILO_RUN_ID", "MIMOCODE_RUN_ID", "QWEN_CODE_SESSION_ID",
		"LETTA_AGENT_ID", "AMP_THREAD_ID", "CLINE_SANDBOX",
	}

	for _, key := range required {
		assert.Containsf(t, IdentityScrubKeys, key, "IdentityScrubKeys must include %q", key)
	}
	for _, key := range sharedHarnessSessionMarkers {
		assert.Containsf(t, IdentityScrubKeys, key, "IdentityScrubKeys must include shared marker %q", key)
		assert.Containsf(t, HarnessDetectionMarkers, key, "HarnessDetectionMarkers must include shared marker %q", key)
	}
}
