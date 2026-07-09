package crdt_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/crdt"
)

// countWorkspaceProjectionEvents tallies the WorkspaceProjectionChanged grant /
// revoke events delivered for one workspace (ignoring the initial materialized
// snapshot and any op batches).
func countWorkspaceProjectionEvents(events []*leapmuxv1.WatchOrgEvent, workspaceID string) (granted, revoked int) {
	for _, evt := range events {
		wp := evt.GetWorkspaceProjection()
		if wp == nil || wp.GetWorkspaceId() != workspaceID {
			continue
		}
		if wp.GetGranted() != nil {
			granted++
		}
		if wp.GetRevoked() != nil {
			revoked++
		}
	}
	return granted, revoked
}

// blockingReadChecker lets a test park ExpandSubscribersForWorkspace inside its
// batch read-ACL lookup -- while the expand holds aclTransitionMu -- to probe
// serialization against CommitWorkspaceAccessTransition. It blocks only once
// armed, so manager setup and workspace seeding never stall on it.
type blockingReadChecker struct {
	armed   atomic.Bool
	reached chan struct{}
	release chan struct{}
	once    sync.Once
}

func (*blockingReadChecker) CanWriteWorkspace(_ context.Context, _, _, _ string) (bool, error) {
	return true, nil
}
func (*blockingReadChecker) CanReadWorkspace(_ context.Context, _, _, _ string) (bool, error) {
	return true, nil
}
func (*blockingReadChecker) CanUseWorker(_ context.Context, _, _, _ string) (bool, error) {
	return true, nil
}
func (b *blockingReadChecker) CanReadWorkspaceForUsers(_ context.Context, _, _ string, userIDs []string) (map[string]bool, error) {
	if b.armed.Load() {
		b.once.Do(func() { close(b.reached) })
		<-b.release
	}
	out := make(map[string]bool, len(userIDs))
	for _, u := range userIDs {
		out[u] = true
	}
	return out, nil
}

// TestExpandSubscribersForWorkspace_SerializesWithACLTransition pins the fix for
// the read-ACL-then-apply TOCTOU: a workspace-create expand must serialize with a
// concurrent unshare of the same workspace under aclTransitionMu, or the expand
// re-adds a reader to the workspace's filter AFTER the unshare revoked them --
// leaving a revoked reader receiving the workspace's ops (a leak the post-commit
// re-classify never reconverges).
func TestExpandSubscribersForWorkspace_SerializesWithACLTransition(t *testing.T) {
	checker := &blockingReadChecker{reached: make(chan struct{}), release: make(chan struct{})}
	mgr, _, _ := runManager(t, "org", checker, 220_000)
	seedRootInternal(t, mgr, "w1", "root1")

	// R can read w1 but does not yet admit it -- an expand candidate.
	capR := &captureSubscriber{}
	subR := &crdt.Subscriber{UserID: "userR", Filter: crdt.SubscriberFilter{WorkspaceIDs: map[string]bool{}}, Send: capR.send}
	_, unsubR := mgr.Subscribe(subR)
	defer unsubR()

	// Arm only now, so setup + seeding never blocked in the read-ACL lookup.
	checker.armed.Store(true)

	expandDone := make(chan struct{})
	go func() {
		_ = mgr.ExpandSubscribersForWorkspace(context.Background(), "w1")
		close(expandDone)
	}()
	<-checker.reached // expand is parked in the read-ACL lookup, holding aclTransitionMu

	// A concurrent unshare of the SAME workspace must serialize behind the in-flight
	// expand: its commit callback must NOT fire while expand is parked.
	commitStarted := make(chan struct{})
	commitDone := make(chan struct{})
	go func() {
		_ = mgr.CommitWorkspaceAccessTransition("w1", map[string]struct{}{}, func() error {
			close(commitStarted)
			return nil
		})
		close(commitDone)
	}()

	select {
	case <-commitStarted:
		t.Fatal("ACL transition ran concurrently with an in-flight expand -- aclTransitionMu is not serializing them (lost-update TOCTOU)")
	case <-time.After(150 * time.Millisecond):
		// Good: the transition is blocked on aclTransitionMu.
	}

	// Release expand: it adds w1 to R's filter, then the now-unblocked transition
	// revokes it. Net: R must NOT admit w1 -- no revoked-reader leak.
	close(checker.release)
	<-expandDone
	<-commitDone

	assert.False(t, subR.Filter.IsAllowed("w1"),
		"after expand then a serialized revoke, R's filter must not admit w1")
}

// TestSubscribeWithACL_SerializesWithACLTransition pins the fix for the
// resolve-then-register TOCTOU: a subscriber's filter must be resolved AND the
// subscriber registered atomically under aclTransitionMu, so a concurrent unshare
// of a workspace in that filter cannot complete in the gap and leave the
// subscriber receiving a just-revoked workspace's ops. Here the subscribe wins the
// lock first: the unshare must block behind it, and once the subscriber is
// registered (with a pre-commit filter admitting w1) the unshare's revoke pass
// catches it -- so it ends up correctly revoked, not leaking.
func TestSubscribeWithACL_SerializesWithACLTransition(t *testing.T) {
	mgr, _, _ := runManager(t, "org", allowAll{}, 230_000)
	seedRootInternal(t, mgr, "w1", "root1")

	reached := make(chan struct{})
	release := make(chan struct{})
	capR := &captureSubscriber{}
	subR := &crdt.Subscriber{UserID: "userR", Send: capR.send}

	type subResult struct {
		unsub func()
		err   error
	}
	resCh := make(chan subResult, 1)
	go func() {
		// The resolve runs while SubscribeWithACL holds aclTransitionMu; parking
		// here parks the whole resolve+register under that lock.
		_, unsub, err := mgr.SubscribeWithACL(subR, func() (map[string]bool, error) {
			close(reached)
			<-release
			return map[string]bool{"w1": true}, nil // R could read w1 pre-commit
		})
		resCh <- subResult{unsub, err}
	}()
	<-reached // subscribe is parked in resolve, holding aclTransitionMu

	commitStarted := make(chan struct{})
	commitDone := make(chan struct{})
	go func() {
		_ = mgr.CommitWorkspaceAccessTransition("w1", map[string]struct{}{}, func() error {
			close(commitStarted)
			return nil
		})
		close(commitDone)
	}()

	select {
	case <-commitStarted:
		t.Fatal("ACL transition ran while a SubscribeWithACL held aclTransitionMu -- not serialized (resolve-then-register TOCTOU)")
	case <-time.After(150 * time.Millisecond):
		// Good: the transition is blocked on aclTransitionMu behind the subscribe.
	}

	close(release)
	res := <-resCh
	require.NoError(t, res.err)
	defer res.unsub()
	<-commitDone

	assert.False(t, subR.Filter.IsAllowed("w1"),
		"the serialized unshare must revoke w1 from the just-registered subscriber -- no leak")
}

// TestSubscribeWithACL_ResolveErrorLeavesUnregistered verifies a resolve failure
// is returned WITHOUT registering the subscriber (no unsub handle, no snapshot),
// so the caller can reject the connection before streaming.
func TestSubscribeWithACL_ResolveErrorLeavesUnregistered(t *testing.T) {
	mgr, _, _ := runManager(t, "org", allowAll{}, 240_000)
	seedRootInternal(t, mgr, "w1", "root1")

	capR := &captureSubscriber{}
	subR := &crdt.Subscriber{UserID: "userR", Send: capR.send}
	wantErr := errors.New("resolve failed")
	initial, unsub, err := mgr.SubscribeWithACL(subR, func() (map[string]bool, error) {
		return nil, wantErr
	})
	require.ErrorIs(t, err, wantErr)
	assert.Nil(t, initial, "no snapshot on a failed resolve")
	assert.Nil(t, unsub, "no unsub handle on a failed resolve")
}

// TestCommitWorkspaceAccessTransition_RevokesBeforeCommit_GrantsAfter pins the
// phase ordering the refactor relies on for its no-stall-without-leak guarantee:
// a revoke is applied to the in-memory filter BEFORE the DB commit (so a
// just-revoked reader stops receiving the workspace's ops before the unshare is
// even durable -- no leak, without holding the projection lock across the
// commit), while a grant is deferred until AFTER the commit (so a grant that
// never persists cannot leak the workspace's ops).
func TestCommitWorkspaceAccessTransition_RevokesBeforeCommit_GrantsAfter(t *testing.T) {
	mgr, _, _ := runManager(t, "org", allowAll{}, 200_000)
	seedRootInternal(t, mgr, "w1", "root1")

	// R currently sees w1 and will be unshared; G currently does not and will be shared.
	capR := &captureSubscriber{}
	subR := &crdt.Subscriber{UserID: "userR", Filter: crdt.SubscriberFilter{WorkspaceIDs: map[string]bool{"w1": true}}, Send: capR.send}
	_, unsubR := mgr.Subscribe(subR)
	defer unsubR()
	capG := &captureSubscriber{}
	subG := &crdt.Subscriber{UserID: "userG", Filter: crdt.SubscriberFilter{WorkspaceIDs: map[string]bool{}}, Send: capG.send}
	_, unsubG := mgr.Subscribe(subG)
	defer unsubG()

	var revokedAtCommit, grantedAtCommit int
	commit := func() error {
		_, revokedAtCommit = countWorkspaceProjectionEvents(capR.snapshot(), "w1")
		grantedAtCommit, _ = countWorkspaceProjectionEvents(capG.snapshot(), "w1")
		return nil
	}
	// New reader set: only userG (userR is being unshared).
	err := mgr.CommitWorkspaceAccessTransition("w1", map[string]struct{}{"userG": {}}, commit)
	require.NoError(t, err)

	assert.Equal(t, 1, revokedAtCommit, "revoke must be delivered BEFORE the commit (pre-commit phase)")
	assert.Equal(t, 0, grantedAtCommit, "grant must NOT be delivered before the commit (deferred to post-commit)")

	_, rRevoked := countWorkspaceProjectionEvents(capR.snapshot(), "w1")
	gGranted, _ := countWorkspaceProjectionEvents(capG.snapshot(), "w1")
	assert.Equal(t, 1, rRevoked, "R ends up revoked exactly once")
	assert.Equal(t, 1, gGranted, "G ends up granted exactly once")
	assert.False(t, subR.Filter.IsAllowed("w1"), "R's filter must no longer admit w1")
	assert.True(t, subG.Filter.IsAllowed("w1"), "G's filter must now admit w1")
}

// TestCommitWorkspaceAccessTransition_RollsBackRevokesOnCommitFailure verifies
// that when the DB commit fails, the premature Phase 1 revokes are undone (the
// change never persisted, so those readers keep access) and no grant is applied.
func TestCommitWorkspaceAccessTransition_RollsBackRevokesOnCommitFailure(t *testing.T) {
	mgr, _, _ := runManager(t, "org", allowAll{}, 210_000)
	seedRootInternal(t, mgr, "w1", "root1")

	capR := &captureSubscriber{}
	subR := &crdt.Subscriber{UserID: "userR", Filter: crdt.SubscriberFilter{WorkspaceIDs: map[string]bool{"w1": true}}, Send: capR.send}
	_, unsubR := mgr.Subscribe(subR)
	defer unsubR()
	capG := &captureSubscriber{}
	subG := &crdt.Subscriber{UserID: "userG", Filter: crdt.SubscriberFilter{WorkspaceIDs: map[string]bool{}}, Send: capG.send}
	_, unsubG := mgr.Subscribe(subG)
	defer unsubG()

	wantErr := errors.New("db commit failed")
	err := mgr.CommitWorkspaceAccessTransition("w1", map[string]struct{}{"userG": {}}, func() error { return wantErr })
	require.ErrorIs(t, err, wantErr)

	// R was revoked in Phase 1, then re-granted by the rollback: net access kept.
	rGranted, rRevoked := countWorkspaceProjectionEvents(capR.snapshot(), "w1")
	assert.Equal(t, 1, rRevoked, "R got the premature revoke")
	assert.Equal(t, 1, rGranted, "R was re-granted by the rollback")
	assert.True(t, subR.Filter.IsAllowed("w1"), "R's filter must still admit w1 after rollback")

	// G was only a grant candidate; a failed commit must never grant it.
	gGranted, _ := countWorkspaceProjectionEvents(capG.snapshot(), "w1")
	assert.Equal(t, 0, gGranted, "a failed commit must not grant access")
	assert.False(t, subG.Filter.IsAllowed("w1"), "G must not gain w1 on a failed commit")
}
