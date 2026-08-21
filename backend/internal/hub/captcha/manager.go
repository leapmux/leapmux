package captcha

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	altcha "github.com/altcha-org/altcha-lib-go/v2"

	"github.com/leapmux/leapmux/internal/hub/settings"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/metrics"
)

// ErrVerificationFailed is the single error surfaced to clients for every
// rejected submission — missing payload, bad solution, expiry, replay, a
// low reCAPTCHA score, or a tripped honeypot. One message so bots cannot
// learn which check failed.
var ErrVerificationFailed = errors.New("captcha verification failed")

// errReplayed distinguishes a reused token (an ALTCHA salt, or an
// external provider's timeout-or-duplicate) from other failures for
// the metrics label only. It wraps ErrVerificationFailed so callers keep
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

// Manager issues and verifies captcha challenges for the selected
// provider (ALTCHA by default; reCAPTCHA v3 or Turnstile when the admin
// CLI activates them). Configuration resolves from the shared settings
// snapshot (the captcha.* keys declared in keys.go); the ALTCHA signing
// secret is provisioned into the altcha key's encrypted half on first
// use.
//
// ALTCHA replay protection is store-backed: a consumed salt's row lives
// until its challenge expiry, so single-use holds across restarts and
// across hub instances sharing the database; the cleanup loop purges
// expired rows. The external providers enforce single use at their
// siteverify endpoints and need no local ledger.
type Manager struct {
	st   store.Store // the ALTCHA consumed-salt ledger
	set  *settings.Manager
	solo bool

	recaptcha *siteverifyClient
	turnstile *siteverifyClient

	fallbackMu   sync.Mutex
	lastFallback string // the degrade reason last reported; "" = healthy
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

// NewManager creates a captcha manager over the shared settings snapshot.
// In solo mode every check is a no-op: solo is a local, single-user
// deployment with no attack surface.
func NewManager(st store.Store, set *settings.Manager, soloMode bool, opts ...Option) *Manager {
	m := &Manager{
		st:        st,
		set:       set,
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
// write the store on a fresh install just to report defaults. It cannot
// fail: the snapshot serves the last good state (or defaults) and
// Effective degrades invalid settings to the built-in defaults, so the
// caller has no error path to handle. When the request context carries a
// non-secure client page URL and ALTCHA is selected, Enabled is false at
// runtime only (the captcha.enabled settings row is not written).
func (m *Manager) Describe(ctx context.Context) Config {
	if m.solo {
		return DisabledConfig()
	}
	cfg, _ := Effective(m.set.Snapshot(ctx))
	return applySecureContextGate(cfg, clientPageURLFromCtx(ctx))
}

// ErrProviderNotAltcha is returned by AltchaChallengeJSON when another
// provider is selected: external providers mint their tokens client-side
// and have no challenge to issue. It is an error, not an empty string —
// the empty string means "captcha disabled" and makes the caller's form
// stand down, while a stale altcha widget under an external selection
// must surface as an error state until the denial-driven system-info
// reload mounts the right field.
var ErrProviderNotAltcha = errors.New("captcha challenge unavailable: the selected provider is not altcha")

// AltchaChallengeJSON issues a fresh ALTCHA challenge and returns its
// JSON — the exact interchange format the frontend widget's
// configure({challenge}) expects. The per-challenge KDF is never run
// here (the solver does the work), so issuance costs one HMAC and is
// safe to expose unauthenticated. Empty only when captcha is disabled.
func (m *Manager) AltchaChallengeJSON(ctx context.Context) (string, error) {
	if m.solo {
		return "", nil
	}
	res, err := m.resolve(ctx)
	if err != nil {
		return "", err
	}
	if !res.cfg.Enabled {
		return "", nil
	}
	if res.cfg.Provider != ProviderAltcha {
		return "", fmt.Errorf("%w (%s)", ErrProviderNotAltcha, ProviderAlias(res.cfg.Provider))
	}
	settings := res.cfg.Altcha
	expires := time.Now().Add(time.Duration(settings.ChallengeExpirySeconds) * time.Second)
	challenge, err := altcha.CreateChallenge(altcha.CreateChallengeOptions{
		Algorithm:           settings.Algorithm,
		Cost:                int(settings.Cost),
		MemoryCost:          int(settings.MemoryCost),
		Parallelism:         int(settings.Parallelism),
		ExpiresAt:           &expires,
		HMACSignatureSecret: string(res.secret),
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
// specifies the procedure being protected ("login", "signup",
// "complete_signup"): reCAPTCHA v3 requires verifying it server-side,
// Turnstile echoes it back, and ALTCHA ignores it.
func (m *Manager) Verify(ctx context.Context, action, payload string) error {
	if m.solo {
		return nil
	}
	res, err := m.resolve(ctx)
	if err != nil {
		// A store or keystore outage fails every submission closed; the
		// denial still counts, under the "unknown" provider label, so the
		// outage shows up as failed traffic on the counter instead of as
		// silence.
		m.countUnattributedDenial()
		return fmt.Errorf("captcha verify: %w", err)
	}
	if !res.cfg.Enabled {
		return nil
	}
	// A missing token is a provider-independent failure; one guard here
	// keeps the uniform denial in exactly one place (ALTCHA's decode path
	// produces the same outcome).
	if payload == "" {
		return m.counted(res.cfg.Provider, ResultFailed, ErrVerificationFailed)
	}
	spec, ok := specFor(res.cfg.Provider)
	if !ok {
		// Effective never returns an unknown provider; this arm exists so
		// an enum value without a registered spec fails closed.
		return m.counted(res.cfg.Provider, ResultFailed, ErrVerificationFailed)
	}
	return spec.verify(m, ctx, res, action, payload)
}

// verifyAltcha decodes a base64 ALTCHA payload and checks signature,
// expiry, and solution, enforcing single use per salt. The checks run
// cheapest-first so an unauthenticated flood of garbage payloads dies on
// CPU (the signature-only pre-check) before it can buy store reads, and
// a replay dies on one indexed read before the memory-hard derivation.
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

	// Signature-only pre-check: VerifySolution with no DeriveKey and no
	// key-signature secret runs exactly the expiry integer-compare and the
	// challenge HMAC (microseconds, zero I/O) and never the KDF. Garbage
	// payloads die here; only a payload this hub actually signed proceeds
	// to the store and the derivation below.
	sigOK, err := altcha.VerifySolution(altcha.VerifySolutionOptions{
		Challenge:           p.Challenge,
		Solution:            p.Solution,
		HMACSignatureSecret: string(res.secret),
	})
	if err != nil || !sigOK.Verified {
		return m.counted(ProviderAltcha, ResultFailed, ErrVerificationFailed)
	}

	// The salt ledger answers before the memory-hard derivation runs: a
	// replayed solved payload then costs one indexed read instead of a
	// full KDF re-derivation, and this unauthenticated path cannot turn
	// one solved challenge into unbounded server work at line rate. The
	// lookup is advisory — ConsumeAltchaSalt below stays the single-use
	// authority — so a lookup error falls through to the full checks
	// instead of failing open or closed on its own.
	if used, err := m.st.AltchaSalts().HasAltchaSalt(ctx, p.Challenge.Parameters.Salt); err == nil && used {
		return m.counted(ProviderAltcha, ResultReplayed, errReplayed)
	}

	result, err := altcha.VerifySolution(altcha.VerifySolutionOptions{
		Challenge:           p.Challenge,
		Solution:            p.Solution,
		DeriveKey:           deriveKey,
		HMACSignatureSecret: string(res.secret),
	})
	if err != nil || !result.Verified {
		return m.counted(ProviderAltcha, ResultFailed, ErrVerificationFailed)
	}

	salt := p.Challenge.Parameters.Salt
	expiresAt := time.Unix(p.Challenge.Parameters.ExpiresAt, 0)
	// Single-use enforcement lives in the store, so it holds across hub
	// restarts and across instances sharing the database. A store failure
	// fails closed with the uniform error, like every other captcha fault.
	consumed, err := m.st.AltchaSalts().ConsumeAltchaSalt(ctx, store.ConsumeAltchaSaltParams{
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
// beside the verification outcomes it belongs with.
func (m *Manager) NoteHoneypotDenial(ctx context.Context) {
	m.countResult(m.Describe(ctx).Provider, ResultFailed)
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

// unknownProviderLabel marks a denial whose provider cannot be known
// because the config read itself failed. It keeps the Prometheus label
// set closed: unattributed denials stay distinguishable from every real
// provider's series.
const unknownProviderLabel = "unknown"

// countUnattributedDenial records a fail-closed denial the resolve path
// produced. A store or keystore outage denies every submission; counting
// it keeps the outage visible on the counter instead of as silence.
func (m *Manager) countUnattributedDenial() {
	metrics.CaptchaVerificationsTotal.WithLabelValues(unknownProviderLabel, string(ResultFailed)).Inc()
}

// resolve returns the selected provider's effective config plus its
// decrypted secret, provisioning the default altcha row when altcha is
// selected but has no stored secret (a fresh install, or a row deleted
// outside the admin CLI). The secure-context gate runs after Effective so
// ALTCHA on insecure HTTP stands down without a settings write.
func (m *Manager) resolve(ctx context.Context) (*resolvedConfig, error) {
	snap := m.set.Snapshot(ctx)
	cfg, fallback := Effective(snap)
	m.noteFallback(fallback)
	cfg = applySecureContextGate(cfg, clientPageURLFromCtx(ctx))
	if cfg.Provider != ProviderAltcha {
		return m.resolveSecret(snap, cfg)
	}
	if snap.Customized(AltchaKey) && len(AltchaKey.Of(snap).HMACKey) > 0 {
		return m.resolveSecret(snap, cfg)
	}
	// First-use self-heal: provision the altcha row's signing key (with the
	// row itself, and its default settings, when neither exists) and
	// re-read. A row that exists without a key can only come from a
	// tuning-only `captcha set` on a data dir the hub has never started on
	// — the key is filled without touching the stored tuning.
	if err := provisionAltchaRow(ctx, m.set); err != nil {
		return nil, err
	}
	snap = m.set.Snapshot(ctx)
	cfg, fallback = Effective(snap)
	m.noteFallback(fallback)
	cfg = applySecureContextGate(cfg, clientPageURLFromCtx(ctx))
	return m.resolveSecret(snap, cfg)
}

// noteFallback reports the effective-config degrade state on transition:
// a new fallback warns once (not once per request — resolve runs on every
// protected submission, the exact flood path captcha exists for), and a
// recovery logs at info so the operator sees the bad window close. The
// settings manager applies the same warn-on-transition contract to its
// rows.
func (m *Manager) noteFallback(reason string) {
	m.fallbackMu.Lock()
	prev := m.lastFallback
	if prev == reason {
		m.fallbackMu.Unlock()
		return
	}
	m.lastFallback = reason
	m.fallbackMu.Unlock()
	switch {
	case reason != "":
		slog.Warn("captcha settings degraded; using built-in defaults", "reason", reason)
	default:
		slog.Info("captcha settings recovered")
	}
}

// resolveSecret extracts the selected provider's secret from its stored
// row. An external provider selected without its secret is the
// misconfigured-selection case Effective already fell back on; an altcha
// row without a key can only follow a partial direct-SQL write, and
// failing closed here (the uniform error) is the honest answer.
func (m *Manager) resolveSecret(snap *settings.Snapshot, cfg Config) (*resolvedConfig, error) {
	switch cfg.Provider {
	case ProviderAltcha:
		row := AltchaKey.Of(snap)
		if len(row.HMACKey) == 0 {
			return nil, fmt.Errorf("captcha altcha signing key missing")
		}
		return &resolvedConfig{cfg: cfg, secret: row.HMACKey}, nil
	case ProviderRecaptchaV3:
		row := RecaptchaV3Key.Of(snap)
		if row.SecretKey == "" {
			return nil, fmt.Errorf("captcha recaptcha_v3 api secret missing")
		}
		return &resolvedConfig{cfg: cfg, secret: []byte(row.SecretKey)}, nil
	case ProviderTurnstile:
		row := TurnstileKey.Of(snap)
		if row.SecretKey == "" {
			return nil, fmt.Errorf("captcha turnstile api secret missing")
		}
		return &resolvedConfig{cfg: cfg, secret: []byte(row.SecretKey)}, nil
	default:
		return nil, fmt.Errorf("unsupported captcha provider %v", cfg.Provider)
	}
}

// EnsureProvisioned guarantees that the altcha row exists with its
// generated signing secret. The hub does this at startup so the request
// path never writes: a first Login on a fresh install must not depend on
// a store write completing mid-request. The selection needs no
// provisioning — an absent captcha.selected row IS the default (altcha).
func (m *Manager) EnsureProvisioned(ctx context.Context) error {
	if m.solo {
		return nil
	}
	return provisionAltchaRow(ctx, m.set)
}

// provisionAltchaRow guarantees the altcha settings row exists AND carries
// an HMAC signing key, generating a fresh random key when it must. Two
// paths: no row at all (a fresh install — SetIfAbsent makes racing first
// uses a one-winner race, so the loser's key is simply discarded) or a row
// without a key (a tuning-only `captcha set` on a data dir the hub has
// never started on — a partial-document update fills the key and preserves
// the stored tuning).
func provisionAltchaRow(ctx context.Context, set *settings.Manager) error {
	snap := set.Snapshot(ctx)
	if snap.Customized(AltchaKey) && len(AltchaKey.Of(snap).HMACKey) > 0 {
		return nil
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return fmt.Errorf("generate captcha secret: %w", err)
	}
	if snap.Customized(AltchaKey) {
		keyDoc, err := json.Marshal(map[string]any{"hmac_key": secret})
		if err != nil {
			return fmt.Errorf("marshal captcha secret update: %w", err)
		}
		if err := set.Update(ctx, AltchaKey, keyDoc); err != nil {
			return fmt.Errorf("provision captcha signing key: %w", err)
		}
		return nil
	}
	row := defaultAltchaRow()
	row.HMACKey = secret
	if err := AltchaKey.SetIfAbsent(ctx, set, row); err != nil {
		return fmt.Errorf("provision captcha config: %w", err)
	}
	return nil
}

// DescribeProvider returns one provider's effective settings from its own
// key — not the selection. The admin CLI overlays flag edits onto this
// base, so a switch back to a provider keeps that row's stored tuning
// (only the selection changes), and a key the CLI has never written
// reports that provider's defaults rather than altcha's.
func DescribeProvider(s *settings.Snapshot, provider Provider) Config {
	spec, ok := specFor(provider)
	if !ok {
		return DefaultConfig()
	}
	switch provider {
	case ProviderAltcha:
		row := AltchaKey.Of(s)
		return Config{Provider: provider, Enabled: CaptchaEnabledKey.Of(s), Altcha: &row.AltchaSettings}
	case ProviderRecaptchaV3:
		row := RecaptchaV3Key.Of(s)
		return Config{Provider: provider, Enabled: CaptchaEnabledKey.Of(s), RecaptchaV3: &RecaptchaV3Settings{SiteKey: row.SiteKey, MinScore: row.MinScore}}
	case ProviderTurnstile:
		row := TurnstileKey.Of(s)
		return Config{Provider: provider, Enabled: CaptchaEnabledKey.Of(s), Turnstile: &TurnstileSettings{SiteKey: row.SiteKey}}
	default:
		return spec.defaults()
	}
}
