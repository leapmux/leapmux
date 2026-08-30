package agentlabels

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// The label VALUES live in contracts/providers.json and arrive here through
// the generated tables -- restating them in this file would rebuild the very
// pin (literal on both sides, kept green by discipline) that the contract
// replaced. What these tests pin is the BEHAVIOR of the functions over those
// tables: fallbacks, round-trips, ordering, and completeness.

// TestDisplayName_FallsBackToOneWordForUnknown keeps every surface that
// interpolates a provider into prose ("Starting {provider}…") from exposing
// a bare enum int when the enum arrives from a newer peer.
func TestDisplayName_FallsBackToOneWordForUnknown(t *testing.T) {
	assert.Equal(t, "agent", DisplayName(leapmuxv1.AgentProvider_AGENT_PROVIDER_UNSPECIFIED))
	assert.Equal(t, "agent", DisplayName(leapmuxv1.AgentProvider(9999)))
}

// TestDisplayName_EveryKnownProviderHasADistinctLabel guards the property
// the picker depends on: two providers must never share a label, or the
// dropdown renders one name for two entries.
func TestDisplayName_EveryKnownProviderHasADistinctLabel(t *testing.T) {
	seen := map[string]leapmuxv1.AgentProvider{}
	for _, p := range AllProviders() {
		label := DisplayName(p)
		require.NotEmptyf(t, label, "DisplayName(%v)", p)
		owner, dup := seen[label]
		require.Falsef(t, dup, "DisplayName collision: %v and %v both render %q", owner, p, label)
		seen[label] = p
	}
}

// TestCLIAlias_UnknownReturnsEmpty is the guard callers rely on when emitting
// LEAPMUX_CONTROL_AGENT_PROVIDER: an empty alias means "omit the env var",
// never "write the enum's String()".
func TestCLIAlias_UnknownReturnsEmpty(t *testing.T) {
	assert.Equal(t, "", CLIAlias(leapmuxv1.AgentProvider_AGENT_PROVIDER_UNSPECIFIED))
	assert.Equal(t, "", CLIAlias(leapmuxv1.AgentProvider(9999)))
}

// TestCLIAlias_RoundTripsThroughParseProvider confirms that for every
// known provider the CLIAlias output is one of the inputs
// ParseProvider accepts and maps back to the same enum. Without this,
// the env-var injection could ship a string `leapmux control` doesn't
// recognise. (The generator also cross-checks alias uniqueness at
// generation time; this is the runtime half.)
func TestCLIAlias_RoundTripsThroughParseProvider(t *testing.T) {
	for _, p := range AllProviders() {
		alias := CLIAlias(p)
		require.NotEmptyf(t, alias, "CLIAlias must be defined for %v", p)
		got, ok := ParseProvider(alias)
		require.Truef(t, ok, "ParseProvider must accept CLIAlias(%v)=%q", p, alias)
		assert.Equalf(t, p, got, "ParseProvider(%q) must round-trip to %v", alias, p)
	}
}

// TestParseProvider_DisplayNameAndAliasesMapBack pins the structural half of
// the parse table: for every provider, its display name and every alias the
// generated table carries for it must map back to that provider. A new
// provider arrives through the contract, so this stays true by construction
// -- and fails loudly if a future refactor breaks the reverse map.
func TestParseProvider_DisplayNameAndAliasesMapBack(t *testing.T) {
	for _, p := range AllProviders() {
		for _, in := range AliasesFor(p) {
			got, ok := ParseProvider(in)
			require.Truef(t, ok, "ParseProvider(%q) for %v", in, p)
			assert.Equalf(t, p, got, "ParseProvider(%q)", in)
		}
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

// TestAllProviders_MatchesTheGeneratedTable pins AllProviders against the
// generated enumeration (proto order, UNSPECIFIED excluded). The generator
// cross-checks the table against the proto enum at generation time; this is
// the Go-side view of the same property.
func TestAllProviders_MatchesTheGeneratedTable(t *testing.T) {
	assert.Equal(t, contracts.AllProviders, AllProviders())
	for _, p := range AllProviders() {
		assert.NotEqual(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_UNSPECIFIED, p, "AllProviders must not include UNSPECIFIED")
	}
	seen := map[leapmuxv1.AgentProvider]bool{}
	for _, p := range AllProviders() {
		seen[p] = true
	}
	assert.Len(t, seen, len(AllProviders()), "AllProviders must not contain duplicates")
	for _, p := range contracts.ProviderParseAliases {
		assert.Truef(t, seen[p], "AllProviders is missing %v (used by the generated parse table)", p)
	}
}

// TestAliasesFor_CanonicalFirstThenSorted pins the contract used by CLI
// callers that render `agent providers` rows: the canonical display
// name always leads, remaining aliases follow in lexicographic order,
// and the set is exactly the generated parse table's entries for that
// provider.
func TestAliasesFor_CanonicalFirstThenSorted(t *testing.T) {
	for _, provider := range AllProviders() {
		got := AliasesFor(provider)
		require.NotEmpty(t, got, "AliasesFor(%v) returned empty", provider)
		// Canonical comes first, regardless of lexicographic order.
		assert.Equalf(t, DisplayName(provider), got[0], "canonical name must be first for %v", provider)
		// Tail must be sorted.
		tail := append([]string(nil), got[1:]...)
		sortedTail := append([]string(nil), tail...)
		sort.Strings(sortedTail)
		assert.Equalf(t, sortedTail, tail, "tail must be lexicographically sorted for %v", provider)
		// No duplicate of the canonical name in the tail.
		for _, alias := range tail {
			assert.NotEqualf(t, got[0], alias, "canonical name must not repeat for %v", provider)
		}
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
