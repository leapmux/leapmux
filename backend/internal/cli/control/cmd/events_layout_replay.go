package cmd

import (
	"bytes"
	"errors"
	"fmt"
	"slices"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
)

const layoutReplayWait = 10 * time.Second

type layoutEventReplay struct {
	opts       eventsOptions
	printer    *cli.Printer
	workspace  string
	kinds      []string
	state      *eventCheckpoint
	save       func(*eventCheckpoint) error
	ready      bool
	replayHigh int64
	printed    int
}

func (s *session) runLayoutCheckpointedEvents(opts eventsOptions) error {
	checkpoint, err := openEventCheckpoint(opts.checkpoint)
	if err != nil {
		return err
	}
	defer checkpoint.close()
	state, err := checkpoint.read()
	if err != nil {
		return err
	}
	if state != nil && state.Scope != layoutCheckpointScope {
		return cli.Newf(cli.CodeConflict, "the event checkpoint belongs to an exact tab")
	}
	workspaceID, err := s.resolveLayoutCheckpointWorkspace(opts.workspace, state)
	if err != nil {
		return err
	}
	kinds := make([]string, 0)
	streamSelected := false
	for _, kind := range eventKinds {
		if !opts.filter.selects(kind.name) {
			continue
		}
		if kind.source == sourceHub {
			kinds = append(kinds, kind.name)
		}
		if kind.source == sourceWorker && opts.filter.selected != nil {
			return cli.Usagef("layout checkpoints only support Hub and stream event kinds").
				WithHint("use an account checkpoint or pass --tab for Worker events")
		}
		if kind.source == sourceStream && kind.name != kindStreamRetired {
			streamSelected = true
		}
	}
	if len(kinds) == 0 && !streamSelected {
		return cli.Usagef("the selected event kinds are unavailable in a Hub layout checkpoint")
	}
	if state != nil {
		if state.WorkspaceID != workspaceID {
			return cli.Newf(cli.CodeConflict, "the event checkpoint belongs to a different layout scope")
		}
		if !slices.Equal(kinds, state.Filter) {
			return cli.Newf(cli.CodeConflict, "the event checkpoint uses a different --kind selection").
				WithHint("use the saved kind selection or another checkpoint file")
		}
	}
	replay := layoutEventReplay{
		opts: opts, printer: s.inv.Printer, workspace: workspaceID, kinds: kinds, state: state, save: checkpoint.save,
	}
	return replay.run(s)
}

func (s *session) resolveLayoutCheckpointWorkspace(selector string, saved *eventCheckpoint) (string, error) {
	if selector == "" {
		return "", nil
	}
	if saved != nil && saved.WorkspaceID != "" && resolve.ExplicitID(resolve.KindWorkspace, selector) == saved.WorkspaceID {
		return saved.WorkspaceID, nil
	}
	bounded := s.bounded()
	defer bounded.Close()
	target, err := bounded.resolveOne(resolve.KindWorkspace, selector)
	return target.ID, err
}

func (r *layoutEventReplay) run(s *session) error {
	backoff := newReconnectBackoff(activityReconnectInitial, activityReconnectMax)
	nextHeartbeat := time.Now().Add(r.opts.heartbeat)
	for s.ctx.Err() == nil {
		request := &leapmuxv1.ReadLayoutEventsRequest{WorkspaceId: r.workspace}
		if r.state != nil {
			request.AfterCursor = r.state.Cursor
		}
		if r.ready {
			wait := layoutReplayWait
			if r.opts.heartbeat > 0 {
				wait = min(wait, max(time.Until(nextHeartbeat), 0))
			}
			request.WaitMillis = uint32(wait.Milliseconds())
		}
		ctx, cancel := rpcDeadline(s.ctx)
		page := new(leapmuxv1.ReadLayoutEventsResponse)
		err := hubCallUnary(ctx, s.c, leapmuxv1connect.WorkspaceServiceReadLayoutEventsProcedure, request, page)
		cancel()
		if s.ctx.Err() != nil {
			return nil
		}
		if err != nil {
			failure := cli.FromError(err)
			if !failure.Retryable() {
				return failure
			}
			event := r.notice(kindStreamUnavailable)
			event.Code, event.Message, event.Summary, event.Retrying = string(failure.Code), failure.Message, failure.Message, boolPtr(true)
			if err := r.print(event); err != nil {
				return err
			}
			timer := time.NewTimer(backoff.afterSession(false))
			select {
			case <-timer.C:
			case <-s.ctx.Done():
				timer.Stop()
				return nil
			}
			continue
		}
		backoff = newReconnectBackoff(activityReconnectInitial, activityReconnectMax)
		finished, err := r.acceptPage(page)
		if err != nil || finished {
			return err
		}
		if r.opts.heartbeat > 0 && !time.Now().Before(nextHeartbeat) {
			event := r.notice(kindStreamHeartbeat)
			event.Summary = "watching Hub layout history"
			if err := r.print(event); err != nil {
				return err
			}
			nextHeartbeat = time.Now().Add(r.opts.heartbeat)
		}
	}
	return nil
}

func (r *layoutEventReplay) acceptPage(page *leapmuxv1.ReadLayoutEventsResponse) (bool, error) {
	after, high, scanned := page.GetAfterSequence(), page.GetHighWaterSequence(), page.GetScannedSequence()
	if page.GetJournalId() == "" || after < 0 || after > scanned || scanned > high ||
		len(page.GetCursor()) == 0 || len(page.GetScannedCursor()) == 0 {
		return false, inconsistentLayoutPage()
	}
	if r.state != nil {
		if r.state.JournalID != page.GetJournalId() || r.state.Sequence != after || !bytes.Equal(r.state.Cursor, page.GetCursor()) {
			return false, inconsistentLayoutPage()
		}
	} else if after != high {
		return false, inconsistentLayoutPage()
	}
	if !r.ready {
		event := r.notice(kindStreamReady)
		event.JournalID, event.Sequence = page.GetJournalId(), &after
		event.Summary = "watching Hub layout history"
		if err := r.print(event); err != nil {
			return false, err
		}
		r.ready, r.replayHigh = true, high
		if r.state == nil {
			initial := &eventCheckpoint{
				Version: eventCheckpointVersion, Scope: layoutCheckpointScope, WorkspaceID: r.workspace,
				JournalID: page.GetJournalId(), Filter: r.kinds, Sequence: after, Cursor: page.GetCursor(),
			}
			if err := r.save(initial); err != nil {
				return false, err
			}
			r.state = initial
		}
	}
	if end := page.GetEnd(); end != nil {
		if r.workspace == "" || end.GetFinalSequence() != after || high != after || scanned != after ||
			len(page.GetRecords()) != 0 || page.GetGap() != nil {
			return false, inconsistentLayoutPage()
		}
		return true, nil
	}
	if gap := page.GetGap(); gap != nil {
		resume := gap.GetResumeAfterSequence()
		if len(page.GetRecords()) != 0 || scanned != after || resume <= after || resume > high || len(gap.GetCursor()) == 0 {
			return false, inconsistentLayoutPage()
		}
		event := r.notice(kindStreamGap)
		event.JournalID, event.Sequence, event.AfterSequence = r.state.JournalID, &resume, &after
		event.GapReason, event.Lost = "retention", lostKinds(sourceHub, true)
		event.Summary = fmt.Sprintf("Hub layout events expired after sequence %d", after)
		if err := r.print(event); err != nil {
			return false, err
		}
		return false, r.savePosition(resume, gap.GetCursor())
	}
	for _, record := range page.GetRecords() {
		sequence := record.GetSequence()
		event, ok := retainedLayoutEvent(record.GetFact())
		if !ok || sequence <= after || sequence > scanned || len(record.GetCursor()) == 0 {
			return false, inconsistentLayoutPage()
		}
		event.Time = time.UnixMilli(record.GetObservedUnixMillis()).UTC()
		event.JournalID, event.Sequence, event.Replayed = r.state.JournalID, &sequence, boolPtr(sequence <= r.replayHigh)
		event.Source = "hub"
		selected := r.opts.filter.selects(event.Kind)
		if event.Kind == kindStreamGap {
			event.WorkspaceID, event.AfterSequence = r.workspace, &after
		}
		if selected || event.Kind == kindStreamGap {
			if err := r.print(event); err != nil {
				return false, err
			}
		}
		if err := r.savePosition(sequence, record.GetCursor()); err != nil {
			return false, err
		}
		if selected && event.Kind != kindStreamGap {
			r.printed++
		}
		if r.opts.limit > 0 && r.printed >= r.opts.limit {
			return true, nil
		}
		after = sequence
	}
	if scanned > after {
		return false, r.savePosition(scanned, page.GetScannedCursor())
	}
	return false, nil
}

func (r *layoutEventReplay) savePosition(sequence int64, cursor []byte) error {
	next := *r.state
	next.Sequence, next.Cursor = sequence, cursor
	if err := r.save(&next); err != nil {
		return err
	}
	r.state = &next
	return nil
}

func (r *layoutEventReplay) print(event streamEvent) error {
	return printCheckpointEvent(r.printer, r.opts.filter, event)
}

func (r *layoutEventReplay) notice(kind string) streamEvent {
	return streamEvent{Kind: kind, Time: time.Now().UTC(), Source: "hub", WorkspaceID: r.workspace}
}

var errInvalidLayoutHistory = errors.New("the Hub sent an inconsistent layout event position")

func inconsistentLayoutPage() error {
	return cli.Wrap(cli.CodeInternal, errInvalidLayoutHistory, "")
}

func retainedLayoutEvent(fact *leapmuxv1.LayoutEventFact) (streamEvent, bool) {
	if fact.GetLoss() != nil {
		return streamEvent{
			Kind: kindStreamGap, GapReason: "unreadable_lifecycle", Lost: lostKinds(sourceHub, true),
			Summary: "the Hub could not read a stored workspace lifecycle change",
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
