package cmd

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/coder/quartz"

	"github.com/leapmux/leapmux/channelwire"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
)

// validateLayoutPageHeader validates the header fields of page against saved
// (when non-nil) or requires a fresh tail start when saved is nil.
func validateLayoutPageHeader(page *leapmuxv1.ReadLayoutEventsResponse, saved *eventHistoryPosition) (*eventHistoryPosition, error) {
	pos := &eventHistoryPosition{
		JournalID: page.GetJournalId(),
		Sequence:  page.GetAfterSequence(),
		Cursor:    page.GetCursor(),
	}
	high, scanned := page.GetHighWaterSequence(), page.GetScannedSequence()
	if !pos.valid() || pos.Sequence > scanned || scanned > high || len(page.GetScannedCursor()) == 0 {
		return nil, inconsistentLayoutPage()
	}
	if saved != nil {
		if !sameHistoryPosition(saved, pos) {
			return nil, inconsistentLayoutPage()
		}
	} else if pos.Sequence != high {
		return nil, inconsistentLayoutPage()
	}
	return pos, nil
}

// layoutPageConsumer decodes, prints, and checkpoints ReadLayoutEventsResponse
// pages. Both layoutEventReplay and accountReplay compose it so Hub replay does
// not inherit single-source snapshot exit or mutate shared limit state.
type layoutPageConsumer struct {
	workspace  string
	filter     kindFilter
	printer    *eventPrinter
	clock      quartz.Clock
	position   *eventHistoryPosition
	replayHigh int64
	dirty      bool
	persist    func(*eventHistoryPosition) error
}

func (c *layoutPageConsumer) nowClock() quartz.Clock {
	if c.clock == nil {
		c.clock = quartz.NewReal()
	}
	return c.clock
}

func (c *layoutPageConsumer) notice(kind string) streamEvent {
	return streamEvent{Kind: kind, Time: c.nowClock().Now().UTC(), Source: "hub", WorkspaceID: c.workspace}
}

func (c *layoutPageConsumer) caughtUp() bool {
	return c.position != nil && c.position.Sequence >= c.replayHigh
}

func (c *layoutPageConsumer) advanceInMemory(sequence int64, cursor []byte) {
	next := *c.position
	next.Sequence, next.Cursor = sequence, cursor
	c.position = &next
	c.dirty = true
}

func (c *layoutPageConsumer) saveNow(sequence int64, cursor []byte) error {
	next := *c.position
	next.Sequence, next.Cursor = sequence, cursor
	if err := c.persist(&next); err != nil {
		return err
	}
	c.position = &next
	c.dirty = false
	return nil
}

func (c *layoutPageConsumer) flush() error {
	if !c.dirty || c.position == nil {
		return nil
	}
	if err := c.persist(c.position); err != nil {
		return err
	}
	c.dirty = false
	return nil
}

// consumePage validates and consumes one Hub layout page after ready admission.
// It returns endReached when a workspace stream ends cleanly, limitReached when
// --limit has been satisfied, or an error on protocol/output/save failure.
func (c *layoutPageConsumer) consumePage(page *leapmuxv1.ReadLayoutEventsResponse) (endReached, limitReached bool, err error) {
	if _, err := validateLayoutPageHeader(page, c.position); err != nil {
		return false, false, err
	}
	after, high, scanned := page.GetAfterSequence(), page.GetHighWaterSequence(), page.GetScannedSequence()
	if end := page.GetEnd(); end != nil {
		if c.workspace == "" || end.GetFinalSequence() != after || high != after || scanned != after ||
			len(page.GetRecords()) != 0 || page.GetGap() != nil {
			return false, false, inconsistentLayoutPage()
		}
		return true, false, c.flush()
	}
	if gap := page.GetGap(); gap != nil {
		resume := gap.GetResumeAfterSequence()
		if len(page.GetRecords()) != 0 || scanned != after || resume <= after || resume > high || len(gap.GetCursor()) == 0 {
			return false, false, inconsistentLayoutPage()
		}
		event := c.notice(kindStreamGap)
		event.JournalID, event.Sequence, event.AfterSequence = c.position.JournalID, &resume, &after
		event.GapReason, event.Lost = "retention", layoutCheckpointKinds(c.filter)
		event.Summary = fmt.Sprintf("Hub layout events expired after sequence %d", after)
		if _, err := c.printer.printCheckpointed(event); err != nil {
			return false, false, err
		}
		if err := c.saveNow(resume, gap.GetCursor()); err != nil {
			return false, false, err
		}
		return false, false, nil
	}
	for _, record := range page.GetRecords() {
		sequence := record.GetSequence()
		event, ok := retainedLayoutEvent(record.GetFact(), c.filter)
		if !ok || sequence <= after || sequence > scanned || len(record.GetCursor()) == 0 {
			return false, false, inconsistentLayoutPage()
		}
		event.Time = time.UnixMilli(record.GetObservedUnixMillis()).UTC()
		event.JournalID, event.Sequence, event.Replayed = c.position.JournalID, &sequence, new(sequence <= c.replayHigh)
		if event.Kind == kindStreamGap {
			event.WorkspaceID, event.AfterSequence = c.workspace, &after
		}
		if c.filter.selects(event.Kind) || event.Kind == kindStreamGap {
			selected, err := c.printer.printCheckpointed(event)
			if err != nil {
				return false, false, err
			}
			if err := c.saveNow(sequence, record.GetCursor()); err != nil {
				return false, false, err
			}
			if selected && event.Kind != kindStreamGap {
				if c.printer.recordCounted() {
					return false, true, nil
				}
			}
		} else {
			c.advanceInMemory(sequence, record.GetCursor())
		}
		after = sequence
	}
	if scanned > after {
		c.advanceInMemory(scanned, page.GetScannedCursor())
	}
	if err := c.flush(); err != nil {
		return false, false, err
	}
	return false, false, nil
}

type layoutEventReplay struct {
	opts      eventsOptions
	printer   *eventPrinter
	clock     quartz.Clock
	workspace string
	kinds     []string
	state     *eventCheckpoint
	save      func(*eventCheckpoint) error
	ready     bool
	consumer  *layoutPageConsumer
}

func (s *session) runLayoutCheckpointedEvents(ctx context.Context, clock quartz.Clock, opts eventsOptions) error {
	checkpoint, state, err := openCheckedEventCheckpoint(opts.checkpoint, layoutCheckpointScope)
	if err != nil {
		return err
	}
	defer checkpoint.close()
	workspaceID, err := s.resolveLayoutCheckpointWorkspace(ctx, opts.workspace, state)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	kinds, err := validateLayoutCheckpointFilter(opts.filter)
	if err != nil {
		return err
	}
	if state != nil {
		if state.Layout.WorkspaceID != workspaceID {
			return cli.Newf(cli.CodeConflict, "the event checkpoint belongs to a different layout scope")
		}
		if err := verifyCheckpointFilter(state, kinds); err != nil {
			return err
		}
	}
	replay := layoutEventReplay{
		opts:      opts,
		printer:   newEventPrinter(s.inv.Printer, opts.filter, opts.limit),
		clock:     clock,
		workspace: workspaceID,
		kinds:     kinds,
		state:     state,
		save:      checkpoint.save,
	}
	return replay.run(ctx, s)
}

func (s *session) resolveLayoutCheckpointWorkspace(ctx context.Context, selector string, saved *eventCheckpoint) (string, error) {
	if selector == "" {
		return "", nil
	}
	if saved != nil && saved.Layout != nil && saved.Layout.WorkspaceID != "" &&
		resolve.ExplicitID(resolve.KindWorkspace, selector) == saved.Layout.WorkspaceID {
		return saved.Layout.WorkspaceID, nil
	}
	callCtx, callCancel := rpcDeadline(ctx)
	callSession := &session{inv: s.inv, c: s.c, ctx: callCtx, cancel: callCancel, located: s.located}
	defer callSession.Close()
	target, err := callSession.resolveOne(resolve.KindWorkspace, selector)
	return target.ID, err
}

func (r *layoutEventReplay) nowClock() quartz.Clock {
	if r.clock == nil {
		r.clock = quartz.NewReal()
	}
	return r.clock
}

func (r *layoutEventReplay) flush() error {
	if r.consumer == nil {
		return nil
	}
	return r.consumer.flush()
}

func (r *layoutEventReplay) readInitialPage(ctx context.Context, s *session) (*leapmuxv1.ReadLayoutEventsResponse, error) {
	request := &leapmuxv1.ReadLayoutEventsRequest{WorkspaceId: r.workspace}
	if r.state != nil {
		request.AfterCursor = r.state.Layout.Position.Cursor
	}
	callCtx, cancel := rpcDeadline(ctx)
	defer cancel()
	page := new(leapmuxv1.ReadLayoutEventsResponse)
	err := hubCallUnary(callCtx, s.c, leapmuxv1connect.WorkspaceServiceReadLayoutEventsProcedure, request, page)
	return page, err
}

type layoutStreamFrame struct {
	page *leapmuxv1.ReadLayoutEventsResponse
	err  error
}

func (r *layoutEventReplay) streamPages(ctx context.Context, s *session, heartbeat <-chan time.Time) (finished, retry, delivered bool, err error) {
	streamCtx, cancel := context.WithCancel(ctx)
	frames := make(chan layoutStreamFrame, 32)
	done := make(chan struct{})
	sessionFn := hubUserEventsWithOptions(s.c, channelwire.UserEventsOptions{
		Topics:            []leapmuxv1.UserEventTopic{leapmuxv1.UserEventTopic_USER_EVENT_TOPIC_LAYOUT_JOURNAL},
		LayoutWorkspaceID: r.workspace,
		LayoutAfterCursor: r.state.Layout.Position.Cursor,
	})
	go func() {
		defer close(done)
		streamErr := sessionFn(streamCtx, func(evt *leapmuxv1.WatchUserEvent) error {
			page := evt.GetLayoutEvents()
			if page == nil {
				return nil
			}
			select {
			case frames <- layoutStreamFrame{page: page}:
				return nil
			case <-streamCtx.Done():
				return streamCtx.Err()
			}
		})
		select {
		case frames <- layoutStreamFrame{err: streamErr}:
		case <-streamCtx.Done():
		}
	}()
	defer func() {
		cancel()
		<-done
	}()
	for {
		select {
		case <-ctx.Done():
			return true, false, delivered, r.flush()
		case <-heartbeat:
			if r.ready {
				if err := r.emitHeartbeat(); err != nil {
					return false, false, delivered, err
				}
			}
		case frame := <-frames:
			if frame.page == nil {
				if frame.err == nil {
					return false, true, delivered, errHubStreamEnded
				}
				return false, true, delivered, frame.err
			}
			delivered = true
			donePage, err := r.acceptPage(frame.page)
			if err != nil {
				return false, false, delivered, err
			}
			if donePage {
				return true, false, delivered, r.flush()
			}
		}
	}
}

func (r *layoutEventReplay) run(ctx context.Context, s *session) error {
	defer func() { _ = r.flush() }()
	clock := r.nowClock()
	backoff := newReconnectBackoff(activityReconnectInitial, activityReconnectMax)
	var heartbeat <-chan time.Time
	if r.opts.heartbeat > 0 {
		ticker := clock.NewTicker(r.opts.heartbeat, "events-layout-heartbeat")
		defer ticker.Stop("events-layout-heartbeat")
		heartbeat = ticker.C
	}
	for ctx.Err() == nil {
		if !r.ready {
			page, err := r.readInitialPage(ctx, s)
			if ctx.Err() != nil {
				return r.flush()
			}
			if err != nil {
				if err := r.handleRetryableError(ctx, clock, &backoff, false, err); err != nil {
					return err
				}
				continue
			}
			backoff = newReconnectBackoff(activityReconnectInitial, activityReconnectMax)
			finished, err := r.acceptPage(page)
			if err != nil {
				return err
			}
			if finished {
				return r.flush()
			}
		}
		finished, retry, delivered, err := r.streamPages(ctx, s, heartbeat)
		if ctx.Err() != nil {
			return r.flush()
		}
		if finished {
			return err
		}
		if !retry {
			return err
		}
		if flushErr := r.flush(); flushErr != nil {
			return flushErr
		}
		if err := r.handleRetryableError(ctx, clock, &backoff, delivered, err); err != nil {
			return err
		}
	}
	return r.flush()
}

func (r *layoutEventReplay) handleRetryableError(ctx context.Context, clock quartz.Clock, backoff *reconnectBackoff, delivered bool, err error) error {
	failure := cli.FromError(err)
	if r.opts.noReconnect || !failure.Retryable() {
		return failure
	}
	event := unavailableEvent(r.notice(kindStreamUnavailable), failure, true)
	if _, err := r.printer.printCheckpointed(event); err != nil {
		return err
	}
	if !waitReplayRetry(ctx, clock, backoff.afterSession(delivered)) {
		return nil
	}
	return nil
}

func (r *layoutEventReplay) emitHeartbeat() error {
	event := r.notice(kindStreamHeartbeat)
	event.Summary = "watching Hub layout history"
	_, err := r.printer.printCheckpointed(event)
	return err
}

func (r *layoutEventReplay) acceptPage(page *leapmuxv1.ReadLayoutEventsResponse) (bool, error) {
	if !r.ready {
		var saved *eventHistoryPosition
		if r.state != nil {
			saved = r.state.Layout.Position
		}
		pos, err := validateLayoutPageHeader(page, saved)
		if err != nil {
			return false, err
		}
		after, high := pos.Sequence, page.GetHighWaterSequence()
		event := r.notice(kindStreamReady)
		event.JournalID, event.Sequence = pos.JournalID, &after
		event.Summary = "watching Hub layout history"
		if _, err := r.printer.printCheckpointed(event); err != nil {
			return false, err
		}
		if r.state == nil {
			initial := &eventCheckpoint{
				Version: eventCheckpointVersion,
				Scope:   layoutCheckpointScope,
				Filter:  r.kinds,
				Layout: &layoutEventCheckpoint{
					WorkspaceID: r.workspace,
					Position: &eventHistoryPosition{
						JournalID: pos.JournalID,
						Sequence:  after,
						Cursor:    pos.Cursor,
					},
				},
			}
			if err := r.save(initial); err != nil {
				return false, err
			}
			r.state = initial
		}
		r.ready = true
		r.consumer = &layoutPageConsumer{
			workspace:  r.workspace,
			filter:     r.opts.filter,
			printer:    r.printer,
			clock:      r.nowClock(),
			position:   pos,
			replayHigh: high,
			persist: func(nextPos *eventHistoryPosition) error {
				layoutCopy := *r.state.Layout
				layoutCopy.Position = &eventHistoryPosition{
					JournalID: nextPos.JournalID,
					Sequence:  nextPos.Sequence,
					Cursor:    nextPos.Cursor,
				}
				next := *r.state
				next.Layout = &layoutCopy
				if err := r.save(&next); err != nil {
					return err
				}
				r.state = &next
				return nil
			},
		}
		if r.opts.snapshot && r.consumer.caughtUp() {
			return true, nil
		}
	}
	endReached, limitReached, err := r.consumer.consumePage(page)
	if err != nil {
		return false, err
	}
	if endReached || limitReached {
		return true, nil
	}
	if r.opts.snapshot && r.consumer.caughtUp() {
		return true, nil
	}
	return false, nil
}

func (r *layoutEventReplay) notice(kind string) streamEvent {
	return streamEvent{Kind: kind, Time: r.nowClock().Now().UTC(), Source: "hub", WorkspaceID: r.workspace}
}

var errInvalidLayoutHistory = errors.New("the Hub sent an inconsistent layout event position")

func inconsistentLayoutPage() error {
	return cli.Wrap(cli.CodeInternal, errInvalidLayoutHistory, "")
}

func retainedLayoutEvent(fact *leapmuxv1.LayoutEventFact, filter kindFilter) (streamEvent, bool) {
	if fact.GetLoss() != nil {
		return streamEvent{
			Kind:      kindStreamGap,
			Source:    "hub",
			GapReason: "unreadable_lifecycle",
			Lost:      layoutCheckpointKinds(filter),
			Summary:   "the Hub could not read a stored workspace lifecycle change",
		}, true
	}
	if workspace := fact.GetWorkspace(); workspace != nil {
		event := streamEvent{WorkspaceID: workspace.GetWorkspaceId(), Title: workspace.GetTitle()}
		switch workspace.GetOperation() {
		case leapmuxv1.WorkspaceLifecycleOp_WORKSPACE_LIFECYCLE_OP_CREATE:
			event.Kind, event.Summary = kindWorkspaceCreated, fmt.Sprintf("%q", event.Title)
		case leapmuxv1.WorkspaceLifecycleOp_WORKSPACE_LIFECYCLE_OP_RENAME:
			event.Kind, event.Summary = kindWorkspaceRenamed, fmt.Sprintf("to %q", event.Title)
		case leapmuxv1.WorkspaceLifecycleOp_WORKSPACE_LIFECYCLE_OP_DELETE:
			event.Kind, event.Summary = kindWorkspaceDeleted, "deleted"
		case leapmuxv1.WorkspaceLifecycleOp_WORKSPACE_LIFECYCLE_OP_UNSPECIFIED:
			return streamEvent{}, false
		}
		return event, event.WorkspaceID != "" && event.Kind != ""
	}
	tab := fact.GetTab()
	if tab.GetTab().GetTabId() == "" || tab.GetTab().GetTabType() == leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED {
		return streamEvent{}, false
	}
	before, after := tab.GetBefore(), tab.GetAfter()
	event := streamEvent{TabID: tab.GetTab().GetTabId(), TabType: tabTypeName(tab.GetTab().GetTabType())}
	event.Summary = event.TabType + " tab"
	switch tab.GetKind() {
	case leapmuxv1.LayoutTabChangeKind_LAYOUT_TAB_CHANGE_KIND_OPENED:
		event.Kind, event.WorkspaceID, event.WorkerID = kindTabOpened, after.GetWorkspaceId(), after.GetWorkerId()
	case leapmuxv1.LayoutTabChangeKind_LAYOUT_TAB_CHANGE_KIND_CLOSED:
		event.Kind, event.WorkspaceID, event.WorkerID = kindTabClosed, before.GetWorkspaceId(), before.GetWorkerId()
	case leapmuxv1.LayoutTabChangeKind_LAYOUT_TAB_CHANGE_KIND_MOVED:
		event.Kind, event.WorkspaceID, event.FromWorkspaceID = kindTabMoved, after.GetWorkspaceId(), before.GetWorkspaceId()
		event.WorkerID = after.GetWorkerId()
		if after == nil {
			event.WorkerID = before.GetWorkerId()
			event.Summary += " left the watched workspace"
		} else if before == nil {
			event.Summary += " entered the watched workspace"
		}
	case leapmuxv1.LayoutTabChangeKind_LAYOUT_TAB_CHANGE_KIND_UNSPECIFIED:
		return streamEvent{}, false
	}
	return event, event.Kind != "" && (event.WorkspaceID != "" || event.FromWorkspaceID != "")
}
