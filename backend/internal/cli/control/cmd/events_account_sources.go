package cmd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"

	"connectrpc.com/connect/v2"
	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/channelwire"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
)

func (r *accountReplay) startWorker(key workerHistoryKey, previouslyWatching bool) {
	ctx, cancel := context.WithCancel(r.ctx)
	position := r.workerPosition(key)
	watch := &accountWorkerWatch{
		replaySourceState: replaySourceState{
			pending:   !r.ready,
			uncovered: position == nil && previouslyWatching,
		},
		key:    key,
		cancel: cancel,
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
		scan, err := r.streamWorker(ctx, watch, cursor)
		cursor = scan.cursor
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, errWorkerScanComplete) {
			backoff = newReconnectBackoff(activityReconnectInitial, activityReconnectMax)
			if scan.progress {
				if err := r.deliver(ctx, func() (bool, error) { return false, r.flush() }); err != nil {
					return
				}
			}
			if scan.waitReady {
				select {
				case <-r.readySignal:
				case <-ctx.Done():
					return
				}
			} else if scan.caughtUp && !waitReplayRetry(ctx, r.nowClock(), accountRotationInterval) {
				return
			}
			continue
		}
		if isSlotContentionError(err) {
			r.enterRotationMode()
		}
		failure := accountWorkerFailure(err)
		retrying := !r.opts.noReconnect && failure.Retryable()
		if err := r.deliver(ctx, func() (bool, error) {
			if flushErr := r.flush(); flushErr != nil {
				return false, flushErr
			}
			return false, r.workerFailed(watch, failure, retrying)
		}); err != nil || !retrying || !waitReplayRetry(ctx, r.nowClock(), backoff.afterSession(scan.progress)) {
			return
		}
	}
}

// isSlotContentionError reports whether err indicates that the Hub or Worker
// rejected the open because its concurrent connection slots are saturated,
// triggering bounded-slice rotation across Workers.
func isSlotContentionError(err error) bool {
	if err == nil {
		return false
	}
	if channelwire.IsTooManyConnectionsError(err) {
		return true
	}
	return strings.Contains(err.Error(), "connection slot is busy")
}

func (r *accountReplay) streamWorker(ctx context.Context, watch *accountWorkerWatch, cursor []byte) (workerScan, error) {
	scan := workerScan{cursor: cursor}
	rotating := r.isRotating()
	if rotating {
		select {
		case r.workerSlots <- struct{}{}:
		case <-ctx.Done():
			return scan, ctx.Err()
		}
		defer func() { <-r.workerSlots }()
	}

	key := watch.key
	request := &leapmuxv1.WatchWorkerSemanticEventsRequest{TabType: key.tabType, AfterCursor: cursor}
	payload, err := proto.Marshal(request)
	if err != nil {
		return scan, err
	}

	var attempt context.Context
	var cancel context.CancelFunc
	var openTimedOut atomic.Bool
	var rotatedOut atomic.Bool
	var stopOpening func()
	if rotating {
		attempt, cancel = rpcDeadline(ctx)
		defer cancel()
	} else {
		attempt, cancel = context.WithCancel(ctx)
		openTimer := r.nowClock().AfterFunc(defaultInnerRPCTimeout, func() {
			openTimedOut.Store(true)
			cancel()
		}, "events-account-worker-open")
		stopOpening = func() { openTimer.Stop("events-account-worker-open") }
		defer stopOpening()

		watcherDone := make(chan struct{})
		go func() {
			defer close(watcherDone)
			select {
			case <-r.rotationMode:
				rotatedOut.Store(true)
				cancel()
			case <-attempt.Done():
			}
		}()
		defer func() {
			cancel()
			<-watcherDone
		}()
	}

	var high int64
	var seenReady bool
	frames := 0
	onFrame := func(raw []byte) error {
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
			if stopOpening != nil {
				stopOpening()
			}
			select {
			case <-r.readySignal:
			default:
				if rotating || r.isRotating() {
					scan.waitReady = true
					return errWorkerScanComplete
				}
				// Stay connected on the same stream while sibling sources admit
				// unless rotation mode activates under slot contention.
				select {
				case <-r.readySignal:
				case <-r.rotationMode:
					scan.waitReady = true
					return errWorkerScanComplete
				case <-attempt.Done():
					return attempt.Err()
				}
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
		if rotating || r.isRotating() {
			if scan.caughtUp || frames >= accountWorkerScanFrames {
				return errWorkerScanComplete
			}
			return nil
		}
		if scan.caughtUp {
			if err := r.deliver(ctx, func() (bool, error) { return false, r.flush() }); err != nil {
				return err
			}
		}
		return nil
	}

	err = r.streamWorkerSemanticEvents(attempt, key.id, payload, onFrame)
	if ctx.Err() != nil {
		return scan, ctx.Err()
	}
	if rotatedOut.Load() {
		select {
		case <-r.readySignal:
		default:
			if seenReady {
				scan.waitReady = true
			}
		}
		return scan, errWorkerScanComplete
	}
	if openTimedOut.Load() && !seenReady {
		return scan, context.DeadlineExceeded
	}
	return scan, err
}

func (r *accountReplay) streamWorkerSemanticEvents(ctx context.Context, workerID string, payload []byte, onFrame func([]byte) error) error {
	procedure := leapmuxv1connect.WorkerWatchServiceWatchWorkerSemanticEventsProcedure
	if r.workerChannels != nil {
		ch, release, err := r.workerChannels.acquire(ctx, workerID)
		if err != nil {
			return err
		}
		defer release()
		return readEncryptedSemanticWatch(ctx, ch, procedure, payload, func(raw []byte, rpcError *leapmuxv1.RpcError, end bool) error {
			if err := semanticFrameError(rpcError); err != nil {
				return err
			}
			if len(raw) != 0 {
				if err := onFrame(raw); err != nil {
					return err
				}
			}
			if end {
				return io.EOF
			}
			return nil
		})
	}
	return streamWorkerInner(ctx, r.session.c, workerID, procedure, payload, onFrame)
}

func accountWorkerFailure(err error) *cli.Error {
	if err == nil {
		err = io.EOF
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

func (r *accountReplay) workerFailed(watch *accountWorkerWatch, failure *cli.Error, retrying bool) error {
	if r.workers[watch.key] != watch {
		return nil
	}
	watch.live = false
	if !retrying {
		watch.stopped = failure
	}
	r.markSourceResponded(&watch.replaySourceState)
	watch.uncovered = r.workerPosition(watch.key) == nil
	event := unavailableEvent(r.workerNotice(watch, kindStreamUnavailable, ""), failure, retrying)
	return r.printNotice(event)
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
	event, next, isGap, err := r.workerEvent(watch, position, frame)
	if err != nil {
		return false, err
	}
	if !isGap && !r.opts.filter.selects(event.Kind) {
		r.state.Account.Workers[watch.key.id][tabTypeName(watch.key.tabType)] = next
		r.dirty = true
		return false, nil
	}
	selected, err := r.printer.printCheckpointed(event)
	if err != nil {
		return false, err
	}
	if err := r.saveWorkerPosition(watch.key, next); err != nil {
		return false, err
	}
	if !isGap && selected {
		return r.printer.recordCounted(), nil
	}
	return false, nil
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
			if err := r.printNotice(event); err != nil {
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
	r.markSourceResponded(&watch.replaySourceState)
	return nil
}

func (r *accountReplay) workerEvent(watch *accountWorkerWatch, position *eventHistoryPosition,
	frame *leapmuxv1.WatchWorkerSemanticEventsResponse,
) (streamEvent, *eventHistoryPosition, bool, error) {
	cur := semanticHistoryCursor{
		journalID: position.JournalID,
		sequence:  position.Sequence,
		cursor:    position.Cursor,
	}
	if record := frame.GetRecord(); record != nil {
		tab := record.GetTab()
		if tab.GetTabId() == "" || tab.GetTabType() != watch.key.tabType {
			return streamEvent{}, nil, false, invalidSemanticEventSequence()
		}
		event, nextCur, err := cur.decodeRecord(
			scopedTab{ID: tab.GetTabId(), Type: tab.GetTabType(), WorkerID: watch.key.id},
			record.GetSequence(), record.GetObservedUnixMillis(), record.GetCursor(),
			record.GetFact(), record.GetCurrentRequestHandle(), watch.high,
		)
		if err != nil {
			return streamEvent{}, nil, false, err
		}
		return event, nextCur.position(), false, nil
	}
	if gap := frame.GetGap(); gap != nil {
		event, nextCur, err := cur.decodeGap(r.workerNotice(watch, kindStreamGap, ""), gap, r.opts.filter, watch.key.tabType)
		if err != nil {
			return streamEvent{}, nil, true, err
		}
		return event, nextCur.position(), true, nil
	}
	return streamEvent{}, nil, false, invalidSemanticEventSequence()
}

func (r *accountReplay) saveWorkerPosition(key workerHistoryKey, position *eventHistoryPosition) error {
	r.state.Account.Workers[key.id][tabTypeName(key.tabType)] = position
	return r.saveStateNow()
}

func sameHistoryPosition(old, new *eventHistoryPosition) bool {
	return old.JournalID == new.JournalID && old.Sequence == new.Sequence && bytes.Equal(old.Cursor, new.Cursor)
}

func (r *accountReplay) startHub(fresh bool) error {
	selected := r.opts.filter.selectsJournal(replayHubLayout)
	saved := r.state.Account.Hub
	if !selected {
		if saved != nil {
			return cli.Newf(cli.CodeConflict, "the checkpoint's Hub position does not match its event selection")
		}
		if !r.opts.snapshot {
			r.sources.Go(func() { r.followHub(nil) })
		}
		return nil
	}
	r.hubExpected = true
	r.hubState.pending = true
	r.hubState.uncovered = !fresh && saved == nil
	r.pending++
	r.sources.Go(func() { r.followHub(saved) })
	return nil
}

func (r *accountReplay) acceptHubReady(saved *eventHistoryPosition, first *leapmuxv1.ReadLayoutEventsResponse) error {
	position, err := validateLayoutPageHeader(first, saved)
	if err != nil || first.GetEnd() != nil ||
		(saved == nil && (len(first.GetRecords()) != 0 || first.GetGap() != nil)) {
		return inconsistentLayoutPage()
	}
	if saved == nil {
		if r.hubState.uncovered {
			event := r.notice(kindStreamGap, "no saved event position covers the Hub layout")
			event.Source, event.GapReason, event.Lost = "hub", "source_unenrolled", layoutCheckpointKinds(r.opts.filter)
			if err := r.printNotice(event); err != nil {
				return err
			}
		}
		r.state.Account.Hub = position
		if err := r.saveStateNow(); err != nil {
			return err
		}
	}
	if !r.hubState.admitted {
		r.hubState.high = first.GetHighWaterSequence()
		r.hubState.admitted = true
	}
	r.hubState.live = true
	r.hub = &layoutPageConsumer{
		filter:     r.opts.filter,
		printer:    r.printer,
		clock:      r.nowClock(),
		position:   position,
		replayHigh: r.hubState.high,
		persist: func(next *eventHistoryPosition) error {
			r.state.Account.Hub = &eventHistoryPosition{JournalID: next.JournalID, Sequence: next.Sequence, Cursor: next.Cursor}
			return r.saveStateNow()
		},
	}
	r.markSourceResponded(&r.hubState)
	return nil
}

func (r *accountReplay) readHubPage(after []byte) (*leapmuxv1.ReadLayoutEventsResponse, error) {
	ctx, cancel := rpcDeadline(r.ctx)
	defer cancel()
	page := new(leapmuxv1.ReadLayoutEventsResponse)
	err := hubCallUnary(ctx, r.session.c, leapmuxv1connect.WorkspaceServiceReadLayoutEventsProcedure,
		&leapmuxv1.ReadLayoutEventsRequest{AfterCursor: after}, page)
	return page, err
}

func advanceLayoutCursor(page *leapmuxv1.ReadLayoutEventsResponse) []byte {
	if gap := page.GetGap(); gap != nil {
		return gap.GetCursor()
	}
	return page.GetScannedCursor()
}

// followHub owns the single Hub connection for account replay:
//  1. When Hub layout kinds are selected, it performs one non-blocking
//     ReadLayoutEvents admission RPC so the cursor is validated, stream.ready
//     can emit immediately even with zero new layout records, and workspace:read
//     denials degrade cleanly to workers-only without breaking Worker streams.
//  2. It then holds ONE /ws/userevents WebSocket subscribed to
//     [WORKERS, LAYOUT_JOURNAL] (or [WORKERS] alone when Hub layout is not
//     active), reconciling Worker membership on WorkersChanged via atomic
//     GetWorkerInventory and consuming LayoutEvents pages pushed by the Hub.
func (r *accountReplay) followHub(saved *eventHistoryPosition) {
	var cursor []byte
	if saved != nil {
		cursor = saved.Cursor
	}
	backoff := newReconnectBackoff(activityReconnectInitial, activityReconnectMax)
	hubActive := r.hubExpected
	hubAdmitted := false

	// Phase 1: admit Hub layout (if selected) before opening the WebSocket.
	for hubActive && !hubAdmitted && r.ctx.Err() == nil {
		page, err := r.readHubPage(cursor)
		if err == nil {
			err = r.deliverSource(r.ctx, errInvalidLayoutHistory, func() (bool, error) {
				return false, r.acceptHubReady(saved, page)
			})
			if err == nil {
				hubAdmitted = true
				select {
				case <-r.readySignal:
				case <-r.ctx.Done():
					return
				}
				err = r.deliverSource(r.ctx, errInvalidLayoutHistory, func() (bool, error) {
					_, limitReached, consumeErr := r.hub.consumePage(page)
					r.hubState.live = consumeErr == nil
					return limitReached, consumeErr
				})
				if err == nil {
					cursor = advanceLayoutCursor(page)
					backoff = newReconnectBackoff(activityReconnectInitial, activityReconnectMax)
					break
				}
			}
		}
		if r.ctx.Err() != nil {
			return
		}
		failure := cli.FromError(err)
		retrying := !r.opts.noReconnect && failure.Retryable()
		if deliverErr := r.deliver(r.ctx, func() (bool, error) { return false, r.hubFailed(failure, retrying) }); deliverErr != nil {
			return
		}
		if !retrying {
			hubActive = false
			if r.opts.noReconnect || r.opts.snapshot {
				return
			}
			// Hub layout was refused permanently (for example, missing
			// workspace:read); continue with workers-only WebSocket notifications.
			break
		}
		if !waitReplayRetry(r.ctx, r.nowClock(), backoff.afterSession(false)) {
			return
		}
	}

	if r.ctx.Err() != nil {
		return
	}
	if r.opts.snapshot {
		if !hubActive || (r.hub != nil && r.hub.caughtUp()) {
			return
		}
	}

	// Phase 2: stream live / backlog updates over ONE /ws/userevents WebSocket.
	for r.ctx.Err() == nil {
		var topics []leapmuxv1.UserEventTopic
		if !r.opts.snapshot {
			topics = append(topics, leapmuxv1.UserEventTopic_USER_EVENT_TOPIC_WORKERS)
		}
		var opts channelwire.UserEventsOptions
		if hubActive {
			topics = append(topics, leapmuxv1.UserEventTopic_USER_EVENT_TOPIC_LAYOUT_JOURNAL)
			opts.LayoutAfterCursor = cursor
		}
		if len(topics) == 0 {
			return
		}
		opts.Topics = topics

		delivered := false
		sessionFn := hubUserEventsWithOptions(r.session.c, opts)
		err := sessionFn(r.ctx, func(evt *leapmuxv1.WatchUserEvent) error {
			delivered = true
			switch e := evt.GetEvent().(type) {
			case *leapmuxv1.WatchUserEvent_LayoutEvents:
				if !hubActive || e.LayoutEvents == nil {
					return nil
				}
				page := e.LayoutEvents
				if err := r.deliverSource(r.ctx, errInvalidLayoutHistory, func() (bool, error) {
					_, limitReached, consumeErr := r.hub.consumePage(page)
					r.hubState.live = consumeErr == nil
					return limitReached, consumeErr
				}); err != nil {
					return err
				}
				cursor = advanceLayoutCursor(page)
				return nil
			case *leapmuxv1.WatchUserEvent_WorkersChanged:
				if r.opts.snapshot {
					return nil
				}
				inventory, invErr := readAccountInventory(r.ctx, r.session)
				if r.ctx.Err() != nil {
					return r.ctx.Err()
				}
				return r.deliver(r.ctx, func() (bool, error) {
					if invErr == nil {
						return false, r.applyInventory(inventory, false)
					}
					failure := cli.FromError(invErr)
					if r.opts.noReconnect || !failure.Retryable() {
						return false, failure
					}
					event := unavailableEvent(streamEvent{Time: r.nowClock().Now().UTC(), Source: "worker"}, failure, true)
					return false, r.printNotice(event)
				})
			default:
				return nil
			}
		})
		if r.ctx.Err() != nil {
			return
		}
		if err == nil {
			err = errHubStreamEnded
		}
		failure := cli.FromError(err)
		retrying := !r.opts.noReconnect && failure.Retryable()
		if hubActive {
			if deliverErr := r.deliver(r.ctx, func() (bool, error) { return false, r.hubFailed(failure, retrying) }); deliverErr != nil {
				return
			}
			if !retrying {
				hubActive = false
				if r.opts.noReconnect || r.opts.snapshot {
					return
				}
				continue
			}
		} else {
			if deliverErr := r.deliver(r.ctx, func() (bool, error) {
				if !retrying {
					return false, failure
				}
				event := unavailableEvent(streamEvent{Time: r.nowClock().Now().UTC(), Source: "worker"}, failure, true)
				return false, r.printNotice(event)
			}); deliverErr != nil || !retrying {
				return
			}
		}
		if !waitReplayRetry(r.ctx, r.nowClock(), backoff.afterSession(delivered)) {
			return
		}
	}
}

func (r *accountReplay) hubFailed(failure *cli.Error, retrying bool) error {
	r.hubState.live = false
	r.markSourceResponded(&r.hubState)
	r.hubState.uncovered = r.state.Account.Hub == nil
	if !retrying {
		r.hubState.stopped = failure
	}
	event := unavailableEvent(streamEvent{Time: r.nowClock().Now().UTC(), Source: "hub"}, failure, retrying)
	return r.printNotice(event)
}
