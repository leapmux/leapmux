package settings

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/sqlite"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type testValue struct {
	Host string `json:"host,omitempty"`
	Port int    `json:"port,omitempty"`
	Pass string `json:"pass,omitempty"`
}

func testKey() *Key[testValue] {
	return NewKey[testValue]("test.obj").
		WithDefault(testValue{Port: 587}).
		WithValidate(func(v testValue) error {
			if v.Port < 1 || v.Port > 65535 {
				return errTest("port out of range")
			}
			return nil
		}).
		SecretFields("pass")
}

type errTest string

func (e errTest) Error() string { return string(e) }

func newTestManager(t *testing.T) (*Manager, *Key[testValue]) {
	t.Helper()
	st, err := sqlite.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	key, err := keystore.GenerateKey()
	require.NoError(t, err)
	ks, err := keystore.New(map[uint32][32]byte{1: key})
	require.NoError(t, err)

	k := testKey()
	m := NewManager(st, ks, []Descriptor{k, KeySMTP, KeyEmailVerificationRequired},
		WithCrossValidation(SMTPConfigured))
	require.NoError(t, m.Load(context.Background()))
	return m, k
}

func TestDefaultsWhenNoRow(t *testing.T) {
	m, k := newTestManager(t)
	snap := m.Snapshot(context.Background())
	v := k.Of(snap)
	assert.Equal(t, 587, v.Port, "absent row resolves to the key's default")
	assert.False(t, snap.Customized(k), "absent row is not customized")
}

// TestCoreTimeoutsDefaultAndDurationHelpers pins the hoisted
// DefaultTimeouts — the one source the solo worker pins and the service
// test harnesses derive their timeouts from — to the core key's
// effective default and to the seconds→duration conversions.
func TestCoreTimeoutsDefaultAndDurationHelpers(t *testing.T) {
	st, err := sqlite.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	m := NewManager(st, nil, CoreDescriptors())
	v := KeyTimeouts.Of(m.Snapshot(context.Background()))
	assert.Equal(t, DefaultTimeouts, v, "the timeouts key resolves to DefaultTimeouts")
	assert.Equal(t, 10*time.Second, v.APITimeout())
	assert.Equal(t, 5*time.Minute, v.AgentStartupTimeout())
	assert.Equal(t, time.Minute, v.WorktreeCreateTimeout())
}

func TestUpdateMergesPartialDocuments(t *testing.T) {
	m, k := newTestManager(t)
	ctx := context.Background()

	require.NoError(t, m.Update(ctx, k, json.RawMessage(`{"host":"smtp.example.com"}`)))
	v := k.Of(m.Snapshot(ctx))
	assert.Equal(t, "smtp.example.com", v.Host)
	assert.Equal(t, 587, v.Port, "an omitted field keeps the default")

	// A second partial overlays the stored row, not the default.
	require.NoError(t, m.Update(ctx, k, json.RawMessage(`{"port":465}`)))
	v = k.Of(m.Snapshot(ctx))
	assert.Equal(t, "smtp.example.com", v.Host, "a prior partial's field survives")
	assert.Equal(t, 465, v.Port)

	assert.True(t, m.Snapshot(ctx).Customized(k))
}

func TestUpdateRejectsInvalidValues(t *testing.T) {
	m, k := newTestManager(t)
	ctx := context.Background()
	err := m.Update(ctx, k, json.RawMessage(`{"port":99999}`))
	require.Error(t, err)
	assert.Equal(t, 587, k.Of(m.Snapshot(ctx)).Port, "a rejected write changes nothing")
}

func TestCrossValidationRejectsImpossibleCombination(t *testing.T) {
	m, _ := newTestManager(t)
	ctx := context.Background()
	err := m.Update(ctx, KeyEmailVerificationRequired, json.RawMessage(`true`))
	require.Error(t, err, "verification without SMTP must be refused at write time")

	// With SMTP staged first, the same write succeeds.
	require.NoError(t, m.Update(ctx, KeySMTP, json.RawMessage(`{"host":"smtp.example.com","from_address":"hub@example.com","port":587,"tls_mode":"starttls"}`)))
	require.NoError(t, m.Update(ctx, KeyEmailVerificationRequired, json.RawMessage(`true`)))
}

func TestSecretRoundTripAndRedaction(t *testing.T) {
	m, k := newTestManager(t)
	ctx := context.Background()

	require.NoError(t, m.Update(ctx, k, json.RawMessage(`{"host":"smtp.example.com"}`)))
	require.NoError(t, m.UpdateSecret(ctx, k, json.RawMessage(`{"pass":"hunter2"}`)))

	// The decrypted value reaches readers through the snapshot.
	v := k.Of(m.Snapshot(ctx))
	assert.Equal(t, "hunter2", v.Pass)
	assert.Equal(t, "smtp.example.com", v.Host, "a secret write preserves the public half")

	// The stored row never carries the secret in the clear.
	row, err := m.st.Settings().Get(ctx, k.Name())
	require.NoError(t, err)
	require.NotNil(t, row.Value)
	assert.NotContains(t, *row.Value, "hunter2")
	assert.NotEmpty(t, row.Secret, "the secret half is stored encrypted")

	// Redaction erases it for display.
	redacted := k.Redacted(k.Of(m.Snapshot(ctx)))
	b, err := json.Marshal(redacted)
	require.NoError(t, err)
	assert.NotContains(t, string(b), "hunter2")
	var shown map[string]any
	require.NoError(t, json.Unmarshal(b, &shown))
	assert.Equal(t, "<redacted>", shown["pass"])
	assert.Equal(t, "smtp.example.com", shown["host"])
}

func TestPublicWriteNeverLosesSecret(t *testing.T) {
	m, k := newTestManager(t)
	ctx := context.Background()
	require.NoError(t, m.UpdateSecret(ctx, k, json.RawMessage(`{"pass":"hunter2"}`)))
	// A public-half write must merge with (not wipe) the secret half.
	require.NoError(t, m.Update(ctx, k, json.RawMessage(`{"host":"smtp.example.com","port":25}`)))
	v := k.Of(m.Snapshot(ctx))
	assert.Equal(t, "hunter2", v.Pass)
	assert.Equal(t, "smtp.example.com", v.Host)
}

func TestResetReturnsToDefault(t *testing.T) {
	m, k := newTestManager(t)
	ctx := context.Background()
	require.NoError(t, m.Update(ctx, k, json.RawMessage(`{"host":"x"}`)))
	require.NoError(t, m.Reset(ctx, k))
	assert.False(t, m.Snapshot(ctx).Customized(k))
	assert.Equal(t, 587, k.Of(m.Snapshot(ctx)).Port)
	assert.Empty(t, k.Of(m.Snapshot(ctx)).Host)
}

func TestInvalidRowDegradesToDefault(t *testing.T) {
	m, k := newTestManager(t)
	ctx := context.Background()
	// Write directly through the store, bypassing the validated write path.
	bad := `{"port":99999}`
	require.NoError(t, m.st.Settings().Upsert(ctx, upsertParams(k.Name(), bad, nil)))

	v := k.Of(m.Snapshot(ctx))
	assert.Equal(t, 587, v.Port, "an invalid stored row degrades to the default, never bricks startup")

	// Undecodable JSON degrades the same way.
	worse := `{not json`
	require.NoError(t, m.st.Settings().Upsert(ctx, upsertParams(k.Name(), worse, nil)))
	assert.Equal(t, 587, k.Of(m.Snapshot(ctx)).Port)
}

func TestOrphanRowIsIgnored(t *testing.T) {
	m, _ := newTestManager(t)
	ctx := context.Background()
	require.NoError(t, m.st.Settings().Upsert(ctx, upsertParams("removed.setting.key", `true`, nil)))
	snap := m.Snapshot(ctx)
	assert.Nil(t, snap.ValueOf(NewKey[bool]("removed.setting.key")), "an unknown key's row is inert")
}

func TestSetIfAbsentIsOneWinner(t *testing.T) {
	m, k := newTestManager(t)
	ctx := context.Background()
	require.NoError(t, k.SetIfAbsent(ctx, m, testValue{Host: "first", Port: 25}))
	require.NoError(t, k.SetIfAbsent(ctx, m, testValue{Host: "second", Port: 26}))
	assert.Equal(t, "first", k.Of(m.Snapshot(ctx)).Host, "the second provisioning attempt is discarded")
}

func TestSubscribeFiresOnChangeOnly(t *testing.T) {
	m, k := newTestManager(t)
	ctx := context.Background()

	var snaps []*Snapshot
	m.Subscribe(func(s *Snapshot) { snaps = append(snaps, s) })

	m.Snapshot(ctx) // TTL refresh without a change: no fire.
	require.Len(t, snaps, 0)

	require.NoError(t, m.Update(ctx, k, json.RawMessage(`{"host":"a"}`)))
	require.Len(t, snaps, 1)

	m.Snapshot(ctx) // cached, no fire
	require.Len(t, snaps, 1)

	require.NoError(t, m.Update(ctx, k, json.RawMessage(`{"host":"a"}`))) // same effective value
	require.Len(t, snaps, 1, "an identical rewrite is not a change")

	require.NoError(t, m.Update(ctx, k, json.RawMessage(`{"host":"b"}`)))
	require.Len(t, snaps, 2)
	assert.Equal(t, "b", k.Of(snaps[1]).Host)
}

func TestTTLRefreshPicksUpExternalWrites(t *testing.T) {
	// The admin CLI is a separate process: it writes rows directly and the
	// manager must converge on the next TTL expiry.
	st, err := sqlite.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	key, err := keystore.GenerateKey()
	require.NoError(t, err)
	ks, err := keystore.New(map[uint32][32]byte{1: key})
	require.NoError(t, err)

	k := testKey()
	now := time.Now()
	m := NewManager(st, ks, []Descriptor{k}, WithNow(func() time.Time { return now }))
	require.NoError(t, m.Load(context.Background()))
	ctx := context.Background()

	require.NoError(t, st.Settings().Upsert(ctx, upsertParams(k.Name(), `{"port":25}`, nil)))
	assert.Equal(t, 587, k.Of(m.Snapshot(ctx)).Port, "inside the TTL the snapshot is served")

	now = now.Add(2 * cacheTTL)
	assert.Equal(t, 25, k.Of(m.Snapshot(ctx)).Port, "past the TTL the write converges")
}

func TestSnapshotServesDefaultsBeforeLoad(t *testing.T) {
	st, err := sqlite.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	k := testKey()
	m := NewManager(st, nil, []Descriptor{k})
	snap := m.Snapshot(context.Background())
	assert.Equal(t, 587, k.Of(snap).Port, "a never-loaded manager still answers with defaults")
}

// flakySettingsStore fails every GetAll once tripped, standing in for a
// store outage mid-flight: the settings read path must degrade to the
// last good snapshot rather than erroring every request that trips the
// TTL.
type flakySettingsStore struct {
	store.Store
	failed atomic.Bool
}

func (s *flakySettingsStore) Settings() store.SettingsStore {
	return &flakySettings{SettingsStore: s.Store.Settings(), failed: &s.failed}
}

type flakySettings struct {
	store.SettingsStore
	failed *atomic.Bool
}

func (f *flakySettings) GetAll(ctx context.Context) ([]store.SettingRow, error) {
	if f.failed.Load() {
		return nil, errors.New("store down")
	}
	return f.SettingsStore.GetAll(ctx)
}

func TestSnapshotServesLastGoodOnStoreFailure(t *testing.T) {
	real, err := sqlite.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = real.Close() })
	key, err := keystore.GenerateKey()
	require.NoError(t, err)
	ks, err := keystore.New(map[uint32][32]byte{1: key})
	require.NoError(t, err)

	k := testKey()
	now := time.Now()
	st := &flakySettingsStore{Store: real}
	m := NewManager(st, ks, []Descriptor{k}, WithNow(func() time.Time { return now }))
	require.NoError(t, m.Load(context.Background()))
	ctx := context.Background()
	require.NoError(t, m.Update(ctx, k, json.RawMessage(`{"host":"smtp.example.com"}`)))
	assert.Equal(t, "smtp.example.com", k.Of(m.Snapshot(ctx)).Host)

	// The store goes away; the TTL expiry tries to refresh and fails. The
	// last good snapshot keeps serving, so a transient outage cannot turn
	// every settings read into an error.
	st.failed.Store(true)
	now = now.Add(2 * cacheTTL)
	snap := m.Snapshot(ctx)
	assert.Equal(t, "smtp.example.com", k.Of(snap).Host)

	// Recovery is equally silent: once the store answers again, the next
	// refresh picks up writes that landed meanwhile.
	require.NoError(t, real.Settings().Upsert(ctx, upsertParams(k.Name(), `{"host":"smtp2.example.com"}`, nil)))
	st.failed.Store(false)
	now = now.Add(2 * cacheTTL)
	assert.Equal(t, "smtp2.example.com", k.Of(m.Snapshot(ctx)).Host)
}

func TestUpdateSecretRefusesNonSecretKey(t *testing.T) {
	m, _ := newTestManager(t)
	k := NewKey[bool]("plain.bool")
	err := m.UpdateSecret(context.Background(), k, json.RawMessage(`true`))
	require.ErrorContains(t, err, "has no secret fields")
}

func TestEmailVerificationEffectiveDegradesWithoutSMTP(t *testing.T) {
	st, err := sqlite.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	key, err := keystore.GenerateKey()
	require.NoError(t, err)
	ks, err := keystore.New(map[uint32][32]byte{1: key})
	require.NoError(t, err)

	now := time.Now()
	m := NewManager(st, ks, []Descriptor{KeySMTP, KeyEmailVerificationRequired},
		WithCrossValidation(SMTPConfigured), WithNow(func() time.Time { return now }))
	require.NoError(t, m.Load(context.Background()))
	ctx := context.Background()

	// Out of the box: verification off, SMTP off.
	assert.False(t, EmailVerificationEffective(m.Snapshot(ctx)))

	// Verification on with SMTP configured: the cross rule allowed the
	// write, and the read agrees.
	require.NoError(t, m.Update(ctx, KeySMTP, json.RawMessage(`{"host":"smtp.example.com","from_address":"hub@example.com","port":587,"tls_mode":"starttls"}`)))
	require.NoError(t, m.Update(ctx, KeyEmailVerificationRequired, json.RawMessage(`true`)))
	assert.True(t, EmailVerificationEffective(m.Snapshot(ctx)))

	// SMTP removed behind the write path's back (direct SQL): the read
	// degrades verification to off rather than locking every signup
	// behind an email that can never be sent. The raw key still says
	// true; only the effective view degrades.
	require.NoError(t, st.Settings().Delete(ctx, KeySMTP.Name()))
	now = now.Add(2 * cacheTTL) // let the cached snapshot expire
	snap := m.Snapshot(ctx)
	assert.True(t, KeyEmailVerificationRequired.Of(snap),
		"the raw key still says true (only the write path was bypassed)")
	assert.False(t, EmailVerificationEffective(snap),
		"effective verification degrades once the SMTP removal is visible")
}

// upsertParams writes a row directly through the store, bypassing the
// manager's validated write path (the direct-SQL hazard the read path's
// degradation rules exist for).
func upsertParams(key, value string, secret []byte) store.UpsertSettingParams {
	p := store.UpsertSettingParams{Key: key, Secret: secret}
	if value != "" {
		v := value
		p.Value = &v
	}
	return p
}

// TestUpdateStoresExplicitZeroCap pins the round-trip of the one stored
// value omitempty would eat: an explicit 0 means "unlimited", and the
// stored document must carry it — otherwise the next decode merges the
// non-zero default back over it and the hub silently re-arms the cap the
// administrator lifted.
func TestUpdateStoresExplicitZeroCap(t *testing.T) {
	st, err := sqlite.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	m := NewManager(st, nil, []Descriptor{KeyLimits})
	require.NoError(t, m.Load(context.Background()))
	ctx := context.Background()

	require.NoError(t, m.Update(ctx, KeyLimits, json.RawMessage(`{"max_connections_per_user":0}`)))
	assert.EqualValues(t, 0, KeyLimits.Of(m.Snapshot(ctx)).MaxConnectionsPerUser,
		"an explicit 0 (unlimited) must survive the write")

	// The stored document itself carries the zero: a fresh manager over
	// the same rows reads it back, proving the round-trip rather than a
	// cached view.
	m2 := NewManager(st, nil, []Descriptor{KeyLimits})
	require.NoError(t, m2.Load(ctx))
	assert.EqualValues(t, 0, KeyLimits.Of(m2.Snapshot(ctx)).MaxConnectionsPerUser)
	assert.EqualValues(t, 64, KeyLimits.Of(m2.Snapshot(ctx)).MaxWorkersPerUser,
		"the untouched field keeps the declared default")
}

// TestUpdateRejectsUnknownFields pins the typo guard: a partial document
// whose every field name misses must fail the write instead of merging
// to the unchanged value and reporting success.
func TestUpdateRejectsUnknownFields(t *testing.T) {
	m, k := newTestManager(t)
	ctx := context.Background()
	require.NoError(t, m.Update(ctx, k, json.RawMessage(`{"host":"smtp.example.com"}`)))

	err := m.Update(ctx, k, json.RawMessage(`{"hast":"typo.example.com"}`))
	require.ErrorContains(t, err, "unknown field")
	assert.Equal(t, "smtp.example.com", k.Of(m.Snapshot(ctx)).Host, "a rejected write changes nothing")
}

// TestResetRunsCrossValidation pins that Reset cannot store the exact
// combination the update path refuses: dropping the smtp row while
// email_verification_required stays true must fail the same way
// `settings set smtp '{"host":""}'` does.
func TestResetRunsCrossValidation(t *testing.T) {
	m, _ := newTestManager(t)
	ctx := context.Background()
	require.NoError(t, m.Update(ctx, KeySMTP, json.RawMessage(`{"host":"smtp.example.com","from_address":"hub@example.com","port":587,"tls_mode":"starttls"}`)))
	require.NoError(t, m.Update(ctx, KeyEmailVerificationRequired, json.RawMessage(`true`)))

	err := m.Reset(ctx, KeySMTP)
	require.ErrorContains(t, err, "email_verification_required=true needs smtp host")

	// Lowering the requirement first makes the same reset succeed.
	require.NoError(t, m.Update(ctx, KeyEmailVerificationRequired, json.RawMessage(`false`)))
	require.NoError(t, m.Reset(ctx, KeySMTP))
	assert.False(t, m.Snapshot(ctx).Customized(KeySMTP))
}

// TestUpdateRefusesToDropUndecryptableSecret pins the secret-preservation
// rule: when the current keystore cannot decrypt a key's stored secret
// half, a public-field update must refuse (it would re-encrypt a default
// over the only ciphertext), while a document that supplies the secret
// fields explicitly is a rotation and is allowed.
func TestUpdateRefusesToDropUndecryptableSecret(t *testing.T) {
	st, err := sqlite.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	key1, err := keystore.GenerateKey()
	require.NoError(t, err)
	ks1, err := keystore.New(map[uint32][32]byte{1: key1})
	require.NoError(t, err)

	m1 := NewManager(st, ks1, []Descriptor{KeySMTP})
	require.NoError(t, m1.Load(context.Background()))
	ctx := context.Background()
	require.NoError(t, m1.Update(ctx, KeySMTP, json.RawMessage(`{"host":"smtp.example.com","from_address":"hub@example.com","port":587,"tls_mode":"starttls","password":"s3cret"}`)))
	require.Equal(t, "s3cret", KeySMTP.Of(m1.Snapshot(ctx)).Password)

	// A different key ring cannot decrypt the stored secret half.
	key2, err := keystore.GenerateKey()
	require.NoError(t, err)
	ks2, err := keystore.New(map[uint32][32]byte{1: key2})
	require.NoError(t, err)
	m2 := NewManager(st, ks2, []Descriptor{KeySMTP})
	require.NoError(t, m2.Load(ctx))

	err = m2.Update(ctx, KeySMTP, json.RawMessage(`{"port":25}`))
	require.ErrorContains(t, err, "cannot decrypt",
		"a public-field update must not silently replace an unreadable secret")
	require.Equal(t, "s3cret", KeySMTP.Of(m1.Snapshot(ctx)).Password,
		"the stored ciphertext survives the refused write")

	// An explicit rotation supplies the secret field and is allowed.
	require.NoError(t, m2.Update(ctx, KeySMTP, json.RawMessage(`{"port":25,"password":"rotated"}`)))
	require.Equal(t, "rotated", KeySMTP.Of(m2.Snapshot(ctx)).Password)
}

// TestPublishRefreshDropsOvertakenSnapshot pins the publish ordering: a
// refresh that started reading before a write committed must not
// overwrite the fresher snapshot that write already published, or the
// served state would revert to pre-write values for a further TTL.
func TestPublishRefreshDropsOvertakenSnapshot(t *testing.T) {
	m, k := newTestManager(t)
	ctx := context.Background()

	// The state an in-flight refresh read before the write committed.
	require.NoError(t, m.Update(ctx, k, json.RawMessage(`{"host":"old.example.com"}`)))
	stale := m.Snapshot(ctx)
	oldEpoch := m.currentEpoch()

	// The write commits and publishes: the value moves, the epoch advances.
	require.NoError(t, m.Update(ctx, k, json.RawMessage(`{"host":"fresh.example.com"}`)))
	assert.Equal(t, "fresh.example.com", k.Of(m.Snapshot(ctx)).Host)

	// The in-flight refresh finishes late with its pre-write view: it is
	// dropped instead of reverting the served state for a further TTL.
	m.publishRefresh(stale, oldEpoch)
	assert.Equal(t, "fresh.example.com", k.Of(m.Snapshot(ctx)).Host,
		"an overtaken refresh must not revert the published state")

	// A current-epoch refresh publishes as before.
	m.publishRefresh(m.buildSnapshotFromStore(ctx), m.currentEpoch())
	assert.Equal(t, "fresh.example.com", k.Of(m.Snapshot(ctx)).Host)
}

// TestWarnOnceClearsOnRecovery pins the warn-on-transition contract: a
// row that goes bad, is fixed, and goes bad again warns TWICE — a
// once-per-process tag would hide the second regression, which is
// exactly the one an operator did not already investigate.
func TestWarnOnceClearsOnRecovery(t *testing.T) {
	var warns atomic.Int64
	counting := &countingHandler{count: &warns}
	prev := slog.Default()
	slog.SetDefault(slog.New(counting))
	t.Cleanup(func() { slog.SetDefault(prev) })

	st, err := sqlite.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	now := time.Now()
	m := NewManager(st, nil, []Descriptor{k2()}, WithNow(func() time.Time { return now }), WithTTL(time.Minute))
	require.NoError(t, m.Load(context.Background()))
	ctx := context.Background()

	bad := `{"port":99999}`
	require.NoError(t, st.Settings().Upsert(ctx, upsertParams("test.obj", bad, nil)))
	now = now.Add(2 * time.Minute)
	m.Snapshot(ctx)
	assert.EqualValues(t, 1, warns.Load(), "the bad row warns once")

	// Fix the row; the next refresh stays silent.
	good := `{"port":25}`
	require.NoError(t, st.Settings().Upsert(ctx, upsertParams("test.obj", good, nil)))
	now = now.Add(2 * time.Minute)
	m.Snapshot(ctx)
	assert.EqualValues(t, 1, warns.Load(), "a healthy row adds no warning")

	// Break it again: the regression warns again.
	require.NoError(t, st.Settings().Upsert(ctx, upsertParams("test.obj", bad, nil)))
	now = now.Add(2 * time.Minute)
	m.Snapshot(ctx)
	assert.EqualValues(t, 2, warns.Load(), "the second regression warns again")
}

// k2 is a second test key with a validator whose failures exercise the
// warn-once machinery without the cross rules of the shared testKey.
func k2() *Key[testValue] {
	return NewKey[testValue]("test.obj").
		WithDefault(testValue{Port: 587}).
		WithValidate(func(v testValue) error {
			if v.Port < 1 || v.Port > 65535 {
				return errTest("port out of range")
			}
			return nil
		})
}

type countingHandler struct {
	count *atomic.Int64
}

func (h *countingHandler) Enabled(_ context.Context, l slog.Level) bool { return l >= slog.LevelWarn }
func (h *countingHandler) Handle(_ context.Context, _ slog.Record) error {
	h.count.Add(1)
	return nil
}
func (h *countingHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *countingHandler) WithGroup(_ string) slog.Handler      { return h }

// TestSignupEnabledEffectiveDevDefaultNotCustomized pins the dev-mode
// signup default resolved at read time: dev mode serves open signup
// WITHOUT storing a row (a row stays an operator write), an explicit
// operator write wins, and a reset restores the dev default without a
// restart.
func TestSignupEnabledEffectiveDevDefaultNotCustomized(t *testing.T) {
	st, err := sqlite.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	m := NewManager(st, nil, CoreDescriptors())
	require.NoError(t, m.Load(context.Background()))
	ctx := context.Background()

	snap := m.Snapshot(ctx)
	assert.False(t, snap.Customized(KeySignupEnabled), "the dev default must not store a row")
	assert.True(t, SignupEnabledEffective(snap, true), "dev mode defaults to open signup")
	assert.False(t, SignupEnabledEffective(snap, false), "the production default is closed signup")

	require.NoError(t, KeySignupEnabled.Set(ctx, m, false))
	snap = m.Snapshot(ctx)
	assert.True(t, snap.Customized(KeySignupEnabled))
	assert.False(t, SignupEnabledEffective(snap, true), "an explicit operator write wins over the dev default")

	require.NoError(t, m.Reset(ctx, KeySignupEnabled))
	snap = m.Snapshot(ctx)
	assert.False(t, snap.Customized(KeySignupEnabled), "reset returns the key to its rowless default")
	assert.True(t, SignupEnabledEffective(snap, true), "reset in dev restores the dev default without a restart")
}
