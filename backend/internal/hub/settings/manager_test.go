package settings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
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
	m, k, _ := newTestManagerWithStore(t)
	return m, k
}

// newTestManagerWithStore also hands back the store, for a test that must
// read the stored ROW rather than the manager's snapshot. The snapshot is
// cached, so it can agree with the expectation for the wrong reason.
func newTestManagerWithStore(t *testing.T) (*Manager, *Key[testValue], store.Store) {
	t.Helper()
	st, err := sqlite.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	key, err := keystore.GenerateKey()
	require.NoError(t, err)
	ks, err := keystore.New(map[uint32][32]byte{1: key})
	require.NoError(t, err)

	k := testKey()
	m := NewManager(st, ks, []Descriptor{k, KeySMTP})
	require.NoError(t, m.Load(context.Background()))
	return m, k, st
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

	m := NewManager(st, ks, []Descriptor{KeySMTP})
	require.NoError(t, m.Load(context.Background()))
	ctx := context.Background()

	assert.False(t, EmailVerificationEffective(m.Snapshot(ctx)))

	require.NoError(t, m.Update(ctx, KeySMTP, json.RawMessage(`{"host":"smtp.example.com","from_address":"hub@example.com","port":587,"tls_mode":"starttls"}`)))
	assert.True(t, EmailVerificationEffective(m.Snapshot(ctx)))

	require.NoError(t, m.Reset(ctx, KeySMTP))
	assert.False(t, EmailVerificationEffective(m.Snapshot(ctx)))
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

// TestResetClearsSMTPRow pins that Reset removes a customized SMTP row and
// returns the setting to its code default (verification gate off).
func TestResetClearsSMTPRow(t *testing.T) {
	m, _ := newTestManager(t)
	ctx := context.Background()
	require.NoError(t, m.Update(ctx, KeySMTP, json.RawMessage(`{"host":"smtp.example.com","from_address":"hub@example.com","port":587,"tls_mode":"starttls"}`)))
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

// undecryptableSMTPManager seeds an SMTP row whose secret half one key ring
// encrypted, then returns a manager whose ring cannot read it -- the state
// an operator reaches after a key version leaves the ring.
func undecryptableSMTPManager(t *testing.T) (*Manager, context.Context) {
	t.Helper()
	st, err := sqlite.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()

	key1, err := keystore.GenerateKey()
	require.NoError(t, err)
	ks1, err := keystore.New(map[uint32][32]byte{1: key1})
	require.NoError(t, err)
	seeder := NewManager(st, ks1, []Descriptor{KeySMTP})
	require.NoError(t, seeder.Load(ctx))
	require.NoError(t, seeder.Update(ctx, KeySMTP, json.RawMessage(
		`{"host":"smtp.example.com","from_address":"hub@example.com","password":"original"}`)))

	key2, err := keystore.GenerateKey()
	require.NoError(t, err)
	ks2, err := keystore.New(map[uint32][32]byte{1: key2})
	require.NoError(t, err)
	m := NewManager(st, ks2, []Descriptor{KeySMTP})
	require.NoError(t, m.Load(ctx))
	return m, ctx
}

// TestSecretRotationIsAcceptedThroughEitherHalf pins the rotation test in
// mergeForUpdate, which reads BOTH halves of a KeyWrite.
//
// Which half a rotation arrives in is the VERB's choice, not the operator's:
// Update sends its whole document in KeyWrite.Public and UpdateSecret sends
// its own in KeyWrite.Secret. A test that reads one half therefore refuses
// every rotation that arrives through the other verb -- the operator is told
// to restore the key version or run the reencrypt command although the
// document already carries the replacement secret.
//
// Each case starts from a REFUSED public-field write, so the fixture is
// proven undecryptable before the rotation is accepted.
func TestSecretRotationIsAcceptedThroughEitherHalf(t *testing.T) {
	for _, tc := range []struct {
		name  string
		write func(m *Manager, ctx context.Context, doc json.RawMessage) error
	}{
		{"Update carries it in the public half", func(m *Manager, ctx context.Context, doc json.RawMessage) error {
			return m.Update(ctx, KeySMTP, doc)
		}},
		{"UpdateSecret carries it in the secret half", func(m *Manager, ctx context.Context, doc json.RawMessage) error {
			return m.UpdateSecret(ctx, KeySMTP, doc)
		}},
		{"UpdateMany carries it in the public half", func(m *Manager, ctx context.Context, doc json.RawMessage) error {
			return m.UpdateMany(ctx, []KeyWrite{{Desc: KeySMTP, Public: doc}})
		}},
		{"UpdateMany carries it in the secret half", func(m *Manager, ctx context.Context, doc json.RawMessage) error {
			return m.UpdateMany(ctx, []KeyWrite{{Desc: KeySMTP, Secret: doc}})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, ctx := undecryptableSMTPManager(t)

			// The control: a document with no secret field must still refuse,
			// or the case below proves nothing about the rotation.
			err := m.Update(ctx, KeySMTP, json.RawMessage(`{"port":25}`))
			require.ErrorContains(t, err, "cannot decrypt",
				"a public-field write must not replace an unreadable secret")
			var undecryptable *SecretUndecryptableError
			require.ErrorAs(t, err, &undecryptable,
				"the refusal is FailedPrecondition, not the caller's input")

			require.NoError(t, tc.write(m, ctx, json.RawMessage(`{"password":"rotated"}`)),
				"a document that supplies the secret field is a rotation, whichever half carries it")
			assert.Equal(t, "rotated", KeySMTP.Of(m.Snapshot(ctx)).Password)
			assert.Equal(t, "smtp.example.com", KeySMTP.Of(m.Snapshot(ctx)).Host,
				"the rotation keeps the public fields the stored row already held")
		})
	}
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

// The admin RPC surface classifies write failures by TYPE, not by message
// text. These assertions are what make that safe: matching on a substring
// meant every one of these error strings was load-bearing, and rewording
// one silently downgraded an actionable InvalidArgument to a 500 with no
// signal at the edit site.
func TestManagerWriteErrorsAreTyped(t *testing.T) {
	ctx := context.Background()
	m, k := newTestManager(t)

	t.Run("an unregistered key", func(t *testing.T) {
		unknown := NewKey[testValue]("never.registered").WithDefault(testValue{Port: 1})
		var invalid *InvalidError
		assert.ErrorAs(t, m.Update(ctx, unknown, json.RawMessage(`{"port":25}`)), &invalid,
			"an unregistered key is the caller's mistake, not a store fault")
	})

	t.Run("a value the key's validator refuses", func(t *testing.T) {
		var invalid *InvalidError
		err := m.Update(ctx, k, json.RawMessage(`{"port":70000}`))
		require.ErrorAs(t, err, &invalid)
		assert.Contains(t, err.Error(), "port out of range", "the cause stays readable")
		assert.NotNil(t, invalid.Unwrap(), "and reachable through Unwrap")
	})

	t.Run("a partial document that will not merge", func(t *testing.T) {
		var invalid *InvalidError
		assert.ErrorAs(t, m.Update(ctx, k, json.RawMessage(`{"port":"not-a-number"}`)), &invalid)
	})

	t.Run("a secret write to a key with no secret half", func(t *testing.T) {
		plain := NewKey[bool]("test.plain").WithDefault(false)
		var invalid *InvalidError
		assert.ErrorAs(t, m.UpdateSecret(ctx, plain, json.RawMessage(`{"x":"y"}`)), &invalid)
	})
}

// TestUpdateManyLandsEveryKeyWithItsOwnDocument is the atomic verb's
// happy path: several keys, DIFFERENT documents, one transaction.
//
// The distinct documents are the point. Every earlier case writes the same
// value to each key, so a merge step that carried one key's document into
// another's row would pass them all. The single-key verbs route through this
// loop now, which makes the per-key merge the whole write surface's.
func TestUpdateManyLandsEveryKeyWithItsOwnDocument(t *testing.T) {
	m, k, _ := newTestManagerWithStore(t)
	ctx := context.Background()

	require.NoError(t, m.UpdateMany(ctx, []KeyWrite{
		// A key whose two halves travel together, which is what lets a site
		// key and its secret land together or not at all.
		{Desc: k, Public: json.RawMessage(`{"host":"first.example"}`), Secret: json.RawMessage(`{"pass":"s3cret"}`)},
		{Desc: KeySMTP, Public: json.RawMessage(`{"host":"second.example","port":2525}`)},
	}))

	snap := m.Snapshot(ctx)
	got := k.Of(snap)
	assert.Equal(t, "first.example", got.Host, "the first key keeps its own document")
	assert.Equal(t, "s3cret", got.Pass, "including the half that travelled beside it")
	assert.Equal(t, 587, got.Port, "and the fields neither half specified stay at the default")

	smtp := KeySMTP.Of(snap)
	assert.Equal(t, "second.example", smtp.Host, "the second key keeps its own")
	assert.Equal(t, 2525, smtp.Port)

	assert.True(t, snap.Customized(k))
	assert.True(t, snap.Customized(KeySMTP))
}

// TestUpdateManyRefusesADuplicateKey pins the one shape the atomic write
// cannot resolve: two edits to the same key in one request. Applying them
// in order would hide one behind the other, and refusing says so.
func TestUpdateManyRefusesADuplicateKey(t *testing.T) {
	m, k := newTestManager(t)
	ctx := context.Background()

	err := m.UpdateMany(ctx, []KeyWrite{
		{Desc: k, Public: json.RawMessage(`{"host":"a"}`)},
		{Desc: k, Public: json.RawMessage(`{"host":"b"}`)},
	})
	require.ErrorContains(t, err, "appears twice")

	var invalid *InvalidError
	require.ErrorAs(t, err, &invalid, "a caller-supplied shape is InvalidArgument, not a store fault")

	require.ErrorContains(t, m.UpdateMany(ctx, nil), "no settings writes")
}

// TestUpdateManyValidatesEveryKeyBeforeWritingAny pins that a validation
// failure on the LAST write leaves the earlier ones unstored.
//
// The FIRST write must merge cleanly, or the test proves nothing: UpdateMany
// returns on the first error, so a first write that is itself refused never
// reaches the second key and never exercises the rollback.
//
// Two mechanisms hold the guarantee, and either one alone is sufficient:
// UpdateMany merges every key before it upserts any, and the whole body runs
// in one transaction. A negative check that breaks only the ordering cannot
// make this test fail, because the rollback still hides the write. Breaking
// the transaction as well is impossible here: the sqlite store serializes on
// one connection, so a write issued around the open transaction deadlocks
// instead of persisting.
func TestUpdateManyValidatesEveryKeyBeforeWritingAny(t *testing.T) {
	m, k, st := newTestManagerWithStore(t)
	ctx := context.Background()

	before := k.Of(m.Snapshot(ctx))
	require.NotEqual(t, "applied.example.com", before.Host, "the fixture must start elsewhere")

	err := m.UpdateMany(ctx, []KeyWrite{
		// A clean merge onto a declared field.
		{Desc: k, Public: json.RawMessage(`{"host":"applied.example.com"}`)},
		// An unknown field name: ApplyPartial refuses it.
		{Desc: KeySMTP, Public: json.RawMessage(`{"nope":1}`)},
	})
	require.ErrorContains(t, err, "nope")

	assert.Equal(t, before, k.Of(m.Snapshot(ctx)),
		"a key that merged cleanly must not be stored when a later one fails")
	// And the row itself, not only the cached snapshot: an in-memory
	// rollback that left the transaction committed would pass the line above.
	_, getErr := st.Settings().Get(ctx, k.Name())
	assert.ErrorIs(t, getErr, store.ErrNotFound, "the first write must leave no stored row behind")
}

// TestUpdateManyRefusesASecretHalfForAKeyWithNoSecretFields keeps the
// atomic verb's secret rule identical to UpdateSecret's.
//
// mergeForUpdate overlays Secret through the same loop it overlays Public
// with, so a secret half sent to a key that declares no secret field was
// applied as if it were public -- through the one verb that never checked
// whether the key HAS an encrypted half. The refusal belongs in the
// manager, not in a handler, so every caller of the verb is covered.
func TestUpdateManyRefusesASecretHalfForAKeyWithNoSecretFields(t *testing.T) {
	st, err := sqlite.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	key, err := keystore.GenerateKey()
	require.NoError(t, err)
	ks, err := keystore.New(map[uint32][32]byte{1: key})
	require.NoError(t, err)

	m := NewManager(st, ks, []Descriptor{KeySignupEnabled, testKey()})
	require.NoError(t, m.Load(context.Background()))
	ctx := context.Background()
	partial := json.RawMessage(`{"pass":"hunter2"}`)

	err = m.UpdateMany(ctx, []KeyWrite{
		{Desc: KeySignupEnabled, Secret: partial},
	})
	require.Error(t, err)
	var invalid *InvalidError
	require.ErrorAs(t, err, &invalid, "a caller-supplied shape is InvalidArgument, not a store fault")

	single := m.UpdateSecret(ctx, KeySignupEnabled, partial)
	require.Error(t, single)
	assert.Equal(t, single.Error(), err.Error(),
		"the two verbs must refuse the same input with the same words")

	assert.False(t, m.Snapshot(ctx).Customized(KeySignupEnabled),
		"the refused write must store nothing")

	// A key that DOES declare secret fields still takes the same document.
	k := testKey()
	require.NoError(t, m.UpdateMany(ctx, []KeyWrite{{Desc: k, Secret: partial}}))
	assert.Equal(t, "hunter2", k.Of(m.Snapshot(ctx)).Pass)
}

// TestUpdateManyRefusesAnUnregisteredKey keeps the atomic path's argument
// checks identical to the single-key path's.
func TestUpdateManyRefusesAnUnregisteredKey(t *testing.T) {
	m, _ := newTestManager(t)
	stranger := NewKey[testValue]("not.registered")

	err := m.UpdateMany(context.Background(), []KeyWrite{
		{Desc: stranger, Public: json.RawMessage(`{"host":"x"}`)},
	})
	require.ErrorContains(t, err, "is not registered")
}

// TestEveryWriteVerbRefusesAnInertPartialDocument pins the empty-document
// rule over the WHOLE partial-document surface at once.
//
// A clean merge is not a no-op: the write path upserts the row anyway, so
// the key starts reporting customized=true and the surface offers a reset
// for a change that never happened. Every shape in the table arrives from a
// real client -- an omitted proto3 string is "", the preferences dialog can
// send {} for an object-shaped key, and null is what a stale client sends
// for "leave it alone".
//
// The table crosses every verb with every shape, so one message and one
// classification cover the surface. Update and UpdateSecret reach the rule
// through UpdateMany, and UpdateSecret ALSO calls it itself: its own
// secret-field check finds no secret field in an empty document, so it must
// answer the empty-document message rather than the secret-field one.
//
// SetValue and SetIfAbsent are deliberately absent. They take a COMPLETE
// value, so they carry no partial document that can be empty; their
// analogous refusal is the empty-ROW one that rowParams owns.
func TestEveryWriteVerbRefusesAnInertPartialDocument(t *testing.T) {
	m, k, st := newTestManagerWithStore(t)
	ctx := context.Background()

	verbs := []struct {
		name  string
		write func(json.RawMessage) error
	}{
		{"Update", func(p json.RawMessage) error { return m.Update(ctx, k, p) }},
		{"UpdateSecret", func(p json.RawMessage) error { return m.UpdateSecret(ctx, k, p) }},
		{"UpdateMany public half", func(p json.RawMessage) error {
			return m.UpdateMany(ctx, []KeyWrite{{Desc: k, Public: p}})
		}},
		{"UpdateMany secret half", func(p json.RawMessage) error {
			return m.UpdateMany(ctx, []KeyWrite{{Desc: k, Secret: p}})
		}},
	}
	documents := []struct {
		name string
		doc  string
		want string
	}{
		{"absent", "", "the partial document is required"},
		{"whitespace only", "   ", "the partial document is required"},
		{"empty object", "{}", "specifies no field"},
		{"null", "null", "specifies no field"},
	}
	for _, verb := range verbs {
		for _, doc := range documents {
			t.Run(verb.name+"/"+doc.name, func(t *testing.T) {
				err := verb.write(json.RawMessage(doc.doc))
				require.Error(t, err)
				assert.Contains(t, err.Error(), doc.want,
					"every entry point refuses the same shape with the same words")
				var invalid *InvalidError
				assert.ErrorAs(t, err, &invalid,
					"an inert document is the caller's mistake, not a store fault")

				_, getErr := st.Settings().Get(ctx, k.Name())
				assert.ErrorIs(t, getErr, store.ErrNotFound, "an inert document writes no row")
				assert.False(t, m.Snapshot(ctx).Customized(k), "and never flips customized")
			})
		}
	}

	// A KeyWrite that carries NEITHER half is the same refusal. Only the
	// multi-key verb can express it; the single-key verbs always fill one.
	t.Run("UpdateMany with neither half", func(t *testing.T) {
		err := m.UpdateMany(ctx, []KeyWrite{{Desc: k}})
		require.ErrorContains(t, err, "the partial document is required")
		var invalid *InvalidError
		assert.ErrorAs(t, err, &invalid)
		assert.False(t, m.Snapshot(ctx).Customized(k))
	})

	// The positive control: a document that really specifies a field is
	// still stored, so the table above is not refusing everything.
	require.NoError(t, m.Update(ctx, k, json.RawMessage(`{"port":2525}`)))
	assert.True(t, m.Snapshot(ctx).Customized(k))
	assert.Equal(t, 2525, k.Of(m.Snapshot(ctx)).Port)
}

// TestUpdateSecretRefusesADocumentWithNoSecretField pins the secret verb's
// own rule: the surface documents it as writing the ENCRYPTED half, and a
// document that specifies only public fields would rewrite them through
// the verb whose whole purpose is the secret.
func TestUpdateSecretRefusesADocumentWithNoSecretField(t *testing.T) {
	m, k, st := newTestManagerWithStore(t)
	ctx := context.Background()

	err := m.UpdateSecret(ctx, k, json.RawMessage(`{"host":"evil.example"}`))
	require.Error(t, err)
	var invalid *InvalidError
	require.ErrorAs(t, err, &invalid)
	assert.Contains(t, err.Error(), "must specify at least one of")
	_, err = st.Settings().Get(ctx, k.Name())
	assert.ErrorIs(t, err, store.ErrNotFound, "the refused write must store nothing")

	// A document that does specify the secret field is accepted, including
	// beside a public one.
	require.NoError(t, m.UpdateSecret(ctx, k, json.RawMessage(`{"pass":"s3cret"}`)))
	assert.Equal(t, "s3cret", k.Of(m.Snapshot(ctx)).Pass)
}

// TestUpdateMergesOntoTheDegradedBaseWhenTheStoredRowIsInvalid pins the
// read/write symmetry. The snapshot degrades a row its own validator
// refuses to the code default, so the write path must start from that same
// base -- otherwise a row written outside the write path (direct SQL, or
// an older hub whose rules were wider) makes the key permanently
// unwritable while the surface shows the default.
//
// The decrypted secret half must SURVIVE the degrade, so recovering a
// public field never overwrites an operator-entered secret with a default.
func TestUpdateMergesOntoTheDegradedBaseWhenTheStoredRowIsInvalid(t *testing.T) {
	m, k, st := newTestManagerWithStore(t)
	ctx := context.Background()

	// A healthy write first, so the row carries a real encrypted secret.
	require.NoError(t, m.Update(ctx, k, json.RawMessage(`{"host":"a.example","port":25,"pass":"s3cret"}`)))

	// Now corrupt the PUBLIC half behind the write path's back, exactly as
	// direct SQL would: port 70000 is outside the validator's range.
	row, err := st.Settings().Get(ctx, k.Name())
	require.NoError(t, err)
	bad := `{"host":"a.example","port":70000}`
	require.NoError(t, st.Settings().Upsert(ctx, store.UpsertSettingParams{
		Key: k.Name(), Value: &bad, Secret: row.Secret,
	}))

	// The read path already degrades to the default.
	m.mu.Lock()
	m.snap = nil
	m.mu.Unlock()
	assert.Equal(t, 587, k.Of(m.Snapshot(ctx)).Port, "the read path degrades an invalid row")

	// The write path must accept a write onto that same base.
	require.NoError(t, m.Update(ctx, k, json.RawMessage(`{"host":"b.example"}`)))
	got := k.Of(m.Snapshot(ctx))
	assert.Equal(t, "b.example", got.Host)
	assert.Equal(t, 587, got.Port, "the merge starts from the default the reader saw")
	assert.Equal(t, "s3cret", got.Pass, "the decrypted secret half survives the degrade")
}

// lockOrderKeys returns two int keys whose names put "aaa" before "bbb" in
// sort order, which is what makes the canonical upsert order observable.
//
// The typed handles, rather than plain Descriptors, are what let the verb
// table below reach Set and SetIfAbsent: both take the key's own value type.
func lockOrderKeys() (*Key[int64], *Key[int64]) {
	a := NewKey[int64]("test.aaa").WithDefault(int64(1)).
		WithUI(UIMeta{Category: "general", Title: "A", Fields: []Field{{Kind: FieldInt}}})
	b := NewKey[int64]("test.bbb").WithDefault(int64(1)).
		WithUI(UIMeta{Category: "general", Title: "B", Fields: []Field{{Kind: FieldInt}}})
	return a, b
}

// newLockOrderManager builds a manager over the recording store with the two
// int keys the lock-order tests write, and drops what Load recorded so each
// test reads only its own write.
func newLockOrderManager(t *testing.T) (*Manager, *lockOrderStore, *Key[int64], *Key[int64]) {
	t.Helper()
	st, err := sqlite.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	tracker := &lockOrderStore{Store: st, rec: &lockRecorder{}}

	a, b := lockOrderKeys()
	m := NewManager(tracker, nil, []Descriptor{a, b})
	require.NoError(t, m.Load(context.Background()))
	tracker.rec.reset()
	return m, tracker, a, b
}

// TestUpdateManyTakesTheTableLockFirstThenWritesInKeyOrder pins the write
// path's lock order, which is the whole defence against a deadlock between
// two concurrent writers.
//
// TWO rules, and the first is the load-bearing one. (1) The transaction
// takes the whole-table lock FIRST and takes no other settings lock. A
// per-key row lock taken before it lets two writers form a cycle -- one
// holds test.bbb and scans toward test.aaa while the other holds test.aaa
// and scans toward test.bbb -- which Postgres and MySQL end by aborting
// one of the pair. (2) The upserts then run in key order, because a key
// with no row yet takes no lock from the table read, so two racing INSERTs
// of the same pair could still deadlock on the new rows.
//
// The trailing "get-all" is the post-commit reload, which runs OUTSIDE the
// transaction and takes no lock.
func TestUpdateManyTakesTheTableLockFirstThenWritesInKeyOrder(t *testing.T) {
	m, tracker, a, b := newLockOrderManager(t)

	require.NoError(t, m.UpdateMany(context.Background(), []KeyWrite{
		{Desc: b, Public: json.RawMessage(`2`)},
		{Desc: a, Public: json.RawMessage(`2`)},
	}))
	assert.Equal(t,
		[]string{"lock-all", "upsert:test.aaa", "upsert:test.bbb", "get-all"},
		tracker.rec.locked(),
		"the table lock comes first, then the upserts in key order, not in the caller's order")
}

// TestEveryWriteVerbTakesTheTableLockFirst pins the lock order over the
// WHOLE write surface, not only the two partial-document verbs.
//
// FIVE verbs reach writeInTransaction: Update and UpdateMany merge partial
// documents, SetValue and SetIfAbsent carry a complete value, and ResetMany
// deletes. They share one transaction body, so a per-key row lock re-added
// to any one of them forms the identical cycle against every other one --
// two writers, one holding test.bbb and scanning toward test.aaa while the
// other holds test.aaa and scans toward test.bbb. UpdateSecret is absent
// because it has no transaction of its own: it validates its document and
// then calls UpdateMany, which the table covers.
//
// The expected sequence is the COMPLETE recorded call list, so a second
// settings read slipped in front of the table lock shows up as an extra
// entry rather than passing unseen.
func TestEveryWriteVerbTakesTheTableLockFirst(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		// seed stores a row for both keys before the recorder starts, for a
		// verb whose call sequence depends on the row already existing.
		seed  bool
		write func(m *Manager, a, b *Key[int64]) error
		want  []string
	}{
		{"Update", false, func(m *Manager, _, b *Key[int64]) error {
			return m.Update(ctx, b, json.RawMessage(`2`))
		}, []string{"lock-all", "upsert:test.bbb", "get-all"}},
		{"UpdateMany", false, func(m *Manager, a, b *Key[int64]) error {
			return m.UpdateMany(ctx, []KeyWrite{
				{Desc: b, Public: json.RawMessage(`2`)},
				{Desc: a, Public: json.RawMessage(`2`)},
			})
		}, []string{"lock-all", "upsert:test.aaa", "upsert:test.bbb", "get-all"}},
		{"SetValue", false, func(m *Manager, _, b *Key[int64]) error {
			return b.Set(ctx, m, 2)
		}, []string{"lock-all", "upsert:test.bbb", "get-all"}},
		{"SetIfAbsent", false, func(m *Manager, _, b *Key[int64]) error {
			return b.SetIfAbsent(ctx, m, 2)
		}, []string{"lock-all", "insert-if-absent:test.bbb", "get-all"}},
		// ResetMany deletes in the CALLER's order, not in key order, and
		// that is safe for the reason the upserts are not: every row it
		// deletes already exists, so the table lock the transaction opened
		// with holds it. An upsert can create a row the table read never
		// locked, which is why UpdateMany sorts and this verb does not.
		{"ResetMany", true, func(m *Manager, a, b *Key[int64]) error {
			return m.ResetMany(ctx, []Descriptor{b, a})
		}, []string{"lock-all", "delete:test.bbb", "delete:test.aaa", "get-all"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, tracker, a, b := newLockOrderManager(t)
			if tc.seed {
				require.NoError(t, m.UpdateMany(ctx, []KeyWrite{
					{Desc: a, Public: json.RawMessage(`2`)},
					{Desc: b, Public: json.RawMessage(`2`)},
				}))
				tracker.rec.reset()
			}
			require.NoError(t, tc.write(m, a, b))
			assert.Equal(t, tc.want, tracker.rec.locked(),
				"the table lock comes first, and the transaction takes no other settings lock")
		})
	}
}

// TestSettingsStoreDeclaresExactlyOneLockingRead is the mechanical guard
// behind every lock-order assertion above.
//
// The recorded sequences prove that no CALL takes a second settings lock.
// This one fails at the DECLARATION of a re-added per-key locking read --
// the exact method this write path deleted -- before any call site exists.
// A locking read is what forms the cycle two writers deadlock on, so the
// store must offer exactly one, and it must be the whole-table one.
func TestSettingsStoreDeclaresExactlyOneLockingRead(t *testing.T) {
	typ := reflect.TypeOf((*store.SettingsStore)(nil)).Elem()
	var locking []string
	for i := range typ.NumMethod() {
		if name := typ.Method(i).Name; strings.HasSuffix(name, "ForUpdate") {
			locking = append(locking, name)
		}
	}
	assert.Equal(t, []string{"GetAllForUpdate"}, locking,
		"the settings store must offer ONE locking read, over the whole table")
}

// TestConcurrentOppositeKeySetWritesBothComplete runs the shape that
// deadlocked: two multi-key writes over the same pair, listed in opposite
// orders, at the same time.
//
// SQLite serializes its writers, so this cannot REPRODUCE the deadlock a
// row-lock-first order causes on Postgres and MySQL -- it pins that both
// writers commit and that neither loses the other's key.
//
// The wait carries a deadline, because a deadlock is the failure this test
// exists to report. An unguarded WaitGroup answers that failure with a hung
// test binary and a package-wide panic dump ten minutes later, which reports
// nothing about this test. The deadline is generous on purpose: it turns a
// hang into a failure at this line, and it measures nothing about how long
// two contending writers take.
//
// The value assertion accepts EITHER writer's pair, because both writers
// race and either one may commit last. That leaves the published ORDER
// unpinned here, and reloadMu is what holds it; the deterministic test for
// that rule is
// TestPublishFromStoreHoldsTheReloadLockAcrossTheReadAndThePublish.
func TestConcurrentOppositeKeySetWritesBothComplete(t *testing.T) {
	dir := t.TempDir()
	st, err := sqlite.Open(filepath.Join(dir, "settings.db"), sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	a, b := lockOrderKeys()
	m := NewManager(st, nil, []Descriptor{a, b})
	require.NoError(t, m.Load(context.Background()))

	// Both keys already have a row, so the table lock covers them and the
	// writers contend on the lock rather than on two inserts.
	require.NoError(t, m.UpdateMany(context.Background(), []KeyWrite{
		{Desc: a, Public: json.RawMessage(`1`)},
		{Desc: b, Public: json.RawMessage(`1`)},
	}))

	var wg sync.WaitGroup
	errs := make([]error, 2)
	start := make(chan struct{})
	for i, writes := range [][]KeyWrite{
		{{Desc: a, Public: json.RawMessage(`2`)}, {Desc: b, Public: json.RawMessage(`2`)}},
		{{Desc: b, Public: json.RawMessage(`3`)}, {Desc: a, Public: json.RawMessage(`3`)}},
	} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs[i] = m.UpdateMany(context.Background(), writes)
		}()
	}
	close(start)
	finished := make(chan struct{})
	go func() {
		wg.Wait()
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(deadlockDeadline):
		t.Fatal("the two writers never both returned: the write path deadlocked")
	}

	require.NoError(t, errs[0])
	require.NoError(t, errs[1])
	snap := m.Snapshot(context.Background())
	got := []any{snap.ValueOf(a), snap.ValueOf(b)}
	assert.Contains(t, [][]any{{int64(2), int64(2)}, {int64(3), int64(3)}}, got,
		"the writes are atomic, so one writer's pair wins whole")
}

// TestEffectiveReportsTheKeysReadTimeRule pins where a read-time override
// lives: on the KEY, through the manager, not in the surface that reports
// it. Every admin surface asks the manager the same question, so two of them
// cannot report different values for one key.
func TestEffectiveReportsTheKeysReadTimeRule(t *testing.T) {
	ctx := context.Background()

	t.Run("a rule that applies replaces the stored-merged value", func(t *testing.T) {
		m, k, _ := newTestManagerWithStore(t)
		m.Configure(WithEffective(k.Name(), func(*Snapshot) (any, bool) {
			return testValue{Host: "resolved.example", Port: 2525}, true
		}))
		require.NoError(t, m.Update(ctx, k, json.RawMessage(`{"host":"stored.example"}`)))

		snap := m.Snapshot(ctx)
		assert.Equal(t, testValue{Host: "stored.example", Port: 587}, snap.ValueOf(k),
			"the snapshot still carries the stored row merged onto the default")
		assert.Equal(t, testValue{Host: "resolved.example", Port: 2525}, m.Effective(snap, k),
			"but the effective value is the rule's")
	})

	t.Run("a rule that declines leaves the stored-merged value alone", func(t *testing.T) {
		m, k, _ := newTestManagerWithStore(t)
		var calls int
		m.Configure(WithEffective(k.Name(), func(*Snapshot) (any, bool) {
			calls++
			return testValue{Host: "never.used"}, false
		}))
		require.NoError(t, m.Update(ctx, k, json.RawMessage(`{"host":"stored.example"}`)))

		snap := m.Snapshot(ctx)
		assert.Equal(t, snap.ValueOf(k), m.Effective(snap, k),
			"a rule that reports false must not override")
		assert.Positive(t, calls, "and the rule really ran")
	})

	t.Run("a key with no rule reports the snapshot value", func(t *testing.T) {
		m, k, _ := newTestManagerWithStore(t)
		require.NoError(t, m.Update(ctx, k, json.RawMessage(`{"host":"stored.example"}`)))
		snap := m.Snapshot(ctx)
		assert.Equal(t, snap.ValueOf(k), m.Effective(snap, k))
		assert.Equal(t, snap.ValueOf(KeySMTP), m.Effective(snap, KeySMTP),
			"a sibling key is unaffected by another key's rule")
	})

	t.Run("the rule reads the snapshot it is given", func(t *testing.T) {
		m, k, _ := newTestManagerWithStore(t)
		m.Configure(WithEffective(k.Name(), func(s *Snapshot) (any, bool) {
			// The dev-mode signup rule has this exact shape: it answers from
			// whether the operator stored a row.
			if s.Customized(k) {
				return nil, false
			}
			return testValue{Host: "default.example"}, true
		}))
		assert.Equal(t, testValue{Host: "default.example"}, m.Effective(m.Snapshot(ctx), k),
			"an untouched key takes the rule's value")

		require.NoError(t, m.Update(ctx, k, json.RawMessage(`{"host":"stored.example"}`)))
		snap := m.Snapshot(ctx)
		assert.Equal(t, testValue{Host: "stored.example", Port: 587}, m.Effective(snap, k),
			"an operator write wins over the rule")
	})
}

// TestAfterResetRunsTheKeysStep pins the post-reset step's home. The step is
// the KEY's, and the reset handler runs it without knowing which key needs
// one.
func TestAfterResetRunsTheKeysStep(t *testing.T) {
	ctx := context.Background()

	t.Run("the registered key's step runs; a sibling has none", func(t *testing.T) {
		m, k, _ := newTestManagerWithStore(t)
		var calls int
		m.Configure(WithAfterReset(k.Name(), func(context.Context) error { calls++; return nil }))

		require.NoError(t, m.AfterReset(ctx, k))
		assert.Equal(t, 1, calls)

		require.NoError(t, m.AfterReset(ctx, KeySMTP))
		assert.Equal(t, 1, calls, "a key with no step is not an error and runs nothing")
	})

	t.Run("a failing step identifies its key and keeps its cause", func(t *testing.T) {
		m, k, _ := newTestManagerWithStore(t)
		m.Configure(WithAfterReset(k.Name(), func(context.Context) error {
			return errTest("keystore unavailable")
		}))

		err := m.AfterReset(ctx, k)
		require.Error(t, err)
		assert.Contains(t, err.Error(), k.Name(), "the operator learns which key")
		assert.Contains(t, err.Error(), "the post-reset step failed")
		assert.Contains(t, err.Error(), "keystore unavailable", "and the step's own cause survives")
	})

	t.Run("ResetMany does not fire the step", func(t *testing.T) {
		m, k, _ := newTestManagerWithStore(t)
		var calls int
		m.Configure(WithAfterReset(k.Name(), func(context.Context) error { calls++; return nil }))
		require.NoError(t, m.Update(ctx, k, json.RawMessage(`{"host":"stored.example"}`)))

		require.NoError(t, m.Reset(ctx, k))
		assert.False(t, m.Snapshot(ctx).Customized(k), "the row is gone")
		assert.Zero(t, calls,
			"the step writes settings itself, so the caller runs it after the reset commits")
	})
}

// TestConfigureRegistersRulesAfterTheManagerLoads pins the ordering the hub
// needs. The queue-budget rule closes over pool capacities the startup
// snapshot sizes, and the ALTCHA step closes over a captcha manager built ON
// this manager, so neither can reach NewManager.
func TestConfigureRegistersRulesAfterTheManagerLoads(t *testing.T) {
	ctx := context.Background()
	m, k, _ := newTestManagerWithStore(t)
	snap := m.Snapshot(ctx)
	require.Equal(t, snap.ValueOf(k), m.Effective(snap, k), "no rule yet")

	m.Configure(WithEffective(k.Name(), func(*Snapshot) (any, bool) {
		return testValue{Host: "late.example"}, true
	}))
	assert.Equal(t, testValue{Host: "late.example"}, m.Effective(m.Snapshot(ctx), k),
		"a rule registered after Load applies to the snapshot already published")
}

// TestPerKeyRulePanicsOnAWiringMistake pins the registration guards. A rule
// for a key name that no descriptor carries would silently never fire, and a
// second rule for one key would silently replace the first -- both are
// wiring bugs a running hub can never report.
func TestPerKeyRulePanicsOnAWiringMistake(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(m *Manager, k *Key[testValue])
		want string
	}{
		{"read-time rule for an unregistered key", func(m *Manager, _ *Key[testValue]) {
			m.Configure(WithEffective("never.registered", func(*Snapshot) (any, bool) { return nil, false }))
		}, "unregistered key"},
		{"post-reset step for an unregistered key", func(m *Manager, _ *Key[testValue]) {
			m.Configure(WithAfterReset("never.registered", func(context.Context) error { return nil }))
		}, "unregistered key"},
		{"two read-time rules for one key", func(m *Manager, k *Key[testValue]) {
			rule := func(*Snapshot) (any, bool) { return nil, false }
			m.Configure(WithEffective(k.Name(), rule), WithEffective(k.Name(), rule))
		}, "duplicate read-time rule"},
		{"two post-reset steps for one key", func(m *Manager, k *Key[testValue]) {
			step := func(context.Context) error { return nil }
			m.Configure(WithAfterReset(k.Name(), step), WithAfterReset(k.Name(), step))
		}, "duplicate post-reset step"},
		{"a nil read-time rule", func(m *Manager, k *Key[testValue]) {
			m.Configure(WithEffective(k.Name(), nil))
		}, "nil read-time rule"},
		{"a nil post-reset step", func(m *Manager, k *Key[testValue]) {
			m.Configure(WithAfterReset(k.Name(), nil))
		}, "nil post-reset step"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, k, _ := newTestManagerWithStore(t)
			msg := recoveredPanic(t, func() { tc.call(m, k) })
			assert.Contains(t, msg, tc.want, "the panic says which wiring mistake it caught")
			assert.Contains(t, msg, "settings:", "and which package caught it")
		})
	}
}

// recoveredPanic runs fn, requires that it panics, and returns the panic
// value rendered as text.
func recoveredPanic(t *testing.T, fn func()) (msg string) {
	t.Helper()
	panicked := false
	func() {
		defer func() {
			if r := recover(); r != nil {
				panicked = true
				msg = fmt.Sprint(r)
			}
		}()
		fn()
	}()
	require.True(t, panicked, "the call must panic")
	return msg
}

// lockRecorder collects the settings locks and writes one write path took,
// shared by the outer store and the transaction-bound one it hands the
// caller.
type lockRecorder struct {
	mu    sync.Mutex
	order []string
}

func (r *lockRecorder) record(op string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.order = append(r.order, op)
}

func (r *lockRecorder) locked() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.order)
}

// reset drops what Load recorded, so a test reads only its own write.
func (r *lockRecorder) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.order = nil
}

// lockOrderStore records the order the settings write path takes its locks
// and writes its rows in.
type lockOrderStore struct {
	store.Store
	rec *lockRecorder
}

func (s *lockOrderStore) Settings() store.SettingsStore {
	return &lockOrderSettings{inner: s.Store.Settings(), rec: s.rec}
}

func (s *lockOrderStore) RunInTransaction(ctx context.Context, fn func(tx store.Store) error) error {
	return s.Store.RunInTransaction(ctx, func(tx store.Store) error {
		return fn(&lockOrderStore{Store: tx, rec: s.rec})
	})
}

// lockOrderSettings records EVERY settings-store call, not only the locked
// read. The assertion is over the complete sequence, so a second read
// slipped in front of the table lock -- the shape that formed the cycle --
// shows up as an extra entry rather than passing unseen.
//
// It holds an EXPLICIT inner store instead of embedding the interface, and
// that is what makes the claim above true. An embedded interface promotes
// every method the recorder does not override, so a re-added per-key
// locking read would reach the real store unrecorded and every sequence
// assertion below would still pass with its identical expected slice. With
// the explicit field, the compile-time assertion beside it breaks the build
// until a method added to store.SettingsStore is wrapped here too.
type lockOrderSettings struct {
	inner store.SettingsStore
	rec   *lockRecorder
}

var _ store.SettingsStore = (*lockOrderSettings)(nil)

func (s *lockOrderSettings) GetAllForUpdate(ctx context.Context) ([]store.SettingRow, error) {
	s.rec.record("lock-all")
	return s.inner.GetAllForUpdate(ctx)
}

func (s *lockOrderSettings) GetAll(ctx context.Context) ([]store.SettingRow, error) {
	s.rec.record("get-all")
	return s.inner.GetAll(ctx)
}

func (s *lockOrderSettings) Get(ctx context.Context, key string) (*store.SettingRow, error) {
	s.rec.record("get:" + key)
	return s.inner.Get(ctx, key)
}

func (s *lockOrderSettings) Upsert(ctx context.Context, p store.UpsertSettingParams) error {
	s.rec.record("upsert:" + p.Key)
	return s.inner.Upsert(ctx, p)
}

func (s *lockOrderSettings) InsertIfAbsent(ctx context.Context, p store.UpsertSettingParams) (bool, error) {
	s.rec.record("insert-if-absent:" + p.Key)
	return s.inner.InsertIfAbsent(ctx, p)
}

func (s *lockOrderSettings) Delete(ctx context.Context, key string) error {
	s.rec.record("delete:" + key)
	return s.inner.Delete(ctx, key)
}

// TestSnapshotDegradesToDefaultsWhenTheStoreFailsBeforeAnyLoad pins the
// cold-cache outage path. A store that fails BEFORE the first successful
// Load leaves no last-good snapshot, so the fallback has to build a
// defaults-only one -- and building a snapshot takes the manager lock for
// its warning tags, which the fallback used to hold across the whole
// build. Every settings read then wedged forever, on the exact path a
// startup outage takes.
func TestSnapshotDegradesToDefaultsWhenTheStoreFailsBeforeAnyLoad(t *testing.T) {
	real, err := sqlite.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = real.Close() })

	k := testKey()
	st := &flakySettingsStore{Store: real}
	st.failed.Store(true)
	m := NewManager(st, nil, []Descriptor{k})

	got := make(chan testValue, 1)
	go func() { got <- k.Of(m.Snapshot(context.Background())) }()
	select {
	case v := <-got:
		assert.Equal(t, 587, v.Port, "a cold cache over a broken store answers with defaults")
	case <-time.After(5 * time.Second):
		t.Fatal("Snapshot never returned: the fallback holds the manager lock while it builds")
	}
}

// TestPostWriteReloadFailureDoesNotWedgeTheManager pins the same rule on
// the post-commit reload, whose fallback has the identical shape.
func TestPostWriteReloadFailureDoesNotWedgeTheManager(t *testing.T) {
	real, err := sqlite.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = real.Close() })

	encKey, err := keystore.GenerateKey()
	require.NoError(t, err)
	ks, err := keystore.New(map[uint32][32]byte{1: encKey})
	require.NoError(t, err)

	k := testKey()
	st := &postWriteFailStore{Store: real}
	m := NewManager(st, ks, []Descriptor{k})

	done := make(chan error, 1)
	go func() { done <- m.Update(context.Background(), k, json.RawMessage(`{"host":"a.example"}`)) }()
	select {
	case err := <-done:
		assert.NoError(t, err, "the write committed; only the reload failed")
	case <-time.After(5 * time.Second):
		t.Fatal("Update never returned: the post-write reload fallback holds the manager lock")
	}
}

// postWriteFailStore lets the in-transaction reads through and fails only
// the unguarded reads, which is exactly the post-commit reload.
type postWriteFailStore struct {
	store.Store
	inTx bool
}

func (s *postWriteFailStore) Settings() store.SettingsStore {
	if s.inTx {
		return s.Store.Settings()
	}
	return failingSettings{SettingsStore: s.Store.Settings()}
}

func (s *postWriteFailStore) RunInTransaction(ctx context.Context, fn func(tx store.Store) error) error {
	return s.Store.RunInTransaction(ctx, func(tx store.Store) error {
		return fn(&postWriteFailStore{Store: tx, inTx: true})
	})
}

type failingSettings struct {
	store.SettingsStore
}

func (f failingSettings) GetAll(context.Context) ([]store.SettingRow, error) {
	return nil, errors.New("store down")
}

// deadlockDeadline caps how long a test waits for a write path that must
// not block. It exists to turn a deadlock into a reported failure, so it is
// far longer than any contended sqlite write needs and far shorter than the
// package-wide panic that a hung test binary produces instead.
const deadlockDeadline = 30 * time.Second

// gatedSettingsStore holds one whole-table read open AFTER it reads. A test
// can then place a second commit and a second reload beside a reload that
// already carries a pre-write view -- the interleaving reloadMu exists to
// order.
type gatedSettingsStore struct {
	store.Store
	// armed lets exactly one read block. It clears when that read starts
	// blocking (compare-and-swap), so every later read passes straight
	// through.
	armed   atomic.Bool
	reading chan struct{}
	release chan struct{}
}

func (s *gatedSettingsStore) Settings() store.SettingsStore {
	return &gatedSettings{SettingsStore: s.Store.Settings(), gate: s}
}

type gatedSettings struct {
	store.SettingsStore
	gate *gatedSettingsStore
}

func (g *gatedSettings) GetAll(ctx context.Context) ([]store.SettingRow, error) {
	rows, err := g.SettingsStore.GetAll(ctx)
	if g.gate.armed.CompareAndSwap(true, false) {
		close(g.gate.reading)
		<-g.gate.release
	}
	return rows, err
}

// TestPublishFromStoreHoldsTheReloadLockAcrossTheReadAndThePublish pins the
// rule the epoch counter alone cannot hold: the published order equals the
// commit order.
//
// Every write publish advances the epoch, so the epoch drops only a stale
// TTL refresh -- it cannot order two post-commit reloads against each other.
// Without reloadMu, a reload that read the rows of commit one can publish
// AFTER the reload of commit two, and the hub then serves the older state
// until the TTL expires.
//
// The gate holds the first reload inside its read window, which is where the
// inversion is possible. The lock check is what makes the failure
// deterministic: a scheduler that never runs the second reload during that
// window would leave an ordering-only assertion passing over broken code.
func TestPublishFromStoreHoldsTheReloadLockAcrossTheReadAndThePublish(t *testing.T) {
	real, err := sqlite.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = real.Close() })

	gate := &gatedSettingsStore{
		Store:   real,
		reading: make(chan struct{}),
		release: make(chan struct{}),
	}
	a, _ := lockOrderKeys()
	m := NewManager(gate, nil, []Descriptor{a})
	ctx := context.Background()
	require.NoError(t, m.Load(ctx))

	var mu sync.Mutex
	var published []any
	m.Subscribe(func(s *Snapshot) {
		mu.Lock()
		defer mu.Unlock()
		published = append(published, s.ValueOf(a))
	})

	// Commit one, and start its reload. The reload reads the row and then
	// stops inside the gate, still holding the lock it must not release.
	require.NoError(t, real.Settings().Upsert(ctx, upsertParams(a.Name(), `2`, nil)))
	gate.armed.Store(true)
	first := make(chan struct{})
	go func() {
		defer close(first)
		m.publishFromStore(ctx)
	}()
	<-gate.reading

	held := !m.reloadMu.TryLock()
	if !held {
		m.reloadMu.Unlock()
	}
	assert.True(t, held,
		"the reload must hold reloadMu across its read AND its publish, or a later reload publishes first")

	// Commit two, and start its reload while the first one still sits in its
	// read. The lock is what makes it wait.
	require.NoError(t, real.Settings().Upsert(ctx, upsertParams(a.Name(), `3`, nil)))
	second := make(chan struct{})
	go func() {
		defer close(second)
		m.publishFromStore(ctx)
	}()

	close(gate.release)
	select {
	case <-first:
	case <-time.After(deadlockDeadline):
		t.Fatal("the first reload never returned")
	}
	select {
	case <-second:
	case <-time.After(deadlockDeadline):
		t.Fatal("the second reload never returned: it waits on a lock the first one never released")
	}

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []any{int64(2), int64(3)}, published,
		"the subscribers see the two commits in commit order")
	assert.Equal(t, int64(3), m.Snapshot(ctx).ValueOf(a),
		"and the served snapshot is the LAST commit, not the reload that started first")
}

// TestARefusedWriteNeitherPublishesNorFiresSubscribers pins the one
// observable a refused write leaves: none.
//
// writeInTransaction publishes AFTER it checks the transaction's error, and
// moving the publish above that check breaks no value assertion anywhere:
// the store is unchanged, so the reloaded snapshot matches the previous one
// and no subscriber fires. The epoch is what does move -- every write
// publish advances it -- so it is the observable that reports the refused
// write's extra reload.
func TestARefusedWriteNeitherPublishesNorFiresSubscribers(t *testing.T) {
	m, k := newTestManager(t)
	ctx := context.Background()

	var fires int
	m.Subscribe(func(*Snapshot) { fires++ })
	before := m.currentEpoch()

	// The refusal classes, in the order the write path meets them: an
	// argument check before the transaction, the key's own validator inside
	// it, and a cross-key rule over the whole candidate.
	require.Error(t, m.UpdateMany(ctx, nil), "no writes given")
	require.Error(t, m.Update(ctx, k, json.RawMessage(`{"port":99999}`)), "the key's validator refuses the value")
	require.Error(t, m.ResetMany(ctx, []Descriptor{NewKey[bool]("never.registered")}))

	assert.Equal(t, before, m.currentEpoch(),
		"a refused write must not reload and publish; it stored nothing")
	assert.Zero(t, fires, "and no subscriber hears about it")

	// The positive control: an accepted write does both.
	require.NoError(t, m.Update(ctx, k, json.RawMessage(`{"host":"a.example"}`)))
	assert.Greater(t, m.currentEpoch(), before, "an accepted write advances the epoch")
	assert.Equal(t, 1, fires)
}

// cancelAfterCommitStore cancels the caller's context the moment the write
// transaction commits. That is what an abandoned request context does to the
// post-commit reload: the RPC answers, the client goes away, and the reload
// runs on a context nobody waits for.
type cancelAfterCommitStore struct {
	store.Store
	cancel context.CancelFunc
}

func (s *cancelAfterCommitStore) RunInTransaction(ctx context.Context, fn func(tx store.Store) error) error {
	err := s.Store.RunInTransaction(ctx, fn)
	s.cancel()
	return err
}

// TestPostCommitReloadSurvivesACancelledContext pins the reason
// buildSnapshotFromStore reads under context.WithoutCancel.
//
// The write already committed. A reload that fails takes the fallback, which
// returns the PREVIOUS snapshot pointer -- publishWrite then finds prev and
// s equal, fires no subscriber, and the committed write reaches no pushed
// consumer until the TTL expires. So a cancelled caller context must not
// stop the reload.
func TestPostCommitReloadSurvivesACancelledContext(t *testing.T) {
	real, err := sqlite.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = real.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st := &cancelAfterCommitStore{Store: real, cancel: cancel}

	a, _ := lockOrderKeys()
	m := NewManager(st, nil, []Descriptor{a})
	require.NoError(t, m.Load(context.Background()))

	var published []any
	m.Subscribe(func(s *Snapshot) { published = append(published, s.ValueOf(a)) })

	require.NoError(t, m.Update(ctx, a, json.RawMessage(`2`)))
	require.Error(t, ctx.Err(), "the fixture really cancelled the caller's context")

	assert.Equal(t, []any{int64(2)}, published,
		"the committed write must reach the subscribers although the caller's context is cancelled")
	assert.Equal(t, int64(2), m.Snapshot(context.Background()).ValueOf(a),
		"and the served snapshot must carry it, not the pre-write state")
}

// TestResetManyArgumentChecks pins the batch reset's argument refusals. They
// are the reset half of UpdateMany's, and the admin RPC surface classifies
// each by TYPE.
func TestResetManyArgumentChecks(t *testing.T) {
	m, k := newTestManager(t)
	ctx := context.Background()

	for _, tc := range []struct {
		name  string
		descs []Descriptor
		want  string
	}{
		{"an empty list", nil, "no settings resets given"},
		{"an empty slice", []Descriptor{}, "no settings resets given"},
		{"an unregistered key", []Descriptor{NewKey[testValue]("never.registered")}, "is not registered"},
		{"a duplicate key", []Descriptor{k, k}, "appears twice in one reset"},
		{"a duplicate beside a registered sibling", []Descriptor{KeySMTP, k, KeySMTP}, "appears twice in one reset"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := m.ResetMany(ctx, tc.descs)
			require.ErrorContains(t, err, tc.want)
			var invalid *InvalidError
			assert.ErrorAs(t, err, &invalid,
				"a caller-supplied shape is InvalidArgument, not a store fault")
		})
	}

	// The positive control: the same keys clear when the list is well
	// formed, and an untouched sibling keeps its row.
	require.NoError(t, m.Update(ctx, k, json.RawMessage(`{"host":"a.example"}`)))
	require.NoError(t, m.Update(ctx, KeySMTP, json.RawMessage(`{"host":"b.example"}`)))
	require.NoError(t, m.ResetMany(ctx, []Descriptor{k}))
	snap := m.Snapshot(ctx)
	assert.False(t, snap.Customized(k), "the listed key is back at its default")
	assert.True(t, snap.Customized(KeySMTP), "and a key the list omitted keeps its row")
}

// TestResetManyOfAKeyWithNoRowIsASilentNoOp pins what a reset of an
// untouched key does TODAY: it succeeds, deletes nothing, and still runs the
// post-commit reload.
//
// The reload is not free -- the epoch advances, which is one whole-table read
// per pointless reset -- but no subscriber fires, because the rows did not
// change and the snapshot's canonical hash is what fireSubs compares. An
// operator who resets an already-default key gets the answer the admin
// surface promises, not an error about a row that never existed.
func TestResetManyOfAKeyWithNoRowIsASilentNoOp(t *testing.T) {
	m, k := newTestManager(t)
	ctx := context.Background()

	var fires int
	m.Subscribe(func(*Snapshot) { fires++ })
	require.False(t, m.Snapshot(ctx).Customized(k), "the fixture starts with no row")
	before := m.currentEpoch()

	require.NoError(t, m.ResetMany(ctx, []Descriptor{k}),
		"deleting a row that does not exist is not an error")
	assert.False(t, m.Snapshot(ctx).Customized(k))
	assert.Equal(t, 587, k.Of(m.Snapshot(ctx)).Port, "the key stays at its default")
	assert.Zero(t, fires, "nothing changed, so no subscriber hears about it")
	assert.Greater(t, m.currentEpoch(), before,
		"but the transaction committed, so the post-commit reload still ran")
}

// TestEveryWriteVerbRefusesAnUnregisteredKey pins the rule that makes the
// table and the snapshot agree: a key this manager never registered reaches
// no row, through ANY verb.
//
// SetValue and SetIfAbsent used to run no such check while the
// partial-document verbs did, and the asymmetry was not survivable. The row
// landed in hub_settings and every snapshot then dropped it, because
// buildSnapshotWith walks the REGISTERED names -- so the value was invisible
// to every reader AND to every cross-key rule, it survived a restart, it
// warned once per process that it belonged to an unknown key, and removing
// it again needed direct SQL because the reset verbs refuse the same name.
//
// The refusal is the caller's to fix (InvalidError), and it lands BEFORE the
// transaction opens: the epoch does not move, because every committed write
// publishes and advances it.
func TestEveryWriteVerbRefusesAnUnregisteredKey(t *testing.T) {
	st, err := sqlite.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()

	stranger := NewKey[int64]("never.registered").WithDefault(int64(1))
	a, b := lockOrderKeys()

	// The cross-key rule counts its runs: the refusal must happen before
	// prepareRows builds a candidate, so no rule sees the stranger at all.
	var crossRuns int
	m := NewManager(st, nil, []Descriptor{a, b}, WithCrossValidation(func(*Snapshot) error {
		crossRuns++
		return nil
	}))
	require.NoError(t, m.Load(ctx))
	before := m.currentEpoch()

	for name, write := range map[string]func() error{
		"Update":      func() error { return m.Update(ctx, stranger, json.RawMessage(`2`)) },
		"UpdateMany":  func() error { return m.UpdateMany(ctx, []KeyWrite{{Desc: stranger, Public: json.RawMessage(`2`)}}) },
		"Reset":       func() error { return m.Reset(ctx, stranger) },
		"ResetMany":   func() error { return m.ResetMany(ctx, []Descriptor{stranger}) },
		"SetValue":    func() error { return m.SetValue(ctx, stranger, int64(2)) },
		"SetIfAbsent": func() error { return m.SetIfAbsent(ctx, stranger, int64(2)) },
	} {
		t.Run(name, func(t *testing.T) {
			err := write()
			require.ErrorContains(t, err, `settings key "never.registered" is not registered`)
			var invalid *InvalidError
			assert.ErrorAs(t, err, &invalid, "a key the manager does not know is the caller's mistake")
		})
	}

	// Nothing reached the store, and nothing reached the publish path.
	_, getErr := st.Settings().Get(ctx, stranger.Name())
	assert.ErrorIs(t, getErr, store.ErrNotFound, "no refused verb leaves an orphan row behind")
	assert.Equal(t, before, m.currentEpoch(),
		"the refusal precedes the transaction, so no write ever committed")
	assert.Zero(t, crossRuns, "the cross-key rules never see a key that is not registered")

	// The typed wrappers reach the same refusal: a caller holding a Key
	// handle cannot route around the check by using Key.Set.
	require.ErrorContains(t, stranger.Set(ctx, m, 2), "is not registered")
	require.ErrorContains(t, stranger.SetIfAbsent(ctx, m, 2), "is not registered")

	// A REGISTERED key still writes through both complete-value verbs, so
	// the check refuses the stranger rather than the verb.
	require.NoError(t, m.SetValue(ctx, a, int64(7)))
	assert.Equal(t, int64(7), a.Of(m.Snapshot(ctx)))
	require.NoError(t, m.SetIfAbsent(ctx, b, int64(9)))
	assert.Equal(t, int64(9), b.Of(m.Snapshot(ctx)))
}

// TestSetIfAbsentSharesPrepareRowsWithOtherWrites pins that SetIfAbsent
// goes through the same prepareRows path as Update (validation, registry
// check). Email verification no longer has a cross-key rule — it follows SMTP.
func TestSetIfAbsentSharesPrepareRowsWithOtherWrites(t *testing.T) {
	m, k := newTestManager(t)
	ctx := context.Background()
	require.NoError(t, k.SetIfAbsent(ctx, m, testValue{Port: 2525}))
	assert.Equal(t, 2525, k.Of(m.Snapshot(ctx)).Port)
}

// TestUpdateSecretRefusalOrder pins the ORDER the secret verb's three
// refusals answer in, which its own doc comment calls part of the contract.
//
// The key's shape comes first: a key with no encrypted half cannot take a
// secret document at all, whatever the document says. The empty-document
// check comes second, because the secret-field check below it finds no
// secret field in an absent document and would otherwise answer an omitted
// document with a message listing fields the caller never had to supply.
func TestUpdateSecretRefusalOrder(t *testing.T) {
	m, k := newTestManager(t)
	ctx := context.Background()

	t.Run("a key with no secret half, and no document", func(t *testing.T) {
		err := m.UpdateSecret(ctx, KeySignupEnabled, nil)
		require.ErrorContains(t, err, "has no secret fields")
		assert.NotContains(t, err.Error(), "the partial document is required",
			"the key's shape is answered before the document is examined")
	})

	t.Run("a secret-bearing key with no document", func(t *testing.T) {
		err := m.UpdateSecret(ctx, k, nil)
		require.ErrorContains(t, err, "the partial document is required")
		assert.NotContains(t, err.Error(), "must specify at least one of",
			"an omitted document is not a document that misses the secret fields")
	})

	t.Run("a secret-bearing key with a public-only document", func(t *testing.T) {
		err := m.UpdateSecret(ctx, k, json.RawMessage(`{"host":"evil.example"}`))
		require.ErrorContains(t, err, "must specify at least one of")
		assert.Contains(t, err.Error(), "pass", "the message lists the key's secret fields")
	})
}

// TestResetKeepsTheErrorClassThroughItsWrap pins that the single-key reset's
// context wrap does not hide the class the admin RPC surface classifies on.
// Reset wraps ResetMany's error to say which key it was resetting, and a
// wrap that dropped the class would downgrade an actionable InvalidArgument
// to a 500.
func TestResetKeepsTheErrorClassThroughItsWrap(t *testing.T) {
	m, _ := newTestManager(t)
	ctx := context.Background()

	t.Run("an unregistered key", func(t *testing.T) {
		err := m.Reset(ctx, NewKey[bool]("never.registered"))
		var invalid *InvalidError
		require.ErrorAs(t, err, &invalid)
		assert.Contains(t, err.Error(), "is not registered")
	})
}

// TestConfigureDoesNotRaceALiveReader pins why the two per-key rule tables
// are guarded. The hub registers the queue-budget rule and the ALTCHA step
// through Configure at its wiring site, and a reader that reaches Effective
// or Snapshot at that moment must not read a map another goroutine writes.
//
// Run under -race, this fails on an unguarded table.
func TestConfigureDoesNotRaceALiveReader(t *testing.T) {
	m, k, _ := newTestManagerWithStore(t)
	ctx := context.Background()

	const readers = 4
	started := make(chan struct{}, readers)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for range readers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			started <- struct{}{}
			for {
				select {
				case <-stop:
					return
				default:
				}
				snap := m.Snapshot(ctx)
				_ = m.Effective(snap, k)
				_ = m.Effective(snap, KeySMTP)
				_ = m.AfterReset(ctx, KeySMTP)
			}
		}()
	}
	for range readers {
		<-started
	}

	m.Configure(WithEffective(k.Name(), func(*Snapshot) (any, bool) {
		return testValue{Host: "late.example"}, true
	}))
	m.Configure(WithAfterReset(KeySMTP.Name(), func(context.Context) error { return nil }))

	close(stop)
	wg.Wait()

	assert.Equal(t, testValue{Host: "late.example"}, m.Effective(m.Snapshot(ctx), k),
		"the late registration applies once the readers stop")
}
