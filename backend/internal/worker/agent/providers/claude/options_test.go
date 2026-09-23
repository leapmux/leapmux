package claude

import (
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionids"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestValidateLaunchOptions_RejectsUnknownPermissionMode guards [S1]: for a CLI-managed provider
// whose permission modes are a FIXED enum (Claude/Codex), an explicitly-requested mode the provider
// doesn't offer is rejected, while a valid mode and an unsupplied mode pass.
func TestValidateLaunchOptions_RejectsUnknownPermissionMode(t *testing.T) {
	t.Parallel()

	registry := agenttest.MustNewRegistry(Registration())

	claude := leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE
	pmg := optionids.GroupByID(registry.StaticOptionGroups(claude), agent.OptionIDPermissionMode)
	require.NotNil(t, pmg)
	require.NotEmpty(t, pmg.GetOptions(), "Claude has a fixed permission-mode enum")
	validMode := pmg.GetOptions()[0].GetId()

	require.NoError(t, registry.ValidateLaunchOptions(claude, optionmap.Map{agent.OptionIDPermissionMode: validMode}),
		"a valid permission mode passes")
	require.NoError(t, registry.ValidateLaunchOptions(claude, optionmap.Map{}),
		"an unsupplied permission mode is skipped")
	require.Error(t, registry.ValidateLaunchOptions(claude, optionmap.Map{agent.OptionIDPermissionMode: "bogus-mode"}),
		"an unknown permission mode is rejected")
}

// TestValidateLaunchOptions_DoesNotValidateModelOrEffort guards [S1]: model and effort are NOT
// validated at spawn -- every provider (including Claude) discovers its model catalog and effort
// tiers from the running CLI, seeding only a fallback, so a value valid in the live catalog but
// absent from the seed must NOT be rejected. A model/effort not in any seed therefore passes.
func TestValidateLaunchOptions_DoesNotValidateModelOrEffort(t *testing.T) {
	t.Parallel()

	registry := agenttest.MustNewRegistry(Registration())

	claude := leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE
	require.NoError(t, registry.ValidateLaunchOptions(claude, optionmap.Map{agent.OptionIDModel: "a-future-model-not-in-the-seed"}),
		"a model absent from the seed (but maybe in the live catalog) is not rejected")
	require.NoError(t, registry.ValidateLaunchOptions(claude, optionmap.Map{agent.OptionIDEffort: "a-non-seed-effort"}),
		"effort is not validated at spawn")
}

// TestEffortSupportedByModel covers the helper sanitizeIncomingOptions uses to decide
// whether a requested effort survives a model switch. It reads each model's per-model effort
// sub_group (carried independently of which model is current), so it answers for a model
// other than the catalog's current one.
func TestEffortSupportedByModel(t *testing.T) {
	t.Parallel()

	registry := agenttest.MustNewRegistry(Registration())

	models := []*agent.ModelInfo{
		{Id: "opus", DisplayName: "Opus", DefaultEffort: "high", SupportedEfforts: []*agent.EffortInfo{
			{Id: "auto"}, {Id: "high"}, {Id: "xhigh"},
		}},
		{Id: "sonnet", DisplayName: "Sonnet", DefaultEffort: "high", SupportedEfforts: []*agent.EffortInfo{
			{Id: "auto"}, {Id: "high"},
		}},
		{Id: "haiku", DisplayName: "Haiku"}, // no effort axis
	}
	// Build a catalog whose CURRENT model is opus, then query OTHER models too.
	catalog := []*leapmuxv1.AvailableOptionGroup{agent.ModelOptionGroup(models, "opus", agent.EffortSubGroups)}
	claude := leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE

	assert.True(t, registry.EffortSupportedByModel(catalog, claude, "opus", "xhigh"), "opus offers xhigh")
	assert.True(t, registry.EffortSupportedByModel(catalog, claude, "sonnet", "high"), "sonnet offers high even though opus is current")
	assert.False(t, registry.EffortSupportedByModel(catalog, claude, "sonnet", "xhigh"), "sonnet does not offer xhigh")
	assert.False(t, registry.EffortSupportedByModel(catalog, claude, "haiku", "high"), "haiku has no effort axis")
	assert.False(t, registry.EffortSupportedByModel(catalog, claude, "unknown-model", "high"), "an unlisted model is unsupported")
	assert.False(t, registry.EffortSupportedByModel(nil, claude, "opus", "high"), "no catalog -> unsupported")
	assert.False(t, registry.EffortSupportedByModel(catalog, claude, "opus", "low"), "a tier the model doesn't list is unsupported")

	// A re-spelled model alias must resolve to its canonical catalog id: the
	// fully-qualified CLI spelling "claude-opus-4-8[1m]" normalizes to the catalog's
	// "opus[1m]". Matching raw would miss the model and wrongly reset a valid effort.
	aliasModels := []*agent.ModelInfo{
		{Id: "opus[1m]", DisplayName: "Opus (1M)", DefaultEffort: "high", SupportedEfforts: []*agent.EffortInfo{
			{Id: "auto"}, {Id: "high"}, {Id: "xhigh"},
		}},
	}
	aliasCatalog := []*leapmuxv1.AvailableOptionGroup{agent.ModelOptionGroup(aliasModels, "opus[1m]", agent.EffortSubGroups)}
	assert.True(t, registry.EffortSupportedByModel(aliasCatalog, claude, "claude-opus-4-8[1m]", "xhigh"),
		"a fully-qualified alias resolves to the canonical catalog id and finds its efforts")
	assert.True(t, registry.EffortSupportedByModel(aliasCatalog, claude, "OPUS[1M]", "xhigh"),
		"an uppercased alias normalizes and matches")

	// [V5] A model the picker HIDES (e.g. Claude's standard-context "opus", surfaced only as
	// the model group's current value, never as a selectable option because agent.ModelOptionGroup
	// drops Hidden models) carries no per-model effort sub_group. The top-level effort group is
	// nonetheless built for that current model (providerkit.ModelAndEffortGroups resolves it via
	// FindAvailableModel, which does NOT filter Hidden), so its effort must validate against
	// that group rather than wrongly resetting a valid tier to auto.
	hiddenModels := []*agent.ModelInfo{
		{Id: "opus", DisplayName: "Opus", DefaultEffort: "high", Hidden: true, SupportedEfforts: []*agent.EffortInfo{
			{Id: "auto"}, {Id: "high"}, {Id: "xhigh"},
		}},
		{Id: "sonnet", DisplayName: "Sonnet", DefaultEffort: "high", SupportedEfforts: []*agent.EffortInfo{
			{Id: "auto"}, {Id: "high"},
		}},
	}
	hiddenCatalog := providerkit.ModelAndEffortGroups(hiddenModels, "opus", "auto", agent.EffortGroupLabel, nil)
	require.Nil(t, agenttest.OptionByID(optionids.GroupByID(hiddenCatalog, agent.OptionIDModel), "opus"),
		"the hidden current model is not a selectable option")
	assert.True(t, registry.EffortSupportedByModel(hiddenCatalog, claude, "opus", "xhigh"),
		"the hidden current model's effort validates against the top-level effort group")
	assert.False(t, registry.EffortSupportedByModel(hiddenCatalog, claude, "opus", "low"),
		"a tier the hidden current model doesn't offer is still rejected")
	assert.True(t, registry.EffortSupportedByModel(hiddenCatalog, claude, "sonnet", "high"),
		"a LISTED model still validates against its own per-model sub_group, not the current model's")
	assert.False(t, registry.EffortSupportedByModel(hiddenCatalog, claude, "haiku", "high"),
		"a model neither listed nor the current one stays unsupported")
}

// TestModelEffortKnown covers the gate the effort reset uses to avoid clobbering a valid effort for a
// model the catalog doesn't describe (a tier valid in a running provider's live catalog but absent
// from a stopped agent's static seed): known for a selectable model and for the hidden current model
// with a top-level effort group, UNKNOWN for a model absent from the catalog entirely.
func TestModelEffortKnown(t *testing.T) {
	t.Parallel()

	registry := agenttest.MustNewRegistry(Registration())

	claude := leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE
	models := []*agent.ModelInfo{
		{Id: "opus", DisplayName: "Opus", DefaultEffort: "high", SupportedEfforts: []*agent.EffortInfo{{Id: "auto"}, {Id: "high"}, {Id: "xhigh"}}},
		{Id: "sonnet", DisplayName: "Sonnet", DefaultEffort: "high", SupportedEfforts: []*agent.EffortInfo{{Id: "auto"}, {Id: "high"}}},
		{Id: "haiku", DisplayName: "Haiku"}, // selectable, but no effort axis
	}
	catalog := []*leapmuxv1.AvailableOptionGroup{agent.ModelOptionGroup(models, "opus", agent.EffortSubGroups)}

	assert.True(t, registry.ModelEffortKnown(catalog, claude, "opus"), "a selectable model is known")
	assert.True(t, registry.ModelEffortKnown(catalog, claude, "sonnet"), "a non-current selectable model is known")
	assert.True(t, registry.ModelEffortKnown(catalog, claude, "haiku"), "a selectable but effort-less model is still known")
	assert.True(t, registry.ModelEffortKnown(catalog, claude, "claude-opus-4-8"), "a re-spelled alias normalizes to a selectable id")
	assert.False(t, registry.ModelEffortKnown(catalog, claude, "future-model"), "a model absent from the catalog is unknown")
	assert.False(t, registry.ModelEffortKnown(nil, claude, "opus"), "no catalog -> unknown")

	// A hidden current model (not selectable, but the model group's current value) is known via the
	// top-level effort group built for it. sonnet is selectable so the model group exists at all.
	hiddenModels := []*agent.ModelInfo{
		{Id: "opus", DisplayName: "Opus", Hidden: true, DefaultEffort: "high", SupportedEfforts: []*agent.EffortInfo{{Id: "auto"}, {Id: "high"}, {Id: "xhigh"}}},
		{Id: "sonnet", DisplayName: "Sonnet", DefaultEffort: "high", SupportedEfforts: []*agent.EffortInfo{{Id: "auto"}, {Id: "high"}}},
	}
	hiddenCatalog := providerkit.ModelAndEffortGroups(hiddenModels, "opus", "auto", agent.EffortGroupLabel, nil)
	require.Nil(t, agenttest.OptionByID(optionids.GroupByID(hiddenCatalog, agent.OptionIDModel), "opus"),
		"the hidden current model is not a selectable option")
	assert.True(t, registry.ModelEffortKnown(hiddenCatalog, claude, "opus"),
		"the hidden current model is known via the top-level effort group")
	assert.False(t, registry.ModelEffortKnown(hiddenCatalog, claude, "future-model"),
		"a model that is neither selectable nor the current value is unknown even with an effort group present")

	// The account-default sentinel is SELECTABLE and carries no efforts, so the
	// selectable-option loop would report it known and every tier unsupported --
	// which makes resetEffortToAutoIfUnsupported clamp a user's effort to auto.
	// "Unresolved" is not "offers nothing": haiku above is genuinely effort-less
	// and must stay known, while the sentinel must not.
	sentinelModels := []*agent.ModelInfo{
		agent.AccountDefaultModelEntry("Use your account's default model"),
		{Id: "sonnet", DisplayName: "Sonnet", DefaultEffort: "high", SupportedEfforts: []*agent.EffortInfo{{Id: "auto"}, {Id: "high"}}},
	}
	sentinelCatalog := []*leapmuxv1.AvailableOptionGroup{agent.ModelOptionGroup(sentinelModels, agent.DefaultModelSentinel, agent.EffortSubGroups)}
	require.NotNil(t, agenttest.OptionByID(optionids.GroupByID(sentinelCatalog, agent.OptionIDModel), agent.DefaultModelSentinel),
		"the sentinel IS a selectable option, which is what makes the loop report it known")
	assert.False(t, registry.ModelEffortKnown(sentinelCatalog, claude, agent.DefaultModelSentinel),
		"the account default has not resolved to a concrete model, so its effort set is unknown")
	assert.False(t, registry.ModelEffortKnown(sentinelCatalog, claude, ""),
		"an unset model means the account default too")
	assert.True(t, registry.ModelEffortKnown(sentinelCatalog, claude, "sonnet"),
		"a concrete model beside the sentinel is still known")
}
