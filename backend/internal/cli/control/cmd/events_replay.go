package cmd

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
)

type checkpointedEvents struct {
	opts    eventsOptions
	tab     scopedTab
	printer *cli.Printer
	kinds   []string
	state   *eventCheckpoint
	save    func(*eventCheckpoint) error
	printed int
}

func (s *session) runCheckpointedEvents(opts eventsOptions) error {
	bounded := s.bounded()
	target, err := bounded.resolveCheckpointTab(opts.tab)
	bounded.Close()
	if err != nil {
		return err
	}
	if target.Type != leapmuxv1.TabType_TAB_TYPE_AGENT && target.Type != leapmuxv1.TabType_TAB_TYPE_TERMINAL {
		return cli.Usagef("--checkpoint requires an agent or terminal tab")
	}
	checkpoint, err := openEventCheckpoint(opts.checkpoint)
	if err != nil {
		return err
	}
	defer checkpoint.close()
	state, err := checkpoint.read()
	if err != nil {
		return err
	}
	kinds := eventCheckpointKinds(opts.filter, target.Type)
	if state != nil {
		if target.ID != state.TabID || target.WorkerID != state.WorkerID || tabTypeName(target.Type) != state.TabType {
			return cli.Newf(cli.CodeConflict, "the event checkpoint belongs to a different tab or Worker")
		}
		if !slices.Equal(kinds, state.Filter) {
			return cli.Newf(cli.CodeConflict, "the event checkpoint uses a different --kind selection").
				WithHint("use the saved kind selection or another checkpoint file")
		}
	}
	replay := checkpointedEvents{
		opts: opts, tab: scopedTab{ID: target.ID, Type: target.Type, WorkerID: target.WorkerID},
		printer: s.inv.Printer, kinds: kinds, state: state, save: checkpoint.save,
	}
	backoff := newReconnectBackoff(activityReconnectInitial, activityReconnectMax)
	for s.ctx.Err() == nil {
		before := replay.state
		retry, err := replay.runAttempt(s)
		if s.ctx.Err() != nil {
			return nil
		}
		if !retry {
			return err
		}
		failure := eventReplayTransportError(err)
		if !failure.Retryable() {
			return failure
		}
		event := replay.notice(kindStreamUnavailable)
		event.Code, event.Message, event.Retrying = string(failure.Code), failure.Message, boolPtr(true)
		event.Summary = failure.Message
		if err := replay.print(event); err != nil {
			return err
		}
		timer := time.NewTimer(backoff.afterSession(before != replay.state))
		select {
		case <-s.ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
	return nil
}

func (s *session) resolveCheckpointTab(selector string) (tabTarget, error) {
	if !s.c.IsWorkerIPC() || strings.TrimSpace(selector) != resolve.SelfTarget {
		return s.resolveTab(resolve.KindTab, selector, resolve.Need{WorkerID: true, Want: resolve.Wants{TabType: true}})
	}
	ipc, err := s.c.ControlIPCService()
	if err != nil {
		return tabTarget{}, err
	}
	identity, err := ipc.Whoami(s.ctx, &leapmuxv1.WhoamiRequest{})
	if err != nil {
		return tabTarget{}, err
	}
	if identity.GetTabId() == "" || identity.GetWorkerId() == "" {
		return tabTarget{}, cli.Usagef("--tab self requires a local agent or terminal tab")
	}
	return tabTarget{ID: identity.GetTabId(), Type: identity.GetTabType(), WorkerID: identity.GetWorkerId(), Self: true}, nil
}

func (r *checkpointedEvents) runAttempt(s *session) (retry bool, err error) {
	ctx, cancel := context.WithCancel(s.ctx)
	frames := make(chan eventFrame, 32)
	done := make(chan struct{})
	request := &leapmuxv1.WatchSemanticEventsRequest{Tab: &leapmuxv1.TabIdent{TabType: r.tab.Type, TabId: r.tab.ID}}
	if r.state != nil {
		request.AfterCursor = r.state.Cursor
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
	opening := time.NewTimer(defaultInnerRPCTimeout)
	defer opening.Stop()
	var heartbeat <-chan time.Time
	if r.opts.heartbeat > 0 {
		ticker := time.NewTicker(r.opts.heartbeat)
		defer ticker.Stop()
		heartbeat = ticker.C
	}
	var after, high int64
	ready := false
	for {
		select {
		case <-ctx.Done():
			return false, nil
		case <-opening.C:
			return true, cli.Newf(cli.CodeUnavailable, "the Worker did not open the event history stream in time")
		case <-heartbeat:
			if ready {
				event := r.notice(kindStreamHeartbeat)
				event.Summary = "watching " + tabTypeName(r.tab.Type) + " event history"
				if err := r.print(event); err != nil {
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
				opening.Stop()
				ready = true
				after, high = start.GetAfterSequence(), start.GetHighWaterSequence()
				continue
			}
			if !ready {
				return false, invalidSemanticEventSequence()
			}
			next, err := r.acceptFrame(frame.response, after, high)
			if err != nil {
				return false, err
			}
			after = next
			if r.opts.limit > 0 && r.printed >= r.opts.limit {
				return false, nil
			}
		}
	}
}

func (r *checkpointedEvents) acceptReady(ready *leapmuxv1.SemanticWatchReady) error {
	after := ready.GetAfterSequence()
	if ready.GetWorkerId() != r.tab.WorkerID || ready.GetTab().GetTabId() != r.tab.ID ||
		ready.GetTab().GetTabType() != r.tab.Type || ready.GetJournalId() == "" || ready.GetTabStreamId() == "" ||
		after < 0 || after > ready.GetHighWaterSequence() || len(ready.GetCursor()) == 0 {
		return invalidSemanticEventSequence()
	}
	start := &eventCheckpoint{
		Version: eventCheckpointVersion, WorkerID: r.tab.WorkerID, TabID: r.tab.ID,
		TabType: tabTypeName(r.tab.Type), JournalID: ready.GetJournalId(), TabStreamID: ready.GetTabStreamId(),
		Filter: r.kinds, Sequence: after, Cursor: ready.GetCursor(),
	}
	if state := r.state; state != nil {
		if start.JournalID != state.JournalID || start.TabStreamID != state.TabStreamID ||
			start.Sequence != state.Sequence || !bytes.Equal(start.Cursor, state.Cursor) {
			return invalidSemanticEventSequence()
		}
	} else if after != ready.GetHighWaterSequence() {
		return invalidSemanticEventSequence()
	}
	event := r.notice(kindStreamReady)
	event.JournalID, event.TabStreamID, event.Sequence = start.JournalID, start.TabStreamID, &after
	event.Summary = "watching " + tabTypeName(r.tab.Type) + " event history"
	if err := r.print(event); err != nil {
		return err
	}
	if r.state == nil {
		if err := r.save(start); err != nil {
			return err
		}
		r.state = start
	}
	return nil
}

func (r *checkpointedEvents) acceptFrame(frame *leapmuxv1.WatchSemanticEventsResponse, after, high int64) (int64, error) {
	var event streamEvent
	var cursor []byte
	var sequence int64
	printRecord := true
	count := false
	switch {
	case frame.GetRecord() != nil:
		record := frame.GetRecord()
		sequence, cursor = record.GetSequence(), record.GetCursor()
		if sequence <= after || sequence-after != 1 {
			return after, invalidSemanticEventSequence()
		}
		decoded, ok := checkpointFactEvent(record, r.tab)
		if !ok {
			return after, invalidSemanticEventSequence()
		}
		event = decoded
		event.Time = time.UnixMilli(record.GetObservedUnixMillis()).UTC()
		event.Replayed = boolPtr(sequence <= high)
		printRecord = r.opts.filter.selects(decoded.Kind)
		count = printRecord
	case frame.GetGap() != nil:
		gap := frame.GetGap()
		sequence, cursor = gap.GetResumeAfterSequence(), gap.GetCursor()
		reason, message := semanticGapDescription(gap.GetReason())
		if reason == "" || gap.GetAfterSequence() != after || sequence <= after {
			return after, invalidSemanticEventSequence()
		}
		event = r.notice(kindStreamGap)
		event.GapReason, event.AfterSequence = reason, &after
		event.Lost = eventCheckpointKinds(kindFilter{}, r.tab.Type)
		event.Summary = fmt.Sprintf("%s after sequence %d", message, after)
	default:
		return after, invalidSemanticEventSequence()
	}
	if len(cursor) == 0 {
		return after, invalidSemanticEventSequence()
	}
	event.Sequence = &sequence
	event.JournalID, event.TabStreamID = r.state.JournalID, r.state.TabStreamID
	if printRecord {
		if err := r.print(event); err != nil {
			return after, err
		}
	}
	next := *r.state
	next.Sequence, next.Cursor = sequence, cursor
	if err := r.save(&next); err != nil {
		return after, err
	}
	r.state = &next
	if count {
		r.printed++
	}
	return sequence, nil
}

func (r *checkpointedEvents) print(event streamEvent) error {
	return printCheckpointEvent(r.printer, r.opts.filter, event)
}

func (r *checkpointedEvents) notice(kind string) streamEvent {
	event := tabStreamEvent(kind, r.tab)
	event.Source, event.Time = "worker", time.Now().UTC()
	return event
}

func invalidSemanticEventSequence() error {
	return cli.Newf(cli.CodeInternal, "the Worker sent an inconsistent event history position")
}

func semanticGapDescription(reason leapmuxv1.SemanticWatchGapReason) (string, string) {
	switch reason {
	case leapmuxv1.SemanticWatchGapReason_SEMANTIC_WATCH_GAP_REASON_RETENTION:
		return "retention", "tab events expired"
	case leapmuxv1.SemanticWatchGapReason_SEMANTIC_WATCH_GAP_REASON_UNCLEAN_STOP:
		return "unclean_stop", "the Worker stopped before it confirmed all events were recorded"
	case leapmuxv1.SemanticWatchGapReason_SEMANTIC_WATCH_GAP_REASON_SOURCE_LIMIT:
		return "source_limit", "the Worker could not retain a tab event"
	default:
		return "", ""
	}
}
