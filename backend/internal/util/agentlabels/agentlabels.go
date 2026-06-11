// Package agentlabels owns the two human-facing label tables for the
// AgentProvider proto enum: DisplayName (enum → user-facing string) and
// ParseProvider (free-form input → enum). Both the worker package
// (`internal/worker/agent`) and the remote CLI (`internal/cli/remote`)
// depend on these mappings, but the worker package pulls in a large
// dependency tree that the CLI shouldn't have to inherit just to render
// a label — so the tables live in a leaf package both can import.
//
// Keep this file in lockstep with
// frontend/src/components/common/AgentProviderIcon.tsx agentProviderLabel.
//
// Moving the display strings into the proto via EnumValueOptions custom
// extensions was considered and rejected: descriptor-introspection
// plumbing on both Go and TS sides is disproportionate for a handful of
// labels, and the test coverage in this package catches Go-side drift
// before it ships.
package agentlabels

import (
	"sort"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// CLIAlias returns the canonical kebab-case identifier that the
// `leapmux remote` CLI accepts (and emits) for an AgentProvider. This
// is the hyphenated short form embedded in
// `LEAPMUX_REMOTE_AGENT_PROVIDER` so a child `leapmux remote tab open
// --type agent` invocation can inherit the parent's provider with zero
// flags. Unknown / unspecified providers return "" so callers can
// guard the env-var emit with `if alias != ""`.
//
// Kept in lockstep with the long-form ParseProvider table — every
// alias here is also in providerAliases so a value emitted by
// CLIAlias parses back to the same enum.
func CLIAlias(provider leapmuxv1.AgentProvider) string {
	switch provider {
	case leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE:
		return "claude-code"
	case leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX:
		return "codex"
	case leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE:
		return "opencode"
	case leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT:
		return "copilot"
	case leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR:
		return "cursor"
	case leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE:
		return "goose"
	case leapmuxv1.AgentProvider_AGENT_PROVIDER_KILO:
		return "kilo"
	case leapmuxv1.AgentProvider_AGENT_PROVIDER_PI:
		return "pi"
	case leapmuxv1.AgentProvider_AGENT_PROVIDER_REASONIX:
		return "reasonix"
	default:
		return ""
	}
}

// DisplayName returns a human-readable label for an AgentProvider
// (e.g. "Claude Code", "GitHub Copilot"). Unknown providers render as
// "agent" so log lines and tooltips never expose the bare enum int.
func DisplayName(provider leapmuxv1.AgentProvider) string {
	switch provider {
	case leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE:
		return "Claude Code"
	case leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX:
		return "Codex"
	case leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE:
		return "OpenCode"
	case leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT:
		return "GitHub Copilot"
	case leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR:
		return "Cursor"
	case leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE:
		return "Goose"
	case leapmuxv1.AgentProvider_AGENT_PROVIDER_KILO:
		return "Kilo"
	case leapmuxv1.AgentProvider_AGENT_PROVIDER_PI:
		return "Pi"
	case leapmuxv1.AgentProvider_AGENT_PROVIDER_REASONIX:
		return "Reasonix"
	default:
		return "agent"
	}
}

// ParseProvider maps a free-form provider identifier — the canonical
// display name ("Claude Code"), a lowercase alias ("claude"), or a
// hyphenated short form ("claude-code") — to the matching
// AgentProvider enum value. Returns ok=false for unrecognized input so
// callers can choose how to handle the miss (CLI flag → reject with
// invalid_request; admin RPC → reject with INVALID_ARGUMENT).
func ParseProvider(s string) (leapmuxv1.AgentProvider, bool) {
	p, ok := providerAliases[s]
	return p, ok
}

// AllProviders returns every defined AgentProvider enum value in the
// order they appear in the proto. AGENT_PROVIDER_UNSPECIFIED is
// excluded — callers iterating to render UI / build CLI alias tables
// never want to surface the zero value.
func AllProviders() []leapmuxv1.AgentProvider {
	return []leapmuxv1.AgentProvider{
		leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_KILO,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_PI,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_REASONIX,
	}
}

// AliasesFor returns every string ParseProvider accepts as input for
// the given enum value, with the canonical display name first followed
// by the remaining aliases in lexicographic order. Returns nil for
// AGENT_PROVIDER_UNSPECIFIED or any enum value with no alias entry.
// The deterministic order lets callers embed the slice in CLI output /
// error messages without re-sorting at the call site.
func AliasesFor(provider leapmuxv1.AgentProvider) []string {
	if provider == leapmuxv1.AgentProvider_AGENT_PROVIDER_UNSPECIFIED {
		return nil
	}
	canonical := DisplayName(provider)
	var rest []string
	for alias, p := range providerAliases {
		if p != provider || alias == canonical {
			continue
		}
		rest = append(rest, alias)
	}
	if _, ok := providerAliases[canonical]; !ok && len(rest) == 0 {
		return nil
	}
	sort.Strings(rest)
	out := make([]string, 0, 1+len(rest))
	if _, ok := providerAliases[canonical]; ok {
		out = append(out, canonical)
	}
	return append(out, rest...)
}

// providerAliases is the inverse of DisplayName plus user-friendly
// lowercase / short-form aliases. Adding a new AgentProvider proto
// value requires updating both DisplayName and this map; the package
// test pins parity between the two so the drift is caught at CI.
var providerAliases = map[string]leapmuxv1.AgentProvider{
	"Claude Code":    leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	"claude":         leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	"claude-code":    leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
	"Codex":          leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	"codex":          leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	"Cursor":         leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR,
	"cursor":         leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR,
	"GitHub Copilot": leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT,
	"Copilot":        leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT,
	"copilot":        leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT,
	"Kilo":           leapmuxv1.AgentProvider_AGENT_PROVIDER_KILO,
	"kilo":           leapmuxv1.AgentProvider_AGENT_PROVIDER_KILO,
	"OpenCode":       leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE,
	"opencode":       leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE,
	"Goose":          leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE,
	"goose":          leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE,
	"Pi":             leapmuxv1.AgentProvider_AGENT_PROVIDER_PI,
	"pi":             leapmuxv1.AgentProvider_AGENT_PROVIDER_PI,
	"Reasonix":       leapmuxv1.AgentProvider_AGENT_PROVIDER_REASONIX,
	"reasonix":       leapmuxv1.AgentProvider_AGENT_PROVIDER_REASONIX,
}
