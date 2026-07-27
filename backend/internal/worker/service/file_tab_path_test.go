package service_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	"github.com/leapmux/leapmux/internal/util/userid"
	workerdb "github.com/leapmux/leapmux/internal/worker/db"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/service"
)

// newFileTabPathTestStore creates a worker DB and FileTabPathStore for
// tests. Returns the store along with the bus so tests can subscribe.
func newFileTabPathTestStore(t *testing.T) (*service.FileTabPathStore, *service.PrivateEventsBus, *db.Queries) {
	t.Helper()
	sqlDB, err := workerdb.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, workerdb.Migrate(sqlDB))
	q := db.New(sqlDB)
	bus := service.NewPrivateEventsBus()
	t.Cleanup(bus.Stop)
	return service.NewFileTabPathStore(q, bus), bus, q
}

func TestFileTabPath_RegisterAndGet(t *testing.T) {
	store, _, _ := newFileTabPathTestStore(t)
	ctx := context.Background()
	require.NoError(t, store.Register(ctx, service.RegisterFileTabPathParams{
		UserID: "user-1", TabID: "t1", WorkspaceID: "w1", FilePath: "/repo/README.md",
	}))
	wsID, path, err := store.Get(ctx, "user-1", "t1")
	require.NoError(t, err)
	assert.Equal(t, "w1", wsID)
	assert.Equal(t, "/repo/README.md", path)
}

func TestFileTabPath_GetMissingReturnsNotFound(t *testing.T) {
	store, _, _ := newFileTabPathTestStore(t)
	_, _, err := store.Get(context.Background(), "user-1", "ghost")
	assert.ErrorIs(t, err, service.ErrFileTabPathNotFound)
}

func TestFileTabPath_RevokeRemovesAndEmits(t *testing.T) {
	store, bus, _ := newFileTabPathTestStore(t)
	ctx := context.Background()
	require.NoError(t, store.Register(ctx, service.RegisterFileTabPathParams{
		UserID: "user-1", TabID: "t1", WorkspaceID: "w1", FilePath: "/repo/a.go",
	}))

	// Subscribe to the workspace; expect a Revoked event after revoke.
	got := make(chan *leapmuxv1.WorkspacePrivateEvent, 4)
	subCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	go func() {
		_ = bus.Subscribe(subCtx, "w1", func(evt *leapmuxv1.WorkspacePrivateEvent) error {
			got <- evt
			return nil
		})
	}()
	time.Sleep(50 * time.Millisecond)

	require.NoError(t, store.RevokeRow(ctx, "user-1", "t1"))

	select {
	case evt := <-got:
		require.NotNil(t, evt.GetFileTabPathRevoked(), "expected FileTabPathRevoked")
		assert.Equal(t, "t1", evt.GetFileTabPathRevoked().GetTabId())
	case <-time.After(time.Second):
		t.Fatal("revoke did not produce a private event")
	}

	// Row gone.
	_, _, err := store.Get(ctx, "user-1", "t1")
	assert.ErrorIs(t, err, service.ErrFileTabPathNotFound)
}

func TestFileTabPath_RelocateEmitsRevokedThenRegistered(t *testing.T) {
	store, bus, _ := newFileTabPathTestStore(t)
	ctx := context.Background()
	require.NoError(t, store.Register(ctx, service.RegisterFileTabPathParams{
		UserID: "user-1", TabID: "t1", WorkspaceID: "w1", FilePath: "/repo/a.go",
	}))

	subCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	srcEvents := make(chan *leapmuxv1.WorkspacePrivateEvent, 4)
	dstEvents := make(chan *leapmuxv1.WorkspacePrivateEvent, 4)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_ = bus.Subscribe(subCtx, "w1", func(evt *leapmuxv1.WorkspacePrivateEvent) error {
			srcEvents <- evt
			return nil
		})
	}()
	go func() {
		defer wg.Done()
		_ = bus.Subscribe(subCtx, "w2", func(evt *leapmuxv1.WorkspacePrivateEvent) error {
			dstEvents <- evt
			return nil
		})
	}()
	time.Sleep(50 * time.Millisecond)

	require.NoError(t, store.Relocate(ctx, "user-1", "t1", "w2"))

	select {
	case evt := <-srcEvents:
		require.NotNil(t, evt.GetFileTabPathRevoked(),
			"source workspace must see Revoked (no FileTabPathRelocated event in plan)")
	case <-time.After(time.Second):
		t.Fatal("source workspace did not get Revoked")
	}
	select {
	case evt := <-dstEvents:
		reg := evt.GetFileTabPathRegistered()
		require.NotNil(t, reg, "destination must see Registered with full path")
		assert.Equal(t, "w2", reg.GetWorkspaceId())
		assert.Equal(t, "/repo/a.go", reg.GetFilePath())
	case <-time.After(time.Second):
		t.Fatal("destination workspace did not get Registered")
	}

	// Worker row reflects the new workspace.
	wsID, path, err := store.Get(ctx, "user-1", "t1")
	require.NoError(t, err)
	assert.Equal(t, "w2", wsID)
	assert.Equal(t, "/repo/a.go", path)
}

func TestFileTabPath_RelocateMissingReturnsNotFound(t *testing.T) {
	store, _, _ := newFileTabPathTestStore(t)
	err := store.Relocate(context.Background(), "user-1", "ghost", "w2")
	assert.ErrorIs(t, err, service.ErrFileTabPathNotFound)
}

func TestFileTabPath_SnapshotForWorkspaceFiltersByWorkspace(t *testing.T) {
	store, _, _ := newFileTabPathTestStore(t)
	ctx := context.Background()
	require.NoError(t, store.Register(ctx, service.RegisterFileTabPathParams{
		UserID: "user-1", TabID: "t1", WorkspaceID: "w1", FilePath: "/a"}))
	require.NoError(t, store.Register(ctx, service.RegisterFileTabPathParams{
		UserID: "user-1", TabID: "t2", WorkspaceID: "w1", FilePath: "/b"}))
	require.NoError(t, store.Register(ctx, service.RegisterFileTabPathParams{
		UserID: "user-1", TabID: "t3", WorkspaceID: "w2", FilePath: "/c"}))

	user1 := userid.MustNew("user-1")
	snapW1, err := store.SnapshotForWorkspace(ctx, user1, "w1")
	require.NoError(t, err)
	require.Len(t, snapW1, 2)
	for _, evt := range snapW1 {
		reg := evt.GetFileTabPathRegistered()
		require.NotNil(t, reg)
		assert.Equal(t, "w1", reg.GetWorkspaceId())
	}

	snapW2, err := store.SnapshotForWorkspace(ctx, user1, "w2")
	require.NoError(t, err)
	require.Len(t, snapW2, 1)
	assert.Equal(t, "/c", snapW2[0].GetFileTabPathRegistered().GetFilePath())
}

// TestFileTabPath_SnapshotForWorkspaceBindsTheOwner proves the snapshot's
// owner predicate is load-bearing rather than incidental.
//
// Production cannot reach this state -- workspace_id is unique across users --
// so the seed here is deliberately impossible data. That is the point: it is
// the only way to show the SQL binds user_id, so that a future edit which drops
// the predicate (returning to the whole-table walk this replaced) fails here
// instead of silently leaking another owner's file paths into a snapshot.
func TestFileTabPath_SnapshotForWorkspaceBindsTheOwner(t *testing.T) {
	store, _, _ := newFileTabPathTestStore(t)
	ctx := context.Background()
	require.NoError(t, store.Register(ctx, service.RegisterFileTabPathParams{
		UserID: "user-1", TabID: "t1", WorkspaceID: "shared-ws", FilePath: "/mine"}))
	require.NoError(t, store.Register(ctx, service.RegisterFileTabPathParams{
		UserID: "user-2", TabID: "t2", WorkspaceID: "shared-ws", FilePath: "/theirs"}))

	mine, err := store.SnapshotForWorkspace(ctx, userid.MustNew("user-1"), "shared-ws")
	require.NoError(t, err)
	require.Len(t, mine, 1, "only the caller's own row is replayed")
	assert.Equal(t, "/mine", mine[0].GetFileTabPathRegistered().GetFilePath())

	theirs, err := store.SnapshotForWorkspace(ctx, userid.MustNew("user-2"), "shared-ws")
	require.NoError(t, err)
	require.Len(t, theirs, 1)
	assert.Equal(t, "/theirs", theirs[0].GetFileTabPathRegistered().GetFilePath())

	// An unminted owner reaches no row, rather than falling back to every row.
	none, err := store.SnapshotForWorkspace(ctx, userid.UserID{}, "shared-ws")
	require.NoError(t, err)
	assert.Empty(t, none, "a zero owner selects nothing")
}

func TestFileTabPath_SnapshotAndSubscribe_RaceFreeBootstrap(t *testing.T) {
	store, bus, _ := newFileTabPathTestStore(t)
	ctx := context.Background()
	require.NoError(t, store.Register(ctx, service.RegisterFileTabPathParams{
		UserID: "user-1", TabID: "tBootstrap", WorkspaceID: "w1", FilePath: "/repo/seed",
	}))

	// SnapshotAndSubscribe must replay the existing row before any
	// subsequent live event. The atomicity guarantee is critical: an
	// external Register that lands during the subscribe call must not be
	// missed.
	subCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	got := make(chan *leapmuxv1.WorkspacePrivateEvent, 8)
	go func() {
		_ = bus.SnapshotAndSubscribe(subCtx, "w1",
			func(workspaceID string) []*leapmuxv1.WorkspacePrivateEvent {
				snap, err := store.SnapshotForWorkspace(subCtx, userid.MustNew("user-1"), workspaceID)
				if err != nil {
					return nil
				}
				return snap
			},
			func(evt *leapmuxv1.WorkspacePrivateEvent) error {
				got <- evt
				return nil
			})
	}()
	time.Sleep(50 * time.Millisecond)

	// A live Register after subscribe should arrive after the bootstrap
	// snapshot.
	require.NoError(t, store.Register(ctx, service.RegisterFileTabPathParams{
		UserID: "user-1", TabID: "tLive", WorkspaceID: "w1", FilePath: "/repo/live",
	}))

	collected := []*leapmuxv1.WorkspacePrivateEvent{}
	timeout := time.After(time.Second)
	for len(collected) < 2 {
		select {
		case evt := <-got:
			collected = append(collected, evt)
		case <-timeout:
			t.Fatalf("expected 2 events, got %d", len(collected))
		}
	}
	// The first event must be the bootstrap row (tBootstrap).
	assert.Equal(t, "tBootstrap", collected[0].GetFileTabPathRegistered().GetTabId())
	assert.Equal(t, "tLive", collected[1].GetFileTabPathRegistered().GetTabId())
}

func TestFileTabPath_RegisterRequiresAllFields(t *testing.T) {
	store, _, _ := newFileTabPathTestStore(t)
	cases := []service.RegisterFileTabPathParams{
		{UserID: "", TabID: "t1", WorkspaceID: "w1", FilePath: "/p"},
		{UserID: "user-1", TabID: "", WorkspaceID: "w1", FilePath: "/p"},
		{UserID: "user-1", TabID: "t1", WorkspaceID: "", FilePath: "/p"},
		{UserID: "user-1", TabID: "t1", WorkspaceID: "w1", FilePath: ""},
	}
	for _, c := range cases {
		err := store.Register(context.Background(), c)
		require.Error(t, err, "expected error on incomplete params: %+v", c)
	}
}

// TestFileTabPathStore_RefusesBlankOwner pins the blank-owner floor on
// worker_file_tabs from BOTH ends.
//
// The table is keyed by (user_id, tab_id), so a blank user_id is not "no
// filter" -- it is half the row's identity gone missing. The floor is now two
// layers: the schema refuses to STORE one (CHECK (user_id <> ”)), and the
// three tab-keyed entry points refuse to READ, DELETE, or RELOCATE by one.
//
// This test used to seed a blank-owner row directly through sqlc, because the
// schema permitted it and only the Go guards stood in the way. It cannot any
// more -- that seed is now a constraint violation, which is the stronger
// property and is asserted first. The Go guards stay pinned against a real
// owner's row, since they defend the caller-side mistake (a lost tenant) that
// no constraint can catch.
func TestFileTabPathStore_RefusesBlankOwner(t *testing.T) {
	store, _, q := newFileTabPathTestStore(t)
	ctx := context.Background()

	// The schema floor. This is the assertion that makes the blank-owner row
	// unrepresentable rather than merely unreachable through one API: the
	// worker's database has no users table, so the hub's REFERENCES users(id)
	// cannot reach here and a CHECK is the only available floor.
	require.Error(t, q.UpsertWorkerFileTab(ctx, db.UpsertWorkerFileTabParams{
		UserID:      "",
		TabID:       "shared-tab",
		WorkspaceID: "ws-blank",
		FilePath:    "/blank/a.go",
	}), "the schema must refuse a blank owner even when sqlc is driven directly")

	// A real owner's row, which every control below resolves against.
	require.NoError(t, store.Register(ctx, service.RegisterFileTabPathParams{
		UserID: "user-1", TabID: "shared-tab", WorkspaceID: "ws-1", FilePath: "/real/a.go",
	}))

	// The Go guards. Each pairs a blank-owner refusal with a real-owner
	// control, so a passing assertion means "refused" and not "the fixture was
	// never reachable".
	t.Run("get", func(t *testing.T) {
		_, _, err := store.Get(ctx, "", "shared-tab")
		require.Error(t, err)
		assert.NotErrorIs(t, err, service.ErrFileTabPathNotFound,
			"a blank owner is a caller bug, not a missing row")

		wsID, path, err := store.Get(ctx, "user-1", "shared-tab")
		require.NoError(t, err, "control: a real owner still resolves")
		assert.Equal(t, "ws-1", wsID)
		assert.Equal(t, "/real/a.go", path)
	})

	t.Run("relocate", func(t *testing.T) {
		require.Error(t, store.Relocate(ctx, "", "shared-tab", "ws-moved"))
		row, err := q.GetWorkerFileTab(ctx, db.GetWorkerFileTabParams{UserID: "user-1", TabID: "shared-tab"})
		require.NoError(t, err)
		assert.Equal(t, "ws-1", row.WorkspaceID, "a blank-owner relocate must move nothing")

		require.NoError(t, store.Relocate(ctx, "user-1", "shared-tab", "ws-moved"),
			"control: a real owner still relocates")
	})

	t.Run("revoke", func(t *testing.T) {
		require.Error(t, store.RevokeRow(ctx, "", "shared-tab"))
		_, err := q.GetWorkerFileTab(ctx, db.GetWorkerFileTabParams{UserID: "user-1", TabID: "shared-tab"})
		require.NoError(t, err, "a blank-owner revoke must delete nothing")

		require.NoError(t, store.RevokeRow(ctx, "user-1", "shared-tab"),
			"control: a real owner still revokes")
	})

	// A blank tab id is refused on the same footing.
	_, _, err := store.Get(ctx, "user-1", "")
	assert.Error(t, err)
}
