package service_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/channelmgr"
	"github.com/leapmux/leapmux/internal/hub/crdt"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/storetest"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/hub/workermgr"
	"github.com/leapmux/leapmux/internal/util/id"
)

type blockingWorkspaceChannelCloser struct {
	mu          sync.Mutex
	blockNext   bool
	entered     chan struct{}
	release     chan struct{}
	cancelAfter context.CancelFunc
}

func (c *blockingWorkspaceChannelCloser) CloseChannelsByUsersForWorkspace(_ string, _ []string) int {
	c.mu.Lock()
	block := c.blockNext
	if block {
		c.blockNext = false
	}
	c.mu.Unlock()
	if c.cancelAfter != nil {
		c.cancelAfter()
	}
	if block {
		close(c.entered)
		<-c.release
	}
	return 0
}

func TestUpdateWorkspaceSharingRevokesSubscriberAndChannelsForRemovedUsers(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	orgID := storetest.SeedOrg(t, st, "sharing-org", false)
	owner := storetest.SeedUser(t, st, orgID, "owner")
	removedMember := storetest.SeedUser(t, st, orgID, "removed-member")
	retainedMember := storetest.SeedUser(t, st, orgID, "retained-member")
	storetest.SeedOrgMember(t, st, orgID, removedMember.ID, leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_MEMBER)
	storetest.SeedOrgMember(t, st, orgID, retainedMember.ID, leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_MEMBER)
	workspaceID := storetest.SeedWorkspace(t, st, orgID, owner.ID, "shared")

	registry := crdt.NewRegistry(func(ctx context.Context, requestedOrgID string) (*crdt.Manager, error) {
		mgr := crdt.NewManager(requestedOrgID, newMemJournal(), service.NewCRDTAuthChecker(st), nil, time.Now)
		if err := mgr.Bootstrap(ctx); err != nil {
			return nil, err
		}
		return mgr, nil
	}, nil, crdt.WithManagerIdleTTL(0))
	t.Cleanup(func() { registry.Shutdown(2 * time.Second) })
	mgr, err := registry.Get(context.Background(), orgID)
	require.NoError(t, err)

	sub := &crdt.Subscriber{
		UserID: removedMember.ID,
		Filter: crdt.SubscriberFilter{WorkspaceIDs: map[string]bool{}},
		Send:   func(*crdt.MarshaledEvent) error { return nil },
	}
	_, unsubscribe := mgr.Subscribe(sub)
	defer unsubscribe()

	workerID := id.Generate()
	removedChannelID := id.Generate()
	retainedChannelID := id.Generate()
	unrelatedDelegationChannelID := id.Generate()
	channels := channelmgr.New()
	channels.RegisterWithAuthInfo(removedChannelID, workerID, removedMember.ID, channelmgr.AuthInfo{}, nil)
	channels.RegisterWithAuthInfo(retainedChannelID, workerID, retainedMember.ID, channelmgr.AuthInfo{}, nil)
	channels.RegisterWithAuthInfo(unrelatedDelegationChannelID, workerID, removedMember.ID, channelmgr.AuthInfo{
		Credential: auth.DelegationCredential("test-delegation", id.Generate()),
	}, nil)
	workers := workermgr.New()
	workerMessages := make(chan *leapmuxv1.ConnectResponse, 2)
	workers.Register(&workermgr.Conn{
		WorkerID: workerID,
		SendFn: func(msg *leapmuxv1.ConnectResponse) error {
			workerMessages <- msg
			return nil
		},
	})
	channelSvc := service.NewChannelService(
		st, workers, channels, workermgr.NewPendingRequests(func() time.Duration { return time.Second }), allowAllAuthFreshness{})
	svc := service.NewWorkspaceService(st, false, registry, channelSvc)
	ownerCtx := auth.WithUser(context.Background(), &auth.UserInfo{ID: owner.ID, OrgID: orgID})
	_, err = svc.UpdateWorkspaceSharing(ownerCtx, connect.NewRequest(&leapmuxv1.UpdateWorkspaceSharingRequest{
		WorkspaceId: workspaceID,
		ShareMode:   leapmuxv1.ShareMode_SHARE_MODE_MEMBERS,
		UserIds:     []string{removedMember.ID, retainedMember.ID},
	}))
	require.NoError(t, err)
	assert.True(t, sub.Filter.IsAllowed(workspaceID),
		"an existing subscriber must gain visibility immediately after the grant commits")

	_, err = svc.UpdateWorkspaceSharing(ownerCtx, connect.NewRequest(&leapmuxv1.UpdateWorkspaceSharingRequest{
		WorkspaceId: workspaceID,
		ShareMode:   leapmuxv1.ShareMode_SHARE_MODE_MEMBERS,
		UserIds:     []string{retainedMember.ID},
	}))
	require.NoError(t, err)
	assert.False(t, sub.Filter.IsAllowed(workspaceID),
		"an existing subscriber must lose visibility immediately after the revoke commits")
	assert.False(t, channels.Exists(removedChannelID), "removed member's stale workspace snapshot must be closed")
	assert.True(t, channels.Exists(retainedChannelID), "retained member's channel must remain open")
	assert.True(t, channels.Exists(unrelatedDelegationChannelID),
		"removed member's delegation channel for another workspace must remain open")
	select {
	case msg := <-workerMessages:
		require.NotNil(t, msg.GetChannelClose())
		assert.Equal(t, removedChannelID, msg.GetChannelClose().GetChannelId())
	case <-time.After(time.Second):
		t.Fatal("worker was not notified about the removed member's channel")
	}
}

// A workspace may be shared with a user who is not a member of the owning
// organization (cross-org collaboration). The grant is created and confers
// read access on its own, without an org_members row.
func TestUpdateWorkspaceSharingAllowsUserOutsideWorkspaceOrg(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	workspaceOrgID := storetest.SeedOrg(t, st, "workspace-org", false)
	otherOrgID := storetest.SeedOrg(t, st, "other-org", false)
	owner := storetest.SeedUser(t, st, workspaceOrgID, "owner")
	outsider := storetest.SeedUser(t, st, otherOrgID, "outsider")
	storetest.SeedOrgMember(t, st, otherOrgID, outsider.ID, leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_MEMBER)
	workspaceID := storetest.SeedWorkspace(t, st, workspaceOrgID, owner.ID, "private")

	svc := service.NewWorkspaceService(st, false, nil, &noopWorkspaceChannelCloser{})
	ownerCtx := auth.WithUser(context.Background(), &auth.UserInfo{ID: owner.ID, OrgID: workspaceOrgID})
	_, err := svc.UpdateWorkspaceSharing(ownerCtx, connect.NewRequest(&leapmuxv1.UpdateWorkspaceSharingRequest{
		WorkspaceId: workspaceID,
		ShareMode:   leapmuxv1.ShareMode_SHARE_MODE_MEMBERS,
		UserIds:     []string{outsider.ID},
	}))

	require.NoError(t, err)
	hasAccess, accessErr := st.WorkspaceAccess().HasAccess(context.Background(), store.HasWorkspaceAccessParams{
		WorkspaceID: workspaceID,
		UserID:      outsider.ID,
	})
	require.NoError(t, accessErr)
	assert.True(t, hasAccess, "cross-org share must create the grant")

	// The outsider can read the workspace on the strength of the grant alone.
	canRead, readErr := auth.WorkspaceCanRead(context.Background(), st, workspaceID, outsider.ID)
	require.NoError(t, readErr)
	assert.True(t, canRead, "a non-member with a grant must be able to read the workspace")

	// Sharing to a non-existent user is still rejected.
	_, err = svc.UpdateWorkspaceSharing(ownerCtx, connect.NewRequest(&leapmuxv1.UpdateWorkspaceSharingRequest{
		WorkspaceId: workspaceID,
		ShareMode:   leapmuxv1.ShareMode_SHARE_MODE_MEMBERS,
		UserIds:     []string{"nonexistent-user-id"},
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

// The owner-only sharing handlers must reject a delegation bearer even though it
// authenticates as the owner, matching the sibling workspace-lifecycle handlers.
func TestWorkspaceSharingHandlersRejectDelegationBearer(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	orgID := storetest.SeedOrg(t, st, "org", false)
	owner := storetest.SeedUser(t, st, orgID, "owner")
	workspaceID := storetest.SeedWorkspace(t, st, orgID, owner.ID, "private")

	svc := service.NewWorkspaceService(st, false, nil, &noopWorkspaceChannelCloser{})
	// A delegation-scoped bearer for the owner, pinned to this very workspace.
	delegationCtx := auth.WithUser(context.Background(), &auth.UserInfo{
		ID:         owner.ID,
		OrgID:      orgID,
		Credential: auth.DelegationCredential("del-token", workspaceID),
	})

	_, err := svc.UpdateWorkspaceSharing(delegationCtx, connect.NewRequest(&leapmuxv1.UpdateWorkspaceSharingRequest{
		WorkspaceId: workspaceID,
		ShareMode:   leapmuxv1.ShareMode_SHARE_MODE_MEMBERS,
		UserIds:     []string{owner.ID},
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err), "UpdateWorkspaceSharing must reject a delegation bearer")

	_, err = svc.ListWorkspaceShares(delegationCtx, connect.NewRequest(&leapmuxv1.ListWorkspaceSharesRequest{
		WorkspaceId: workspaceID,
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err), "ListWorkspaceShares must reject a delegation bearer")
}

func TestUpdateWorkspaceSharingSerializesCommitAndReconciliation(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	orgID := storetest.SeedOrg(t, st, "sharing-order-org", false)
	owner := storetest.SeedUser(t, st, orgID, "owner")
	member := storetest.SeedUser(t, st, orgID, "member")
	storetest.SeedOrgMember(t, st, orgID, member.ID, leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_MEMBER)
	workspaceID := storetest.SeedWorkspace(t, st, orgID, owner.ID, "shared")
	registry := crdt.NewRegistry(func(ctx context.Context, requestedOrgID string) (*crdt.Manager, error) {
		mgr := crdt.NewManager(requestedOrgID, newMemJournal(), service.NewCRDTAuthChecker(st), nil, time.Now)
		if err := mgr.Bootstrap(ctx); err != nil {
			return nil, err
		}
		return mgr, nil
	}, nil, crdt.WithManagerIdleTTL(0))
	t.Cleanup(func() { registry.Shutdown(2 * time.Second) })
	mgr, err := registry.Get(context.Background(), orgID)
	require.NoError(t, err)
	projectionEvents := make(chan *leapmuxv1.WorkspaceProjectionChanged, 4)
	sub := &crdt.Subscriber{
		UserID: member.ID,
		Filter: crdt.SubscriberFilter{WorkspaceIDs: map[string]bool{}},
		Send: func(event *crdt.MarshaledEvent) error {
			projectionEvents <- event.Event.GetWorkspaceProjection()
			return nil
		},
	}
	_, unsubscribe := mgr.Subscribe(sub)
	defer unsubscribe()
	closer := &blockingWorkspaceChannelCloser{entered: make(chan struct{}), release: make(chan struct{})}
	svc := service.NewWorkspaceService(st, false, registry, closer)
	ownerCtx := auth.WithUser(context.Background(), &auth.UserInfo{ID: owner.ID, OrgID: orgID})

	_, err = svc.UpdateWorkspaceSharing(ownerCtx, connect.NewRequest(&leapmuxv1.UpdateWorkspaceSharingRequest{
		WorkspaceId: workspaceID,
		ShareMode:   leapmuxv1.ShareMode_SHARE_MODE_MEMBERS,
		UserIds:     []string{member.ID},
	}))
	require.NoError(t, err)
	require.NotNil(t, (<-projectionEvents).GetGranted())
	closer.mu.Lock()
	closer.blockNext = true
	closer.mu.Unlock()

	firstDone := make(chan error, 1)
	go func() {
		_, revokeErr := svc.UpdateWorkspaceSharing(ownerCtx, connect.NewRequest(&leapmuxv1.UpdateWorkspaceSharingRequest{
			WorkspaceId: workspaceID,
			ShareMode:   leapmuxv1.ShareMode_SHARE_MODE_PRIVATE,
		}))
		firstDone <- revokeErr
	}()
	<-closer.entered
	revoke := <-projectionEvents
	require.NotNil(t, revoke.GetRevoked(), "projection revoke must publish immediately after the ACL commit")
	assert.False(t, sub.Filter.IsAllowed(workspaceID),
		"post-commit channel cleanup must not create a stale subscriber-visibility gap")

	secondDone := make(chan error, 1)
	go func() {
		_, grantErr := svc.UpdateWorkspaceSharing(ownerCtx, connect.NewRequest(&leapmuxv1.UpdateWorkspaceSharingRequest{
			WorkspaceId: workspaceID,
			ShareMode:   leapmuxv1.ShareMode_SHARE_MODE_MEMBERS,
			UserIds:     []string{member.ID},
		}))
		secondDone <- grantErr
	}()
	select {
	case err := <-secondDone:
		require.NoError(t, err)
		t.Fatal("a later ACL replacement completed before the earlier post-commit reconciliation")
	case <-time.After(100 * time.Millisecond):
	}
	close(closer.release)
	require.NoError(t, <-firstDone)
	require.NoError(t, <-secondDone)
	require.NotNil(t, (<-projectionEvents).GetGranted())
}

func TestUpdateWorkspaceSharingReconcilesAfterRequestCancellation(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	orgID := storetest.SeedOrg(t, st, "sharing-cancel-org", false)
	owner := storetest.SeedUser(t, st, orgID, "owner")
	member := storetest.SeedUser(t, st, orgID, "member")
	storetest.SeedOrgMember(t, st, orgID, member.ID, leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_MEMBER)
	workspaceID := storetest.SeedWorkspace(t, st, orgID, owner.ID, "shared")

	registry := crdt.NewRegistry(func(ctx context.Context, requestedOrgID string) (*crdt.Manager, error) {
		mgr := crdt.NewManager(requestedOrgID, newMemJournal(), service.NewCRDTAuthChecker(st), nil, time.Now)
		if err := mgr.Bootstrap(ctx); err != nil {
			return nil, err
		}
		return mgr, nil
	}, nil, crdt.WithManagerIdleTTL(0))
	t.Cleanup(func() { registry.Shutdown(2 * time.Second) })
	mgr, err := registry.Get(context.Background(), orgID)
	require.NoError(t, err)
	sub := &crdt.Subscriber{
		UserID: member.ID,
		Filter: crdt.SubscriberFilter{WorkspaceIDs: map[string]bool{}},
		Send:   func(*crdt.MarshaledEvent) error { return nil },
	}
	_, unsubscribe := mgr.Subscribe(sub)
	defer unsubscribe()

	baseCtx, cancel := context.WithCancel(context.Background())
	// The closer cancels the request context from inside CloseChannelsByUsersForWorkspace
	// -- the LAST step of the handler, after the DB commit and the in-memory grant
	// reconciliation. The grant must already be applied (and survive) by then: the
	// reconciliation lives in manager state, not the request lifecycle, so a
	// teardown-time cancellation cannot roll it back or create a stale-visibility gap.
	closer := &blockingWorkspaceChannelCloser{cancelAfter: cancel}
	svc := service.NewWorkspaceService(st, false, registry, closer)
	ownerCtx := auth.WithUser(baseCtx, &auth.UserInfo{ID: owner.ID, OrgID: orgID})
	_, err = svc.UpdateWorkspaceSharing(ownerCtx, connect.NewRequest(&leapmuxv1.UpdateWorkspaceSharingRequest{
		WorkspaceId: workspaceID,
		ShareMode:   leapmuxv1.ShareMode_SHARE_MODE_MEMBERS,
		UserIds:     []string{member.ID},
	}))
	require.NoError(t, err)
	// Guard against the assertion passing vacuously: the cancellation must have
	// actually fired during the handler (otherwise this proves nothing about
	// cancellation resilience).
	require.ErrorIs(t, baseCtx.Err(), context.Canceled,
		"the request context must have been cancelled by the channel-close step")
	assert.True(t, sub.Filter.IsAllowed(workspaceID),
		"the committed grant must remain reconciled in manager state after the request context was cancelled at teardown")
}
