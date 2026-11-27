package cmd

import (
	"context"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli/control/streamevents"
)

const (
	maxScreenFramesBeforeReplay = 512
	maxScreenBytesBeforeReplay  = 1 << 20
)

type terminalReplayForwarder interface {
	receive(ctx context.Context, frame screenFeedItem) bool
	nextPosition() terminalScreenPosition
}

// screenReplayForwarder holds live output while the Worker captures replay.
// It clips that overlap only after the replay has established both cursors.
type screenReplayForwarder struct {
	feed         *screenFeed
	position     terminalScreenPosition
	replayed     bool
	pending      []screenFeedItem
	pendingBytes int
}

func (f *screenReplayForwarder) nextPosition() terminalScreenPosition {
	return f.position
}

func (f *screenReplayForwarder) receive(ctx context.Context, frame screenFeedItem) bool {
	if frame.event == nil {
		return f.feed.send(ctx, frame)
	}
	data := frame.event.GetData()
	if timeline := data.GetScreenTimeline(); timeline != nil {
		if !f.feed.send(ctx, frame) {
			return false
		}
		f.position = terminalScreenPositionFromTimeline(timeline, data.GetEndOffset())
		f.replayed = true
		for _, pending := range f.pending {
			epoch := pending.event.GetData().GetScreenEpoch()
			if resize := pending.event.GetScreenGeometry(); resize != nil {
				epoch = resize.GetEpoch()
			}
			if pending.event.GetClosed() == nil && f.position.epoch != "" && epoch != f.position.epoch {
				// The replay cannot prove which side of its cut these bytes
				// belong to. Resubscribe from the position we did deliver.
				return false
			}
			if !f.deliverLive(ctx, pending) {
				return false
			}
		}
		f.pending = nil
		f.pendingBytes = 0
		return true
	}
	if f.replayed {
		return f.deliverLive(ctx, frame)
	}
	if data == nil && frame.event.GetScreenGeometry() == nil && frame.event.GetClosed() == nil {
		// Replay supplies the current shell status. Keep live exits because
		// they report the shell's exit code.
		return true
	}
	f.pendingBytes += len(data.GetData())
	if len(f.pending) >= maxScreenFramesBeforeReplay || f.pendingBytes > maxScreenBytesBeforeReplay {
		f.feed.send(ctx, screenFeedItem{overflow: true})
		return false
	}
	f.pending = append(f.pending, frame)
	return true
}

func (f *screenReplayForwarder) deliverLive(ctx context.Context, frame screenFeedItem) bool {
	position := f.position
	if geometry := frame.event.GetScreenGeometry(); geometry != nil {
		next, stale, ok := position.applyLiveGeometry(geometry)
		if !ok {
			return false
		}
		if stale {
			return true
		}
		position = next
	} else if data := frame.event.GetData(); data != nil {
		if data.GetScreenEpoch() != position.epoch {
			return false
		}
		frame.event = streamevents.ClipTerminalOutput(frame.event, position.offset)
		if frame.event == nil {
			return true
		}
		next, ok := position.advanceOutput(frame.event.GetData())
		if !ok {
			return false
		}
		position = next
	}
	if !f.feed.send(ctx, frame) {
		return false
	}
	f.position = position
	return true
}

// outputReplayForwarder holds live terminal bytes while the Worker captures a
// non-geometry catch-up frame (ScreenGeometry == false). It uses
// TerminalData.ReplayComplete as the replay barrier, delivers the replay frame
// first, and then drains buffered live chunks strictly past the replay offset.
type outputReplayForwarder struct {
	feed         *screenFeed
	offset       int64
	replayed     bool
	pending      []screenFeedItem
	pendingBytes int
}

func (f *outputReplayForwarder) nextPosition() terminalScreenPosition {
	return terminalScreenPosition{offset: f.offset}
}

func (f *outputReplayForwarder) receive(ctx context.Context, frame screenFeedItem) bool {
	if frame.event == nil {
		return f.feed.send(ctx, frame)
	}
	data := frame.event.GetData()
	if data != nil && data.GetReplayComplete() {
		if data.GetEndOffset() < 0 {
			return true
		}
		switch {
		case data.GetIsSnapshot():
			if !f.feed.send(ctx, frame) {
				return false
			}
			f.offset = data.GetEndOffset()
		case len(data.GetData()) > 0:
			clipped := streamevents.ClipTerminalOutput(frame.event, f.offset)
			if clipped != nil {
				clipped.GetData().ReplayComplete = true
				if !f.feed.send(ctx, screenFeedItem{event: clipped}) {
					return false
				}
				f.offset = clipped.GetData().GetEndOffset()
			} else if !f.replayed {
				if !f.feed.send(ctx, screenFeedItem{event: &leapmuxv1.TerminalEvent{
					TerminalId: frame.event.GetTerminalId(),
					Event: &leapmuxv1.TerminalEvent_Data{Data: &leapmuxv1.TerminalData{
						EndOffset:      f.offset,
						ReplayComplete: true,
					}},
				}}) {
					return false
				}
			}
		default:
			if data.GetEndOffset() > f.offset {
				f.offset = data.GetEndOffset()
			}
			if !f.feed.send(ctx, frame) {
				return false
			}
		}
		f.replayed = true
		for _, pending := range f.pending {
			if !f.deliverLive(ctx, pending) {
				return false
			}
		}
		f.pending = nil
		f.pendingBytes = 0
		return true
	}
	if f.replayed {
		return f.deliverLive(ctx, frame)
	}
	if data == nil && frame.event.GetClosed() == nil {
		// Replay supplies the current shell status. Keep live exits because
		// they report the shell's exit code.
		return true
	}
	f.pendingBytes += len(data.GetData())
	if len(f.pending) >= maxScreenFramesBeforeReplay || f.pendingBytes > maxScreenBytesBeforeReplay {
		f.feed.send(ctx, screenFeedItem{overflow: true})
		return false
	}
	f.pending = append(f.pending, frame)
	return true
}

func (f *outputReplayForwarder) deliverLive(ctx context.Context, frame screenFeedItem) bool {
	if frame.event.GetScreenGeometry() != nil {
		return true
	}
	nextOffset := f.offset
	if data := frame.event.GetData(); data != nil {
		frame.event = streamevents.ClipTerminalOutput(frame.event, f.offset)
		if frame.event == nil {
			return true
		}
		nextOffset = frame.event.GetData().GetEndOffset()
	}
	if !f.feed.send(ctx, frame) {
		return false
	}
	f.offset = nextOffset
	return true
}
