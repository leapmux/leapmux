package crdt_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/crdt"
)

// blockingReadChecker lets a test park ExpandSubscribersForWorkspace inside its
// batch read-ACL lookup -- while the expand holds subscribeExpandMu -- to probe
// serialization against SubscribeWithACL. It blocks only once armed, so
// manager setup and workspace seeding never stall on it.
type blockingReadChecker struct {
	armed   atomic.Bool
	reached chan struct{}
	release chan struct{}
	once    sync.Once
}

func (*blockingReadChecker) CanAccessWorkspace(_ context.Context, _, _ string) (bool, error) {
	return true, nil
}
func (*blockingReadChecker) CanUseWorker(_ context.Context, _, _ string) (bool, error) {
	return true, nil
}
func (b *blockingReadChecker) CanAccessWorkspaceForUsers(_ context.Context, _ string, userIDs []string) (map[string]bool, error) {
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

// TestResumeWithACL_SerializesWithExpand pins the resolve-then-register
// TOCTOU fix: SubscribeWithACL holds subscribeExpandMu across resolve+register,
// so a concurrent workspace-create expansion for the same user must block behind
// it. Once the subscriber is registered (with a pre-create filter that does
// not admit w1) the now-unblocked expand visits it and adds w1 -- without the
// serialization the expand could run entirely in the resolve/register gap,
// miss the not-yet-registered subscriber, and the subscriber would never see
// the new workspace until reconnect.
func TestResumeWithACL_SerializesWithExpand(t *testing.T) {
	mgr, _, _ := runManager(t, "user-1", allowAll{}, 230_000)
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
		// The resolve runs while SubscribeWithACL holds subscribeExpandMu;
		// parking here parks the whole resolve+register under that lock. A nil
		// cursor exercises the FALLBACK arm, which takes the same lock path as a
		// full-snapshot subscribe (the legacy SubscribeWithACL path this test
		// was written against).
		out, err := mgr.SubscribeWithACL(context.Background(), subR, nil, 0, func() (map[string]bool, error) {
			close(reached)
			<-release
			// Resolved BEFORE w1's create expansion: does not admit w1.
			return map[string]bool{}, nil
		})
		resCh <- subResult{out.Unsub(), err}
	}()
	<-reached // subscribe is parked in resolve, holding subscribeExpandMu

	expandStarted := make(chan struct{})
	expandDone := make(chan struct{})
	go func() {
		close(expandStarted)
		_ = mgr.ExpandSubscribersForWorkspace(context.Background(), "w1")
		close(expandDone)
	}()
	<-expandStarted

	select {
	case <-expandDone:
		t.Fatal("expand completed while a SubscribeWithACL held subscribeExpandMu -- not serialized (resolve-then-register TOCTOU)")
	case <-time.After(150 * time.Millisecond):
		// Good: the expand is blocked on subscribeExpandMu behind the subscribe.
	}

	close(release)
	res := <-resCh
	require.NoError(t, res.err)
	defer res.unsub()
	<-expandDone

	assert.True(t, subR.Filter.IsAllowed("w1"),
		"the serialized expand must visit the just-registered subscriber and admit w1 -- no missed workspace")
}

// TestExpandSubscribersForWorkspace_SerializesWithSubscribe is the mirror
// ordering: an in-flight expand (parked in its read-ACL lookup while holding
// subscribeExpandMu) must block a concurrent SubscribeWithACL until it
// finishes, so the subscriber's resolve reads the post-expand state rather
// than racing it.
func TestExpandSubscribersForWorkspace_SerializesWithSubscribe(t *testing.T) {
	checker := &blockingReadChecker{reached: make(chan struct{}), release: make(chan struct{})}
	mgr, _, _ := runManager(t, "user-1", checker, 220_000)
	seedRootInternal(t, mgr, "w1", "root1")

	// R is registered with a filter that does not admit w1 -- an expand candidate.
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
	<-checker.reached // expand is parked in the read-ACL lookup, holding subscribeExpandMu

	// A concurrent SubscribeWithACL must serialize behind the in-flight expand:
	// its resolve must NOT run while the expand is parked.
	resolveStarted := make(chan struct{})
	subDone := make(chan struct{})
	capS := &captureSubscriber{}
	subS := &crdt.Subscriber{UserID: "userS", Send: capS.send}
	go func() {
		out, err := mgr.SubscribeWithACL(context.Background(), subS, nil, 0, func() (map[string]bool, error) {
			close(resolveStarted)
			return map[string]bool{"w1": true}, nil
		})
		if err == nil {
			defer out.Unsub()()
		}
		close(subDone)
	}()

	select {
	case <-resolveStarted:
		t.Fatal("SubscribeWithACL resolve ran concurrently with an in-flight expand -- subscribeExpandMu is not serializing them")
	case <-time.After(150 * time.Millisecond):
		// Good: the subscribe is blocked on subscribeExpandMu.
	}

	// Release the expand: it admits w1 into R's filter, then the unblocked
	// subscribe registers with its own post-expand-resolved filter.
	close(checker.release)
	<-expandDone
	<-subDone

	assert.True(t, subR.Filter.IsAllowed("w1"),
		"the expand must have admitted w1 into the pre-registered subscriber's filter")
	assert.True(t, subS.Filter.IsAllowed("w1"),
		"the serialized subscriber's resolved filter must admit w1")
}

// TestContractSubscribersForWorkspace_SerializesWithSubscribe pins the
// phantom-filter-key fix: contractSubscribersForWorkspace (workspace
// delete/rollback) now holds subscribeExpandMu, the SAME lock SubscribeWithACL
// holds across resolve+register. Without it, a subscriber whose resolve() read
// the pre-delete ACL (W present) but which registered after the contract ran
// would keep W as a stale filter key no pass ever removes. So a contract must
// block behind an in-flight SubscribeWithACL that is parked in resolve.
func TestContractSubscribersForWorkspace_SerializesWithSubscribe(t *testing.T) {
	mgr, _, _ := runManager(t, "user-1", allowAll{}, 210_000)
	seedRootInternal(t, mgr, "w1", "root1")

	reached := make(chan struct{})
	release := make(chan struct{})
	capS := &captureSubscriber{}
	subS := &crdt.Subscriber{UserID: "userS", Send: capS.send}

	type subResult struct {
		unsub func()
		err   error
	}
	resCh := make(chan subResult, 1)
	go func() {
		// Parking in resolve holds subscribeExpandMu across the whole
		// resolve+register, mirroring a subscriber whose resolve read the
		// pre-delete ACL.
		out, err := mgr.SubscribeWithACL(context.Background(), subS, nil, 0, func() (map[string]bool, error) {
			close(reached)
			<-release
			return map[string]bool{"w1": true}, nil
		})
		resCh <- subResult{out.Unsub(), err}
	}()
	<-reached // subscribe parked in resolve, holding subscribeExpandMu

	contractDone := make(chan struct{})
	go func() {
		mgr.ContractSubscribersForWorkspaceForTest("w1")
		close(contractDone)
	}()

	select {
	case <-contractDone:
		t.Fatal("contractSubscribersForWorkspace ran while a SubscribeWithACL held subscribeExpandMu -- not serialized (phantom-filter-key race)")
	case <-time.After(150 * time.Millisecond):
		// Good: the contract is blocked on subscribeExpandMu behind the subscribe.
	}

	close(release)
	res := <-resCh
	require.NoError(t, res.err)
	// Keep the subscription registered until after the assertion: unsubscribing
	// here would race the contract and remove subS before MutateEach visits it.
	defer res.unsub()
	<-contractDone

	// The serialized contract ran after the subscribe registered, so it removed
	// w1 from the just-registered subscriber's filter -- no phantom key survives.
	assert.False(t, subS.Filter.IsAllowed("w1"),
		"the serialized contract must strip w1 from the registered subscriber's filter")
}

// TestResumeWithACL_ResolveErrorLeavesUnregistered verifies a resolve failure
// is returned WITHOUT registering the subscriber (no delta, no fallback
// snapshot, no unsub handle), so the caller can reject the connection before
// streaming.
func TestResumeWithACL_ResolveErrorLeavesUnregistered(t *testing.T) {
	mgr, _, _ := runManager(t, "user-1", allowAll{}, 240_000)
	seedRootInternal(t, mgr, "w1", "root1")

	capR := &captureSubscriber{}
	subR := &crdt.Subscriber{UserID: "userR", Send: capR.send}
	wantErr := errors.New("resolve failed")
	out, err := mgr.SubscribeWithACL(context.Background(), subR, nil, 0, func() (map[string]bool, error) {
		return nil, wantErr
	})
	require.ErrorIs(t, err, wantErr)
	// A failed resolve never registers, so there is no live handle. Unsub()
	// returns a no-op (not nil) so a caller that defers it before checking err
	// is safe; calling it here must not panic.
	assert.NotPanics(t, func() { out.Unsub()() }, "Unsub on a failed-resolve outcome must be a safe no-op")
}

// TestSubscribeWithACL_ResumeFilterImmutableNoRaceWithExpand is a concurrency
// smoke guard: expand/contract REPLACE the whole Filter via WithWorkspace /
// WithoutWorkspace (never mutate WorkspaceIDs in place), so a plain value copy
// of SubscriberFilter handed to buildResumeDelta is safe to read while
// Expand/Contract replace the live Filter under subscribeExpandMu + the
// controller lock. In-place map mutation would race the resume's IsAllowed
// reads — a Go runtime fatal "concurrent map read and map write".
//
// This test exercises the concurrent path (resume + churn) so a regression that
// re-introduces in-place filter mutation has a chance to trip the race
// detector; it is not a deterministic reproducer (the in-memory fake journal's
// frame-build is fast, so the overlap window is narrow), but it widens the
// window with a deep tail and many iterations.
func TestSubscribeWithACL_ResumeFilterImmutableNoRaceWithExpand(t *testing.T) {
	mgr, _, _ := runManager(t, "user-1", allowAll{}, 250_000)
	seedRootInternal(t, mgr, "w1", "root1")
	seedRootInternal(t, mgr, "w2", "root2")
	epoch := mgr.Materialized(crdt.SubscriberFilter{}).GetCurrentEpoch()

	// Seed a deep tail of stable-visibility ops on w1 so each resume's frame
	// build iterates many filter lookups, widening the overlap window.
	for i := 0; i < 200; i++ {
		submitNodePositionBatch(t, mgr, fmt.Sprintf("tail-%d", i), "root1", fmt.Sprintf("p%d", i))
	}
	cursor := mgr.Materialized(crdt.SubscriberFilter{}).GetMaxHlc()
	require.NotNil(t, cursor)

	resolveOnlyW1 := func() (map[string]bool, error) { return map[string]bool{"w1": true}, nil }

	// Hammer expand/contract on w2 while repeatedly resuming. The resume reads
	// an immutable Filter value copy, so live WithWorkspace/WithoutWorkspace
	// replacements must not race the scan's reads. In-place map mutation would
	// make `go test -race` report a data race on WorkspaceIDs within a few iterations.
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_ = mgr.ExpandSubscribersForWorkspace(context.Background(), "w2")
				mgr.ContractSubscribersForWorkspaceForTest("w2")
			}
		}
	}()

	// Many short resumes so the race detector reliably observes the overlap.
	for i := 0; i < 20; i++ {
		cap := &captureSubscriber{}
		sub := &crdt.Subscriber{UserID: "user-1", Send: cap.send}
		out, err := mgr.SubscribeWithACL(context.Background(), sub, crdt.HLCClone(cursor), epoch, resolveOnlyW1)
		require.NoError(t, err, "resume must complete cleanly despite concurrent filter mutation")
		require.NotNil(t, out.Unsub())
		out.Unsub()()
	}
	close(stop)
	wg.Wait()
}
