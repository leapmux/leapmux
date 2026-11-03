package solo

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/hub"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/config"
	"github.com/leapmux/leapmux/internal/workerkeys"
	"github.com/leapmux/leapmux/internal/workerkeys/workerkeystest"
)

// fakeRegistrar stands in for *hub.Server, recording what the loader asked of it.
type fakeRegistrar struct {
	registration    hub.WorkerRegistration
	registrationErr error

	adminID  string
	adminErr error

	registerErr    error
	registered     int
	registeredKeys []*leapmuxv1.WorkerPublicKeys
	newWorker      hub.WorkerCredentials
	// onRegister runs inside RegisterWorker, so a test can change the filesystem
	// under the persistState call that follows.
	onRegister func()

	retireErr  error
	retiredIDs []string
}

func (f *fakeRegistrar) GetWorkerRegistration(context.Context, string) (hub.WorkerRegistration, error) {
	return f.registration, f.registrationErr
}

func (f *fakeRegistrar) GetAdminUser(context.Context) (string, error) {
	return f.adminID, f.adminErr
}

func (f *fakeRegistrar) RegisterWorker(_ context.Context, _ string, keys *leapmuxv1.WorkerPublicKeys) (*hub.WorkerCredentials, error) {
	f.registered++
	f.registeredKeys = append(f.registeredKeys, keys)
	if f.onRegister != nil {
		f.onRegister()
	}
	if f.registerErr != nil {
		return nil, f.registerErr
	}
	c := f.newWorker
	return &c, nil
}

func (f *fakeRegistrar) RetireLocalWorker(_ context.Context, workerID string) error {
	f.retiredIDs = append(f.retiredIDs, workerID)
	return f.retireErr
}

// workerKeyMaterial builds key material from three base64 fields, for the cases
// that need a broken or arbitrary one.
func workerKeyMaterial(x25519, mlkem, slhdsa string) config.KeyMaterial {
	return config.KeyMaterial{
		X25519PrivateKey: x25519,
		MlkemPrivateKey:  mlkem,
		SlhdsaPrivateKey: slhdsa,
	}
}

// writeState seeds a solo state file and returns its path.
func writeState(t *testing.T, dir string, s soloState) string {
	t.Helper()
	path := filepath.Join(dir, "worker-state.json")
	data, err := json.MarshalIndent(s, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0o600))
	return path
}

// seedUsableState writes a state file whose keypair matches the keys it returns,
// so no test can pair a state with keys it did not seed.
func seedUsableState(t *testing.T, dir string, s soloState) (string, *leapmuxv1.WorkerPublicKeys) {
	t.Helper()
	ck, err := s.GenerateKeypair()
	require.NoError(t, err)
	keys, err := workerkeys.Of(ck)
	require.NoError(t, err)
	return writeState(t, dir, s), keys
}

func readState(t *testing.T, path string) soloState {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var s soloState
	require.NoError(t, json.Unmarshal(data, &s))
	return s
}

// activeRegistration is the registration of a row a launcher can use.
func activeRegistration(owner string, keys *leapmuxv1.WorkerPublicKeys) hub.WorkerRegistration {
	return hub.WorkerRegistration{
		Owner:      owner,
		Status:     leapmuxv1.WorkerStatus_WORKER_STATUS_ACTIVE,
		PublicKeys: keys,
	}
}

// A first launch generates the keypair, registers the Worker with its public
// keys, and persists the private halves -- so a later launch restores exactly the
// keys the Hub pinned.
func TestLoadOrCreateWorkerState_RegistersWithTheKeysItPersists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	reg := &fakeRegistrar{
		adminID:   "admin-1",
		newWorker: hub.WorkerCredentials{WorkerID: "w-new", AuthToken: "tok-new"},
	}

	got, ck, err := loadOrCreateWorkerState(context.Background(), reg, path, filepath.Join(dir, "data"))
	require.NoError(t, err)
	require.Equal(t, 1, reg.registered)
	require.Len(t, reg.registeredKeys, 1)
	assert.Equal(t, "w-new", got.WorkerID)

	// The keys the Hub was told about are the ones the saved state reproduces.
	restored, err := readState(t, path).CompositeKeypair()
	require.NoError(t, err)
	restoredKeys, err := workerkeys.Of(restored)
	require.NoError(t, err)
	assert.True(t, workerkeys.Equal(restoredKeys, reg.registeredKeys[0]),
		"the registered keys must be the ones the state file can reproduce")
	assert.Equal(t, restored.Fingerprint(), ck.Fingerprint())

	// A second launch with the pinned keys registers nothing and retires nothing.
	reg.registration = activeRegistration("admin-1", reg.registeredKeys[0])
	second, secondKey, err := loadOrCreateWorkerState(context.Background(), reg, path, filepath.Join(dir, "data"))
	require.NoError(t, err)
	assert.Equal(t, "w-new", second.WorkerID)
	assert.Equal(t, 1, reg.registered, "a usable state must not register again")
	assert.Empty(t, reg.retiredIDs, "a usable state must not be retired")
	secondKeys, err := workerkeys.Of(secondKey)
	require.NoError(t, err)
	assert.True(t, workerkeys.Equal(secondKeys, reg.registeredKeys[0]))
}

// A saved state the launcher cannot use is retired at once and replaced. Retiring
// first matters: a store error must fail the launch and keep the file, so the next
// launch retries, and the grace period must not keep a token valid for private
// keys that sit in a preserved .corrupt-* file.
func TestLoadOrCreateWorkerState_AnUnusableStateReRegisters(t *testing.T) {
	t.Parallel()

	for name, prepare := range map[string]func(t *testing.T, dir string) (*fakeRegistrar, string){
		"a missing keypair": func(t *testing.T, dir string) (*fakeRegistrar, string) {
			t.Helper()
			path := writeState(t, dir, soloState{WorkerID: "w-old", AuthToken: "tok", RegisteredBy: "admin-1"})
			return &fakeRegistrar{registration: activeRegistration("admin-1", workerkeystest.Keys(0x77))}, path
		},
		"an undecodable keypair": func(t *testing.T, dir string) (*fakeRegistrar, string) {
			t.Helper()
			path := writeState(t, dir, soloState{
				WorkerID: "w-old", AuthToken: "tok", RegisteredBy: "admin-1",
				KeyMaterial: workerKeyMaterial("not!valid!base64", "not!valid!base64", "not!valid!base64"),
			})
			return &fakeRegistrar{registration: activeRegistration("admin-1", workerkeystest.Keys(0x77))}, path
		},
		"keys that differ from the pin": func(t *testing.T, dir string) (*fakeRegistrar, string) {
			t.Helper()
			path, _ := seedUsableState(t, dir, soloState{WorkerID: "w-old", AuthToken: "tok", RegisteredBy: "admin-1"})
			return &fakeRegistrar{registration: activeRegistration("admin-1", workerkeystest.Keys(0x77))}, path
		},
		"a deregistering row": func(t *testing.T, dir string) (*fakeRegistrar, string) {
			t.Helper()
			path, keys := seedUsableState(t, dir, soloState{WorkerID: "w-old", AuthToken: "tok", RegisteredBy: "admin-1"})
			return &fakeRegistrar{registration: hub.WorkerRegistration{
				Owner:      "admin-1",
				Status:     leapmuxv1.WorkerStatus_WORKER_STATUS_DEREGISTERING,
				PublicKeys: keys,
			}}, path
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			reg, path := prepare(t, dir)
			reg.adminID = "admin-1"
			reg.newWorker = hub.WorkerCredentials{WorkerID: "w-new", AuthToken: "tok-new"}

			got, _, err := loadOrCreateWorkerState(context.Background(), reg, path, filepath.Join(dir, "data"))
			require.NoError(t, err)

			assert.Equal(t, []string{"w-old"}, reg.retiredIDs, "the unusable row must be retired")
			assert.Equal(t, 1, reg.registered, "a fresh worker must be registered")
			assert.Equal(t, "w-new", got.WorkerID)
			assert.Equal(t, "w-new", readState(t, path).WorkerID)
			matches, err := filepath.Glob(path + ".corrupt-*")
			require.NoError(t, err)
			assert.Len(t, matches, 1, "the unusable state must be preserved")
		})
	}
}

// A retire that fails is a store failure, not a verdict on the state: the launch
// fails and leaves state.json in place so the next launch retries. A refusal to
// retire a non-bundled row only logs, because there is nothing the launcher could
// do about it.
func TestLoadOrCreateWorkerState_ARetireFailureFailsTheLaunch(t *testing.T) {
	t.Parallel()

	t.Run("a store error fails the launch and keeps the file", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path, _ := seedUsableState(t, dir, soloState{WorkerID: "w-old", AuthToken: "tok", RegisteredBy: "admin-1"})
		boom := errors.New("database is locked")
		reg := &fakeRegistrar{
			registration: activeRegistration("admin-1", workerkeystest.Keys(0x77)),
			retireErr:    boom,
			adminID:      "admin-1",
			newWorker:    hub.WorkerCredentials{WorkerID: "w-new", AuthToken: "tok-new"},
		}

		_, _, err := loadOrCreateWorkerState(context.Background(), reg, path, filepath.Join(dir, "data"))
		require.ErrorIs(t, err, boom)
		assert.Zero(t, reg.registered, "a failed retire must not proceed to register")
		assert.Equal(t, "w-old", readState(t, path).WorkerID, "state.json must stay in place for a retry")
		matches, err := filepath.Glob(path + ".corrupt-*")
		require.NoError(t, err)
		assert.Empty(t, matches, "a failed retire must not preserve (and thus hide) the file")
	})

	t.Run("a non-bundled row is only logged", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path, _ := seedUsableState(t, dir, soloState{WorkerID: "w-old", AuthToken: "tok", RegisteredBy: "admin-1"})
		reg := &fakeRegistrar{
			registration: activeRegistration("admin-1", workerkeystest.Keys(0x77)),
			retireErr:    hub.ErrNotAutoRegistered,
			adminID:      "admin-1",
			newWorker:    hub.WorkerCredentials{WorkerID: "w-new", AuthToken: "tok-new"},
		}

		_, _, err := loadOrCreateWorkerState(context.Background(), reg, path, filepath.Join(dir, "data"))
		require.NoError(t, err)
		assert.Equal(t, []string{"w-old"}, reg.retiredIDs)
		assert.Equal(t, 1, reg.registered, "the refusal only logs, so the launch registers a fresh worker")
	})
}

// A state file that cannot be written leaves a row pinned to private keys that
// were lost with the write, so the fresh row is retired. When that fails too, both
// errors are reported.
func TestLoadOrCreateWorkerState_AFailedWriteRetiresTheNewWorker(t *testing.T) {
	t.Parallel()

	for name, retireErr := range map[string]error{"a successful retire": nil, "a failed retire": errors.New("database is locked")} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			// Its parent does not exist yet, so the read is os.ErrNotExist and the
			// launch proceeds; RegisterWorker then creates a FILE at that path, so
			// atomicfile.WriteFile cannot create its temp file in it.
			blocked := filepath.Join(dir, "blocked")
			path := filepath.Join(blocked, "state.json")
			reg := &fakeRegistrar{
				adminID:    "admin-1",
				newWorker:  hub.WorkerCredentials{WorkerID: "w-new", AuthToken: "tok"},
				retireErr:  retireErr,
				onRegister: func() { require.NoError(t, os.WriteFile(blocked, []byte("not a directory"), 0o600)) },
			}

			_, _, err := loadOrCreateWorkerState(context.Background(), reg, path, filepath.Join(dir, "data"))
			require.Error(t, err, "a failed write must fail the launch")
			assert.Equal(t, []string{"w-new"}, reg.retiredIDs, "the row whose keys were lost must be retired")
			if retireErr != nil {
				assert.ErrorIs(t, err, retireErr, "the retire failure must be reported too")
			}
		})
	}
}

// The DB is the authority on worker ownership: workers.registered_by is NOT NULL and
// is the fact every machine-scoped gate keys on, so a state file that disagrees with
// it loses -- and gets refreshed rather than silently re-read next launch.
func TestLoadOrCreateWorkerState_DBOwnerWinsOverStateFile(t *testing.T) {
	dir := t.TempDir()
	path, keys := seedUsableState(t, dir, soloState{WorkerID: "w1", AuthToken: "tok", RegisteredBy: "stale-user"})
	reg := &fakeRegistrar{registration: activeRegistration("real-owner", keys)}

	got, _, err := loadOrCreateWorkerState(context.Background(), reg, path, filepath.Join(dir, "data"))
	require.NoError(t, err)

	assert.Equal(t, "real-owner", got.RegisteredBy, "the DB's owner must win over the state file's")
	assert.Equal(t, "w1", got.WorkerID, "a worker with a known owner must not be re-registered")
	assert.Zero(t, reg.registered)
	assert.Empty(t, reg.retiredIDs)
	assert.Equal(t, "real-owner", readState(t, path).RegisteredBy,
		"the corrected owner must be written back so the next launch matches")
}

// A state file predating the field (no RegisteredBy) must take the DB's owner --
// NOT a backfill from whoever the admin happens to be now, which answers a different
// question and would silently reassign the machine on any install where the admin
// changed.
func TestLoadOrCreateWorkerState_MissingOwnerTakenFromDBNotAdmin(t *testing.T) {
	dir := t.TempDir()
	path, keys := seedUsableState(t, dir, soloState{WorkerID: "w1", AuthToken: "tok"})
	reg := &fakeRegistrar{registration: activeRegistration("real-owner", keys), adminID: "current-admin"}

	got, _, err := loadOrCreateWorkerState(context.Background(), reg, path, filepath.Join(dir, "data"))
	require.NoError(t, err)

	assert.Equal(t, "real-owner", got.RegisteredBy,
		"an owner-less state file must be repaired from the DB, not from the current admin")
	assert.NotEqual(t, "current-admin", got.RegisteredBy)
	assert.Zero(t, reg.registered)
	assert.Empty(t, reg.retiredIDs)
}

// A worker the DB does not know (deleted, or a stale file from another install) is
// re-registered rather than launched against a missing row.
func TestLoadOrCreateWorkerState_UnknownWorkerReRegisters(t *testing.T) {
	dir := t.TempDir()
	path := writeState(t, dir, soloState{WorkerID: "gone", AuthToken: "tok", RegisteredBy: "someone"})
	reg := &fakeRegistrar{
		registrationErr: store.ErrNotFound,
		adminID:         "admin-1",
		newWorker:       hub.WorkerCredentials{WorkerID: "w-new", AuthToken: "tok-new"},
	}

	got, _, err := loadOrCreateWorkerState(context.Background(), reg, path, filepath.Join(dir, "data"))
	require.NoError(t, err)

	assert.Equal(t, 1, reg.registered, "an unknown worker must be re-registered")
	assert.Empty(t, reg.retiredIDs, "there is no row to retire")
	assert.Equal(t, "w-new", got.WorkerID)
	assert.Equal(t, "admin-1", got.RegisteredBy, "a NEW worker is attributed to the admin user")
	assert.Equal(t, "w-new", readState(t, path).WorkerID, "the new state must be persisted")
}

// A transient store failure (for example a disk I/O error) is NOT a missing
// worker. Treating it as one would discard the saved WorkerID and re-register a
// brand-new identity, orphaning every workspace and tab still pointed at the old
// worker. The launch must fail so a retry finds the row intact -- only
// store.ErrNotFound re-registers.
func TestLoadOrCreateWorkerState_TransientLookupErrorDoesNotReRegister(t *testing.T) {
	dir := t.TempDir()
	path, _ := seedUsableState(t, dir, soloState{WorkerID: "w1", AuthToken: "tok", RegisteredBy: "real-owner"})
	transient := errors.New("disk I/O error")
	reg := &fakeRegistrar{
		registrationErr: transient,
		adminID:         "admin-1",
		newWorker:       hub.WorkerCredentials{WorkerID: "w-new", AuthToken: "tok-new"},
	}

	_, _, err := loadOrCreateWorkerState(context.Background(), reg, path, filepath.Join(dir, "data"))
	require.Error(t, err, "a transient owner-lookup failure must fail the launch, not re-register")
	assert.ErrorIs(t, err, transient, "the underlying store error must be surfaced")
	assert.Zero(t, reg.registered, "a transient error must never trigger re-registration")
	assert.Empty(t, reg.retiredIDs)
	assert.Equal(t, "w1", readState(t, path).WorkerID,
		"the saved worker identity must be left intact for a retry")
}

// An ownerless row is unreachable through the schema (registered_by is NOT NULL), so
// it means a hand-edited row or a mint path that dropped it. Re-register rather than
// launch a worker whose every machine-scoped family is permanently dead for its own
// legitimate user -- a fail-closed outage indistinguishable from a real cross-tenant
// refusal.
func TestLoadOrCreateWorkerState_OwnerlessRowReRegisters(t *testing.T) {
	dir := t.TempDir()
	// ACTIVE with the seeded keys, so the row fails for the owner alone.
	path, keys := seedUsableState(t, dir, soloState{WorkerID: "w1", AuthToken: "tok", RegisteredBy: "someone"})
	reg := &fakeRegistrar{
		registration: activeRegistration("", keys),
		adminID:      "admin-1",
		newWorker:    hub.WorkerCredentials{WorkerID: "w-new", AuthToken: "tok-new"},
	}

	got, _, err := loadOrCreateWorkerState(context.Background(), reg, path, filepath.Join(dir, "data"))
	require.NoError(t, err)

	assert.Equal(t, 1, reg.registered, "an ownerless worker row must not be launched as-is")
	assert.Empty(t, reg.retiredIDs, "an ownerless row takes the re-register branch, not the retire one")
	assert.Equal(t, "admin-1", got.RegisteredBy)
	assert.NotEmpty(t, got.RegisteredBy, "a solo worker must never launch without an owner")
}

// A corrupt worker-state file (a partial write from a crash or power loss before
// writes became atomic, a hand-edit, or a truncated tail) has already lost the
// saved identity -- the bytes are unparseable -- so re-registering a fresh worker
// is the only forward path. But that re-registration ORPHANS every workspace and
// tab still pointed at the old worker_id, exactly the loss the transient-DB-error
// guard exists to prevent for the store analogue. The launcher must therefore
// re-register LOUDLY (the operator has to know to re-link the orphaned
// workspaces) and preserve the corrupt bytes for forensics rather than silently
// overwriting them.
func TestLoadOrCreateWorkerState_CorruptStateReRegistersLoudlyAndPreserves(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "worker-state.json")
	require.NoError(t, os.WriteFile(path, []byte("{not valid json"), 0o600))
	reg := &fakeRegistrar{
		adminID:   "admin-1",
		newWorker: hub.WorkerCredentials{WorkerID: "w-new", AuthToken: "tok-new"},
	}

	got, _, err := loadOrCreateWorkerState(context.Background(), reg, path, filepath.Join(dir, "data"))
	require.NoError(t, err, "a corrupt file has no recoverable identity, so re-register is the only path")

	assert.Equal(t, 1, reg.registered, "the unreadable identity must be replaced with a fresh worker")
	assert.Empty(t, reg.retiredIDs, "an unparseable file names no row to retire")
	assert.Equal(t, "w-new", got.WorkerID)
	assert.Equal(t, "w-new", readState(t, path).WorkerID, "the fresh identity must be persisted")

	matches, err := filepath.Glob(path + ".corrupt-*")
	require.NoError(t, err)
	require.Len(t, matches, 1, "the corrupt state must be set aside under a .corrupt-<ts> name")
	preserved, err := os.ReadFile(matches[0])
	require.NoError(t, err)
	assert.Equal(t, "{not valid json", string(preserved), "the corrupt bytes must be preserved verbatim")
}

// persistState writes atomically (temp file in the same dir, then rename), so a
// reader never observes a half-written file. The observable contract from outside
// is: the written file round-trips through the parser, and no temp remnants are
// left behind on success. The temp is atomicfile's `<name>.tmp<random>`.
func TestPersistState_RoundTripsAndLeavesNoTempRemnants(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "worker-state.json")
	in := soloState{WorkerID: "w1", AuthToken: "tok", RegisteredBy: "owner",
		KeyMaterial: workerKeyMaterial("sk", "msk", "ssk")}

	require.NoError(t, persistState(path, &in))

	assert.Equal(t, in, readState(t, path), "the written state must round-trip exactly")
	tmpRemnants, err := filepath.Glob(path + ".tmp*")
	require.NoError(t, err)
	assert.Empty(t, tmpRemnants, "a successful atomic write must not leave temp files behind")
}

// persistState stamps the pid as the FIRST field and keeps the file at 0600:
// the pid is how a reader tells which process wrote the file, and the mode is
// what keeps the credentials in it to their owner.
func TestPersistState_PIDIsFirstAndModeIs0600(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	in := soloState{WorkerID: "w1", AuthToken: "tok"}

	require.NoError(t, persistState(path, &in))

	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Regexp(t, `^\{\s*"pid"`, string(raw), "pid must be the first JSON key")
	assert.Equal(t, os.Getpid(), readState(t, path).PID, "the writer names this process")
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(testutil.PrivateFilePerm()), info.Mode().Perm(),
		"the credentials file must not be readable by anyone but its owner")
}

// The deferral arm's discriminator. "No admin has completed /setup yet" is the ONE
// condition dev mode waits on, and it reaches the switch as errNoAdminYet -- so a
// store.ErrNotFound from anywhere ELSE under bring-up must NOT wear that sentinel,
// or a real registration failure becomes a launch that reports success and polls
// forever against a Worker that is never coming.
func TestLoadOrCreateWorkerState_OnlyTheMissingAdminIsDeferrable(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	t.Run("a missing admin is deferrable", func(t *testing.T) {
		t.Parallel()
		reg := &fakeRegistrar{adminErr: store.ErrNotFound}
		_, _, err := loadOrCreateWorkerState(t.Context(), reg, filepath.Join(t.TempDir(), "state.json"), t.TempDir())
		require.Error(t, err)
		assert.ErrorIs(t, err, errNoAdminYet, "dev mode waits for /setup on exactly this")
		assert.ErrorIs(t, err, store.ErrNotFound, "the underlying cause stays reachable")
	})

	t.Run("a not-found from registration is not", func(t *testing.T) {
		t.Parallel()
		reg := &fakeRegistrar{adminID: "u1", registerErr: store.ErrNotFound}
		_, _, err := loadOrCreateWorkerState(t.Context(), reg, filepath.Join(t.TempDir(), "state.json"), t.TempDir())
		require.Error(t, err)
		assert.NotErrorIs(t, err, errNoAdminYet,
			"the admin existed; a not-found here is a real failure, not something to wait out")
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("a non-not-found admin lookup failure is not", func(t *testing.T) {
		t.Parallel()
		boom := errors.New("disk I/O error")
		reg := &fakeRegistrar{adminErr: boom}
		_, _, err := loadOrCreateWorkerState(t.Context(), reg, filepath.Join(dir, "locked.json"), t.TempDir())
		require.Error(t, err)
		assert.ErrorIs(t, err, boom)
		assert.NotErrorIs(t, err, errNoAdminYet)
	})
}

// An unreadable state file must be MOVED aside, not copied. A copy leaves it
// exactly where the next load reads it, and dev mode's poller re-enters that path
// every tick until somebody completes /setup -- re-logging the orphaning and
// writing a fresh sidecar each time, without bound.
func TestPreserveCorruptState_MovesTheFileAsideSoTheNextLoadCannotSeeIt(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	require.NoError(t, os.WriteFile(path, []byte("{ this is not json"), 0o600))

	preserveCorruptState(path, []byte("{ this is not json"))

	_, err := os.Stat(path)
	assert.ErrorIs(t, err, os.ErrNotExist,
		"the corrupt file must not be left where the next load will read it again")

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	var sidecars int
	for _, e := range entries {
		if strings.Contains(e.Name(), ".corrupt-") {
			sidecars++
			data, readErr := os.ReadFile(filepath.Join(dir, e.Name()))
			require.NoError(t, readErr)
			assert.Equal(t, "{ this is not json", string(data), "the bytes must survive for forensics")
		}
	}
	assert.Equal(t, 1, sidecars)
}
