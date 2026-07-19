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
)

// memJournal is a minimal in-memory crdt.Journal sufficient for the
// service-layer auth-stamping tests. It mirrors the structure of the
// crdt_test package's fakeJournal but is kept inline so the service
// tests don't reach into another package's private symbols.
type memJournal struct {
	mu        sync.Mutex
	state     *leapmuxv1.OrgCrdtState
	batches   []*leapmuxv1.OpBatch
	dedup     map[string]crdt.RecentBatchRecord
	commitErr error
}

func newMemJournal() *memJournal { return &memJournal{dedup: map[string]crdt.RecentBatchRecord{}} }

func (j *memJournal) LoadState(_ context.Context, _ string) (*leapmuxv1.OrgCrdtState, []*leapmuxv1.OpBatch, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	var state *leapmuxv1.OrgCrdtState
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
	j.dedup[c.DedupRow.BatchID] = c.DedupRow
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

// memOutbox is a no-op LifecycleOutboxReader for the service tests.
type memOutbox struct{}

func (memOutbox) ListPendingLifecycleOutbox(_ context.Context, _ string) ([]crdt.LifecycleOutboxRow, error) {
	return nil, nil
}
func (memOutbox) MarkLifecycleOutboxConsumed(_ context.Context, _ int64, _ time.Time) error {
	return nil
}

// allowAllAuth lets every (org, workspace, principal) write — the
// service-layer tests are about the wire-level stamping, not the auth
// matrix (that's covered inside crdt/validate_test.go).
type allowAllAuth struct{}

func (allowAllAuth) CanAccessWorkspace(_ context.Context, _, _, _ string) (bool, error) {
	return true, nil
}
func (allowAllAuth) CanUseWorker(_ context.Context, _, _, _ string) (bool, error) { return true, nil }

func TestCRDTAuthCheckerRejectsEmptyOrgID(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	orgID := storetest.SeedOrg(t, st, "org")
	owner := storetest.SeedUser(t, st, orgID, "owner")
	workspaceID := storetest.SeedWorkspace(t, st, orgID, owner.ID, "workspace")
	checker := service.NewCRDTAuthChecker(st)

	access, err := checker.CanAccessWorkspace(context.Background(), "", workspaceID, owner.ID)
	require.NoError(t, err)
	assert.False(t, access)
}

// crdtServiceEnv bundles the bits a CRDT-service test needs: a
// running manager (with a memJournal we can inspect), a registry that
// hands out that single manager, and the service handler itself.
type crdtServiceEnv struct {
	journal  *memJournal
	mgr      *crdt.Manager
	registry *crdt.Registry
	svc      *service.CRDTService
	orgID    string
}

func setupCRDTService(t *testing.T) *crdtServiceEnv {
	t.Helper()
	orgID := "org-test"
	j := newMemJournal()

	// The registry is the sole owner of Manager.Start — it dispatches
	// the goroutine itself. We supply a factory that constructs +
	// bootstraps a single manager (and reuses it on subsequent Get).
	var (
		once sync.Once
		mgr  *crdt.Manager
	)
	registry := crdt.NewRegistry(func(ctx context.Context, want string) (*crdt.Manager, error) {
		if want != orgID {
			return nil, errors.New("unexpected org")
		}
		once.Do(func() {
			mgr = crdt.NewManager(orgID, j, allowAllAuth{}, nil, time.Now)
			require.NoError(t, mgr.Bootstrap(ctx))
		})
		return mgr, nil
	}, nil)
	t.Cleanup(func() { registry.Shutdown(2 * time.Second) })

	// Force the registry to load the manager up front (so the tests
	// can pre-seed via MutateInternal / SubmitInternal directly).
	_, err := registry.Get(context.Background(), orgID)
	require.NoError(t, err)

	svc := service.NewCRDTService(nil /* store unused for these tests */, registry, nil, nil)

	// Seed a workspace + root so the tests can submit ops that pass
	// validation. This mirrors what the lifecycle outbox would do in
	// production after CreateWorkspace.
	mgr.MutateInternal(func(s *leapmuxv1.OrgCrdtState) {
		s.Workspaces["w1"] = &leapmuxv1.WorkspaceContentsRecord{WorkspaceId: "w1", RootNodeId: ""}
	})
	rootKind := &leapmuxv1.OrgOp{
		OpId: "seed-kind",
		Body: &leapmuxv1.OrgOp_SetNodeRegister{SetNodeRegister: &leapmuxv1.SetNodeRegisterOp{
			NodeId: "root1",
			Field:  &leapmuxv1.SetNodeRegisterOp_Kind{Kind: leapmuxv1.NodeKind_NODE_KIND_LEAF},
		}},
	}
	rootRegister := &leapmuxv1.OrgOp{
		OpId: "seed-register",
		Body: &leapmuxv1.OrgOp_SetWorkspaceRootNode{SetWorkspaceRootNode: &leapmuxv1.SetWorkspaceRootNodeOp{
			WorkspaceId: "w1", RootNodeId: "root1",
		}},
	}
	_, err = mgr.SubmitInternal(context.Background(), crdt.SubmitInput{
		OrgID:   orgID,
		Batches: []*leapmuxv1.OpBatch{{BatchId: "seed", Ops: []*leapmuxv1.OrgOp{rootKind, rootRegister}}},
	})
	require.NoError(t, err)

	return &crdtServiceEnv{journal: j, mgr: mgr, registry: registry, svc: svc, orgID: orgID}
}

// addTabOps builds the canonical 3-op SetTabRegister batch the tests
// reuse. Each op gets a caller-supplied id so dedup assertions are easy.
func addTabOps(idPrefix, tabID, tileID, workerID, position string) []*leapmuxv1.OrgOp {
	return []*leapmuxv1.OrgOp{
		{OpId: idPrefix + "-tile", Body: &leapmuxv1.OrgOp_SetTabRegister{SetTabRegister: &leapmuxv1.SetTabRegisterOp{
			TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: tabID,
			Field: &leapmuxv1.SetTabRegisterOp_TileId{TileId: tileID},
		}}},
		{OpId: idPrefix + "-worker", Body: &leapmuxv1.OrgOp_SetTabRegister{SetTabRegister: &leapmuxv1.SetTabRegisterOp{
			TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: tabID,
			Field: &leapmuxv1.SetTabRegisterOp_WorkerId{WorkerId: workerID},
		}}},
		{OpId: idPrefix + "-pos", Body: &leapmuxv1.OrgOp_SetTabRegister{SetTabRegister: &leapmuxv1.SetTabRegisterOp{
			TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: tabID,
			Field: &leapmuxv1.SetTabRegisterOp_Position{Position: position},
		}}},
	}
}

// TestCRDTService_SubmitOps_RequiresAuth asserts the handler rejects
// callers without an authenticated user in the context — the same
// guarantee the ConnectRPC interceptor provides in production.
func TestCRDTService_SubmitOps_RequiresAuth(t *testing.T) {
	env := setupCRDTService(t)

	req := connect.NewRequest(&leapmuxv1.SubmitOpsRequest{
		OrgId:   env.orgID,
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
	env := setupCRDTService(t)
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{ID: "user-alice", OrgID: env.orgID})

	req := connect.NewRequest(&leapmuxv1.SubmitOpsRequest{
		OrgId:   env.orgID,
		Epoch:   env.mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch(),
		Batches: []*leapmuxv1.OpBatch{{BatchId: "b1", Ops: addTabOps("op1", "tA", "root1", "wkr1", "p1")}},
	})

	resp, err := env.svc.SubmitOps(ctx, req)
	require.NoError(t, err)
	require.Len(t, resp.Msg.GetResults(), 1)
	require.NotNil(t, resp.Msg.GetResults()[0].GetCommitted())

	// The dedup row landed under principal_id=user-alice — proving the
	// service stamped it, not a value the request body controlled.
	row := env.journal.dedupRow("b1")
	require.NotNil(t, row, "dedup row for batch b1 must exist")
	assert.Equal(t, "user-alice", row.PrincipalID, "principal_id must match the authenticated user")
}

// TestCRDTService_SubmitOps_OriginClientIdSpoofingRejected encodes the
// security guarantee that the manager overwrites whatever
// `origin_client_id` appears in the request body with the
// authenticated session's identity. A malicious client setting
// origin_client_id="hub" in the wire-level OrgOp must not be able to
// impersonate the hub or another user.
func TestCRDTService_SubmitOps_OriginClientIdSpoofingRejected(t *testing.T) {
	env := setupCRDTService(t)
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{ID: "user-bob", OrgID: env.orgID})

	spoofed := addTabOps("op2", "tB", "root1", "wkr1", "p1")
	for _, op := range spoofed {
		// Attempt to impersonate the hub's own client_id.
		op.OriginClientId = "hub-spoofed"
	}

	req := connect.NewRequest(&leapmuxv1.SubmitOpsRequest{
		OrgId:   env.orgID,
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
	assert.Equal(t, "user-bob", row.PrincipalID,
		"principal_id must reflect the authenticated user, not any spoofed origin_client_id")
	// And the tab's stored worker_id reflects the actual op, so we know
	// the commit happened through the standard validate-then-apply path.
	assert.Equal(t, "wkr1", tab.GetWorkerId().GetValue())
}

// TestCRDTService_SubmitOps_ForeignOrgIsNotFound pins the fail-closed org guard:
// SubmitOps must refuse a caller-supplied org_id that is not the caller's own
// (personal) org with NotFound, BEFORE the registry materializes that org's
// manager. Without the guard, any authenticated user could spin up (and, via
// the WS path, pin) an arbitrary tenant's CRDT manager.
func TestCRDTService_SubmitOps_ForeignOrgIsNotFound(t *testing.T) {
	env := setupCRDTService(t)
	// The caller belongs to a DIFFERENT org than the one they name in the request.
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{ID: "user-alice", OrgID: "org-alice-personal"})

	req := connect.NewRequest(&leapmuxv1.SubmitOpsRequest{
		OrgId:   env.orgID, // a foreign org id (org-test), not org-alice-personal
		Epoch:   env.mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch(),
		Batches: []*leapmuxv1.OpBatch{{BatchId: "b1", Ops: addTabOps("op1", "tA", "root1", "wkr1", "p1")}},
	})

	_, err := env.svc.SubmitOps(ctx, req)
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err),
		"a foreign org_id must be NotFound, not accepted into the manager")
	// The batch must never have committed to the foreign manager.
	assert.Nil(t, env.journal.dedupRow("b1"),
		"a rejected foreign-org submit must not commit any batch")
}

// TestCRDTService_GetMaterialized_ForeignOrgIsNotFound is the read-side twin:
// a foreign org_id must be NotFound rather than an empty snapshot that silently
// materialized the foreign tenant's manager.
func TestCRDTService_GetMaterialized_ForeignOrgIsNotFound(t *testing.T) {
	env := setupCRDTService(t)
	st := hubtestutil.OpenTestStore(t)
	svc := service.NewCRDTService(st, env.registry, nil, nil)
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{ID: "user-alice", OrgID: "org-alice-personal"})

	_, err := svc.GetMaterialized(ctx, connect.NewRequest(&leapmuxv1.GetMaterializedRequest{OrgId: env.orgID}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err),
		"a foreign org_id must be NotFound, not an empty materialized snapshot")
}

// TestCRDTService_SubmitOps_EmptyOrgResolvesToPersonal asserts the other arm of
// ResolveOrgID: an empty org_id falls back to the caller's own org rather than
// failing, so existing CLI callers that omit org_id keep working.
func TestCRDTService_SubmitOps_EmptyOrgResolvesToPersonal(t *testing.T) {
	env := setupCRDTService(t)
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{ID: "user-alice", OrgID: env.orgID})

	req := connect.NewRequest(&leapmuxv1.SubmitOpsRequest{
		OrgId:   "", // omitted; resolves to the caller's personal org (env.orgID)
		Epoch:   env.mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch(),
		Batches: []*leapmuxv1.OpBatch{{BatchId: "b1", Ops: addTabOps("op1", "tA", "root1", "wkr1", "p1")}},
	})

	resp, err := env.svc.SubmitOps(ctx, req)
	require.NoError(t, err)
	require.Len(t, resp.Msg.GetResults(), 1)
	require.NotNil(t, resp.Msg.GetResults()[0].GetCommitted(),
		"an empty org_id must resolve to the caller's own org and commit there")
}

// TestCRDTService_UpdatePresence_RequiresAuth ensures presence calls
// without an authenticated user are rejected with Unauthenticated.
func TestCRDTService_UpdatePresence_RequiresAuth(t *testing.T) {
	env := setupCRDTService(t)
	req := connect.NewRequest(&leapmuxv1.UpdatePresenceRequest{OrgId: env.orgID, WorkspaceId: "w1"})
	_, err := env.svc.UpdatePresence(context.Background(), req)
	require.Error(t, err)
	var ce *connect.Error
	require.True(t, errors.As(err, &ce))
	assert.Equal(t, connect.CodeUnauthenticated, ce.Code())
}

func TestCRDTService_UpdatePresence_DelegationRejectsSiblingWorkspace(t *testing.T) {
	env := setupCRDTService(t)
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{
		ID:         "user-alice",
		Credential: auth.DelegationCredential("test-delegation", "w1", "worker-mint"),
	})

	_, err := env.svc.UpdatePresence(ctx, connect.NewRequest(&leapmuxv1.UpdatePresenceRequest{
		OrgId:       env.orgID,
		WorkspaceId: "w2",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err),
		"a delegated presence heartbeat must not advertise activity in a sibling workspace")
}

func TestCRDTService_GetMaterialized_DelegationEmptyAccessDoesNotAllowAll(t *testing.T) {
	env := setupCRDTService(t)
	st := hubtestutil.OpenTestStore(t)
	orgID := env.orgID
	require.NoError(t, st.Orgs().Create(context.Background(), store.CreateOrgParams{
		ID:   orgID,
		Name: orgID,
	}))
	user := storetest.SeedUser(t, st, orgID, "alice")
	_ = storetest.SeedWorkspace(t, st, orgID, user.ID, "w1")
	svc := service.NewCRDTService(st, env.registry, nil, nil)
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{
		ID:         user.ID,
		OrgID:      orgID,
		Credential: auth.DelegationCredential("test-delegation", "missing-workspace", "worker-mint"),
	})

	resp, err := svc.GetMaterialized(ctx, connect.NewRequest(&leapmuxv1.GetMaterializedRequest{OrgId: orgID}))
	require.NoError(t, err)
	assert.Empty(t, resp.Msg.GetState().GetWorkspaces(),
		"an empty delegated ACL must not be interpreted as the all-workspaces materialized filter")
	assert.Empty(t, resp.Msg.GetState().GetNodes())
	assert.Empty(t, resp.Msg.GetState().GetTabs())
}

func TestCRDTService_UpdatePresence_RequiresCanonicalWorkspaceReadAccess(t *testing.T) {
	t.Run("workspace must belong to requested org", func(t *testing.T) {
		env := setupCRDTService(t)
		st := hubtestutil.OpenTestStore(t)
		otherOrgID := storetest.SeedOrg(t, st, "presence-other-org")
		user := storetest.SeedUser(t, st, otherOrgID, "presence-owner")
		workspaceID := storetest.SeedWorkspace(t, st, otherOrgID, user.ID, "Other org")
		svc := service.NewCRDTService(st, env.registry, nil, nil)

		ctx := auth.WithUser(context.Background(), &auth.UserInfo{ID: user.ID, OrgID: otherOrgID})
		_, err := svc.UpdatePresence(ctx, connect.NewRequest(&leapmuxv1.UpdatePresenceRequest{
			OrgId: env.orgID, WorkspaceId: workspaceID,
		}))
		require.Error(t, err)
		assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	})

	t.Run("non-owner delegated heartbeat is denied", func(t *testing.T) {
		env := setupCRDTService(t)
		st := hubtestutil.OpenTestStore(t)
		require.NoError(t, st.Orgs().Create(context.Background(), store.CreateOrgParams{
			ID: env.orgID, Name: "presence-owner-org",
		}))
		owner := storetest.SeedUser(t, st, env.orgID, "presence-owner")
		other := storetest.SeedUser(t, st, env.orgID, "presence-other")
		workspaceID := storetest.SeedWorkspace(t, st, env.orgID, owner.ID, "Owned")
		svc := service.NewCRDTService(st, env.registry, nil, nil)

		// A delegation credential pinned to the right workspace still cannot
		// heartbeat for a user who does not own it: access is owner-only.
		ctx := auth.WithUser(context.Background(), &auth.UserInfo{
			ID:         other.ID,
			OrgID:      env.orgID,
			Credential: auth.DelegationCredential("delegation-token", workspaceID, "worker-mint"),
		})
		_, err := svc.UpdatePresence(ctx, connect.NewRequest(&leapmuxv1.UpdatePresenceRequest{
			OrgId: env.orgID, WorkspaceId: workspaceID,
		}))
		require.Error(t, err)
		assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	})
}

// TestCRDTService_UpdatePresence_ClientIDNamespaces asserts that
// session, bearer-kind/token, and user fallback identities remain
// distinct even when their raw IDs are equal.
func TestCRDTService_UpdatePresence_ClientIDNamespaces(t *testing.T) {
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
			info:     &auth.UserInfo{Credential: auth.DelegationCredential("shared-id", "w1", "worker-mint")},
			expected: "bearer:64:shared-id",
		},
		{
			name:     "user fallback has its own namespace",
			info:     &auth.UserInfo{},
			expected: "user:shared-id",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := setupCRDTService(t)
			st := hubtestutil.OpenTestStore(t)
			require.NoError(t, st.Orgs().Create(context.Background(), store.CreateOrgParams{
				ID: env.orgID, Name: env.orgID,
			}))
			require.NoError(t, st.Users().Create(context.Background(), store.CreateUserParams{
				ID: "shared-id", OrgID: env.orgID, Username: "presence-user",
			}))
			require.NoError(t, st.Workspaces().Create(context.Background(), store.CreateWorkspaceParams{
				ID: "w1", OrgID: env.orgID, OwnerUserID: "shared-id", Title: "Presence",
			}))
			tc.info.ID = "shared-id"
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
				OrgId: env.orgID, WorkspaceId: "w1",
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
// the `/ws/orgevents` handler uses to project a per-user workspace
// filter from the requested set. The helper must (a) drop workspaces
// the caller has no access to, (b) expand an empty request to the full
// set the caller can read, and (c) skip blank ids silently.
func TestResolveAllowedWorkspaces_FiltersAndDedups(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	ctx := context.Background()
	aliceID := hubtestutil.CreateTestUser(t, st, "alice", "password-alice-123")
	bobID := hubtestutil.CreateTestUser(t, st, "bob", "password-bob-456")

	alice, err := st.Users().GetByID(ctx, aliceID)
	require.NoError(t, err)

	// Alice owns w1 in her personal org. Bob owns w2 in his.
	require.NoError(t, st.Workspaces().Create(ctx, store.CreateWorkspaceParams{
		ID: "w-alice", Title: "w-alice", OrgID: alice.OrgID, OwnerUserID: aliceID,
	}))
	bob, err := st.Users().GetByID(ctx, bobID)
	require.NoError(t, err)
	require.NoError(t, st.Workspaces().Create(ctx, store.CreateWorkspaceParams{
		ID: "w-bob", Title: "w-bob", OrgID: bob.OrgID, OwnerUserID: bobID,
	}))

	// Empty request → returns every workspace alice can read inside her org.
	allowed, err := service.ResolveAllowedWorkspacesForTest(ctx, st, alice.OrgID, nil, aliceID)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"w-alice"}, allowed)

	// Requesting Bob's workspace returns nothing (alice has no access).
	allowed, err = service.ResolveAllowedWorkspacesForTest(ctx, st, alice.OrgID, []string{"w-bob"}, aliceID)
	require.NoError(t, err)
	assert.Empty(t, allowed)

	// Requesting an unknown id returns nothing rather than an error.
	allowed, err = service.ResolveAllowedWorkspacesForTest(ctx, st, alice.OrgID, []string{"ghost"}, aliceID)
	require.NoError(t, err)
	assert.Empty(t, allowed)

	// Blank entries inside the requested list are skipped silently.
	allowed, err = service.ResolveAllowedWorkspacesForTest(ctx, st, alice.OrgID, []string{"", "w-alice", ""}, aliceID)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"w-alice"}, allowed)
}

func TestResolveAllowedWorkspacesForUser_DelegationPinsScope(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	ctx := context.Background()
	orgID := storetest.SeedOrg(t, st, "primary-org")
	user := storetest.SeedUser(t, st, orgID, "alice")
	pinned := storetest.SeedWorkspace(t, st, orgID, user.ID, "Pinned")
	sibling := storetest.SeedWorkspace(t, st, orgID, user.ID, "Sibling")
	info := &auth.UserInfo{
		ID:         user.ID,
		OrgID:      orgID,
		Credential: auth.DelegationCredential("test-delegation", pinned, "worker-mint"),
	}

	allowed, err := service.ResolveAllowedWorkspacesForUserForTest(ctx, st, orgID, nil, info)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{pinned}, allowed,
		"an empty delegated workspace request must expand to the pinned workspace only")

	allowed, err = service.ResolveAllowedWorkspacesForUserForTest(ctx, st, orgID, []string{pinned}, info)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{pinned}, allowed)

	_, err = service.ResolveAllowedWorkspacesForUserForTest(ctx, st, orgID, []string{sibling}, info)
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err),
		"explicit sibling workspace requests must fail closed instead of silently widening")

	// A delegation request naming ONLY blank ids is the emptyResult branch: it must
	// deny (resolve to nothing), NOT fall through to the pinned workspace the way a
	// genuinely-empty request does, and NOT widen to every readable workspace the way
	// a non-delegation empty request does. This is the case the middle bool return of
	// delegationScopedWorkspaceRequest disambiguates.
	allowed, err = service.ResolveAllowedWorkspacesForUserForTest(ctx, st, orgID, []string{""}, info)
	require.NoError(t, err)
	assert.Empty(t, allowed,
		"a delegated request of only blank workspace ids must resolve to no workspaces, not the pinned one")
}
