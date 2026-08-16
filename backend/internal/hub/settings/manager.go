package settings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/hub/store"
)

// cacheTTL bounds how long a decoded snapshot is reused, mirroring the
// session and captcha caches. It is also the bound on how long an admin
// CLI write takes to reach a running hub (and the convergence bound
// between hub instances sharing one database).
const cacheTTL = 30 * time.Second

// Snapshot is an immutable, decoded view of every registered key: the
// effective value (stored row merged onto the default), whether a row
// exists, and the row's last-write time. Values are read through their
// typed keys (Key.Of), never through the map directly.
type Snapshot struct {
	at time.Time
	// values holds one decoded value per registered key.
	values map[string]any
	// customized records which keys have a stored row (the admin CLI's
	// "default vs customized" marker).
	customized map[string]bool
	// updatedAt records each row's last-write time (zero when absent).
	updatedAt map[string]time.Time
	// canon is the canonical encoding of values+customized, used to detect
	// effective changes for subscribers.
	canon string
}

// ValueOf returns the key's effective value, or nil when the key was
// never registered with the manager that produced this snapshot.
func (s *Snapshot) ValueOf(desc Descriptor) any {
	return s.values[desc.Name()]
}

// Customized reports whether the key has a stored row.
func (s *Snapshot) Customized(desc Descriptor) bool {
	return s.customized[desc.Name()]
}

// UpdatedAt returns the key's row last-write time (zero when no row).
func (s *Snapshot) UpdatedAt(desc Descriptor) time.Time {
	return s.updatedAt[desc.Name()]
}

// At returns when the snapshot was loaded.
func (s *Snapshot) At() time.Time { return s.at }

// Manager owns the hub_settings table: it decodes rows into typed
// snapshots (TTL-cached, singleflighted), and provides the validated,
// transactional write path the admin CLI and the captcha provisioning
// share. It is the single process-wide settings authority — consumers
// read snapshots through their Key handles and never touch the store.
type Manager struct {
	st  store.Store
	ks  *keystore.Keystore
	now func() time.Time
	ttl time.Duration

	byName map[string]Descriptor
	names  []string // registration order, for stable listing
	cross  []func(*Snapshot) error

	mu     sync.Mutex
	snap   *Snapshot
	subs   []func(*Snapshot)
	warned map[string]bool
	flight singleflight.Group
}

// Option configures a Manager beyond its production defaults.
type Option func(*Manager)

// WithTTL overrides the snapshot TTL (tests shrink it to observe
// propagation).
func WithTTL(d time.Duration) Option {
	return func(m *Manager) { m.ttl = d }
}

// WithNow overrides the clock (tests freeze it).
func WithNow(fn func() time.Time) Option {
	return func(m *Manager) { m.now = fn }
}

// WithCrossValidation attaches rules that span keys (e.g. "email
// verification requires SMTP"). They run against a candidate snapshot on
// every write, so an impossible combination is rejected before it is
// stored rather than degraded after.
func WithCrossValidation(rules ...func(*Snapshot) error) Option {
	return func(m *Manager) { m.cross = append(m.cross, rules...) }
}

// NewManager creates a settings manager over the store. The keystore
// encrypts secret halves; a nil keystore makes every secret-bearing key
// degrade to its default (tests that do not exercise secrets).
func NewManager(st store.Store, ks *keystore.Keystore, descs []Descriptor, opts ...Option) *Manager {
	m := &Manager{
		st:     st,
		ks:     ks,
		now:    time.Now,
		ttl:    cacheTTL,
		byName: make(map[string]Descriptor, len(descs)),
		warned: make(map[string]bool),
	}
	for _, d := range descs {
		if _, dup := m.byName[d.Name()]; dup {
			panic(fmt.Sprintf("settings: duplicate key %q", d.Name()))
		}
		m.byName[d.Name()] = d
		m.names = append(m.names, d.Name())
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// Registered lists the manager's descriptors in registration order.
func (m *Manager) Registered() []Descriptor {
	out := make([]Descriptor, 0, len(m.names))
	for _, name := range m.names {
		out = append(out, m.byName[name])
	}
	return out
}

// Descriptor returns the registered descriptor with the given key name.
func (m *Manager) Descriptor(name string) (Descriptor, bool) {
	d, ok := m.byName[name]
	return d, ok
}

// Load performs the initial synchronous snapshot. The hub calls it at
// startup so a broken store fails startup (and restart-class values are
// available before any pool is constructed) instead of degrading every
// request.
func (m *Manager) Load(ctx context.Context) error {
	s, err := m.refresh(ctx)
	if err != nil {
		return err
	}
	m.publish(s)
	return nil
}

// Snapshot returns the current decoded view, refreshing it when the TTL
// has expired. A refresh failure logs and returns the last good snapshot:
// a transient store outage must not turn every request that happens to
// trip the TTL into an error. Before Load it returns a defaults-only
// snapshot rather than nil, so consumers always have a usable value.
func (m *Manager) Snapshot(ctx context.Context) *Snapshot {
	m.mu.Lock()
	if m.snap != nil && m.now().Sub(m.snap.at) < m.ttl {
		s := m.snap
		m.mu.Unlock()
		return s
	}
	m.mu.Unlock()

	v, err, _ := m.flight.Do("refresh", func() (any, error) {
		// Re-check under the flight: the burst that gathered at the TTL
		// expiry shares one load, and a later arrival finds it fresh.
		m.mu.Lock()
		if m.snap != nil && m.now().Sub(m.snap.at) < m.ttl {
			s := m.snap
			m.mu.Unlock()
			return s, nil
		}
		m.mu.Unlock()
		return m.refresh(context.WithoutCancel(ctx))
	})
	if err != nil {
		slog.Warn("settings snapshot refresh failed; serving last good snapshot", "error", err)
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.snap != nil {
			return m.snap
		}
		return defaultsSnapshot(m)
	}
	s := v.(*Snapshot)
	m.publish(s)
	return s
}

// Subscribe registers a callback fired whenever an effective snapshot
// change is published (initial load, TTL refresh, or a write through this
// manager). Callbacks run on the publishing goroutine, outside the
// manager lock, and must not call back into Snapshot (they receive the
// new snapshot directly).
func (m *Manager) Subscribe(fn func(*Snapshot)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.subs = append(m.subs, fn)
}

// publish stores the snapshot (computing its canonical form first) and
// fires subscribers when it differs from the previous one.
func (m *Manager) publish(s *Snapshot) {
	m.mu.Lock()
	prev := m.snap
	m.snap = s
	var subs []func(*Snapshot)
	subs = append(subs, m.subs...)
	m.mu.Unlock()
	if prev == nil || prev.canon != s.canon {
		for _, fn := range subs {
			fn(s)
		}
	}
}

// refresh reads every row, decrypts secrets, decodes each registered key
// onto its default, and degrades bad rows to defaults with a warning.
func (m *Manager) refresh(ctx context.Context) (*Snapshot, error) {
	rows, err := m.st.Settings().GetAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("load settings: %w", err)
	}
	return m.buildSnapshot(rows, "", nil), nil
}

// buildSnapshot assembles a snapshot from store rows. overrideName /
// overrideValue substitute one key's decoded value (the write path's
// candidate), letting cross-key rules validate the post-write state.
func (m *Manager) buildSnapshot(rows []store.SettingRow, overrideName string, overrideValue any) *Snapshot {
	s := &Snapshot{
		at:         m.now(),
		values:     make(map[string]any, len(m.byName)),
		customized: make(map[string]bool, len(rows)),
		updatedAt:  make(map[string]time.Time, len(rows)),
	}
	stored := make(map[string]store.SettingRow, len(rows))
	for _, row := range rows {
		stored[row.Key] = row
	}
	for _, name := range m.names {
		desc := m.byName[name]
		if name == overrideName {
			s.values[name] = overrideValue
			s.customized[name] = true
			continue
		}
		row, has := stored[name]
		var decoded Row
		if has {
			s.customized[name] = true
			s.updatedAt[name] = row.UpdatedAt
			decoded.Value = json.RawMessage(ptrBytes(row.Value))
			if len(row.Secret) > 0 && m.ks != nil {
				plain, err := m.ks.Decrypt(row.Secret, keystore.SettingsSecretAAD(name))
				if err != nil {
					// A secret that no longer decrypts (key removed from
					// the ring) must not brick the whole key: decode the
					// public half and leave the secret at its default.
					m.warnOnce("secret:"+name,
						"settings secret failed to decrypt; using default for the secret half",
						"key", name, "error", err)
				} else {
					decoded.Secret = plain
				}
			}
		}
		v, err := desc.Decode(decoded)
		if err != nil {
			m.warnOnce("decode:"+name, "settings row undecodable; using default", "key", name, "error", err)
			v = desc.Default()
		} else if err := desc.Validate(v); err != nil {
			// The write path validates; a row that fails here was written
			// outside it (direct SQL). Degrade to the default so a bad row
			// can never take the hub down — the write path's rejection is
			// the real guard.
			m.warnOnce("invalid:"+name, "settings row invalid; using default", "key", name, "error", err)
			v = desc.Default()
		}
		s.values[name] = v
	}
	for key := range stored {
		if _, known := m.byName[key]; !known {
			// A row for a key no longer registered (setting removed, or a
			// hub version that never knew it). Harmless: nobody reads it.
			m.warnOnce("orphan:"+key, "settings row for unknown key ignored", "key", key)
		}
	}
	if b, err := json.Marshal([]any{s.values, s.customized}); err == nil {
		s.canon = string(b)
	}
	return s
}

// warnOnce logs at warn level the first time tag is seen, so a persistently
// bad row does not spam the log on every refresh.
func (m *Manager) warnOnce(tag, msg string, args ...any) {
	m.mu.Lock()
	first := !m.warned[tag]
	if first {
		m.warned[tag] = true
	}
	m.mu.Unlock()
	if first {
		slog.Warn(msg, args...)
	}
}

// Update merges a partial JSON document onto the key's current value and
// writes it. Fields the document omits keep their current (or default)
// values, so `{"port": 465}` retunes one SMTP field without restating
// the host. Validation — the key's own rules plus cross-key rules — runs
// against the post-write view before anything is stored.
func (m *Manager) Update(ctx context.Context, desc Descriptor, partial json.RawMessage) error {
	return m.apply(ctx, desc, partial)
}

// UpdateSecret merges a partial JSON document onto the key's secret half.
// Mechanically identical to Update — the named fields simply belong to
// the encrypted half — but a separate verb keeps the admin CLI's intent
// explicit and lets the write path require the key to be secret-bearing.
func (m *Manager) UpdateSecret(ctx context.Context, desc Descriptor, partial json.RawMessage) error {
	if !desc.HasSecret() {
		return fmt.Errorf("settings key %q has no secret fields", desc.Name())
	}
	return m.apply(ctx, desc, partial)
}

// SetValue writes a fully-merged typed value (wrapper helpers and the
// captcha provisioning use this when they hold the complete value).
func (m *Manager) SetValue(ctx context.Context, desc Descriptor, v any) error {
	if err := desc.Validate(v); err != nil {
		return fmt.Errorf("invalid value for %q: %w", desc.Name(), err)
	}
	public, secret, err := desc.Split(v)
	if err != nil {
		return err
	}
	return m.writeHalves(ctx, desc, public, secret, v)
}

// SetIfAbsent writes the value only when the key has no row, making
// first-use provisioning a one-winner race: racing provisioners each
// generate a value, the transaction serializes them, and the loser's
// value is discarded rather than overwriting the winner's (a mid-flight
// challenge signed with the loser's key must not be invalidated by the
// race itself).
func (m *Manager) SetIfAbsent(ctx context.Context, desc Descriptor, v any) error {
	err := m.st.RunInTransaction(ctx, func(tx store.Store) error {
		row, err := loadRow(ctx, tx, desc.Name())
		if err != nil {
			return err
		}
		if len(row.Value) > 0 || len(row.Secret) > 0 {
			return nil
		}
		return m.validateAndWrite(ctx, tx, desc, v)
	})
	if err != nil {
		return err
	}
	m.publish(m.buildSnapshotFromStore(ctx))
	return nil
}

// Reset removes the key's row, returning it to its code default.
func (m *Manager) Reset(ctx context.Context, desc Descriptor) error {
	if err := m.st.Settings().Delete(ctx, desc.Name()); err != nil {
		return fmt.Errorf("reset setting %q: %w", desc.Name(), err)
	}
	s := m.buildSnapshotFromStore(ctx)
	m.publish(s)
	return nil
}

// apply is the shared Update/UpdateSecret path: inside one transaction,
// read the current row, decode the merged value, overlay the partial
// document, validate against the post-write view, then split and write.
func (m *Manager) apply(ctx context.Context, desc Descriptor, partial json.RawMessage) error {
	if _, ok := m.byName[desc.Name()]; !ok {
		return fmt.Errorf("settings key %q is not registered", desc.Name())
	}
	err := m.st.RunInTransaction(ctx, func(tx store.Store) error {
		row, err := loadRow(ctx, tx, desc.Name())
		if err != nil {
			return err
		}
		if len(row.Secret) > 0 && m.ks != nil {
			if plain, derr := m.ks.Decrypt(row.Secret, keystore.SettingsSecretAAD(desc.Name())); derr == nil {
				row.Secret = json.RawMessage(plain)
			} else {
				// The existing secret no longer decrypts; treat it as
				// absent so the write replaces rather than corrupts it.
				row.Secret = nil
			}
		} else {
			row.Secret = nil
		}
		merged, err := desc.Decode(row)
		if err != nil {
			return fmt.Errorf("decode current value of %q: %w", desc.Name(), err)
		}
		if len(partial) > 0 {
			merged, err = desc.ApplyPartial(merged, partial)
			if err != nil {
				return fmt.Errorf("merge partial document for %q: %w", desc.Name(), err)
			}
		}
		return m.validateAndWrite(ctx, tx, desc, merged)
	})
	if err != nil {
		return err
	}
	m.publish(m.buildSnapshotFromStore(ctx))
	return nil
}

// validateAndWrite is the shared tail of both write paths: per-key
// validation, cross-key validation against the post-write view, then the
// split-encrypt-upsert.
func (m *Manager) validateAndWrite(ctx context.Context, tx store.Store, desc Descriptor, merged any) error {
	if err := desc.Validate(merged); err != nil {
		return fmt.Errorf("invalid value for %q: %w", desc.Name(), err)
	}
	rows, err := tx.Settings().GetAll(ctx)
	if err != nil {
		return fmt.Errorf("load settings for cross validation: %w", err)
	}
	candidate := m.buildSnapshot(rows, desc.Name(), merged)
	for _, rule := range m.cross {
		if err := rule(candidate); err != nil {
			return fmt.Errorf("cross-validation failed: %w", err)
		}
	}
	public, secret, err := desc.Split(merged)
	if err != nil {
		return err
	}
	return m.upsertHalves(ctx, tx, desc, public, secret)
}

// writeHalves is SetValue's path: the caller already holds the fully
// merged value (and may have validated it); validation and cross rules
// still run here so no write path can skip them.
func (m *Manager) writeHalves(ctx context.Context, desc Descriptor, public, secret json.RawMessage, v any) error {
	if err := m.st.RunInTransaction(ctx, func(tx store.Store) error {
		return m.validateAndWrite(ctx, tx, desc, v)
	}); err != nil {
		return err
	}
	m.publish(m.buildSnapshotFromStore(ctx))
	return nil
}

// upsertHalves encrypts the secret half (when the key has one and a
// keystore is available) and writes the row. A secret-bearing key with no
// keystore refuses to write its secret half rather than storing it in the
// clear: the public half still lands, and the missing keystore is a bug
// in the caller's wiring.
func (m *Manager) upsertHalves(ctx context.Context, tx store.Store, desc Descriptor, public, secret json.RawMessage) error {
	var encrypted []byte
	if len(secret) > 0 {
		if m.ks == nil {
			return fmt.Errorf("settings key %q carries a secret but no keystore is configured", desc.Name())
		}
		enc, err := m.ks.Encrypt(secret, keystore.SettingsSecretAAD(desc.Name()))
		if err != nil {
			return fmt.Errorf("encrypt secret half of %q: %w", desc.Name(), err)
		}
		encrypted = enc
	}
	p := store.UpsertSettingParams{Key: desc.Name()}
	if len(public) > 0 {
		s := string(public)
		p.Value = &s
	}
	p.Secret = encrypted
	if p.Value == nil && p.Secret == nil {
		return fmt.Errorf("settings key %q would write an empty row", desc.Name())
	}
	if err := tx.Settings().Upsert(ctx, p); err != nil {
		return fmt.Errorf("upsert setting %q: %w", desc.Name(), err)
	}
	return nil
}

// buildSnapshotFromStore refreshes from the store and is best-effort: the
// write already committed, so a read failure logs and falls back to the
// last published snapshot (the TTL refresh will converge).
func (m *Manager) buildSnapshotFromStore(ctx context.Context) *Snapshot {
	rows, err := m.st.Settings().GetAll(ctx)
	if err != nil {
		slog.Warn("settings post-write reload failed; serving previous snapshot until next refresh", "error", err)
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.snap != nil {
			return m.snap
		}
		return defaultsSnapshot(m)
	}
	return m.buildSnapshot(rows, "", nil)
}

// loadRow reads one key's row and decodes its halves; a missing row is an
// empty Row (defaults apply), not an error.
func loadRow(ctx context.Context, st store.Store, name string) (Row, error) {
	row, err := st.Settings().Get(ctx, name)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return Row{}, nil
		}
		return Row{}, fmt.Errorf("load setting %q: %w", name, err)
	}
	var out Row
	if row.Value != nil {
		out.Value = json.RawMessage(*row.Value)
	}
	out.Secret = row.Secret
	return out, nil
}

// defaultsSnapshot is the pre-Load fallback: every registered key at its
// default, nothing customized.
func defaultsSnapshot(m *Manager) *Snapshot {
	s := &Snapshot{
		at:         m.now(),
		values:     make(map[string]any, len(m.byName)),
		customized: make(map[string]bool),
		updatedAt:  make(map[string]time.Time),
	}
	for name, desc := range m.byName {
		s.values[name] = desc.Default()
	}
	if b, err := json.Marshal([]any{s.values, s.customized}); err == nil {
		s.canon = string(b)
	}
	return s
}

func ptrBytes(s *string) []byte {
	if s == nil {
		return nil
	}
	return []byte(*s)
}
