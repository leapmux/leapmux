package agent_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionids"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

// The default-model ladder (defaultModelIDForList) tells three kinds of provider
// apart. These synthetic registrations state each kind's facts explicitly, so the
// ladder tests depend on no provider's catalog.
const (
	// sentinelProvider reports a "default" sentinel entry in its catalog, and its
	// configured default is that sentinel, as Claude Code's is.
	sentinelProvider = leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE
	// configuredProvider has a configured default, the sentinel, but does not
	// report the sentinel entry itself, as Codex does.
	configuredProvider = leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX
	// unconfiguredProvider has no configured default, as an ACP provider that
	// marks its current model itself.
	unconfiguredProvider = leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE

	// Each kind has an operator override of its own, so a test sets only the one
	// it is about, and an ambient override of a real provider cannot reach it.
	sentinelOverrideEnv     = "LEAPMUX_TEST_SENTINEL_DEFAULT_MODEL"
	configuredOverrideEnv   = "LEAPMUX_TEST_CONFIGURED_DEFAULT_MODEL"
	unconfiguredOverrideEnv = "LEAPMUX_TEST_UNCONFIGURED_DEFAULT_MODEL"
)

// sentinelPlugin reports the "default" sentinel entry in its catalog.
type sentinelPlugin struct{ agent.ProviderDefaults }

func (sentinelPlugin) ReportsDefaultModelSentinel() bool { return true }

// ladderRegistry returns a registry of the three kinds of provider. The sentinel
// provider's normalizer maps the fully-qualified "claude-opus-4-8[1m]" to its
// alias "opus[1m]", the one spelling pair these tests need.
func ladderRegistry() *agent.Registry {
	sentinel := testRegistration(sentinelProvider)
	sentinel.Plugin = sentinelPlugin{}
	sentinel.DefaultModels = []*agent.ModelInfo{agent.AccountDefaultModelEntry("Use the account's default model"), {Id: "opus[1m]"}}
	sentinel.EnvModelKey = sentinelOverrideEnv
	sentinel.NormalizeModelID = func(id string) string {
		if id == "claude-opus-4-8[1m]" {
			return "opus[1m]"
		}
		return id
	}
	configured := testRegistration(configuredProvider)
	configured.DefaultModels = []*agent.ModelInfo{agent.AccountDefaultModelEntry("Use the account's default model"), {Id: "gpt-5.4"}}
	configured.EnvModelKey = configuredOverrideEnv
	unconfigured := testRegistration(unconfiguredProvider)
	unconfigured.EnvModelKey = unconfiguredOverrideEnv
	return agenttest.MustNewRegistry(sentinel, configured, unconfigured)
}

// TestModelGroupDefault_PreservesACPCurrentModel verifies that for a provider with
// no configured default (like the ACP providers that self-mark the currently-selected
// model in buildACPModels), the ladder leaves the per-agent default in place instead
// of promoting the first entry. Regression guard: defaultModelIDForList must return
// "" rather than falling through to "mark the first entry" when there is no
// configured default to anchor on.
func TestModelGroupDefault_PreservesACPCurrentModel(t *testing.T) {
	r := ladderRegistry()
	require.Empty(t, r.DefaultModel(unconfiguredProvider), "precondition: no configured default")

	// buildACPModels marks the active model; here that's the 2nd entry.
	models := []*agent.ModelInfo{
		{Id: "anthropic/claude-x", DisplayName: "Claude X"},
		{Id: "openai/gpt-y", DisplayName: "GPT Y", IsDefault: true},
		{Id: "xai/grok-z", DisplayName: "Grok Z"},
	}

	assert.Equal(t, "openai/gpt-y", agenttest.DerivedModelDefault(t, r, models, unconfiguredProvider),
		"the current model keeps the badge; the first entry must not be promoted")

	// An operator override still wins and moves the badge to the override target.
	t.Setenv(unconfiguredOverrideEnv, "xai/grok-z")
	assert.Equal(t, "xai/grok-z", agenttest.DerivedModelDefault(t, r, models, unconfiguredProvider),
		"the override clears the per-agent badge and marks its target")
}

// TestModelGroupDefault_ToleratesNilAndHiddenEntries covers the reduction
// withModelGroupDefaultMarked performs on a catalog carrying nil and hidden
// entries: ModelOptionGroup drops them, so "the highest-preference entry present"
// is the first VISIBLE model, and a nil-bearing catalog must not panic.
func TestModelGroupDefault_ToleratesNilAndHiddenEntries(t *testing.T) {
	r := ladderRegistry()
	require.NotEmpty(t, r.DefaultModel(configuredProvider), "precondition: a configured default")

	// No entry designates a default and the configured default is absent, so the
	// ladder falls back to the first entry the group actually carries.
	assert.Equal(t, "visible-a", agenttest.DerivedModelDefault(t, r, []*agent.ModelInfo{
		nil,
		{Id: "hidden-one", Hidden: true},
		{Id: "visible-a"},
		nil,
		{Id: "visible-b"},
	}, configuredProvider), "nil and hidden entries are skipped when picking the fallback")

	// A designated default deeper in the list still wins over that fallback.
	assert.Equal(t, "visible-b", agenttest.DerivedModelDefault(t, r, []*agent.ModelInfo{
		nil,
		{Id: "visible-a"},
		{Id: "visible-b", IsDefault: true},
	}, configuredProvider), "the designated default outranks the first-entry fallback")
}

// TestModelGroupDefault_SentinelIsClaudeOnly verifies that only a
// provider that reports the sentinel treats an entry with the id "default" as the
// sentinel and prefers it. For every other provider "default" is an ordinary model
// id, so the self-marked current model must keep the badge.
func TestModelGroupDefault_SentinelIsClaudeOnly(t *testing.T) {
	r := ladderRegistry()

	// A list containing a model id'd "default" with a DIFFERENT self-marked current
	// model. The self-marked model must keep the badge.
	models := []*agent.ModelInfo{
		{Id: "default", DisplayName: "Some Local Default Model"},
		{Id: "openai/gpt-y", DisplayName: "GPT Y", IsDefault: true},
	}
	assert.Equal(t, "openai/gpt-y", agenttest.DerivedModelDefault(t, r, models, unconfiguredProvider),
		"a 'default'-id model is not the sentinel for a provider that reports none; the self-marked current model keeps the badge")

	// The same list under a provider that reports the sentinel: the rule DOES apply,
	// so the "default" entry is badged.
	assert.Equal(t, agent.DefaultModelSentinel, agenttest.DerivedModelDefault(t, r, models, sentinelProvider),
		"a provider that reports the sentinel badges the 'default' entry")
}

// TestModelGroupDefault_PreservesProviderDefaultWhenConfiguredAbsent verifies that
// for a provider WITH a configured default (e.g. Codex), when that configured
// default is absent from an account-specific list, the ladder respects a default the
// provider already designated on the list itself (Codex's queryAvailableModels
// copies the CLI's isDefault) rather than promoting the first entry. Regression
// guard: the step-3 fallback must not move the badge off the model the CLI marked
// just because the registry default isn't offered.
func TestModelGroupDefault_PreservesProviderDefaultWhenConfiguredAbsent(t *testing.T) {
	r := ladderRegistry()
	configured := r.DefaultModel(configuredProvider)
	require.NotEmpty(t, configured, "precondition: a configured default")

	// An account-specific list that does NOT contain the configured default, with
	// the CLI's own default marked on the 2nd (non-first) entry.
	models := []*agent.ModelInfo{
		{Id: "codex-mini", DisplayName: "Mini"},
		{Id: "codex-pro", DisplayName: "Pro", IsDefault: true},
	}
	require.Nil(t, agent.FindAvailableModel(models, configured), "precondition: configured default absent from list")

	assert.Equal(t, "codex-pro", agenttest.DerivedModelDefault(t, r, models, configuredProvider),
		"the provider-marked default keeps its badge; the first entry must not be promoted")

	// Sanity: with NO entry pre-marked, the badge still falls back to the first
	// entry so the picker always shows a default.
	unmarked := []*agent.ModelInfo{{Id: "codex-mini"}, {Id: "codex-pro"}}
	assert.Equal(t, "codex-mini", agenttest.DerivedModelDefault(t, r, unmarked, configuredProvider),
		"no designated default -> first entry marked")
}

// TestModelGroupDefault_EnvOverrideAbsentFallsThrough verifies that an operator
// default-model override pointing at a model the (account-specific) list does not
// contain -- or naming it with a different spelling than the catalog stores -- does
// NOT leave the group badging nothing. defaultModelIDForList honors the override only
// when it resolves to a model in the list (by exact id or provider-normalized alias);
// otherwise it falls through the ladder so the picker still shows a default.
func TestModelGroupDefault_EnvOverrideAbsentFallsThrough(t *testing.T) {
	r := ladderRegistry()

	// A list with the account-default sentinel present.
	models := []*agent.ModelInfo{
		{Id: agent.DefaultModelSentinel, DisplayName: "Default (recommended)", IsDefault: true},
		{Id: "sonnet", DisplayName: "Sonnet"},
	}

	// Override names a model the account does not offer -> falls through to the
	// sentinel (the list-designated default); the badge is preserved.
	t.Setenv(sentinelOverrideEnv, "opus[1m]")
	assert.Equal(t, agent.DefaultModelSentinel, agenttest.DerivedModelDefault(t, r, models, sentinelProvider),
		"an absent override falls through to the sentinel rather than badging nothing")

	// Override names a PRESENT model with a fully-qualified spelling: it resolves to
	// the catalog's normalized alias and wins, moving the badge off the sentinel.
	present := []*agent.ModelInfo{
		{Id: agent.DefaultModelSentinel, DisplayName: "Default (recommended)", IsDefault: true},
		{Id: "opus[1m]", DisplayName: "Opus (1M context)"},
	}
	t.Setenv(sentinelOverrideEnv, "claude-opus-4-8[1m]")
	assert.Equal(t, "opus[1m]", agenttest.DerivedModelDefault(t, r, present, sentinelProvider),
		"a fully-qualified override matches the normalized opus[1m] and takes the badge off the sentinel")
}

// TestWithModelGroupDefaultMarked_ReDerivesProtoModelGroupDefault is the [V26] guard for the
// proto-shape default-marking path run on every OptionGroups read: it re-derives the model group's
// DefaultValue via the defaultModelIDForList ladder, straight off the group's own options, and
// leaves non-model groups untouched by reference.
func TestWithModelGroupDefaultMarked_ReDerivesProtoModelGroupDefault(t *testing.T) {
	r := ladderRegistry()
	other := agent.SelectGroup("fastMode", "Fast Mode", agent.OptionOrderProviderSecond, "off", []agent.OptionDef{
		{Id: "on", Name: "On"}, {Id: "off", Name: "Off", Default: true},
	})
	// A fully-qualified operator override resolves to the normalized catalog alias opus[1m].
	t.Setenv(sentinelOverrideEnv, "claude-opus-4-8[1m]")

	// (1) Re-derivation: the model group's default moves off the sentinel onto opus[1m]; the
	// non-model group is returned by the same reference (only the model group is re-cloned).
	stale := []*leapmuxv1.AvailableOptionGroup{
		agent.SelectGroup(agent.OptionIDModel, "Model", agent.OptionOrderModel, agent.DefaultModelSentinel, []agent.OptionDef{
			{Id: agent.DefaultModelSentinel, Name: "Default (recommended)", Default: true},
			{Id: "opus[1m]", Name: "Opus (1M context)"},
		}),
		other,
	}
	got := r.WithModelGroupDefaultMarkedForTest(stale, sentinelProvider)
	require.Len(t, got, 2)
	assert.Equal(t, "opus[1m]", optionids.GroupByID(got, agent.OptionIDModel).GetDefaultValue(),
		"the model group's default is re-derived to the override's normalized id")
	assert.Same(t, other, got[1], "a non-model group is returned by the same reference, untouched")

	// (2) Already-correct fast path: when the model group's default already matches the derived
	// id, the input is returned unchanged (the model group is not re-cloned).
	correct := []*leapmuxv1.AvailableOptionGroup{
		agent.SelectGroup(agent.OptionIDModel, "Model", agent.OptionOrderModel, "opus[1m]", []agent.OptionDef{
			{Id: agent.DefaultModelSentinel, Name: "Default (recommended)"},
			{Id: "opus[1m]", Name: "Opus (1M context)", Default: true},
		}),
		other,
	}
	result := r.WithModelGroupDefaultMarkedForTest(correct, sentinelProvider)
	assert.Same(t, correct[0], result[0], "an already-correct default returns the input unchanged, not a re-clone")
}

// TestModelGroupDefault_ClaudeNoSentinelFallsBackToFirst covers
// the branch of defaultModelIDForList step 3 for a provider that reports the
// sentinel: a CLI reporting concrete models but NO "default" sentinel (and no
// operator override) falls back to badging the first model, so the picker still
// shows a default.
func TestModelGroupDefault_ClaudeNoSentinelFallsBackToFirst(t *testing.T) {
	r := ladderRegistry()
	require.Equal(t, agent.DefaultModelSentinel, r.DefaultModel(sentinelProvider), "precondition: the configured default is the sentinel")

	models := []*agent.ModelInfo{
		{Id: "opus", DisplayName: "Opus"},
		{Id: "sonnet", DisplayName: "Sonnet"},
	}
	require.Nil(t, agent.FindAvailableModel(models, agent.DefaultModelSentinel), "precondition: no sentinel in the list")

	assert.Equal(t, "opus", agenttest.DerivedModelDefault(t, r, models, sentinelProvider),
		"no sentinel -> first concrete model badged")
}

// TestModelGroupDefault_EmptyAndAllNilCatalogs covers the degenerate catalogs the
// live path must survive: ModelOptionGroup drops nil entries, so an all-nil (or
// empty) catalog projects to NO model group at all, and withModelGroupDefaultMarked
// must hand such a group set back untouched rather than deref a missing group.
func TestModelGroupDefault_EmptyAndAllNilCatalogs(t *testing.T) {
	assert.Nil(t, agent.ModelOptionGroup(nil, "", nil), "an empty catalog projects to no model group")
	assert.Nil(t, agent.ModelOptionGroup([]*agent.ModelInfo{nil, nil}, "", nil),
		"an all-nil catalog projects to no model group, no panic")

	other := agent.SelectGroup("fastMode", "Fast Mode", agent.OptionOrderProviderSecond, "off", []agent.OptionDef{
		{Id: "on", Name: "On"}, {Id: "off", Name: "Off", Default: true},
	})
	groups := []*leapmuxv1.AvailableOptionGroup{other}
	got := ladderRegistry().WithModelGroupDefaultMarkedForTest(groups, sentinelProvider)
	require.Len(t, got, 1)
	assert.Same(t, other, got[0], "no model group -> the input is returned untouched")
}

// TestModelGroupDefault_CodexBadgesTheSentinelOnceReconciled covers the badge for a
// RUNNING agent of a provider whose configured default is the sentinel, as Codex's
// is. Codex's reconcileModelCatalog puts the account-default row back into the live
// catalog that model/list omits, so the ladder badges it at the configured-default
// step -- ahead of the concrete model the CLI marked isDefault. Before the
// reconciliation the sentinel was absent from a live list and the badge landed on
// that concrete model instead, so this pins which of the two the picker highlights.
func TestModelGroupDefault_CodexBadgesTheSentinelOnceReconciled(t *testing.T) {
	r := ladderRegistry()
	require.Equal(t, agent.DefaultModelSentinel, r.DefaultModel(configuredProvider), "precondition: the configured default is the sentinel")

	reconciled := []*agent.ModelInfo{
		agent.AccountDefaultModelEntry("Use the account's default model"),
		{Id: "gpt-5.6-sol", DisplayName: "GPT-5.6-Sol", IsDefault: true},
		{Id: "gpt-5.4", DisplayName: "GPT-5.4"},
	}
	assert.Equal(t, agent.DefaultModelSentinel, agenttest.DerivedModelDefault(t, r, reconciled, configuredProvider),
		"the account default carries the badge once the sentinel is listed")

	// A live list the reconciliation never touched (it no-ops on an empty catalog,
	// and an older persisted catalog can lack the row) still badges the model the
	// CLI marked, rather than badging nothing.
	rawLive := []*agent.ModelInfo{
		{Id: "gpt-5.6-sol", DisplayName: "GPT-5.6-Sol", IsDefault: true},
		{Id: "gpt-5.4", DisplayName: "GPT-5.4"},
	}
	assert.Equal(t, "gpt-5.6-sol", agenttest.DerivedModelDefault(t, r, rawLive, configuredProvider),
		"with no sentinel listed the CLI's own default keeps the badge")
}
