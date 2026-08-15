package captcha

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	altcha "github.com/altcha-org/altcha-lib-go/v2"

	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/metrics"
)

// ErrVerificationFailed is the single error surfaced to clients for every
// rejected submission — missing payload, bad solution, expiry, replay, a
// low reCAPTCHA score, or a tripped honeypot. One message so bots cannot
// learn which check failed.
var ErrVerificationFailed = errors.New("captcha verification failed")

// errReplayed distinguishes a reused token (an ALTCHA salt, or an
// external provider's timeout-or-duplicate) from other failures for the
// metrics label only. It wraps ErrVerificationFailed so callers keep
// matching the uniform error; clients can never tell a replay apart.
var errReplayed = fmt.Errorf("%w: token or salt already used", ErrVerificationFailed)

// Result classifies a verification outcome for the metrics counter. The
// typed constants keep the Prometheus label set closed: a typo'd string
// would silently mint a fourth series.
type Result string

const (
	ResultPassed   Result = "passed"
	ResultFailed   Result = "failed"
	ResultReplayed Result = "replayed"
)

// cacheTTL limits how long a store read (and the decrypted secret) is
// reused, mirroring the auth interceptor's session cache. It also limits
// how long an admin CLI change takes to propagate to a running hub.
const cacheTTL = 30 * time.Second

// Manager issues and verifies captcha challenges for the selected
// provider (ALTCHA by default; reCAPTCHA v3 or Turnstile when the admin
// CLI activates them).
//
// ALTCHA replay protection is store-backed: a consumed salt's row lives
// until its challenge expiry, so single-use holds across restarts and
// across hub instances sharing the database; the cleanup loop purges
// expired rows. The external providers enforce single use at their
// siteverify endpoints and need no local ledger.
type Manager struct {
	st   store.Store
	ks   *keystore.Keystore
	solo bool

	recaptcha *siteverifyClient
	turnstile *siteverifyClient

	mu       sync.Mutex // guards cached/descCached and their timestamps
	cached   *resolvedConfig
	cachedAt time.Time

	descCached     *Config
	descCustomized bool
	descCachedAt   time.Time
}

type resolvedConfig struct {
	cfg    Config
	secret []byte // decrypted provider secret: ALTCHA HMAC key or siteverify API secret
}

// Option configures a Manager beyond its production defaults.
type Option func(*Manager)

// WithRecaptchaEndpoint overrides the reCAPTCHA siteverify endpoint.
// Production uses Google's fixed URL; tests point it at a local stub.
func WithRecaptchaEndpoint(endpoint string) Option {
	return func(m *Manager) { m.recaptcha = newSiteverifyClient(ProviderRecaptchaV3, endpoint) }
}

// WithTurnstileEndpoint overrides the Turnstile siteverify endpoint.
// Production uses Cloudflare's fixed URL; tests point it at a local stub.
func WithTurnstileEndpoint(endpoint string) Option {
	return func(m *Manager) { m.turnstile = newSiteverifyClient(ProviderTurnstile, endpoint) }
}

// NewManager creates a captcha manager. In solo mode every check is a
// no-op: solo is a local, single-user deployment with no attack surface.
func NewManager(st store.Store, ks *keystore.Keystore, soloMode bool, opts ...Option) *Manager {
	m := &Manager{
		st:        st,
		ks:        ks,
		solo:      soloMode,
		recaptcha: newSiteverifyClient(ProviderRecaptchaV3, recaptchaVerifyURL),
		turnstile: newSiteverifyClient(ProviderTurnstile, turnstileVerifyURL),
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// Describe returns the selected provider's effective configuration
// without provisioning anything — used by GetSystemInfo, which must not
// write the store on a fresh install just to report defaults. The second
// return reports whether a selected row exists (the admin CLI's
// "customized" flag). Reads are served from the same 30-second cache the
// enforcement path uses, so system-info traffic adds no store load of
// its own.
func (m *Manager) Describe(ctx context.Context) (Config, bool, error) {
	if m.solo {
		cfg := DefaultConfig()
		cfg.Enabled = false
		return cfg, false, nil
	}
	m.mu.Lock()
	if m.descCached != nil && time.Since(m.descCachedAt) < cacheTTL {
		cfg, customized := *m.descCached, m.descCustomized
		m.mu.Unlock()
		return cfg, customized, nil
	}
	m.mu.Unlock()

	row, err := m.loadSelectedRow(ctx)
	if err != nil {
		return DefaultConfig(), false, err
	}
	customized := row != nil
	cfg := Effective(row)

	m.mu.Lock()
	m.descCached, m.descCustomized, m.descCachedAt = &cfg, customized, time.Now()
	m.mu.Unlock()
	return cfg, customized, nil
}

// AltchaChallengeJSON issues a fresh ALTCHA challenge and returns its
// JSON — the exact interchange format the frontend widget's
// configure({challenge}) expects. The per-challenge KDF is never run
// here (the solver does the work), so issuance costs one HMAC and is
// safe to expose unauthenticated. Empty when captcha is disabled or
// another provider is selected: external providers mint their tokens
// client-side and have nothing to issue.
func (m *Manager) AltchaChallengeJSON(ctx context.Context) (string, error) {
	if m.solo {
		return "", nil
	}
	res, err := m.resolve(ctx)
	if err != nil {
		return "", err
	}
	if !res.cfg.Enabled || res.cfg.Provider != ProviderAltcha {
		return "", nil
	}
	settings := res.cfg.Altcha
	expires := time.Now().Add(time.Duration(settings.ChallengeExpirySeconds) * time.Second)
	challenge, err := altcha.CreateChallenge(altcha.CreateChallengeOptions{
		Algorithm:           settings.Algorithm,
		Cost:                int(settings.Cost),
		MemoryCost:          int(settings.MemoryCost),
		Parallelism:         int(settings.Parallelism),
		ExpiresAt:           &expires,
		HMACSignatureSecret: hex.EncodeToString(res.secret),
	})
	if err != nil {
		return "", fmt.Errorf("create captcha challenge: %w", err)
	}
	b, err := json.Marshal(challenge)
	if err != nil {
		return "", fmt.Errorf("marshal captcha challenge: %w", err)
	}
	return string(b), nil
}

// Verify checks a provider token minted under the given action and
// enforces single use, dispatching on the selected provider. The action
// names the procedure being protected ("login", "signup",
// "complete_signup"): reCAPTCHA v3 requires verifying it server-side,
// Turnstile echoes it back, and ALTCHA ignores it.
func (m *Manager) Verify(ctx context.Context, action, payload string) error {
	if m.solo {
		return nil
	}
	res, err := m.resolve(ctx)
	if err != nil {
		return fmt.Errorf("captcha verify: %w", err)
	}
	if !res.cfg.Enabled {
		return nil
	}
	switch res.cfg.Provider {
	case ProviderAltcha:
		return m.verifyAltcha(ctx, res, payload)
	case ProviderRecaptchaV3:
		if payload == "" {
			return m.counted(ProviderRecaptchaV3, ResultFailed, ErrVerificationFailed)
		}
		return m.verifyRecaptcha(ctx, string(res.secret), payload, action, res.cfg.RecaptchaV3.MinScore)
	case ProviderTurnstile:
		if payload == "" {
			return m.counted(ProviderTurnstile, ResultFailed, ErrVerificationFailed)
		}
		return m.verifyTurnstile(ctx, string(res.secret), payload, action)
	default:
		// Effective never returns an unknown provider; this arm exists so
		// a future registry entry without a dispatch case fails closed.
		return m.counted(res.cfg.Provider, ResultFailed, ErrVerificationFailed)
	}
}

// verifyAltcha decodes a base64 ALTCHA payload and checks signature,
// expiry, and solution, enforcing single use per salt.
func (m *Manager) verifyAltcha(ctx context.Context, res *resolvedConfig, payload string) error {
	var p altcha.Payload
	if err := decodeAltchaPayload(payload, &p); err != nil {
		return m.counted(ProviderAltcha, ResultFailed, ErrVerificationFailed)
	}

	// The payload's own (signature-covered) parameters select the KDF, not
	// the current stored config — challenges issued just before an admin
	// switches algorithms must still verify.
	deriveKey, ok := deriveKeyFuncs[p.Challenge.Parameters.Algorithm]
	if !ok {
		return m.counted(ProviderAltcha, ResultFailed, ErrVerificationFailed)
	}

	result, err := altcha.VerifySolution(altcha.VerifySolutionOptions{
		Challenge:           p.Challenge,
		Solution:            p.Solution,
		DeriveKey:           deriveKey,
		HMACSignatureSecret: hex.EncodeToString(res.secret),
	})
	if err != nil || !result.Verified {
		return m.counted(ProviderAltcha, ResultFailed, ErrVerificationFailed)
	}

	salt := p.Challenge.Parameters.Salt
	expiresAt := time.Unix(p.Challenge.Parameters.ExpiresAt, 0)
	// Single-use enforcement lives in the store, so it holds across hub
	// restarts and across instances sharing the database. A store failure
	// fails closed with the uniform error, like every other captcha fault.
	consumed, err := m.st.CaptchaConfig().ConsumeAltchaSalt(ctx, store.ConsumeAltchaSaltParams{
		Salt:      salt,
		ExpiresAt: expiresAt,
	})
	if err != nil {
		return m.counted(ProviderAltcha, ResultFailed, ErrVerificationFailed)
	}
	if consumed == 0 {
		return m.counted(ProviderAltcha, ResultReplayed, errReplayed)
	}
	return m.counted(ProviderAltcha, ResultPassed, nil)
}

// Enabled reports whether submissions must carry captcha payloads. Store
// errors fail closed with the uniform client-facing error; an unreachable
// store also makes the subsequent login lookup fail anyway.
func (m *Manager) Enabled(ctx context.Context) bool {
	if m.solo {
		return false
	}
	res, err := m.resolve(ctx)
	if err != nil {
		return true
	}
	return res.cfg.Enabled
}

// NoteHoneypotDenial records a honeypot trip under the selected
// provider's metric label. The honeypot check itself is provider-agnostic
// and runs even when captcha is disabled; the label keeps the denial
// beside the verification outcomes it belongs with. A config read that
// fails drops only the metric — the denial itself already happened.
func (m *Manager) NoteHoneypotDenial(ctx context.Context) {
	cfg, _, err := m.Describe(ctx)
	if err != nil {
		return
	}
	m.countResult(cfg.Provider, ResultFailed)
}

// counted records a verification outcome and returns err, pairing the
// metric label with the decision it describes at every return site.
func (m *Manager) counted(provider Provider, result Result, err error) error {
	m.countResult(provider, result)
	return err
}

func (m *Manager) countResult(provider Provider, result Result) {
	metrics.CaptchaVerificationsTotal.WithLabelValues(ProviderAlias(provider), string(result)).Inc()
}

// resolve returns the selected provider's effective config plus its
// decrypted secret, provisioning the default altcha row when no row is
// selected (a fresh install, or a reset whose self-heal has not run yet).
func (m *Manager) resolve(ctx context.Context) (*resolvedConfig, error) {
	m.mu.Lock()
	if m.cached != nil && time.Since(m.cachedAt) < cacheTTL {
		cached := m.cached
		m.mu.Unlock()
		return cached, nil
	}
	m.mu.Unlock()

	row, err := m.ensureSelectedRow(ctx)
	if err != nil {
		return nil, err
	}

	secret, err := m.ks.Decrypt(row.Secret, keystore.CaptchaSecretAAD(ProviderAlias(row.Provider)))
	if err != nil {
		return nil, fmt.Errorf("decrypt captcha secret: %w", err)
	}
	res := &resolvedConfig{
		cfg:    Effective(row),
		secret: secret,
	}

	m.mu.Lock()
	m.cached, m.cachedAt = res, time.Now()
	m.descCached, m.descCustomized, m.descCachedAt = &res.cfg, true, m.cachedAt
	m.mu.Unlock()
	return res, nil
}

// EnsureProvisioned guarantees that some provider row is selected,
// provisioning and activating the default altcha row when none is. The
// hub does this lazily on first enforcement; the admin CLI calls it
// before enable/disable and settings writes so those always act on a
// row that exists.
func (m *Manager) EnsureProvisioned(ctx context.Context) error {
	if m.solo {
		return nil
	}
	if _, err := m.ensureSelectedRow(ctx); err != nil {
		return err
	}
	m.mu.Lock()
	m.cached = nil
	m.descCached = nil
	m.mu.Unlock()
	return nil
}

// EnsureAltchaRow guarantees the altcha row exists with a generated
// signing secret, without touching the selection. The admin CLI calls it
// before activating altcha, so a switch back from an external provider
// reuses the altcha row's original secret rather than landing on a
// missing row (which the hub would self-heal only by regenerating it).
func (m *Manager) EnsureAltchaRow(ctx context.Context) error {
	if m.solo {
		return nil
	}
	row, err := m.loadRow(ctx, ProviderAltcha)
	if err != nil {
		return err
	}
	if row != nil {
		return nil
	}
	return m.insertAltchaRow(ctx)
}

// EncryptSecret encrypts a provider secret for storage, with the same
// provider-scoped AAD the resolver decrypts with. The admin CLI is the
// only caller: it is the only writer of external providers' secrets.
func (m *Manager) EncryptSecret(provider Provider, secret string) ([]byte, error) {
	encrypted, err := m.ks.Encrypt([]byte(secret), keystore.CaptchaSecretAAD(ProviderAlias(provider)))
	if err != nil {
		return nil, fmt.Errorf("encrypt captcha secret: %w", err)
	}
	return encrypted, nil
}

// insertAltchaRow creates the altcha row (unselected, fresh random HMAC
// secret, default settings). The dialects' INSERT ... ON CONFLICT DO
// NOTHING makes concurrent first-use provisioning a race with one winner,
// so the loser's secret is simply discarded.
func (m *Manager) insertAltchaRow(ctx context.Context) error {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return fmt.Errorf("generate captcha secret: %w", err)
	}
	encrypted, err := m.ks.Encrypt(secret, keystore.CaptchaSecretAAD(ProviderAlias(ProviderAltcha)))
	if err != nil {
		return fmt.Errorf("encrypt captcha secret: %w", err)
	}
	settings, err := json.Marshal(DefaultAltchaSettings())
	if err != nil {
		return fmt.Errorf("marshal altcha settings: %w", err)
	}
	if err := m.st.CaptchaConfig().InsertIfAbsent(ctx, store.InsertCaptchaConfigIfAbsentParams{
		Provider: ProviderAltcha,
		Secret:   encrypted,
		Settings: string(settings),
	}); err != nil {
		return fmt.Errorf("provision captcha config: %w", err)
	}
	return nil
}

// ensureSelectedRow returns the selected config row, provisioning and
// activating the default altcha row when none is selected.
func (m *Manager) ensureSelectedRow(ctx context.Context) (*store.CaptchaConfig, error) {
	row, err := m.loadSelectedRow(ctx)
	if err != nil {
		return nil, err
	}
	if row != nil {
		return row, nil
	}
	if err := m.insertAltchaRow(ctx); err != nil {
		return nil, err
	}
	if err := m.st.CaptchaConfig().Activate(ctx, ProviderAltcha); err != nil {
		return nil, fmt.Errorf("activate captcha config: %w", err)
	}
	row, err = m.loadSelectedRow(ctx)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, fmt.Errorf("selected captcha row missing after provisioning")
	}
	return row, nil
}

// loadSelectedRow returns the selected row or nil when none exists
// (defaults apply, provisioning self-heals on the next resolve).
func (m *Manager) loadSelectedRow(ctx context.Context) (*store.CaptchaConfig, error) {
	row, err := m.st.CaptchaConfig().GetSelected(ctx)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("load captcha config: %w", err)
	}
	return row, nil
}

// loadRow returns one provider's row or nil when it has no row yet.
func (m *Manager) loadRow(ctx context.Context, provider Provider) (*store.CaptchaConfig, error) {
	row, err := m.st.CaptchaConfig().Get(ctx, provider)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("load captcha config: %w", err)
	}
	return row, nil
}
