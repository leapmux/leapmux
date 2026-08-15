package captcha

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	altcha "github.com/altcha-org/altcha-lib-go/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/sqlite"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
)

func newTestManager(t *testing.T, solo bool) *Manager {
	t.Helper()
	st, err := sqlite.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	key := [32]byte{}
	ks, err := keystore.New(map[uint32][32]byte{1: key})
	require.NoError(t, err)
	return NewManager(st, ks, solo)
}

// solveChallenge brute-forces a challenge the way the browser widget does.
// It uses a cheap SHA-256 cost so the roundtrip stays fast in tests.
func solveChallenge(t *testing.T, challengeJSON string) string {
	t.Helper()
	var challenge altcha.Challenge
	require.NoError(t, json.Unmarshal([]byte(challengeJSON), &challenge))
	solution, err := altcha.SolveChallenge(altcha.SolveChallengeOptions{
		Challenge: challenge,
		DeriveKey: altcha.DeriveKeySHA(),
	})
	require.NoError(t, err)
	payload, err := json.Marshal(altcha.Payload{Challenge: challenge, Solution: *solution})
	require.NoError(t, err)
	return base64.StdEncoding.EncodeToString(payload)
}

// testConfig overrides the default to the cheapest verifiable algorithm.
func applyTestConfig(t *testing.T, m *Manager, mutate func(*store.UpdateCaptchaConfigParams)) {
	t.Helper()
	ctx := context.Background()
	row, err := m.st.CaptchaConfig().Get(ctx)
	if err != nil {
		require.ErrorIs(t, err, store.ErrNotFound)
		require.NoError(t, m.provision(ctx))
		row, err = m.st.CaptchaConfig().Get(ctx)
		require.NoError(t, err)
	}
	require.NoError(t, m.st.CaptchaConfig().Delete(ctx))
	require.NoError(t, m.st.CaptchaConfig().Insert(ctx, store.InsertCaptchaConfigParams{
		Enabled:                true,
		Algorithm:              "SHA-256",
		Cost:                   1000,
		ChallengeExpirySeconds: 1200,
		Secret:                 row.Secret,
	}))
	if mutate != nil {
		p := store.UpdateCaptchaConfigParams{
			Enabled:                true,
			Algorithm:              "SHA-256",
			Cost:                   1000,
			ChallengeExpirySeconds: 1200,
		}
		mutate(&p)
		require.NoError(t, m.st.CaptchaConfig().Update(ctx, p))
	}
	// Bust the manager's cache so the new row is observed.
	m.mu.Lock()
	m.cached = nil
	m.descCached = nil
	m.mu.Unlock()
}

func TestManagerDefaultsAndProvisioning(t *testing.T) {
	m := newTestManager(t, false)

	// Describe on a fresh install reports defaults and writes nothing.
	cfg, customized, err := m.Describe(context.Background())
	require.NoError(t, err)
	assert.True(t, cfg.Enabled)
	assert.Equal(t, "PBKDF2/SHA-256", cfg.Algorithm)
	assert.EqualValues(t, 10000, cfg.Cost)
	assert.False(t, customized)
	_, err = m.st.CaptchaConfig().Get(context.Background())
	assert.ErrorIs(t, err, store.ErrNotFound)

	// The first challenge provisions the singleton row with a secret.
	_, err = m.ChallengeJSON(context.Background())
	require.NoError(t, err)
	row, err := m.st.CaptchaConfig().Get(context.Background())
	require.NoError(t, err)
	assert.True(t, row.Enabled)
	assert.NotEmpty(t, row.Secret)
}

func TestManagerChallengeVerifyRoundtrip(t *testing.T) {
	m := newTestManager(t, false)
	// Provision under the default (expensive-to-solve) config; the issued
	// challenge is thrown away.
	_, err := m.ChallengeJSON(context.Background())
	require.NoError(t, err)

	// Swap to cheap SHA for the roundtrip without disturbing the
	// provisioned secret.
	applyTestConfig(t, m, nil)
	challengeJSON, err := m.ChallengeJSON(context.Background())
	require.NoError(t, err)

	payload := solveChallenge(t, challengeJSON)
	require.NoError(t, m.Verify(context.Background(), payload))

	// Replaying the same payload must fail: salts are single-use.
	err = m.Verify(context.Background(), payload)
	assert.ErrorIs(t, err, ErrVerificationFailed)

	// A fresh challenge verifies again.
	challengeJSON, err = m.ChallengeJSON(context.Background())
	require.NoError(t, err)
	require.NoError(t, m.Verify(context.Background(), solveChallenge(t, challengeJSON)))
}

func TestManagerVerifyRejectsGarbage(t *testing.T) {
	m := newTestManager(t, false)
	applyTestConfig(t, m, nil)

	for _, payload := range []string{"", "not-base64!!!", "e30="} { // {} — valid b64, wrong shape
		err := m.Verify(context.Background(), payload)
		assert.ErrorIs(t, err, ErrVerificationFailed, "payload %q", payload)
	}
}

func TestManagerVerifyRejectsForeignSecret(t *testing.T) {
	m := newTestManager(t, false)
	applyTestConfig(t, m, nil)
	challengeJSON, err := m.ChallengeJSON(context.Background())
	require.NoError(t, err)
	payload := solveChallenge(t, challengeJSON)

	// Force re-provisioning with a different secret by deleting the row;
	// the challenge's signature was produced by the old secret.
	require.NoError(t, m.st.CaptchaConfig().Delete(context.Background()))
	m.mu.Lock()
	m.cached = nil
	m.mu.Unlock()

	_, err = m.ChallengeJSON(context.Background()) // provisions a new secret
	require.NoError(t, err)

	err = m.Verify(context.Background(), payload)
	assert.ErrorIs(t, err, ErrVerificationFailed)
}

func TestManagerExpiry(t *testing.T) {
	m := newTestManager(t, false)
	applyTestConfig(t, m, nil)
	challengeJSON, err := m.ChallengeJSON(context.Background())
	require.NoError(t, err)
	payload := solveChallenge(t, challengeJSON)

	// Tamper the solved payload's expiry into the past. (The config cannot
	// carry a sub-minute expiry anymore — Effective() validates it and
	// falls back to defaults — so the expiry path is exercised on the
	// payload, where the widget's clock actually applies it.)
	var p altcha.Payload
	raw, err := base64.StdEncoding.DecodeString(payload)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &p))
	p.Challenge.Parameters.ExpiresAt = time.Now().Add(-time.Minute).Unix()
	tampered, err := json.Marshal(p)
	require.NoError(t, err)
	payload = base64.StdEncoding.EncodeToString(tampered)

	err = m.Verify(context.Background(), payload)
	assert.ErrorIs(t, err, ErrVerificationFailed)
}

func TestManagerDisabledAndSolo(t *testing.T) {
	m := newTestManager(t, false)
	applyTestConfig(t, m, func(p *store.UpdateCaptchaConfigParams) {
		p.Enabled = false
	})
	assert.False(t, m.Enabled(context.Background()))
	// No payload required while disabled.
	require.NoError(t, m.Verify(context.Background(), ""))
	challengeJSON, err := m.ChallengeJSON(context.Background())
	require.NoError(t, err)
	assert.Empty(t, challengeJSON, "disabled hub must not hand out challenges")

	solo := newTestManager(t, true)
	assert.False(t, solo.Enabled(context.Background()))
	require.NoError(t, solo.Verify(context.Background(), ""))
}

// TestEnsureProvisionedRefreshesDescribeCache pins the shared ensureRow
// semantics: provisioning flips Describe's customized flag on the next
// read (the admin CLI's `captcha show` depends on it).
func TestEnsureProvisionedRefreshesDescribeCache(t *testing.T) {
	m := newTestManager(t, false)

	_, customized, err := m.Describe(context.Background())
	require.NoError(t, err)
	assert.False(t, customized, "fresh install reports built-in defaults")

	require.NoError(t, m.EnsureProvisioned(context.Background()))
	_, customized, err = m.Describe(context.Background())
	require.NoError(t, err)
	assert.True(t, customized, "a provisioned row is a stored configuration")
}

// TestEffectiveFallsBackOnInvalidRow pins consumption-time validation: a
// row written outside the CLI (direct SQL, a future migration) degrades to
// built-in defaults instead of issuing challenges nothing can solve.
func TestEffectiveFallsBackOnInvalidRow(t *testing.T) {
	// Non-power-of-two SCRYPT N: every derivation would error in
	// scrypt.Key, denying all logins with the uniform message.
	assert.Equal(t, DefaultConfig(), Effective(&store.CaptchaConfig{
		Enabled:                true,
		Algorithm:              "SCRYPT",
		Cost:                   3000,
		MemoryCost:             8,
		Parallelism:            1,
		ChallengeExpirySeconds: 1200,
	}))

	// A valid row overlays as before.
	family, err := FamilyDefaults("SCRYPT")
	require.NoError(t, err)
	got := Effective(&store.CaptchaConfig{
		Enabled:                false,
		Algorithm:              family.Algorithm,
		Cost:                   family.Cost,
		MemoryCost:             family.MemoryCost,
		Parallelism:            family.Parallelism,
		ChallengeExpirySeconds: 600,
	})
	assert.False(t, got.Enabled)
	assert.Equal(t, family.Algorithm, got.Algorithm)
	assert.EqualValues(t, family.Cost, got.Cost)
	assert.EqualValues(t, 600, got.ChallengeExpirySeconds)

	// Nil row is the built-in default.
	assert.Equal(t, DefaultConfig(), Effective(nil))
}

// TestReplayRejectedAcrossManagerInstances pins the store-backed
// single-use enforcement: a payload consumed by one manager (one hub
// instance, or the same hub after a restart) is rejected by another
// manager over the same database.
func TestReplayRejectedAcrossManagerInstances(t *testing.T) {
	st, err := sqlite.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	key := [32]byte{}
	ks, err := keystore.New(map[uint32][32]byte{1: key})
	require.NoError(t, err)

	first := NewManager(st, ks, false)
	payload := freshVerifiedPayload(t, first)
	require.NoError(t, first.Verify(context.Background(), payload))

	second := NewManager(st, ks, false)
	// Bust the second instance's config cache so it resolves the row the
	// first instance provisioned.
	second.mu.Lock()
	second.cached = nil
	second.descCached = nil
	second.mu.Unlock()
	err = second.Verify(context.Background(), payload)
	assert.ErrorIs(t, err, ErrVerificationFailed)
}

func TestConfigValidate(t *testing.T) {
	base := DefaultConfig()

	assert.NoError(t, base.Validate())

	unknown := base
	unknown.Algorithm = "MD5"
	assert.Error(t, unknown.Validate())

	lowCost := base
	lowCost.Cost = 10
	assert.Error(t, lowCost.Validate())

	highCost := base
	highCost.Cost = 10_000_000
	assert.Error(t, highCost.Validate())

	// Cost ranges are family-specific: ARGON2ID's cost is the time
	// parameter (normally 1-4) and SCRYPT's is N, which must also be a
	// power of two (scrypt.Key rejects anything else after validation
	// would have passed it through).
	smallN := base
	smallN.Algorithm, smallN.Cost, smallN.MemoryCost, smallN.Parallelism = "SCRYPT", 512, 8, 1
	assert.Error(t, smallN.Validate())
	okN := base
	okN.Algorithm, okN.Cost, okN.MemoryCost, okN.Parallelism = "SCRYPT", 1024, 8, 1
	assert.NoError(t, okN.Validate())
	oddN := base
	oddN.Algorithm, oddN.Cost, oddN.MemoryCost, oddN.Parallelism = "SCRYPT", 3000, 8, 1
	assert.Error(t, oddN.Validate(), "non-power-of-two SCRYPT N must be rejected at validation, not at derive time")

	okT := base
	okT.Algorithm, okT.Cost, okT.MemoryCost, okT.Parallelism = "ARGON2ID", 1, 65536, 1
	assert.NoError(t, okT.Validate())
	hugeT := base
	hugeT.Algorithm, hugeT.Cost, hugeT.MemoryCost, hugeT.Parallelism = "ARGON2ID", 65, 65536, 1
	assert.Error(t, hugeT.Validate())

	// Family-foreign parameters are rejected instead of silently
	// reinterpreted: an ARGON2ID memory (KiB) carried into SCRYPT becomes
	// the block multiplier r and multiplies the derivation memory.
	foreign := base
	foreign.Algorithm, foreign.MemoryCost = "PBKDF2/SHA-256", 65536
	assert.Error(t, foreign.Validate())
	foreignSha := base
	foreignSha.Algorithm, foreignSha.Parallelism = "SHA-256", 4
	assert.Error(t, foreignSha.Validate())

	// Memory ceilings: both the browser worker and the hub's re-derivation
	// allocate the full per-derivation memory on unauthenticated paths.
	hugeArgon := base
	hugeArgon.Algorithm, hugeArgon.MemoryCost, hugeArgon.Parallelism = "ARGON2ID", 512*1024, 1
	assert.Error(t, hugeArgon.Validate())
	hugeScrypt := base
	hugeScrypt.Algorithm, hugeScrypt.Cost, hugeScrypt.MemoryCost, hugeScrypt.Parallelism = "SCRYPT", 1<<20, 32, 1
	assert.Error(t, hugeScrypt.Validate(), "128 * N * r above the ceiling must be rejected")

	shortExpiry := base
	shortExpiry.ChallengeExpirySeconds = 1
	assert.Error(t, shortExpiry.Validate())

	longExpiry := base
	longExpiry.ChallengeExpirySeconds = 10 * 86400
	assert.Error(t, longExpiry.Validate())

	for _, tc := range []struct {
		alg  string
		cost int64
	}{
		{"SHA-256", 10000}, {"SHA-384", 10000}, {"SHA-512", 10000},
		{"PBKDF2/SHA-256", 10000}, {"PBKDF2/SHA-384", 10000}, {"PBKDF2/SHA-512", 10000},
	} {
		cfg := base
		cfg.Algorithm, cfg.Cost = tc.alg, tc.cost
		assert.NoError(t, cfg.Validate(), tc.alg)
	}
	scryptDefault, err := FamilyDefaults("SCRYPT")
	require.NoError(t, err)
	assert.NoError(t, scryptDefault.Validate())
	argonDefault, err := FamilyDefaults("ARGON2ID")
	require.NoError(t, err)
	assert.NoError(t, argonDefault.Validate())
	_, err = FamilyDefaults("MD5")
	assert.Error(t, err)

	// The supported-algorithm list is derived from the KDF registry and
	// sorted, so error text and CLI help cannot drift from it.
	assert.Equal(t, []string{
		"ARGON2ID", "PBKDF2/SHA-256", "PBKDF2/SHA-384", "PBKDF2/SHA-512",
		"SCRYPT", "SHA-256", "SHA-384", "SHA-512",
	}, SupportedAlgorithms())
}
