package channelmgr

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

var noopSender = func(*leapmuxv1.ChannelMessage) error { return nil }

func TestRegisterAndExists(t *testing.T) {
	m := New()
	assert.False(t, m.Exists("ch1"))

	m.Register("ch1", "w1", "u1", nil)
	assert.True(t, m.Exists("ch1"))
	assert.Equal(t, "w1", m.GetWorkerID("ch1"))
	assert.Equal(t, "u1", m.GetUserID("ch1"))
}

func TestUnregister(t *testing.T) {
	m := New()
	cancelled := false
	m.Register("ch1", "w1", "u1", func() { cancelled = true })

	m.Unregister("ch1")
	assert.False(t, m.Exists("ch1"))
	assert.True(t, cancelled)
}

func TestUnregisterByWorker(t *testing.T) {
	m := New()
	var cancelledIDs []string
	var mu sync.Mutex

	for _, id := range []string{"ch1", "ch2", "ch3"} {
		channelID := id
		m.Register(channelID, "w1", "u1", func() {
			mu.Lock()
			cancelledIDs = append(cancelledIDs, channelID)
			mu.Unlock()
		})
	}
	m.Register("ch4", "w2", "u2", nil) // Different worker.

	removed := m.UnregisterByWorker("w1")
	assert.Len(t, removed, 3)
	assert.True(t, m.Exists("ch4"))
	assert.False(t, m.Exists("ch1"))
	assert.False(t, m.Exists("ch2"))
	assert.False(t, m.Exists("ch3"))
	assert.Len(t, cancelledIDs, 3)
}

func TestGetWorkerIDNonexistent(t *testing.T) {
	m := New()
	assert.Empty(t, m.GetWorkerID("nonexistent"))
	assert.Empty(t, m.GetUserID("nonexistent"))
}

func TestSetChannelConn_TargetedRouting(t *testing.T) {
	m := New()
	m.Register("ch1", "w1", "u1", nil)

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
	assert.True(t, m.SetChannelConn("ch1", "conn1"))

	// SendToFrontend should only go to conn1.
	msg := &leapmuxv1.ChannelMessage{ChannelId: "ch1", Ciphertext: []byte("targeted")}
	assert.True(t, m.SendToFrontend(msg))

	assert.Len(t, received1, 1)
	assert.Len(t, received2, 0) // conn2 should NOT receive it.
	assert.Equal(t, []byte("targeted"), received1[0].GetCiphertext())
}

func TestSetChannelConn_Nonexistent(t *testing.T) {
	m := New()
	assert.False(t, m.SetChannelConn("nonexistent", "conn1"))
}

func TestSendToFrontend_NoConnID(t *testing.T) {
	m := New()
	m.Register("ch1", "w1", "u1", nil)

	// Channel without SetChannelConn — SendToFrontend should return false
	// because there's no route. In practice this never happens because the
	// worker only responds to frontend-initiated requests, and SetChannelConn
	// is called when the relay processes the first frontend→worker message.
	m.BindUser("u1", "conn1", func(msg *leapmuxv1.ChannelMessage) error {
		return nil
	}, nil)

	msg := &leapmuxv1.ChannelMessage{ChannelId: "ch1", Ciphertext: []byte("no-route")}
	assert.False(t, m.SendToFrontend(msg))
}

func TestSendToFrontend_WithConnID(t *testing.T) {
	m := New()
	m.Register("ch1", "w1", "u1", nil)

	var received []*leapmuxv1.ChannelMessage
	m.BindUser("u1", "conn1", func(msg *leapmuxv1.ChannelMessage) error {
		received = append(received, msg)
		return nil
	}, nil)

	m.SetChannelConn("ch1", "conn1")

	msg := &leapmuxv1.ChannelMessage{ChannelId: "ch1", Ciphertext: []byte("routed")}
	assert.True(t, m.SendToFrontend(msg))
	assert.Len(t, received, 1)

	// Also works when a new channel is registered and associated.
	m.Register("ch2", "w1", "u1", nil)
	m.SetChannelConn("ch2", "conn1")

	msg2 := &leapmuxv1.ChannelMessage{ChannelId: "ch2", Ciphertext: []byte("also-routed")}
	assert.True(t, m.SendToFrontend(msg2))
	assert.Len(t, received, 2)
}

func TestMultipleConnections_EachChannelTargeted(t *testing.T) {
	m := New()
	m.Register("ch1", "w1", "u1", nil)
	m.Register("ch2", "w1", "u1", nil)

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
	m.SetChannelConn("ch1", "conn1")
	m.SetChannelConn("ch2", "conn2")

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
	m := New()
	m.Register("ch1", "w1", "u1", nil)

	var received1, received2 []*leapmuxv1.ChannelMessage
	m.BindUser("u1", "conn1", func(msg *leapmuxv1.ChannelMessage) error {
		received1 = append(received1, msg)
		return nil
	}, nil)
	m.BindUser("u1", "conn2", func(msg *leapmuxv1.ChannelMessage) error {
		received2 = append(received2, msg)
		return nil
	}, nil)

	m.SetChannelConn("ch1", "conn2")

	// Unbind conn1 — conn2 should still work.
	m.UnbindUser("u1", "conn1")

	msg := &leapmuxv1.ChannelMessage{ChannelId: "ch1", Ciphertext: []byte("after-unbind")}
	assert.True(t, m.SendToFrontend(msg))

	assert.Len(t, received1, 0)
	assert.Len(t, received2, 1)
}

func TestUnbindUser_LastConnection(t *testing.T) {
	m := New()
	m.Register("ch1", "w1", "u1", nil)

	m.BindUser("u1", "conn1", func(msg *leapmuxv1.ChannelMessage) error {
		return nil
	}, nil)
	m.SetChannelConn("ch1", "conn1")

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
	m := New()
	cancelled := false
	m.BindUser("u1", "conn1", func(msg *leapmuxv1.ChannelMessage) error {
		return nil
	}, func() { cancelled = true })

	m.UnbindUser("u1", "conn1")
	assert.True(t, cancelled)
}

func TestUnregisterByWorker_DoesNotCloseUserSender(t *testing.T) {
	m := New()

	userCancelled := false
	m.BindUser("u1", "conn1", func(msg *leapmuxv1.ChannelMessage) error {
		return nil
	}, func() { userCancelled = true })

	m.Register("ch1", "w1", "u1", nil)
	m.Register("ch2", "w1", "u1", nil)

	// UnregisterByWorker removes channels but should NOT cancel user sender.
	removed := m.UnregisterByWorker("w1")
	assert.Len(t, removed, 2)
	assert.False(t, userCancelled)

	// New channels for same user should still work after SetChannelConn.
	m.Register("ch3", "w2", "u1", nil)
	m.SetChannelConn("ch3", "conn1")
	msg := &leapmuxv1.ChannelMessage{ChannelId: "ch3", Ciphertext: []byte("still works")}
	assert.True(t, m.SendToFrontend(msg))
}

func TestUnregister_SendsCloseNotification(t *testing.T) {
	m := New()

	var received []*leapmuxv1.ChannelMessage
	m.BindUser("u1", "conn1", func(msg *leapmuxv1.ChannelMessage) error {
		received = append(received, msg)
		return nil
	}, nil)

	m.Register("ch1", "w1", "u1", nil)
	m.SetChannelConn("ch1", "conn1")

	// Unregister should send a close notification (empty ciphertext) to conn1.
	m.Unregister("ch1")

	if assert.Len(t, received, 1) {
		assert.Equal(t, "ch1", received[0].GetChannelId())
		assert.Empty(t, received[0].GetCiphertext()) // Empty = close notification.
	}
}

func TestUnregister_CloseNotification_NoConnID(t *testing.T) {
	m := New()

	// Channel without SetChannelConn — close notification cannot be sent
	// because we don't know which connection to target. This is acceptable;
	// the channel was never associated, so no frontend is waiting for it.
	var received []*leapmuxv1.ChannelMessage
	m.BindUser("u1", "conn1", func(msg *leapmuxv1.ChannelMessage) error {
		received = append(received, msg)
		return nil
	}, nil)

	m.Register("ch1", "w1", "u1", nil)
	// No SetChannelConn — close notification goes nowhere.
	m.Unregister("ch1")

	assert.Len(t, received, 0)
}

func TestUnbindUserAndCleanup_RemovesBoundAndUnboundChannels(t *testing.T) {
	m := New()

	m.BindUser("u1", "conn1", noopSender, nil)

	var boundCancelled, unboundCancelled bool
	m.Register("ch_bound", "w1", "u1", func() { boundCancelled = true })
	m.SetChannelConn("ch_bound", "conn1")
	m.Register("ch_unbound", "w1", "u1", func() { unboundCancelled = true })
	m.Register("ch_other_user", "w1", "u2", nil)

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
	m := New()

	m.BindUser("u1", "old", noopSender, nil)
	m.BindUser("u1", "new", noopSender, nil)

	m.Register("ch_old_bound", "w1", "u1", nil)
	m.SetChannelConn("ch_old_bound", "old")
	m.Register("ch_new_unbound", "w1", "u1", nil)

	removed := m.UnbindUserAndCleanup("u1", "old")

	assert.Len(t, removed, 1)
	assert.Equal(t, "ch_old_bound", removed[0].ChannelID)
	assert.False(t, m.Exists("ch_old_bound"))
	assert.True(t, m.Exists("ch_new_unbound"), "unbound channel must survive while another conn exists")
}

func TestUnbindUserAndCleanup_UnknownConn(t *testing.T) {
	m := New()
	m.Register("ch1", "w1", "u1", nil)

	removed := m.UnbindUserAndCleanup("u1", "never-bound")

	// No conn for user → unbound channel is swept.
	assert.Len(t, removed, 1)
	assert.Equal(t, "ch1", removed[0].ChannelID)
}

func TestUnbindUserAndCleanup_CallsConnCancel(t *testing.T) {
	m := New()

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
		m := New()
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
			m.Register(newChannelID, "w1", "u1", nil)
		}()

		wg.Wait()

		// "new" always finishes binding+registering, so the new channel
		// must always exist after both goroutines complete.
		assert.True(t, m.Exists(newChannelID),
			"iteration %d: ch_new must survive concurrent cleanup", i)
	}
}

func TestUnbindUser_ReturnsNoConns(t *testing.T) {
	m := New()
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
	m := New()

	var received []*leapmuxv1.ChannelMessage
	m.BindUser("u1", "conn1", func(msg *leapmuxv1.ChannelMessage) error {
		received = append(received, msg)
		return nil
	}, nil)

	m.Register("ch1", "w1", "u1", nil)
	m.Register("ch2", "w1", "u1", nil)
	m.Register("ch3", "w2", "u1", nil) // Different worker, should not be removed.

	m.SetChannelConn("ch1", "conn1")
	m.SetChannelConn("ch2", "conn1")

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
	m := New()

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
	m := New()

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
	m := New()

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
	m := New()
	// Should not panic on unknown user.
	msg := &leapmuxv1.ChannelMessage{ChannelId: HubControlChannelID, Ciphertext: []byte("ctrl")}
	m.SendToUser("nonexistent", msg)
}
