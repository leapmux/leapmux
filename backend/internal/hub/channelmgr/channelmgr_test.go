package channelmgr

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
)

var noopSender = func(*leapmuxv1.ChannelMessage) error { return nil }

func bindChannelConn(t testing.TB, m *Manager, channelID, connID string) bool {
	t.Helper()
	// UseAuthorizedChannel with a nil operation binds the conn and reports
	// liveness. Callers must BindUser the target conn first — first-bind
	// refuses when the conn is not registered.
	_, ok, _ := m.UseAuthorizedChannel(channelID, connID, nil, nil)
	if !ok {
		if info, exists := m.GetChannelInfo(channelID); exists {
			t.Fatalf("bindChannelConn(%s, %s) failed; BindUser(%s, %s, ...) must run first (ConnID=%q)",
				channelID, connID, info.UserID, connID, info.ConnID)
		}
	}
	return ok
}

func TestRegisterAndExists(t *testing.T) {
	m := New(0)
	assert.False(t, m.Exists("ch1"))

	require.True(t, m.RegisterWithAuthInfo("ch1", "w1", "u1", AuthInfo{}, nil))
	assert.True(t, m.Exists("ch1"))
	info, ok := m.GetChannelInfo("ch1")
	require.True(t, ok)
	assert.Equal(t, "w1", info.WorkerID)
	assert.Equal(t, "u1", info.UserID)
}

// TestRegisterWithAuthInfo_RejectsDuplicateID ensures a second register for the
// same channelID is refused and leaves the original entry (and reverse indexes)
// untouched — previously an overwrite orphaned the old worker index.
func TestRegisterWithAuthInfo_RejectsDuplicateID(t *testing.T) {
	m := New(0)
	require.True(t, m.RegisterWithAuthInfo("ch1", "w1", "u1", AuthInfo{}, nil))

	assert.False(t, m.RegisterWithAuthInfo("ch1", "w2", "u2", AuthInfo{}, nil),
		"duplicate channelID must be rejected")

	info, ok := m.GetChannelInfo("ch1")
	require.True(t, ok)
	assert.Equal(t, "w1", info.WorkerID)
	assert.Equal(t, "u1", info.UserID)

	assert.Empty(t, m.UnregisterByWorker("w2"), "rejected register must not index the new worker")
	removed := m.UnregisterByWorker("w1")
	require.Equal(t, []string{"ch1"}, removed)
	assert.False(t, m.Exists("ch1"))
}

// TestRegisterWithAuthInfo_AllowsReuseAfterClose: once CloseByID removes the
// map entry, the same channelID may be registered again.
func TestRegisterWithAuthInfo_AllowsReuseAfterClose(t *testing.T) {
	m := New(0)
	require.True(t, m.RegisterWithAuthInfo("ch1", "w1", "u1", AuthInfo{}, nil))
	m.CloseByID("ch1")
	assert.False(t, m.Exists("ch1"))

	require.True(t, m.RegisterWithAuthInfo("ch1", "w2", "u2", AuthInfo{}, nil))
	info, ok := m.GetChannelInfo("ch1")
	require.True(t, ok)
	assert.Equal(t, "w2", info.WorkerID)
	assert.Equal(t, "u2", info.UserID)
}

func TestUnregister(t *testing.T) {
	m := New(0)
	cancelled := false
	m.RegisterWithAuthInfo("ch1", "w1", "u1", AuthInfo{}, func() { cancelled = true })

	m.CloseByID("ch1")
	assert.False(t, m.Exists("ch1"))
	assert.True(t, cancelled)
}

func TestUnregisterByWorker(t *testing.T) {
	m := New(0)
	var cancelledIDs []string
	var mu sync.Mutex

	for _, id := range []string{"ch1", "ch2", "ch3"} {
		channelID := id
		m.RegisterWithAuthInfo(channelID, "w1", "u1", AuthInfo{}, func() {
			mu.Lock()
			cancelledIDs = append(cancelledIDs, channelID)
			mu.Unlock()
		})
	}
	m.RegisterWithAuthInfo("ch4", "w2", "u2", AuthInfo{}, nil) // Different worker.

	removed := m.UnregisterByWorker("w1")
	assert.Len(t, removed, 3)
	assert.True(t, m.Exists("ch4"))
	assert.False(t, m.Exists("ch1"))
	assert.False(t, m.Exists("ch2"))
	assert.False(t, m.Exists("ch3"))
	assert.Len(t, cancelledIDs, 3)
}

func TestGetWorkerIDNonexistent(t *testing.T) {
	m := New(0)
	_, ok := m.GetChannelInfo("nonexistent")
	assert.False(t, ok)
}

func TestChannelConnBinding_TargetedRouting(t *testing.T) {
	m := New(0)
	m.RegisterWithAuthInfo("ch1", "w1", "u1", AuthInfo{}, nil)

	// Two connections for the same user.
	var received1, received2 []*leapmuxv1.ChannelMessage
	m.BindUser("u1", "conn1", func(msg *leapmuxv1.ChannelMessage) error {
		received1 = append(received1, msg)
		return nil
	}, nil)
	m.BindUser("u1", "conn2", func(msg *leapmuxv1.ChannelMessage) error {
		received2 = append(received2, msg)
		return nil
	}, nil)

	// Associate ch1 with conn1.
	assert.True(t, bindChannelConn(t, m, "ch1", "conn1"))

	// SendToFrontend should only go to conn1.
	msg := &leapmuxv1.ChannelMessage{ChannelId: "ch1", Ciphertext: []byte("targeted")}
	assert.True(t, m.SendToFrontend(msg))

	assert.Len(t, received1, 1)
	assert.Len(t, received2, 0) // conn2 should NOT receive it.
	assert.Equal(t, []byte("targeted"), received1[0].GetCiphertext())
}

func TestChannelConnBinding_Nonexistent(t *testing.T) {
	m := New(0)
	assert.False(t, bindChannelConn(t, m, "nonexistent", "conn1"))
}

func TestSendToFrontend_NoConnID(t *testing.T) {
	m := New(0)
	m.RegisterWithAuthInfo("ch1", "w1", "u1", AuthInfo{}, nil)

	// Channel without a bound conn — SendToFrontend should return false
	// because there's no route. In practice this never happens because the
	// worker only responds to frontend-initiated requests, and UseAuthorizedChannel
	// is called when the relay processes the first frontend→worker message.
	m.BindUser("u1", "conn1", func(msg *leapmuxv1.ChannelMessage) error {
		return nil
	}, nil)

	msg := &leapmuxv1.ChannelMessage{ChannelId: "ch1", Ciphertext: []byte("no-route")}
	assert.False(t, m.SendToFrontend(msg))
}

func TestSendToFrontend_WithConnID(t *testing.T) {
	m := New(0)
	m.RegisterWithAuthInfo("ch1", "w1", "u1", AuthInfo{}, nil)

	var received []*leapmuxv1.ChannelMessage
	m.BindUser("u1", "conn1", func(msg *leapmuxv1.ChannelMessage) error {
		received = append(received, msg)
		return nil
	}, nil)

	bindChannelConn(t, m, "ch1", "conn1")

	msg := &leapmuxv1.ChannelMessage{ChannelId: "ch1", Ciphertext: []byte("routed")}
	assert.True(t, m.SendToFrontend(msg))
	assert.Len(t, received, 1)

	// Also works when a new channel is registered and associated.
	m.RegisterWithAuthInfo("ch2", "w1", "u1", AuthInfo{}, nil)
	bindChannelConn(t, m, "ch2", "conn1")

	msg2 := &leapmuxv1.ChannelMessage{ChannelId: "ch2", Ciphertext: []byte("also-routed")}
	assert.True(t, m.SendToFrontend(msg2))
	assert.Len(t, received, 2)
}

func TestSendToFrontendOrdersCloseAfterInFlightMessage(t *testing.T) {
	m := New(0)
	m.RegisterWithAuthInfo("ch1", "w1", "u1", AuthInfo{}, nil)
	started := make(chan struct{})
	release := make(chan struct{})
	var mu sync.Mutex
	var flags []leapmuxv1.ChannelMessageFlags
	m.BindUser("u1", "conn1", func(msg *leapmuxv1.ChannelMessage) error {
		if msg.GetFlags() != leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_CLOSE {
			close(started)
			<-release
		}
		mu.Lock()
		flags = append(flags, msg.GetFlags())
		mu.Unlock()
		return nil
	}, nil)
	bindChannelConn(t, m, "ch1", "conn1")

	sendDone := make(chan bool, 1)
	go func() {
		sendDone <- m.SendToFrontend(&leapmuxv1.ChannelMessage{ChannelId: "ch1", Ciphertext: []byte("data")})
	}()
	<-started
	closeDone := make(chan struct{})
	go func() {
		m.CloseByID("ch1")
		close(closeDone)
	}()
	select {
	case <-closeDone:
		t.Fatal("channel close crossed an in-flight worker-to-frontend send")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	assert.True(t, <-sendDone)
	<-closeDone

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, flags, 2)
	assert.Equal(t, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED, flags[0])
	assert.Equal(t, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_CLOSE, flags[1])
}

func TestCloseByIDDoesNotBlockUnrelatedManagerOperations(t *testing.T) {
	m := New(0)
	m.RegisterWithAuthInfo("blocked", "w1", "u1", AuthInfo{}, nil)
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	m.BindUser("u1", "conn1", func(msg *leapmuxv1.ChannelMessage) error {
		if msg.GetFlags() == leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_CLOSE {
			return nil
		}
		once.Do(func() { close(started) })
		<-release
		return nil
	}, nil)
	bindChannelConn(t, m, "blocked", "conn1")
	go m.SendToFrontend(&leapmuxv1.ChannelMessage{ChannelId: "blocked"})
	<-started
	closeDone := make(chan struct{})
	go func() {
		m.CloseByID("blocked")
		close(closeDone)
	}()

	registered := make(chan struct{})
	go func() {
		m.RegisterWithAuthInfo("unrelated", "w2", "u2", AuthInfo{}, nil)
		close(registered)
	}()
	select {
	case <-registered:
	case <-time.After(time.Second):
		t.Fatal("unrelated registration blocked behind channel operation lock")
	}
	close(release)
	<-closeDone
}

func TestCloseByIDIfLeavesUnauthorizedChannelOpen(t *testing.T) {
	m := New(0)
	m.RegisterWithAuthInfo("ch1", "w1", "owner", AuthInfo{}, nil)

	closed := m.CloseByIDIf("ch1", func(info ChannelInfo) bool {
		return info.UserID == "attacker"
	})

	assert.Empty(t, closed)
	assert.True(t, m.Exists("ch1"))
}

func TestUseAuthorizedChannelOrdersCloseAfterInFlightOperation(t *testing.T) {
	m := New(0)
	m.RegisterWithAuthInfo("ch1", "w1", "u1", AuthInfo{}, nil)
	m.BindUser("u1", "conn1", noopSender, nil)
	started := make(chan struct{})
	release := make(chan struct{})
	operationDone := make(chan struct{})
	go func() {
		_, ok, err := m.UseAuthorizedChannel("ch1", "conn1", nil, func(ChannelInfo) error {
			close(started)
			<-release
			return nil
		})
		assert.True(t, ok)
		require.NoError(t, err)
		close(operationDone)
	}()
	<-started

	closeDone := make(chan struct{})
	go func() {
		m.CloseByID("ch1")
		close(closeDone)
	}()
	select {
	case <-closeDone:
		t.Fatal("channel close crossed an in-flight frontend-to-worker operation")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	<-operationDone
	<-closeDone
	assert.False(t, m.Exists("ch1"))
}

// waitOrDeadlock runs fn in a goroutine and fails the test if it does not
// return within the timeout -- used to prove a panic did not leak a lock that a
// later manager operation blocks on.
func waitOrDeadlock(t *testing.T, msg string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		fn()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal(msg)
	}
}

func TestUseChannelOperationPanicReleasesChannelOpLock(t *testing.T) {
	m := New(0)
	m.RegisterWithAuthInfo("ch1", "w1", "u1", AuthInfo{}, nil)

	// A panic in the operation callback (in production, the worker-open
	// SendAndWait) must not leave the channel's opMu held: teardown re-acquires
	// it, so a leaked opMu would make revocation unable to close the channel.
	func() {
		defer func() { _ = recover() }()
		_, _, _ = m.UseChannelIf("ch1", nil, func(ChannelInfo) error {
			panic("boom in operation")
		})
	}()

	waitOrDeadlock(t, "CloseByID blocked: operation panic leaked the channel op lock", func() {
		m.CloseByID("ch1")
	})
	assert.False(t, m.Exists("ch1"))
}

func TestUseChannelAuthorizePanicReleasesManagerLock(t *testing.T) {
	m := New(0)
	m.RegisterWithAuthInfo("ch1", "w1", "u1", AuthInfo{}, nil)

	// authorize runs under the manager-wide m.mu; a panic there must not freeze
	// every other manager operation for every channel.
	func() {
		defer func() { _ = recover() }()
		_, _, _ = m.UseChannelIf("ch1", func(ChannelInfo) bool {
			panic("boom in authorize")
		}, nil)
	}()

	waitOrDeadlock(t, "Register blocked: authorize panic leaked the manager lock", func() {
		m.RegisterWithAuthInfo("ch2", "w2", "u2", AuthInfo{}, nil)
	})
	// The channel's opMu must also be free for teardown.
	waitOrDeadlock(t, "CloseByID blocked: authorize panic leaked the channel op lock", func() {
		m.CloseByID("ch1")
	})
}

func TestCloseByIDIfPredicatePanicReleasesLocks(t *testing.T) {
	m := New(0)
	m.RegisterWithAuthInfo("ch1", "w1", "u1", AuthInfo{}, nil)

	// The teardown predicate runs under both opMu and m.mu; a panic must leak
	// neither, or the manager deadlocks on the next operation.
	func() {
		defer func() { _ = recover() }()
		_ = m.CloseByIDIf("ch1", func(ChannelInfo) bool {
			panic("boom in predicate")
		})
	}()

	waitOrDeadlock(t, "manager op blocked: teardown predicate panic leaked a lock", func() {
		m.RegisterWithAuthInfo("ch2", "w2", "u2", AuthInfo{}, nil)
		m.CloseByID("ch1")
	})
}

func TestSendToFrontendIfAuthorizePanicReleasesLocks(t *testing.T) {
	m := New(0)
	m.RegisterWithAuthInfo("ch1", "w1", "u1", AuthInfo{}, nil)

	// The relay authorize predicate runs under opMu + m.mu.RLock; a panic must
	// leak neither, or teardown (which re-acquires opMu) can no longer close the
	// channel.
	func() {
		defer func() { _ = recover() }()
		m.SendToFrontendIf(&leapmuxv1.ChannelMessage{ChannelId: "ch1"}, func(ChannelInfo) bool {
			panic("boom in authorize")
		})
	}()

	waitOrDeadlock(t, "CloseByID blocked: SendToFrontendIf authorize panic leaked a lock", func() {
		m.CloseByID("ch1")
	})
}

func TestMultipleConnections_EachChannelTargeted(t *testing.T) {
	m := New(0)
	m.RegisterWithAuthInfo("ch1", "w1", "u1", AuthInfo{}, nil)
	m.RegisterWithAuthInfo("ch2", "w1", "u1", AuthInfo{}, nil)

	// Two connections for the same user, each owning a different channel.
	var received1, received2 []*leapmuxv1.ChannelMessage
	m.BindUser("u1", "conn1", func(msg *leapmuxv1.ChannelMessage) error {
		received1 = append(received1, msg)
		return nil
	}, nil)
	m.BindUser("u1", "conn2", func(msg *leapmuxv1.ChannelMessage) error {
		received2 = append(received2, msg)
		return nil
	}, nil)

	// ch1 -> conn1, ch2 -> conn2
	bindChannelConn(t, m, "ch1", "conn1")
	bindChannelConn(t, m, "ch2", "conn2")

	// Messages route to correct connection only.
	msg1 := &leapmuxv1.ChannelMessage{ChannelId: "ch1", Ciphertext: []byte("for-conn1")}
	assert.True(t, m.SendToFrontend(msg1))
	msg2 := &leapmuxv1.ChannelMessage{ChannelId: "ch2", Ciphertext: []byte("for-conn2")}
	assert.True(t, m.SendToFrontend(msg2))

	assert.Len(t, received1, 1)
	assert.Len(t, received2, 1)
	assert.Equal(t, "ch1", received1[0].GetChannelId())
	assert.Equal(t, "ch2", received2[0].GetChannelId())
}

func TestUnbindUser_KeepsOtherConnection(t *testing.T) {
	m := New(0)
	m.RegisterWithAuthInfo("ch1", "w1", "u1", AuthInfo{}, nil)

	var received1, received2 []*leapmuxv1.ChannelMessage
	m.BindUser("u1", "conn1", func(msg *leapmuxv1.ChannelMessage) error {
		received1 = append(received1, msg)
		return nil
	}, nil)
	m.BindUser("u1", "conn2", func(msg *leapmuxv1.ChannelMessage) error {
		received2 = append(received2, msg)
		return nil
	}, nil)

	bindChannelConn(t, m, "ch1", "conn2")

	// Unbind conn1 — conn2 should still work.
	m.UnbindUser("u1", "conn1")

	msg := &leapmuxv1.ChannelMessage{ChannelId: "ch1", Ciphertext: []byte("after-unbind")}
	assert.True(t, m.SendToFrontend(msg))

	assert.Len(t, received1, 0)
	assert.Len(t, received2, 1)
}

func TestUnbindUser_LastConnection(t *testing.T) {
	m := New(0)
	m.RegisterWithAuthInfo("ch1", "w1", "u1", AuthInfo{}, nil)

	m.BindUser("u1", "conn1", func(msg *leapmuxv1.ChannelMessage) error {
		return nil
	}, nil)
	bindChannelConn(t, m, "ch1", "conn1")

	// Verify bound.
	msg := &leapmuxv1.ChannelMessage{ChannelId: "ch1", Ciphertext: []byte("hi")}
	assert.True(t, m.SendToFrontend(msg))

	// Unbind last connection.
	m.UnbindUser("u1", "conn1")

	// SendToFrontend should fail — no connections left.
	assert.False(t, m.SendToFrontend(msg))

	// Channel should still exist.
	assert.True(t, m.Exists("ch1"))
}

func TestUnbindUser_CallsCancel(t *testing.T) {
	m := New(0)
	cancelled := false
	m.BindUser("u1", "conn1", func(msg *leapmuxv1.ChannelMessage) error {
		return nil
	}, func() { cancelled = true })

	m.UnbindUser("u1", "conn1")
	assert.True(t, cancelled)
}

func TestUnregisterByWorker_DoesNotCloseUserSender(t *testing.T) {
	m := New(0)

	userCancelled := false
	m.BindUser("u1", "conn1", func(msg *leapmuxv1.ChannelMessage) error {
		return nil
	}, func() { userCancelled = true })

	m.RegisterWithAuthInfo("ch1", "w1", "u1", AuthInfo{}, nil)
	m.RegisterWithAuthInfo("ch2", "w1", "u1", AuthInfo{}, nil)

	// UnregisterByWorker removes channels but should NOT cancel user sender.
	removed := m.UnregisterByWorker("w1")
	assert.Len(t, removed, 2)
	assert.False(t, userCancelled)

	// New channels for same user should still work after conn binding.
	m.RegisterWithAuthInfo("ch3", "w2", "u1", AuthInfo{}, nil)
	bindChannelConn(t, m, "ch3", "conn1")
	msg := &leapmuxv1.ChannelMessage{ChannelId: "ch3", Ciphertext: []byte("still works")}
	assert.True(t, m.SendToFrontend(msg))
}

func TestUnregister_SendsCloseNotification(t *testing.T) {
	m := New(0)

	var received []*leapmuxv1.ChannelMessage
	m.BindUser("u1", "conn1", func(msg *leapmuxv1.ChannelMessage) error {
		received = append(received, msg)
		return nil
	}, nil)

	m.RegisterWithAuthInfo("ch1", "w1", "u1", AuthInfo{}, nil)
	bindChannelConn(t, m, "ch1", "conn1")

	// Unregister should send a close notification (empty ciphertext) to conn1.
	m.CloseByID("ch1")

	if assert.Len(t, received, 1) {
		assert.Equal(t, "ch1", received[0].GetChannelId())
		assert.Empty(t, received[0].GetCiphertext()) // Empty = close notification.
	}
}

func TestUnregister_CloseNotification_NoConnID(t *testing.T) {
	m := New(0)

	// Channel without a bound conn — close notification cannot be sent
	// because we don't know which connection to target. This is acceptable;
	// the channel was never associated, so no frontend is waiting for it.
	var received []*leapmuxv1.ChannelMessage
	m.BindUser("u1", "conn1", func(msg *leapmuxv1.ChannelMessage) error {
		received = append(received, msg)
		return nil
	}, nil)

	m.RegisterWithAuthInfo("ch1", "w1", "u1", AuthInfo{}, nil)
	// No bound conn -- close notification goes nowhere.
	m.CloseByID("ch1")

	assert.Len(t, received, 0)
}

func TestUnbindUserAndCleanup_RemovesBoundAndUnboundChannels(t *testing.T) {
	m := New(0)

	m.BindUser("u1", "conn1", noopSender, nil)

	var boundCancelled, unboundCancelled bool
	m.RegisterWithAuthInfo("ch_bound", "w1", "u1", AuthInfo{}, func() { boundCancelled = true })
	bindChannelConn(t, m, "ch_bound", "conn1")
	m.RegisterWithAuthInfo("ch_unbound", "w1", "u1", AuthInfo{}, func() { unboundCancelled = true })
	m.RegisterWithAuthInfo("ch_other_user", "w1", "u2", AuthInfo{}, nil)

	removed := m.UnbindUserAndCleanup("u1", "conn1")

	assert.Len(t, removed, 2)
	closedIDs := map[string]bool{}
	for _, cc := range removed {
		closedIDs[cc.ChannelID] = true
		assert.Equal(t, "w1", cc.WorkerID)
	}
	assert.True(t, closedIDs["ch_bound"])
	assert.True(t, closedIDs["ch_unbound"])
	assert.True(t, boundCancelled)
	assert.True(t, unboundCancelled)
	assert.False(t, m.Exists("ch_bound"))
	assert.False(t, m.Exists("ch_unbound"))
	assert.True(t, m.Exists("ch_other_user"))
}

// TestUnbindUserAndCleanup_PreservesUnboundChannelsWhenAnotherConnExists
// guards the race that surfaces during a desktop dev refresh: the OLD
// relay's defer must not sweep unbound channels if a NEW relay session has
// already bound for the same user. Without atomic unbind+cleanup the OLD
// session would observe `noConns=true` between separate calls and wipe
// channels that the NEW session is about to use.
func TestUnbindUserAndCleanup_PreservesUnboundChannelsWhenAnotherConnExists(t *testing.T) {
	m := New(0)

	m.BindUser("u1", "old", noopSender, nil)
	m.BindUser("u1", "new", noopSender, nil)

	m.RegisterWithAuthInfo("ch_old_bound", "w1", "u1", AuthInfo{}, nil)
	bindChannelConn(t, m, "ch_old_bound", "old")
	m.RegisterWithAuthInfo("ch_new_unbound", "w1", "u1", AuthInfo{}, nil)

	removed := m.UnbindUserAndCleanup("u1", "old")

	assert.Len(t, removed, 1)
	assert.Equal(t, "ch_old_bound", removed[0].ChannelID)
	assert.False(t, m.Exists("ch_old_bound"))
	assert.True(t, m.Exists("ch_new_unbound"), "unbound channel must survive while another conn exists")
}

func TestUnbindUserAndCleanup_UnknownConn(t *testing.T) {
	m := New(0)
	m.RegisterWithAuthInfo("ch1", "w1", "u1", AuthInfo{}, nil)

	removed := m.UnbindUserAndCleanup("u1", "never-bound")

	// No conn for user → unbound channel is swept.
	assert.Len(t, removed, 1)
	assert.Equal(t, "ch1", removed[0].ChannelID)
}

func TestUnbindUserAndCleanup_CallsConnCancel(t *testing.T) {
	m := New(0)

	cancelled := false
	m.BindUser("u1", "conn1", noopSender, func() { cancelled = true })

	m.UnbindUserAndCleanup("u1", "conn1")
	assert.True(t, cancelled)
}

// TestUnbindUserAndCleanup_RaceWithConcurrentBind exercises the race window
// that motivated the atomic implementation. The "new" relay's BindUser+
// Register sequence races with the "old" relay's cleanup. Whichever
// finishes first must produce a consistent post-state:
//   - If "new" wins: noConns observed as false, the new channel survives.
//   - If "old" wins: noConns observed as true, no new channel exists yet.
//
// A non-atomic implementation can interleave a BindUser+Register between
// its own UnbindUser and unbound sweep, observing noConns=true while the
// new channel is already present — and wipe it. With the atomic version,
// either the new conn is present at sweep time or the new channel hasn't
// been registered yet, so the new channel always survives.
func TestUnbindUserAndCleanup_RaceWithConcurrentBind(t *testing.T) {
	const iterations = 5000

	for i := 0; i < iterations; i++ {
		m := New(0)
		m.BindUser("u1", "old", noopSender, nil)

		var wg sync.WaitGroup
		wg.Add(2)

		go func() {
			defer wg.Done()
			m.UnbindUserAndCleanup("u1", "old")
		}()

		newChannelID := "ch_new"
		go func() {
			defer wg.Done()
			m.BindUser("u1", "new", noopSender, nil)
			m.RegisterWithAuthInfo(newChannelID, "w1", "u1", AuthInfo{}, nil)
		}()

		wg.Wait()

		// "new" always finishes binding+registering, so the new channel
		// must always exist after both goroutines complete.
		assert.True(t, m.Exists(newChannelID),
			"iteration %d: ch_new must survive concurrent cleanup", i)
	}
}

func TestUnbindUserAndCleanup_RechecksConcurrentBindAfterChannelOperation(t *testing.T) {
	m := New(0)
	operationStarted := make(chan struct{})
	releaseOperation := make(chan struct{})
	m.BindUser("u1", "old", noopSender, nil)
	m.BindUser("u1", "new", noopSender, nil)
	m.RegisterWithAuthInfo("ch1", "w1", "u1", AuthInfo{}, nil)

	operationDone := make(chan struct{})
	go func() {
		defer close(operationDone)
		// Hold opMu on the Lock bind path (first write of ConnID) so cleanup
		// can drop "old" while a concurrent BindUser("new") races the recheck.
		_, found, err := m.UseAuthorizedChannel("ch1", "new", nil, func(ChannelInfo) error {
			close(operationStarted)
			<-releaseOperation
			return nil
		})
		assert.True(t, found)
		assert.NoError(t, err)
	}()
	<-operationStarted

	cleanupDone := make(chan []ClosedChannel, 1)
	go func() { cleanupDone <- m.UnbindUserAndCleanup("u1", "old") }()
	require.Eventually(t, func() bool {
		m.mu.RLock()
		defer m.mu.RUnlock()
		_, hasOld := m.userSenders["u1"]["old"]
		return !hasOld
	}, time.Second, time.Millisecond)

	close(releaseOperation)
	<-operationDone
	require.Empty(t, <-cleanupDone)
	assert.True(t, m.Exists("ch1"), "a concurrent relay bind must preserve the unbound channel")
	got, ok := m.GetChannelInfo("ch1")
	require.True(t, ok)
	assert.Equal(t, "new", got.ConnID)
}

func TestUnbindUser_ReturnsNoConns(t *testing.T) {
	m := New(0)
	m.BindUser("u1", "conn1", func(msg *leapmuxv1.ChannelMessage) error {
		return nil
	}, nil)
	m.BindUser("u1", "conn2", func(msg *leapmuxv1.ChannelMessage) error {
		return nil
	}, nil)

	// First unbind — still has conn2.
	noConns := m.UnbindUser("u1", "conn1")
	assert.False(t, noConns)

	// Second unbind — no connections left.
	noConns = m.UnbindUser("u1", "conn2")
	assert.True(t, noConns)

	// Unbind nonexistent user.
	noConns = m.UnbindUser("u1", "conn3")
	assert.True(t, noConns)
}

func TestUnregisterByWorker_SendsCloseNotifications(t *testing.T) {
	m := New(0)

	var received []*leapmuxv1.ChannelMessage
	m.BindUser("u1", "conn1", func(msg *leapmuxv1.ChannelMessage) error {
		received = append(received, msg)
		return nil
	}, nil)

	m.RegisterWithAuthInfo("ch1", "w1", "u1", AuthInfo{}, nil)
	m.RegisterWithAuthInfo("ch2", "w1", "u1", AuthInfo{}, nil)
	m.RegisterWithAuthInfo("ch3", "w2", "u1", AuthInfo{}, nil) // Different worker, should not be removed.

	bindChannelConn(t, m, "ch1", "conn1")
	bindChannelConn(t, m, "ch2", "conn1")

	removed := m.UnregisterByWorker("w1")
	assert.Len(t, removed, 2)

	// Should have received close notifications for ch1 and ch2 (empty ciphertext).
	assert.Len(t, received, 2)
	closedIDs := map[string]bool{}
	for _, msg := range received {
		closedIDs[msg.GetChannelId()] = true
		assert.Empty(t, msg.GetCiphertext())
	}
	assert.True(t, closedIDs["ch1"])
	assert.True(t, closedIDs["ch2"])
}

func TestSendToUser_SingleConnection(t *testing.T) {
	m := New(0)

	var received []*leapmuxv1.ChannelMessage
	m.BindUser("u1", "conn1", func(msg *leapmuxv1.ChannelMessage) error {
		received = append(received, msg)
		return nil
	}, nil)

	msg := &leapmuxv1.ChannelMessage{ChannelId: HubControlChannelID, Ciphertext: []byte("ctrl")}
	m.SendToUser("u1", msg)

	assert.Len(t, received, 1)
	assert.Equal(t, HubControlChannelID, received[0].GetChannelId())
	assert.Equal(t, []byte("ctrl"), received[0].GetCiphertext())
}

func TestSendToUser_MultipleConnections(t *testing.T) {
	m := New(0)

	var received1, received2 []*leapmuxv1.ChannelMessage
	m.BindUser("u1", "conn1", func(msg *leapmuxv1.ChannelMessage) error {
		received1 = append(received1, msg)
		return nil
	}, nil)
	m.BindUser("u1", "conn2", func(msg *leapmuxv1.ChannelMessage) error {
		received2 = append(received2, msg)
		return nil
	}, nil)

	msg := &leapmuxv1.ChannelMessage{ChannelId: HubControlChannelID, Ciphertext: []byte("ctrl")}
	m.SendToUser("u1", msg)

	// Both connections should receive the message.
	assert.Len(t, received1, 1)
	assert.Len(t, received2, 1)
}

func TestSendToUser_OtherUserNotAffected(t *testing.T) {
	m := New(0)

	var receivedU1, receivedU2 []*leapmuxv1.ChannelMessage
	m.BindUser("u1", "conn1", func(msg *leapmuxv1.ChannelMessage) error {
		receivedU1 = append(receivedU1, msg)
		return nil
	}, nil)
	m.BindUser("u2", "conn2", func(msg *leapmuxv1.ChannelMessage) error {
		receivedU2 = append(receivedU2, msg)
		return nil
	}, nil)

	msg := &leapmuxv1.ChannelMessage{ChannelId: HubControlChannelID, Ciphertext: []byte("ctrl")}
	m.SendToUser("u1", msg)

	assert.Len(t, receivedU1, 1)
	assert.Len(t, receivedU2, 0, "other user should not receive the message")
}

func TestSendToUser_UnknownUser(t *testing.T) {
	m := New(0)
	// Should not panic on unknown user.
	msg := &leapmuxv1.ChannelMessage{ChannelId: HubControlChannelID, Ciphertext: []byte("ctrl")}
	m.SendToUser("nonexistent", msg)
}

// --- Credential- and ACL-scoped close tests ---

// TestCloseByBearer_DropsOnlyMatchingChannels verifies the bearer-keyed
// teardown that fires alongside delegation-token revocation: only the
// channels authenticated by the revoked token go away. Cookie
// channels and channels held by other bearers are unaffected.
func TestCloseByBearer_DropsOnlyMatchingChannels(t *testing.T) {
	m := New(0)
	var bearerCancelled, otherCancelled, cookieCancelled bool

	m.RegisterWithAuthInfo("ch-bearer", "w1", "u1", AuthInfo{Credential: auth.APICredential("tok-revoke-me")}, func() { bearerCancelled = true })
	m.RegisterWithAuthInfo("ch-other", "w1", "u1", AuthInfo{Credential: auth.APICredential("tok-keep")}, func() { otherCancelled = true })
	m.RegisterWithAuthInfo("ch-cookie", "w1", "u1", AuthInfo{}, func() { cookieCancelled = true }) // no bearer

	closed := m.CloseByBearer(auth.NewBearerRef(auth.BearerKindAPI, "tok-revoke-me"))
	closedIDs := make([]string, 0, len(closed))
	for _, cc := range closed {
		closedIDs = append(closedIDs, cc.ChannelID)
	}
	assert.ElementsMatch(t, []string{"ch-bearer"}, closedIDs)
	assert.True(t, bearerCancelled, "bearer's channel cancel must fire")
	assert.False(t, otherCancelled, "other bearer's channel must survive")
	assert.False(t, cookieCancelled, "cookie channel must survive")
	assert.False(t, m.Exists("ch-bearer"))
	assert.True(t, m.Exists("ch-other"))
	assert.True(t, m.Exists("ch-cookie"))
}

func TestCloseByBearer_IsScopedByBearerKind(t *testing.T) {
	m := New(0)
	var apiCancelled, delegationCancelled bool

	m.RegisterWithAuthInfo("ch-api", "w1", "u1", AuthInfo{
		Credential: auth.APICredential("same-id"),
	}, func() { apiCancelled = true })
	m.RegisterWithAuthInfo("ch-delegation", "w1", "u1", AuthInfo{
		Credential: auth.DelegationCredential("same-id", "worker-mint"),
	}, func() { delegationCancelled = true })

	closed := m.CloseByBearer(auth.NewBearerRef(auth.BearerKindDelegation, "same-id"))
	closedIDs := make([]string, 0, len(closed))
	for _, cc := range closed {
		closedIDs = append(closedIDs, cc.ChannelID)
	}

	assert.ElementsMatch(t, []string{"ch-delegation"}, closedIDs)
	assert.False(t, apiCancelled, "api-token channel must survive delegation-token revocation with same row id")
	assert.True(t, delegationCancelled)
	assert.True(t, m.Exists("ch-api"))
	assert.False(t, m.Exists("ch-delegation"))
}

// TestCloseByBearer_EmptyTokenIDIsNoop locks down the safety check:
// a buggy revoke path that passes "" must NOT match every cookie
// channel (whose credential identity has no bearer row).
func TestCloseByBearer_EmptyTokenIDIsNoop(t *testing.T) {
	m := New(0)
	m.RegisterWithAuthInfo("ch-cookie", "w1", "u1", AuthInfo{}, nil)
	closed := m.CloseByBearer(auth.NewBearerRef(auth.BearerKindAPI, ""))
	assert.Empty(t, closed)
	assert.True(t, m.Exists("ch-cookie"))
}

func TestScheduleExpiryReusesTimerAndHonorsRescheduleAndRemoval(t *testing.T) {
	m := New(0)
	m.RegisterWithAuthInfo("first", "worker", "user", AuthInfo{}, nil)
	m.RegisterWithAuthInfo("rescheduled", "worker", "user", AuthInfo{}, nil)
	m.RegisterWithAuthInfo("removed", "worker", "user", AuthInfo{}, nil)

	expired := make(chan string, 3)
	now := time.Now()
	require.True(t, m.ScheduleExpiry("rescheduled", auth.DeadlineAt(now.Add(25*time.Millisecond)), func(ch ClosedChannel) {
		expired <- ch.ChannelID
	}))
	timer := m.expiryTimer
	require.NotNil(t, timer)
	require.True(t, m.ScheduleExpiry("first", auth.DeadlineAt(now.Add(45*time.Millisecond)), func(ch ClosedChannel) {
		expired <- ch.ChannelID
	}))
	require.Same(t, timer, m.expiryTimer, "all channel expirations must share one timer")
	require.True(t, m.ScheduleExpiry("rescheduled", auth.DeadlineAt(now.Add(70*time.Millisecond)), func(ch ClosedChannel) {
		expired <- ch.ChannelID
	}))
	require.True(t, m.ScheduleExpiry("removed", auth.DeadlineAt(now.Add(30*time.Millisecond)), func(ch ClosedChannel) {
		expired <- ch.ChannelID
	}))
	m.CloseByID("removed")

	select {
	case got := <-expired:
		assert.Equal(t, "first", got)
	case <-time.After(time.Second):
		t.Fatal("first channel did not expire")
	}
	select {
	case got := <-expired:
		assert.Equal(t, "rescheduled", got)
	case <-time.After(time.Second):
		t.Fatal("rescheduled channel did not expire")
	}
	select {
	case got := <-expired:
		t.Fatalf("removed channel unexpectedly expired: %s", got)
	case <-time.After(50 * time.Millisecond):
	}
	assert.False(t, m.Exists("first"))
	assert.False(t, m.Exists("rescheduled"))
}

// A panic in a channel's onExpire callback (or the frontend close-notification
// inside drainRemoved) must be RECOVERED on the detached expiry-teardown
// goroutine: it runs with no caller to propagate to, so an unrecovered panic
// would crash the whole Hub process instead of dropping one expiry notification.
// Without the recover this test crashes the test binary; with it, the manager
// stays usable.
func TestExpirySweepRecoversFromCallbackPanic(t *testing.T) {
	m := New(0)
	m.RegisterWithAuthInfo("boom", "worker", "user", AuthInfo{}, nil)

	fired := make(chan struct{})
	now := time.Now()
	require.True(t, m.ScheduleExpiry("boom", auth.DeadlineAt(now.Add(10*time.Millisecond)), func(ClosedChannel) {
		defer close(fired)
		panic("onExpire blew up")
	}))

	select {
	case <-fired:
	case <-time.After(2 * time.Second):
		t.Fatal("expiry callback never fired")
	}

	// The panicking channel is still torn down and the manager keeps working --
	// the sweep goroutine recovered rather than crashing the process.
	require.Eventually(t, func() bool { return !m.Exists("boom") },
		time.Second, 5*time.Millisecond, "expired channel must be removed despite the panic")
	m.RegisterWithAuthInfo("after", "worker", "user", AuthInfo{}, nil)
	assert.True(t, m.Exists("after"), "manager must remain usable after recovering the panic")
}

// A revocation sweep must not be head-of-line-blocked by a single channel whose
// routed operation is wedged on an unresponsive worker: the shared teardown core
// tears each channel down under its OWN opMu concurrently (bounded), so a revoked
// credential's remaining channels stop relaying promptly instead of waiting behind
// the stuck one -- a security window, not just a latency one.
func TestCloseDoesNotHeadOfLineBlockOnWedgedChannel(t *testing.T) {
	m := New(0)
	m.RegisterWithAuthInfo("ch-wedged", "w", "u1", AuthInfo{}, nil)
	for _, id := range []string{"ch-a", "ch-b", "ch-c"} {
		m.RegisterWithAuthInfo(id, "w", "u1", AuthInfo{}, nil)
	}

	// Wedge ch-wedged: hold its opMu across a routed op that blocks.
	opHolding := make(chan struct{})
	opRelease := make(chan struct{})
	go func() {
		_, _, _ = m.UseChannelIf("ch-wedged", nil, func(ChannelInfo) error {
			close(opHolding)
			<-opRelease
			return nil
		})
	}()
	<-opHolding // ch-wedged's opMu is now held by the in-flight op

	// Fail-safe user revocation (non-positive generation) closes all of u1's
	// channels; the wedged one blocks on its opMu, the others must not wait for it.
	closeDone := make(chan struct{})
	go func() {
		m.CloseByUserRevocation("u1", -1)
		close(closeDone)
	}()

	require.Eventually(t, func() bool {
		return !m.Exists("ch-a") && !m.Exists("ch-b") && !m.Exists("ch-c")
	}, 2*time.Second, 5*time.Millisecond,
		"non-wedged channels must tear down without waiting on the wedged channel")

	// The sweep itself is still blocked on the wedged channel.
	select {
	case <-closeDone:
		t.Fatal("CloseByUserRevocation returned before the wedged channel's op released")
	case <-time.After(50 * time.Millisecond):
	}

	// Release the wedged op; the sweep completes and the last channel is gone.
	close(opRelease)
	<-closeDone
	assert.False(t, m.Exists("ch-wedged"))
}

// A bearer rotation (or session slide) that lands while a channel is still being
// opened -- registered and indexed, but not yet scheduled (onExpire == nil) --
// must not be lost. The recorded extension is carried into ScheduleExpiry so the
// channel is armed at the extended deadline, not the stale connect-time one.
func TestScheduleExpiryHonorsRescheduleDuringOpenWindow(t *testing.T) {
	m := New(0)
	now := time.Now()
	connectExpiry := now.Add(20 * time.Millisecond)
	extended := now.Add(10 * time.Second)
	m.RegisterWithAuthInfo("opening", "worker", "user", AuthInfo{
		Credential:          auth.APICredential("tok-1"),
		CredentialExpiresAt: auth.DeadlineAt(connectExpiry),
	}, nil)

	// Rotation extends the bearer mid-handshake: the channel is indexed under the
	// bearer but not yet scheduled (onExpire nil, not in the heap).
	m.RescheduleExpiryByBearer(auth.NewBearerRef(auth.BearerKindAPI, "tok-1"), auth.DeadlineAt(extended))

	fired := make(chan string, 1)
	require.True(t, m.ScheduleExpiry("opening", auth.DeadlineAt(connectExpiry), func(ch ClosedChannel) {
		fired <- ch.ChannelID
	}))

	// It must NOT tear down at the stale pre-rotation deadline (~20ms).
	select {
	case id := <-fired:
		t.Fatalf("channel %s torn down at the stale pre-rotation deadline", id)
	case <-time.After(80 * time.Millisecond):
	}
	assert.True(t, m.Exists("opening"), "channel must survive to its extended deadline")
	info, ok := m.GetChannelInfo("opening")
	require.True(t, ok)
	assert.Equal(t, auth.DeadlineAt(extended), info.AuthInfo.CredentialExpiresAt, "channel must carry the extended expiry")
}

// ScheduleExpiry must report failure for a never-expires (zero) credential when
// the channel was torn down during the open handshake, instead of the previous
// unconditional true that committed a phantom-open for a channel already gone
// from the manager (and already closed at the worker).
func TestScheduleExpiryZeroExpiryRejectsRemovedChannel(t *testing.T) {
	m := New(0)
	m.RegisterWithAuthInfo("gone", "worker", "user", AuthInfo{
		Credential: auth.APICredential("tok-1"), // zero CredentialExpiresAt -> never expires
	}, nil)
	// A concurrent revocation removes the channel during the open handshake.
	m.CloseByBearer(auth.NewBearerRef(auth.BearerKindAPI, "tok-1"))

	ok := m.ScheduleExpiry("gone", auth.NeverExpires(), func(ClosedChannel) {})
	assert.False(t, ok, "scheduling a never-expires channel that was torn down must fail")
}

// A live never-expires channel still schedules successfully -- the liveness gate
// added for the removed case must not reject the healthy one.
func TestScheduleExpiryZeroExpiryAcceptsLiveChannel(t *testing.T) {
	m := New(0)
	m.RegisterWithAuthInfo("live", "worker", "user", AuthInfo{
		Credential: auth.APICredential("tok-1"),
	}, nil)

	ok := m.ScheduleExpiry("live", auth.NeverExpires(), func(ClosedChannel) {})
	assert.True(t, ok)
	assert.True(t, m.Exists("live"))
}

// TestScheduleExpiryRejectsNilOnExpire pins the entry-point guard. A scheduled
// channel is identified downstream by ch.onExpire != nil (rescheduleExpiryLocked
// routes a mid-open reschedule to pendingExpiry vs re-timing the heap on exactly
// that signal), so ScheduleExpiry rejects a nil callback rather than arming a
// channel that a later reschedule would misread as "still opening" and silently
// drop. Without the guard a live channel would schedule with onExpire == nil and
// this would return true.
func TestScheduleExpiryRejectsNilOnExpire(t *testing.T) {
	m := New(0)
	m.RegisterWithAuthInfo("live", "worker", "user", AuthInfo{
		Credential: auth.APICredential("tok-1"),
	}, nil)

	ok := m.ScheduleExpiry("live", auth.DeadlineAt(time.Now().Add(time.Hour)), nil)
	assert.False(t, ok,
		"a nil onExpire must be rejected so 'scheduled implies onExpire != nil' holds mechanically")
	assert.True(t, m.Exists("live"), "rejecting the schedule must not tear the channel down")
}

// A reschedule that CLEARS the deadline (zero) while the channel is still being
// opened (onExpire nil, not yet in the heap) must be honored verbatim, exactly
// like the already-scheduled zero-clear path -- the channel must never expire,
// not be armed at the stale connect-time deadline. Before the reschedule-flag
// change, the mid-open carry folded a zero recorded value back to the connect
// base and tore the channel down at ~20ms.
func TestScheduleExpiryHonorsZeroClearDuringOpenWindow(t *testing.T) {
	m := New(0)
	connectExpiry := time.Now().Add(20 * time.Millisecond)
	m.RegisterWithAuthInfo("opening", "worker", "user", AuthInfo{
		Credential:          auth.APICredential("tok-1"),
		CredentialExpiresAt: auth.DeadlineAt(connectExpiry),
	}, nil)

	// A reschedule clears the bearer's deadline mid-handshake, before
	// ScheduleExpiry arms the timer (indexed by bearer, not yet scheduled).
	m.RescheduleExpiryByBearer(auth.NewBearerRef(auth.BearerKindAPI, "tok-1"), auth.NeverExpires())

	fired := make(chan string, 1)
	require.True(t, m.ScheduleExpiry("opening", auth.DeadlineAt(connectExpiry), func(ch ClosedChannel) {
		fired <- ch.ChannelID
	}))

	select {
	case id := <-fired:
		t.Fatalf("channel %s torn down at the stale connect deadline despite the clear", id)
	case <-time.After(80 * time.Millisecond):
	}
	assert.True(t, m.Exists("opening"), "a cleared channel must never expire")
	info, ok := m.GetChannelInfo("opening")
	require.True(t, ok)
	assert.True(t, info.AuthInfo.CredentialExpiresAt.IsNever(), "cleared expiry must survive scheduling")
}

// An out-of-order reschedule to an EARLIER instant must not regress a
// still-later scheduled deadline: two concurrent same-credential extensions can
// arrive reversed, and without the monotonic guard the earlier one would win the
// heap and tear a still-valid channel down early. Mirrors the cache-side
// RecordBearerExpiry CAS.
func TestRescheduleExpiryDoesNotRegressScheduledDeadline(t *testing.T) {
	m := New(0)
	now := time.Now()
	late := now.Add(10 * time.Second)
	early := now.Add(20 * time.Millisecond)
	m.RegisterWithAuthInfo("ch", "worker", "user", AuthInfo{
		Credential:          auth.APICredential("tok-1"),
		CredentialExpiresAt: auth.DeadlineAt(late),
	}, nil)
	fired := make(chan string, 1)
	require.True(t, m.ScheduleExpiry("ch", auth.DeadlineAt(late), func(c ClosedChannel) {
		fired <- c.ChannelID
	}))

	// A stale reschedule delivered out of order carries the earlier deadline.
	m.RescheduleExpiryByBearer(auth.NewBearerRef(auth.BearerKindAPI, "tok-1"), auth.DeadlineAt(early))

	select {
	case id := <-fired:
		t.Fatalf("channel %s torn down at the regressed earlier deadline", id)
	case <-time.After(80 * time.Millisecond):
	}
	assert.True(t, m.Exists("ch"))
	info, ok := m.GetChannelInfo("ch")
	require.True(t, ok)
	assert.Equal(t, auth.DeadlineAt(late), info.AuthInfo.CredentialExpiresAt,
		"the later finite deadline must survive an out-of-order earlier reschedule")
}

// A genuine LATER reschedule of an already-scheduled channel still extends it (the
// monotonic guard must not block a real extension). A clear still disarms it.
func TestRescheduleExpiryExtendsAndClearsScheduledDeadline(t *testing.T) {
	m := New(0)
	now := time.Now()
	ref := auth.NewBearerRef(auth.BearerKindAPI, "tok-1")
	m.RegisterWithAuthInfo("ch", "worker", "user", AuthInfo{
		Credential:          auth.APICredential("tok-1"),
		CredentialExpiresAt: auth.DeadlineAt(now.Add(time.Minute)),
	}, nil)
	require.True(t, m.ScheduleExpiry("ch", auth.DeadlineAt(now.Add(time.Minute)), func(ClosedChannel) {}))

	extended := now.Add(time.Hour)
	m.RescheduleExpiryByBearer(ref, auth.DeadlineAt(extended))
	info, ok := m.GetChannelInfo("ch")
	require.True(t, ok)
	assert.Equal(t, auth.DeadlineAt(extended), info.AuthInfo.CredentialExpiresAt, "a later reschedule extends")

	m.RescheduleExpiryByBearer(ref, auth.NeverExpires())
	info, ok = m.GetChannelInfo("ch")
	require.True(t, ok)
	assert.True(t, info.AuthInfo.CredentialExpiresAt.IsNever(), "a clear disarms the deadline")
}

// expireDueChannels must not spin when a zero (never-expires) entry ends up in
// the expiry heap. That state is prevented by construction today (ScheduleExpiry
// never pushes a zero; a clear heap.Removes it), but the outer loop guard reads a
// zero as year-1/already-past, so without the defensive drop the loop would
// re-select the same heap-top forever and hang the manager goroutine. Inject the
// invariant-violating state directly and assert the sweep terminates, drops the
// stray entry, and leaves the channel alive (never expires).
func TestExpireDueChannelsDropsStrayZeroHeapEntryWithoutSpinning(t *testing.T) {
	m := New(0)
	m.RegisterWithAuthInfo("ch", "worker", "user", AuthInfo{
		Credential:          auth.APICredential("tok-1"),
		CredentialExpiresAt: auth.DeadlineAt(time.Now().Add(time.Hour)),
	}, nil)
	require.True(t, m.ScheduleExpiry("ch", auth.DeadlineAt(time.Now().Add(time.Hour)), func(ClosedChannel) {}))

	// Corrupt the invariant: a zero (never-expires) expiresAt left in the heap.
	m.mu.Lock()
	ch := m.channels["ch"]
	require.NotNil(t, ch)
	ch.expiresAt = auth.NeverExpires()
	require.Len(t, m.expiries, 1)
	m.mu.Unlock()

	done := make(chan struct{})
	go func() {
		m.expireDueChannels()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("expireDueChannels spun on a stray zero heap entry instead of dropping it")
	}

	assert.True(t, m.Exists("ch"), "a never-expires channel must survive the sweep")
	m.mu.Lock()
	empty := len(m.expiries) == 0
	m.mu.Unlock()
	assert.True(t, empty, "the stray zero heap entry must be dropped")
}

// A due channel whose routed operation is wedged (its opMu is held) must not
// head-of-line-block the expiry of the other due channels: the sweep tears each
// down under its own opMu concurrently. The old sequential sweep processed the
// earliest-deadline channel first, so a wedge there blocked every later expiry.
func TestExpireDueChannelsConcurrent_WedgedChannelDoesNotBlockOthers(t *testing.T) {
	m := New(0)
	// Two live channels. A has the earlier deadline so it is the heap top and is
	// processed first -- the position that, when wedged, blocks a sequential sweep.
	m.RegisterWithAuthInfo("A", "w", "u", AuthInfo{Credential: auth.APICredential("ta"), CredentialExpiresAt: auth.DeadlineAt(time.Now().Add(time.Hour))}, nil)
	m.RegisterWithAuthInfo("B", "w", "u", AuthInfo{Credential: auth.APICredential("tb"), CredentialExpiresAt: auth.DeadlineAt(time.Now().Add(2 * time.Hour))}, nil)
	require.True(t, m.ScheduleExpiry("A", auth.DeadlineAt(time.Now().Add(time.Hour)), func(ClosedChannel) {}))
	bTorn := make(chan string, 1)
	require.True(t, m.ScheduleExpiry("B", auth.DeadlineAt(time.Now().Add(2*time.Hour)), func(c ClosedChannel) { bTorn <- c.ChannelID }))

	// Make both due (past deadline) without re-heapifying: A stays the heap top.
	past := time.Now().Add(-time.Hour)
	m.mu.Lock()
	chA := m.channels["A"]
	m.channels["A"].expiresAt = auth.DeadlineAt(past.Add(-time.Hour))
	m.channels["B"].expiresAt = auth.DeadlineAt(past)
	m.mu.Unlock()

	// Wedge A: hold its opMu so A's teardown goroutine blocks in acquireChannelOp.
	chA.opMu.Lock()

	sweepDone := make(chan struct{})
	go func() { m.expireDueChannels(); close(sweepDone) }()

	// B must be torn down even though A is wedged.
	select {
	case id := <-bTorn:
		assert.Equal(t, "B", id)
	case <-time.After(2 * time.Second):
		t.Fatal("B's expiry was head-of-line-blocked by the wedged channel A")
	}
	assert.False(t, m.Exists("B"), "B must be torn down while A is wedged")
	assert.True(t, m.Exists("A"), "A stays live while its teardown blocks on opMu")

	// Release A; the sweep completes and tears A down.
	chA.opMu.Unlock()
	select {
	case <-sweepDone:
	case <-time.After(2 * time.Second):
		t.Fatal("sweep did not complete after the wedged channel was released")
	}
	assert.False(t, m.Exists("A"), "A is torn down once its opMu is released")
}

func TestCloseByUserRevocation_DropsOnlyOlderUserGeneration(t *testing.T) {
	m := New(0)
	var oldSessionCancelled, oldCacheCancelled, currentCancelled, otherCancelled bool

	m.RegisterWithAuthInfo("ch-old-session", "w1", "u1", AuthInfo{
		UserAuthGeneration: 0,
	}, func() { oldSessionCancelled = true })
	m.RegisterWithAuthInfo("ch-old-cache", "w1", "u1", AuthInfo{
		UserAuthGeneration: 0,
	}, func() { oldCacheCancelled = true })
	m.RegisterWithAuthInfo("ch-current", "w1", "u1", AuthInfo{
		UserAuthGeneration: 1,
	}, func() { currentCancelled = true })
	m.RegisterWithAuthInfo("ch-other", "w1", "u2", AuthInfo{
		UserAuthGeneration: 0,
	}, func() { otherCancelled = true })

	closed := m.CloseByUserRevocation("u1", 1)
	ids := make([]string, 0, len(closed))
	for _, cc := range closed {
		ids = append(ids, cc.ChannelID)
	}

	assert.ElementsMatch(t, []string{"ch-old-session", "ch-old-cache"}, ids)
	assert.True(t, oldSessionCancelled)
	assert.True(t, oldCacheCancelled, "channels opened from pre-revocation cached auth must close")
	assert.False(t, currentCancelled, "current-generation auth must survive revocation replay")
	assert.False(t, otherCancelled)
	assert.False(t, m.Exists("ch-old-session"))
	assert.False(t, m.Exists("ch-old-cache"))
	assert.True(t, m.Exists("ch-current"))
	assert.True(t, m.Exists("ch-other"))
}

func TestRestampSessionGenerationSparesSurvivingSession(t *testing.T) {
	m := New(0)
	m.RegisterWithAuthInfo("ch-current", "w1", "u1", AuthInfo{
		Credential: auth.SessionCredential("s-current"), UserAuthGeneration: 0,
	}, nil)
	m.RegisterWithAuthInfo("ch-old", "w1", "u1", AuthInfo{
		Credential: auth.SessionCredential("s-old"), UserAuthGeneration: 0,
	}, nil)

	// Only the acting session is re-stamped onto the post-change generation.
	m.RestampSessionGeneration("s-current", 1)

	closed := m.CloseByUserRevocation("u1", 1)
	ids := make([]string, 0, len(closed))
	for _, cc := range closed {
		ids = append(ids, cc.ChannelID)
	}

	assert.ElementsMatch(t, []string{"ch-old"}, ids, "only the un-restamped older session closes")
	assert.True(t, m.Exists("ch-current"), "the re-stamped surviving session's channel must not be torn down")
	assert.False(t, m.Exists("ch-old"))
}

func TestRescheduleExpiryBySessionExtendsDeadline(t *testing.T) {
	m := New(0)
	expired := make(chan string, 1)
	m.RegisterWithAuthInfo("ch", "w1", "u1", AuthInfo{Credential: auth.SessionCredential("s1")}, nil)
	require.True(t, m.ScheduleExpiry("ch", auth.DeadlineAt(time.Now().Add(30*time.Millisecond)), func(cc ClosedChannel) {
		expired <- cc.ChannelID
	}))

	// Extend well before the original deadline fires.
	m.RescheduleExpiryBySession("s1", auth.DeadlineAt(time.Now().Add(10*time.Second)))

	select {
	case id := <-expired:
		t.Fatalf("channel %q expired at the stale deadline despite the reschedule", id)
	case <-time.After(80 * time.Millisecond):
	}
	assert.True(t, m.Exists("ch"), "a rescheduled channel must survive past its original deadline")
}

func TestRescheduleExpiryByBearerClearsDeadlineOnZeroExpiry(t *testing.T) {
	m := New(0)
	expired := make(chan string, 1)
	m.RegisterWithAuthInfo("ch", "w1", "u1", AuthInfo{Credential: auth.APICredential("tok")}, nil)
	require.True(t, m.ScheduleExpiry("ch", auth.DeadlineAt(time.Now().Add(30*time.Millisecond)), func(cc ClosedChannel) {
		expired <- cc.ChannelID
	}))

	// A zero expiry disarms the deadline entirely.
	m.RescheduleExpiryByBearer(auth.NewBearerRef(auth.BearerKindAPI, "tok"), auth.NeverExpires())

	select {
	case id := <-expired:
		t.Fatalf("channel %q expired despite the deadline being cleared", id)
	case <-time.After(80 * time.Millisecond):
	}
	assert.True(t, m.Exists("ch"))
}

// TestCloseByBearer_NotifiesFrontend verifies the close path drives
// the same CHANNEL_CLOSE notification the frontend receives on a
// user-initiated CloseChannel. Without this, the browser-side store
// would keep the channel "open" until its own RPC timed out.
func TestCloseByBearer_NotifiesFrontend(t *testing.T) {
	m := New(0)

	var mu sync.Mutex
	var msgs []*leapmuxv1.ChannelMessage
	m.BindUser("u1", "conn1", func(msg *leapmuxv1.ChannelMessage) error {
		mu.Lock()
		defer mu.Unlock()
		msgs = append(msgs, msg)
		return nil
	}, nil)
	m.RegisterWithAuthInfo("ch", "w1", "u1", AuthInfo{Credential: auth.APICredential("tok")}, nil)
	bindChannelConn(t, m, "ch", "conn1")

	closed := m.CloseByBearer(auth.NewBearerRef(auth.BearerKindAPI, "tok"))
	assert.Len(t, closed, 1)
	mu.Lock()
	defer mu.Unlock()
	closeFrames := 0
	for _, m := range msgs {
		if m.GetFlags() == leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_CLOSE && m.GetChannelId() == "ch" {
			closeFrames++
		}
	}
	assert.Equal(t, 1, closeFrames, "frontend must receive a CHANNEL_MESSAGE_FLAGS_CLOSE for the dropped channel")
}

// The expiry sweep's Phase-2 fan-out is bounded by channelExpireConcurrency so a
// large simultaneously-expiring cohort cannot spawn an unbounded goroutine
// burst. The bound must (a) never exceed channelExpireConcurrency concurrent
// teardowns and (b) still tear down EVERY due channel -- the semaphore caps
// concurrency, it must not drop work.
func TestExpireDueChannelsBoundedFanOutClosesAll(t *testing.T) {
	m := New(0)
	const n = channelExpireConcurrency*3 + 5 // comfortably over the bound
	expired := make(chan string, n)
	var concurrent, maxConcurrent atomic.Int64
	now := time.Now()
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("ch-%d", i)
		m.RegisterWithAuthInfo(id, "worker", "user", AuthInfo{}, nil)
		require.True(t, m.ScheduleExpiry(id, auth.DeadlineAt(now.Add(20*time.Millisecond)), func(ch ClosedChannel) {
			cur := concurrent.Add(1)
			for {
				prev := maxConcurrent.Load()
				if cur <= prev || maxConcurrent.CompareAndSwap(prev, cur) {
					break
				}
			}
			// Hold the slot briefly so overlapping teardowns are observable; the
			// semaphore is released only after this callback returns.
			time.Sleep(3 * time.Millisecond)
			concurrent.Add(-1)
			expired <- ch.ChannelID
		}))
	}

	seen := make(map[string]struct{}, n)
	deadline := time.After(10 * time.Second)
	for len(seen) < n {
		select {
		case id := <-expired:
			seen[id] = struct{}{}
		case <-deadline:
			t.Fatalf("only %d of %d channels expired under the bounded fan-out", len(seen), n)
		}
	}

	assert.LessOrEqual(t, maxConcurrent.Load(), int64(channelExpireConcurrency),
		"the expiry fan-out must not exceed channelExpireConcurrency concurrent teardowns")
	for i := 0; i < n; i++ {
		assert.False(t, m.Exists(fmt.Sprintf("ch-%d", i)), "every due channel must be torn down")
	}
}

// TestChannelInfoCarriesTheCurrentConnID pins the field a close predicate
// needs to tell one frontend binding from another.
//
// A relay that fails to deliver tears the channel down, but the frontend
// can rebind that channel to a fresh connection in the window between the
// failure and the close. Without ConnID a predicate can only match on the
// worker, so it would close the NEW binding -- exactly what
// UnbindUserAndCleanup's own ConnID check exists to protect.
func TestChannelInfoCarriesTheCurrentConnID(t *testing.T) {
	m := New(0)
	m.RegisterWithAuthInfo("ch1", "w1", "u1", AuthInfo{}, nil)
	m.BindUser("u1", "conn-old", func(*leapmuxv1.ChannelMessage) error { return nil }, nil)

	_, ok, err := m.UseAuthorizedChannel("ch1", "conn-old",
		func(ChannelInfo) bool { return true },
		func(ChannelInfo) error { return nil })
	require.NoError(t, err)
	require.True(t, ok)

	// A close aimed at the binding that failed still finds it.
	closed := m.CloseByIDIf("ch1", func(info ChannelInfo) bool {
		return info.WorkerID == "w1" && info.ConnID == "conn-old"
	})
	assert.Len(t, closed, 1, "the failing binding must be closable")
}

// TestCloseByIDIfSkipsAChannelReboundToAnotherConnection is the guard
// itself: a close carrying a stale conn id must leave the new binding
// alone.
func TestCloseByIDIfSkipsAChannelReboundToAnotherConnection(t *testing.T) {
	m := New(0)
	m.RegisterWithAuthInfo("ch1", "w1", "u1", AuthInfo{}, nil)
	m.BindUser("u1", "conn-new", func(*leapmuxv1.ChannelMessage) error { return nil }, nil)

	// The channel has moved on to a fresh relay connection.
	_, ok, err := m.UseAuthorizedChannel("ch1", "conn-new",
		func(ChannelInfo) bool { return true },
		func(ChannelInfo) error { return nil })
	require.NoError(t, err)
	require.True(t, ok)

	// A teardown raised against the PREVIOUS connection must not take it.
	closed := m.CloseByIDIf("ch1", func(info ChannelInfo) bool {
		return info.WorkerID == "w1" && info.ConnID == "conn-old"
	})

	assert.Empty(t, closed, "a stale conn id must not close the rebound channel")
	assert.True(t, m.Exists("ch1"), "the new binding survives")
}

// TestUseAuthorizedChannel_SameConnReusePreservesBinding covers the #279
// steady-state path: after the first bind, a later frame with the same ConnID
// must succeed and leave the binding unchanged (RLock-only, no write).
func TestUseAuthorizedChannel_SameConnReusePreservesBinding(t *testing.T) {
	m := New(0)
	m.RegisterWithAuthInfo("ch1", "w1", "u1", AuthInfo{}, nil)
	m.BindUser("u1", "conn1", noopSender, nil)

	info, ok, err := m.UseAuthorizedChannel("ch1", "conn1",
		func(ChannelInfo) bool { return true },
		func(ChannelInfo) error { return nil })
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "conn1", info.ConnID, "first bind must return the newly written ConnID in the snapshot")

	var ops atomic.Int32
	info, ok, err = m.UseAuthorizedChannel("ch1", "conn1",
		func(ChannelInfo) bool { return true },
		func(ChannelInfo) error {
			ops.Add(1)
			return nil
		})
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "conn1", info.ConnID)
	assert.Equal(t, int32(1), ops.Load(), "RLock reuse path must still run the operation")

	got, ok := m.GetChannelInfo("ch1")
	require.True(t, ok)
	assert.Equal(t, "conn1", got.ConnID)

	closed := m.CloseByIDIf("ch1", func(info ChannelInfo) bool {
		return info.ConnID == "conn1"
	})
	assert.Len(t, closed, 1)
}

// TestUseAuthorizedChannel_ReuseAuthorizeHoldsRLockOnly proves the steady-state
// path authorizes under RLock: a concurrent GetChannelInfo (another reader)
// must complete while authorize is still inside the critical section. Nested
// Manager calls inside authorize are forbidden (see liveAndAuthorizeLocked) —
// under Lock, or under RLock with a waiting writer, they deadlock.
func TestUseAuthorizedChannel_ReuseAuthorizeHoldsRLockOnly(t *testing.T) {
	m := New(0)
	m.RegisterWithAuthInfo("ch1", "w1", "u1", AuthInfo{}, nil)
	m.BindUser("u1", "conn1", noopSender, nil)
	require.True(t, bindChannelConn(t, m, "ch1", "conn1"))

	authorizeEntered := make(chan struct{})
	releaseAuthorize := make(chan struct{})
	readerDone := make(chan struct{})

	go func() {
		<-authorizeEntered
		_, infoOK := m.GetChannelInfo("ch1")
		assert.True(t, infoOK)
		close(readerDone)
	}()

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, ok, err := m.UseAuthorizedChannel("ch1", "conn1",
			func(ChannelInfo) bool {
				close(authorizeEntered)
				<-releaseAuthorize
				return true
			},
			func(ChannelInfo) error { return nil })
		assert.NoError(t, err)
		assert.True(t, ok)
	}()

	waitOrDeadlock(t, "reuse authorize blocked concurrent GetChannelInfo (path took write lock)", func() {
		<-readerDone
	})
	close(releaseAuthorize)
	waitOrDeadlock(t, "reuse authorize did not finish after reader", func() {
		<-done
	})
}

// TestUseAuthorizedChannel_RebindUpdatesConnID covers the write-lock upgrade:
// a real ConnID change must update the snapshot and leave stale-conn closes alone.
func TestUseAuthorizedChannel_RebindUpdatesConnID(t *testing.T) {
	m := New(0)
	m.RegisterWithAuthInfo("ch1", "w1", "u1", AuthInfo{}, nil)
	m.BindUser("u1", "conn-old", noopSender, nil)
	m.BindUser("u1", "conn-new", noopSender, nil)

	_, ok, err := m.UseAuthorizedChannel("ch1", "conn-old",
		func(ChannelInfo) bool { return true },
		func(ChannelInfo) error { return nil })
	require.NoError(t, err)
	require.True(t, ok)

	info, ok, err := m.UseAuthorizedChannel("ch1", "conn-new",
		func(ChannelInfo) bool { return true },
		func(ChannelInfo) error { return nil })
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "conn-new", info.ConnID)

	got, ok := m.GetChannelInfo("ch1")
	require.True(t, ok)
	assert.Equal(t, "conn-new", got.ConnID)

	assert.Empty(t, m.CloseByIDIf("ch1", func(info ChannelInfo) bool {
		return info.ConnID == "conn-old"
	}), "stale conn must not close the rebound channel")
	assert.True(t, m.Exists("ch1"))
}

// TestUseChannelIf_NilBindLeavesConnIDEmpty covers bindModeNone: the RLock
// path must succeed without ever writing ConnID.
func TestUseChannelIf_NilBindLeavesConnIDEmpty(t *testing.T) {
	m := New(0)
	m.RegisterWithAuthInfo("ch1", "w1", "u1", AuthInfo{}, nil)

	info, ok, err := m.UseChannelIf("ch1", nil, func(ChannelInfo) error { return nil })
	require.NoError(t, err)
	require.True(t, ok)
	assert.Empty(t, info.ConnID)

	got, ok := m.GetChannelInfo("ch1")
	require.True(t, ok)
	assert.Empty(t, got.ConnID)
}

// TestUseChannelIf_PreservesExistingConnID: bindModeNone must not clear or
// rewrite an already-bound ConnID (and must not require hasUserConn).
func TestUseChannelIf_PreservesExistingConnID(t *testing.T) {
	m := New(0)
	m.RegisterWithAuthInfo("ch1", "w1", "u1", AuthInfo{}, nil)
	m.BindUser("u1", "conn1", noopSender, nil)
	require.True(t, bindChannelConn(t, m, "ch1", "conn1"))
	m.UnbindUser("u1", "conn1") // sender gone; bindModeNone must still operate

	info, ok, err := m.UseChannelIf("ch1", nil, func(ChannelInfo) error { return nil })
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "conn1", info.ConnID)

	got, ok := m.GetChannelInfo("ch1")
	require.True(t, ok)
	assert.Equal(t, "conn1", got.ConnID, "UseChannelIf must leave ConnID untouched")
}

// TestUseAuthorizedChannel_RefuseClearToEmptyString: clearing ConnID via
// bindModeConn("") is a NeedWrite that refuses when "" is not registered.
func TestUseAuthorizedChannel_RefuseClearToEmptyString(t *testing.T) {
	m := New(0)
	m.RegisterWithAuthInfo("ch1", "w1", "u1", AuthInfo{}, nil)
	m.BindUser("u1", "conn1", noopSender, nil)
	require.True(t, bindChannelConn(t, m, "ch1", "conn1"))

	_, ok, err := m.UseAuthorizedChannel("ch1", "",
		func(ChannelInfo) bool { return true },
		func(ChannelInfo) error { return nil })
	require.NoError(t, err)
	assert.False(t, ok)

	got, ok := m.GetChannelInfo("ch1")
	require.True(t, ok)
	assert.Equal(t, "conn1", got.ConnID, "refuse-to-clear must preserve the prior binding")
}

// TestUseAuthorizedChannel_RejectDoesNotBindConnID ensures a failing authorize
// on the first-bind Lock path leaves ConnID empty (authorize runs once under
// Lock with the ConnID write, not under the RLock needBind peek).
func TestUseAuthorizedChannel_RejectDoesNotBindConnID(t *testing.T) {
	m := New(0)
	m.RegisterWithAuthInfo("ch1", "w1", "u1", AuthInfo{}, nil)
	m.BindUser("u1", "conn1", noopSender, nil)

	var calls atomic.Int32
	_, ok, err := m.UseAuthorizedChannel("ch1", "conn1",
		func(ChannelInfo) bool {
			calls.Add(1)
			return false
		},
		func(ChannelInfo) error { return nil })
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Equal(t, int32(1), calls.Load(), "bind-path authorize must run once under Lock")

	got, ok := m.GetChannelInfo("ch1")
	require.True(t, ok)
	assert.Empty(t, got.ConnID, "rejected authorize must not bind ConnID")
}

// TestUseAuthorizedChannel_RejectOnRebindPreservesPriorConnID covers authorize
// failing under Lock during a real ConnID change: the prior binding must stay.
func TestUseAuthorizedChannel_RejectOnRebindPreservesPriorConnID(t *testing.T) {
	m := New(0)
	m.RegisterWithAuthInfo("ch1", "w1", "u1", AuthInfo{}, nil)
	m.BindUser("u1", "conn-old", noopSender, nil)
	m.BindUser("u1", "conn-new", noopSender, nil)
	require.True(t, bindChannelConn(t, m, "ch1", "conn-old"))

	var calls atomic.Int32
	_, ok, err := m.UseAuthorizedChannel("ch1", "conn-new",
		func(ChannelInfo) bool {
			calls.Add(1)
			return false
		},
		func(ChannelInfo) error { return nil })
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Equal(t, int32(1), calls.Load())

	got, ok := m.GetChannelInfo("ch1")
	require.True(t, ok)
	assert.Equal(t, "conn-old", got.ConnID, "failed rebind authorize must not clear or overwrite ConnID")
}

// TestUseAuthorizedChannel_SteadyStateRejectPreservesConnID: once bound, a
// later authorize failure on the RLock reuse path must leave ConnID intact.
func TestUseAuthorizedChannel_SteadyStateRejectPreservesConnID(t *testing.T) {
	m := New(0)
	m.RegisterWithAuthInfo("ch1", "w1", "u1", AuthInfo{}, nil)
	m.BindUser("u1", "conn1", noopSender, nil)
	require.True(t, bindChannelConn(t, m, "ch1", "conn1"))

	var calls atomic.Int32
	_, ok, err := m.UseAuthorizedChannel("ch1", "conn1",
		func(ChannelInfo) bool {
			calls.Add(1)
			return false
		},
		func(ChannelInfo) error { return nil })
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Equal(t, int32(1), calls.Load())

	got, ok := m.GetChannelInfo("ch1")
	require.True(t, ok)
	assert.Equal(t, "conn1", got.ConnID, "steady-state reject must not clear the binding")
}

// TestUseAuthorizedChannel_RefuseBindWhenTargetConnGone: if the bind target is
// no longer a registered user conn (Unbind finished before bind — same outcome
// as an RUnlock→Lock gap), refuse rather than attaching a dead ConnID while
// another user conn kept the unbound-channel sweep from closing ch1.
func TestUseAuthorizedChannel_RefuseBindWhenTargetConnGone(t *testing.T) {
	m := New(0)
	m.RegisterWithAuthInfo("ch1", "w1", "u1", AuthInfo{}, nil)
	m.BindUser("u1", "conn-dead", noopSender, nil)
	m.BindUser("u1", "conn-alive", noopSender, nil)

	closed := m.UnbindUserAndCleanup("u1", "conn-dead")
	assert.Empty(t, closed, "alive conn must keep the unbound channel out of the sweep")

	_, ok, err := m.UseAuthorizedChannel("ch1", "conn-dead",
		func(ChannelInfo) bool { return true },
		func(ChannelInfo) error { return nil })
	require.NoError(t, err)
	assert.False(t, ok, "must refuse bind to an unbound conn")

	got, exists := m.GetChannelInfo("ch1")
	require.True(t, exists)
	assert.Empty(t, got.ConnID, "must not bind a dead conn")
}

// TestUseAuthorizedChannel_RefuseReuseWhenBoundConnUnbound: Ready/reuse must
// reject when ConnID still matches but UnbindUser dropped the registration
// without cleanup (channel kept alive by another conn).
func TestUseAuthorizedChannel_RefuseReuseWhenBoundConnUnbound(t *testing.T) {
	m := New(0)
	m.RegisterWithAuthInfo("ch1", "w1", "u1", AuthInfo{}, nil)
	m.BindUser("u1", "conn1", noopSender, nil)
	m.BindUser("u1", "conn2", noopSender, nil)
	require.True(t, bindChannelConn(t, m, "ch1", "conn1"))

	assert.False(t, m.UnbindUser("u1", "conn1"), "conn2 keeps the user map alive")

	_, ok, err := m.UseAuthorizedChannel("ch1", "conn1",
		func(ChannelInfo) bool { return true },
		func(ChannelInfo) error { return nil })
	require.NoError(t, err)
	assert.False(t, ok, "reuse must refuse a matching but unbound ConnID")

	got, exists := m.GetChannelInfo("ch1")
	require.True(t, exists)
	assert.Equal(t, "conn1", got.ConnID, "refuse must not clear the stale binding")
}

// TestUseAuthorizedChannel_RefuseRebindWhenTargetConnGone: a rebind to a conn
// that was unbound must fail under Lock and leave the prior ConnID intact.
func TestUseAuthorizedChannel_RefuseRebindWhenTargetConnGone(t *testing.T) {
	m := New(0)
	m.RegisterWithAuthInfo("ch1", "w1", "u1", AuthInfo{}, nil)
	m.BindUser("u1", "conn-old", noopSender, nil)
	m.BindUser("u1", "conn-gone", noopSender, nil)
	require.True(t, bindChannelConn(t, m, "ch1", "conn-old"))

	assert.Empty(t, m.UnbindUserAndCleanup("u1", "conn-gone"))

	_, ok, err := m.UseAuthorizedChannel("ch1", "conn-gone",
		func(ChannelInfo) bool { return true },
		func(ChannelInfo) error { return nil })
	require.NoError(t, err)
	assert.False(t, ok)

	got, exists := m.GetChannelInfo("ch1")
	require.True(t, exists)
	assert.Equal(t, "conn-old", got.ConnID, "failed rebind must preserve the prior binding")
}

// TestUseChannelIf_RejectAuthorizeLeavesChannel: nil-bind peek path must reject
// under RLock without ever taking the write upgrade. Concurrent GetChannelInfo
// while authorize runs proves the critical section is still a shared RLock.
func TestUseChannelIf_RejectAuthorizeLeavesChannel(t *testing.T) {
	m := New(0)
	m.RegisterWithAuthInfo("ch1", "w1", "u1", AuthInfo{}, nil)

	authorizeEntered := make(chan struct{})
	releaseAuthorize := make(chan struct{})
	readerDone := make(chan struct{})

	go func() {
		<-authorizeEntered
		_, infoOK := m.GetChannelInfo("ch1")
		assert.True(t, infoOK)
		close(readerDone)
	}()

	var calls atomic.Int32
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, ok, err := m.UseChannelIf("ch1",
			func(ChannelInfo) bool {
				calls.Add(1)
				close(authorizeEntered)
				<-releaseAuthorize
				return false
			},
			func(ChannelInfo) error { return nil })
		assert.NoError(t, err)
		assert.False(t, ok)
	}()

	waitOrDeadlock(t, "nil-bind reject took write lock (blocked concurrent GetChannelInfo)", func() {
		<-readerDone
	})
	close(releaseAuthorize)
	waitOrDeadlock(t, "nil-bind reject did not finish", func() {
		<-done
	})
	assert.Equal(t, int32(1), calls.Load())
	assert.True(t, m.Exists("ch1"))
}

// TestUseAuthorizedChannel_AuthorizePanicOnBindLockReleasesLocks ensures a
// panic in authorize under the Lock bind path still releases m.mu and opMu
// and leaves ConnID unbound.
func TestUseAuthorizedChannel_AuthorizePanicOnBindLockReleasesLocks(t *testing.T) {
	m := New(0)
	m.RegisterWithAuthInfo("ch1", "w1", "u1", AuthInfo{}, nil)
	m.BindUser("u1", "conn1", noopSender, nil)

	var calls atomic.Int32
	func() {
		defer func() { _ = recover() }()
		_, _, _ = m.UseAuthorizedChannel("ch1", "conn1",
			func(ChannelInfo) bool {
				calls.Add(1)
				panic("boom on bind authorize")
			},
			nil)
	}()
	assert.Equal(t, int32(1), calls.Load(), "panic must occur on the Lock bind authorize")

	got, exists := m.GetChannelInfo("ch1")
	require.True(t, exists)
	assert.Empty(t, got.ConnID, "panic before ConnID write must leave the channel unbound")

	waitOrDeadlock(t, "Register blocked: bind authorize panic leaked m.mu", func() {
		m.RegisterWithAuthInfo("ch2", "w2", "u2", AuthInfo{}, nil)
	})
	waitOrDeadlock(t, "CloseByID blocked: bind authorize panic leaked opMu", func() {
		m.CloseByID("ch1")
	})
	assert.False(t, m.Exists("ch1"))
}

// TestUseAuthorizedChannel_AuthorizePanicOnReuseReleasesLocks covers Ready-path
// authorize panic with a matching ConnID (not only nil-bind UseChannelIf).
func TestUseAuthorizedChannel_AuthorizePanicOnReuseReleasesLocks(t *testing.T) {
	m := New(0)
	m.RegisterWithAuthInfo("ch1", "w1", "u1", AuthInfo{}, nil)
	m.BindUser("u1", "conn1", noopSender, nil)
	require.True(t, bindChannelConn(t, m, "ch1", "conn1"))

	var calls atomic.Int32
	func() {
		defer func() { _ = recover() }()
		_, _, _ = m.UseAuthorizedChannel("ch1", "conn1",
			func(ChannelInfo) bool {
				calls.Add(1)
				panic("boom on reuse authorize")
			},
			nil)
	}()
	assert.Equal(t, int32(1), calls.Load())

	got, exists := m.GetChannelInfo("ch1")
	require.True(t, exists)
	assert.Equal(t, "conn1", got.ConnID, "reuse panic must not clear the binding")

	waitOrDeadlock(t, "Register blocked: reuse authorize panic leaked m.mu", func() {
		m.RegisterWithAuthInfo("ch2", "w2", "u2", AuthInfo{}, nil)
	})
	waitOrDeadlock(t, "CloseByID blocked: reuse authorize panic leaked opMu", func() {
		m.CloseByID("ch1")
	})
}

// TestUseAuthorizedChannel_HasUserConnIgnoresNilSendFn: bind presence keys off
// registration, not SendFunc — a registered conn with nil sendFn still binds.
func TestUseAuthorizedChannel_HasUserConnIgnoresNilSendFn(t *testing.T) {
	m := New(0)
	m.RegisterWithAuthInfo("ch1", "w1", "u1", AuthInfo{}, nil)
	m.BindUser("u1", "conn1", nil, nil)

	info, ok, err := m.UseAuthorizedChannel("ch1", "conn1",
		func(ChannelInfo) bool { return true },
		func(ChannelInfo) error { return nil })
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "conn1", info.ConnID)
}

// TestSendToFrontendIf_AuthorizeRejectSharesLiveGate: SendToFrontendIf must
// refuse via the shared liveAndAuthorizeLocked helper (same gate as useChannel).
func TestSendToFrontendIf_AuthorizeRejectSharesLiveGate(t *testing.T) {
	m := New(0)
	m.RegisterWithAuthInfo("ch1", "w1", "u1", AuthInfo{}, nil)
	m.BindUser("u1", "conn1", noopSender, nil)
	require.True(t, bindChannelConn(t, m, "ch1", "conn1"))

	var calls atomic.Int32
	ok := m.SendToFrontendIf(
		&leapmuxv1.ChannelMessage{ChannelId: "ch1", Ciphertext: []byte("x")},
		func(ChannelInfo) bool {
			calls.Add(1)
			return false
		},
	)
	assert.False(t, ok)
	assert.Equal(t, int32(1), calls.Load())
	assert.True(t, m.Exists("ch1"))
}

// TestUnbindUserAndCleanup_BlankUserClosesNothing pins the fail-closed
// direction of the owner predicate.
//
// The comparison used to be a raw `ch.UserID != userID`, which treats
// blank-vs-blank as a MATCH -- so a blank caller id would have selected every
// blank-owner channel in the manager for teardown. Matches never matches a
// blank, so an unidentifiable caller closes nothing. This is a destructive
// sweep; the safe answer to "who is this?" is "nobody's channels".
func TestUnbindUserAndCleanup_BlankUserClosesNothing(t *testing.T) {
	m := New(0)

	m.BindUser("", "conn1", noopSender, nil)
	m.RegisterWithAuthInfo("ch_blank_owner", "w1", "", AuthInfo{}, nil)
	bindChannelConn(t, m, "ch_blank_owner", "conn1")

	removed := m.UnbindUserAndCleanup("", "conn1")
	assert.Empty(t, removed, "a blank user id must close nothing at all")
	assert.True(t, m.Exists("ch_blank_owner"), "the blank-owner channel survives")

	// Control: a real owner still closes its own channel, so the assertion
	// above means "refused" rather than "this fixture closes nothing".
	m.BindUser("u1", "conn2", noopSender, nil)
	m.RegisterWithAuthInfo("ch_owned", "w1", "u1", AuthInfo{}, nil)
	bindChannelConn(t, m, "ch_owned", "conn2")
	removed = m.UnbindUserAndCleanup("u1", "conn2")
	assert.NotEmpty(t, removed, "control: the owning id still unbinds its channel")
}
