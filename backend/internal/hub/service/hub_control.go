package service

import (
	"log/slog"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/channelmgr"
)

// DefaultDebounceInterval is the default debounce window for hub control
// frame broadcasting. Multiple events of the same type for the same user
// within this window are consolidated into a single frame.
const DefaultDebounceInterval = 3 * time.Second

// HubEventBroadcaster debounces and sends hub control frames to frontends
// via the channel manager. Multiple calls for the same user and event type
// within the debounce window are consolidated into a single send.
type HubEventBroadcaster struct {
	cMgr     *channelmgr.Manager
	interval time.Duration

	mu     sync.Mutex
	timers map[string]*time.Timer // key: userID + ":" + eventType
}

// NewHubEventBroadcaster creates a new broadcaster with the default debounce interval.
func NewHubEventBroadcaster(cMgr *channelmgr.Manager) *HubEventBroadcaster {
	return &HubEventBroadcaster{
		cMgr:     cMgr,
		interval: DefaultDebounceInterval,
		timers:   make(map[string]*time.Timer),
	}
}

// SetDebounceInterval overrides the debounce window (useful for testing).
func (b *HubEventBroadcaster) SetDebounceInterval(d time.Duration) {
	b.interval = d
}

// NotifyWorkersChanged schedules a WorkersChanged control frame to the
// specified user. If called again for the same user within the debounce
// window, the timer resets and only one frame is sent.
func (b *HubEventBroadcaster) NotifyWorkersChanged(userID string) {
	if b == nil || b.cMgr == nil {
		return
	}
	b.debounce("workersChanged:"+userID, func() {
		sendWorkersChanged(b.cMgr, userID)
	})
}

// debounce resets or creates a timer for the given key. When the timer
// fires, fn is called exactly once.
func (b *HubEventBroadcaster) debounce(key string, fn func()) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if t, ok := b.timers[key]; ok {
		t.Stop()
	}
	b.timers[key] = time.AfterFunc(b.interval, func() {
		b.mu.Lock()
		delete(b.timers, key)
		b.mu.Unlock()
		fn()
	})
}

// sendWorkersChanged marshals and sends a WorkersChanged control frame.
func sendWorkersChanged(cMgr *channelmgr.Manager, userID string) {
	frame := &leapmuxv1.HubControlFrame{
		Event: &leapmuxv1.HubControlFrame_WorkersChanged{
			WorkersChanged: &leapmuxv1.WorkersChanged{},
		},
	}
	data, err := proto.Marshal(frame)
	if err != nil {
		slog.Error("failed to marshal HubControlFrame", "error", err)
		return
	}
	cMgr.SendToUser(userID, &leapmuxv1.ChannelMessage{
		ProtocolVersion: 1,
		ChannelId:       channelmgr.HubControlChannelID,
		Ciphertext:      data,
	})
}
