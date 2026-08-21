package captcha

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	altcha "github.com/altcha-org/altcha-lib-go/v2"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/hub/settings"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/sqlite"
	"github.com/leapmux/leapmux/internal/metrics"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
)

// testEnv bundles a captcha manager with the settings manager and store
// beneath it, so a test can reconfigure the manager the way the admin CLI
// does: typed writes through the shared settings manager apply to the
// next resolve without any cache busting.
type testEnv struct {
	m   *Manager
	set *settings.Manager
	st  store.Store
	ks  *keystore.Keystore
}

func newTestManager(t *testing.T, solo bool, opts ...Option) *testEnv {
	t.Helper()
	st, err := sqlite.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	key := [32]byte{}
	ks, err := keystore.New(map[uint32][32]byte{1: key})
	require.NoError(t, err)
	return newTestManagerOver(t, st, ks, solo, opts...)
}

// newTestManagerOver builds the env over a caller-supplied store (the
// counting wrappers below) so settings writes and snapshot reads flow
// through the same store the test observes.
func newTestManagerOver(t *testing.T, st store.Store, ks *keystore.Keystore, solo bool, opts ...Option) *testEnv {
	t.Helper()
	set := settings.NewManager(st, ks, SettingsDescriptors())
	require.NoError(t, set.Load(context.Background()))
	return &testEnv{
		m:   NewManager(st, set, solo, opts...),
		set: set,
		st:  st,
		ks:  ks,
	}
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
// provisioned secret and selection) through a settings-only update.
func applyTestAltchaSettings(t *testing.T, e *testEnv, raw string) {
	t.Helper()
	ctx := context.Background()
	// Provision first when no row exists: the update merges onto the
	// current row, and the secret must survive the swap.
	require.NoError(t, e.m.EnsureProvisioned(ctx))
	require.NoError(t, e.set.Update(ctx, AltchaKey, json.RawMessage(raw)))
}

// activateExternal writes an external provider's row with its secret and
// selects it, the way the admin CLI does.
func activateExternal(t *testing.T, e *testEnv, provider Provider, raw, secret string) {
	t.Helper()
	ctx := context.Background()
	switch provider {
	case ProviderRecaptchaV3:
		var row RecaptchaV3Row
		require.NoError(t, json.Unmarshal([]byte(raw), &row))
		row.SecretKey = secret
		require.NoError(t, RecaptchaV3Key.Set(ctx, e.set, row))
	case ProviderTurnstile:
		var row TurnstileRow
		require.NoError(t, json.Unmarshal([]byte(raw), &row))
		row.SecretKey = secret
		require.NoError(t, TurnstileKey.Set(ctx, e.set, row))
	default:
		t.Fatalf("unsupported external provider %v", provider)
	}
	require.NoError(t, CaptchaSelectedKey.Set(ctx, e.set, ProviderAlias(provider)))
}

// siteverifyStub serves canned siteverify replies; each test swaps the
// active response by writing resp before verifying.
type siteverifyStub struct {
	server *httptest.Server
	status int
	body   string
	// formMu guards lastForm: handler goroutines outlive their callers (a
	// deadline-expired request leaves one blocked on hold), so the record
	// of the latest form is shared between live goroutines.
	formMu   sync.Mutex
	lastForm url.Values
	requests atomic.Int32
	// hold, when non-nil, blocks every handler call until closed; tests
	// use it to keep one siteverify call in flight deterministically.
	hold chan struct{}
}

func newSiteverifyStub(t *testing.T) *siteverifyStub {
	t.Helper()
	stub := &siteverifyStub{status: http.StatusOK}
	stub.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		stub.formMu.Lock()
		stub.lastForm = r.PostForm
		stub.formMu.Unlock()
		stub.requests.Add(1)
		if stub.hold != nil {
			<-stub.hold
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(stub.status)
		_, _ = w.Write([]byte(stub.body))
	}))
	t.Cleanup(stub.server.Close)
	return stub
}

// form returns the form body of the most recent siteverify call.
func (s *siteverifyStub) form() url.Values {
	s.formMu.Lock()
	defer s.formMu.Unlock()
	return s.lastForm
}

func TestManagerDefaultsAndProvisioning(t *testing.T) {
	e := newTestManager(t, false)
	ctx := context.Background()

	// Describe on a fresh install reports defaults and writes nothing.
	cfg := e.m.Describe(ctx)
	assert.True(t, cfg.Enabled)
	assert.Equal(t, ProviderAltcha, cfg.Provider)
	require.NotNil(t, cfg.Altcha)
	assert.Equal(t, "PBKDF2/SHA-256", cfg.Altcha.Algorithm)
	assert.EqualValues(t, 10000, cfg.Altcha.Cost)
	assert.Empty(t, cfg.SiteKey())
	assert.False(t, e.set.Snapshot(ctx).Customized(AltchaKey))
	_, err := e.st.Settings().Get(ctx, AltchaKey.Name())
	assert.ErrorIs(t, err, store.ErrNotFound)

	// The first challenge provisions the altcha row (with a secret).
	_, err = e.m.AltchaChallengeJSON(ctx)
	require.NoError(t, err)
	snap := e.set.Snapshot(ctx)
	assert.True(t, snap.Customized(AltchaKey))
	assert.NotEmpty(t, AltchaKey.Of(snap).HMACKey)
}

func TestManagerChallengeVerifyRoundtrip(t *testing.T) {
	e := newTestManager(t, false)
	// Provision under the default (expensive-to-solve) config; the issued
	// challenge is thrown away.
	_, err := e.m.AltchaChallengeJSON(context.Background())
	require.NoError(t, err)

	// Swap to cheap SHA for the roundtrip without disturbing the
	// provisioned secret.
	applyTestAltchaSettings(t, e, cheapAltchaSettings)
	challengeJSON, err := e.m.AltchaChallengeJSON(context.Background())
	require.NoError(t, err)

	payload := solveChallenge(t, challengeJSON)
	require.NoError(t, e.m.Verify(context.Background(), "login", payload))

	// Replaying the same payload must fail: salts are single-use.
	err = e.m.Verify(context.Background(), "login", payload)
	assert.ErrorIs(t, err, ErrVerificationFailed)

	// A fresh challenge verifies again.
	challengeJSON, err = e.m.AltchaChallengeJSON(context.Background())
	require.NoError(t, err)
	require.NoError(t, e.m.Verify(context.Background(), "login", solveChallenge(t, challengeJSON)))
}

func TestManagerVerifyRejectsGarbage(t *testing.T) {
	e := newTestManager(t, false)
	applyTestAltchaSettings(t, e, cheapAltchaSettings)

	for _, payload := range []string{"", "not-base64!!!", "e30="} { // {} — valid b64, wrong shape
		err := e.m.Verify(context.Background(), "login", payload)
		assert.ErrorIs(t, err, ErrVerificationFailed, "payload %q", payload)
	}
}

// countingSaltStore counts HasAltchaSalt reads so tests can pin the
// verify path's cheapest-first ordering.
type countingSaltStore struct {
	store.Store
	saltReads atomic.Int32
}

type countingSaltAltchaSalts struct {
	store.AltchaSaltsStore
	reads *atomic.Int32
}

func (s countingSaltAltchaSalts) HasAltchaSalt(ctx context.Context, salt string) (bool, error) {
	s.reads.Add(1)
	return s.AltchaSaltsStore.HasAltchaSalt(ctx, salt)
}

func (s *countingSaltStore) AltchaSalts() store.AltchaSaltsStore {
	return countingSaltAltchaSalts{AltchaSaltsStore: s.Store.AltchaSalts(), reads: &s.saltReads}
}

// TestGarbagePayloadsBuyNoSaltReads pins the cheapest-first ordering: a
// decodable payload with a signature this hub never produced dies on the
// CPU-only signature pre-check, so an unauthenticated garbage flood
// cannot convert itself into salt-ledger reads. Only a hub-signed
// payload reaches the ledger — and its replay pays one indexed read,
// never the memory-hard derivation.
func TestGarbagePayloadsBuyNoSaltReads(t *testing.T) {
	inner := newTestManager(t, false)
	wrapped := &countingSaltStore{Store: inner.st}
	e := newTestManagerOver(t, wrapped, inner.ks, false)
	m := e.m
	applyTestAltchaSettings(t, e, cheapAltchaSettings)

	// A decodable, correctly-shaped payload whose signature no secret of
	// this hub produced: the ledger must never hear about it.
	garbage := base64.StdEncoding.EncodeToString([]byte(
		`{"challenge":{"parameters":{"algorithm":"SHA-1","nonce":"00","salt":"aabb","cost":100,"keyLength":8,"keyPrefix":"00"},"signature":"deadbeef"},"solution":{"counter":1,"derivedKey":"00"}}`))
	err := m.Verify(context.Background(), "login", garbage)
	assert.ErrorIs(t, err, ErrVerificationFailed)
	assert.EqualValues(t, 0, wrapped.saltReads.Load(), "a signature-invalid payload must not buy a store read")

	// A solved, hub-signed payload reaches the ledger exactly once.
	challengeJSON, err := m.AltchaChallengeJSON(context.Background())
	require.NoError(t, err)
	payload := solveChallenge(t, challengeJSON)
	require.NoError(t, m.Verify(context.Background(), "login", payload))
	assert.EqualValues(t, 1, wrapped.saltReads.Load())

	// Its replay is answered by the ledger — one more read, no derivation.
	err = m.Verify(context.Background(), "login", payload)
	assert.ErrorIs(t, err, ErrVerificationFailed)
	assert.EqualValues(t, 2, wrapped.saltReads.Load())
}

func TestManagerVerifyRejectsForeignSecret(t *testing.T) {
	e := newTestManager(t, false)
	applyTestAltchaSettings(t, e, cheapAltchaSettings)
	challengeJSON, err := e.m.AltchaChallengeJSON(context.Background())
	require.NoError(t, err)
	payload := solveChallenge(t, challengeJSON)

	// Force re-provisioning with a different secret by resetting the row
	// to its default; the challenge's signature was produced by the old
	// secret.
	require.NoError(t, e.set.Reset(context.Background(), AltchaKey))

	_, err = e.m.AltchaChallengeJSON(context.Background()) // provisions a new secret
	require.NoError(t, err)

	err = e.m.Verify(context.Background(), "login", payload)
	assert.ErrorIs(t, err, ErrVerificationFailed)
}

func TestManagerExpiry(t *testing.T) {
	e := newTestManager(t, false)
	applyTestAltchaSettings(t, e, cheapAltchaSettings)
	challengeJSON, err := e.m.AltchaChallengeJSON(context.Background())
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

	err = e.m.Verify(context.Background(), "login", payload)
	assert.ErrorIs(t, err, ErrVerificationFailed)
}

func TestManagerDisabledAndSolo(t *testing.T) {
	e := newTestManager(t, false)
	applyTestAltchaSettings(t, e, cheapAltchaSettings)
	require.NoError(t, CaptchaEnabledKey.Set(context.Background(), e.set, false))
	assert.False(t, e.m.Enabled(context.Background()))
	// No payload required while disabled.
	require.NoError(t, e.m.Verify(context.Background(), "login", ""))
	challengeJSON, err := e.m.AltchaChallengeJSON(context.Background())
	require.NoError(t, err)
	assert.Empty(t, challengeJSON, "disabled hub must not hand out challenges")

	solo := newTestManager(t, true)
	assert.False(t, solo.m.Enabled(context.Background()))
	require.NoError(t, solo.m.Verify(context.Background(), "login", ""))
}

// TestManagerSecureContextGateRuntimeDisablesAltcha pins the HTTP
// stand-down: when the browser page Origin is not a secure context and
// ALTCHA is selected, Describe / Verify / challenge issuance treat
// captcha as disabled without writing captcha.enabled. Turnstile on the
// same Origin stays enforced; missing Origin fails closed (stored on).
func TestManagerSecureContextGateRuntimeDisablesAltcha(t *testing.T) {
	e := newTestManager(t, false)
	applyTestAltchaSettings(t, e, cheapAltchaSettings)
	ctx := context.Background()

	assert.True(t, CaptchaEnabledKey.Of(e.set.Snapshot(ctx)),
		"precondition: settings row stays enabled through the whole test")

	insecure := withClientPageURL(ctx, "http://192.168.1.5:8080")
	cfg := e.m.Describe(insecure)
	assert.False(t, cfg.Enabled)
	assert.Equal(t, ProviderAltcha, cfg.Provider)
	assert.True(t, CaptchaEnabledKey.Of(e.set.Snapshot(ctx)),
		"runtime gate must not write captcha.enabled")

	require.NoError(t, e.m.Verify(insecure, "login", ""),
		"empty payload must pass when ALTCHA is runtime-gated")
	challengeJSON, err := e.m.AltchaChallengeJSON(insecure)
	require.NoError(t, err)
	assert.Empty(t, challengeJSON)

	secureHTTPS := withClientPageURL(ctx, "https://example.com")
	assert.True(t, e.m.Describe(secureHTTPS).Enabled)
	secureLocal := withClientPageURL(ctx, "http://localhost:8080")
	assert.True(t, e.m.Describe(secureLocal).Enabled)

	// Missing Origin: leave stored enablement alone (fail closed).
	assert.True(t, e.m.Describe(ctx).Enabled)
	require.Error(t, e.m.Verify(ctx, "login", ""))

	// External providers are never gated by the page scheme.
	stub := newSiteverifyStub(t)
	stub.body = `{"success":true,"action":"login"}`
	ext := newTestManager(t, false, WithTurnstileEndpoint(stub.server.URL))
	activateExternal(t, ext, ProviderTurnstile, `{"site_key":"1x00000000000000000000AA"}`, "secret-key")
	insecureExt := withClientPageURL(ctx, "http://192.168.1.5:8080")
	assert.True(t, ext.m.Describe(insecureExt).Enabled)
	require.NoError(t, ext.m.Verify(insecureExt, "login", "token"))
	require.Error(t, ext.m.Verify(insecureExt, "login", ""),
		"Turnstile on plain HTTP still requires a token")
}

// TestEnsureProvisionedRefreshesDescribeCache pins the provisioning
// semantics: provisioning flips the altcha row's customized flag on the
// next read (the admin CLI's `captcha show` depends on it).
func TestEnsureProvisionedRefreshesDescribeCache(t *testing.T) {
	e := newTestManager(t, false)

	assert.False(t, e.set.Snapshot(context.Background()).Customized(AltchaKey),
		"fresh install reports built-in defaults")

	require.NoError(t, e.m.EnsureProvisioned(context.Background()))
	assert.True(t, e.set.Snapshot(context.Background()).Customized(AltchaKey),
		"a provisioned row is a stored configuration")
}

// TestTuningBeforeFirstUseGetsItsSigningKeyFilled pins the order a
// tuning-only `captcha set` on a data dir the hub has never started on
// produces: a customized altcha row with NO signing key. Because the row
// is customized, neither SetIfAbsent nor the customized-row fast path
// would ever add one -- provisioning must notice the missing key, fill it
// with a partial-document update, and preserve the stored tuning, or
// every Login would fail closed with the uniform captcha error forever.
func TestTuningBeforeFirstUseGetsItsSigningKeyFilled(t *testing.T) {
	e := newTestManager(t, false)
	ctx := context.Background()

	// The admin CLI's tuning-only write on a fresh dir: settings reach the
	// row, the signing key never does.
	require.NoError(t, e.set.Update(ctx, AltchaKey, json.RawMessage(cheapAltchaSettings)))

	require.NoError(t, e.m.EnsureProvisioned(ctx))
	snap := e.set.Snapshot(ctx)
	row := AltchaKey.Of(snap)
	assert.NotEmpty(t, row.HMACKey, "provisioning fills the missing signing key")
	assert.Equal(t, "SHA-256", row.Algorithm, "the stored tuning survives the fill")
	assert.EqualValues(t, 1000, row.Cost)

	// The filled key actually works end to end: a challenge issues and
	// verifies under it.
	challengeJSON, err := e.m.AltchaChallengeJSON(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, challengeJSON)
	payload := solveChallenge(t, challengeJSON)
	require.NoError(t, e.m.Verify(ctx, "login", payload))

	// The resolve-path self-heal (no EnsureProvisioned call) covers the
	// same state on a fresh manager over the same database.
	e2 := newTestManagerOver(t, e.st, e.ks, false)
	challengeJSON, err = e2.m.AltchaChallengeJSON(ctx)
	require.NoError(t, err, "the first-use self-heal fills a keyless row too")
	require.NoError(t, e2.m.Verify(ctx, "login", solveChallenge(t, challengeJSON)))
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

	first := newTestManagerOver(t, st, ks, false)
	_, err = first.m.AltchaChallengeJSON(context.Background()) // provisions
	require.NoError(t, err)
	applyTestAltchaSettings(t, first, cheapAltchaSettings)
	challengeJSON, err := first.m.AltchaChallengeJSON(context.Background())
	require.NoError(t, err)
	payload := solveChallenge(t, challengeJSON)
	require.NoError(t, first.m.Verify(context.Background(), "login", payload))

	// The second instance loads its own snapshot, resolving the row the
	// first instance provisioned.
	second := newTestManagerOver(t, st, ks, false)
	err = second.m.Verify(context.Background(), "login", payload)
	assert.ErrorIs(t, err, ErrVerificationFailed)
}

// TestExternalProviderSwitchAndChallengeStanddown pins the switching
// semantics the CLI builds on: activating an external provider redirects
// verification, leaves the altcha row (and its secret) untouched, and
// stops ALTCHA challenge issuance with a typed error — an empty string
// means "captcha disabled" and would make a stale altcha widget stand
// down; switching back reuses the original altcha secret.
func TestExternalProviderSwitchAndChallengeStanddown(t *testing.T) {
	stub := newSiteverifyStub(t)
	stub.body = `{"success":true,"action":"login","hostname":"example.com"}`
	e := newTestManager(t, false, WithTurnstileEndpoint(stub.server.URL))

	// Provision altcha and capture its secret.
	_, err := e.m.AltchaChallengeJSON(context.Background())
	require.NoError(t, err)
	altchaRow := AltchaKey.Of(e.set.Snapshot(context.Background()))

	activateExternal(t, e, ProviderTurnstile, `{"site_key":"1x00000000000000000000AA"}`, "secret-key")
	require.NoError(t, e.m.Verify(context.Background(), "login", "tok"))

	// No altcha challenge while turnstile is selected — an error, not an
	// empty string, so a stale altcha widget shows its error state instead
	// of standing down — and the altcha row keeps its original secret.
	_, err = e.m.AltchaChallengeJSON(context.Background())
	require.ErrorIs(t, err, ErrProviderNotAltcha)
	assert.Equal(t, altchaRow.HMACKey, AltchaKey.Of(e.set.Snapshot(context.Background())).HMACKey,
		"switching providers must not regenerate the altcha secret")

	// Switch back: the same secret still signs challenges.
	require.NoError(t, CaptchaSelectedKey.Set(context.Background(), e.set, ProviderAlias(ProviderAltcha)))
	applyTestAltchaSettings(t, e, cheapAltchaSettings)
	challengeJSON, err := e.m.AltchaChallengeJSON(context.Background())
	require.NoError(t, err)
	require.NoError(t, e.m.Verify(context.Background(), "login", solveChallenge(t, challengeJSON)))
}

// TestVerifyTurnstile pins the Turnstile policy checks: success + action
// match, duplicate tokens, and transport failures failing closed — all
// with the uniform client-facing error.
func TestVerifyTurnstile(t *testing.T) {
	stub := newSiteverifyStub(t)
	e := newTestManager(t, false, WithTurnstileEndpoint(stub.server.URL))
	activateExternal(t, e, ProviderTurnstile, `{"site_key":"1x00000000000000000000AA"}`, "secret-key")

	// Success requires the echoed action to match the procedure's.
	stub.body = `{"success":true,"action":"login","hostname":"example.com"}`
	require.NoError(t, e.m.Verify(context.Background(), "login", "tok"))
	// The wire contract with the provider: form-encoded secret + response,
	// the encoding both Google's and Cloudflare's endpoints require.
	lastForm := stub.form()
	require.NotNil(t, lastForm)
	assert.Equal(t, "secret-key", lastForm.Get("secret"))
	assert.Equal(t, "tok", lastForm.Get("response"))
	stub.body = `{"success":true,"action":"signup","hostname":"example.com"}`
	assert.ErrorIs(t, e.m.Verify(context.Background(), "login", "tok"), ErrVerificationFailed)

	// The provider's duplicate-token error maps to the replayed metric
	// label but stays uniform to clients.
	stub.body = `{"success":false,"error-codes":["timeout-or-duplicate"]}`
	assert.ErrorIs(t, e.m.Verify(context.Background(), "login", "tok"), ErrVerificationFailed)

	// A plain denial and a missing payload both fail.
	stub.body = `{"success":false,"error-codes":["invalid-input-response"]}`
	assert.ErrorIs(t, e.m.Verify(context.Background(), "login", "tok"), ErrVerificationFailed)
	assert.ErrorIs(t, e.m.Verify(context.Background(), "login", ""), ErrVerificationFailed)

	// Transport faults fail closed: an unreachable provider must not
	// become an open door.
	stub.status = http.StatusInternalServerError
	assert.ErrorIs(t, e.m.Verify(context.Background(), "login", "tok"), ErrVerificationFailed)
	stub.status = http.StatusOK
	stub.body = `not-json`
	assert.ErrorIs(t, e.m.Verify(context.Background(), "login", "tok"), ErrVerificationFailed)
}

// TestVerifyRecaptcha pins the reCAPTCHA v3 policy checks on top of the
// shared ones: the score must clear the configured minimum.
func TestVerifyRecaptcha(t *testing.T) {
	stub := newSiteverifyStub(t)
	e := newTestManager(t, false, WithRecaptchaEndpoint(stub.server.URL))
	activateExternal(t, e, ProviderRecaptchaV3, `{"site_key":"site","min_score":0.6}`, "secret-key")

	stub.body = `{"success":true,"action":"signup","score":0.7}`
	require.NoError(t, e.m.Verify(context.Background(), "signup", "tok"))

	stub.body = `{"success":true,"action":"signup","score":0.5}`
	assert.ErrorIs(t, e.m.Verify(context.Background(), "signup", "tok"), ErrVerificationFailed, "score below the configured minimum is denied")

	stub.body = `{"success":true,"action":"login","score":0.9}`
	assert.ErrorIs(t, e.m.Verify(context.Background(), "signup", "tok"), ErrVerificationFailed, "an action mismatch is denied")
}

// TestVerifyMetricsLabelByProvider pins the provider label split on the
// captcha counter. The counter is process-global, so every assertion is
// a before/after delta.
func TestVerifyMetricsLabelByProvider(t *testing.T) {
	stub := newSiteverifyStub(t)
	e := newTestManager(t, false, WithTurnstileEndpoint(stub.server.URL))

	altchaPassedBefore := testutil.ToFloat64(metrics.CaptchaVerificationsTotal.WithLabelValues(ProviderAlias(ProviderAltcha), "passed"))
	turnstilePassedBefore := testutil.ToFloat64(metrics.CaptchaVerificationsTotal.WithLabelValues(ProviderAlias(ProviderTurnstile), "passed"))

	applyTestAltchaSettings(t, e, cheapAltchaSettings)
	challengeJSON, err := e.m.AltchaChallengeJSON(context.Background())
	require.NoError(t, err)
	require.NoError(t, e.m.Verify(context.Background(), "login", solveChallenge(t, challengeJSON)))
	assert.Equal(t, altchaPassedBefore+1, testutil.ToFloat64(metrics.CaptchaVerificationsTotal.WithLabelValues(ProviderAlias(ProviderAltcha), "passed")))
	assert.Equal(t, turnstilePassedBefore, testutil.ToFloat64(metrics.CaptchaVerificationsTotal.WithLabelValues(ProviderAlias(ProviderTurnstile), "passed")))

	turnstileReplayedBefore := testutil.ToFloat64(metrics.CaptchaVerificationsTotal.WithLabelValues(ProviderAlias(ProviderTurnstile), "replayed"))
	stub.body = `{"success":true,"action":"login"}`
	activateExternal(t, e, ProviderTurnstile, `{"site_key":"1x00000000000000000000AA"}`, "secret-key")
	require.NoError(t, e.m.Verify(context.Background(), "login", "tok"))
	assert.Equal(t, turnstilePassedBefore+1, testutil.ToFloat64(metrics.CaptchaVerificationsTotal.WithLabelValues(ProviderAlias(ProviderTurnstile), "passed")))

	stub.body = `{"success":false,"error-codes":["timeout-or-duplicate"]}`
	assert.ErrorIs(t, e.m.Verify(context.Background(), "login", "tok"), ErrVerificationFailed)
	assert.Equal(t, turnstileReplayedBefore+1, testutil.ToFloat64(metrics.CaptchaVerificationsTotal.WithLabelValues(ProviderAlias(ProviderTurnstile), "replayed")))
}

// TestSecretAADIsProviderScoped pins the key-scoped AAD: pasting one
// settings key's ciphertext into another key's secret half must fail
// decryption under the wrong key name — the manager never serves the
// pasted (wrong) secret. It self-heals a fresh signing key in its place,
// and a submission under the tampered row still fails.
func TestSecretAADIsProviderScoped(t *testing.T) {
	e := newTestManager(t, false)
	ctx := context.Background()
	activateExternal(t, e, ProviderTurnstile, `{"site_key":"1x00000000000000000000AA"}`, "secret-key")
	turnstileRow, err := e.st.Settings().Get(ctx, TurnstileKey.Name())
	require.NoError(t, err)
	require.NotEmpty(t, turnstileRow.Secret)

	// The ciphertext is bound to the turnstile key's AAD: it must not
	// decrypt under the altcha key name.
	_, err = e.ks.Decrypt(turnstileRow.Secret, keystore.SettingsSecretAAD(AltchaKey.Name()))
	require.Error(t, err, "ciphertext pasted into another key's row must not decrypt")

	// Move it into the altcha row's secret half (direct SQL, outside the
	// write path) and switch the selection back.
	empty := "{}"
	require.NoError(t, e.st.Settings().Upsert(ctx, store.UpsertSettingParams{
		Key:    AltchaKey.Name(),
		Value:  &empty,
		Secret: turnstileRow.Secret,
	}))
	require.NoError(t, CaptchaSelectedKey.Set(ctx, e.set, ProviderAlias(ProviderAltcha)))

	// Reload through a fresh manager: the pasted secret fails decryption
	// and is never served — the manager self-heals a fresh key, so the
	// wrong secret authenticates nothing and the submission fails closed.
	tampered := newTestManagerOver(t, e.st, e.ks, false)
	err = tampered.m.Verify(ctx, "login", "anything")
	require.ErrorIs(t, err, ErrVerificationFailed)
	healed := AltchaKey.Of(tampered.set.Snapshot(ctx))
	require.NotEmpty(t, healed.HMACKey, "the tampered row must be self-healed with a fresh key")
	assert.NotEqual(t, []byte("secret-key"), healed.HMACKey, "the pasted provider secret must never serve as the altcha key")
	assert.True(t, tampered.m.Enabled(ctx), "a broken row must fail closed, not stand down")
}

// rawSnapshot loads a settings snapshot from raw stored JSON documents —
// rows written outside the admin CLI's write path (direct SQL, a future
// migration) — so consumption-time validation runs on exactly what the
// store holds.
func rawSnapshot(t *testing.T, rows map[string]string) *settings.Snapshot {
	t.Helper()
	st, err := sqlite.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	for key, value := range rows {
		v := value
		require.NoError(t, st.Settings().Upsert(ctx, store.UpsertSettingParams{Key: key, Value: &v}))
	}
	set := settings.NewManager(st, nil, SettingsDescriptors())
	require.NoError(t, set.Load(ctx))
	return set.Snapshot(ctx)
}

// TestEffectiveFallsBackOnInvalidRow pins consumption-time validation: a
// row written outside the CLI (direct SQL, a future migration) degrades
// to built-in defaults instead of issuing challenges nothing can solve.
func TestEffectiveFallsBackOnInvalidRow(t *testing.T) {
	// Rows the settings manager itself degrades (per-key validation
	// failures) never reach Effective broken: the snapshot already holds
	// the key's default, so Effective reports it with no fallback reason
	// — the warn-once came from the manager's decode path.
	//
	// Non-power-of-two SCRYPT N: every derivation would error in
	// scrypt.Key, denying all logins with the uniform message.
	cfg, reason := Effective(rawSnapshot(t, map[string]string{
		AltchaKey.Name(): `{"algorithm":"SCRYPT","cost":3000,"memory_cost":8,"parallelism":1}`,
	}))
	assert.Equal(t, DefaultConfig(), cfg)
	assert.Empty(t, reason, "the manager degraded the row before Effective saw it")

	// An unknown provider alias cannot be dispatched on — the selected
	// row degrades to the altcha default the same way.
	cfg, reason = Effective(rawSnapshot(t, map[string]string{
		CaptchaSelectedKey.Name(): `"hcaptcha"`,
	}))
	assert.Equal(t, DefaultConfig(), cfg)
	assert.Empty(t, reason)

	// Undecodable settings degrade to defaults.
	cfg, reason = Effective(rawSnapshot(t, map[string]string{
		AltchaKey.Name(): "not-json",
	}))
	assert.Equal(t, DefaultConfig(), cfg)
	assert.Empty(t, reason)

	// The composite case the per-key validators cannot see: an external
	// provider selected with a key-valid but unconfigured row (an empty
	// site key passes key validation; SelectedConfigured is the write-path
	// guard). Effective falls back to the built-in defaults here and
	// carries the reason.
	cfg, reason = Effective(rawSnapshot(t, map[string]string{
		CaptchaSelectedKey.Name(): `"` + ProviderAlias(ProviderTurnstile) + `"`,
		TurnstileKey.Name():       `{"site_key":""}`,
	}))
	assert.Equal(t, DefaultConfig(), cfg)
	assert.NotEmpty(t, reason, "the composite fallback carries its reason")
	assert.Contains(t, reason, ProviderAlias(ProviderTurnstile))

	// The fallback preserves the enabled switch: a deliberately disabled
	// hub stays disabled through corruption, instead of the fallback
	// silently re-arming captcha the admin turned off.
	disabled := DefaultConfig()
	disabled.Enabled = false
	cfg, reason = Effective(rawSnapshot(t, map[string]string{
		CaptchaEnabledKey.Name(): "false",
		AltchaKey.Name():         "not-json",
	}))
	assert.Equal(t, disabled, cfg)
	assert.Empty(t, reason)
	cfg, reason = Effective(rawSnapshot(t, map[string]string{
		CaptchaEnabledKey.Name():  "false",
		CaptchaSelectedKey.Name(): `"` + ProviderAlias(ProviderTurnstile) + `"`,
		TurnstileKey.Name():       `{"site_key":""}`,
	}))
	assert.Equal(t, disabled, cfg)
	assert.NotEmpty(t, reason)

	// A valid altcha row overlays as before, with no fallback reason.
	family, err := DefaultAltchaSettingsFor("SCRYPT")
	require.NoError(t, err)
	got, reason := Effective(rawSnapshot(t, map[string]string{
		CaptchaEnabledKey.Name(): "false",
		AltchaKey.Name():         fmtSettings(t, family, 600),
	}))
	assert.Empty(t, reason)
	assert.False(t, got.Enabled)
	require.NotNil(t, got.Altcha)
	assert.Equal(t, family.Algorithm, got.Altcha.Algorithm)
	assert.EqualValues(t, family.Cost, got.Altcha.Cost)
	assert.EqualValues(t, 600, got.Altcha.ChallengeExpirySeconds)

	// A valid external row round-trips, with partial JSON filling from
	// that provider's defaults (an absent min_score means the 0.5 default,
	// an explicit one survives the round-trip).
	recaptcha, reason := Effective(rawSnapshot(t, map[string]string{
		CaptchaSelectedKey.Name(): `"` + ProviderAlias(ProviderRecaptchaV3) + `"`,
		RecaptchaV3Key.Name():     `{"site_key":"site-key"}`,
	}))
	assert.Empty(t, reason)
	require.NotNil(t, recaptcha.RecaptchaV3)
	assert.Equal(t, "site-key", recaptcha.SiteKey())
	assert.Equal(t, 0.5, recaptcha.RecaptchaV3.MinScore)
	recaptcha, reason = Effective(rawSnapshot(t, map[string]string{
		CaptchaSelectedKey.Name(): `"` + ProviderAlias(ProviderRecaptchaV3) + `"`,
		RecaptchaV3Key.Name():     `{"site_key":"site-key","min_score":0.7}`,
	}))
	assert.Empty(t, reason)
	assert.Equal(t, 0.7, recaptcha.RecaptchaV3.MinScore)

	// A row whose document names no fields keeps the key's declared
	// defaults, the partial-row semantics the write path merges with.
	partial, reason := Effective(rawSnapshot(t, map[string]string{
		AltchaKey.Name(): `{}`,
	}))
	assert.Empty(t, reason)
	require.NotNil(t, partial.Altcha)
	assert.Equal(t, DefaultAltchaSettings(), *partial.Altcha)

	// An empty snapshot is the built-in default.
	cfg, reason = Effective(rawSnapshot(t, nil))
	assert.Equal(t, DefaultConfig(), cfg)
	assert.Empty(t, reason)
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

// TestVerifyCountsUnattributedDenials pins the outage signal: an
// unreachable store fails every submission closed (first-use provisioning
// cannot write), and the denial still counts under the "unknown" label so
// the counter shows failed traffic during the outage instead of silence.
// A corrupted secret half no longer drives this path — it self-heals.
func TestVerifyCountsUnattributedDenials(t *testing.T) {
	e := newTestManager(t, false)
	ctx := context.Background()
	require.NoError(t, e.st.Close())

	before := testutil.ToFloat64(metrics.CaptchaVerificationsTotal.WithLabelValues(unknownProviderLabel, string(ResultFailed)))
	require.Error(t, e.m.Verify(ctx, "login", "tok"))
	assert.Equal(t, before+1, testutil.ToFloat64(metrics.CaptchaVerificationsTotal.WithLabelValues(unknownProviderLabel, string(ResultFailed))))
}

// TestSiteverifyBreakerShedsLoad pins the breaker: consecutive transport
// faults open the circuit, an open circuit fails closed WITHOUT reaching
// the provider, and the half-open probe after the cooldown closes it
// again on success.
func TestSiteverifyBreakerShedsLoad(t *testing.T) {
	stub := newSiteverifyStub(t)
	e := newTestManager(t, false, WithTurnstileEndpoint(stub.server.URL))
	activateExternal(t, e, ProviderTurnstile, `{"site_key":"1x00AA"}`, "secret-key")
	m := e.m
	m.turnstile.cooldown = 250 * time.Millisecond

	// Consecutive transport faults trip the breaker.
	stub.status = http.StatusInternalServerError
	for i := 0; i < siteverifyBreakerThreshold; i++ {
		assert.ErrorIs(t, m.Verify(context.Background(), "login", "tok"), ErrVerificationFailed)
	}
	requestsAtTrip := int(stub.requests.Load())
	require.Equal(t, siteverifyBreakerThreshold, requestsAtTrip)

	// While the circuit is open, attempts fail closed without a call.
	stub.status = http.StatusOK
	stub.body = `{"success":true,"action":"login","hostname":"example.com"}`
	assert.ErrorIs(t, m.Verify(context.Background(), "login", "tok"), ErrVerificationFailed)
	assert.Equal(t, requestsAtTrip, int(stub.requests.Load()), "an open breaker must not dial the provider")

	// After the cooldown the circuit half-opens: the next call probes,
	// and a success closes it.
	time.Sleep(300 * time.Millisecond)
	require.NoError(t, m.Verify(context.Background(), "login", "tok"))
	assert.Equal(t, requestsAtTrip+1, int(stub.requests.Load()), "the half-open probe is exactly one call")

	// A faulting half-open probe re-opens the circuit without another
	// threshold run: the probe dials once, and the next attempt — with
	// the provider healthy again — still fails closed without dialing.
	stub.status = http.StatusInternalServerError
	for i := 0; i < siteverifyBreakerThreshold; i++ {
		assert.ErrorIs(t, m.Verify(context.Background(), "login", "tok"), ErrVerificationFailed)
	}
	time.Sleep(300 * time.Millisecond)
	assert.ErrorIs(t, m.Verify(context.Background(), "login", "tok"), ErrVerificationFailed) // the faulting probe
	requestsAfterProbe := int(stub.requests.Load())
	stub.status = http.StatusOK
	assert.ErrorIs(t, m.Verify(context.Background(), "login", "tok"), ErrVerificationFailed)
	assert.Equal(t, requestsAfterProbe, int(stub.requests.Load()), "a re-tripped breaker must not dial the provider")
}

// countingSettingsStore counts snapshot loads (Settings().GetAll) so
// tests can pin the cold-cache coalescing behind resolve and Describe.
type countingSettingsStore struct {
	store.Store
	loads atomic.Int32
}

type countingSettings struct {
	store.SettingsStore
	loads *atomic.Int32
}

func (s countingSettings) GetAll(ctx context.Context) ([]store.SettingRow, error) {
	s.loads.Add(1)
	return s.SettingsStore.GetAll(ctx)
}

func (s *countingSettingsStore) Settings() store.SettingsStore {
	return countingSettings{SettingsStore: s.Store.Settings(), loads: &s.loads}
}

// newColdManagerOver builds a captcha manager whose settings snapshot has
// NOT been loaded yet, over a caller-supplied store.
func newColdManagerOver(t *testing.T, st store.Store, ks *keystore.Keystore) *Manager {
	t.Helper()
	return NewManager(st, settings.NewManager(st, ks, SettingsDescriptors()), false)
}

// TestResolveCoalescesConcurrentColdLoads pins the singleflight: a burst
// of resolves arriving at a cold (or freshly expired) snapshot shares ONE
// store load instead of each goroutine running its own full load (row +
// keystore decrypt + settings validation). The row is pre-provisioned so
// what the burst shares is exactly the load, not the first-use write.
func TestResolveCoalescesConcurrentColdLoads(t *testing.T) {
	inner := newTestManager(t, false)
	require.NoError(t, inner.m.EnsureProvisioned(context.Background()))

	wrapped := &countingSettingsStore{Store: inner.st}
	m := newColdManagerOver(t, wrapped, inner.ks)

	const burst = 8
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make([]error, burst)
	for i := 0; i < burst; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, err := m.resolve(context.Background())
			errs[i] = err
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		require.NoErrorf(t, err, "resolve %d", i)
	}
	assert.EqualValues(t, 1, wrapped.loads.Load(),
		"a concurrent cold-cache burst must share one snapshot load, not one load per goroutine")
}

// TestDescribeCoalescesConcurrentColdLoads pins Describe's own share of
// the singleflight: a burst of Describe calls at a cold snapshot shares
// ONE store load. Describe never provisions, so one load is exactly one
// read.
func TestDescribeCoalescesConcurrentColdLoads(t *testing.T) {
	inner := newTestManager(t, false)
	wrapped := &countingSettingsStore{Store: inner.st}
	m := newColdManagerOver(t, wrapped, inner.ks)

	const burst = 8
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < burst; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			m.Describe(context.Background())
		}()
	}
	close(start)
	wg.Wait()

	assert.EqualValues(t, 1, wrapped.loads.Load(),
		"a concurrent cold-cache Describe burst must share one snapshot load (describe never provisions, so one load is exactly one read)")
}

// TestSiteverifyBreakerIgnoresCallerCancellation pins the fault
// accounting: a call that ends because the CALLER went away (client
// disconnect, cancelled request) is neither a provider fault nor a
// success, so any number of aborted requests must not open the circuit
// against a healthy provider.
func TestSiteverifyBreakerIgnoresCallerCancellation(t *testing.T) {
	stub := newSiteverifyStub(t)
	stub.body = `{"success":true,"action":"login","hostname":"example.com"}`
	e := newTestManager(t, false, WithTurnstileEndpoint(stub.server.URL))

	// Drive the siteverify client directly: through Manager.Verify a
	// cancelled context dies earlier, in the config resolve.
	client := e.m.turnstile
	for i := 0; i < siteverifyBreakerThreshold*3; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := client.verify(ctx, "secret-key", "tok")
		require.Error(t, err)
	}
	assert.False(t, client.breakerTripped(), "aborted calls must not open the circuit")

	// A healthy caller still dials and succeeds.
	resp, err := client.verify(context.Background(), "secret-key", "tok")
	require.NoError(t, err)
	assert.True(t, resp.Success)
	assert.Equal(t, 1, int(stub.requests.Load()), "aborted calls must not dial; only the healthy call dials")
}

// TestSiteverifyBreakerHalfOpenAdmitsOneProbe pins the single-probe gate:
// after the cooldown, exactly one caller dials the provider while every
// concurrent caller still fails fast — an unbounded probe burst on a
// recovering provider would re-trip the circuit and stretch the outage.
func TestSiteverifyBreakerHalfOpenAdmitsOneProbe(t *testing.T) {
	stub := newSiteverifyStub(t)
	e := newTestManager(t, false, WithTurnstileEndpoint(stub.server.URL))
	activateExternal(t, e, ProviderTurnstile, `{"site_key":"1x00AA"}`, "secret-key")
	m := e.m
	m.turnstile.cooldown = 250 * time.Millisecond

	stub.status = http.StatusInternalServerError
	for i := 0; i < siteverifyBreakerThreshold; i++ {
		assert.ErrorIs(t, m.Verify(context.Background(), "login", "tok"), ErrVerificationFailed)
	}
	requestsAtTrip := int(stub.requests.Load())
	time.Sleep(300 * time.Millisecond)

	// Hold the probe's HTTP call in flight, then prove the next concurrent
	// caller fails fast without a second dial.
	stub.status = http.StatusOK
	stub.body = `{"success":true,"action":"login","hostname":"example.com"}`
	stub.hold = make(chan struct{})
	probeDone := make(chan error, 1)
	go func() {
		probeDone <- m.Verify(context.Background(), "login", "tok")
	}()
	require.Eventually(t, func() bool {
		return int(stub.requests.Load()) == requestsAtTrip+1
	}, 2*time.Second, 5*time.Millisecond, "the probe must be the one call that dials")

	assert.ErrorIs(t, m.Verify(context.Background(), "login", "tok"), ErrVerificationFailed,
		"a concurrent caller must fail fast while the probe is in flight")
	assert.Equal(t, requestsAtTrip+1, int(stub.requests.Load()), "the concurrent caller must not dial")

	close(stub.hold)
	require.NoError(t, <-probeDone, "the probe's success closes the circuit")
	require.NoError(t, m.Verify(context.Background(), "login", "tok"), "post-probe calls dial normally")
	assert.Equal(t, requestsAtTrip+2, int(stub.requests.Load()))
}

// TestSiteverifyBreakerCountsDeadlineAsFault pins the outage mode the
// caller-cancellation fix must not hide: the request deadline (which in
// production wiring fires before the client's own timeout) ending every
// call against a provider that accepts connections and never answers.
// Those deadline expiries are faults — the breaker must open and shed the
// load instead of classifying the outage away as caller-side cancels.
func TestSiteverifyBreakerCountsDeadlineAsFault(t *testing.T) {
	stub := newSiteverifyStub(t)
	e := newTestManager(t, false, WithTurnstileEndpoint(stub.server.URL))
	client := e.m.turnstile

	// The provider accepts the request and never answers: every call ends
	// at the caller's deadline.
	stub.hold = make(chan struct{})
	for i := 0; i < siteverifyBreakerThreshold; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		_, err := client.verify(ctx, "secret-key", "tok")
		require.ErrorIs(t, err, context.DeadlineExceeded)
		cancel()
	}
	assert.True(t, client.breakerTripped(), "a hanging provider must open the circuit through its deadline expiries")

	// The open circuit fails fast without another dial.
	requestsAtTrip := int(stub.requests.Load())
	_, err := client.verify(context.Background(), "secret-key", "tok")
	assert.ErrorIs(t, err, errBreakerOpen)
	assert.Equal(t, requestsAtTrip, int(stub.requests.Load()), "an open breaker must not dial the provider")
	close(stub.hold)
}

// TestSiteverifyBreakerAbandonedProbeStaysOpen pins the probe-slot return
// path: a probe whose caller disconnects mid-flight must hand the circuit
// back OPEN (fresh cooldown), never closed — otherwise every concurrent
// caller dials the struggling provider at once, the exact burst the
// single-probe gate exists to prevent.
func TestSiteverifyBreakerAbandonedProbeStaysOpen(t *testing.T) {
	stub := newSiteverifyStub(t)
	e := newTestManager(t, false, WithTurnstileEndpoint(stub.server.URL))
	client := e.m.turnstile
	client.cooldown = 250 * time.Millisecond

	stub.status = http.StatusInternalServerError
	for i := 0; i < siteverifyBreakerThreshold; i++ {
		_, err := client.verify(context.Background(), "secret-key", "tok")
		require.Error(t, err)
	}
	time.Sleep(300 * time.Millisecond)

	// Hold the probe in flight, then disconnect its caller.
	stub.hold = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	probeDone := make(chan error, 1)
	go func() {
		_, err := client.verify(ctx, "secret-key", "tok")
		probeDone <- err
	}()
	require.Eventually(t, func() bool {
		return int(stub.requests.Load()) == siteverifyBreakerThreshold+1
	}, 2*time.Second, 5*time.Millisecond, "the probe must be the one call that dials")
	cancel()
	require.ErrorIs(t, <-probeDone, context.Canceled)

	assert.True(t, client.breakerTripped(), "an abandoned probe must leave the circuit open")
	requestsAfterAbandon := int(stub.requests.Load())
	_, err := client.verify(context.Background(), "secret-key", "tok")
	assert.ErrorIs(t, err, errBreakerOpen, "post-abandon callers must fail fast, not dial concurrently")
	assert.Equal(t, requestsAfterAbandon, int(stub.requests.Load()))
	close(stub.hold)
}

// TestSiteverifyBreakerFailFastCancellationKeepsProbeSlot pins the probe
// slot's ownership: a caller that fails fast on the open circuit and then
// finds its own context cancelled records nothing — it never held the
// slot, so it must not release the in-flight probe's slot either. The
// probe then succeeds and closes the circuit normally.
func TestSiteverifyBreakerFailFastCancellationKeepsProbeSlot(t *testing.T) {
	stub := newSiteverifyStub(t)
	e := newTestManager(t, false, WithTurnstileEndpoint(stub.server.URL))
	client := e.m.turnstile
	client.cooldown = 250 * time.Millisecond

	stub.status = http.StatusInternalServerError
	for i := 0; i < siteverifyBreakerThreshold; i++ {
		_, err := client.verify(context.Background(), "secret-key", "tok")
		require.Error(t, err)
	}
	requestsAtTrip := int(stub.requests.Load())
	time.Sleep(300 * time.Millisecond)

	// The probe dials and hangs; a sibling caller with an already-cancelled
	// context fails fast at the gate.
	stub.status = http.StatusOK
	stub.body = `{"success":true,"action":"login","hostname":"example.com"}`
	stub.hold = make(chan struct{})
	probeDone := make(chan error, 1)
	go func() {
		_, err := client.verify(context.Background(), "secret-key", "tok")
		probeDone <- err
	}()
	require.Eventually(t, func() bool {
		return int(stub.requests.Load()) == requestsAtTrip+1
	}, 2*time.Second, 5*time.Millisecond, "the probe must be the one call that dials")

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := client.verify(cancelled, "secret-key", "tok")
	assert.ErrorIs(t, err, errBreakerOpen, "a cancelled non-holder must fail fast at the gate")
	assert.True(t, client.breakerTripped(), "the fail-fast cancel must not release the probe slot")

	close(stub.hold)
	require.NoError(t, <-probeDone, "the probe's success still closes the circuit")
	_, err = client.verify(context.Background(), "secret-key", "tok")
	require.NoError(t, err, "post-probe calls dial normally")
	assert.Equal(t, requestsAtTrip+2, int(stub.requests.Load()))
}

// TestProviderSpecRegistryRoundTrips pins the registration invariant the
// dispatch collapse rests on: every alias the CLI accepts resolves to a
// registered spec whose defaults carry that provider, and an enum value
// outside the registry fails the lookup (the fail-closed path).
func TestProviderSpecRegistryRoundTrips(t *testing.T) {
	aliases := SupportedProviders()
	require.Contains(t, aliases, "altcha")
	require.Contains(t, aliases, "recaptcha_v3")
	require.Contains(t, aliases, "turnstile")
	for _, alias := range aliases {
		p, err := ParseProvider(alias)
		require.NoError(t, err, "alias %q must parse", alias)
		spec, ok := specFor(p)
		require.True(t, ok, "alias %q has no registered spec", alias)
		assert.Equal(t, alias, spec.alias())
		assert.Equal(t, p, spec.defaults().Provider, "defaults must carry the provider itself")
	}

	// The kebab-case spelling stays accepted for shell ergonomics.
	p, err := ParseProvider("recaptcha-v3")
	require.NoError(t, err)
	assert.Equal(t, ProviderRecaptchaV3, p)

	// An enum value outside the registry (direct SQL on an open proto3
	// enum) fails the lookup, and every caller fails closed on that path.
	_, ok := specFor(Provider(99))
	assert.False(t, ok)
}
