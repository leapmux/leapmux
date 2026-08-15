package captcha

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	altcha "github.com/altcha-org/altcha-lib-go/v2"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/sqlite"
	"github.com/leapmux/leapmux/internal/metrics"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
)

func newTestManager(t *testing.T, solo bool, opts ...Option) *Manager {
	t.Helper()
	st, err := sqlite.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	key := [32]byte{}
	ks, err := keystore.New(map[uint32][32]byte{1: key})
	require.NoError(t, err)
	return NewManager(st, ks, solo, opts...)
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

// cheapAltchaSettings overrides the defaults to the cheapest verifiable
// algorithm so challenge roundtrips stay fast in tests.
const cheapAltchaSettings = `{"algorithm":"SHA-256","cost":1000,"challenge_expiry_seconds":1200}`

// applyTestAltchaSettings swaps the altcha row's settings (keeping its
// provisioned secret and selection) and busts the manager's caches.
func applyTestAltchaSettings(t *testing.T, m *Manager, settings string) {
	t.Helper()
	ctx := context.Background()
	row, err := m.st.CaptchaConfig().Get(ctx, ProviderAltcha)
	if err != nil {
		require.ErrorIs(t, err, store.ErrNotFound)
		require.NoError(t, m.insertAltchaRow(ctx))
		row, err = m.st.CaptchaConfig().Get(ctx, ProviderAltcha)
		require.NoError(t, err)
	}
	require.NoError(t, m.st.CaptchaConfig().Activate(ctx, ProviderAltcha))
	require.NoError(t, m.st.CaptchaConfig().Upsert(ctx, store.UpsertCaptchaConfigParams{
		Provider: ProviderAltcha,
		Secret:   row.Secret,
		Settings: settings,
	}))
	bustCaches(m)
}

func bustCaches(m *Manager) {
	m.mu.Lock()
	m.cached = nil
	m.descCached = nil
	m.mu.Unlock()
}

// activateExternal writes an external provider's row with an encrypted
// secret and selects it, the way the admin CLI does.
func activateExternal(t *testing.T, m *Manager, provider Provider, settings, secret string) {
	t.Helper()
	ctx := context.Background()
	encrypted, err := m.EncryptSecret(provider, secret)
	require.NoError(t, err)
	require.NoError(t, m.st.CaptchaConfig().Upsert(ctx, store.UpsertCaptchaConfigParams{
		Provider: provider,
		Secret:   encrypted,
		Settings: settings,
	}))
	require.NoError(t, m.st.CaptchaConfig().Activate(ctx, provider))
	bustCaches(m)
}

// siteverifyStub serves canned siteverify replies; each test swaps the
// active response by writing resp before verifying.
type siteverifyStub struct {
	server   *httptest.Server
	status   int
	body     string
	lastForm url.Values
}

func newSiteverifyStub(t *testing.T) *siteverifyStub {
	t.Helper()
	stub := &siteverifyStub{status: http.StatusOK}
	stub.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		stub.lastForm = r.PostForm
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(stub.status)
		_, _ = w.Write([]byte(stub.body))
	}))
	t.Cleanup(stub.server.Close)
	return stub
}

func TestManagerDefaultsAndProvisioning(t *testing.T) {
	m := newTestManager(t, false)

	// Describe on a fresh install reports defaults and writes nothing.
	cfg, customized, err := m.Describe(context.Background())
	require.NoError(t, err)
	assert.True(t, cfg.Enabled)
	assert.Equal(t, ProviderAltcha, cfg.Provider)
	require.NotNil(t, cfg.Altcha)
	assert.Equal(t, "PBKDF2/SHA-256", cfg.Altcha.Algorithm)
	assert.EqualValues(t, 10000, cfg.Altcha.Cost)
	assert.Empty(t, cfg.SiteKey())
	assert.False(t, customized)
	_, err = m.st.CaptchaConfig().GetSelected(context.Background())
	assert.ErrorIs(t, err, store.ErrNotFound)

	// The first challenge provisions the altcha row (selected, with a
	// secret).
	_, err = m.AltchaChallengeJSON(context.Background())
	require.NoError(t, err)
	row, err := m.st.CaptchaConfig().GetSelected(context.Background())
	require.NoError(t, err)
	assert.Equal(t, ProviderAltcha, row.Provider)
	assert.True(t, row.Selected)
	assert.True(t, row.Enabled)
	assert.NotEmpty(t, row.Secret)
}

func TestManagerChallengeVerifyRoundtrip(t *testing.T) {
	m := newTestManager(t, false)
	// Provision under the default (expensive-to-solve) config; the issued
	// challenge is thrown away.
	_, err := m.AltchaChallengeJSON(context.Background())
	require.NoError(t, err)

	// Swap to cheap SHA for the roundtrip without disturbing the
	// provisioned secret.
	applyTestAltchaSettings(t, m, cheapAltchaSettings)
	challengeJSON, err := m.AltchaChallengeJSON(context.Background())
	require.NoError(t, err)

	payload := solveChallenge(t, challengeJSON)
	require.NoError(t, m.Verify(context.Background(), "login", payload))

	// Replaying the same payload must fail: salts are single-use.
	err = m.Verify(context.Background(), "login", payload)
	assert.ErrorIs(t, err, ErrVerificationFailed)

	// A fresh challenge verifies again.
	challengeJSON, err = m.AltchaChallengeJSON(context.Background())
	require.NoError(t, err)
	require.NoError(t, m.Verify(context.Background(), "login", solveChallenge(t, challengeJSON)))
}

func TestManagerVerifyRejectsGarbage(t *testing.T) {
	m := newTestManager(t, false)
	applyTestAltchaSettings(t, m, cheapAltchaSettings)

	for _, payload := range []string{"", "not-base64!!!", "e30="} { // {} — valid b64, wrong shape
		err := m.Verify(context.Background(), "login", payload)
		assert.ErrorIs(t, err, ErrVerificationFailed, "payload %q", payload)
	}
}

func TestManagerVerifyRejectsForeignSecret(t *testing.T) {
	m := newTestManager(t, false)
	applyTestAltchaSettings(t, m, cheapAltchaSettings)
	challengeJSON, err := m.AltchaChallengeJSON(context.Background())
	require.NoError(t, err)
	payload := solveChallenge(t, challengeJSON)

	// Force re-provisioning with a different secret by deleting the row;
	// the challenge's signature was produced by the old secret.
	require.NoError(t, m.st.CaptchaConfig().Delete(context.Background()))
	bustCaches(m)

	_, err = m.AltchaChallengeJSON(context.Background()) // provisions a new secret
	require.NoError(t, err)

	err = m.Verify(context.Background(), "login", payload)
	assert.ErrorIs(t, err, ErrVerificationFailed)
}

func TestManagerExpiry(t *testing.T) {
	m := newTestManager(t, false)
	applyTestAltchaSettings(t, m, cheapAltchaSettings)
	challengeJSON, err := m.AltchaChallengeJSON(context.Background())
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

	err = m.Verify(context.Background(), "login", payload)
	assert.ErrorIs(t, err, ErrVerificationFailed)
}

func TestManagerDisabledAndSolo(t *testing.T) {
	m := newTestManager(t, false)
	applyTestAltchaSettings(t, m, cheapAltchaSettings)
	require.NoError(t, m.st.CaptchaConfig().SetEnabled(context.Background(), false))
	bustCaches(m)
	assert.False(t, m.Enabled(context.Background()))
	// No payload required while disabled.
	require.NoError(t, m.Verify(context.Background(), "login", ""))
	challengeJSON, err := m.AltchaChallengeJSON(context.Background())
	require.NoError(t, err)
	assert.Empty(t, challengeJSON, "disabled hub must not hand out challenges")

	solo := newTestManager(t, true)
	assert.False(t, solo.Enabled(context.Background()))
	require.NoError(t, solo.Verify(context.Background(), "login", ""))
}

// TestEnsureProvisionedRefreshesDescribeCache pins the shared
// ensureSelectedRow semantics: provisioning flips Describe's customized
// flag on the next read (the admin CLI's `captcha show` depends on it).
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
	_, err = first.AltchaChallengeJSON(context.Background()) // provisions
	require.NoError(t, err)
	applyTestAltchaSettings(t, first, cheapAltchaSettings)
	challengeJSON, err := first.AltchaChallengeJSON(context.Background())
	require.NoError(t, err)
	payload := solveChallenge(t, challengeJSON)
	require.NoError(t, first.Verify(context.Background(), "login", payload))

	second := NewManager(st, ks, false)
	// Bust the second instance's config cache so it resolves the row the
	// first instance provisioned.
	bustCaches(second)
	err = second.Verify(context.Background(), "login", payload)
	assert.ErrorIs(t, err, ErrVerificationFailed)
}

// TestExternalProviderSwitchAndChallengeStanddown pins the switching
// semantics the CLI builds on: activating an external provider redirects
// verification, leaves the altcha row (and its secret) untouched, and
// stops ALTCHA challenge issuance; switching back reuses the original
// altcha secret.
func TestExternalProviderSwitchAndChallengeStanddown(t *testing.T) {
	stub := newSiteverifyStub(t)
	stub.body = `{"success":true,"action":"login","hostname":"example.com"}`
	m := newTestManager(t, false, WithTurnstileEndpoint(stub.server.URL))

	// Provision altcha and capture its secret.
	_, err := m.AltchaChallengeJSON(context.Background())
	require.NoError(t, err)
	altchaRow, err := m.st.CaptchaConfig().Get(context.Background(), ProviderAltcha)
	require.NoError(t, err)

	activateExternal(t, m, ProviderTurnstile, `{"site_key":"1x00000000000000000000AA"}`, "secret-key")
	require.NoError(t, m.Verify(context.Background(), "login", "tok"))

	// No altcha challenge while turnstile is selected, and the altcha row
	// keeps its original secret.
	challengeJSON, err := m.AltchaChallengeJSON(context.Background())
	require.NoError(t, err)
	assert.Empty(t, challengeJSON, "external providers have nothing to issue")
	row, err := m.st.CaptchaConfig().Get(context.Background(), ProviderAltcha)
	require.NoError(t, err)
	assert.Equal(t, altchaRow.Secret, row.Secret, "switching providers must not regenerate the altcha secret")

	// Switch back: the same secret still signs challenges.
	require.NoError(t, m.st.CaptchaConfig().Activate(context.Background(), ProviderAltcha))
	bustCaches(m)
	require.NoError(t, m.insertAltchaRow(context.Background())) // no-op: row exists
	applyTestAltchaSettings(t, m, cheapAltchaSettings)
	challengeJSON, err = m.AltchaChallengeJSON(context.Background())
	require.NoError(t, err)
	require.NoError(t, m.Verify(context.Background(), "login", solveChallenge(t, challengeJSON)))
}

// TestVerifyTurnstile pins the Turnstile policy checks: success + action
// match, duplicate tokens, and transport failures failing closed — all
// with the uniform client-facing error.
func TestVerifyTurnstile(t *testing.T) {
	stub := newSiteverifyStub(t)
	m := newTestManager(t, false, WithTurnstileEndpoint(stub.server.URL))
	activateExternal(t, m, ProviderTurnstile, `{"site_key":"1x00000000000000000000AA"}`, "secret-key")

	// Success requires the echoed action to match the procedure's.
	stub.body = `{"success":true,"action":"login","hostname":"example.com"}`
	require.NoError(t, m.Verify(context.Background(), "login", "tok"))
	// The wire contract with the provider: form-encoded secret + response,
	// the encoding both Google's and Cloudflare's endpoints require.
	require.NotNil(t, stub.lastForm)
	assert.Equal(t, "secret-key", stub.lastForm.Get("secret"))
	assert.Equal(t, "tok", stub.lastForm.Get("response"))
	stub.body = `{"success":true,"action":"signup","hostname":"example.com"}`
	assert.ErrorIs(t, m.Verify(context.Background(), "login", "tok"), ErrVerificationFailed)

	// The provider's duplicate-token error maps to the replayed metric
	// label but stays uniform to clients.
	stub.body = `{"success":false,"error-codes":["timeout-or-duplicate"]}`
	assert.ErrorIs(t, m.Verify(context.Background(), "login", "tok"), ErrVerificationFailed)

	// A plain denial and a missing payload both fail.
	stub.body = `{"success":false,"error-codes":["invalid-input-response"]}`
	assert.ErrorIs(t, m.Verify(context.Background(), "login", "tok"), ErrVerificationFailed)
	assert.ErrorIs(t, m.Verify(context.Background(), "login", ""), ErrVerificationFailed)

	// Transport faults fail closed: an unreachable provider must not
	// become an open door.
	stub.status = http.StatusInternalServerError
	assert.ErrorIs(t, m.Verify(context.Background(), "login", "tok"), ErrVerificationFailed)
	stub.status = http.StatusOK
	stub.body = `not-json`
	assert.ErrorIs(t, m.Verify(context.Background(), "login", "tok"), ErrVerificationFailed)
}

// TestVerifyRecaptcha pins the reCAPTCHA v3 policy checks on top of the
// shared ones: the score must clear the configured minimum.
func TestVerifyRecaptcha(t *testing.T) {
	stub := newSiteverifyStub(t)
	m := newTestManager(t, false, WithRecaptchaEndpoint(stub.server.URL))
	activateExternal(t, m, ProviderRecaptchaV3, `{"site_key":"site","min_score":0.6}`, "secret-key")

	stub.body = `{"success":true,"action":"signup","score":0.7}`
	require.NoError(t, m.Verify(context.Background(), "signup", "tok"))

	stub.body = `{"success":true,"action":"signup","score":0.5}`
	assert.ErrorIs(t, m.Verify(context.Background(), "signup", "tok"), ErrVerificationFailed, "score below the configured minimum is denied")

	stub.body = `{"success":true,"action":"login","score":0.9}`
	assert.ErrorIs(t, m.Verify(context.Background(), "signup", "tok"), ErrVerificationFailed, "an action mismatch is denied")
}

// TestVerifyMetricsLabelByProvider pins the provider label split on the
// captcha counter. The counter is process-global, so every assertion is
// a before/after delta.
func TestVerifyMetricsLabelByProvider(t *testing.T) {
	stub := newSiteverifyStub(t)
	m := newTestManager(t, false, WithTurnstileEndpoint(stub.server.URL))

	altchaPassedBefore := testutil.ToFloat64(metrics.CaptchaVerificationsTotal.WithLabelValues(ProviderAlias(ProviderAltcha), "passed"))
	turnstilePassedBefore := testutil.ToFloat64(metrics.CaptchaVerificationsTotal.WithLabelValues(ProviderAlias(ProviderTurnstile), "passed"))

	applyTestAltchaSettings(t, m, cheapAltchaSettings)
	challengeJSON, err := m.AltchaChallengeJSON(context.Background())
	require.NoError(t, err)
	require.NoError(t, m.Verify(context.Background(), "login", solveChallenge(t, challengeJSON)))
	assert.Equal(t, altchaPassedBefore+1, testutil.ToFloat64(metrics.CaptchaVerificationsTotal.WithLabelValues(ProviderAlias(ProviderAltcha), "passed")))
	assert.Equal(t, turnstilePassedBefore, testutil.ToFloat64(metrics.CaptchaVerificationsTotal.WithLabelValues(ProviderAlias(ProviderTurnstile), "passed")))

	turnstileReplayedBefore := testutil.ToFloat64(metrics.CaptchaVerificationsTotal.WithLabelValues(ProviderAlias(ProviderTurnstile), "replayed"))
	stub.body = `{"success":true,"action":"login"}`
	activateExternal(t, m, ProviderTurnstile, `{"site_key":"1x00000000000000000000AA"}`, "secret-key")
	require.NoError(t, m.Verify(context.Background(), "login", "tok"))
	assert.Equal(t, turnstilePassedBefore+1, testutil.ToFloat64(metrics.CaptchaVerificationsTotal.WithLabelValues(ProviderAlias(ProviderTurnstile), "passed")))

	stub.body = `{"success":false,"error-codes":["timeout-or-duplicate"]}`
	assert.ErrorIs(t, m.Verify(context.Background(), "login", "tok"), ErrVerificationFailed)
	assert.Equal(t, turnstileReplayedBefore+1, testutil.ToFloat64(metrics.CaptchaVerificationsTotal.WithLabelValues(ProviderAlias(ProviderTurnstile), "replayed")))
}

// TestSecretAADIsProviderScoped pins the provider-scoped AAD: pasting one
// provider row's ciphertext into another row must fail decryption (and
// therefore verification) rather than silently using the wrong key.
func TestSecretAADIsProviderScoped(t *testing.T) {
	m := newTestManager(t, false)
	activateExternal(t, m, ProviderTurnstile, `{"site_key":"1x00000000000000000000AA"}`, "secret-key")
	turnstileRow, err := m.st.CaptchaConfig().Get(context.Background(), ProviderTurnstile)
	require.NoError(t, err)

	// Move the turnstile ciphertext into the altcha row and select it.
	require.NoError(t, m.st.CaptchaConfig().Upsert(context.Background(), store.UpsertCaptchaConfigParams{
		Provider: ProviderAltcha,
		Secret:   turnstileRow.Secret,
		Settings: cheapAltchaSettings,
	}))
	require.NoError(t, m.st.CaptchaConfig().Activate(context.Background(), ProviderAltcha))
	bustCaches(m)

	// Decryption fails under the altcha AAD, so verification fails closed.
	// (The resolve error is wrapped, not the uniform sentinel — the
	// interceptor applies the uniform denial on top.)
	err = m.Verify(context.Background(), "login", "anything")
	require.Error(t, err)
	assert.ErrorContains(t, err, "decrypt captcha secret")
	assert.True(t, m.Enabled(context.Background()), "a broken row must fail closed, not stand down")
}

// TestEffectiveFallsBackOnInvalidRow pins consumption-time validation: a
// row written outside the CLI (direct SQL, a future migration) degrades to
// built-in defaults instead of issuing challenges nothing can solve.
func TestEffectiveFallsBackOnInvalidRow(t *testing.T) {
	// Non-power-of-two SCRYPT N: every derivation would error in
	// scrypt.Key, denying all logins with the uniform message.
	assert.Equal(t, DefaultConfig(), Effective(&store.CaptchaConfig{
		Provider: ProviderAltcha,
		Enabled:  true,
		Settings: `{"algorithm":"SCRYPT","cost":3000,"memory_cost":8,"parallelism":1}`,
	}))

	// An external provider with an empty site key cannot work at runtime.
	assert.Equal(t, DefaultConfig(), Effective(&store.CaptchaConfig{
		Provider: ProviderTurnstile,
		Enabled:  true,
		Settings: `{"site_key":""}`,
	}))

	// An unknown provider string cannot be dispatched on.
	assert.Equal(t, DefaultConfig(), Effective(&store.CaptchaConfig{
		Provider: Provider(99),
		Enabled:  true,
		Settings: `{}`,
	}))

	// Undecodable settings degrade to defaults.
	assert.Equal(t, DefaultConfig(), Effective(&store.CaptchaConfig{
		Provider: ProviderAltcha,
		Enabled:  true,
		Settings: "not-json",
	}))

	// A valid altcha row overlays as before.
	family, err := DefaultAltchaSettingsFor("SCRYPT")
	require.NoError(t, err)
	got := Effective(&store.CaptchaConfig{
		Provider: ProviderAltcha,
		Enabled:  false,
		Settings: fmtSettings(t, family, 600),
	})
	assert.False(t, got.Enabled)
	require.NotNil(t, got.Altcha)
	assert.Equal(t, family.Algorithm, got.Altcha.Algorithm)
	assert.EqualValues(t, family.Cost, got.Altcha.Cost)
	assert.EqualValues(t, 600, got.Altcha.ChallengeExpirySeconds)

	// A valid external row round-trips, with partial JSON filling from
	// that provider's defaults (min_score 0 means the 0.5 default).
	recaptcha := Effective(&store.CaptchaConfig{
		Provider: ProviderRecaptchaV3,
		Enabled:  true,
		Settings: `{"site_key":"site-key"}`,
	})
	require.NotNil(t, recaptcha.RecaptchaV3)
	assert.Equal(t, "site-key", recaptcha.SiteKey())
	assert.Equal(t, 0.5, recaptcha.RecaptchaV3.MinScore)

	// A partial altcha row fills from the algorithm family's defaults,
	// matching what the derive funcs substitute for zero values.
	partial := Effective(&store.CaptchaConfig{
		Provider: ProviderAltcha,
		Enabled:  true,
		Settings: `{"algorithm":"SCRYPT"}`,
	})
	scryptFamily, err := DefaultAltchaSettingsFor("SCRYPT")
	require.NoError(t, err)
	require.NotNil(t, partial.Altcha)
	assert.Equal(t, scryptFamily, *partial.Altcha)

	// Nil row is the built-in default.
	assert.Equal(t, DefaultConfig(), Effective(nil))
}

func fmtSettings(t *testing.T, s AltchaSettings, expiry int64) string {
	t.Helper()
	s.ChallengeExpirySeconds = expiry
	b, err := json.Marshal(s)
	require.NoError(t, err)
	return string(b)
}

func TestConfigValidate(t *testing.T) {
	// ALTCHA settings matrix.
	base := DefaultAltchaSettings()

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
	scryptDefault, err := DefaultAltchaSettingsFor("SCRYPT")
	require.NoError(t, err)
	assert.NoError(t, scryptDefault.Validate())
	argonDefault, err := DefaultAltchaSettingsFor("ARGON2ID")
	require.NoError(t, err)
	assert.NoError(t, argonDefault.Validate())
	_, err = DefaultAltchaSettingsFor("MD5")
	assert.Error(t, err)

	// The supported-algorithm list is derived from the KDF registry and
	// sorted, so error text and CLI help cannot drift from it.
	assert.Equal(t, []string{
		"ARGON2ID", "PBKDF2/SHA-256", "PBKDF2/SHA-384", "PBKDF2/SHA-512",
		"SCRYPT", "SHA-256", "SHA-384", "SHA-512",
	}, SupportedAltchaAlgorithms())

	// External provider settings: a site key each, plus reCAPTCHA's score
	// window.
	assert.Error(t, TurnstileSettings{}.Validate())
	assert.NoError(t, TurnstileSettings{SiteKey: "1x00AA"}.Validate())
	recaptcha := RecaptchaV3Settings{SiteKey: "site"}
	assert.Error(t, recaptcha.Validate(), "recaptcha without a site key is unusable")
	recaptcha.MinScore = 0.6
	assert.NoError(t, recaptcha.Validate())
	recaptcha.MinScore = 0
	assert.Error(t, recaptcha.Validate(), "a zero score threshold accepts everything")
	recaptcha.MinScore = 1.5
	assert.Error(t, recaptcha.Validate())

	// Config dispatches validation to the provider matching its settings
	// pointer and refuses unknown providers.
	assert.NoError(t, DefaultConfig().Validate())
	missingSettings := Config{Provider: ProviderAltcha}
	assert.Error(t, missingSettings.Validate())
	// An enum value outside the known set (possible on an open proto3
	// enum via direct SQL) is refused, and UNSPECIFIED never validates.
	unknownProvider := Config{Provider: Provider(99)}
	assert.Error(t, unknownProvider.Validate())
	assert.Error(t, Config{Provider: leapmuxv1.CaptchaProvider_CAPTCHA_PROVIDER_UNSPECIFIED}.Validate())

	// The alias registry, aliases, and parser share one closed set.
	assert.Equal(t, []string{"altcha", "recaptcha_v3", "turnstile"}, SupportedProviders())
	assert.Equal(t, "altcha", ProviderAlias(ProviderAltcha))
	assert.Equal(t, "recaptcha_v3", ProviderAlias(ProviderRecaptchaV3))
	assert.Equal(t, "turnstile", ProviderAlias(ProviderTurnstile))
	assert.Contains(t, ProviderAlias(Provider(99)), "99", "an unknown enum degrades to its number")
	for _, name := range SupportedProviders() {
		p, err := ParseProvider(name)
		require.NoError(t, err, name)
		assert.NotEqual(t, leapmuxv1.CaptchaProvider_CAPTCHA_PROVIDER_UNSPECIFIED, p)
	}
	// The kebab-case spelling parses too; the CAPS proto name does not.
	p, err := ParseProvider("recaptcha-v3")
	require.NoError(t, err)
	assert.Equal(t, ProviderRecaptchaV3, p)
	_, err = ParseProvider("CAPTCHA_PROVIDER_TURNSTILE")
	assert.Error(t, err)
	_, err = ParseProvider("hcaptcha")
	assert.Error(t, err)
}
