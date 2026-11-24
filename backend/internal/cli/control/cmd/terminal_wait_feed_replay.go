package cmd

import (
	"context"

	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control/streamevents"
)

const (
	maxScreenFramesBeforeReplay = 512
	maxScreenBytesBeforeReplay  = 1 << 20
)

// screenReplayForwarder holds live output while the Worker captures replay.
// It clips that overlap only after the replay has established both cursors.
type screenReplayForwarder struct {
	feed         *screenFeed
	position     terminalScreenPosition
	replayed     bool
	pending      []screenFeedItem
	pendingBytes int
}

func (f *screenReplayForwarder) receive(ctx context.Context, frame screenFeedItem) bool {
	if frame.kind != screenFeedEvent {
		return f.feed.send(ctx, frame)
	}
	data := frame.event.GetData()
	if timeline := data.GetScreenTimeline(); timeline != nil {
		if !f.feed.send(ctx, frame) {
			return false
		}
		geometry := timeline.GetBase()
		if changes := timeline.GetChanges(); len(changes) != 0 {
			geometry = changes[len(changes)-1]
		}
		f.position = terminalScreenPosition{epoch: geometry.GetEpoch(), offset: data.GetEndOffset(), revision: geometry.GetRevision()}
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
		f.feed.send(ctx, screenFeedItem{kind: screenFeedFailed, err: cli.Newf(cli.CodeUnavailable,
			"terminal %s produced too much screen output before replay finished", f.feed.terminalID).WithExitCode(cli.ExitWaitEnded)})
		return false
	}
	f.pending = append(f.pending, frame)
	return true
}

func (f *screenReplayForwarder) deliverLive(ctx context.Context, frame screenFeedItem) bool {
	position := f.position
	if geometry := frame.event.GetScreenGeometry(); geometry != nil {
		if geometry.GetEpoch() == position.epoch {
			if geometry.GetRevision() <= position.revision {
				return true
			}
			if geometry.GetOffset() != position.offset || geometry.GetRevision() != position.revision+1 {
				return false
			}
		} else if geometry.GetEpoch() == "" || geometry.GetRevision() != 1 || geometry.GetOffset() < 0 {
			return false
		}
		position = terminalScreenPosition{epoch: geometry.GetEpoch(), offset: geometry.GetOffset(), revision: geometry.GetRevision()}
	} else if data := frame.event.GetData(); data != nil {
		if data.GetScreenEpoch() != position.epoch {
			return false
		}
		frame.event = streamevents.ClipTerminalOutput(frame.event, position.offset)
		if frame.event == nil {
			return true
		}
		data = frame.event.GetData()
		if data.GetGeometryRevision() != position.revision ||
			position.epoch != "" && (data.GetIsSnapshot() || data.GetEndOffset()-position.offset != int64(len(data.GetData()))) {
			return false
		}
		position.offset = data.GetEndOffset()
	}
	if !f.feed.send(ctx, frame) {
		return false
	}
	f.position = position
	return true
}
