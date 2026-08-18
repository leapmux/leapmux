package settings

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/hub/store"
)

// cacheTTL limits how long a decoded snapshot is reused, mirroring the
// session and captcha caches. It is also the limit on how long an admin
// CLI write takes to reach a running hub (and the convergence limit
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
	// stored holds each key's STORED public document, byte for byte, for
	// the keys that have a row. It is what "the operator changed this"
	// looks like; values holds the merged view instead, in which every
	// field of the default is always present.
	stored map[string]json.RawMessage
	// canon is the hash of the canonical encoding of values+customized,
	// used to detect effective changes for subscribers. A hash, not the
	// encoding itself: the values include decrypted secret halves, and the
	// snapshot must not retain them in a second form.
	canon [sha256.Size]byte
}

// ValueOf returns the key's effective value, or nil when the key was
// never registered with the manager that produced this snapshot.
func (s *Snapshot) ValueOf(desc Descriptor) any {
	return s.values[desc.Name()]
}

// StoredValue returns the key's stored public document verbatim, or nil
// when the key has no row. It is NOT the effective value: a stored row
// carries only the fields it stores, and Decode merges the rest of the
// default in.
//
// No redaction is needed. The stored public half is secret-free by
// construction: the write path splits the secret fields out before it
// writes the public column.
func (s *Snapshot) StoredValue(desc Descriptor) json.RawMessage {
	return s.stored[desc.Name()]
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

	mu   sync.Mutex
	snap *Snapshot
	// epoch orders publishes: every write-path publish increments it, and
	// a refresh publishes only at the epoch it started reading at, so a
	// refresh that read rows before a write committed can never overwrite
	// the fresher snapshot that write already published.
	epoch  uint64
	subs   []func(*Snapshot)
	warned map[string]bool
	flight singleflight.Group
	// effective holds each key's read-time rule (WithEffective) and
	// afterReset each key's post-reset step (WithAfterReset). Both are
	// per-key rules that belong to the KEY, so every surface that reports
	// "what is in effect" reports the same thing. They are guarded because
	// the hub registers some of them after the manager loads (Configure).
	effective  map[string]func(*Snapshot) (any, bool)
	afterReset map[string]func(context.Context) error

	// reloadMu serializes each post-commit reload with the publish that
	// follows it. The epoch counter alone is not sufficient: it drops a
	// stale TTL refresh, but every write publish advances it, so two
	// writers whose reloads interleave would both publish and the slower
	// writer's older rows could land last. Holding this mutex across the
	// read and the publish keeps the published order equal to the commit
	// order.
	reloadMu sync.Mutex
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

// WithEffective attaches one key's read-time rule: the value the hub
// ACTUALLY uses right now, when that differs from the stored row merged
// onto the code default.
//
// The rule is a property of the KEY. A stored queue budget of 0 auto-sizes
// from the process memory limit, dev mode holds signup open until an
// operator stores a row, a selected captcha provider that is not fully
// configured degrades to another one — each of those is a fact about one
// key, so it belongs beside that key rather than restated in every surface
// that reports the key. A surface that held them carried per-key knowledge
// it could not keep correct, and had to import the domain package of every
// key it special-cased.
//
// The rule reports (value, true) to override, or (nil, false) to leave the
// stored-merged value alone. The captcha selection needs that second form:
// it overrides only while the selected provider is incomplete.
//
// The rule must not write. It runs on the read path, inside an RPC handler.
func WithEffective(name string, fn func(*Snapshot) (any, bool)) Option {
	return func(m *Manager) {
		if fn == nil {
			panic(fmt.Sprintf("settings: nil read-time rule for key %q", name))
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		if _, ok := m.byName[name]; !ok {
			panic(fmt.Sprintf("settings: read-time rule for unregistered key %q", name))
		}
		if _, dup := m.effective[name]; dup {
			panic(fmt.Sprintf("settings: duplicate read-time rule for key %q", name))
		}
		m.effective[name] = fn
	}
}

// WithAfterReset attaches one key's post-reset step: work the hub must
// complete before it answers a reset that removed the key's row.
//
// The ALTCHA row is the case that needs it. A reset removes the signing key
// every outstanding challenge carries, and the hub's standing rule is that
// the request path never writes settings — so the reset re-provisions before
// it answers rather than leaving the next unauthenticated login to write
// hub_settings from inside its own handler.
//
// ResetMany does NOT fire the step. The step writes settings itself, so
// firing it inside the reset would re-enter the write path; the caller runs
// it after the reset commits (see Manager.AfterReset).
func WithAfterReset(name string, fn func(context.Context) error) Option {
	return func(m *Manager) {
		if fn == nil {
			panic(fmt.Sprintf("settings: nil post-reset step for key %q", name))
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		if _, ok := m.byName[name]; !ok {
			panic(fmt.Sprintf("settings: post-reset step for unregistered key %q", name))
		}
		if _, dup := m.afterReset[name]; dup {
			panic(fmt.Sprintf("settings: duplicate post-reset step for key %q", name))
		}
		m.afterReset[name] = fn
	}
}

// NewManager creates a settings manager over the store. The keystore
// encrypts secret halves; a nil keystore makes every secret-bearing key
// degrade to its default (tests that do not exercise secrets).
func NewManager(st store.Store, ks *keystore.Keystore, descs []Descriptor, opts ...Option) *Manager {
	m := &Manager{
		st:         st,
		ks:         ks,
		now:        time.Now,
		ttl:        cacheTTL,
		byName:     make(map[string]Descriptor, len(descs)),
		warned:     make(map[string]bool),
		effective:  make(map[string]func(*Snapshot) (any, bool)),
		afterReset: make(map[string]func(context.Context) error),
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

// Configure applies further options to a manager that already exists.
//
// It exists for the per-key rules (WithEffective, WithAfterReset), which the
// hub cannot supply to NewManager: the resolved queue capacities come from
// pools that the startup snapshot sizes, and the captcha manager that
// re-provisions the ALTCHA row is itself built on this manager. Both close
// over something that only exists AFTER the manager loads, so passing them
// at construction is impossible, not merely inconvenient.
//
// Call it from the startup wiring, before the hub serves a request. The two
// rule tables are guarded, so a late registration cannot race a reader; the
// plain options (WithTTL, WithNow) write unguarded fields and belong at
// construction.
func (m *Manager) Configure(opts ...Option) {
	for _, opt := range opts {
		opt(m)
	}
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

// Effective returns the value the hub uses for the key right now: the
// registered read-time rule's value when that rule applies (WithEffective),
// and otherwise the snapshot's own value, which is the stored row merged
// onto the code default.
//
// The rule runs outside the manager lock. It reads the snapshot it is given
// and nothing else, so it cannot re-enter the manager.
func (m *Manager) Effective(s *Snapshot, desc Descriptor) any {
	m.mu.Lock()
	fn := m.effective[desc.Name()]
	m.mu.Unlock()
	if fn != nil {
		if v, ok := fn(s); ok {
			return v
		}
	}
	return s.ValueOf(desc)
}

// AfterReset runs the key's registered post-reset step (WithAfterReset). A
// key with no step is not an error: most keys need nothing once their row is
// gone.
//
// The caller runs it AFTER the reset commits. The step writes settings
// itself, so ResetMany cannot fire it from inside its own transaction.
func (m *Manager) AfterReset(ctx context.Context, desc Descriptor) error {
	m.mu.Lock()
	fn := m.afterReset[desc.Name()]
	m.mu.Unlock()
	if fn == nil {
		return nil
	}
	if err := fn(ctx); err != nil {
		return fmt.Errorf("settings key %q: the post-reset step failed: %w", desc.Name(), err)
	}
	return nil
}

// Load performs the initial synchronous snapshot. The hub calls it at
// startup so a broken store fails startup (and restart-class values are
// available before any pool is constructed) instead of degrading every
// request.
func (m *Manager) Load(ctx context.Context) error {
	epoch := m.currentEpoch()
	s, err := m.refresh(ctx)
	if err != nil {
		return err
	}
	m.publishRefresh(s, epoch)
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
		epoch := m.currentEpoch()
		s, err := m.refresh(context.WithoutCancel(ctx))
		if err != nil {
			return nil, err
		}
		return refreshResult{s: s, epoch: epoch}, nil
	})
	if err != nil {
		slog.Warn("settings snapshot refresh failed; serving last good snapshot", "error", err)
		return m.lastGoodOrDefaults()
	}
	switch res := v.(type) {
	case *Snapshot:
		// The flight found the cache fresh again; nothing to publish.
		return res
	case refreshResult:
		m.publishRefresh(res.s, res.epoch)
		return res.s
	default:
		// Unreachable: the flight returns one of the two shapes above.
		return m.lastGoodOrDefaults()
	}
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

// refreshResult pairs a refreshed snapshot with the epoch its read
// started at, so publishRefresh can drop one that a write overtook.
type refreshResult struct {
	s     *Snapshot
	epoch uint64
}

// currentEpoch reads the publish ordering counter.
func (m *Manager) currentEpoch() uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.epoch
}

// publishWrite stores a snapshot produced by a committed write and fires
// subscribers when it differs from the previous one. Write publishes
// advance the epoch, overtaking any in-flight refresh.
func (m *Manager) publishWrite(s *Snapshot) {
	m.mu.Lock()
	m.epoch++
	prev := m.snap
	m.snap = s
	var subs []func(*Snapshot)
	subs = append(subs, m.subs...)
	m.mu.Unlock()
	m.fireSubs(prev, s, subs)
}

// publishRefresh stores a snapshot produced by a TTL refresh that started
// reading at the given epoch. A refresh that a write overtook (its epoch
// is older than the current one) is dropped: publishing it would revert
// the served snapshot to pre-write state for a further TTL even though
// the write already published the post-commit state.
func (m *Manager) publishRefresh(s *Snapshot, epoch uint64) {
	m.mu.Lock()
	if epoch < m.epoch {
		m.mu.Unlock()
		return
	}
	prev := m.snap
	m.snap = s
	var subs []func(*Snapshot)
	subs = append(subs, m.subs...)
	m.mu.Unlock()
	m.fireSubs(prev, s, subs)
}

// publishFromStore re-reads every row after a commit and publishes the
// result. It is the one post-commit publish path: holding reloadMu across
// the read and the publish keeps the published order equal to the commit
// order.
func (m *Manager) publishFromStore(ctx context.Context) {
	m.reloadMu.Lock()
	defer m.reloadMu.Unlock()
	m.publishWrite(m.buildSnapshotFromStore(ctx))
}

// fireSubs runs the subscribers when the effective state changed.
func (m *Manager) fireSubs(prev, s *Snapshot, subs []func(*Snapshot)) {
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
	return m.buildSnapshotWith(rows, nil), nil
}

// buildSnapshotWith decodes rows into a snapshot, substituting each
// override for the key it specifies.
//
// The override map is what makes an ATOMIC multi-key write checkable: a
// cross-key rule has to see every value the transaction is about to store
// at once, not one candidate at a time. Checking them one at a time is
// what forced callers to order their writes so each intermediate state
// stayed legal.
func (m *Manager) buildSnapshotWith(rows []store.SettingRow, overrides map[string]any) *Snapshot {
	s := &Snapshot{
		at:         m.now(),
		values:     make(map[string]any, len(m.byName)),
		customized: make(map[string]bool, len(rows)),
		updatedAt:  make(map[string]time.Time, len(rows)),
		stored:     make(map[string]json.RawMessage, len(rows)),
	}
	stored := make(map[string]store.SettingRow, len(rows))
	for _, row := range rows {
		stored[row.Key] = row
	}
	for _, name := range m.names {
		desc := m.byName[name]
		if v, ok := overrides[name]; ok {
			s.values[name] = v
			s.customized[name] = true
			// stored stays UNSET here. An override snapshot is the write
			// path's cross-validation candidate: it holds a value that is
			// not stored yet, and it never reaches the wire.
			continue
		}
		row, has := stored[name]
		var decoded Row
		if has {
			s.customized[name] = true
			s.updatedAt[name] = row.UpdatedAt
			if row.Value != nil {
				decoded.Value = json.RawMessage(*row.Value)
				s.stored[name] = decoded.Value
			}
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
		} else {
			// Healthy: clear this key's warning tags so a row that goes
			// bad again warns again — a once-per-process tag would hide
			// the second regression, which is exactly the one an operator
			// did not already investigate.
			m.clearWarned("secret:" + name)
			m.clearWarned("decode:" + name)
			m.clearWarned("invalid:" + name)
		}
		s.values[name] = v
	}
	orphans := make(map[string]bool)
	for key := range stored {
		if _, known := m.byName[key]; !known {
			// A row for a key no longer registered (setting removed, or a
			// hub version that never knew it). Harmless: nobody reads it.
			m.warnOnce("orphan:"+key, "settings row for unknown key ignored", "key", key)
			orphans[key] = true
		}
	}
	m.clearMissingOrphanTags(orphans)
	s.canon = canonicalSum(s.values, s.customized)
	return s
}

// canonicalSum hashes the canonical encoding of values+customized: the
// change-detection token publish compares. The values include decrypted
// secret halves, so the token must not keep the encoding itself — only
// its digest.
func canonicalSum(values map[string]any, customized map[string]bool) [sha256.Size]byte {
	b, err := json.Marshal([]any{values, customized})
	if err != nil {
		return [sha256.Size]byte{}
	}
	return sha256.Sum256(b)
}

// warnOnce logs at warn level the first time tag is seen, so a persistently
// bad row does not spam the log on every refresh. Tags clear again when the
// state they describe recovers (see buildSnapshotWith), which keeps the
// once-per-state guarantee while a regression stays visible.
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

// clearWarned removes one warning tag.
func (m *Manager) clearWarned(tag string) {
	m.mu.Lock()
	delete(m.warned, tag)
	m.mu.Unlock()
}

// clearMissingOrphanTags drops orphan-row warning tags for keys whose
// rows disappeared, so a row that comes back warns again.
func (m *Manager) clearMissingOrphanTags(present map[string]bool) {
	m.mu.Lock()
	for tag := range m.warned {
		if key, ok := strings.CutPrefix(tag, "orphan:"); ok && !present[key] {
			delete(m.warned, tag)
		}
	}
	m.mu.Unlock()
}

// Update merges a partial JSON document onto the key's current value and
// writes it. Fields the document omits keep their current (or default)
// values, so `{"port": 465}` retunes one SMTP field without restating
// the host. Validation — the key's own rules plus cross-key rules — runs
// against the post-write view before anything is stored.
//
// It is UpdateMany with one key. The single-key and the multi-key verb
// therefore cannot drift on the argument checks, the lock order, the merge,
// or the refusal messages: there is one write path, and this is a caller of
// it.
//
// The partial travels in KeyWrite.Public because mergeForUpdate overlays
// both halves the same way; which half a field belongs to is the
// descriptor's decision, not the caller's. That is why the secret-rotation
// test in mergeForUpdate reads BOTH halves — a rotation sent through this
// verb arrives in Public.
func (m *Manager) Update(ctx context.Context, desc Descriptor, partial json.RawMessage) error {
	return m.UpdateMany(ctx, []KeyWrite{{Desc: desc, Public: partial}})
}

// UpdateSecret merges a partial JSON document onto the key's secret half.
// Mechanically identical to Update — the fields the document specifies
// simply belong to the encrypted half — but a separate verb keeps the
// admin CLI's intent explicit and lets the write path require the key to
// be secret-bearing.
//
// The three refusals below are this verb's own, and their ORDER is part of
// the contract. UpdateMany repeats the first two over every KeyWrite, but a
// caller that reaches this verb must get this verb's answer.
func (m *Manager) UpdateSecret(ctx context.Context, desc Descriptor, partial json.RawMessage) error {
	if !desc.HasSecret() {
		return Invalidf("settings key %q has no secret fields", desc.Name())
	}
	// Ordered BEFORE the secret-field check below: partialNamesSecret finds
	// no secret field in an empty document, so the reverse order would
	// answer an omitted document with the secret-field message.
	if err := RequireNonEmptyPartial(desc.Name(), partial); err != nil {
		return err
	}
	// The document must specify at least one SECRET field. Without this,
	// the secret verb merges any declared field, so a caller could rewrite
	// the SMTP host or the ALTCHA cost through the verb whose whole purpose
	// is the encrypted half -- and the admin surface documents the opposite.
	if !partialNamesSecret(desc, partial) {
		return Invalidf("settings key %q: the secret document must specify at least one of %v",
			desc.Name(), desc.SecretFieldNames())
	}
	return m.UpdateMany(ctx, []KeyWrite{{Desc: desc, Secret: partial}})
}

// SetValue writes a fully-merged typed value (wrapper helpers and the
// captcha provisioning use this when they hold the complete value).
//
// Beyond the registration check it runs no pre-check of its own:
// prepareRows inside the transaction applies the key's rules, the cross-key
// rules, and the split, and it is the only place that can see the rest of
// the table.
func (m *Manager) SetValue(ctx context.Context, desc Descriptor, v any) error {
	return m.setComplete(ctx, desc, v, upsertRows)
}

// SetIfAbsent writes the value only when the key has no row, making
// first-use provisioning a one-winner race: racing provisioners each
// generate a value, the insert-if-absent keeps whichever commits first,
// and the loser's value is discarded rather than overwriting the winner's
// (a mid-flight challenge signed with the loser's key must not be
// invalidated by the race itself). The conditional insert is atomic in
// the store, so this holds across processes and hub instances sharing
// one database, under every dialect's isolation.
//
// It shares SetValue's whole tail. The conditional insert is the only
// difference between the two.
func (m *Manager) SetIfAbsent(ctx context.Context, desc Descriptor, v any) error {
	return m.setComplete(ctx, desc, v, insertRowsIfAbsent)
}

// rowWriter is the one step the complete-value verbs do not share: the
// unconditional upsert, or the conditional insert that makes first-use
// provisioning a one-winner race.
type rowWriter func(ctx context.Context, tx store.Store, params []store.UpsertSettingParams) error

// setComplete is the write path both complete-value verbs share. Holding it
// in ONE function is what keeps the registration check on both: a third such
// verb cannot be written without it.
func (m *Manager) setComplete(ctx context.Context, desc Descriptor, v any, write rowWriter) error {
	if err := m.requireRegistered(desc); err != nil {
		return err
	}
	return m.writeInTransaction(ctx, func(tx store.Store, locked lockedRows) error {
		params, err := m.prepareRows([]pendingWrite{completeWrite(desc, v)}, locked)
		if err != nil {
			return err
		}
		return write(ctx, tx, params)
	})
}

// requireRegistered refuses a key this manager never registered. EVERY write
// verb runs it, BEFORE it opens a transaction, so the caller's mistake costs
// no transaction and reaches no row.
//
// The check is what keeps the table and the snapshot in agreement.
// buildSnapshotWith walks the REGISTERED names, so an unregistered key's row
// would be stored and then dropped from every snapshot AND from every
// cross-key candidate: a durable row that no reader can ever get back, that
// no rule can refuse, and that warns once per process that it belongs to an
// unknown key. Removing the row again needs direct SQL, because the reset
// verbs refuse the same unregistered name.
//
// It compares NAMES, not descriptor identity, which is how the whole package
// resolves a key (Snapshot.ValueOf, Key.Of, the row keys themselves).
func (m *Manager) requireRegistered(desc Descriptor) error {
	if _, ok := m.byName[desc.Name()]; !ok {
		return Invalidf("settings key %q is not registered", desc.Name())
	}
	return nil
}

// Reset removes the key's row, returning it to its code default. The
// cross-key rules run against the post-reset view first — the same
// contract every write path holds — so a reset cannot store the exact
// combination an update refuses (e.g. dropping the smtp row while
// email_verification_required stays true).
func (m *Manager) Reset(ctx context.Context, desc Descriptor) error {
	if err := m.ResetMany(ctx, []Descriptor{desc}); err != nil {
		return fmt.Errorf("reset setting %q: %w", desc.Name(), err)
	}
	return nil
}

// ResetMany removes several keys' rows ATOMICALLY: one transaction, and
// ONE cross-key validation over the whole post-reset state.
//
// A caller that must clear more than one key cannot express that as a
// sequence of single-key resets, for the reason UpdateMany states: each
// Reset validates against the state the previous one left, so an
// intermediate state a cross-key rule refuses blocks the sequence
// part-way, and a failure after the first delete leaves stored state the
// caller was told it never reached. The captcha verbs pay both costs when
// they clear a provider's row beside the selection that points at it.
func (m *Manager) ResetMany(ctx context.Context, descs []Descriptor) error {
	if len(descs) == 0 {
		return Invalidf("no settings resets given")
	}
	names := make(map[string]bool, len(descs))
	for _, desc := range descs {
		name := desc.Name()
		if err := m.requireRegistered(desc); err != nil {
			return err
		}
		if names[name] {
			return Invalidf("settings key %q appears twice in one reset", name)
		}
		names[name] = true
	}
	// lockAll's whole-table lock already covers the rows this transaction
	// deletes, so no separate per-key lock is needed here.
	return m.writeInTransaction(ctx, func(tx store.Store, locked lockedRows) error {
		kept := make([]store.SettingRow, 0, len(locked.all))
		for _, row := range locked.all {
			if !names[row.Key] {
				kept = append(kept, row)
			}
		}
		if err := m.crossValidate(m.buildSnapshotWith(kept, nil)); err != nil {
			return err
		}
		for _, desc := range descs {
			if err := tx.Settings().Delete(ctx, desc.Name()); err != nil {
				return fmt.Errorf("delete setting %q: %w", desc.Name(), err)
			}
		}
		return nil
	})
}

// crossValidate runs every cross-key rule against a candidate snapshot.
// Every write path shares it, so the rules and the refusal message have
// one form.
func (m *Manager) crossValidate(candidate *Snapshot) error {
	for _, rule := range m.cross {
		if err := rule(candidate); err != nil {
			return Invalidf("cross-validation failed: %w", err)
		}
	}
	return nil
}

// writeInTransaction runs one settings write transaction and publishes the
// state it committed.
//
// EVERY write path goes through it, which makes two invariants mechanical
// rather than remembered. The transaction opens with lockAll and takes no
// other settings lock, which is what keeps the lock order acyclic (see
// lockAll). And a commit is always followed by the post-commit reload that
// pushes the new snapshot to the subscribers, so no path can store a value
// that the running hub never sees.
func (m *Manager) writeInTransaction(ctx context.Context, fn func(tx store.Store, locked lockedRows) error) error {
	if err := m.st.RunInTransaction(ctx, func(tx store.Store) error {
		locked, err := lockAll(ctx, tx)
		if err != nil {
			return err
		}
		return fn(tx, locked)
	}); err != nil {
		return err
	}
	m.publishFromStore(ctx)
	return nil
}

// KeyWrite is one key's edit inside an atomic UpdateMany: a partial
// document for the public half, the secret half, or both. An empty
// document leaves that half alone.
type KeyWrite struct {
	Desc   Descriptor
	Public json.RawMessage
	Secret json.RawMessage
}

// UpdateMany applies several keys' partial documents ATOMICALLY: one
// transaction, and ONE cross-key validation over the whole result.
//
// A caller that must change more than one key cannot express that as a
// sequence of single-key writes. Each Update validates against the state
// the previous one left, so an intermediate state a cross-key rule refuses
// blocks the sequence part-way — and a failure after the first write
// leaves stored state the caller was told it never reached. Both cost the
// captcha verbs: selecting a provider before its keys exist is refused,
// and re-keying the SELECTED provider could publish a new site key beside
// the old secret.
//
// Both halves of one key travel in the same KeyWrite, which is what lets a
// site key and its secret land together or not at all.
func (m *Manager) UpdateMany(ctx context.Context, writes []KeyWrite) error {
	if len(writes) == 0 {
		return Invalidf("no settings writes given")
	}
	seen := make(map[string]bool, len(writes))
	for _, w := range writes {
		name := w.Desc.Name()
		if err := m.requireRegistered(w.Desc); err != nil {
			return err
		}
		if seen[name] {
			return Invalidf("settings key %q appears twice in one write", name)
		}
		seen[name] = true
		// A secret half for a key that declares no secret field. UpdateSecret
		// refuses exactly this, in exactly these words; without the same
		// refusal here the multi-key verb accepts a document the single-key
		// verb rejects, and mergeForUpdate then overlays it as if it were the
		// public half.
		if len(w.Secret) > 0 && !w.Desc.HasSecret() {
			return Invalidf("settings key %q has no secret fields", name)
		}
		// Each half this write carries must be able to change something, and
		// a write that carries neither half is the same refusal. Update and
		// UpdateSecret get it HERE too, because both route through this verb.
		// Without it an inert document reaches the merge, changes nothing,
		// and still upserts the row -- which reports the key as customized
		// for a change that never happened.
		if len(w.Public) == 0 && len(w.Secret) == 0 {
			// An absent document always refuses; the helper owns the message.
			return RequireNonEmptyPartial(name, nil)
		}
		for _, partial := range []json.RawMessage{w.Public, w.Secret} {
			if len(partial) == 0 {
				continue
			}
			if err := RequireNonEmptyPartial(name, partial); err != nil {
				return err
			}
		}
	}
	// Upsert the rows in one canonical order, whatever order the caller
	// listed the writes in. A key with no row yet takes no lock from the
	// transaction's whole-table read (lockAll), so two concurrent UpdateMany
	// calls that INSERT an overlapping key set would otherwise take those new
	// rows' locks in opposite orders and deadlock. The caller's slice order
	// carries no meaning -- the whole point of this verb is that the writes
	// land together.
	sorted := slices.Clone(writes)
	slices.SortFunc(sorted, func(a, b KeyWrite) int {
		return strings.Compare(a.Desc.Name(), b.Desc.Name())
	})

	pending := make([]pendingWrite, 0, len(sorted))
	for _, w := range sorted {
		pending = append(pending, pendingWrite{
			desc: w.Desc,
			merge: func(locked lockedRows) (any, error) {
				return m.mergeForUpdate(locked.row(w.Desc.Name()), w)
			},
		})
	}
	return m.writeInTransaction(ctx, func(tx store.Store, locked lockedRows) error {
		params, err := m.prepareRows(pending, locked)
		if err != nil {
			return err
		}
		return upsertRows(ctx, tx, params)
	})
}

// pendingWrite is one key's contribution to a write transaction: the
// descriptor, and the step that produces the value the transaction stores.
//
// The step runs INSIDE the transaction, over its locked rows, because a
// partial write has to merge onto the row the lock protects. A caller that
// already holds the complete value supplies completeWrite instead.
type pendingWrite struct {
	desc  Descriptor
	merge func(lockedRows) (any, error)
}

// completeWrite is the pendingWrite for a caller that already holds the
// whole value. SetValue and SetIfAbsent never merge onto the stored row:
// every field of their value is explicit, which is the same boundary
// WithNormalize states.
func completeWrite(desc Descriptor, v any) pendingWrite {
	return pendingWrite{desc: desc, merge: func(lockedRows) (any, error) { return v, nil }}
}

// prepareRows is the write tail every path shares: each key's merge, then
// the key's own rules, then ONE cross-key validation over the whole
// candidate snapshot, then the split and the encryption of every row.
//
// The cross-key rules run ONCE over every candidate at once. That is what
// makes a multi-key write checkable: a rule that spans keys has to see the
// whole result, not one candidate at a time.
//
// It returns the assembled row writes rather than storing them, because the
// store call is the only thing the paths do not share — SetIfAbsent
// substitutes the conditional insert that makes first-use provisioning a
// one-winner race.
func (m *Manager) prepareRows(pending []pendingWrite, locked lockedRows) ([]store.UpsertSettingParams, error) {
	candidate := make(map[string]any, len(pending))
	for _, w := range pending {
		v, err := w.merge(locked)
		if err != nil {
			return nil, err
		}
		if err := w.desc.Validate(v); err != nil {
			return nil, Invalidf("invalid value for %q: %w", w.desc.Name(), err)
		}
		candidate[w.desc.Name()] = v
	}
	if err := m.crossValidate(m.buildSnapshotWith(locked.all, candidate)); err != nil {
		return nil, err
	}
	params := make([]store.UpsertSettingParams, 0, len(pending))
	for _, w := range pending {
		public, secret, err := w.desc.Split(candidate[w.desc.Name()])
		if err != nil {
			return nil, err
		}
		row, err := m.rowParams(w.desc, public, secret)
		if err != nil {
			return nil, err
		}
		params = append(params, row)
	}
	return params, nil
}

// upsertRows stores every prepared row, in the order prepareRows assembled
// them (see the upsert order UpdateMany fixes).
func upsertRows(ctx context.Context, tx store.Store, params []store.UpsertSettingParams) error {
	for _, p := range params {
		if err := tx.Settings().Upsert(ctx, p); err != nil {
			return fmt.Errorf("upsert setting %q: %w", p.Key, err)
		}
	}
	return nil
}

// insertRowsIfAbsent stores every prepared row only when its key has no row
// yet. SetIfAbsent is the one caller.
func insertRowsIfAbsent(ctx context.Context, tx store.Store, params []store.UpsertSettingParams) error {
	for _, p := range params {
		if _, err := tx.Settings().InsertIfAbsent(ctx, p); err != nil {
			return fmt.Errorf("insert setting %q: %w", p.Key, err)
		}
	}
	return nil
}

// mergeForUpdate decodes one key's current row and overlays the write's
// partial documents. It is the merge step of a pendingWrite, and every
// partial-document verb reaches it through UpdateMany.
//
// The row comes from the caller's locked whole-table read (lockAll), never
// from a second query. That lock is what makes the read-modify-write merge
// safe, and taking it once per transaction is what keeps the lock order
// acyclic.
func (m *Manager) mergeForUpdate(row Row, w KeyWrite) (any, error) {
	desc := w.Desc
	if len(row.Secret) > 0 && m.ks != nil {
		plain, derr := m.ks.Decrypt(row.Secret, keystore.SettingsSecretAAD(desc.Name()))
		if derr != nil {
			// An explicit rotation (a document that supplies new secret
			// fields, such as the altcha self-heal's fresh signing key) may
			// replace a secret the keystore can no longer read. Anything
			// else must STOP rather than upsert a re-encrypted default over
			// the only copy of an operator-entered secret: restoring the
			// key version, or re-encrypting under the active one, is the
			// recovery path.
			//
			// BOTH halves are tested. Update sends its whole document in
			// Public, so reading Secret alone would refuse the rotation
			// that arrives through that verb.
			if partialNamesSecret(desc, w.Public) || partialNamesSecret(desc, w.Secret) {
				row.Secret = nil
			} else {
				return nil, secretUndecryptablef("settings key %q stores a secret the current keystore cannot decrypt; restore the key version, run `leapmux recover encryption-key reencrypt`, or supply the secret fields explicitly: %w", desc.Name(), derr)
			}
		} else {
			row.Secret = json.RawMessage(plain)
		}
	} else {
		row.Secret = nil
	}
	merged, err := desc.Decode(row)
	if err != nil {
		return nil, Invalidf("decode current value of %q: %w", desc.Name(), err)
	}
	if verr := desc.Validate(merged); verr != nil {
		// The READ path degrades a stored row its own validator refuses to
		// the code default (buildSnapshotWith), so a row written outside
		// the write path -- direct SQL, or an older hub whose rules were
		// wider -- never reaches a consumer. The write path must start from
		// the same base, or every partial write to that key is refused and
		// the key stays unwritable until someone edits the table by hand.
		//
		// The decrypted secret half survives the re-decode, so recovering a
		// public field never overwrites an operator-entered secret with a
		// default.
		m.warnOnce("invalid:"+desc.Name(),
			"settings row invalid; merging the write onto the default",
			"key", desc.Name(), "error", verr)
		merged, err = desc.Decode(Row{Secret: row.Secret})
		if err != nil {
			return nil, Invalidf("decode default value of %q: %w", desc.Name(), err)
		}
	}
	for _, partial := range []json.RawMessage{w.Public, w.Secret} {
		if len(partial) == 0 {
			continue
		}
		merged, err = desc.ApplyPartial(merged, partial)
		if err != nil {
			return nil, Invalidf("merge partial document for %q: %w", desc.Name(), err)
		}
	}
	return merged, nil
}

// rowParams encrypts the secret half and assembles one row write, refusing
// a write whose two halves are both empty (the table's CHECK refuses it
// anyway, with a message that says nothing about the key). prepareRows is
// the one caller, so the refusal covers every write path.
func (m *Manager) rowParams(desc Descriptor, public, secret json.RawMessage) (store.UpsertSettingParams, error) {
	encrypted, err := m.encryptSecret(desc, secret)
	if err != nil {
		return store.UpsertSettingParams{}, err
	}
	p := paramsFromHalves(desc, public, encrypted)
	if p.Value == nil && p.Secret == nil {
		return store.UpsertSettingParams{}, Invalidf("settings key %q would write an empty row", desc.Name())
	}
	return p, nil
}

// encryptSecret encrypts the secret half under the key-name-bound AAD. A
// secret-bearing key with no keystore refuses rather than storing the
// secret in the clear: the missing keystore is a bug in the caller's
// wiring.
func (m *Manager) encryptSecret(desc Descriptor, secret json.RawMessage) ([]byte, error) {
	if len(secret) == 0 {
		return nil, nil
	}
	if m.ks == nil {
		return nil, fmt.Errorf("settings key %q carries a secret but no keystore is configured", desc.Name())
	}
	enc, err := m.ks.Encrypt(secret, keystore.SettingsSecretAAD(desc.Name()))
	if err != nil {
		return nil, fmt.Errorf("encrypt secret half of %q: %w", desc.Name(), err)
	}
	return enc, nil
}

// paramsFromHalves assembles the row write from the split halves. At least
// one half must be non-empty to satisfy the table's CHECK.
func paramsFromHalves(desc Descriptor, public json.RawMessage, encrypted []byte) store.UpsertSettingParams {
	p := store.UpsertSettingParams{Key: desc.Name(), Secret: encrypted}
	if len(public) > 0 {
		s := string(public)
		p.Value = &s
	}
	return p
}

// buildSnapshotFromStore refreshes from the store and is best-effort: the
// write already committed, so a read failure logs and falls back to the
// last published snapshot (the TTL refresh will converge).
//
// It re-reads the WHOLE table rather than reusing the rows the write
// transaction already read or patching the previous snapshot with the one
// value just written. Both of those shortcuts would publish a snapshot
// blind to every other writer that committed in the same window, and the
// hub runs several instances over one database. The read costs one query
// per write on an administrative path.
//
// The read deliberately uses a context that cancellation cannot stop. The
// caller's context is often a request context the client may abandon the
// moment the RPC answers, and the fallback would then republish the
// PREVIOUS snapshot pointer — publishWrite would find prev and s equal and
// fire no subscriber, so a committed write would never reach the pushed
// consumers.
func (m *Manager) buildSnapshotFromStore(ctx context.Context) *Snapshot {
	rows, err := m.st.Settings().GetAll(context.WithoutCancel(ctx))
	if err != nil {
		slog.Warn("settings post-write reload failed; serving previous snapshot until next refresh", "error", err)
		return m.lastGoodOrDefaults()
	}
	return m.buildSnapshotWith(rows, nil)
}

// lastGoodOrDefaults is the store-outage fallback both read paths share:
// the last published snapshot, or a defaults-only one when the store has
// never answered.
//
// The manager lock is released BEFORE the defaults-only build. Holding it
// across the build deadlocks: buildSnapshotWith records and clears warning
// tags through the same non-reentrant mutex, so a store that fails before
// the first successful Load wedged every settings read forever.
func (m *Manager) lastGoodOrDefaults() *Snapshot {
	m.mu.Lock()
	last := m.snap
	m.mu.Unlock()
	if last != nil {
		return last
	}
	return m.buildSnapshotWith(nil, nil)
}

// partialNamesSecret reports whether a partial document supplies any of
// the key's secret fields — the difference between an explicit secret
// rotation and a public-field update that must not silently drop a
// secret the keystore can no longer read.
func partialNamesSecret(desc Descriptor, partial json.RawMessage) bool {
	fields := desc.SecretFieldNames()
	if len(fields) == 0 || len(partial) == 0 {
		return false
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(partial, &doc); err != nil {
		return false
	}
	for _, f := range fields {
		if _, ok := doc[f]; ok {
			return true
		}
	}
	return false
}

// lockedRows is one write transaction's locked whole-table read: the rows
// for the cross-key validation, and the same rows indexed by key for the
// per-key merge.
type lockedRows struct {
	all   []store.SettingRow
	byKey map[string]Row
}

// row returns one key's stored halves. A key with no row is an empty Row —
// the code default applies, which is not an error.
func (l lockedRows) row(name string) Row { return l.byKey[name] }

// lockAll takes the settings table's writer lock and reads every row.
//
// EVERY write transaction starts here and takes NO other settings lock,
// which is what keeps the lock order acyclic. writeInTransaction is the ONE
// caller, so that ordering is mechanical rather than remembered: a write
// path cannot open a transaction without this read. A transaction that locked
// one key's row FIRST and the whole table SECOND could hold key z while it
// waited for key a, against a second writer holding a and waiting for z;
// Postgres and MySQL detect that cycle and abort one of the pair with a
// deadlock error.
//
// The lock carries two guarantees. It makes the read-modify-write merge
// safe — two overlapping partial writes to one key would otherwise both
// merge onto the same base, and the second commit would erase the first's
// fields. It also makes the cross-key rules sound: a plain unlocked read
// sees rows a concurrent writer is about to change, so a rule that spans
// keys ("email verification needs an SMTP host") could pass against a
// sibling row that no longer holds by the time both transactions commit.
//
// A key with no row takes no lock, because there is no row to lock. The
// upsert order is what serializes those (see UpdateMany).
//
// The caller must hold a transaction.
func lockAll(ctx context.Context, tx store.Store) (lockedRows, error) {
	rows, err := tx.Settings().GetAllForUpdate(ctx)
	if err != nil {
		return lockedRows{}, fmt.Errorf("load settings for update: %w", err)
	}
	out := lockedRows{all: rows, byKey: make(map[string]Row, len(rows))}
	for _, r := range rows {
		var row Row
		if r.Value != nil {
			row.Value = json.RawMessage(*r.Value)
		}
		row.Secret = r.Secret
		out.byKey[r.Key] = row
	}
	return out, nil
}
