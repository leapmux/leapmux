// Package agentlabels owns the two human-facing label tables for the
// AgentProvider proto enum: DisplayName (enum → user-facing string) and
// ParseProvider (free-form input → enum). Both the worker package
// (`internal/worker/agent`) and the control CLI (`internal/cli/control`)
// depend on these mappings, but the worker package pulls in a large
// dependency tree that the CLI shouldn't have to inherit just to render
// a label — so the functions live in a leaf package both can import.
//
// The TABLES are generated from contracts/providers.json
// (github.com/leapmux/leapmux/generated/contracts), which the browser's
// AgentProviderIcon.agentProviderLabel is generated from too — one source,
// both languages, and a proto enum value without an entry fails
// `task generate-contracts` instead of rendering blank on one side.
package agentlabels

import (
	"slices"
	"sort"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// CLIAlias returns the canonical kebab-case identifier that the
// `leapmux control` CLI accepts (and emits) for an AgentProvider. This
// is the hyphenated short form embedded in
// `LEAPMUX_CONTROL_AGENT_PROVIDER` so a child `leapmux control tab open
// --type agent` invocation can inherit the parent's provider with zero
// flags. Unknown / unspecified providers return "" so callers can
// guard the env-var emit with `if alias != ""`.
//
// Every alias here is also in the generated parse table, so a value
// emitted by CLIAlias parses back to the same enum.
func CLIAlias(provider leapmuxv1.AgentProvider) string {
	return contracts.ProviderCLIAlias[provider]
}

// DisplayName returns a human-readable label for an AgentProvider
// (e.g. "Claude Code", "GitHub Copilot"). Unknown providers render as
// "agent" so log lines and tooltips never expose the bare enum int.
func DisplayName(provider leapmuxv1.AgentProvider) string {
	name, ok := contracts.ProviderDisplayName[provider]
	if !ok {
		return "agent"
	}
	return name
}

// ParseProvider maps a free-form provider identifier — the canonical
// display name ("Claude Code"), a lowercase alias ("claude"), or a
// hyphenated short form ("claude-code") — to the matching
// AgentProvider enum value. Returns ok=false for unrecognized input so
// callers can choose how to handle the miss (CLI flag → reject with
// invalid_request; admin RPC → reject with INVALID_ARGUMENT).
func ParseProvider(s string) (leapmuxv1.AgentProvider, bool) {
	p, ok := contracts.ProviderParseAliases[s]
	return p, ok
}

// AllProviders returns every defined AgentProvider enum value in the
// order they appear in the proto. AGENT_PROVIDER_UNSPECIFIED is
// excluded — callers iterating to render UI / build CLI alias tables
// never want to surface the zero value.
func AllProviders() []leapmuxv1.AgentProvider {
	return slices.Clone(contracts.AllProviders)
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
	for alias, p := range contracts.ProviderParseAliases {
		if p != provider || alias == canonical {
			continue
		}
		rest = append(rest, alias)
	}
	if len(rest) == 0 {
		if _, ok := contracts.ProviderParseAliases[canonical]; !ok {
			return nil
		}
	}
	sort.Strings(rest)
	out := make([]string, 0, 1+len(rest))
	if _, ok := contracts.ProviderParseAliases[canonical]; ok {
		out = append(out, canonical)
	}
	return append(out, rest...)
}
