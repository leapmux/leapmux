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
		OrgID: "org", TabID: "t1", WorkspaceID: "w1", FilePath: "/repo/README.md",
	}))
	wsID, path, err := store.Get(ctx, "org", "t1")
	require.NoError(t, err)
	assert.Equal(t, "w1", wsID)
	assert.Equal(t, "/repo/README.md", path)
}

func TestFileTabPath_GetMissingReturnsNotFound(t *testing.T) {
	store, _, _ := newFileTabPathTestStore(t)
	_, _, err := store.Get(context.Background(), "org", "ghost")
	assert.ErrorIs(t, err, service.ErrFileTabPathNotFound)
}

func TestFileTabPath_RevokeRemovesAndEmits(t *testing.T) {
	store, bus, _ := newFileTabPathTestStore(t)
	ctx := context.Background()
	require.NoError(t, store.Register(ctx, service.RegisterFileTabPathParams{
		OrgID: "org", TabID: "t1", WorkspaceID: "w1", FilePath: "/repo/a.go",
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

	require.NoError(t, store.RevokeRow(ctx, "org", "t1"))

	select {
	case evt := <-got:
		require.NotNil(t, evt.GetFileTabPathRevoked(), "expected FileTabPathRevoked")
		assert.Equal(t, "t1", evt.GetFileTabPathRevoked().GetTabId())
	case <-time.After(time.Second):
		t.Fatal("revoke did not produce a private event")
	}

	// Row gone.
	_, _, err := store.Get(ctx, "org", "t1")
	assert.ErrorIs(t, err, service.ErrFileTabPathNotFound)
}

func TestFileTabPath_RelocateEmitsRevokedThenRegistered(t *testing.T) {
	store, bus, _ := newFileTabPathTestStore(t)
	ctx := context.Background()
	require.NoError(t, store.Register(ctx, service.RegisterFileTabPathParams{
		OrgID: "org", TabID: "t1", WorkspaceID: "w1", FilePath: "/repo/a.go",
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

	require.NoError(t, store.Relocate(ctx, "org", "t1", "w2"))

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
	wsID, path, err := store.Get(ctx, "org", "t1")
	require.NoError(t, err)
	assert.Equal(t, "w2", wsID)
	assert.Equal(t, "/repo/a.go", path)
}

func TestFileTabPath_RelocateMissingReturnsNotFound(t *testing.T) {
	store, _, _ := newFileTabPathTestStore(t)
	err := store.Relocate(context.Background(), "org", "ghost", "w2")
	assert.ErrorIs(t, err, service.ErrFileTabPathNotFound)
}

func TestFileTabPath_SnapshotForWorkspaceFiltersByWorkspace(t *testing.T) {
	store, _, _ := newFileTabPathTestStore(t)
	ctx := context.Background()
	require.NoError(t, store.Register(ctx, service.RegisterFileTabPathParams{
		OrgID: "org", TabID: "t1", WorkspaceID: "w1", FilePath: "/a"}))
	require.NoError(t, store.Register(ctx, service.RegisterFileTabPathParams{
		OrgID: "org", TabID: "t2", WorkspaceID: "w1", FilePath: "/b"}))
	require.NoError(t, store.Register(ctx, service.RegisterFileTabPathParams{
		OrgID: "org", TabID: "t3", WorkspaceID: "w2", FilePath: "/c"}))

	snapW1, err := store.SnapshotForWorkspace(ctx, "w1")
	require.NoError(t, err)
	require.Len(t, snapW1, 2)
	for _, evt := range snapW1 {
		reg := evt.GetFileTabPathRegistered()
		require.NotNil(t, reg)
		assert.Equal(t, "w1", reg.GetWorkspaceId())
	}

	snapW2, err := store.SnapshotForWorkspace(ctx, "w2")
	require.NoError(t, err)
	require.Len(t, snapW2, 1)
	assert.Equal(t, "/c", snapW2[0].GetFileTabPathRegistered().GetFilePath())
}

func TestFileTabPath_SnapshotAndSubscribe_RaceFreeBootstrap(t *testing.T) {
	store, bus, _ := newFileTabPathTestStore(t)
	ctx := context.Background()
	require.NoError(t, store.Register(ctx, service.RegisterFileTabPathParams{
		OrgID: "org", TabID: "tBootstrap", WorkspaceID: "w1", FilePath: "/repo/seed",
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
				snap, err := store.SnapshotForWorkspace(subCtx, workspaceID)
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
		OrgID: "org", TabID: "tLive", WorkspaceID: "w1", FilePath: "/repo/live",
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
		{OrgID: "", TabID: "t1", WorkspaceID: "w1", FilePath: "/p"},
		{OrgID: "org", TabID: "", WorkspaceID: "w1", FilePath: "/p"},
		{OrgID: "org", TabID: "t1", WorkspaceID: "", FilePath: "/p"},
		{OrgID: "org", TabID: "t1", WorkspaceID: "w1", FilePath: ""},
	}
	for _, c := range cases {
		err := store.Register(context.Background(), c)
		require.Error(t, err, "expected error on incomplete params: %+v", c)
	}
}
