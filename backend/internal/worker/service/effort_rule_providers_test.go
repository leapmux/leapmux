package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionids"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// effortRuleModels is what one catalog states about the effort tiers of its models, in the terms
// that the rule needs: a pair of models that share a tier, a pair where the second model lacks a
// tier of the first, and a model with no effort axis. An empty field means that the catalog holds
// no such model.
type effortRuleModels struct {
	catalog []*leapmuxv1.AvailableOptionGroup

	// from and to offer the tier shared.
	from, to, shared string
	// narrowFrom offers the tier narrowTier, and narrowTo does not.
	narrowFrom, narrowTo, narrowTier string
	// plain offers no effort axis.
	plain string
}

// modelTiers lists, for each selectable model of the catalog, the effort tiers that its sub_group
// offers, except auto. A model with no effort sub_group maps to a nil slice. The ids keep catalog order.
func modelTiers(catalog []*leapmuxv1.AvailableOptionGroup) (ids []string, tiers map[string][]string) {
	tiers = map[string][]string{}
	for _, option := range optionids.GroupByID(catalog, agent.OptionIDModel).GetOptions() {
		// The account default is a placeholder that carries no tiers on purpose. It is no model
		// without an effort axis, and the rule leaves it to the running session.
		if agent.UsesAccountDefaultModel(option.GetId()) {
			continue
		}
		ids = append(ids, option.GetId())
		tiers[option.GetId()] = nil
		for _, tier := range optionids.GroupByID(option.GetSubGroups(), agent.OptionIDEffort).GetOptions() {
			if tier.GetId() != agent.EffortAuto {
				tiers[option.GetId()] = append(tiers[option.GetId()], tier.GetId())
			}
		}
	}
	return ids, tiers
}

func offers(tiers []string, tier string) bool {
	for _, candidate := range tiers {
		if candidate == tier {
			return true
		}
	}
	return false
}

// effortRuleModelsOf derives the models of the rule test from a catalog.
func effortRuleModelsOf(catalog []*leapmuxv1.AvailableOptionGroup) effortRuleModels {
	found := effortRuleModels{catalog: catalog}
	ids, tiers := modelTiers(catalog)
	for _, from := range ids {
		for _, to := range ids {
			if from == to {
				continue
			}
			for _, tier := range tiers[from] {
				if found.shared == "" && offers(tiers[to], tier) {
					found.from, found.to, found.shared = from, to, tier
				}
				if found.narrowTier == "" && !offers(tiers[to], tier) {
					found.narrowFrom, found.narrowTo, found.narrowTier = from, to, tier
				}
			}
		}
	}
	for _, option := range optionids.GroupByID(catalog, agent.OptionIDModel).GetOptions() {
		if found.plain == "" && !agent.UsesAccountDefaultModel(option.GetId()) &&
			optionids.GroupByID(option.GetSubGroups(), agent.OptionIDEffort) == nil {
			found.plain = option.GetId()
		}
	}
	return found
}

// TestResetEffortToAutoIfUnsupported_EveryProviderThatManagesEffort pins the model-switch rule for
// every provider that Registry.ManagesEffort reports, so that a provider added later joins it
// without an edit. The client sends the model alone, so the effort is inherited from the stored row.
//
// Each provider runs against two catalogs:
//   - The fixture catalog has the shape that every such provider builds in its live OptionGroups:
//     one effort sub_group for each model option. Every provider runs it.
//   - The static catalog of the registry, for each provider whose seed lists two models that
//     share a tier. This is the catalog that a stopped agent with no persisted catalog sees.
//
// A rule that resets the effort on every switch fails the first case for every provider. A provider
// whose model ids the rule cannot compare fails the same case.
func TestResetEffortToAutoIfUnsupported_EveryProviderThatManagesEffort(t *testing.T) {
	t.Parallel()

	manager := agent.NewManager(testRegistry, nil)
	staticCatalogs := 0
	for _, provider := range effortManagingProviders(t) {
		sources := map[string]effortRuleModels{
			"the fixture catalog": effortRuleModelsOf(effortTierFixtureCatalog(fixtureModelA, "low")),
		}
		if static := effortRuleModelsOf(manager.OptionGroups("not-running", provider, "")); static.shared != "" {
			sources["the static catalog"] = static
			staticCatalogs++
		}
		for name, models := range sources {
			t.Run(provider.String()+"/"+name, func(t *testing.T) {
				t.Parallel()

				require.NotEmpty(t, models.shared, "the catalog needs two models that share a tier")
				run := func(oldModel, newModel, inherited, explicit string) string {
					options := OptionMap{agent.OptionIDModel: newModel, agent.OptionIDEffort: inherited}
					resetEffortToAutoIfUnsupported(testRegistry, provider, options, models.catalog, oldModel, newModel, explicit)
					return options[agent.OptionIDEffort]
				}

				assert.Equal(t, models.shared, run(models.from, models.to, models.shared, ""),
					"a switch keeps an inherited tier that the new model offers")
				assert.Equal(t, models.shared, run(models.from, models.to, models.shared, models.shared),
					"a switch keeps an explicit tier that the new model offers")
				assert.Equal(t, agent.EffortAuto, run(models.from, "no-such-model-in-any-catalog", models.shared, ""),
					"a switch to a model that the catalog omits resets the tier")
				if models.narrowTier != "" {
					assert.Equal(t, agent.EffortAuto, run(models.narrowFrom, models.narrowTo, models.narrowTier, ""),
						"a switch resets an inherited tier that the new model lacks")
				}
				if models.plain != "" {
					assert.Equal(t, agent.EffortAuto, run(models.from, models.plain, models.shared, ""),
						"a switch to a model with no effort axis resets the tier")
				}
			})
		}
	}
	// A change to a static catalog must not remove this coverage without a failure.
	assert.Positive(t, staticCatalogs, "at least one provider states two models with a shared tier in its static catalog")
}
