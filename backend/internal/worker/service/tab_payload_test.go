package service_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	"github.com/leapmux/leapmux/internal/util/userid"
	workerdb "github.com/leapmux/leapmux/internal/worker/db"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/service"
)

// absTestPath renders a POSIX-style path literal as a NATIVE absolute path, for
// the fixtures in this file and in orphan_reconciler_test.go.
//
// The absoluteness guards these tests drive -- TabPayloadStore.Register's
// filepath.IsAbs on file_path, and normalizeWorkingDir's on working_dir -- are
// platform-specific, and a rooted-but-driveless "/repo/a.go" is NOT absolute on
// Windows. Hardcoded POSIX literals therefore made every Register below fail
// there with `file_path must be absolute`, for a reason no test here meant to
// assert -- and made TestTabPayload_RegisterRefusesRelativeWorkingDir pass for
// the wrong one, since it never reached the working_dir guard it exists to pin.
//
// The paths stay fictional and never reach the filesystem: nothing here opens a
// file, and Register's only disk touch is linkTabToWorktree's best-effort
// `git rev-parse` probe, which fails the same way on a nonexistent directory on
// every platform. They only have to be absolute, and to survive the round trip
// through the database unchanged (Register stores file_path verbatim and
// normalizeWorkingDir does not canonicalize, so they do).
func absTestPath(p string) string {
	return filepath.FromSlash(testPathVolume + p)
}

// testPathVolume is the volume component of the test binary's working directory:
// "" on POSIX (where the literals are already absolute), "C:"/"D:"/... on
// Windows. Derived rather than hardcoded so absTestPath names a volume that
// exists on whatever host is running.
var testPathVolume = func() string {
	wd, err := os.Getwd()
	if err != nil {
		// Only reachable if the cwd was unlinked mid-run. Returning "" keeps
		// absTestPath total; on Windows the result is then not absolute, so
		// Register refuses it and the test fails loudly rather than quietly
		// exercising some other path.
		return ""
	}
	return filepath.VolumeName(wd)
}()

// newTabPayloadTestStore creates a worker DB and TabPayloadStore for
// tests. Returns the store along with the bus so tests can subscribe.
func newTabPayloadTestStore(t *testing.T) (*service.TabPayloadStore, *service.PrivateEventsBus, *db.Queries) {
	t.Helper()
	sqlDB, err := workerdb.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, workerdb.Migrate(context.Background(), sqlDB))
	q := db.New(sqlDB)
	bus := service.NewPrivateEventsBus()
	t.Cleanup(bus.Stop)
	return service.NewTabPayloadStore(q, bus), bus, q
}

// TestAbsTestPath_IsAbsoluteOnEveryHost pins the one property every fixture in
// this package leans on, against the SAME filepath.IsAbs the code under test
// applies. Without it the platform assumption is only implied by the fixtures,
// and a host where it does not hold reports fifteen unrelated "must be absolute"
// failures in tests about ownership and reaping -- which is exactly how the
// POSIX-literal version surfaced on Windows.
func TestAbsTestPath_IsAbsoluteOnEveryHost(t *testing.T) {
	t.Parallel()

	for _, p := range []string{"/r", "/mine-a", "/r/a.go", "/repo/pkg/README.md"} {
		got := absTestPath(p)
		assert.True(t, filepath.IsAbs(got), "absTestPath(%q) = %q must be absolute on %s", p, got, runtime.GOOS)
	}
}

func TestTabPayload_RegisterAndGet(t *testing.T) {
	t.Parallel()

	store, _, _ := newTabPayloadTestStore(t)
	ctx := context.Background()
	require.NoError(t, store.Register(ctx, service.RegisterTabPayloadParams{
		UserID: "user-1", TabID: "t1", Payload: filePayloadIn(absTestPath("/repo/pkg/README.md"), absTestPath("/repo")),
	}))
	loc, err := store.Get(ctx, "user-1", "t1")
	require.NoError(t, err)
	assert.Equal(t, absTestPath("/repo/pkg/README.md"), loc.GetFile().GetFilePath())
	assert.Equal(t, absTestPath("/repo"), loc.GetWorkingDir(),
		"the originating tab's working dir is stored as given, not re-derived from the file's own directory")
}

// A caller with no originating tab to name -- `leapmux control tab open
// --type=file` builds its tab entirely CLI-side -- still gets a usable working
// dir, because every reader of the column treats it as authoritative and a
// blank one would make the file tab unanswerable to `git -C`.
func TestTabPayload_RegisterWithoutWorkingDirFallsBackToFileDir(t *testing.T) {
	t.Parallel()

	store, _, _ := newTabPayloadTestStore(t)
	ctx := context.Background()
	require.NoError(t, store.Register(ctx, service.RegisterTabPayloadParams{
		UserID: "user-1", TabID: "t1", Payload: filePayload(absTestPath("/repo/pkg/README.md")),
	}))
	loc, err := store.Get(ctx, "user-1", "t1")
	require.NoError(t, err)
	assert.Equal(t, absTestPath("/repo/pkg"), loc.GetWorkingDir())
}

// Whitespace is not a working dir. The fallback is keyed on "the caller named
// no tab", and a blank-but-present field is that case however it was spelled --
// otherwise the column holds a string `git -C` rejects, and the close path
// degrades to its tolerant no-prompt branch for a tab that has a perfectly good
// repo one directory up.
func TestTabPayload_RegisterTreatsBlankWorkingDirAsAbsent(t *testing.T) {
	t.Parallel()

	store, _, _ := newTabPayloadTestStore(t)
	ctx := context.Background()
	require.NoError(t, store.Register(ctx, service.RegisterTabPayloadParams{
		UserID: "user-1", TabID: "t1", Payload: filePayloadIn(absTestPath("/repo/pkg/README.md"), "   "),
	}))
	loc, err := store.Get(ctx, "user-1", "t1")
	require.NoError(t, err)
	assert.Equal(t, absTestPath("/repo/pkg"), loc.GetWorkingDir())
}

// A relative working dir is refused for the same reason a relative file_path
// is. `git -C` resolves it against the WORKER PROCESS's cwd -- never the
// client's -- so it does not fail the way a nonexistent absolute dir does; it
// quietly answers for whatever repository the worker happens to sit in. Stored,
// it would let a file tab's close inspection report another repo's branch and
// dirty state, and `--worktree=push` commit and push that repo.
func TestTabPayload_RegisterRefusesRelativeWorkingDir(t *testing.T) {
	t.Parallel()

	store, _, _ := newTabPayloadTestStore(t)
	ctx := context.Background()
	for _, dir := range []string{".", "..", "../peer-repo", "src", "./x"} {
		err := store.Register(ctx, service.RegisterTabPayloadParams{
			UserID: "user-1", TabID: "t1", Payload: filePayloadIn(absTestPath("/repo/pkg/README.md"), dir),
		})
		require.Error(t, err, "working_dir %q must be refused", dir)
		// The WORKING_DIR guard, named: file_path's guard rejects with the same
		// "must be absolute" tail, so a bare substring match would go on passing
		// if the file_path above ever stopped being absolute -- which is exactly
		// what happened on Windows while these fixtures were POSIX literals.
		assert.Contains(t, err.Error(), "working_dir must be absolute")
		_, getErr := store.Get(ctx, "user-1", "t1")
		assert.ErrorIs(t, getErr, service.ErrTabPayloadNotFound,
			"a refused registration must not leave a row behind")
	}
}

// Re-registering the same tab id (the client re-opened the file from a
// different tab's context) must move the working dir with the path. The upsert
// updates both columns or the tab answers branch questions from where it used
// to live.
func TestTabPayload_RegisterOverwritesWorkingDir(t *testing.T) {
	t.Parallel()

	store, _, _ := newTabPayloadTestStore(t)
	ctx := context.Background()
	require.NoError(t, store.Register(ctx, service.RegisterTabPayloadParams{
		UserID: "user-1", TabID: "t1", Payload: filePayloadIn(absTestPath("/repo/a.go"), absTestPath("/repo")),
	}))
	require.NoError(t, store.Register(ctx, service.RegisterTabPayloadParams{
		UserID: "user-1", TabID: "t1", Payload: filePayloadIn(absTestPath("/wt/a.go"), absTestPath("/wt")),
	}))
	loc, err := store.Get(ctx, "user-1", "t1")
	require.NoError(t, err)
	assert.Equal(t, absTestPath("/wt/a.go"), loc.GetFile().GetFilePath())
	assert.Equal(t, absTestPath("/wt"), loc.GetWorkingDir())
}

func TestTabPayload_GetMissingReturnsNotFound(t *testing.T) {
	t.Parallel()

	store, _, _ := newTabPayloadTestStore(t)
	_, err := store.Get(context.Background(), "user-1", "ghost")
	assert.ErrorIs(t, err, service.ErrTabPayloadNotFound)
}

func TestTabPayload_RevokeRemovesAndEmits(t *testing.T) {
	t.Parallel()

	store, bus, _ := newTabPayloadTestStore(t)
	ctx := context.Background()
	require.NoError(t, store.Register(ctx, service.RegisterTabPayloadParams{
		UserID: "user-1", TabID: "t1", Payload: filePayload(absTestPath("/repo/a.go")),
	}))

	// Subscribe as the owner; expect a Revoked event after revoke.
	got := make(chan *leapmuxv1.WorkerPrivateEvent, 4)
	subCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	go func() {
		_ = bus.SnapshotAndSubscribe(subCtx, userid.MustNew("user-1"), nil, func(evt *leapmuxv1.WorkerPrivateEvent) error {
			got <- evt
			return nil
		})
	}()
	time.Sleep(50 * time.Millisecond)

	require.NoError(t, store.RevokeRow(ctx, "user-1", "t1"))

	select {
	case evt := <-got:
		require.NotNil(t, evt.GetTabPayloadRevoked(), "expected TabPayloadRevoked")
		assert.Equal(t, "t1", evt.GetTabPayloadRevoked().GetTabId())
	case <-time.After(time.Second):
		t.Fatal("revoke did not produce a private event")
	}

	// Row gone.
	_, err := store.Get(ctx, "user-1", "t1")
	assert.ErrorIs(t, err, service.ErrTabPayloadNotFound)
}

// The bootstrap snapshot replays every row the CALLER owns, and only those.
//
// The owner predicate is the whole predicate now that there is no workspace to
// narrow by, so it is also the only thing standing between one user's file
// paths and another's. Production cannot reach the two-owner state below on a
// single-user worker; seeding it deliberately is the only way to show the SQL
// binds user_id, so a future edit that drops the predicate (returning to the
// whole-table walk this replaced) fails here rather than leaking silently.
func TestTabPayload_SnapshotForOwnerBindsTheOwner(t *testing.T) {
	t.Parallel()

	store, _, _ := newTabPayloadTestStore(t)
	ctx := context.Background()
	require.NoError(t, store.Register(ctx, service.RegisterTabPayloadParams{
		UserID: "user-1", TabID: "t1", Payload: filePayload(absTestPath("/mine-a"))}))
	require.NoError(t, store.Register(ctx, service.RegisterTabPayloadParams{
		UserID: "user-1", TabID: "t2", Payload: filePayload(absTestPath("/mine-b"))}))
	require.NoError(t, store.Register(ctx, service.RegisterTabPayloadParams{
		UserID: "user-2", TabID: "t3", Payload: filePayload(absTestPath("/theirs"))}))

	mine, err := store.SnapshotForOwner(ctx, userid.MustNew("user-1"))
	require.NoError(t, err)
	require.Len(t, mine, 2, "every row the caller owns is replayed, whatever workspace its tab is in")
	paths := []string{
		mine[0].GetTabPayloadRegistered().GetPayload().GetFile().GetFilePath(),
		mine[1].GetTabPayloadRegistered().GetPayload().GetFile().GetFilePath(),
	}
	assert.ElementsMatch(t, []string{absTestPath("/mine-a"), absTestPath("/mine-b")}, paths)

	theirs, err := store.SnapshotForOwner(ctx, userid.MustNew("user-2"))
	require.NoError(t, err)
	require.Len(t, theirs, 1)
	assert.Equal(t, absTestPath("/theirs"), theirs[0].GetTabPayloadRegistered().GetPayload().GetFile().GetFilePath())

	// An unminted owner reaches no row, rather than falling back to every row.
	none, err := store.SnapshotForOwner(ctx, userid.UserID{})
	require.NoError(t, err)
	assert.Empty(t, none, "a zero owner selects nothing")
}

func TestTabPayload_SnapshotAndSubscribe_RaceFreeBootstrap(t *testing.T) {
	t.Parallel()

	store, bus, _ := newTabPayloadTestStore(t)
	ctx := context.Background()
	require.NoError(t, store.Register(ctx, service.RegisterTabPayloadParams{
		UserID: "user-1", TabID: "tBootstrap", Payload: filePayload(absTestPath("/repo/seed")),
	}))

	// SnapshotAndSubscribe must replay the existing row before any
	// subsequent live event. The atomicity guarantee is critical: an
	// external Register that lands during the subscribe call must not be
	// missed.
	subCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	got := make(chan *leapmuxv1.WorkerPrivateEvent, 8)
	go func() {
		_ = bus.SnapshotAndSubscribe(subCtx, userid.MustNew("user-1"),
			func(owner userid.UserID) []*leapmuxv1.WorkerPrivateEvent {
				snap, err := store.SnapshotForOwner(subCtx, owner)
				if err != nil {
					return nil
				}
				return snap
			},
			func(evt *leapmuxv1.WorkerPrivateEvent) error {
				got <- evt
				return nil
			})
	}()
	time.Sleep(50 * time.Millisecond)

	// A live Register after subscribe should arrive after the bootstrap
	// snapshot.
	require.NoError(t, store.Register(ctx, service.RegisterTabPayloadParams{
		UserID: "user-1", TabID: "tLive", Payload: filePayload(absTestPath("/repo/live")),
	}))

	collected := []*leapmuxv1.WorkerPrivateEvent{}
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
	assert.Equal(t, "tBootstrap", collected[0].GetTabPayloadRegistered().GetTabId())
	assert.Equal(t, "tLive", collected[1].GetTabPayloadRegistered().GetTabId())
}

func TestTabPayload_RegisterRequiresAllFields(t *testing.T) {
	t.Parallel()

	store, _, _ := newTabPayloadTestStore(t)
	cases := []service.RegisterTabPayloadParams{
		{UserID: "", TabID: "t1", Payload: filePayload(absTestPath("/p"))},
		{UserID: "user-1", TabID: "", Payload: filePayload(absTestPath("/p"))},
		{UserID: "user-1", TabID: "t1", Payload: filePayload("")},
	}
	for _, c := range cases {
		err := store.Register(context.Background(), c)
		require.Error(t, err, "expected error on incomplete params: %+v", c)
	}
}

// TestTabPayloadStore_RefusesBlankOwner pins the blank-owner floor on
// worker_tab_payloads from BOTH ends.
//
// The table is keyed by (user_id, tab_id), so a blank user_id is not "no
// filter" -- it is half the row's identity gone missing. The floor is now two
// layers: the schema refuses to STORE one (CHECK (user_id <> ”)), and the
// tab-keyed entry points refuse to READ or DELETE by one.
//
// This test used to seed a blank-owner row directly through sqlc, because the
// schema permitted it and only the Go guards stood in the way. It cannot any
// more -- that seed is now a constraint violation, which is the stronger
// property and is asserted first. The Go guards stay pinned against a real
// owner's row, since they defend the caller-side mistake (a lost tenant) that
// no constraint can catch.
func TestTabPayloadStore_RefusesBlankOwner(t *testing.T) {
	t.Parallel()

	store, _, q := newTabPayloadTestStore(t)
	ctx := context.Background()

	// The schema floor. This is the assertion that makes the blank-owner row
	// unrepresentable rather than merely unreachable through one API: the
	// worker's database has no users table, so the hub's REFERENCES users(id)
	// cannot reach here and a CHECK is the only available floor.
	require.Error(t, q.UpsertWorkerTabPayload(ctx, db.UpsertWorkerTabPayloadParams{
		UserID:     "",
		TabID:      "shared-tab",
		TabType:    int64(leapmuxv1.TabType_TAB_TYPE_FILE),
		Payload:    mustMarshalFilePayload(absTestPath("/blank/a.go"), ""),
		WorkingDir: "",
	}), "the schema must refuse a blank owner even when sqlc is driven directly")

	// A real owner's row, which every control below resolves against.
	require.NoError(t, store.Register(ctx, service.RegisterTabPayloadParams{
		UserID: "user-1", TabID: "shared-tab", Payload: filePayload(absTestPath("/real/a.go")),
	}))

	// The Go guards. Each pairs a blank-owner refusal with a real-owner
	// control, so a passing assertion means "refused" and not "the fixture was
	// never reachable".
	t.Run("get", func(t *testing.T) {
		_, err := store.Get(ctx, "", "shared-tab")
		require.Error(t, err)
		assert.NotErrorIs(t, err, service.ErrTabPayloadNotFound,
			"a blank owner is a caller bug, not a missing row")

		loc, err := store.Get(ctx, "user-1", "shared-tab")
		require.NoError(t, err, "control: a real owner still resolves")
		assert.Equal(t, absTestPath("/real/a.go"), loc.GetFile().GetFilePath())
	})

	t.Run("revoke", func(t *testing.T) {
		require.Error(t, store.RevokeRow(ctx, "", "shared-tab"))
		_, err := q.GetWorkerTabPayload(ctx, db.GetWorkerTabPayloadParams{UserID: "user-1", TabID: "shared-tab"})
		require.NoError(t, err, "a blank-owner revoke must delete nothing")

		require.NoError(t, store.RevokeRow(ctx, "user-1", "shared-tab"),
			"control: a real owner still revokes")
	})

	// A blank tab id is refused on the same footing.
	_, err := store.Get(ctx, "user-1", "")
	assert.Error(t, err)
}

// TestTabPayload_ImageArm covers the IMAGE half of the store: what it accepts,
// what it refuses, and that a round trip preserves the reference.
//
// The refusals matter more here than for a file path. A FILE tab with a bad
// path shows an error and the user retries; an IMAGE tab whose reference cannot
// address a message resolves to nothing forever, with no way for the user to
// tell why, so the door is the right place to stop it.
func TestTabPayload_ImageArm(t *testing.T) {
	t.Parallel()

	store, _, _ := newTabPayloadTestStore(t)
	ctx := context.Background()

	require.NoError(t, store.Register(ctx, service.RegisterTabPayloadParams{
		UserID: "user-1", TabID: "img-1", Payload: imagePayload("agent-1", 42, 1, absTestPath("/repo")),
	}))

	got, err := store.Get(ctx, "user-1", "img-1")
	require.NoError(t, err)
	assert.Equal(t, "agent-1", got.GetImage().GetAgentId())
	assert.Equal(t, int64(42), got.GetImage().GetSeq())
	assert.Equal(t, int32(1), got.GetImage().GetImageIndex())
	assert.Equal(t, absTestPath("/repo"), got.GetWorkingDir())
	// The pixels are the one thing that must never be here.
	assert.Nil(t, got.GetFile())

	// The kind is stored beside the blob so a reader that only needs it -- the
	// close path picking which worktree link to drop -- pays nothing for the
	// payload.
	tabType, err := store.TabTypeOf(ctx, "user-1", "img-1")
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.TabType_TAB_TYPE_IMAGE, tabType)

	for _, c := range []struct {
		name    string
		payload *leapmuxv1.TabPayload
	}{
		{"no agent id", imagePayload("", 42, 0, "")},
		{"zero seq", imagePayload("agent-1", 0, 0, "")},
		{"negative seq", imagePayload("agent-1", -1, 0, "")},
		{"negative index", imagePayload("agent-1", 42, -1, "")},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := store.Register(ctx, service.RegisterTabPayloadParams{
				UserID: "user-1", TabID: "img-bad", Payload: c.payload,
			})
			require.Error(t, err)
		})
	}
}

// TestTabPayload_RefusesAPayloadWithNoKind pins the default branch: a payload
// whose oneof is unset specifies no tab, so there is nothing to store it as.
func TestTabPayload_RefusesAPayloadWithNoKind(t *testing.T) {
	t.Parallel()

	store, _, _ := newTabPayloadTestStore(t)
	err := store.Register(context.Background(), service.RegisterTabPayloadParams{
		UserID: "user-1", TabID: "t1", Payload: &leapmuxv1.TabPayload{WorkingDir: absTestPath("/repo")},
	})
	require.Error(t, err)
}

// TestTabPayload_ImageWorkingDirIsNotDerived pins the one place the two kinds
// genuinely differ: a FILE tab with no stated working dir falls back to the
// file's own directory, and an IMAGE tab has no file to fall back to. An
// one with no stated dir stays blank and simply joins no branch group -- inventing a
// directory for it would put the tab in a repo the user never opened it from.
func TestTabPayload_ImageWorkingDirIsNotDerived(t *testing.T) {
	t.Parallel()

	store, _, _ := newTabPayloadTestStore(t)
	ctx := context.Background()
	require.NoError(t, store.Register(ctx, service.RegisterTabPayloadParams{
		UserID: "user-1", TabID: "img-1", Payload: imagePayload("agent-1", 7, 0, ""),
	}))

	got, err := store.Get(ctx, "user-1", "img-1")
	require.NoError(t, err)
	assert.Empty(t, got.GetWorkingDir())
}

// mustMarshalImagePayload is the blob a raw UpsertWorkerTabPayload writes for an
// IMAGE row, for a test that goes around the store's own validation.
func mustMarshalImagePayload(agentID string, seq int64, imageIndex int32) []byte {
	blob, err := proto.Marshal(imagePayload(agentID, seq, imageIndex, ""))
	if err != nil {
		panic(err)
	}
	return blob
}

func imagePayload(agentID string, seq int64, imageIndex int32, workingDir string) *leapmuxv1.TabPayload {
	return &leapmuxv1.TabPayload{
		WorkingDir: workingDir,
		Kind: &leapmuxv1.TabPayload_Image{Image: &leapmuxv1.ImageTabPayload{
			AgentId:    agentID,
			Seq:        seq,
			ImageIndex: imageIndex,
		}},
	}
}

// The store's refusals fall into two classes, and the handler reports them with
// two different RPC codes. A caller fault is INVALID_ARGUMENT and there is
// something to correct; anything else is the worker's own and a retry is the
// right response. Collapsing both onto INVALID_ARGUMENT told a script branching
// on the code to stop retrying a locked database.
func TestTabPayload_CallerFaultsCarryTheInvalidSentinel(t *testing.T) {
	t.Parallel()

	store, _, _ := newTabPayloadTestStore(t)
	ctx := context.Background()

	cases := []struct {
		name   string
		params service.RegisterTabPayloadParams
	}{
		{
			name:   "a missing required field",
			params: service.RegisterTabPayloadParams{UserID: "user-1", TabID: "", Payload: filePayload(absTestPath("/repo/a.txt"))},
		},
		{
			name:   "a payload that specifies no kind",
			params: service.RegisterTabPayloadParams{UserID: "user-1", TabID: "t1", Payload: &leapmuxv1.TabPayload{}},
		},
		{
			name:   "a relative file path",
			params: service.RegisterTabPayloadParams{UserID: "user-1", TabID: "t1", Payload: filePayload("a.txt")},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := store.Register(ctx, tc.params)
			require.Error(t, err)
			assert.ErrorIs(t, err, service.ErrInvalidTabPayload,
				"the handler branches on this sentinel to pick invalid-argument over internal")
		})
	}
}

// TabTypeOf must distinguish "no such row" from "the read failed". The revoke
// handler falls back to FILE only for the first: an absent row's worktree link
// is already dead either way, while guessing FILE for an IMAGE tab whose read
// merely failed makes the worktree lookup miss, so a requested REMOVE degrades
// to KEEP with nothing reported and the link outlives its payload row.
func TestTabPayload_TabTypeOfReportsNotFoundDistinctly(t *testing.T) {
	t.Parallel()

	store, _, _ := newTabPayloadTestStore(t)
	ctx := context.Background()

	_, err := store.TabTypeOf(ctx, "user-1", "never-registered")
	assert.ErrorIs(t, err, service.ErrTabPayloadNotFound)

	require.NoError(t, store.Register(ctx, service.RegisterTabPayloadParams{
		UserID: "user-1", TabID: "img-1", Payload: imagePayload("agent-1", 7, 0, absTestPath("/repo")),
	}))
	got, err := store.TabTypeOf(ctx, "user-1", "img-1")
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.TabType_TAB_TYPE_IMAGE, got,
		"an IMAGE row must report its own kind, or its worktree link is dropped as a FILE and survives")
}

// The schema's own floor under the four unchecked int64 -> TabType casts.
// Two readers -- the orphan reconciler's key builder and the bootstrap tab
// filter -- have no default case, so a row outside {FILE, IMAGE} would be keyed
// against nothing in the hub's tab list and reaped as an orphan. The Go writer
// cannot produce one today; the CHECK is what keeps that true for a hand-edited
// database or a future kind added without its schema update.
func TestTabPayload_SchemaRefusesATabTypeOutsideThePayloadKinds(t *testing.T) {
	t.Parallel()

	_, _, q := newTabPayloadTestStore(t)
	err := q.UpsertWorkerTabPayload(context.Background(), db.UpsertWorkerTabPayloadParams{
		UserID:     "user-1",
		TabID:      "bogus",
		TabType:    int64(leapmuxv1.TabType_TAB_TYPE_TERMINAL),
		Payload:    mustMarshalImagePayload("agent-1", 7, 0),
		WorkingDir: "",
	})
	require.Error(t, err, "a TERMINAL tab_type has no payload row to be, and the CHECK must say so")
	assert.Contains(t, err.Error(), "CHECK")
}
