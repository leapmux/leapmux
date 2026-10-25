package zcode

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func zcodeStateTestBinding(credential string) zcodeAccountBinding {
	var rule zcodeBuiltinProviderRule
	rule.ProviderID = "account:zai-start-plan"
	rule.Config.Access.Type = zcodeAccountAccessType
	rule.Config.Access.AccountType = zcodeFamilyZAI
	rule.Config.Access.Mode = zcodeStartPlanMode
	return zcodeAccountBinding{
		rule: rule, accountID: rule.ProviderID, legacyID: "builtin:zai-start-plan",
		session: zcodeAccountSession{zcodeJWT: credential}, current: true,
	}
}

func TestZCodeAccountState_RequiresAcceptedEntitlementForChangedCredentials(t *testing.T) {
	t.Parallel()
	now := time.Unix(0, 0)
	old := zcodeStateTestBinding("old")
	state := newZCodeAccountState(zcodeAccountSnapshotInputs{bindings: []zcodeAccountBinding{old}}, map[string]zcodeAccountEntitlement{old.legacyID: zcodeAvailable("old-model")}, now)
	state.accepted(0)
	fresh := zcodeStateTestBinding("new")
	state.reconcileBindings([]zcodeAccountBinding{fresh}, now)
	_, invalidatedGeneration := state.payload("r")
	state.accepted(invalidatedGeneration)
	accepted, _ := state.credentialAccepted(fresh.legacyID, "new", false)
	assert.False(t, accepted, "publishing an unknown entitlement does not authorize a model request")
	state.completeChecks([]zcodeAccountBinding{fresh}, map[string]zcodeAccountEntitlement{fresh.legacyID: zcodeAvailable("new-model")}, now)
	accepted, _ = state.credentialAccepted(fresh.legacyID, "new", false)
	assert.False(t, accepted, "a learned entitlement must reach the app-server before authorization")
	payload, generation := state.payload("r")
	assert.Equal(t, []string{"new-model"}, payload.Providers[fresh.accountID].BuiltinModelIDs)
	state.accepted(generation)
	accepted, _ = state.credentialAccepted(fresh.legacyID, "new", false)
	assert.True(t, accepted)
}

func TestZCodeAccountState_RejectsResultsForReplacedCredentials(t *testing.T) {
	t.Parallel()
	now := time.Unix(0, 0)
	old := zcodeStateTestBinding("old")
	state := newZCodeAccountState(zcodeAccountSnapshotInputs{bindings: []zcodeAccountBinding{old}}, nil, now)
	fresh := zcodeStateTestBinding("new")
	state.reconcileBindings([]zcodeAccountBinding{fresh}, now)
	state.completeChecks([]zcodeAccountBinding{old}, map[string]zcodeAccountEntitlement{old.legacyID: zcodeAvailable("obsolete")}, now)
	payload, _ := state.payload("r")
	assert.False(t, payload.Providers[fresh.accountID].Access.Entitled)
	assert.Equal(t, zcodeAvailabilityUnknown, payload.States[fresh.accountID].Availability)
	require.Len(t, state.dueStartPlans(now), 1)
	state.completeChecks([]zcodeAccountBinding{fresh}, map[string]zcodeAccountEntitlement{fresh.legacyID: zcodeAvailable("current")}, now)
	payload, _ = state.payload("r")
	assert.Equal(t, []string{"current"}, payload.Providers[fresh.accountID].BuiltinModelIDs)
}

func TestZCodeAccountState_KeepsFiniteRetriesUntilTheCredentialChanges(t *testing.T) {
	t.Parallel()
	now := time.Unix(0, 0)
	binding := zcodeStateTestBinding("old")
	state := newZCodeAccountState(zcodeAccountSnapshotInputs{bindings: []zcodeAccountBinding{binding}}, nil, now)
	for _, delay := range zcodeStartPlanRecheckDelays {
		assert.Empty(t, state.dueStartPlans(now))
		now = now.Add(delay)
		due := state.dueStartPlans(now)
		require.Len(t, due, 1)
		state.completeChecks(due, nil, now)
	}
	assert.Empty(t, state.dueStartPlans(now.Add(365*24*time.Hour)))
	fresh := zcodeStateTestBinding("new")
	state.reconcileBindings([]zcodeAccountBinding{fresh}, now)
	require.Len(t, state.dueStartPlans(now), 1, "a new sign-in starts a new check budget")
}

func TestZCodeAccountState_SchedulesPendingTimesWithoutOverflow(t *testing.T) {
	t.Parallel()
	now := time.Unix(0, 0)
	binding := zcodeStateTestBinding("credential")
	for _, test := range []struct {
		name      string
		effective *float64
		wantDelay time.Duration
	}{
		{name: "an absent effective time", wantDelay: zcodeAccountPollInterval},
		{name: "zero", effective: new(float64), wantDelay: zcodeAccountPollInterval},
		{name: "a negative time", effective: func() *float64 { value := -1.0; return &value }(), wantDelay: zcodeAccountPollInterval},
		{name: "a far future time", effective: func() *float64 { value := 1e300; return &value }(), wantDelay: zcodeAccountPollInterval},
		{name: "a fractional time", effective: func() *float64 { value := 0.5; return &value }(), wantDelay: 500 * time.Millisecond},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			state := newZCodeAccountState(zcodeAccountSnapshotInputs{bindings: []zcodeAccountBinding{binding}}, map[string]zcodeAccountEntitlement{
				binding.legacyID: {Availability: zcodeAvailabilityPending, EffectiveAt: test.effective, Models: []string{}},
			}, now)
			assert.Equal(t, test.wantDelay, state.nextDelay(now))
			assert.Empty(t, state.dueStartPlans(now))
		})
	}
}

func TestZCodeAccountState_DistinguishesAbsentAndEmptyModelLists(t *testing.T) {
	t.Parallel()
	binding := zcodeStateTestBinding("credential")
	state := newZCodeAccountState(zcodeAccountSnapshotInputs{bindings: []zcodeAccountBinding{binding}}, map[string]zcodeAccountEntitlement{
		binding.legacyID: {Availability: zcodeAvailabilityAvailable},
	}, time.Unix(0, 0))
	before, first := state.payload("r")
	assert.Nil(t, before.Providers[binding.accountID].BuiltinModelIDs)
	state.learnForTest(map[string]zcodeAccountEntitlement{binding.legacyID: zcodeAvailable()})
	after, second := state.payload("r")
	assert.NotNil(t, after.Providers[binding.accountID].BuiltinModelIDs)
	assert.Empty(t, after.Providers[binding.accountID].BuiltinModelIDs)
	assert.Greater(t, second, first)
	state.learnForTest(map[string]zcodeAccountEntitlement{binding.legacyID: zcodeAvailable()})
	_, same := state.payload("r")
	assert.Equal(t, second, same)
}

func TestZCodeAccountState_DoesNotRegressAcknowledgedGeneration(t *testing.T) {
	t.Parallel()
	binding := zcodeStateTestBinding("credential")
	state := newZCodeAccountState(zcodeAccountSnapshotInputs{bindings: []zcodeAccountBinding{binding}}, nil, time.Unix(0, 0))
	state.learnForTest(map[string]zcodeAccountEntitlement{binding.legacyID: zcodeAvailable("one")})
	_, first := state.payload("r")
	state.learnForTest(map[string]zcodeAccountEntitlement{binding.legacyID: zcodeAvailable("two")})
	_, second := state.payload("r")
	state.accepted(second)
	state.accepted(first)
	assert.False(t, state.unpushed())
}

func TestZCodeAccountState_AdmitsModelsOnlyForUsableEntitlements(t *testing.T) {
	t.Parallel()
	binding := zcodeStateTestBinding("credential")
	for _, test := range []struct {
		name        string
		entitlement zcodeAccountEntitlement
		model       bool
	}{
		{name: "nonempty models", entitlement: zcodeAvailable("model"), model: true},
		{name: "absent override", entitlement: zcodeAccountEntitlement{Availability: zcodeAvailabilityAvailable}, model: true},
		{name: "explicit empty models", entitlement: zcodeAvailable(), model: false},
		{name: "unavailable", entitlement: zcodeNotEntitled},
		{name: "pending", entitlement: zcodePending(100)},
		{name: "unknown", entitlement: zcodeUnknownEntitlement},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			state := newZCodeAccountState(zcodeAccountSnapshotInputs{bindings: []zcodeAccountBinding{binding}}, map[string]zcodeAccountEntitlement{binding.legacyID: test.entitlement}, time.Unix(0, 0))
			model, _ := state.credentialAccepted(binding.legacyID, "credential", false)
			usage, _ := state.credentialAccepted(binding.legacyID, "credential", true)
			assert.Equal(t, test.model, model)
			assert.True(t, usage)
		})
	}
}
