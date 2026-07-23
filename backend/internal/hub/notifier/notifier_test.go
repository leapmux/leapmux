package notifier

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/storetest"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/hub/workermgr"
	"github.com/leapmux/leapmux/internal/util/id"
)

// fakeRegistry is the seam New's workerRegistry parameter exists to provide.
// Before New took the interface, a test could not substitute this without
// reaching into the unexported field, so the offline path below had no
// coverage at all.
type fakeRegistry struct {
	conn        *workermgr.Conn
	lookups     []string
	deregisters []string
	cleared     []string
}

func (f *fakeRegistry) ConnForTrustedPath(workerID string) *workermgr.Conn {
	f.lookups = append(f.lookups, workerID)
	return f.conn
}
func (f *fakeRegistry) MarkDeregistering(workerID string) {
	f.deregisters = append(f.deregisters, workerID)
}
func (f *fakeRegistry) ClearDeregistering(workerID string) { f.cleared = append(f.cleared, workerID) }

// An offline worker must have its notification PERSISTED, not dropped.
//
// The queue is what makes delivery reliable across a worker restart, so a
// silent drop here loses the notification entirely -- the worker reconnects and
// never learns anything happened.
func TestSendOrQueue_OfflineWorkerPersistsToQueue(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	ctx := context.Background()

	orgID := id.Generate()
	require.NoError(t, st.Orgs().Create(ctx, store.CreateOrgParams{ID: orgID, Name: "notifier-org"}))
	userID := id.Generate()
	require.NoError(t, st.Users().Create(ctx, store.CreateUserParams{
		ID: userID, OrgID: orgID, Username: "owner", PasswordHash: "h",
		DisplayName: "Owner", PasswordSet: true,
	}))
	worker := storetest.SeedWorker(t, st, userID).ID

	// conn == nil is the offline case.
	reg := &fakeRegistry{}
	cfg := &config.Config{}
	n := New(st, reg, workermgr.NewPendingRequests(cfg.APITimeout), cfg)

	err := n.SendOrQueue(ctx, worker, leapmuxv1.NotificationType_NOTIFICATION_TYPE_UNSPECIFIED,
		`{"hello":"world"}`, &leapmuxv1.ConnectResponse{})
	require.NoError(t, err, "an offline worker is not an error -- the notification is queued")

	assert.Equal(t, []string{worker}, reg.lookups,
		"the registry is consulted exactly once, for the named worker")

	queued, err := st.WorkerNotifications().ListPendingByWorker(ctx, worker)
	require.NoError(t, err)
	require.Len(t, queued, 1, "the notification must be persisted for later delivery")
	assert.Equal(t, `{"hello":"world"}`, queued[0].Payload)
}

// SendDeregister must mark the worker deregistering BEFORE the notification is
// queued, and must not clear that mark on its own.
//
// The mark is what stops the registry handing out the connection while the
// worker is being torn down; clearing it belongs to
// ProcessPendingNotifications, after the worker has acknowledged. Doing both
// here -- or neither -- would leave a deregistering worker reachable, which is
// the one containment action an operator has.
func TestSendDeregister_MarksDeregisteringAndDoesNotClear(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	ctx := context.Background()

	orgID := id.Generate()
	require.NoError(t, st.Orgs().Create(ctx, store.CreateOrgParams{ID: orgID, Name: "deregister-org"}))
	userID := id.Generate()
	require.NoError(t, st.Users().Create(ctx, store.CreateUserParams{
		ID: userID, OrgID: orgID, Username: "owner", PasswordHash: "h",
		DisplayName: "Owner", PasswordSet: true,
	}))
	worker := storetest.SeedWorker(t, st, userID).ID

	reg := &fakeRegistry{} // offline, so the notification queues
	cfg := &config.Config{}
	n := New(st, reg, workermgr.NewPendingRequests(cfg.APITimeout), cfg)

	require.NoError(t, n.SendDeregister(ctx, worker))

	assert.Equal(t, []string{worker}, reg.deregisters,
		"the worker must be marked deregistering, for exactly the named id")
	assert.Empty(t, reg.cleared,
		"the mark is cleared only after the worker acknowledges, not at send time")
}
