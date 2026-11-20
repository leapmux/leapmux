package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"connectrpc.com/connect/v2"
	"github.com/coder/websocket"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/channelwire"
	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
)

func (r *accountReplay) startWorker(key workerHistoryKey, previouslyWatching bool) {
	ctx, cancel := context.WithCancel(r.ctx)
	position := r.workerPosition(key)
	watch := &accountWorkerWatch{
		key: key, cancel: cancel, pending: !r.ready, uncovered: position == nil && previouslyWatching,
	}
	if watch.pending {
		r.pending++
	}
	r.workers[key] = watch
	r.sources.Go(func() { r.followWorker(ctx, watch, position) })
}

func (r *accountReplay) workerPosition(key workerHistoryKey) *eventHistoryPosition {
	return r.state.Account.Workers[key.id][tabTypeName(key.tabType)]
}

const accountWorkerScanFrames = 128

var errWorkerScanComplete = errors.New("history scan complete")

type workerScan struct {
	cursor    []byte
	waitReady bool
	caughtUp  bool
	progress  bool
}

func (r *accountReplay) followWorker(ctx context.Context, watch *accountWorkerWatch, position *eventHistoryPosition) {
	var cursor []byte
	if position != nil {
		cursor = position.Cursor
	}
	backoff := newReconnectBackoff(activityReconnectInitial, activityReconnectMax)
	for ctx.Err() == nil {
		scan, err := r.scanWorker(ctx, watch, cursor)
		cursor = scan.cursor
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, errWorkerScanComplete) {
			backoff = newReconnectBackoff(activityReconnectInitial, activityReconnectMax)
			if scan.waitReady {
				select {
				case <-r.readySignal:
				case <-ctx.Done():
					return
				}
			} else if scan.caughtUp && !waitAccountReplayRetry(ctx, accountInventoryInterval) {
				return
			}
			continue
		}
		failure := accountWorkerFailure(err)
		if err := r.deliver(ctx, func() (bool, error) { return false, r.workerFailed(watch, failure) }); err != nil ||
			!failure.Retryable() || !waitAccountReplayRetry(ctx, backoff.afterSession(scan.progress)) {
			return
		}
	}
}

func (r *accountReplay) scanWorker(ctx context.Context, watch *accountWorkerWatch, cursor []byte) (workerScan, error) {
	scan := workerScan{cursor: cursor}
	select {
	case r.workerSlots <- struct{}{}:
	case <-ctx.Done():
		return scan, ctx.Err()
	}
	defer func() { <-r.workerSlots }()

	key := watch.key
	request := &leapmuxv1.WatchWorkerSemanticEventsRequest{TabType: key.tabType, AfterCursor: cursor}
	payload, err := proto.Marshal(request)
	if err != nil {
		return scan, err
	}
	attempt, cancel := rpcDeadline(ctx)
	defer cancel()
	var high int64
	var seenReady bool
	frames := 0
	err = r.session.receiveSemanticHistory(attempt, key.id, leapmuxv1connect.WorkerWatchServiceWatchWorkerSemanticEventsProcedure, payload,
		func(raw []byte, rpcError *leapmuxv1.RpcError, end bool) error {
			if rpcError.GetCode() == int32(codes.ResourceExhausted) {
				return cli.Newf(cli.CodeConflict, "%s", rpcError.GetMessage())
			}
			if err := channelwire.ErrorOf(rpcError); err != nil {
				return inBandError(err)
			}
			if end {
				return io.EOF
			}
			frame := new(leapmuxv1.WatchWorkerSemanticEventsResponse)
			if err := proto.Unmarshal(raw, frame); err != nil || frame.GetEvent() == nil ||
				seenReady == (frame.GetReady() != nil) {
				return invalidSemanticEventSequence()
			}
			if err := r.deliverSource(ctx, errInvalidSemanticHistory, func() (bool, error) {
				return r.acceptWorkerFrame(watch, frame)
			}); err != nil {
				return err
			}
			var sequence int64
			switch {
			case frame.GetReady() != nil:
				ready := frame.GetReady()
				seenReady, high, scan.cursor = true, ready.GetHighWaterSequence(), ready.GetCursor()
				sequence = ready.GetAfterSequence()
				select {
				case <-r.readySignal:
				default:
					scan.waitReady = true
					return errWorkerScanComplete
				}
			case frame.GetRecord() != nil:
				record := frame.GetRecord()
				scan.progress, scan.cursor, sequence = true, record.GetCursor(), record.GetSequence()
				frames++
			case frame.GetGap() != nil:
				gap := frame.GetGap()
				scan.progress, scan.cursor, sequence = true, gap.GetCursor(), gap.GetResumeAfterSequence()
				frames++
			}
			scan.caughtUp = sequence >= high
			if scan.caughtUp || frames >= accountWorkerScanFrames {
				return errWorkerScanComplete
			}
			return nil
		})
	return scan, err
}

func accountWorkerFailure(err error) *cli.Error {
	if err == nil {
		err = io.EOF
	}
	var closeError websocket.CloseError
	if errors.As(err, &closeError) && closeError.Reason == contracts.CloseReasonTooManyConnections {
		return cli.Newf(cli.CodeUnavailable, "the Hub has no free Worker connections")
	}
	if errors.Is(err, context.DeadlineExceeded) || connect.CodeOf(err) == connect.CodeDeadlineExceeded {
		return cli.Newf(cli.CodeUnavailable, "the Worker event history request timed out")
	}
	failure := eventReplayTransportError(err)
	if failure.Code == cli.CodeNotFound {
		return cli.Newf(cli.CodeUnavailable, "%s", failure.Message)
	}
	return failure
}

func (r *accountReplay) workerFailed(watch *accountWorkerWatch, failure *cli.Error) error {
	if r.workers[watch.key] != watch {
		return nil
	}
	watch.live = false
	if !failure.Retryable() {
		watch.stopped = failure
	}
	r.markResponded(watch)
	watch.uncovered = r.workerPosition(watch.key) == nil
	event := r.workerNotice(watch, kindStreamUnavailable, failure.Message)
	event.Code, event.Message, event.Retrying = string(failure.Code), failure.Message, boolPtr(failure.Retryable())
	return r.print(event)
}

func (r *accountReplay) workerNotice(watch *accountWorkerWatch, kind, summary string) streamEvent {
	event := r.notice(kind, summary)
	event.Source, event.WorkerID, event.TabType = "worker", watch.key.id, tabTypeName(watch.key.tabType)
	return event
}

func (r *accountReplay) acceptWorkerFrame(watch *accountWorkerWatch, frame *leapmuxv1.WatchWorkerSemanticEventsResponse) (bool, error) {
	if r.workers[watch.key] != watch {
		return false, nil
	}
	position := r.workerPosition(watch.key)
	if ready := frame.GetReady(); ready != nil {
		return false, r.acceptWorkerReady(watch, position, ready)
	}
	if !watch.live || !position.valid() {
		return false, invalidSemanticEventSequence()
	}
	event, next, printed, err := r.workerEvent(watch, position, frame)
	if err != nil {
		return false, err
	}
	if printed || event.Kind == kindStreamGap {
		if err := r.print(event); err != nil {
			return false, err
		}
	}
	if err := r.saveWorkerPosition(watch.key, next); err != nil {
		return false, err
	}
	if printed {
		r.workerPrinted++
	}
	return r.opts.limit > 0 && r.printed() >= r.opts.limit, nil
}

func (r *accountReplay) acceptWorkerReady(watch *accountWorkerWatch, saved *eventHistoryPosition, ready *leapmuxv1.SemanticWorkerWatchReady) error {
	key := watch.key
	position := &eventHistoryPosition{JournalID: ready.GetJournalId(), Sequence: ready.GetAfterSequence(), Cursor: ready.GetCursor()}
	if ready.GetTabType() != key.tabType || ready.GetWorkerId() != key.id ||
		!position.valid() || position.Sequence > ready.GetHighWaterSequence() {
		return invalidSemanticEventSequence()
	}
	if saved == nil {
		if position.Sequence != ready.GetHighWaterSequence() {
			return invalidSemanticEventSequence()
		}
		if watch.uncovered {
			event := r.workerNotice(watch, kindStreamGap, "no saved event position covers this Worker source")
			event.GapReason, event.Lost = "source_unenrolled", eventCheckpointKinds(r.opts.filter, key.tabType)
			if err := r.print(event); err != nil {
				return err
			}
		}
		if err := r.saveWorkerPosition(key, position); err != nil {
			return err
		}
	} else if !sameHistoryPosition(saved, position) {
		return invalidSemanticEventSequence()
	}
	if !watch.admitted {
		watch.high = ready.GetHighWaterSequence()
		watch.admitted = true
	}
	watch.live = true
	r.markResponded(watch)
	return nil
}

func (r *accountReplay) workerEvent(watch *accountWorkerWatch, position *eventHistoryPosition, frame *leapmuxv1.WatchWorkerSemanticEventsResponse) (streamEvent, *eventHistoryPosition, bool, error) {
	next := *position
	var event streamEvent
	printed := false
	if record := frame.GetRecord(); record != nil {
		tab := record.GetTab()
		if tab.GetTabId() == "" || tab.GetTabType() != watch.key.tabType ||
			record.GetSequence() <= position.Sequence || record.GetSequence()-position.Sequence != 1 {
			return event, nil, false, invalidSemanticEventSequence()
		}
		decoded, ok := checkpointFactEvent(record.GetFact(), record.GetCurrentRequestHandle(), scopedTab{
			ID: tab.GetTabId(), Type: tab.GetTabType(), WorkerID: watch.key.id,
		})
		if !ok {
			return event, nil, false, invalidSemanticEventSequence()
		}
		event = decoded
		event.Time = time.UnixMilli(record.GetObservedUnixMillis()).UTC()
		event.Replayed = boolPtr(record.GetSequence() <= watch.high)
		event.Source = "worker"
		next.Sequence, next.Cursor = record.GetSequence(), record.GetCursor()
		printed = r.opts.filter.selects(event.Kind)
	} else if gap := frame.GetGap(); gap != nil {
		reason, message := semanticGapDescription(gap.GetReason())
		if reason == "" || gap.GetAfterSequence() != position.Sequence || gap.GetResumeAfterSequence() <= position.Sequence {
			return event, nil, false, invalidSemanticEventSequence()
		}
		event = r.workerNotice(watch, kindStreamGap, fmt.Sprintf("%s after sequence %d", message, position.Sequence))
		event.GapReason, event.AfterSequence = reason, &position.Sequence
		event.Lost = eventCheckpointKinds(r.opts.filter, watch.key.tabType)
		next.Sequence, next.Cursor = gap.GetResumeAfterSequence(), gap.GetCursor()
	} else {
		return event, nil, false, invalidSemanticEventSequence()
	}
	if !next.valid() {
		return event, nil, false, invalidSemanticEventSequence()
	}
	event.JournalID, event.Sequence = next.JournalID, &next.Sequence
	return event, &next, printed, nil
}

func (r *accountReplay) saveWorkerPosition(key workerHistoryKey, position *eventHistoryPosition) error {
	r.state.Account.Workers[key.id][tabTypeName(key.tabType)] = position
	return r.file.save(r.state)
}

func sameHistoryPosition(old, new *eventHistoryPosition) bool {
	return old.JournalID == new.JournalID && old.Sequence == new.Sequence && bytes.Equal(old.Cursor, new.Cursor)
}

func waitAccountReplayRetry(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func (r *accountReplay) startHub(fresh bool) error {
	selected := false
	for _, kind := range eventKinds {
		if kind.source == sourceHub && r.opts.filter.selects(kind.name) {
			selected = true
			break
		}
	}
	saved := r.state.Account.Hub
	if !selected {
		if saved != nil {
			return cli.Newf(cli.CodeConflict, "the checkpoint's Hub position does not match its event selection")
		}
		return nil
	}
	r.hubExpected, r.hubPending = true, true
	r.hubUncovered = !fresh && saved == nil
	r.pending++
	r.sources.Go(func() { r.followHub(saved) })
	return nil
}

func (r *accountReplay) acceptHubReady(saved *eventHistoryPosition, first *leapmuxv1.ReadLayoutEventsResponse) error {
	position := &eventHistoryPosition{JournalID: first.GetJournalId(), Sequence: first.GetAfterSequence(), Cursor: first.GetCursor()}
	if !position.valid() || position.Sequence > first.GetHighWaterSequence() ||
		first.GetScannedSequence() < position.Sequence || first.GetScannedSequence() > first.GetHighWaterSequence() ||
		len(first.GetScannedCursor()) == 0 || first.GetEnd() != nil ||
		(saved == nil && (position.Sequence != first.GetHighWaterSequence() || len(first.GetRecords()) != 0 || first.GetGap() != nil)) ||
		(saved != nil && !sameHistoryPosition(saved, position)) {
		return inconsistentLayoutPage()
	}
	if saved == nil {
		if r.hubUncovered {
			event := r.notice(kindStreamGap, "no saved event position covers the Hub layout")
			event.Source, event.GapReason, event.Lost = "hub", "source_unenrolled", lostKinds(sourceHub, true)
			if err := r.print(event); err != nil {
				return err
			}
		}
		r.state.Account.Hub = position
		if err := r.file.save(r.state); err != nil {
			return err
		}
	}
	r.hubLive = true
	r.hub = &layoutEventReplay{
		opts: r.opts, printer: r.session.inv.Printer, ready: true, replayHigh: first.GetHighWaterSequence(),
		state: &eventCheckpoint{JournalID: position.JournalID, Sequence: position.Sequence, Cursor: position.Cursor},
		save: func(next *eventCheckpoint) error {
			r.state.Account.Hub = &eventHistoryPosition{JournalID: next.JournalID, Sequence: next.Sequence, Cursor: next.Cursor}
			return r.file.save(r.state)
		},
	}
	r.markHubResponded()
	return nil
}

func (r *accountReplay) markHubResponded() {
	if r.hubPending {
		r.hubPending = false
		r.pending--
	}
}

func (r *accountReplay) readHubPage(after []byte, wait time.Duration) (*leapmuxv1.ReadLayoutEventsResponse, error) {
	ctx, cancel := rpcDeadline(r.ctx)
	defer cancel()
	page := new(leapmuxv1.ReadLayoutEventsResponse)
	err := hubCallUnary(ctx, r.session.c, leapmuxv1connect.WorkspaceServiceReadLayoutEventsProcedure,
		&leapmuxv1.ReadLayoutEventsRequest{AfterCursor: after, WaitMillis: uint32(wait.Milliseconds())}, page)
	return page, err
}

func (r *accountReplay) followHub(saved *eventHistoryPosition) {
	var cursor []byte
	if saved != nil {
		cursor = saved.Cursor
	}
	backoff := newReconnectBackoff(activityReconnectInitial, activityReconnectMax)
	var wait time.Duration
	started := false
	for r.ctx.Err() == nil {
		page, err := r.readHubPage(cursor, wait)
		if err == nil && !started {
			err = r.deliverSource(r.ctx, errInvalidLayoutHistory, func() (bool, error) {
				return false, r.acceptHubReady(saved, page)
			})
			if err == nil {
				started, wait = true, layoutReplayWait
				select {
				case <-r.readySignal:
				case <-r.ctx.Done():
					return
				}
			}
		}
		if err == nil {
			err = r.deliverSource(r.ctx, errInvalidLayoutHistory, func() (bool, error) {
				if r.opts.limit > 0 {
					r.hub.opts.limit = r.opts.limit - r.workerPrinted
				}
				done, err := r.hub.acceptPage(page)
				r.hubLive = err == nil
				return done, err
			})
		}
		if r.ctx.Err() != nil {
			return
		}
		if err == nil {
			cursor = page.GetScannedCursor()
			if gap := page.GetGap(); gap != nil {
				cursor = gap.GetCursor()
			}
			backoff = newReconnectBackoff(activityReconnectInitial, activityReconnectMax)
			continue
		}
		failure := cli.FromError(err)
		if err := r.deliver(r.ctx, func() (bool, error) { return false, r.hubFailed(failure) }); err != nil ||
			!failure.Retryable() || !waitAccountReplayRetry(r.ctx, backoff.afterSession(false)) {
			return
		}
	}
}

func (r *accountReplay) hubFailed(failure *cli.Error) error {
	r.hubLive = false
	r.markHubResponded()
	r.hubUncovered = r.state.Account.Hub == nil
	if !failure.Retryable() {
		r.hubStopped = failure
	}
	event := r.notice(kindStreamUnavailable, failure.Message)
	event.Source, event.Code, event.Message, event.Retrying = "hub", string(failure.Code), failure.Message, boolPtr(failure.Retryable())
	return r.print(event)
}
