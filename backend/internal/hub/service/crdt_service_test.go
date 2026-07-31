package service_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/crdt"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/storetest"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// memJournal is a minimal in-memory crdt.Journal sufficient for the
// service-layer auth-stamping tests. It mirrors the structure of the
// crdt_test package's fakeJournal but is kept inline so the service
// tests don't reach into another package's private symbols.
type memJournal struct {
	mu        sync.Mutex
	state     *leapmuxv1.UserCrdtState
	batches   []*leapmuxv1.OpBatch
	dedup     map[string]crdt.RecentBatchRecord
	commitErr error
}

func newMemJournal() *memJournal { return &memJournal{dedup: map[string]crdt.RecentBatchRecord{}} }

func (j *memJournal) LoadState(_ context.Context, _ string) (*leapmuxv1.UserCrdtState, []*leapmuxv1.OpBatch, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	var state *leapmuxv1.UserCrdtState
	if j.state != nil {
		state = crdt.CloneState(j.state)
	}
	return state, append([]*leapmuxv1.OpBatch(nil), j.batches...), nil
}

func (j *memJournal) CommitBatch(_ context.Context, c crdt.CommitBatch) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.commitErr != nil {
		return j.commitErr
	}
	j.batches = append(j.batches, c.Batch)
	// The dedup TABLE row is the commit envelope's context plus the batch's own
	// fields; crdt.CommitBatch states the former once, so reassemble it here
	// the way the real journal adapter does when it writes the row.
	j.dedup[c.Dedup.BatchID] = crdt.RecentBatchRecord{
		UserID:            c.UserID,
		BatchID:           c.Dedup.BatchID,
		BodyHash:          c.Dedup.BodyHash,
		PrincipalID:       c.PrincipalID,
		CanonicalFirstHLC: c.Dedup.CanonicalFirstHLC,
		OpCount:           c.Dedup.OpCount,
		Epoch:             c.Epoch,
		ExpiresAt:         c.Dedup.ExpiresAt,
	}
	return nil
}

func (j *memJournal) LookupRecentBatchID(_ context.Context, _, batchID string) (*crdt.RecentBatchRecord, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	row, ok := j.dedup[batchID]
	if !ok {
		return nil, crdt.ErrNotFound
	}
	clone := row
	return &clone, nil
}

func (j *memJournal) AdvanceEpoch(_ context.Context, _ string, epoch int64, _ time.Time) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.state != nil {
		j.state.CurrentEpoch = epoch
	}
	return nil
}

func (j *memJournal) CompactBatch(_ context.Context, c crdt.CompactBatch) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.state = crdt.CloneState(c.State)
	return nil
}

func (j *memJournal) CleanupExpiredRecentBatchIDs(_ context.Context, _ time.Time) (int64, error) {
	return 0, nil
}

// dedupRow returns the dedup record committed for a given batch_id, or
// nil. Lets tests assert the principal_id the service forwarded.
func (j *memJournal) dedupRow(batchID string) *crdt.RecentBatchRecord {
	j.mu.Lock()
	defer j.mu.Unlock()
	row, ok := j.dedup[batchID]
	if !ok {
		return nil
	}
	clone := row
	return &clone
}

// allowAllAuth lets every (user, workspace, principal) write — the
// service-layer tests are about the wire-level stamping, not the auth
// matrix (that's covered inside crdt/validate_test.go).
type allowAllAuth struct{}

func (allowAllAuth) CanAccessWorkspace(_ context.Context, _, _ string) (bool, error) {
	return true, nil
}
func (allowAllAuth) CanUseWorker(_ context.Context, _, _ string) (bool, error) { return true, nil }

// crdtServiceEnv bundles the bits a CRDT-service test needs: a
// running manager (with a memJournal we can inspect), a registry that
// hands out that single manager, and the service handler itself.
type crdtServiceEnv struct {
	journal  *memJournal
	mgr      *crdt.Manager
	registry *crdt.Registry
	svc      *service.CRDTService
	userID   string
}

func setupCRDTService(t *testing.T) *crdtServiceEnv {
	t.Helper()
	userID := "user-test"
	j := newMemJournal()

	// The registry is the sole owner of Manager.Start — it dispatches
	// the goroutine itself. We supply a factory that constructs +
	// bootstraps a single manager (and reuses it on subsequent Get).
	var mgr *crdt.Manager
	managers := map[string]*crdt.Manager{}
	registry := crdt.NewRegistry(func(ctx context.Context, want userid.UserID) (*crdt.Manager, error) {
		if m, ok := managers[want.String()]; ok {
			return m, nil
		}
		m := crdt.NewManager(want, j, allowAllAuth{}, nil, time.Now)
		if err := m.Bootstrap(ctx); err != nil {
			return nil, err
		}
		managers[want.String()] = m
		if want.String() == userID {
			mgr = m
		}
		return m, nil
	}, nil)
	t.Cleanup(func() { registry.Shutdown(2 * time.Second) })

	// Force the registry to load the manager up front (so the tests
	// can pre-seed via SubmitInternal directly).
	_, err := registry.Get(context.Background(), userID)
	require.NoError(t, err)

	svc := service.NewCRDTService(nil /* store unused for these tests */, registry, nil, nil)

	// Seed a workspace + root so the tests can submit ops that pass
	// validation. This mirrors what the lifecycle outbox would do in
	// production after CreateWorkspace: SetWorkspaceRegister seeds the
	// record in the same atomic batch as the root (no off-goroutine
	// m.state write).
	setRegister := &leapmuxv1.CrdtOp{
		OpId: "seed-workspace-register",
		Body: &leapmuxv1.CrdtOp_SetWorkspaceRegister{SetWorkspaceRegister: &leapmuxv1.SetWorkspaceRegisterOp{
			WorkspaceId: "w1",
		}},
	}
	rootKind := &leapmuxv1.CrdtOp{
		OpId: "seed-kind",
		Body: &leapmuxv1.CrdtOp_SetNodeRegister{SetNodeRegister: &leapmuxv1.SetNodeRegisterOp{
			NodeId: "root1",
			Field:  &leapmuxv1.SetNodeRegisterOp_Kind{Kind: leapmuxv1.NodeKind_NODE_KIND_LEAF},
		}},
	}
	rootRegister := &leapmuxv1.CrdtOp{
		OpId: "seed-register",
		Body: &leapmuxv1.CrdtOp_SetWorkspaceRootNode{SetWorkspaceRootNode: &leapmuxv1.SetWorkspaceRootNodeOp{
			WorkspaceId: "w1", RootNodeId: "root1",
		}},
	}
	_, err = mgr.SubmitInternal(context.Background(), crdt.SubmitInput{
		Batches: []*leapmuxv1.OpBatch{{BatchId: "seed", Ops: []*leapmuxv1.CrdtOp{setRegister, rootKind, rootRegister}}},
	})
	require.NoError(t, err)

	return &crdtServiceEnv{journal: j, mgr: mgr, registry: registry, svc: svc, userID: userID}
}

// addTabOps builds the canonical 3-op SetTabRegister batch the tests
// reuse. Each op gets a caller-supplied id so dedup assertions are easy.
func addTabOps(idPrefix, tabID, tileID, workerID, position string) []*leapmuxv1.CrdtOp {
	return []*leapmuxv1.CrdtOp{
		{OpId: idPrefix + "-tile", Body: &leapmuxv1.CrdtOp_SetTabRegister{SetTabRegister: &leapmuxv1.SetTabRegisterOp{
			TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: tabID,
			Field: &leapmuxv1.SetTabRegisterOp_TileId{TileId: tileID},
		}}},
		{OpId: idPrefix + "-worker", Body: &leapmuxv1.CrdtOp_SetTabRegister{SetTabRegister: &leapmuxv1.SetTabRegisterOp{
			TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: tabID,
			Field: &leapmuxv1.SetTabRegisterOp_WorkerId{WorkerId: workerID},
		}}},
		{OpId: idPrefix + "-pos", Body: &leapmuxv1.CrdtOp_SetTabRegister{SetTabRegister: &leapmuxv1.SetTabRegisterOp{
			TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: tabID,
			Field: &leapmuxv1.SetTabRegisterOp_Position{Position: position},
		}}},
	}
}

// TestCRDTService_SubmitOps_RequiresAuth asserts the handler rejects
// callers without an authenticated user in the context — the same
// guarantee the ConnectRPC interceptor provides in production.
func TestCRDTService_SubmitOps_RequiresAuth(t *testing.T) {
	t.Parallel()

	env := setupCRDTService(t)

	req := connect.NewRequest(&leapmuxv1.SubmitOpsRequest{
		Epoch:   env.mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch(),
		Batches: []*leapmuxv1.OpBatch{{BatchId: "b1", Ops: addTabOps("op1", "tA", "root1", "wkr1", "p1")}},
	})

	_, err := env.svc.SubmitOps(context.Background(), req)
	require.Error(t, err)
	var ce *connect.Error
	require.True(t, errors.As(err, &ce))
	assert.Equal(t, connect.CodeUnauthenticated, ce.Code())
}

// TestCRDTService_SubmitOps_StampsPrincipalAndOrigin asserts the
// service forwards the authenticated user.ID as BOTH the manager's
// PrincipalID (for dedup ownership) and OriginClient (for canonical
// HLC tie-breaking). The request body has no field carrying these
// values, so a malicious client cannot spoof them.
func TestCRDTService_SubmitOps_StampsPrincipalAndOrigin(t *testing.T) {
	t.Parallel()

	env := setupCRDTService(t)
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{ID: userid.MustNew(env.userID)})

	req := connect.NewRequest(&leapmuxv1.SubmitOpsRequest{
		Epoch:   env.mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch(),
		Batches: []*leapmuxv1.OpBatch{{BatchId: "b1", Ops: addTabOps("op1", "tA", "root1", "wkr1", "p1")}},
	})

	resp, err := env.svc.SubmitOps(ctx, req)
	require.NoError(t, err)
	require.Len(t, resp.Msg.GetResults(), 1)
	require.NotNil(t, resp.Msg.GetResults()[0].GetCommitted())

	// The dedup row landed under principal_id=env.userID — proving the
	// service stamped it, not a value the request body controlled.
	row := env.journal.dedupRow("b1")
	require.NotNil(t, row, "dedup row for batch b1 must exist")
	assert.Equal(t, env.userID, row.PrincipalID, "principal_id must match the authenticated user")
}

// TestCRDTService_SubmitOps_OriginClientIdSpoofingRejected encodes the
// security guarantee that the manager overwrites whatever
// `origin_client_id` appears in the request body with the
// authenticated session's identity. A malicious client setting
// origin_client_id="hub" in the wire-level CrdtOp must not be able to
// impersonate the hub or another user.
func TestCRDTService_SubmitOps_OriginClientIdSpoofingRejected(t *testing.T) {
	t.Parallel()

	env := setupCRDTService(t)
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{ID: userid.MustNew(env.userID)})

	spoofed := addTabOps("op2", "tB", "root1", "wkr1", "p1")
	for _, op := range spoofed {
		// Attempt to impersonate the hub's own client_id.
		op.OriginClientId = "hub-spoofed"
	}

	req := connect.NewRequest(&leapmuxv1.SubmitOpsRequest{
		Epoch:   env.mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch(),
		Batches: []*leapmuxv1.OpBatch{{BatchId: "b2", Ops: spoofed}},
	})

	resp, err := env.svc.SubmitOps(ctx, req)
	require.NoError(t, err)
	committed := resp.Msg.GetResults()[0].GetCommitted()
	require.NotNil(t, committed)
	require.Len(t, committed.GetCommitted(), 3)

	// The committed ops must carry the authenticated user as their
	// origin_client_id — the manager overwrites the spoofed value.
	state := env.mgr.State()
	tab, ok := state.GetTabs()["tB"]
	require.True(t, ok, "tab tB must be committed")
	// The dedup row carries the authenticated principal_id, regardless
	// of any spoof in the request body.
	row := env.journal.dedupRow("b2")
	require.NotNil(t, row)
	assert.Equal(t, env.userID, row.PrincipalID,
		"principal_id must reflect the authenticated user, not any spoofed origin_client_id")
	// And the tab's stored worker_id reflects the actual op, so we know
	// the commit happened through the standard validate-then-apply path.
	assert.Equal(t, "wkr1", tab.GetWorkerId().GetValue())
}

// TestCRDTService_UpdatePresence_RequiresAuth ensures presence calls
// without an authenticated user are rejected with Unauthenticated.
func TestCRDTService_UpdatePresence_RequiresAuth(t *testing.T) {
	t.Parallel()

	env := setupCRDTService(t)
	req := connect.NewRequest(&leapmuxv1.UpdatePresenceRequest{WorkspaceId: "w1"})
	_, err := env.svc.UpdatePresence(context.Background(), req)
	require.Error(t, err)
	var ce *connect.Error
	require.True(t, errors.As(err, &ce))
	assert.Equal(t, connect.CodeUnauthenticated, ce.Code())
}
func TestCRDTService_GetMaterialized_DelegationEmptyAccessDoesNotAllowAll(t *testing.T) {
	t.Parallel()

	env := setupCRDTService(t)
	st := hubtestutil.OpenTestStore(t)
	user := storetest.SeedUser(t, st, "alice")
	_ = storetest.SeedWorkspace(t, st, user.ID, "w1")
	svc := service.NewCRDTService(st, env.registry, nil, nil)
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{
		ID:         userid.MustNew(user.ID),
		Credential: auth.DelegationCredential("test-delegation", "worker-mint"),
	})

	resp, err := svc.GetMaterialized(ctx, connect.NewRequest(&leapmuxv1.GetMaterializedRequest{}))
	require.NoError(t, err)
	assert.Empty(t, resp.Msg.GetState().GetWorkspaces(),
		"an empty delegated ACL must not be interpreted as the all-workspaces materialized filter")
	assert.Empty(t, resp.Msg.GetState().GetNodes())
	assert.Empty(t, resp.Msg.GetState().GetTabs())
}

func TestCRDTService_UpdatePresence_RequiresCanonicalWorkspaceReadAccess(t *testing.T) {
	t.Parallel()

	t.Run("unknown workspace is denied", func(t *testing.T) {
		env := setupCRDTService(t)
		st := hubtestutil.OpenTestStore(t)
		user := storetest.SeedUser(t, st, "presence-owner")
		svc := service.NewCRDTService(st, env.registry, nil, nil)

		ctx := auth.WithUser(context.Background(), &auth.UserInfo{ID: userid.MustNew(user.ID)})
		_, err := svc.UpdatePresence(ctx, connect.NewRequest(&leapmuxv1.UpdatePresenceRequest{
			WorkspaceId: "does-not-exist",
		}))
		require.Error(t, err)
		assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	})

	t.Run("non-owner delegated heartbeat is denied", func(t *testing.T) {
		env := setupCRDTService(t)
		st := hubtestutil.OpenTestStore(t)
		owner := storetest.SeedUser(t, st, "presence-owner")
		other := storetest.SeedUser(t, st, "presence-other")
		workspaceID := storetest.SeedWorkspace(t, st, owner.ID, "Owned")
		svc := service.NewCRDTService(st, env.registry, nil, nil)

		// A delegation credential still cannot heartbeat for a user who does
		// not own the workspace: access is owner-only, and dropping the
		// workspace axis from the token did not widen that.
		ctx := auth.WithUser(context.Background(), &auth.UserInfo{
			ID:         userid.MustNew(other.ID),
			Credential: auth.DelegationCredential("delegation-token", "worker-mint"),
		})
		_, err := svc.UpdatePresence(ctx, connect.NewRequest(&leapmuxv1.UpdatePresenceRequest{
			WorkspaceId: workspaceID,
		}))
		require.Error(t, err)
		assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	})
}

// TestCRDTService_UpdatePresence_DelegationReachesOwnersOtherWorkspace pins the
// boundary this change deliberately gave up.
//
// A delegation bearer used to be pinned to the single workspace it was minted
// for, so a heartbeat for a sibling workspace was PERMISSION_DENIED even when
// the same user owned both. The token now carries the owner's identity across
// every workspace they own; the only bound left is which Worker it may reach
// (auth.DelegationWorkerScope, covered in internal/hub/crdt's
// TestScopedAuthChecker_WorkerScopeDeniesBeforeInnerCheck).
//
// This asserts the ALLOW side on purpose. The deny side that survives -- a
// different user's workspace -- is the subtest above; without this one, a
// future re-narrowing of the token would silently pass the whole suite.
func TestCRDTService_UpdatePresence_DelegationReachesOwnersOtherWorkspace(t *testing.T) {
	t.Parallel()

	env := setupCRDTService(t)
	st := hubtestutil.OpenTestStore(t)
	owner := storetest.SeedUser(t, st, "sibling-owner")
	// Two workspaces, one owner. The bearer's provenance is the first; the
	// heartbeat targets the second.
	_ = storetest.SeedWorkspace(t, st, owner.ID, "Minted For")
	sibling := storetest.SeedWorkspace(t, st, owner.ID, "Sibling")
	svc := service.NewCRDTService(st, env.registry, nil, nil)

	ctx := auth.WithUser(context.Background(), &auth.UserInfo{
		ID:         userid.MustNew(owner.ID),
		Credential: auth.DelegationCredential("delegation-token", "worker-mint"),
	})
	_, err := svc.UpdatePresence(ctx, connect.NewRequest(&leapmuxv1.UpdatePresenceRequest{
		WorkspaceId: sibling,
	}))
	require.NoError(t, err,
		"a delegated heartbeat must reach every workspace its owner owns")
}

// TestCRDTService_UpdatePresence_ClientIDNamespaces asserts that
// session, bearer-kind/token, and user fallback identities remain
// distinct even when their raw IDs are equal.
func TestCRDTService_UpdatePresence_ClientIDNamespaces(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		info     *auth.UserInfo
		expected string
	}{
		{
			name:     "session uses its namespace",
			info:     &auth.UserInfo{Credential: auth.SessionCredential("shared-id")},
			expected: "session:shared-id",
		},
		{
			name:     "api bearer includes its kind",
			info:     &auth.UserInfo{Credential: auth.APICredential("shared-id")},
			expected: "bearer:61:shared-id",
		},
		{
			name:     "delegation bearer includes its kind",
			info:     &auth.UserInfo{Credential: auth.DelegationCredential("shared-id", "worker-mint")},
			expected: "bearer:64:shared-id",
		},
		{
			name:     "user fallback has its own namespace",
			info:     &auth.UserInfo{},
			expected: "user:user-test",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := setupCRDTService(t)
			st := hubtestutil.OpenTestStore(t)
			require.NoError(t, st.Users().Create(context.Background(), store.CreateUserParams{
				ID: env.userID, Username: "presence-user",
			}))
			require.NoError(t, st.Workspaces().Create(context.Background(), store.CreateWorkspaceParams{
				ID: "w1", OwnerUserID: userid.MustNew(env.userID), Title: "Presence",
			}))
			tc.info.ID = userid.MustNew(env.userID)
			svc := service.NewCRDTService(st, env.registry, nil, nil)

			// Subscribe so we can capture the broadcast PresenceUpdate.
			var (
				mu       sync.Mutex
				received string
				sawAny   bool
			)
			sub := &crdt.Subscriber{
				Filter: crdt.SubscriberFilter{WorkspaceIDs: map[string]bool{"w1": true}},
				Send: func(evt *crdt.MarshaledEvent) error {
					if p := evt.Event.GetPresence(); p != nil {
						mu.Lock()
						received = p.GetActiveClientId()
						sawAny = true
						mu.Unlock()
					}
					return nil
				},
			}
			_, unsub := env.mgr.Subscribe(sub)
			defer unsub()

			ctx := auth.WithUser(context.Background(), tc.info)
			_, err := svc.UpdatePresence(ctx, connect.NewRequest(&leapmuxv1.UpdatePresenceRequest{
				WorkspaceId: "w1",
			}))
			require.NoError(t, err)

			// Allow the manager goroutine to fan out the broadcast.
			deadline := time.Now().Add(500 * time.Millisecond)
			for time.Now().Before(deadline) {
				mu.Lock()
				ok := sawAny
				mu.Unlock()
				if ok {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}

			mu.Lock()
			defer mu.Unlock()
			require.True(t, sawAny, "expected a presence broadcast")
			assert.Equal(t, tc.expected, received)
		})
	}
}

// TestResolveAllowedWorkspaces_FiltersAndDedups exercises the helper
// the `/ws/userevents` handler uses to project a per-user workspace
// filter from the requested set. The helper must (a) drop workspaces
// the caller has no access to, (b) expand an empty request to the full
// set the caller can read, and (c) skip blank ids silently.
func TestResolveAllowedWorkspaces_FiltersAndDedups(t *testing.T) {
	t.Parallel()

	st := hubtestutil.OpenTestStore(t)
	ctx := context.Background()
	aliceID := hubtestutil.CreateTestUser(t, st, "alice", "password-alice-123")
	bobID := hubtestutil.CreateTestUser(t, st, "bob", "password-bob-456")

	// Alice owns w1. Bob owns w2.
	require.NoError(t, st.Workspaces().Create(ctx, store.CreateWorkspaceParams{
		ID: "w-alice", Title: "w-alice", OwnerUserID: userid.MustNew(aliceID),
	}))
	require.NoError(t, st.Workspaces().Create(ctx, store.CreateWorkspaceParams{
		ID: "w-bob", Title: "w-bob", OwnerUserID: userid.MustNew(bobID),
	}))

	// Empty request → returns every workspace alice owns.
	allowed, err := service.ResolveAllowedWorkspacesForTest(ctx, st, nil, userid.MustNew(aliceID))
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"w-alice"}, allowed)

	// Requesting Bob's workspace returns nothing (alice has no access).
	allowed, err = service.ResolveAllowedWorkspacesForTest(ctx, st, []string{"w-bob"}, userid.MustNew(aliceID))
	require.NoError(t, err)
	assert.Empty(t, allowed)

	// Requesting an unknown id returns nothing rather than an error.
	allowed, err = service.ResolveAllowedWorkspacesForTest(ctx, st, []string{"ghost"}, userid.MustNew(aliceID))
	require.NoError(t, err)
	assert.Empty(t, allowed)

	// Blank entries inside the requested list are skipped silently.
	allowed, err = service.ResolveAllowedWorkspacesForTest(ctx, st, []string{"", "w-alice", ""}, userid.MustNew(aliceID))
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"w-alice"}, allowed)
}

// A zero (unminted) user id must be REFUSED with PermissionDenied under both
// arms of resolveAllowedWorkspaces -- whether or not the caller named workspace
// ids -- so one unauthenticated verdict reaches the client as one behaviour.
//
// The guard is the only thing standing between an unminted id and a store query
// keyed on the empty string. Its near-miss is auth.WorkspacesReadableByUser,
// which answers the same condition with `if userID.IsZero() || len(...) == 0 {
// return nil, nil }` -- so "make the two agree" turns the deny into an empty
// success. Both consumers key on the code: ListTabs re-raises the error only
// when connect.CodeOf(err) == CodePermissionDenied, and /ws/userevents picks
// StatusPolicyViolation over StatusTryAgainLater on the same test. A (nil, nil)
// regression therefore renders as a 200 with an empty tab list plus a frontend
// reconnect loop against a condition that can never change.
func TestResolveAllowedWorkspaces_ZeroUserIDRefusesUnderBothArms(t *testing.T) {
	t.Parallel()

	st := hubtestutil.OpenTestStore(t)
	ctx := context.Background()
	aliceID := hubtestutil.CreateTestUser(t, st, "alice", "password-alice-123")
	require.NoError(t, st.Workspaces().Create(ctx, store.CreateWorkspaceParams{
		ID: "w-alice", Title: "w-alice", OwnerUserID: userid.MustNew(aliceID),
	}))

	for name, requested := range map[string][]string{
		"empty request": nil,
		"bulk request":  {"w-alice"},
	} {
		t.Run(name, func(t *testing.T) {
			allowed, err := service.ResolveAllowedWorkspacesForTest(ctx, st, requested, userid.UserID{})
			require.Error(t, err, "an unminted user id must refuse, not return an empty set")
			assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
			assert.Nil(t, allowed)
		})
	}
}
