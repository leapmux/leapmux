package captcha

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"

	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/hub/settings"
	"github.com/leapmux/leapmux/internal/hub/store/sqlite"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// crossRuleManager builds a settings manager over the captcha keys with
// the SelectedConfigured cross rule attached — the wiring the hub, the
// CLI, and settingsregistry all use.
func crossRuleManager(t *testing.T) *settings.Manager {
	t.Helper()
	st, err := sqlite.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	key, err := keystore.GenerateKey()
	require.NoError(t, err)
	ks, err := keystore.New(map[uint32][32]byte{1: key})
	require.NoError(t, err)
	m := settings.NewManager(st, ks, SettingsDescriptors(),
		settings.WithCrossValidation(SelectedConfigured))
	require.NoError(t, m.Load(context.Background()))
	return m
}

// TestSelectedConfiguredRejectsUnconfiguredSelection pins the
// write-path guard: selecting an external provider whose row has no
// stored keys must be refused wherever it is introduced — the state
// otherwise sits stored while Effective silently runs ALTCHA, telling
// the operator two different things.
func TestSelectedConfiguredRejectsUnconfiguredSelection(t *testing.T) {
	m := crossRuleManager(t)
	ctx := context.Background()

	err := m.Update(ctx, CaptchaSelectedKey, json.RawMessage(`"recaptcha_v3"`))
	require.ErrorContains(t, err, "requires its site key and secret")

	// Configuring the row first makes the same selection succeed.
	require.NoError(t, RecaptchaV3Key.Set(ctx, m, RecaptchaV3Row{
		SiteKey: "site-key", MinScore: 0.5, SecretKey: "api-secret",
	}))
	require.NoError(t, m.Update(ctx, CaptchaSelectedKey, json.RawMessage(`"recaptcha_v3"`)))
	assert.Equal(t, "recaptcha_v3", CaptchaSelectedKey.Of(m.Snapshot(ctx)))

	// Clearing the keys of the SELECTED provider is refused the same way.
	err = m.Update(ctx, RecaptchaV3Key, json.RawMessage(`{"site_key":""}`))
	require.ErrorContains(t, err, "requires its site key and secret")

	// Resetting the selected provider's row directly is refused too; the
	// selection must move first (the CLI wrapper orders them that way).
	err = m.Reset(ctx, RecaptchaV3Key)
	require.ErrorContains(t, err, "requires its site key and secret")
	require.NoError(t, m.Reset(ctx, CaptchaSelectedKey))
	require.NoError(t, m.Reset(ctx, RecaptchaV3Key))

	// Selecting altcha needs nothing: its row self-provisions.
	require.NoError(t, m.Update(ctx, CaptchaSelectedKey, json.RawMessage(`"altcha"`)))
}

// TestRecaptchaRowRejectsZeroMinScore pins the validator agreement: the
// provider's own validation rejects a zero threshold (it would accept
// every score), so the write path must reject it too — with an error,
// not a silent round-trip back to the 0.5 default.
func TestRecaptchaRowRejectsZeroMinScore(t *testing.T) {
	m := crossRuleManager(t)
	ctx := context.Background()

	err := m.Update(ctx, RecaptchaV3Key, json.RawMessage(`{"min_score":0}`))
	require.ErrorContains(t, err, "greater than 0")

	require.NoError(t, m.Update(ctx, RecaptchaV3Key, json.RawMessage(`{"min_score":0.9}`)))
	assert.Equal(t, 0.9, RecaptchaV3Key.Of(m.Snapshot(ctx)).MinScore)
}

// TestAltchaAdvertisedBoundsAreTheFamilyUnion proves the Min/Max the
// ALTCHA parameter controls advertise are exactly the union of what
// Validate accepts across the algorithm families.
//
// One Field declares one static range, but the three tunables carry a
// different unit per family, so the advertised range has to be the union.
// Two ways that goes wrong, and this test catches both: a bound NO family
// accepts makes the control offer a value the hub always refuses, and a
// bound narrower than some family's range hides a setting an operator can
// legally reach. Widen or narrow a family in Validate and this fails.
func TestAltchaAdvertisedBoundsAreTheFamilyUnion(t *testing.T) {
	// accepts reports whether any algorithm takes `value` in the named
	// slot, holding the other two at that family's own defaults.
	accepts := func(slot string, value int64) bool {
		for _, algorithm := range SupportedAltchaAlgorithms() {
			s, err := DefaultAltchaSettingsFor(algorithm)
			require.NoError(t, err)
			switch slot {
			case "cost":
				s.Cost = value
			case "memory_cost":
				s.MemoryCost = value
			case "parallelism":
				s.Parallelism = value
			}
			if s.Validate() == nil {
				return true
			}
		}
		return false
	}

	for _, tc := range []struct {
		slot     string
		min, max int64
	}{
		{"cost", MinAltchaCost, MaxAltchaCost},
		{"memory_cost", MinAltchaMemoryCost, MaxAltchaMemoryCost},
		{"parallelism", MinAltchaParallelism, MaxAltchaParallelism},
	} {
		assert.Truef(t, accepts(tc.slot, tc.min),
			"%s advertises a minimum of %d but no algorithm accepts it", tc.slot, tc.min)
		assert.Truef(t, accepts(tc.slot, tc.max),
			"%s advertises a maximum of %d but no algorithm accepts it", tc.slot, tc.max)
		assert.Falsef(t, accepts(tc.slot, tc.min-1),
			"%s advertises a minimum of %d but some algorithm accepts %d", tc.slot, tc.min, tc.min-1)
		assert.Falsef(t, accepts(tc.slot, tc.max+1),
			"%s advertises a maximum of %d but some algorithm accepts %d", tc.slot, tc.max, tc.max+1)
	}

	// And the declared field bounds are those constants, so the control
	// and the union cannot drift apart.
	for _, f := range AltchaKey.UI().Fields {
		switch f.Name {
		case "cost":
			require.NotNil(t, f.Min)
			require.NotNil(t, f.Max)
			assert.Equal(t, int64(MinAltchaCost), *f.Min)
			assert.Equal(t, int64(MaxAltchaCost), *f.Max)
		case "memory_cost":
			require.NotNil(t, f.Min)
			require.NotNil(t, f.Max)
			assert.Equal(t, int64(MinAltchaMemoryCost), *f.Min)
			assert.Equal(t, int64(MaxAltchaMemoryCost), *f.Max)
		case "parallelism":
			require.NotNil(t, f.Min)
			require.NotNil(t, f.Max)
			assert.Equal(t, int64(MinAltchaParallelism), *f.Min)
			assert.Equal(t, int64(MaxAltchaParallelism), *f.Max)
		}
	}
}

// TestExternalProviderCredentialsAreAlwaysVisible pins that an external
// provider can be configured from a client that renders only the fields a
// descriptor declares visible.
//
// The credential fields used to carry DependsOn{Key:"captcha.selected"},
// which deadlocked the preferences dialog: SelectedConfigured refuses the
// selection until the keys are stored, and the keys had no field on
// screen until the selection went through. The CLI escaped only because
// it writes the row before the selection. A hide rule keyed on the
// selection can never come back here.
func TestExternalProviderCredentialsAreAlwaysVisible(t *testing.T) {
	for _, key := range []settings.Descriptor{RecaptchaV3Key, TurnstileKey} {
		for _, f := range key.UI().Fields {
			if f.Name != "site_key" && f.Name != "secret_key" {
				continue
			}
			assert.Nilf(t, f.DependsOn,
				"%s.%s must stay visible: SelectedConfigured refuses the selection until it is set, so hiding it makes %s unconfigurable",
				key.Name(), f.Name, key.Name())
		}
	}
}

// TestAlgorithmSwitchAloneIsAccepted pins the reconciler that makes the
// preferences dialog able to change the ALTCHA algorithm at all.
//
// The dialog writes one field per row, so an algorithm switch arrives as
// {"algorithm": X} with nothing else. Merged onto the stored row that
// carries the OLD family's parameters, Validate refused every cross-family
// switch — cost 10000 is not a power of two for SCRYPT, and it is far past
// ARGON2ID's ceiling of 64.
func TestAlgorithmSwitchAloneIsAccepted(t *testing.T) {
	ctx := context.Background()
	for _, algorithm := range SupportedAltchaAlgorithms() {
		m := crossRuleManager(t)
		require.NoErrorf(t, m.Update(ctx, AltchaKey, json.RawMessage(`{"algorithm":`+strconv.Quote(algorithm)+`}`)),
			"switching to %s with no other field must succeed", algorithm)

		got := AltchaKey.Of(m.Snapshot(ctx))
		want, err := DefaultAltchaSettingsFor(algorithm)
		require.NoError(t, err)
		assert.Equal(t, algorithm, got.Algorithm)
		assert.Equalf(t, want.Cost, got.Cost, "%s cost must reset to its family default", algorithm)
		assert.Equalf(t, want.MemoryCost, got.MemoryCost, "%s memory_cost must reset to its family default", algorithm)
		assert.Equalf(t, want.Parallelism, got.Parallelism, "%s parallelism must reset to its family default", algorithm)
	}
}

// TestAlgorithmSwitchKeepsAnExplicitParameter pins the other half of the
// reconciler: a parameter the SAME document names is the operator's
// choice, not a leftover, so the family default must not overwrite it.
func TestAlgorithmSwitchKeepsAnExplicitParameter(t *testing.T) {
	ctx := context.Background()
	m := crossRuleManager(t)

	require.NoError(t, m.Update(ctx, AltchaKey,
		json.RawMessage(`{"algorithm":"ARGON2ID","cost":3}`)))

	got := AltchaKey.Of(m.Snapshot(ctx))
	assert.Equal(t, "ARGON2ID", got.Algorithm)
	assert.Equal(t, int64(3), got.Cost, "an explicitly named cost wins over the family default")
	assert.Equal(t, int64(65536), got.MemoryCost, "an unnamed parameter still resets")
}

// TestTuningWithoutAnAlgorithmSwitchLeavesTheFamilyAlone pins that the
// reconciler only fires on a CHANGE: re-writing the same algorithm, or
// tuning one parameter on its own, must not silently reset the rest.
func TestTuningWithoutAnAlgorithmSwitchLeavesTheFamilyAlone(t *testing.T) {
	ctx := context.Background()
	m := crossRuleManager(t)

	require.NoError(t, m.Update(ctx, AltchaKey, json.RawMessage(`{"algorithm":"SCRYPT"}`)))
	require.NoError(t, m.Update(ctx, AltchaKey, json.RawMessage(`{"memory_cost":4}`)))
	require.NoError(t, m.Update(ctx, AltchaKey, json.RawMessage(`{"algorithm":"SCRYPT"}`)))

	got := AltchaKey.Of(m.Snapshot(ctx))
	assert.Equal(t, int64(4), got.MemoryCost,
		"re-writing the same algorithm must not reset a tuned parameter")
	assert.Equal(t, int64(16384), got.Cost)
}

// TestFirstTimeExternalProviderSetupOrder pins the write ORDER the admin
// CLI declares load-bearing, at the layer that enforces it.
//
// SelectedConfigured runs on EVERY key write, so a first-time setup that
// selects the provider before its row is complete is refused — and the
// reverse order is the only one that works. The CLI states this in a
// comment; this proves it, so reordering the writes fails here rather
// than only in an operator's terminal.
func TestFirstTimeExternalProviderSetupOrder(t *testing.T) {
	ctx := context.Background()

	t.Run("selection last succeeds", func(t *testing.T) {
		m := crossRuleManager(t)
		require.NoError(t, m.Update(ctx, TurnstileKey, json.RawMessage(`{"site_key":"1x000AA"}`)))
		require.NoError(t, m.UpdateSecret(ctx, TurnstileKey, json.RawMessage(`{"secret_key":"1x000SS"}`)))
		require.NoError(t, m.Update(ctx, CaptchaSelectedKey, json.RawMessage(`"turnstile"`)))
		assert.Equal(t, "turnstile", CaptchaSelectedKey.Of(m.Snapshot(ctx)))
	})

	t.Run("selection first is refused", func(t *testing.T) {
		m := crossRuleManager(t)
		err := m.Update(ctx, CaptchaSelectedKey, json.RawMessage(`"turnstile"`))
		require.ErrorContains(t, err, "requires its site key and secret",
			"first-time setup must write the row before the selection")
	})

	t.Run("half a key pair is not enough", func(t *testing.T) {
		m := crossRuleManager(t)
		require.NoError(t, m.Update(ctx, TurnstileKey, json.RawMessage(`{"site_key":"1x000AA"}`)))
		err := m.Update(ctx, CaptchaSelectedKey, json.RawMessage(`"turnstile"`))
		require.ErrorContains(t, err, "requires its site key and secret",
			"a site key with no secret cannot be selected")
	})
}

// TestSwitchingBackKeepsTheStoredSettings pins that a provider's row
// survives a switch away and back. Each provider owns its own key, so
// selecting another one must not disturb the first — an operator who
// switches to ALTCHA to debug must get their Turnstile keys back.
func TestSwitchingBackKeepsTheStoredSettings(t *testing.T) {
	ctx := context.Background()
	m := crossRuleManager(t)

	require.NoError(t, m.Update(ctx, TurnstileKey, json.RawMessage(`{"site_key":"1x000AA"}`)))
	require.NoError(t, m.UpdateSecret(ctx, TurnstileKey, json.RawMessage(`{"secret_key":"1x000SS"}`)))
	require.NoError(t, m.Update(ctx, CaptchaSelectedKey, json.RawMessage(`"turnstile"`)))

	require.NoError(t, m.Update(ctx, CaptchaSelectedKey, json.RawMessage(`"altcha"`)))
	row := TurnstileKey.Of(m.Snapshot(ctx))
	assert.Equal(t, "1x000AA", row.SiteKey, "the row survives a switch away")
	assert.Equal(t, "1x000SS", row.SecretKey)

	// And switching back needs no re-entry, because the row is complete.
	require.NoError(t, m.Update(ctx, CaptchaSelectedKey, json.RawMessage(`"turnstile"`)))
	assert.Equal(t, "turnstile", CaptchaSelectedKey.Of(m.Snapshot(ctx)))
}

// TestAtomicSwitchNeedsNoWriteOrder pins what the atomic multi-key write
// buys: first-time configuration of an external provider stops depending
// on the order the keys are written in.
//
// A sequence of single-key writes had to put the row before the selection,
// because each write was validated against the state the previous one
// left. One transaction validates the cross-key rules ONCE over the whole
// result, so the same three keys succeed in any order.
func TestAtomicSwitchNeedsNoWriteOrder(t *testing.T) {
	ctx := context.Background()

	selection := settings.KeyWrite{
		Desc:   CaptchaSelectedKey,
		Public: json.RawMessage(`"turnstile"`),
	}
	row := settings.KeyWrite{
		Desc:   TurnstileKey,
		Public: json.RawMessage(`{"site_key":"1x000AA"}`),
		Secret: json.RawMessage(`{"secret_key":"1x000SS"}`),
	}

	// Selection FIRST — the order a sequence of single-key writes could
	// never use.
	m := crossRuleManager(t)
	require.NoError(t, m.UpdateMany(ctx, []settings.KeyWrite{selection, row}),
		"one transaction validates the whole result, so the order cannot matter")
	snap := m.Snapshot(ctx)
	assert.Equal(t, "turnstile", CaptchaSelectedKey.Of(snap))
	assert.Equal(t, "1x000AA", TurnstileKey.Of(snap).SiteKey)
	assert.Equal(t, "1x000SS", TurnstileKey.Of(snap).SecretKey)

	// Row first still works.
	m2 := crossRuleManager(t)
	require.NoError(t, m2.UpdateMany(ctx, []settings.KeyWrite{row, selection}))
	assert.Equal(t, "turnstile", CaptchaSelectedKey.Of(m2.Snapshot(ctx)))
}

// TestAtomicWriteRollsBackEverythingOnRefusal is the property the whole
// change exists for: a refused multi-key write stores NOTHING.
//
// The sequence it replaces could not offer this. A selection refused after
// the provider row was already durable left the hub holding a site key and
// a secret under a provider the command reported it never applied — and
// re-keying the SELECTED provider could publish a new site key beside the
// old secret, failing every verification on a live hub.
func TestAtomicWriteRollsBackEverythingOnRefusal(t *testing.T) {
	ctx := context.Background()
	m := crossRuleManager(t)

	// A site key with NO secret cannot be selected, so the whole write is
	// refused — including the row half that would otherwise be durable.
	err := m.UpdateMany(ctx, []settings.KeyWrite{
		{Desc: TurnstileKey, Public: json.RawMessage(`{"site_key":"1x000AA"}`)},
		{Desc: CaptchaSelectedKey, Public: json.RawMessage(`"turnstile"`)},
	})
	require.ErrorContains(t, err, "requires its site key and secret")

	snap := m.Snapshot(ctx)
	assert.Equal(t, "altcha", CaptchaSelectedKey.Of(snap), "the selection is untouched")
	assert.Empty(t, TurnstileKey.Of(snap).SiteKey,
		"the provider row must NOT be durable after a refused write")
}

// TestAtomicRekeyOfTheSelectedProviderIsAllOrNothing covers the worst
// window the sequence left open: tuning the key pair of the provider that
// is ALREADY selected.
func TestAtomicRekeyOfTheSelectedProviderIsAllOrNothing(t *testing.T) {
	ctx := context.Background()
	m := crossRuleManager(t)

	require.NoError(t, m.UpdateMany(ctx, []settings.KeyWrite{
		{
			Desc:   TurnstileKey,
			Public: json.RawMessage(`{"site_key":"old-site"}`),
			Secret: json.RawMessage(`{"secret_key":"old-secret"}`),
		},
		{Desc: CaptchaSelectedKey, Public: json.RawMessage(`"turnstile"`)},
	}))

	// A re-key that empties the site key is refused, and the OLD pair
	// survives intact — the live hub never sees a mismatched pair.
	err := m.UpdateMany(ctx, []settings.KeyWrite{{
		Desc:   TurnstileKey,
		Public: json.RawMessage(`{"site_key":""}`),
		Secret: json.RawMessage(`{"secret_key":"new-secret"}`),
	}})
	require.ErrorContains(t, err, "requires its site key and secret")

	row := TurnstileKey.Of(m.Snapshot(ctx))
	assert.Equal(t, "old-site", row.SiteKey)
	assert.Equal(t, "old-secret", row.SecretKey, "the secret half rolls back with the public one")

	// A complete re-key applies both halves together.
	require.NoError(t, m.UpdateMany(ctx, []settings.KeyWrite{{
		Desc:   TurnstileKey,
		Public: json.RawMessage(`{"site_key":"new-site"}`),
		Secret: json.RawMessage(`{"secret_key":"new-secret"}`),
	}}))
	row = TurnstileKey.Of(m.Snapshot(ctx))
	assert.Equal(t, "new-site", row.SiteKey)
	assert.Equal(t, "new-secret", row.SecretKey)
}

// TestResetManyLeavesNoHalfResetStateWhenACrossRuleRefuses pins the
// atomicity the batched reset exists for. Clearing a provider's row while
// the selection still points at it is exactly the combination
// SelectedConfigured refuses, and a per-key reset loop reached that
// refusal AFTER it had already destroyed the earlier keys.
func TestResetManyLeavesNoHalfResetStateWhenACrossRuleRefuses(t *testing.T) {
	m := crossRuleManager(t)
	ctx := context.Background()

	require.NoError(t, RecaptchaV3Key.Set(ctx, m, RecaptchaV3Row{
		SiteKey: "site-key", MinScore: 0.5, SecretKey: "api-secret",
	}))
	require.NoError(t, TurnstileKey.Set(ctx, m, TurnstileRow{SiteKey: "ts-site", SecretKey: "ts-secret"}))
	require.NoError(t, m.Update(ctx, CaptchaSelectedKey, json.RawMessage(`"recaptcha_v3"`)))

	// Clearing the two provider rows while the selection stands must be
	// refused, and must leave BOTH rows intact.
	err := m.ResetMany(ctx, []settings.Descriptor{TurnstileKey, RecaptchaV3Key})
	require.ErrorContains(t, err, "requires its site key and secret")

	snap := m.Snapshot(ctx)
	assert.True(t, snap.Customized(TurnstileKey), "the first key of a refused batch must survive")
	assert.True(t, snap.Customized(RecaptchaV3Key), "the refused key must survive")
	assert.Equal(t, "site-key", RecaptchaV3Key.Of(snap).SiteKey)
	assert.Equal(t, "ts-site", TurnstileKey.Of(snap).SiteKey)

	// The selection joins the same batch, and then the whole set clears in
	// one transaction -- which is what the CLI's reset verb needs.
	require.NoError(t, m.ResetMany(ctx, []settings.Descriptor{
		CaptchaSelectedKey, TurnstileKey, RecaptchaV3Key,
	}))
	snap = m.Snapshot(ctx)
	assert.False(t, snap.Customized(CaptchaSelectedKey))
	assert.False(t, snap.Customized(TurnstileKey))
	assert.False(t, snap.Customized(RecaptchaV3Key))
}

// TestResetManyRefusesADuplicateOrUnregisteredKey pins the argument
// checks, which mirror UpdateMany's.
func TestResetManyRefusesADuplicateOrUnregisteredKey(t *testing.T) {
	m := crossRuleManager(t)
	ctx := context.Background()

	require.ErrorContains(t, m.ResetMany(ctx, nil), "no settings resets given")
	require.ErrorContains(t, m.ResetMany(ctx, []settings.Descriptor{TurnstileKey, TurnstileKey}),
		"appears twice in one reset")

	stranger := settings.NewKey[bool]("test.unregistered").
		WithUI(settings.UIMeta{Category: "general", Title: "S", Fields: []settings.Field{{Kind: settings.FieldBool}}})
	require.ErrorContains(t, m.ResetMany(ctx, []settings.Descriptor{stranger}), "is not registered")
}

// TestRecaptchaMinScoreFloorIsStorable pins the declared floor against the
// validator: the control offers it, so a write of exactly that value must
// succeed. The declaration advertised 0.00, which every write refused.
func TestRecaptchaMinScoreFloorIsStorable(t *testing.T) {
	var floor float64
	for _, f := range RecaptchaV3Key.UI().Fields {
		if f.Name == "min_score" {
			require.NotNil(t, f.MinF)
			floor = *f.MinF
		}
	}
	require.NotZero(t, floor, "min_score must declare a floor")
	require.NoError(t, validateRecaptchaRow(RecaptchaV3Row{
		SiteKey: "site-key", SecretKey: "api-secret", MinScore: floor,
	}), "the declared floor must be storable")
	require.Error(t, validateRecaptchaRow(RecaptchaV3Row{
		SiteKey: "site-key", SecretKey: "api-secret", MinScore: 0,
	}), "zero disables the check and stays refused")
}
