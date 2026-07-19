package solo

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/hub"
	"github.com/leapmux/leapmux/internal/hub/store"
)

// fakeRegistrar stands in for *hub.Server, recording what the loader asked of it.
type fakeRegistrar struct {
	owner      string
	ownerErr   error
	adminID    string
	registered int
	newWorker  hub.WorkerCredentials
}

func (f *fakeRegistrar) GetWorkerOwner(context.Context, string) (string, error) {
	return f.owner, f.ownerErr
}

func (f *fakeRegistrar) GetAdminUser(context.Context) (string, string, error) {
	return f.adminID, "org-1", nil
}

func (f *fakeRegistrar) RegisterWorker(context.Context, string) (*hub.WorkerCredentials, error) {
	f.registered++
	c := f.newWorker
	return &c, nil
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

func readState(t *testing.T, path string) soloState {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var s soloState
	require.NoError(t, json.Unmarshal(data, &s))
	return s
}

// The DB is the authority on worker ownership: workers.registered_by is NOT NULL and
// is the fact every machine-scoped gate keys on, so a state file that disagrees with
// it loses -- and gets refreshed rather than silently re-read next launch.
func TestLoadOrCreateWorkerState_DBOwnerWinsOverStateFile(t *testing.T) {
	dir := t.TempDir()
	path := writeState(t, dir, soloState{WorkerID: "w1", AuthToken: "tok", RegisteredBy: "stale-user"})
	reg := &fakeRegistrar{owner: "real-owner"}

	got, err := loadOrCreateWorkerState(context.Background(), reg, path, filepath.Join(dir, "data"))
	require.NoError(t, err)

	assert.Equal(t, "real-owner", got.RegisteredBy, "the DB's owner must win over the state file's")
	assert.Equal(t, "w1", got.WorkerID, "a worker with a known owner must not be re-registered")
	assert.Zero(t, reg.registered)
	assert.Equal(t, "real-owner", readState(t, path).RegisteredBy,
		"the corrected owner must be written back so the next launch matches")
}

// A state file predating the field (no RegisteredBy) must take the DB's owner --
// NOT a backfill from whoever the admin happens to be now, which answers a different
// question and would silently reassign the machine on any install where the admin
// changed.
func TestLoadOrCreateWorkerState_MissingOwnerTakenFromDBNotAdmin(t *testing.T) {
	dir := t.TempDir()
	path := writeState(t, dir, soloState{WorkerID: "w1", AuthToken: "tok"})
	reg := &fakeRegistrar{owner: "real-owner", adminID: "current-admin"}

	got, err := loadOrCreateWorkerState(context.Background(), reg, path, filepath.Join(dir, "data"))
	require.NoError(t, err)

	assert.Equal(t, "real-owner", got.RegisteredBy,
		"an owner-less state file must be repaired from the DB, not from the current admin")
	assert.NotEqual(t, "current-admin", got.RegisteredBy)
	assert.Zero(t, reg.registered)
}

// A worker the DB does not know (deleted, or a stale file from another install) is
// re-registered rather than launched against a missing row.
func TestLoadOrCreateWorkerState_UnknownWorkerReRegisters(t *testing.T) {
	dir := t.TempDir()
	path := writeState(t, dir, soloState{WorkerID: "gone", AuthToken: "tok", RegisteredBy: "someone"})
	reg := &fakeRegistrar{
		ownerErr:  store.ErrNotFound,
		adminID:   "admin-1",
		newWorker: hub.WorkerCredentials{WorkerID: "w-new", AuthToken: "tok-new"},
	}

	got, err := loadOrCreateWorkerState(context.Background(), reg, path, filepath.Join(dir, "data"))
	require.NoError(t, err)

	assert.Equal(t, 1, reg.registered, "an unknown worker must be re-registered")
	assert.Equal(t, "w-new", got.WorkerID)
	assert.Equal(t, "admin-1", got.RegisteredBy, "a NEW worker is attributed to the admin user")
	assert.Equal(t, "w-new", readState(t, path).WorkerID, "the new state must be persisted")
}

// A transient store failure (e.g. sqlite "database is locked" racing another writer
// at startup) is NOT a missing worker. Treating it as one would discard the saved
// WorkerID and re-register a brand-new identity, orphaning every workspace and tab
// still pointed at the old worker. The launch must fail so a retry finds the row
// intact -- only store.ErrNotFound re-registers.
func TestLoadOrCreateWorkerState_TransientLookupErrorDoesNotReRegister(t *testing.T) {
	dir := t.TempDir()
	path := writeState(t, dir, soloState{WorkerID: "w1", AuthToken: "tok", RegisteredBy: "real-owner"})
	transient := errors.New("database is locked")
	reg := &fakeRegistrar{
		ownerErr:  transient,
		adminID:   "admin-1",
		newWorker: hub.WorkerCredentials{WorkerID: "w-new", AuthToken: "tok-new"},
	}

	_, err := loadOrCreateWorkerState(context.Background(), reg, path, filepath.Join(dir, "data"))
	require.Error(t, err, "a transient owner-lookup failure must fail the launch, not re-register")
	assert.ErrorIs(t, err, transient, "the underlying store error must be surfaced")
	assert.Zero(t, reg.registered, "a transient error must never trigger re-registration")
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
	path := writeState(t, dir, soloState{WorkerID: "w1", AuthToken: "tok", RegisteredBy: "someone"})
	reg := &fakeRegistrar{
		owner:     "", // known worker, but no recorded owner
		adminID:   "admin-1",
		newWorker: hub.WorkerCredentials{WorkerID: "w-new", AuthToken: "tok-new"},
	}

	got, err := loadOrCreateWorkerState(context.Background(), reg, path, filepath.Join(dir, "data"))
	require.NoError(t, err)

	assert.Equal(t, 1, reg.registered, "an ownerless worker row must not be launched as-is")
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
// overwriting them -- a silent fall-through here is the one failure mode the
// store-side guard cannot cover, because the local file is the only record.
func TestLoadOrCreateWorkerState_CorruptStateReRegistersLoudlyAndPreserves(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "worker-state.json")
	require.NoError(t, os.WriteFile(path, []byte("{not valid json"), 0o600))
	reg := &fakeRegistrar{
		adminID:   "admin-1",
		newWorker: hub.WorkerCredentials{WorkerID: "w-new", AuthToken: "tok-new"},
	}

	got, err := loadOrCreateWorkerState(context.Background(), reg, path, filepath.Join(dir, "data"))
	require.NoError(t, err, "a corrupt file has no recoverable identity, so re-register is the only path")

	assert.Equal(t, 1, reg.registered, "the unreadable identity must be replaced with a fresh worker")
	assert.Equal(t, "w-new", got.WorkerID)
	assert.Equal(t, "w-new", readState(t, path).WorkerID, "the fresh identity must be persisted")

	// The corrupt bytes must be preserved (not silently overwritten) so an operator
	// can re-link the orphaned workspaces before the reaper collects them.
	matches, err := filepath.Glob(filepath.Join(dir, "worker-state.json.corrupt-*"))
	require.NoError(t, err)
	require.Len(t, matches, 1, "the corrupt state must be set aside under a .corrupt-<ts> name")
	preserved, err := os.ReadFile(matches[0])
	require.NoError(t, err)
	assert.Equal(t, "{not valid json", string(preserved), "the corrupt bytes must be preserved verbatim")
}

// persistState writes atomically (temp file in the same dir, then rename), so a
// reader never observes a half-written file. The observable contract from outside
// is: the written file round-trips through the parser, and no .tmp remnants are
// left behind on success.
func TestPersistState_RoundTripsAndLeavesNoTempRemnants(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "worker-state.json")
	in := soloState{WorkerID: "w1", AuthToken: "tok", RegisteredBy: "owner", PublicKey: "pk", PrivateKey: "sk"}

	require.NoError(t, persistState(path, &in))

	assert.Equal(t, in, readState(t, path), "the written state must round-trip exactly")
	tmpRemnants, err := filepath.Glob(filepath.Join(dir, ".worker-state-*.tmp"))
	require.NoError(t, err)
	assert.Empty(t, tmpRemnants, "a successful atomic write must not leave temp files behind")
}
