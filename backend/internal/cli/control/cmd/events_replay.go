package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/coder/quartz"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
)

type semanticHistoryCursor struct {
	journalID   string
	tabStreamID string
	sequence    int64
	cursor      []byte
}

func (c semanticHistoryCursor) position() *eventHistoryPosition {
	return &eventHistoryPosition{JournalID: c.journalID, Sequence: c.sequence, Cursor: c.cursor}
}

func (c semanticHistoryCursor) decodeRecord(tab scopedTab, sequence, observedUnixMillis int64, cursor []byte,
	fact *leapmuxv1.SemanticTabFact, handle string, high int64,
) (streamEvent, semanticHistoryCursor, error) {
	if sequence <= c.sequence || sequence-c.sequence != 1 || len(cursor) == 0 {
		return streamEvent{}, c, invalidSemanticEventSequence()
	}
	event, ok := checkpointFactEvent(fact, handle, tab)
	if !ok {
		return streamEvent{}, c, invalidSemanticEventSequence()
	}
	event.Time = time.UnixMilli(observedUnixMillis).UTC()
	event.JournalID = c.journalID
	event.TabStreamID = c.tabStreamID
	event.Sequence = &sequence
	event.Replayed = new(sequence <= high)
	next := c
	next.sequence = sequence
	next.cursor = cursor
	return event, next, nil
}

func (c semanticHistoryCursor) decodeGap(base streamEvent, gap *leapmuxv1.SemanticWatchGap,
	filter kindFilter, tabType leapmuxv1.TabType,
) (streamEvent, semanticHistoryCursor, error) {
	reason, message := semanticGapDescription(gap.GetReason())
	resume := gap.GetResumeAfterSequence()
	cursor := gap.GetCursor()
	if reason == "" || gap.GetAfterSequence() != c.sequence || resume <= c.sequence || len(cursor) == 0 {
		return streamEvent{}, c, invalidSemanticEventSequence()
	}
	after := c.sequence
	event := base
	event.Kind = kindStreamGap
	event.JournalID = c.journalID
	event.TabStreamID = c.tabStreamID
	event.Sequence = &resume
	event.AfterSequence = &after
	event.GapReason = reason
	event.Lost = eventCheckpointKinds(filter, tabType)
	event.Summary = fmt.Sprintf("%s after sequence %d", message, after)
	next := c
	next.sequence = resume
	next.cursor = cursor
	return event, next, nil
}

func waitReplayRetry(ctx context.Context, clock quartz.Clock, delay time.Duration) bool {
	if clock == nil {
		clock = quartz.NewReal()
	}
	timer := clock.NewTimer(delay, "events-replay-retry")
	defer timer.Stop("events-replay-retry")
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

type checkpointedEvents struct {
	opts    eventsOptions
	tab     scopedTab
	printer *eventPrinter
	clock   quartz.Clock
	kinds   []string
	state   *eventCheckpoint
	save    func(*eventCheckpoint) error
	dirty   bool
}

func (s *session) runCheckpointedEvents(ctx context.Context, clock quartz.Clock, opts eventsOptions) error {
	checkpoint, state, err := openCheckedEventCheckpoint(opts.checkpoint, tabCheckpointScope)
	if err != nil {
		return err
	}
	defer checkpoint.close()
	callCtx, callCancel := rpcDeadline(ctx)
	callSession := &session{inv: s.inv, c: s.c, ctx: callCtx, cancel: callCancel, located: s.located}
	target, err := callSession.resolveCheckpointTab(opts.tab, state)
	callSession.Close()
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	kinds, err := validateTabCheckpointFilter(opts.filter, target.Type)
	if err != nil {
		return err
	}
	if state != nil {
		if target.ID != state.Tab.TabID || target.WorkerID != state.Tab.WorkerID || resolve.TabTypeWireName(target.Type) != state.Tab.TabType {
			return cli.Newf(cli.CodeConflict, "the event checkpoint belongs to a different tab or Worker")
		}
		if err := verifyCheckpointFilter(state, kinds); err != nil {
			return err
		}
	}
	replay := checkpointedEvents{
		opts:    opts,
		tab:     scopedTab{ID: target.ID, Type: target.Type, WorkerID: target.WorkerID},
		printer: newEventPrinter(s.inv.Printer, opts.filter, opts.limit),
		clock:   clock,
		kinds:   kinds,
		state:   state,
		save:    checkpoint.save,
	}
	defer func() { _ = replay.flush() }()
	backoff := newReconnectBackoff(activityReconnectInitial, activityReconnectMax)
	for ctx.Err() == nil {
		before := replay.state
		retry, err := replay.runAttempt(ctx, s)
		if ctx.Err() != nil {
			return replay.flush()
		}
		if !retry {
			if err != nil {
				return err
			}
			return replay.flush()
		}
		if flushErr := replay.flush(); flushErr != nil {
			return flushErr
		}
		failure := eventReplayTransportError(err)
		if replay.opts.noReconnect || !failure.Retryable() {
			return failure
		}
		event := unavailableEvent(replay.notice(kindStreamUnavailable), failure, true)
		if _, err := replay.printer.printCheckpointed(event); err != nil {
			return err
		}
		if !waitReplayRetry(ctx, replay.nowClock(), backoff.afterSession(before != replay.state)) {
			return nil
		}
	}
	return replay.flush()
}

func (s *session) resolveCheckpointTab(selector string, saved *eventCheckpoint) (tabTarget, error) {
	if saved != nil && saved.Tab != nil && resolve.ExplicitID(resolve.KindTab, selector) == saved.Tab.TabID {
		tabType, _ := resolve.ParseTabType(saved.Tab.TabType)
		return tabTarget{ID: saved.Tab.TabID, Type: tabType, WorkerID: saved.Tab.WorkerID}, nil
	}
	return s.resolveTab(resolve.KindTab, selector, resolve.Need{
		WorkerID: true,
		Want:     resolve.Wants{TabType: true},
	})
}

func (r *checkpointedEvents) nowClock() quartz.Clock {
	if r.clock == nil {
		r.clock = quartz.NewReal()
	}
	return r.clock
}

func (r *checkpointedEvents) runAttempt(parentCtx context.Context, s *session) (retry bool, err error) {
	ctx, cancel := context.WithCancel(parentCtx)
	frames := make(chan eventFrame, 32)
	done := make(chan struct{})
	request := &leapmuxv1.WatchSemanticEventsRequest{Tab: &leapmuxv1.TabIdent{TabType: r.tab.Type, TabId: r.tab.ID}}
	if r.state != nil {
		request.AfterCursor = r.state.Tab.Position.Cursor
	}
	go func() {
		defer close(done)
		err := s.receiveSemanticWatch(ctx, r.tab.WorkerID, request, frames)
		select {
		case frames <- eventFrame{err: err}:
		case <-ctx.Done():
		}
	}()
	defer func() {
		cancel()
		<-done
	}()
	clock := r.nowClock()
	opening := clock.NewTimer(defaultInnerRPCTimeout, "events-replay-open")
	defer opening.Stop("events-replay-open")
	var heartbeat <-chan time.Time
	if r.opts.heartbeat > 0 {
		ticker := clock.NewTicker(r.opts.heartbeat, "events-replay-heartbeat")
		defer ticker.Stop("events-replay-heartbeat")
		heartbeat = ticker.C
	}
	var after, high int64
	ready := false
	for {
		select {
		case <-ctx.Done():
			return false, r.flush()
		case <-opening.C:
			return true, cli.Newf(cli.CodeUnavailable, "the Worker did not open the event history stream in time")
		case <-heartbeat:
			if ready {
				event := r.notice(kindStreamHeartbeat)
				event.Summary = "watching " + resolve.TabTypeWireName(r.tab.Type) + " event history"
				if _, err := r.printer.printCheckpointed(event); err != nil {
					return false, err
				}
			}
		case frame := <-frames:
			if frame.response == nil {
				return true, frame.err
			}
			if start := frame.response.GetReady(); start != nil {
				if ready {
					return false, invalidSemanticEventSequence()
				}
				if err := r.acceptReady(start); err != nil {
					return false, err
				}
				opening.Stop("events-replay-open")
				ready = true
				after, high = start.GetAfterSequence(), start.GetHighWaterSequence()
				if r.opts.snapshot && after >= high {
					return false, nil
				}
				continue
			}
			if !ready {
				return false, invalidSemanticEventSequence()
			}
			if retired := frame.response.GetRetired(); retired != nil {
				if !frame.end {
					return false, invalidSemanticEventSequence()
				}
				if err := r.flush(); err != nil {
					return false, err
				}
				return false, r.acceptRetirement(retired, after, high)
			}
			next, err := r.acceptFrame(frame.response, after, high)
			if err != nil {
				return false, err
			}
			after = next
			if after >= high {
				if err := r.flush(); err != nil {
					return false, err
				}
			}
			if r.opts.snapshot && after >= high {
				return false, nil
			}
			if r.printer.limitReached() {
				return false, r.flush()
			}
		}
	}
}

func (r *checkpointedEvents) acceptRetirement(retired *leapmuxv1.SemanticWatchRetired, after, high int64) error {
	tab := r.state.Tab
	if retired.GetFinalSequence() != after || after < high || tab.Position.Sequence != after {
		return invalidSemanticEventSequence()
	}
	event := r.notice(kindStreamRetired)
	event.JournalID, event.TabStreamID, event.Sequence = tab.Position.JournalID, tab.TabStreamID, &after
	event.Summary = "the Worker finished this tab's event history"
	_, err := r.printer.printCheckpointed(event)
	return err
}

func (r *checkpointedEvents) acceptReady(ready *leapmuxv1.SemanticWatchReady) error {
	after := ready.GetAfterSequence()
	if ready.GetWorkerId() != r.tab.WorkerID || ready.GetTab().GetTabId() != r.tab.ID ||
		ready.GetTab().GetTabType() != r.tab.Type || ready.GetJournalId() == "" || ready.GetTabStreamId() == "" ||
		after < 0 || after > ready.GetHighWaterSequence() || len(ready.GetCursor()) == 0 {
		return invalidSemanticEventSequence()
	}
	start := &eventCheckpoint{
		Version: eventCheckpointVersion,
		Scope:   tabCheckpointScope,
		Filter:  r.kinds,
		Tab: &tabEventCheckpoint{
			WorkerID:    r.tab.WorkerID,
			TabID:       r.tab.ID,
			TabType:     resolve.TabTypeWireName(r.tab.Type),
			TabStreamID: ready.GetTabStreamId(),
			Position: &eventHistoryPosition{
				JournalID: ready.GetJournalId(),
				Sequence:  after,
				Cursor:    ready.GetCursor(),
			},
		},
	}
	if state := r.state; state != nil {
		savedTab := state.Tab
		if start.Tab.Position.JournalID != savedTab.Position.JournalID ||
			start.Tab.TabStreamID != savedTab.TabStreamID ||
			start.Tab.Position.Sequence != savedTab.Position.Sequence ||
			!bytes.Equal(start.Tab.Position.Cursor, savedTab.Position.Cursor) {
			return invalidSemanticEventSequence()
		}
	} else if after != ready.GetHighWaterSequence() {
		return invalidSemanticEventSequence()
	}
	event := r.notice(kindStreamReady)
	event.JournalID, event.TabStreamID, event.Sequence = start.Tab.Position.JournalID, start.Tab.TabStreamID, &after
	event.Summary = "watching " + resolve.TabTypeWireName(r.tab.Type) + " event history"
	if _, err := r.printer.printCheckpointed(event); err != nil {
		return err
	}
	if r.state == nil {
		if err := r.save(start); err != nil {
			return err
		}
		r.state = start
		r.dirty = false
	}
	return nil
}

func (r *checkpointedEvents) acceptFrame(frame *leapmuxv1.WatchSemanticEventsResponse, after, high int64) (int64, error) {
	savedTab := r.state.Tab
	cur := semanticHistoryCursor{
		journalID:   savedTab.Position.JournalID,
		tabStreamID: savedTab.TabStreamID,
		sequence:    after,
		cursor:      savedTab.Position.Cursor,
	}
	var event streamEvent
	var nextCur semanticHistoryCursor
	var err error
	isGap := false
	switch {
	case frame.GetRecord() != nil:
		record := frame.GetRecord()
		event, nextCur, err = cur.decodeRecord(r.tab, record.GetSequence(), record.GetObservedUnixMillis(),
			record.GetCursor(), record.GetFact(), record.GetCurrentRequestHandle(), high)
	case frame.GetGap() != nil:
		isGap = true
		event, nextCur, err = cur.decodeGap(r.notice(kindStreamGap), frame.GetGap(), r.opts.filter, r.tab.Type)
	default:
		return after, invalidSemanticEventSequence()
	}
	if err != nil {
		return after, err
	}
	tabCopy := *savedTab
	posCopy := *savedTab.Position
	posCopy.Sequence, posCopy.Cursor = nextCur.sequence, nextCur.cursor
	tabCopy.Position = &posCopy
	next := *r.state
	next.Tab = &tabCopy
	if !isGap && !r.opts.filter.selects(event.Kind) {
		r.state = &next
		r.dirty = true
		return next.Tab.Position.Sequence, nil
	}
	selected, err := r.printer.printCheckpointed(event)
	if err != nil {
		return after, err
	}
	if err := r.save(&next); err != nil {
		return after, err
	}
	r.state = &next
	r.dirty = false
	if !isGap && selected {
		r.printer.recordCounted()
	}
	return next.Tab.Position.Sequence, nil
}

func (r *checkpointedEvents) flush() error {
	if !r.dirty || r.state == nil {
		return nil
	}
	if err := r.save(r.state); err != nil {
		return err
	}
	r.dirty = false
	return nil
}

func (r *checkpointedEvents) notice(kind string) streamEvent {
	event := tabStreamEvent(kind, r.tab)
	event.Source, event.Time = "worker", r.nowClock().Now().UTC()
	return event
}

var errInvalidSemanticHistory = errors.New("the Worker sent an inconsistent event history position")

func invalidSemanticEventSequence() error {
	return cli.Wrap(cli.CodeInternal, errInvalidSemanticHistory, "")
}

func semanticGapDescription(reason leapmuxv1.SemanticWatchGapReason) (string, string) {
	switch reason {
	case leapmuxv1.SemanticWatchGapReason_SEMANTIC_WATCH_GAP_REASON_RETENTION:
		return "retention", "tab events expired"
	case leapmuxv1.SemanticWatchGapReason_SEMANTIC_WATCH_GAP_REASON_UNCLEAN_STOP:
		return "unclean_stop", "the Worker stopped before it confirmed all events were recorded"
	case leapmuxv1.SemanticWatchGapReason_SEMANTIC_WATCH_GAP_REASON_SOURCE_LIMIT:
		return "source_limit", "the Worker could not retain complete tab event history"
	case leapmuxv1.SemanticWatchGapReason_SEMANTIC_WATCH_GAP_REASON_HISTORY_UNAVAILABLE:
		return "history_unavailable", "some Worker event history is unavailable"
	default:
		return "", ""
	}
}
