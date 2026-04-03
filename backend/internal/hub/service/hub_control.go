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
// frame broadcasting. Multiple events for the same user within this window
// are batched into a single frame.
const DefaultDebounceInterval = 3 * time.Second

// hubControlEvent is a function that appends its event to the given frame,
// returning true if the event was not already present.
type hubControlEvent func(frame *leapmuxv1.HubControlFrame) bool

// pendingFlush holds the accumulated events and timer for a single user.
type pendingFlush struct {
	timer  *time.Timer
	events []hubControlEvent
}

// HubEventBroadcaster debounces and sends hub control frames to frontends
// via the channel manager. Multiple events for the same user within the
// debounce window are batched into a single frame.
type HubEventBroadcaster struct {
	cMgr     *channelmgr.Manager
	interval time.Duration

	mu      sync.Mutex
	pending map[string]*pendingFlush // userID -> pending
}

// NewHubEventBroadcaster creates a new broadcaster with the default debounce interval.
func NewHubEventBroadcaster(cMgr *channelmgr.Manager) *HubEventBroadcaster {
	return &HubEventBroadcaster{
		cMgr:     cMgr,
		interval: DefaultDebounceInterval,
		pending:  make(map[string]*pendingFlush),
	}
}

// SetDebounceInterval overrides the debounce window (useful for testing).
func (b *HubEventBroadcaster) SetDebounceInterval(d time.Duration) {
	b.interval = d
}

// NotifyWorkersChanged schedules a WorkersChanged event for the specified
// user. The event is batched with any other pending events for this user
// and sent when the debounce timer fires.
func (b *HubEventBroadcaster) NotifyWorkersChanged(userID string) {
	if b == nil || b.cMgr == nil {
		return
	}
	b.enqueue(userID, func(frame *leapmuxv1.HubControlFrame) bool {
		// Deduplicate: skip if a WorkersChanged event is already pending.
		for _, e := range frame.Events {
			if e.GetWorkersChanged() != nil {
				return false
			}
		}
		frame.Events = append(frame.Events, &leapmuxv1.HubControlEvent{
			Kind: &leapmuxv1.HubControlEvent_WorkersChanged{
				WorkersChanged: &leapmuxv1.WorkersChanged{},
			},
		})
		return true
	})
}

// enqueue adds an event for the given user and resets the debounce timer.
func (b *HubEventBroadcaster) enqueue(userID string, evt hubControlEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()

	p := b.pending[userID]
	if p == nil {
		p = &pendingFlush{}
		b.pending[userID] = p
	}

	p.events = append(p.events, evt)

	if p.timer != nil {
		p.timer.Stop()
	}
	p.timer = time.AfterFunc(b.interval, func() {
		b.flush(userID)
	})
}

// flush builds a HubControlFrame from accumulated events and sends it.
func (b *HubEventBroadcaster) flush(userID string) {
	b.mu.Lock()
	p := b.pending[userID]
	delete(b.pending, userID)
	b.mu.Unlock()

	if p == nil {
		return
	}

	frame := &leapmuxv1.HubControlFrame{}
	for _, evt := range p.events {
		evt(frame)
	}
	if len(frame.Events) == 0 {
		return
	}

	data, err := proto.Marshal(frame)
	if err != nil {
		slog.Error("failed to marshal HubControlFrame", "error", err)
		return
	}
	b.cMgr.SendToUser(userID, &leapmuxv1.ChannelMessage{
		ProtocolVersion: 1,
		ChannelId:       channelmgr.HubControlChannelID,
		Ciphertext:      data,
	})
}
