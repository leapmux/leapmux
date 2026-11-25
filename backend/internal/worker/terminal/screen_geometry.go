package terminal

import (
	"slices"

	"github.com/leapmux/leapmux/internal/util/id"
)

// ScreenGeometry is one size at a PTY byte offset. Revision orders sizes
// even when successive resizes have no output between them.
type ScreenGeometry struct {
	Epoch      string
	Offset     int64
	Revision   uint64
	Cols, Rows uint16
}

// ScreenTimeline describes the geometry of real bytes at and after StartOffset.
// A replacement may have synthesized mode bytes that do not count as output.
type ScreenTimeline struct {
	Base             ScreenGeometry
	Changes          []ScreenGeometry
	StartOffset      int64
	ModePrefix       []byte
	HistoryTruncated bool
}

// ScreenReplay supplies the requested continuation or an explicit replacement.
type ScreenReplay struct {
	Data      []byte
	EndOffset int64
	Snapshot  bool
	Timeline  ScreenTimeline
}

const retainedScreenResizeLimit = 256

// screenGeometryHistory belongs to the same lock as the ring data. Its owner
// identifies the PTY that can change the geometry of a shared buffer.
type screenGeometryHistory struct {
	owner   uint64
	base    ScreenGeometry
	changes []ScreenGeometry
}

func (h *screenGeometryHistory) current() ScreenGeometry {
	if len(h.changes) != 0 {
		return h.changes[len(h.changes)-1]
	}
	return h.base
}

func (h *screenGeometryHistory) record(offset int64, cols, rows uint16) (ScreenGeometry, bool) {
	if h.base.Epoch == "" {
		h.base = ScreenGeometry{Epoch: id.Generate(), Offset: offset, Revision: 1, Cols: cols, Rows: rows}
		return h.base, true
	}
	latest := h.current()
	if cols == latest.Cols && rows == latest.Rows {
		return latest, false
	}
	latest.Cols, latest.Rows, latest.Offset = cols, rows, offset
	latest.Revision++
	h.changes = append(h.changes, latest)
	if excess := len(h.changes) - retainedScreenResizeLimit; excess > 0 {
		h.base = h.changes[excess-1]
		copy(h.changes, h.changes[excess:])
		h.changes = h.changes[:retainedScreenResizeLimit]
	}
	return latest, true
}

func (h *screenGeometryHistory) retained(oldestOffset int64) (ScreenGeometry, []ScreenGeometry, int64) {
	start := max(oldestOffset, h.base.Offset)
	base := h.base
	changes := h.changes
	for len(changes) != 0 && changes[0].Offset < start {
		base = changes[0]
		changes = changes[1:]
	}
	return base, changes, start
}

func (sb *ScreenBuffer) beginGeometry(cols, rows uint16, deliver OutputHandler) uint64 {
	sb.deliverMu.Lock()
	defer sb.deliverMu.Unlock()
	sb.mu.Lock()
	sb.geometry.owner++
	owner := sb.geometry.owner
	geometry, changed := sb.geometry.record(sb.total, cols, rows)
	sb.mu.Unlock()
	if changed && deliver != nil {
		deliver(Output{EndOffset: geometry.Offset, Resize: &geometry})
	}
	return owner
}

func (sb *ScreenBuffer) resizeWithGeometry(owner uint64, cols, rows uint16, resize func() error, deliver OutputHandler) error {
	sb.mu.Lock()
	current := sb.geometry.owner == owner
	sb.mu.Unlock()
	if !current {
		return ErrTerminalInstanceChanged
	}
	// Run the PTY resize outside deliverMu so output delivery and ConPTY
	// close-flush cannot form a lock cycle with ptyMu.
	if err := resize(); err != nil {
		return err
	}
	sb.deliverMu.Lock()
	defer sb.deliverMu.Unlock()
	sb.mu.Lock()
	if sb.geometry.owner != owner {
		sb.mu.Unlock()
		return ErrTerminalInstanceChanged
	}
	geometry, changed := sb.geometry.record(sb.total, cols, rows)
	sb.mu.Unlock()
	if changed && deliver != nil {
		deliver(Output{EndOffset: geometry.Offset, Resize: &geometry})
	}
	return nil
}

// ScreenReplaySince finds the geometry and byte position together. A resize
// since the cursor may need replay even when no bytes are missing.
func (sb *ScreenBuffer) ScreenReplaySince(epoch string, afterOffset int64, afterRevision uint64) (ScreenReplay, bool) {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	if sb.geometry.base.Epoch == "" {
		return ScreenReplay{}, false
	}
	oldest := sb.total - int64(sb.retainedLocked())
	base, changes, start := sb.geometry.retained(oldest)
	snapshot := true
	if epoch == base.Epoch && afterOffset >= start && afterOffset <= sb.total && afterRevision >= base.Revision {
		position := afterRevision - base.Revision
		if position <= uint64(len(changes)) {
			from := base
			if position != 0 {
				from = changes[position-1]
			}
			if from.Offset <= afterOffset && (position == uint64(len(changes)) || changes[position].Offset >= afterOffset) {
				snapshot = false
				base, changes, start = from, changes[position:], afterOffset
			}
		}
	}
	var modePrefix []byte
	if snapshot {
		modePrefix = sb.tracker.snapshotPrefix()
	}
	return ScreenReplay{
		Data: sb.tailBytesLocked(int(sb.total - start)), EndOffset: sb.total, Snapshot: snapshot,
		Timeline: ScreenTimeline{Base: base, Changes: slices.Clone(changes), StartOffset: start, ModePrefix: modePrefix,
			HistoryTruncated: snapshot && start > oldest},
	}, true
}

func (sb *ScreenBuffer) snapshotWithTimeline() ([]byte, int64, *ScreenTimeline) {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	prefix := sb.tracker.snapshotPrefix()
	retained := sb.retainedLocked()
	screen := sb.snapshotBytesLocked(prefix, retained)
	if sb.geometry.base.Epoch == "" {
		return screen, sb.total, nil
	}
	oldest := sb.total - int64(retained)
	base, changes, start := sb.geometry.retained(oldest)
	return screen, sb.total, &ScreenTimeline{Base: base, Changes: slices.Clone(changes), StartOffset: start, ModePrefix: prefix,
		HistoryTruncated: start > oldest}
}
