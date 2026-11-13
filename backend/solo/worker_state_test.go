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
	noiseutil "github.com/leapmux/leapmux/internal/noise"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/config"
	"github.com/leapmux/leapmux/internal/workerkeys"
	"github.com/leapmux/leapmux/internal/workerkeys/workerkeystest"
	"github.com/leapmux/leapmux/lifecycle"
)

// fakeRegistrar stands in for *hub.Server, recording what the loader asked of it.
type fakeRegistrar struct {
	registration       hub.WorkerRegistration
	registrationErr    error
	registrationTokens []string
	reissuedIDs        []string
	reissuedToken      string
	reissueErr         error
	onReissue          func()

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
	// retireCtxErrs records the context error each retire ran under, so a test
	// can tell a retire that the launch's cancellation would have stopped.
	retireCtxErrs []error
}

func (f *fakeRegistrar) GetWorkerRegistration(_ context.Context, _ string, token string) (hub.WorkerRegistration, error) {
	f.registrationTokens = append(f.registrationTokens, token)
	return f.registration, f.registrationErr
}

func (f *fakeRegistrar) ReissueLocalWorkerToken(_ context.Context, workerID string) (string, error) {
	f.reissuedIDs = append(f.reissuedIDs, workerID)
	if f.onReissue != nil {
		f.onReissue()
	}
	if f.reissueErr != nil {
		return "", f.reissueErr
	}
	if f.reissuedToken == "" {
		return "", errors.New("the test did not configure a reissued worker token")
	}
	return f.reissuedToken, nil
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

func (f *fakeRegistrar) RetireLocalWorker(ctx context.Context, workerID string) error {
	f.retiredIDs = append(f.retiredIDs, workerID)
	f.retireCtxErrs = append(f.retireCtxErrs, ctx.Err())
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

// writeState seeds a solo state file at the path a lease of dir derives
// -- the canonical directory's state.json -- and returns that path. The
// canonical form matters: a lease of a symlinked temporary directory
// and the fixture must name one file, not two.
func writeState(t *testing.T, dir string, s soloState) string {
	t.Helper()
	canonical, err := lifecycle.CanonicalDir(dir)
	require.NoError(t, err)
	path := filepath.Join(canonical, "state.json")
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

// preservedState reads the one state file that a launch set aside.
func preservedState(t *testing.T, path string) soloState {
	t.Helper()
	matches, err := filepath.Glob(path + ".corrupt-*")
	require.NoError(t, err)
	require.Len(t, matches, 1, "a discarded state must be preserved exactly once")
	return readState(t, matches[0])
}

// activeRegistration is the registration of a row a launcher can use.
func activeRegistration(owner string, keys *leapmuxv1.WorkerPublicKeys) hub.WorkerRegistration {
	return hub.WorkerRegistration{AuthTokenAccepted: true,
		Owner:      owner,
		Status:     leapmuxv1.WorkerStatus_WORKER_STATUS_ACTIVE,
		PublicKeys: keys,
	}
}

// loadWithLease drives one loadOrCreateWorkerState call under a valid
// containing-owner lease of stateDir, the way the launcher does. The
// lease releases when the call returns; the saved-state fixtures live
// at the path the lease derives.
func loadWithLease(t *testing.T, reg workerRegistrar, stateDir string) (*soloState, *noiseutil.CompositeKeypair, error) {
	t.Helper()
	canonical, err := lifecycle.CanonicalDir(stateDir)
	require.NoError(t, err)
	owner := lifecycle.DirectoryOwner{
		AccountID: lifecycle.CurrentAccountID(),
		Mode:      lifecycle.RoleSolo,
		Root:      filepath.Dir(canonical),
	}
	lease, err := lifecycle.AcquireDirectory(owner, lifecycle.RoleWorker, canonical)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = lease.Close() }()
	return loadOrCreateWorkerState(context.Background(), reg, lease)
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

	got, ck, err := loadWithLease(t, reg, dir)
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
	second, secondKey, err := loadWithLease(t, reg, dir)
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

			got, _, err := loadWithLease(t, reg, dir)
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
		seedUsableState(t, dir, soloState{WorkerID: "w-old", AuthToken: "tok", RegisteredBy: "admin-1"})
		boom := errors.New("database is locked")
		reg := &fakeRegistrar{
			registration: activeRegistration("admin-1", workerkeystest.Keys(0x77)),
			retireErr:    boom,
			adminID:      "admin-1",
			newWorker:    hub.WorkerCredentials{WorkerID: "w-new", AuthToken: "tok-new"},
		}

		_, _, err := loadWithLease(t, reg, dir)
		require.ErrorIs(t, err, boom)
		assert.Zero(t, reg.registered, "a failed retire must not proceed to register")
		assert.Equal(t, "w-old", readState(t, filepath.Join(dir, "state.json")).WorkerID,
			"state.json must stay in place for a retry")
		matches, globErr := filepath.Glob(filepath.Join(dir, "state.json") + ".corrupt-*")
		require.NoError(t, globErr)
		assert.Empty(t, matches, "a failed retire must not preserve (and thus hide) the file")
	})

	t.Run("a non-bundled row is only logged", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		seedUsableState(t, dir, soloState{WorkerID: "w-old", AuthToken: "tok", RegisteredBy: "admin-1"})
		reg := &fakeRegistrar{
			registration: activeRegistration("admin-1", workerkeystest.Keys(0x77)),
			retireErr:    hub.ErrNotAutoRegistered,
			adminID:      "admin-1",
			newWorker:    hub.WorkerCredentials{WorkerID: "w-new", AuthToken: "tok-new"},
		}

		_, _, err := loadWithLease(t, reg, dir)
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
			// Obstruct the lease-derived destination after registration, so
			// the publisher fails on every platform and privilege level.
			stateDir, stateDirErr := lifecycle.CanonicalDir(dir)
			require.NoError(t, stateDirErr)
			reg := &fakeRegistrar{
				adminID:    "admin-1",
				newWorker:  hub.WorkerCredentials{WorkerID: "w-new", AuthToken: "tok"},
				retireErr:  retireErr,
				onRegister: func() { require.NoError(t, os.Mkdir(filepath.Join(stateDir, "state.json"), 0o700)) },
			}

			_, _, err := loadWithLease(t, reg, dir)
			require.Error(t, err, "a failed write must fail the launch")
			require.ErrorContains(t, err, filepath.Join(stateDir, "state.json"))
			assert.Equal(t, []string{"w-new"}, reg.retiredIDs, "the row whose keys were lost must be retired")
			if retireErr != nil {
				assert.ErrorIs(t, err, retireErr, "the retire failure must be reported too")
			}
		})
	}
}

// The retire that compensates for a failed write must run even when the launch
// is cancelled meanwhile, as solo cancels the bring-up when the Hub stops. A
// retire under the cancelled context would fail its store read, and the new row
// would stay ACTIVE for ever, pinned to private keys that exist nowhere and
// counted against the owner's worker limit.
func TestLoadOrCreateWorkerState_AFailedWriteRetiresTheNewWorkerAfterACancel(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	blocked, blockedErr := lifecycle.CanonicalDir(dir)
	require.NoError(t, blockedErr)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reg := &fakeRegistrar{
		adminID:   "admin-1",
		newWorker: hub.WorkerCredentials{WorkerID: "w-new", AuthToken: "tok"},
		onRegister: func() {
			// The destination becomes a directory only after registration.
			require.NoError(t, os.Mkdir(filepath.Join(blocked, "state.json"), 0o700))
			cancel()
		},
	}

	_, _, err := loadOrCreateWorkerStateCancelled(ctx, t, reg, blocked)
	require.Error(t, err, "a failed write must fail the launch")
	require.ErrorContains(t, err, filepath.Join(blocked, "state.json"))
	require.Equal(t, []string{"w-new"}, reg.retiredIDs, "the row whose keys were lost must be retired")
	assert.NoError(t, reg.retireCtxErrs[0], "the compensating retire must not inherit the launch's cancellation")
}

// loadOrCreateWorkerStateCancelled drives the call under a cancelled
// caller and a valid lease, for the compensating-retire contract.
func loadOrCreateWorkerStateCancelled(ctx context.Context, t *testing.T, reg workerRegistrar, stateDir string) (*soloState, *noiseutil.CompositeKeypair, error) {
	t.Helper()
	canonical, err := lifecycle.CanonicalDir(stateDir)
	require.NoError(t, err)
	lease, leaseErr := lifecycle.AcquireDirectory(
		lifecycle.DirectoryOwner{AccountID: lifecycle.CurrentAccountID(), Mode: lifecycle.RoleSolo, Root: filepath.Dir(canonical)},
		lifecycle.RoleWorker, canonical)
	if leaseErr != nil {
		return nil, nil, leaseErr
	}
	defer func() { _ = lease.Close() }()
	return loadOrCreateWorkerState(ctx, reg, lease)
}

// The DB is the authority on worker ownership: workers.registered_by is NOT NULL and
// is the fact every machine-scoped gate keys on, so a state file that disagrees with
// it loses -- and gets refreshed rather than silently re-read next launch.
func TestLoadOrCreateWorkerState_DBOwnerWinsOverStateFile(t *testing.T) {
	dir := t.TempDir()
	path, keys := seedUsableState(t, dir, soloState{WorkerID: "w1", AuthToken: "tok", RegisteredBy: "stale-user"})
	reg := &fakeRegistrar{registration: activeRegistration("real-owner", keys)}

	got, _, err := loadWithLease(t, reg, dir)
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
	_, keys := seedUsableState(t, dir, soloState{WorkerID: "w1", AuthToken: "tok"})
	reg := &fakeRegistrar{registration: activeRegistration("real-owner", keys), adminID: "current-admin"}

	got, _, err := loadWithLease(t, reg, dir)
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

	got, _, err := loadWithLease(t, reg, dir)
	require.NoError(t, err)

	assert.Equal(t, 1, reg.registered, "an unknown worker must be re-registered")
	assert.Empty(t, reg.retiredIDs, "there is no row to retire")
	assert.Equal(t, "w-new", got.WorkerID)
	assert.Equal(t, "admin-1", got.RegisteredBy, "a NEW worker is attributed to the admin user")
	assert.Equal(t, "w-new", readState(t, path).WorkerID, "the new state must be persisted")
	assert.Equal(t, "gone", preservedState(t, path).WorkerID, "the discarded state must be preserved")
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

	_, _, err := loadWithLease(t, reg, dir)
	require.Error(t, err, "a transient owner-lookup failure must fail the launch, not re-register")
	assert.ErrorIs(t, err, transient, "the underlying store error must be surfaced")
	assert.Empty(t, reg.reissuedIDs)
	assert.Equal(t, "tok", readState(t, path).AuthToken)
	assert.Zero(t, reg.registered, "a transient error must never trigger re-registration")
	assert.Empty(t, reg.retiredIDs)
	assert.Equal(t, "w1", readState(t, path).WorkerID,
		"the saved worker identity must be left intact for a retry")
}

// An ownerless row is unreachable through the schema (registered_by is NOT NULL), so
// it means a hand-edited row or a mint path that dropped it. Re-register rather than
// launch a worker whose every machine-scoped family is permanently dead for its own
// legitimate user -- a fail-closed outage indistinguishable from a real cross-tenant
// refusal. It is one more unusable state, so the row is retired and the file is
// preserved, as for every other unusable state.
func TestLoadOrCreateWorkerState_OwnerlessRowReRegisters(t *testing.T) {
	dir := t.TempDir()
	// ACTIVE with the seeded keys, so the row fails for the owner alone.
	path, keys := seedUsableState(t, dir, soloState{WorkerID: "w1", AuthToken: "tok", RegisteredBy: "someone"})
	reg := &fakeRegistrar{
		registration: activeRegistration("", keys),
		adminID:      "admin-1",
		newWorker:    hub.WorkerCredentials{WorkerID: "w-new", AuthToken: "tok-new"},
	}

	got, _, err := loadWithLease(t, reg, dir)
	require.NoError(t, err)

	assert.Equal(t, 1, reg.registered, "an ownerless worker row must not be launched as-is")
	assert.Equal(t, []string{"w1"}, reg.retiredIDs, "an ownerless row is retired like every unusable row")
	assert.Equal(t, "admin-1", got.RegisteredBy)
	assert.NotEmpty(t, got.RegisteredBy, "a solo worker must never launch without an owner")
	assert.Equal(t, "w1", preservedState(t, path).WorkerID, "the discarded state must be preserved")
}

// A file that lacks the worker ID identifies no row that this launch may retire,
// so it is only preserved.
func TestLoadOrCreateWorkerState_AStateWithNoIdentityIsOnlyPreserved(t *testing.T) {
	t.Parallel()

	for name, saved := range map[string]soloState{
		"no worker id": {AuthToken: "tok", RegisteredBy: "admin-1"},
		"neither":      {RegisteredBy: "admin-1"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := writeState(t, dir, saved)
			reg := &fakeRegistrar{
				adminID:   "admin-1",
				newWorker: hub.WorkerCredentials{WorkerID: "w-new", AuthToken: "tok-new"},
			}

			got, _, err := loadWithLease(t, reg, dir)
			require.NoError(t, err)

			assert.Empty(t, reg.retiredIDs, "the file identifies no row to retire")
			assert.Equal(t, "w-new", got.WorkerID)
			assert.Equal(t, saved.WorkerID, preservedState(t, path).WorkerID, "the discarded state must be preserved")
		})
	}
}

// A file with a worker ID and no token identifies a row that no launch can connect
// as. Left alone, that row stays ACTIVE and offline for good.
func TestLoadOrCreateWorkerState_AStateWithNoKeypairRetiresItsRow(t *testing.T) {
	t.Parallel()

	newRegistrar := func() *fakeRegistrar {
		return &fakeRegistrar{
			registration: activeRegistration("admin-1", workerkeystest.Keys(0x77)),
			adminID:      "admin-1",
			newWorker:    hub.WorkerCredentials{WorkerID: "w-new", AuthToken: "tok-new"},
		}
	}
	saved := soloState{WorkerID: "w-old", RegisteredBy: "admin-1"}

	t.Run("the row is retired and the file is preserved", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := writeState(t, dir, saved)
		reg := newRegistrar()

		got, _, err := loadWithLease(t, reg, dir)
		require.NoError(t, err)

		assert.Equal(t, []string{"w-old"}, reg.retiredIDs)
		assert.Equal(t, "w-new", got.WorkerID)
		assert.Equal(t, "w-old", preservedState(t, path).WorkerID, "the discarded state must be preserved")
	})

	t.Run("a row that is gone leaves nothing to retire", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := writeState(t, dir, saved)
		reg := newRegistrar()
		reg.registrationErr = store.ErrNotFound

		got, _, err := loadWithLease(t, reg, dir)
		require.NoError(t, err)

		assert.Empty(t, reg.retiredIDs, "the store does not know this row")
		assert.Equal(t, "w-new", got.WorkerID)
		assert.Equal(t, "w-old", preservedState(t, path).WorkerID, "the discarded state must be preserved")
	})

	t.Run("a failed lookup fails the launch and keeps the file", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := writeState(t, dir, saved)
		boom := errors.New("disk I/O error")
		reg := newRegistrar()
		reg.registrationErr = boom

		_, _, err := loadWithLease(t, reg, dir)

		require.ErrorIs(t, err, boom)
		assert.Empty(t, reg.retiredIDs)
		assert.Zero(t, reg.registered, "a failed lookup must not proceed to register")
		assert.Equal(t, "w-old", readState(t, path).WorkerID, "state.json must stay in place for a retry")
	})

	t.Run("a failed retire fails the launch and keeps the file", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := writeState(t, dir, saved)
		boom := errors.New("database is locked")
		reg := newRegistrar()
		reg.retireErr = boom

		_, _, err := loadWithLease(t, reg, dir)

		require.ErrorIs(t, err, boom)
		assert.Zero(t, reg.registered, "a failed retire must not proceed to register")
		assert.Equal(t, "w-old", readState(t, path).WorkerID, "state.json must stay in place for a retry")
	})
}

// A retire failure fails the launch whichever reason made the state unusable, so
// the ownerless row keeps its file for a retry as every other unusable row does.
func TestLoadOrCreateWorkerState_ARetireFailureOnAnOwnerlessRowKeepsTheFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path, keys := seedUsableState(t, dir, soloState{WorkerID: "w1", AuthToken: "tok", RegisteredBy: "someone"})
	boom := errors.New("database is locked")
	reg := &fakeRegistrar{
		registration: activeRegistration("", keys),
		retireErr:    boom,
		adminID:      "admin-1",
		newWorker:    hub.WorkerCredentials{WorkerID: "w-new", AuthToken: "tok-new"},
	}

	_, _, err := loadWithLease(t, reg, dir)

	require.ErrorIs(t, err, boom)
	assert.Zero(t, reg.registered, "a failed retire must not proceed to register")
	assert.Equal(t, "w1", readState(t, path).WorkerID, "state.json must stay in place for a retry")
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
	canonical, canonicalErr := lifecycle.CanonicalDir(dir)
	require.NoError(t, canonicalErr)
	path := filepath.Join(canonical, "state.json")
	require.NoError(t, os.WriteFile(path, []byte("{not valid json"), 0o600))
	reg := &fakeRegistrar{
		adminID:   "admin-1",
		newWorker: hub.WorkerCredentials{WorkerID: "w-new", AuthToken: "tok-new"},
	}

	got, _, loadErr := loadWithLease(t, reg, dir)
	require.NoError(t, loadErr, "a corrupt file has no recoverable identity, so re-register is the only path")

	assert.Equal(t, 1, reg.registered, "the unreadable identity must be replaced with a fresh worker")
	assert.Empty(t, reg.retiredIDs, "an unparseable file identifies no row to retire")
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
		stateDir := t.TempDir()
		_, _, err := loadWithLease(t, reg, stateDir)
		require.Error(t, err)
		assert.ErrorIs(t, err, errNoAdminYet, "dev mode waits for /setup on exactly this")
		assert.ErrorIs(t, err, store.ErrNotFound, "the underlying cause stays reachable")
	})

	t.Run("a not-found from registration is not", func(t *testing.T) {
		t.Parallel()
		reg := &fakeRegistrar{adminID: "u1", registerErr: store.ErrNotFound}
		_, _, err := loadWithLease(t, reg, t.TempDir())
		require.Error(t, err)
		assert.NotErrorIs(t, err, errNoAdminYet,
			"the admin existed; a not-found here is a real failure, not something to wait out")
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("a non-not-found admin lookup failure is not", func(t *testing.T) {
		t.Parallel()
		boom := errors.New("disk I/O error")
		reg := &fakeRegistrar{adminErr: boom}
		_, _, err := loadWithLease(t, reg, dir)
		require.Error(t, err)
		assert.ErrorIs(t, err, boom)
		assert.NotErrorIs(t, err, errNoAdminYet)
	})
}

// An unreadable state file must be MOVED aside, not copied. A copy leaves it
// exactly where the next load reads it, and dev mode's poller re-enters that path
// every tick until somebody completes /setup -- re-logging the orphaning and
// writing a fresh sidecar each time, without bound.
func TestPreserveDiscardedState_MovesTheFileAsideSoTheNextLoadCannotSeeIt(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	require.NoError(t, os.WriteFile(path, []byte("{ this is not json"), 0o600))

	preserveDiscardedState(path, []byte("{ this is not json"))

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

func TestLoadOrCreateWorkerState_ReusesOrReissuesTheTokenWithoutChangingIdentity(t *testing.T) {
	for _, tc := range []struct {
		name     string
		token    string
		accepted bool
	}{
		{"accepted", "original-token", true},
		{"refused", "original-token", false},
		{"absent", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path, keys := seedUsableState(t, dir, soloState{WorkerID: "w-original", AuthToken: tc.token, RegisteredBy: "owner"})
			before := readState(t, path)
			registration := activeRegistration("owner", keys)
			registration.AuthTokenAccepted = tc.accepted
			reg := &fakeRegistrar{registration: registration, reissuedToken: "replacement-token"}
			got, composite, err := loadWithLease(t, reg, dir)
			require.NoError(t, err)
			require.NotNil(t, composite)
			assert.Equal(t, "w-original", got.WorkerID)
			assert.True(t, before.KeyMaterial == got.KeyMaterial, "recovery preserves private keys")
			persisted := readState(t, path)
			assert.Equal(t, before.WorkerID, persisted.WorkerID)
			assert.True(t, before.KeyMaterial == persisted.KeyMaterial, "recovery persists the original private keys")
			publicKeys, err := workerkeys.Of(composite)
			require.NoError(t, err)
			assert.True(t, workerkeys.Equal(keys, publicKeys))
			assert.Equal(t, []string{tc.token}, reg.registrationTokens)
			want := tc.token
			if tc.accepted {
				assert.Empty(t, reg.reissuedIDs)
			} else {
				want = "replacement-token"
				assert.Equal(t, []string{"w-original"}, reg.reissuedIDs)
			}
			assert.Equal(t, want, got.AuthToken)
			assert.Equal(t, want, readState(t, path).AuthToken)
			assert.Zero(t, reg.registered)
			assert.Empty(t, reg.retiredIDs)
		})
	}
}

func TestLoadOrCreateWorkerState_KeepsSavedStateOnAReissueFailure(t *testing.T) {
	dir := t.TempDir()
	path, keys := seedUsableState(t, dir, soloState{WorkerID: "w-original", AuthToken: "original-token", RegisteredBy: "owner"})
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	registration := activeRegistration("owner", keys)
	registration.AuthTokenAccepted = false
	failure := errors.New("worker token store unavailable")
	reg := &fakeRegistrar{registration: registration, reissueErr: failure}
	got, composite, err := loadWithLease(t, reg, dir)
	require.ErrorIs(t, err, failure)
	assert.Nil(t, got)
	assert.Nil(t, composite)
	assert.Equal(t, []string{"w-original"}, reg.reissuedIDs)
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, before, after)
	assert.Empty(t, reg.retiredIDs)
	assert.Zero(t, reg.registered)
}

func TestLoadOrCreateWorkerState_ChecksPinnedKeysBeforeReissuingARefusedToken(t *testing.T) {
	dir := t.TempDir()
	path, _ := seedUsableState(t, dir, soloState{WorkerID: "w-original", AuthToken: "original-token", RegisteredBy: "owner"})
	registration := activeRegistration("owner", workerkeystest.Keys(0x77))
	registration.AuthTokenAccepted = false
	reg := &fakeRegistrar{registration: registration, adminID: "owner", newWorker: hub.WorkerCredentials{WorkerID: "new-worker", AuthToken: "fresh-token"}}
	got, _, err := loadWithLease(t, reg, dir)
	require.NoError(t, err)
	assert.Equal(t, "new-worker", got.WorkerID)
	assert.Empty(t, reg.reissuedIDs)
	assert.Equal(t, []string{"w-original"}, reg.retiredIDs)
	assert.Equal(t, "w-original", preservedState(t, path).WorkerID)
}

func TestLoadOrCreateWorkerState_DiscardsStateWhenAReissueCannotRecoverTheRow(t *testing.T) {
	for _, failure := range []error{hub.ErrNotAutoRegistered, errors.Join(errors.New("row removed"), store.ErrNotFound)} {
		t.Run(failure.Error(), func(t *testing.T) {
			dir := t.TempDir()
			path, keys := seedUsableState(t, dir, soloState{WorkerID: "w-original", AuthToken: "original-token", RegisteredBy: "owner"})
			registration := activeRegistration("owner", keys)
			registration.AuthTokenAccepted = false
			reg := &fakeRegistrar{registration: registration, reissueErr: failure, adminID: "owner", newWorker: hub.WorkerCredentials{WorkerID: "new-worker", AuthToken: "fresh-token"}}
			got, _, err := loadWithLease(t, reg, dir)
			require.NoError(t, err)
			assert.Equal(t, "new-worker", got.WorkerID)
			assert.Equal(t, []string{"w-original"}, reg.reissuedIDs)
			assert.Equal(t, []string{"w-original"}, reg.retiredIDs)
			assert.Equal(t, "w-original", preservedState(t, path).WorkerID)
		})
	}
}
