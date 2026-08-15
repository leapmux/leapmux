package captcha

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	altcha "github.com/altcha-org/altcha-lib-go/v2"

	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/metrics"
)

// ErrVerificationFailed is the single error surfaced to clients for every
// rejected submission — missing payload, bad solution, expiry, replay, or a
// tripped honeypot. One message so bots cannot learn which check failed.
var ErrVerificationFailed = errors.New("captcha verification failed")

// errReplayed distinguishes a consumed-salt reuse from other failures for
// the metrics label only. It wraps ErrVerificationFailed so callers keep
// matching the uniform error; clients can never tell a replay apart.
var errReplayed = fmt.Errorf("%w: salt already used", ErrVerificationFailed)

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

// Manager issues and verifies ALTCHA v2 challenges.
//
// Replay protection is store-backed: a consumed salt's row lives until its
// challenge expiry, so single-use holds across restarts and across hub
// instances sharing the database; the cleanup loop purges expired rows.
type Manager struct {
	st   store.Store
	ks   *keystore.Keystore
	solo bool

	mu       sync.Mutex // guards cached/descCached and their timestamps
	cached   *resolvedConfig
	cachedAt time.Time

	descCached     *Config
	descCustomized bool
	descCachedAt   time.Time
}

type resolvedConfig struct {
	cfg    Config
	secret string // hex-encoded HMAC signing secret, decrypted
}

// NewManager creates a captcha manager. In solo mode every check is a
// no-op: solo is a local, single-user deployment with no attack surface.
func NewManager(st store.Store, ks *keystore.Keystore, soloMode bool) *Manager {
	return &Manager{
		st:   st,
		ks:   ks,
		solo: soloMode,
	}
}

// Describe returns the effective configuration without provisioning the
// signing secret — used by GetSystemInfo, which must not write the store
// on a fresh install just to report defaults. The second return reports
// whether a stored row exists (the admin CLI's "customized" flag). Reads
// are served from the same 30-second cache the enforcement path uses, so
// system-info traffic adds no store load of its own.
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

	row, err := m.loadRow(ctx)
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

// ChallengeJSON issues a fresh challenge and returns its JSON — the exact
// interchange format the frontend widget's configure({challenge}) expects.
// The per-challenge KDF is never run here (the solver does the work), so
// issuance costs one HMAC and is safe to expose unauthenticated.
func (m *Manager) ChallengeJSON(ctx context.Context) (string, error) {
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
	expires := time.Now().Add(time.Duration(res.cfg.ChallengeExpirySeconds) * time.Second)
	challenge, err := altcha.CreateChallenge(altcha.CreateChallengeOptions{
		Algorithm:           res.cfg.Algorithm,
		Cost:                int(res.cfg.Cost),
		MemoryCost:          int(res.cfg.MemoryCost),
		Parallelism:         int(res.cfg.Parallelism),
		ExpiresAt:           &expires,
		HMACSignatureSecret: res.secret,
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

// Verify decodes a base64 ALTCHA payload and checks signature, expiry, and
// solution, enforcing single use per salt.
func (m *Manager) Verify(ctx context.Context, payload string) error {
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

	var p altcha.Payload
	if err := decodePayload(payload, &p); err != nil {
		return ErrVerificationFailed
	}

	// The payload's own (signature-covered) parameters select the KDF, not
	// the current stored config — challenges issued just before an admin
	// switches algorithms must still verify.
	deriveKey, ok := deriveKeyFuncs[p.Challenge.Parameters.Algorithm]
	if !ok {
		return ErrVerificationFailed
	}

	result, err := altcha.VerifySolution(altcha.VerifySolutionOptions{
		Challenge:           p.Challenge,
		Solution:            p.Solution,
		DeriveKey:           deriveKey,
		HMACSignatureSecret: res.secret,
	})
	if err != nil || !result.Verified {
		return ErrVerificationFailed
	}

	salt := p.Challenge.Parameters.Salt
	expiresAt := time.Unix(p.Challenge.Parameters.ExpiresAt, 0)
	// Single-use enforcement lives in the store, so it holds across hub
	// restarts and across instances sharing the database. A store failure
	// fails closed with the uniform error, like every other captcha fault.
	consumed, err := m.st.CaptchaConfig().ConsumeCaptchaSalt(ctx, store.ConsumeCaptchaSaltParams{
		Salt:      salt,
		ExpiresAt: expiresAt,
	})
	if err != nil {
		return ErrVerificationFailed
	}
	if consumed == 0 {
		return errReplayed
	}
	return nil
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

// CountResult records a verification outcome for the captcha metrics
// counter; the interceptor calls it alongside its enforcement decision.
func CountResult(result Result) {
	metrics.CaptchaVerificationsTotal.WithLabelValues(string(result)).Inc()
}

// resolve returns the effective config plus decrypted signing secret,
// provisioning the singleton row (and its random secret) on first use.
func (m *Manager) resolve(ctx context.Context) (*resolvedConfig, error) {
	m.mu.Lock()
	if m.cached != nil && time.Since(m.cachedAt) < cacheTTL {
		cached := m.cached
		m.mu.Unlock()
		return cached, nil
	}
	m.mu.Unlock()

	row, err := m.ensureRow(ctx)
	if err != nil {
		return nil, err
	}

	secret, err := m.ks.Decrypt(row.Secret, keystore.CaptchaSecretAAD())
	if err != nil {
		return nil, fmt.Errorf("decrypt captcha secret: %w", err)
	}
	res := &resolvedConfig{
		cfg:    Effective(row),
		secret: hex.EncodeToString(secret),
	}

	m.mu.Lock()
	m.cached, m.cachedAt = res, time.Now()
	m.descCached, m.descCustomized, m.descCachedAt = &res.cfg, true, m.cachedAt
	m.mu.Unlock()
	return res, nil
}

// EnsureProvisioned creates the singleton config row (with a freshly
// generated secret) when absent. The hub does this lazily on first
// challenge issuance; the admin CLI calls it before configuration writes
// so a fresh install's `captcha set` does not have to synthesize
// a secret itself.
func (m *Manager) EnsureProvisioned(ctx context.Context) error {
	if m.solo {
		return nil
	}
	if _, err := m.ensureRow(ctx); err != nil {
		return err
	}
	m.mu.Lock()
	m.cached = nil
	m.descCached = nil
	m.mu.Unlock()
	return nil
}

// provision creates the singleton row with defaults and a fresh random
// secret. The dialects' INSERT ... ON CONFLICT DO NOTHING makes concurrent
// first-use provisioning a race with one winner, so the loser's secret is
// simply discarded.
func (m *Manager) provision(ctx context.Context) error {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return fmt.Errorf("generate captcha secret: %w", err)
	}
	encrypted, err := m.ks.Encrypt(secret, keystore.CaptchaSecretAAD())
	if err != nil {
		return fmt.Errorf("encrypt captcha secret: %w", err)
	}
	def := DefaultConfig()
	err = m.st.CaptchaConfig().Insert(ctx, store.InsertCaptchaConfigParams{
		Enabled:                def.Enabled,
		Algorithm:              def.Algorithm,
		Cost:                   def.Cost,
		MemoryCost:             def.MemoryCost,
		Parallelism:            def.Parallelism,
		ChallengeExpirySeconds: def.ChallengeExpirySeconds,
		Secret:                 encrypted,
	})
	if err != nil {
		return fmt.Errorf("provision captcha config: %w", err)
	}
	return nil
}

// loadRow returns the stored row or nil when absent (defaults apply).
func (m *Manager) loadRow(ctx context.Context) (*store.CaptchaConfig, error) {
	row, err := m.st.CaptchaConfig().Get(ctx)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("load captcha config: %w", err)
	}
	return row, nil
}

// ensureRow returns the singleton config row, provisioning it (with a
// fresh random secret) when absent. resolve and EnsureProvisioned share
// it so the provision-if-absent dance — and the "row missing after
// provisioning" guard — cannot drift between them; Describe keeps using
// loadRow directly because it must not write on a fresh install.
func (m *Manager) ensureRow(ctx context.Context) (*store.CaptchaConfig, error) {
	row, err := m.loadRow(ctx)
	if err != nil {
		return nil, err
	}
	if row != nil {
		return row, nil
	}
	if err := m.provision(ctx); err != nil {
		return nil, err
	}
	row, err = m.loadRow(ctx)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, fmt.Errorf("captcha config row missing after provisioning")
	}
	return row, nil
}

// decodePayload accepts both padded and unpadded standard-alphabet base64,
// the two encodings the widget historically produced across versions.
func decodePayload(payload string, out *altcha.Payload) error {
	b, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		b, err = base64.RawStdEncoding.DecodeString(payload)
		if err != nil {
			return err
		}
	}
	return json.Unmarshal(b, out)
}

// Effective overlays a stored row onto DefaultConfig so a row written by an
// older version (or a partial admin write) can never zero out a
// safety-relevant field with a nonsensical value. The result is validated:
// the CLI validates before writing, so a row that fails here was written
// outside the CLI (direct SQL, a future migration), and the built-in
// defaults keep login working instead of issuing unsolvable challenges.
// The hub and the admin CLI share this one definition of "effective".
func Effective(row *store.CaptchaConfig) Config {
	cfg := DefaultConfig()
	if row == nil {
		return cfg
	}
	cfg.Enabled = row.Enabled
	if row.Algorithm != "" {
		cfg.Algorithm = row.Algorithm
	}
	if row.Cost > 0 {
		cfg.Cost = row.Cost
	}
	if row.MemoryCost > 0 {
		cfg.MemoryCost = row.MemoryCost
	}
	if row.Parallelism > 0 {
		cfg.Parallelism = row.Parallelism
	}
	if row.ChallengeExpirySeconds > 0 {
		cfg.ChallengeExpirySeconds = row.ChallengeExpirySeconds
	}
	if err := cfg.Validate(); err != nil {
		slog.Warn("captcha config row invalid; using built-in defaults", "error", err)
		return DefaultConfig()
	}
	return cfg
}
