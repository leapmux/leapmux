package cmd

import (
	"fmt"

	xterm "github.com/gitpod-io/xterm-go"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
)

type terminalScreenPosition struct {
	epoch    string
	offset   int64
	revision uint64
}

// terminalScreenPositionFromTimeline returns the position at the end of
// timeline using endOffset as the byte offset.
func terminalScreenPositionFromTimeline(timeline *leapmuxv1.TerminalScreenTimeline, endOffset int64) terminalScreenPosition {
	geometry := timeline.GetBase()
	if changes := timeline.GetChanges(); len(changes) != 0 {
		geometry = changes[len(changes)-1]
	}
	return terminalScreenPosition{
		epoch:    geometry.GetEpoch(),
		offset:   endOffset,
		revision: geometry.GetRevision(),
	}
}

// startsNewEpoch reports whether geometry opens a fresh screen epoch distinct
// from p.
func (p terminalScreenPosition) startsNewEpoch(geometry *leapmuxv1.TerminalScreenGeometry) bool {
	return geometry.GetEpoch() != "" && geometry.GetEpoch() != p.epoch &&
		geometry.GetRevision() == 1 && geometry.GetOffset() >= 0
}

// nextGeometry validates a sequential geometry revision within the current
// epoch and returns the updated position.
func (p terminalScreenPosition) nextGeometry(geometry *leapmuxv1.TerminalScreenGeometry) (terminalScreenPosition, bool) {
	if p.epoch == "" || geometry.GetEpoch() != p.epoch || geometry.GetOffset() != p.offset ||
		geometry.GetRevision() == 0 || geometry.GetRevision() != p.revision+1 {
		return terminalScreenPosition{}, false
	}
	return terminalScreenPosition{
		epoch:    geometry.GetEpoch(),
		offset:   geometry.GetOffset(),
		revision: geometry.GetRevision(),
	}, true
}

// applyLiveGeometry validates a live geometry update against p. When ok is
// true and stale is true, the update is an older or duplicate revision that
// the caller should skip without advancing p.
func (p terminalScreenPosition) applyLiveGeometry(geometry *leapmuxv1.TerminalScreenGeometry) (next terminalScreenPosition, stale bool, ok bool) {
	if p.startsNewEpoch(geometry) {
		return terminalScreenPosition{
			epoch:    geometry.GetEpoch(),
			offset:   geometry.GetOffset(),
			revision: 1,
		}, false, true
	}
	if p.epoch != "" && geometry.GetEpoch() == p.epoch && geometry.GetRevision() <= p.revision {
		return p, true, true
	}
	next, ok = p.nextGeometry(geometry)
	return next, false, ok
}

// advanceOutput validates non-timeline terminal data against p and returns
// the updated position.
func (p terminalScreenPosition) advanceOutput(data *leapmuxv1.TerminalData) (terminalScreenPosition, bool) {
	end, output := data.GetEndOffset(), data.GetData()
	if data.GetScreenEpoch() != p.epoch || data.GetGeometryRevision() != p.revision {
		return terminalScreenPosition{}, false
	}
	if p.epoch != "" && (data.GetIsSnapshot() || end < p.offset || end-p.offset != int64(len(output))) {
		return terminalScreenPosition{}, false
	}
	p.offset = end
	return p, true
}

func (e *screenEmulator) replayScreenTimeline(data *leapmuxv1.TerminalData, observe func() (bool, error)) error {
	timeline := data.GetScreenTimeline()
	if timeline.GetHistoryTruncated() {
		return e.screenHistoryUnavailable()
	}
	base := timeline.GetBase()
	start, end := timeline.GetStartOffset(), data.GetEndOffset()
	remaining := data.GetData()
	if base.GetEpoch() == "" || base.GetRevision() == 0 || base.GetOffset() < 0 || base.GetOffset() > start ||
		start < 0 || end < start || end-start != int64(len(remaining)) {
		return cli.Newf(cli.CodeInternal, "the worker returned an invalid screen timeline for terminal %s", e.id)
	}
	from := terminalScreenPosition{epoch: base.GetEpoch(), offset: start, revision: base.GetRevision()}
	replacement := data.GetIsSnapshot()
	if replacement {
		if e.position.epoch != "" && e.position.offset != 0 && (e.boundary == nil || !e.boundary.scrollback) {
			return e.screenHistoryUnavailable()
		}
		cols, rows, err := renderDimensions(e.id, base.GetCols(), base.GetRows())
		if err != nil {
			return err
		}
		e.cols, e.rows = cols, rows
		if err := e.reset(timeline.GetModePrefix()); err != nil {
			return err
		}
		e.position = from
	} else if from != e.position || int(base.GetCols()) != e.cols || int(base.GetRows()) != e.rows || len(timeline.GetModePrefix()) != 0 {
		return e.screenHistoryUnavailable()
	}
	observedTail := false
	for _, change := range timeline.GetChanges() {
		offset := change.GetOffset()
		if offset < e.position.offset || offset > end {
			return cli.Newf(cli.CodeInternal, "the worker returned a resize outside the screen timeline for terminal %s", e.id)
		}
		length := int(offset - e.position.offset)
		if err := e.write(remaining[:length]); err != nil {
			return err
		}
		remaining = remaining[length:]
		e.position.offset = offset
		observedTail = false
		if !replacement && length != 0 && observe != nil {
			if done, err := observe(); done || err != nil {
				return err
			}
		}
		if err := e.applyScreenGeometry(change, !replacement); err != nil {
			return err
		}
		if !replacement && observe != nil {
			if done, err := observe(); done || err != nil {
				return err
			}
			observedTail = true
		}
	}
	if len(remaining) > 0 {
		if err := e.write(remaining); err != nil {
			return err
		}
		observedTail = false
	}
	e.position.offset = end
	if replacement {
		if e.boundary != nil {
			e.boundary.followInitialViewport(e.term)
		}
		observedTail = false
	}
	if !observedTail && observe != nil {
		if _, err := observe(); err != nil {
			return err
		}
	}
	return nil
}

func (e *screenEmulator) applyLiveScreenGeometry(geometry *leapmuxv1.TerminalScreenGeometry) error {
	if e.position.startsNewEpoch(geometry) {
		cols, rows, err := renderDimensions(e.id, geometry.GetCols(), geometry.GetRows())
		if err != nil {
			return err
		}
		e.cols, e.rows = cols, rows
		e.position = terminalScreenPosition{epoch: geometry.GetEpoch(), offset: geometry.GetOffset(), revision: 1}
		return e.reset(nil)
	}
	return e.applyScreenGeometry(geometry, true)
}

func (e *screenEmulator) applyScreenGeometry(geometry *leapmuxv1.TerminalScreenGeometry, preserveBoundary bool) (err error) {
	next, ok := e.position.nextGeometry(geometry)
	if !ok {
		return e.screenHistoryUnavailable()
	}
	cols, rows, err := renderDimensions(e.id, geometry.GetCols(), geometry.GetRows())
	if err != nil {
		return err
	}
	defer recoverTerminalEmulator(fmt.Sprintf("resize terminal %s", e.id), &err)
	if cols != e.cols || rows != e.rows {
		if preserveBoundary && e.boundary != nil {
			if err := e.boundary.resize(e.term, e.id, cols, rows); err != nil {
				return err
			}
		} else {
			e.term.Resize(cols, rows)
		}
		e.cols, e.rows = cols, rows
	}
	e.position = next
	return nil
}

// resize follows the first row of the wrapped line across a resize. The row
// that held the boundary can disappear while its cells move to that row.
func (b *screenBoundary) resize(term *xterm.Terminal, id string, cols, rows int) error {
	if b.scrollback || b.normal == nil || b.normal.Lines.Length() == 0 {
		term.Resize(cols, rows)
		b.follow(term.NormalBuffer(), 0)
		return nil
	}
	lines := b.normal.Lines
	anchor := b.row
	if anchor >= lines.Length() || lines.Get(anchor) == nil {
		return screenHistoryUnavailableError(id)
	}
	for anchor > 0 && lines.Get(anchor).IsWrapped {
		anchor--
	}
	cells := b.column
	for y := anchor; y < b.row; y++ {
		cells += wrappedRowLength(lines.Get(y), lines.Get(y+1))
	}
	marker := b.normal.AddMarker(anchor)
	defer marker.Dispose()
	term.Resize(cols, rows)
	normal := term.NormalBuffer()
	if normal != b.normal {
		return screenHistoryUnavailableError(id)
	}
	if marker.IsDisposed {
		if normal.Lines.Length() != 0 && normal.Lines.Get(0).IsWrapped {
			// Part of the boundary's logical line survived a trim. The marker
			// no longer tells us which cells of that line preceded the wait.
			return screenHistoryUnavailableError(id)
		}
		b.follow(normal, 0)
		return nil
	}
	row := marker.Line
	for row+1 < normal.Lines.Length() {
		next := normal.Lines.Get(row + 1)
		if next == nil || !next.IsWrapped {
			break
		}
		width := wrappedRowLength(normal.Lines.Get(row), next)
		if cells < width {
			break
		}
		cells -= width
		row++
	}
	b.follow(normal, row)
	b.column = min(cells, cols)
	return nil
}

func screenHistoryUnavailableError(id string) error {
	return cli.Newf(cli.CodeUnavailable, "the worker could not supply enough screen history to render terminal %s", id)
}

func (e *screenEmulator) screenHistoryUnavailable() error {
	return screenHistoryUnavailableError(e.id)
}

func (e *screenEmulator) consumeScreenOutput(data *leapmuxv1.TerminalData, observe func() (bool, error)) error {
	if data.GetScreenTimeline().GetBase() != nil {
		return e.replayScreenTimeline(data, observe)
	}
	next, ok := e.position.advanceOutput(data)
	if !ok {
		return e.screenHistoryUnavailable()
	}
	e.position = next
	var err error
	if data.GetIsSnapshot() {
		err = e.reset(data.GetData())
	} else {
		err = e.write(data.GetData())
	}
	if err != nil {
		return err
	}
	if observe != nil {
		if _, err := observe(); err != nil {
			return err
		}
	}
	return nil
}
