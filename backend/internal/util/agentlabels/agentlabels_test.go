package agentlabels

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// TestDisplayName keeps the backend label mapping in lockstep with the
// frontend's agentProviderLabel (frontend/src/components/common/
// AgentProviderIcon.tsx). When a new provider is added to the
// AgentProvider enum, both sides should be updated together so
// "Starting {provider}…" renders consistently.
func TestDisplayName(t *testing.T) {
	cases := []struct {
		provider leapmuxv1.AgentProvider
		want     string
	}{
		{leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, "Claude Code"},
		{leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, "Codex"},
		{leapmuxv1.AgentProvider_AGENT_PROVIDER_GEMINI_CLI, "Gemini CLI"},
		{leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE, "OpenCode"},
		{leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT, "GitHub Copilot"},
		{leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR, "Cursor"},
		{leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE, "Goose"},
		{leapmuxv1.AgentProvider_AGENT_PROVIDER_KILO, "Kilo"},
		{leapmuxv1.AgentProvider_AGENT_PROVIDER_PI, "Pi"},
		{leapmuxv1.AgentProvider_AGENT_PROVIDER_UNSPECIFIED, "agent"},
		{leapmuxv1.AgentProvider(9999), "agent"},
	}
	for _, tc := range cases {
		t.Run(tc.provider.String(), func(t *testing.T) {
			assert.Equal(t, tc.want, DisplayName(tc.provider))
		})
	}
}

// TestCLIAlias pins the hyphenated short form CLIAlias emits for each
// provider. Adding a new AgentProvider proto value requires extending
// CLIAlias too — otherwise `LEAPMUX_REMOTE_AGENT_PROVIDER` silently
// drops the new provider on spawn and `leapmux remote tab open` won't
// inherit it. The parity test below verifies every CLIAlias value
// round-trips through ParseProvider.
func TestCLIAlias(t *testing.T) {
	cases := []struct {
		provider leapmuxv1.AgentProvider
		want     string
	}{
		{leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, "claude-code"},
		{leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, "codex"},
		{leapmuxv1.AgentProvider_AGENT_PROVIDER_GEMINI_CLI, "gemini"},
		{leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE, "opencode"},
		{leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT, "copilot"},
		{leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR, "cursor"},
		{leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE, "goose"},
		{leapmuxv1.AgentProvider_AGENT_PROVIDER_KILO, "kilo"},
		{leapmuxv1.AgentProvider_AGENT_PROVIDER_PI, "pi"},
		{leapmuxv1.AgentProvider_AGENT_PROVIDER_UNSPECIFIED, ""},
		{leapmuxv1.AgentProvider(9999), ""},
	}
	for _, tc := range cases {
		t.Run(tc.provider.String(), func(t *testing.T) {
			assert.Equal(t, tc.want, CLIAlias(tc.provider))
		})
	}
}

// TestCLIAlias_RoundTripsThroughParseProvider confirms that for every
// known provider the CLIAlias output is one of the inputs
// ParseProvider accepts and maps back to the same enum. Without this,
// the env-var injection could ship a string `leapmux remote` doesn't
// recognise.
func TestCLIAlias_RoundTripsThroughParseProvider(t *testing.T) {
	for _, p := range AllProviders() {
		alias := CLIAlias(p)
		require.NotEmptyf(t, alias, "CLIAlias must be defined for %v", p)
		got, ok := ParseProvider(alias)
		require.Truef(t, ok, "ParseProvider must accept CLIAlias(%v)=%q", p, alias)
		assert.Equalf(t, p, got, "ParseProvider(%q) must round-trip to %v", alias, p)
	}
}

// TestParseProvider_KnownAliasesAllMap pins the canonical names plus
// the lower-case aliases the CLI accepts as user-typed input. A new
// provider added to AgentProvider proto must show up here too,
// otherwise --provider <new> silently falls back to Claude Code (per
// the CLI's helper default) and the user is misled.
func TestParseProvider_KnownAliasesAllMap(t *testing.T) {
	cases := map[string]leapmuxv1.AgentProvider{
		"Claude Code": leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		"claude":      leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		"claude-code": leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		"Codex":       leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
		"codex":       leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
		"Gemini":      leapmuxv1.AgentProvider_AGENT_PROVIDER_GEMINI_CLI,
		"gemini":      leapmuxv1.AgentProvider_AGENT_PROVIDER_GEMINI_CLI,
		"Cursor":      leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR,
		"cursor":      leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR,
		"Copilot":     leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT,
		"copilot":     leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT,
		"Kilo":        leapmuxv1.AgentProvider_AGENT_PROVIDER_KILO,
		"kilo":        leapmuxv1.AgentProvider_AGENT_PROVIDER_KILO,
		"OpenCode":    leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE,
		"opencode":    leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE,
		"Goose":       leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE,
		"goose":       leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE,
		"Pi":          leapmuxv1.AgentProvider_AGENT_PROVIDER_PI,
		"pi":          leapmuxv1.AgentProvider_AGENT_PROVIDER_PI,
	}
	for in, want := range cases {
		got, ok := ParseProvider(in)
		assert.True(t, ok, "ParseProvider(%q)", in)
		assert.Equal(t, want, got, "ParseProvider(%q)", in)
	}
}

// TestParseProvider_UnknownReturnsFalse documents the explicit
// "unrecognized input" path. Callers that want a default (CLI flag)
// substitute it themselves; callers that want strict validation
// (admin RPC) reject the input. The package itself returns no enum
// value to avoid baking a one-size-fits-all default into the parser.
func TestParseProvider_UnknownReturnsFalse(t *testing.T) {
	for _, in := range []string{"", "not-a-provider", "CLAUDE"} {
		_, ok := ParseProvider(in)
		assert.False(t, ok, "ParseProvider(%q) should report unknown", in)
	}
}

// TestAllProviders_IsComplete pins the AllProviders enumeration against
// every alias entry. A new AgentProvider proto value must be added to
// AllProviders alongside DisplayName / providerAliases; this test fails
// loudly when the lists drift, so the CLI's --provider error messages
// stay in sync with what the parser accepts.
func TestAllProviders_IsComplete(t *testing.T) {
	all := AllProviders()
	seen := map[leapmuxv1.AgentProvider]bool{}
	for _, p := range all {
		seen[p] = true
		assert.NotEqual(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_UNSPECIFIED, p, "AllProviders must not include UNSPECIFIED")
	}
	assert.Len(t, seen, len(all), "AllProviders must not contain duplicates")
	for _, p := range providerAliases {
		assert.Truef(t, seen[p], "AllProviders is missing %v (referenced by providerAliases)", p)
	}
}

// TestAliasesFor_CanonicalFirstThenSorted pins the contract used by CLI
// callers that render `agent providers` rows: the canonical display
// name always leads, remaining aliases follow in lexicographic order,
// and no duplicate of the canonical name appears in the tail.
func TestAliasesFor_CanonicalFirstThenSorted(t *testing.T) {
	cases := map[leapmuxv1.AgentProvider][]string{
		leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE:    {"Claude Code", "claude", "claude-code"},
		leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX:          {"Codex", "codex"},
		leapmuxv1.AgentProvider_AGENT_PROVIDER_GEMINI_CLI:     {"Gemini CLI", "Gemini", "gemini"},
		leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT: {"GitHub Copilot", "Copilot", "copilot"},
		leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR:         {"Cursor", "cursor"},
		leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE:       {"OpenCode", "opencode"},
		leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE:          {"Goose", "goose"},
		leapmuxv1.AgentProvider_AGENT_PROVIDER_KILO:           {"Kilo", "kilo"},
		leapmuxv1.AgentProvider_AGENT_PROVIDER_PI:             {"Pi", "pi"},
	}
	for provider, want := range cases {
		got := AliasesFor(provider)
		// Canonical comes first, regardless of lexicographic order.
		require.NotEmpty(t, got, "AliasesFor(%v) returned empty", provider)
		assert.Equalf(t, DisplayName(provider), got[0], "canonical name must be first for %v", provider)
		// Tail must be sorted.
		tail := append([]string(nil), got[1:]...)
		sortedTail := append([]string(nil), tail...)
		sort.Strings(sortedTail)
		assert.Equalf(t, sortedTail, tail, "tail must be lexicographically sorted for %v", provider)
		// Every expected alias must be present exactly once.
		assert.ElementsMatchf(t, want, got, "AliasesFor(%v) alias set", provider)
	}
}

// TestAliasesFor_UnspecifiedReturnsNil pins the zero-value contract:
// AGENT_PROVIDER_UNSPECIFIED has no aliases. CLI render paths use
// AliasesFor in a loop over the worker's installed providers; a nil
// return for an unexpected unspecified value lets callers omit the
// entry without a special case.
func TestAliasesFor_UnspecifiedReturnsNil(t *testing.T) {
	assert.Nil(t, AliasesFor(leapmuxv1.AgentProvider_AGENT_PROVIDER_UNSPECIFIED))
}
