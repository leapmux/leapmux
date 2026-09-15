package service

import (
	"sync"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/stretchr/testify/require"
)

func TestWatcherOwnershipRejectsAnUninitializedSession(t *testing.T) {
	manager := NewWatcherManager()
	require.False(t, manager.isOwner("channel", 0))
	sender := newTestWatcher("channel")
	manager.SetAgentWatchesForSession("channel", 0, []watchEntry{{id: "agent", mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}}, sender)
	manager.SetTerminalWatchesForSession("channel", 0, []watchEntry{{id: "terminal", mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}}, sender)
	require.Empty(t, manager.AgentModesForChannel("channel"))
	require.Empty(t, manager.TerminalModesForChannel("channel"))
}

// Concurrent stream replacement must leave both subscriptions on the latest session.
func TestWatcherOwnershipSurvivesConcurrentReplacement(t *testing.T) {
	for round := range 40 {
		manager := NewWatcherManager()
		var completed sync.WaitGroup
		var latestMu sync.Mutex
		var latestID uint64
		var latest *mockResponseWriter
		start := make(chan struct{})
		for range 16 {
			completed.Go(func() {
				<-start
				var previous uint64
				for range 32 {
					if previous != 0 {
						manager.UnwatchSession("channel", previous)
					}
					sender := newTestWatcher("channel")
					id := manager.BeginSession("channel")
					latestMu.Lock()
					if id > latestID {
						latestID, latest = id, sender
					}
					latestMu.Unlock()
					manager.SetAgentWatchesForSession("channel", id, []watchEntry{{id: "agent", mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}}, sender)
					manager.SetTerminalWatchesForSession("channel", id, []watchEntry{{id: "terminal", mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}}, sender)
					previous = id
				}
			})
		}
		close(start)
		completed.Wait()
		require.True(t, manager.isOwner("channel", latestID))
		manager.BroadcastAgentEvent("agent", &leapmuxv1.AgentEvent{
			AgentId: "agent",
			Event: &leapmuxv1.AgentEvent_ControlRequest{ControlRequest: &leapmuxv1.AgentControlRequest{
				AgentId: "agent", RequestId: "pending-question",
			}},
		})
		manager.BroadcastTerminalEvent("terminal", testTerminalEvent("terminal", []byte("output")))
		require.Equal(t, int64(2), latest.streamCount.Load(), "round %d: both events must reach the latest session", round)
	}
}
