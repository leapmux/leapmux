package service

import (
	"context"
	"crypto/hmac"
	"database/sql"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/channelwire"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/semanticevents"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/internal/worker/eventjournal"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/util/drain"
)

const semanticJournalDeadline = 5 * time.Second
const semanticRetirementTimerTag = "semantic history retirement"

type semanticEventCapture struct {
	mu                         sync.Mutex
	enabled, draining, stopped bool
	starting                   bool
	producersFinished          bool
	journal                    atomic.Pointer[eventjournal.Journal]
	retirements                drain.Counter
	agentMutations             agentPublicationDrain
}

// EnableSemanticEvents is called at Worker bootstrap, before starting producers.
func (svc *Service) EnableSemanticEvents() {
	capture := &svc.semanticEvents
	capture.mu.Lock()
	capture.enabled = true
	capture.mu.Unlock()
	svc.bindSemanticEvents()
}

func (svc *Service) bindSemanticEvents() {
	capture := &svc.semanticEvents
	for {
		capture.mu.Lock()
		owner := svc.RegisteredBy()
		if !capture.enabled || capture.draining || capture.stopped || owner.IsZero() {
			capture.mu.Unlock()
			return
		}
		if journal := capture.journal.Load(); journal != nil {
			if !journal.OwnedBy(owner) {
				journal.Revoke()
			}
			capture.mu.Unlock()
			return
		}
		if capture.starting {
			capture.mu.Unlock()
			return
		}
		capture.starting = true
		capture.mu.Unlock()

		ctx, cancel := context.WithTimeout(context.Background(), semanticJournalDeadline)
		journal, err := eventjournal.Start(ctx, svc.DB, svc.WorkerID, owner, svc.Clock)
		cancel()

		capture.mu.Lock()
		capture.starting = false
		if err != nil {
			capture.mu.Unlock()
			slog.Warn("start Worker event journal failed", "error", err)
			return
		}
		currentOwner := svc.RegisteredBy()
		if !capture.enabled || capture.draining || capture.stopped || !journal.OwnedBy(currentOwner) {
			retry := capture.enabled && !capture.draining && !capture.stopped && !currentOwner.IsZero() && !journal.OwnedBy(currentOwner)
			capture.mu.Unlock()
			stopCtx, stopCancel := context.WithTimeout(context.Background(), semanticJournalDeadline)
			_ = journal.Stop(stopCtx, false)
			stopCancel()
			if retry {
				continue
			}
			return
		}
		capture.journal.Store(journal)
		capture.mu.Unlock()
		return
	}
}

func (svc *Service) recordTerminalFact(tabID string, event *leapmuxv1.TerminalEvent) {
	tab := eventjournal.Tab{Type: leapmuxv1.TabType_TAB_TYPE_TERMINAL, ID: tabID}
	if journal := svc.semanticEvents.journal.Load(); journal != nil && journal.OwnedBy(svc.RegisteredBy()) {
		journal.Record(tab, semanticevents.TerminalFact(event))
	}
}

func (svc *Service) recordAgentFact(agentID string, event *leapmuxv1.AgentEvent) {
	tab := eventjournal.Tab{Type: leapmuxv1.TabType_TAB_TYPE_AGENT, ID: agentID}
	if journal := svc.semanticEvents.journal.Load(); journal != nil && journal.OwnedBy(svc.RegisteredBy()) {
		fact := semanticevents.AgentFact(event, func(claim string) []byte {
			return journal.RequestInstance(agentID, claim)
		})
		journal.Record(tab, fact)
	}
}

func (svc *Service) retireTerminalEventHistory(terminalID string) {
	svc.retireSemanticHistory(leapmuxv1.TabType_TAB_TYPE_TERMINAL, []string{terminalID}, func(ctx context.Context) bool {
		return svc.TerminalStartup.waitForTabStartup(ctx, terminalID) == nil &&
			svc.Terminals.WaitForTabPublishers(ctx, terminalID) == nil
	})
}

func (svc *Service) retireSemanticHistory(tabType leapmuxv1.TabType, ids []string, join func(context.Context) bool) bool {
	capture := &svc.semanticEvents
	capture.mu.Lock()
	if capture.stopped {
		capture.mu.Unlock()
		return true
	}
	journal := capture.journal.Load()
	if journal == nil || !journal.OwnedBy(svc.RegisteredBy()) {
		capture.mu.Unlock()
		return false
	}
	tabs := make([]eventjournal.Tab, len(ids))
	for i, id := range ids {
		tabs[i] = eventjournal.Tab{Type: tabType, ID: id}
	}
	// Track the seal and the producer join separately. The seal can report
	// a gap while a transport callback still holds the producer join.
	capture.retirements.Add()
	capture.retirements.Add()
	capture.mu.Unlock()
	go func() {
		defer capture.retirements.Done()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		timer := svc.Clock.AfterFunc(semanticJournalDeadline, cancel, semanticRetirementTimerTag)
		defer timer.Stop()
		finished := make(chan bool, 1)
		go func() {
			defer capture.retirements.Done()
			finished <- join(ctx)
		}()
		var drained bool
		select {
		case drained = <-finished:
		case <-ctx.Done():
		}
		journal.Seal(tabs, drained)
	}()
	return true
}

func (svc *Service) stopSemanticEventReaders(producersFinished bool) {
	capture := &svc.semanticEvents
	capture.mu.Lock()
	defer capture.mu.Unlock()
	capture.draining = true
	capture.producersFinished = producersFinished
	if journal := capture.journal.Load(); journal != nil {
		journal.StopReaders()
	}
}

// FinalizeSemanticEvents joins the journal writer after the dispatcher joins
// its admitted handlers. A plain handler can commit a control response after
// the provider processes have stopped.
func (svc *Service) FinalizeSemanticEvents(handlersFinished bool) {
	capture := &svc.semanticEvents
	capture.mu.Lock()
	if capture.stopped {
		capture.mu.Unlock()
		return
	}
	capture.stopped = true
	producersFinished := capture.producersFinished
	journal := capture.journal.Load()
	capture.mu.Unlock()
	if journal == nil {
		return
	}
	retireBudget := svc.ShutdownDeadline.Cap(semanticJournalDeadline)
	sealsFinished := drain.WaitBounded(capture.retirements.DoneChan(), retireBudget, "")
	stopBudget := svc.ShutdownDeadline.Cap(semanticJournalDeadline)
	ctx, cancel := context.WithTimeout(context.Background(), stopBudget)
	defer cancel()
	mutationsFinished := capture.agentMutations.waitAll(ctx) == nil
	if err := journal.Stop(ctx, producersFinished && handlersFinished && sealsFinished && mutationsFinished && stopBudget > 0); err != nil {
		slog.Warn("stop Worker event journal failed", "error", err)
	}
}

func registerSemanticEventsHandler(registrar ownerOnlyRegistrar, svc *Service) {
	registerOwnerGuarded(registrar, leapmuxv1connect.WorkerWatchServiceWatchSemanticEventsProcedure, dispatchPlain, svc.watchSemanticEvents)
	registerOwnerGuarded(registrar, leapmuxv1connect.WorkerWatchServiceWatchWorkerSemanticEventsProcedure, dispatchPlain, svc.watchWorkerSemanticEvents)
}

type semanticWatchControl struct{ cancel context.CancelFunc }

// A history watch keeps the scope and cursor from its opening request.
func (semanticWatchControl) OnClientFrame([]byte) {}
func (c semanticWatchControl) OnCancel()          { c.cancel() }

type semanticRequestHandleTarget struct {
	agentID   string
	request   *leapmuxv1.SemanticAgentRequest
	setHandle func(string)
}

type semanticFrameOps[F proto.Message] struct {
	open           func(ctx context.Context, journal *eventjournal.Journal) (read func(context.Context, int64) ([]F, <-chan struct{}, error), readyFrame proto.Message, after int64, err error)
	requestBinding func(F) (semanticRequestHandleTarget, bool)
	isFinished     func(F) bool
	nextAfter      func(F) int64
}

func (svc *Service) watchSemanticEvents(ctx context.Context, caller channel.Caller, req *leapmuxv1.WatchSemanticEventsRequest, sender channel.ResponseWriter) {
	tab := eventjournal.Tab{Type: req.GetTab().GetTabType(), ID: req.GetTab().GetTabId()}
	if !tab.Valid() {
		sendSemanticWatchError(sender, codes.InvalidArgument, "an agent or terminal tab is required")
		return
	}
	streamSemanticEvents(svc, ctx, caller, tab.Type, sender, semanticFrameOps[*leapmuxv1.WatchSemanticEventsResponse]{
		open: func(ctx context.Context, journal *eventjournal.Journal) (func(context.Context, int64) ([]*leapmuxv1.WatchSemanticEventsResponse, <-chan struct{}, error), proto.Message, int64, error) {
			reader, ready, err := journal.Open(ctx, tab, req.GetAfterCursor())
			if err != nil {
				return nil, nil, 0, err
			}
			return reader.Read, &leapmuxv1.WatchSemanticEventsResponse{Event: &leapmuxv1.WatchSemanticEventsResponse_Ready{Ready: ready}}, ready.GetAfterSequence(), nil
		},
		requestBinding: func(frame *leapmuxv1.WatchSemanticEventsResponse) (semanticRequestHandleTarget, bool) {
			record := frame.GetRecord()
			request := record.GetFact().GetAgentRequest()
			if request == nil {
				return semanticRequestHandleTarget{}, false
			}
			return semanticRequestHandleTarget{
				agentID:   tab.ID,
				request:   request,
				setHandle: func(handle string) { record.CurrentRequestHandle = handle },
			}, true
		},
		isFinished: func(frame *leapmuxv1.WatchSemanticEventsResponse) bool {
			return frame.GetRetired() != nil
		},
		nextAfter: func(frame *leapmuxv1.WatchSemanticEventsResponse) int64 {
			if gap := frame.GetGap(); gap != nil {
				return gap.GetResumeAfterSequence()
			}
			return frame.GetRecord().GetSequence()
		},
	})
}

func streamSemanticEvents[F proto.Message](svc *Service, ctx context.Context, caller channel.Caller, tabType leapmuxv1.TabType, sender channel.ResponseWriter, ops semanticFrameOps[F]) {
	scope, grant := semanticHistoryReadGrant(tabType)
	if !caller.Allows(scope) {
		sendSemanticWatchError(sender, codes.PermissionDenied, "event history requires "+grant)
		return
	}
	journal := svc.semanticEvents.journal.Load()
	if journal == nil {
		sendSemanticWatchError(sender, codes.Unavailable, eventjournal.ErrUnavailable.Error())
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	release, ok := sender.BindStream(semanticWatchControl{cancel: cancel})
	if !ok {
		sendSemanticWatchError(sender, codes.FailedPrecondition, "stream binding unavailable")
		return
	}
	defer release()

	checkOwner := func() bool {
		if !callerIsWorkerOwner(svc, caller.UserID) || !journal.OwnedBy(caller.UserID) {
			sendSemanticWatchError(sender, codes.PermissionDenied, workerOwnerDenial)
			return false
		}
		return true
	}
	if !checkOwner() {
		return
	}
	read, readyFrame, after, err := ops.open(ctx, journal)
	if err != nil {
		reportSemanticWatchError(ctx, sender, err)
		return
	}
	if !checkOwner() {
		return
	}
	if !sendSemanticWatchFrame(sender, readyFrame, false) {
		return
	}
	for ctx.Err() == nil && checkOwner() {
		frames, changed, err := read(ctx, after)
		if err != nil {
			reportSemanticWatchError(ctx, sender, err)
			return
		}
		if err := populateSemanticRequestHandles(svc, ctx, journal, frames, ops.requestBinding); err != nil {
			reportSemanticWatchError(ctx, sender, err)
			return
		}
		for _, frame := range frames {
			if ctx.Err() != nil || !checkOwner() {
				return
			}
			finished := ops.isFinished(frame)
			if !sendSemanticWatchFrame(sender, frame, finished) {
				return
			}
			if finished {
				return
			}
			after = ops.nextAfter(frame)
		}
		if len(frames) == 0 {
			select {
			case <-ctx.Done():
			case <-changed:
			}
		}
	}
}

func isAnswerableSemanticPromptKind(kind leapmuxv1.SemanticControlPromptKind) bool {
	return kind >= leapmuxv1.SemanticControlPromptKind_SEMANTIC_CONTROL_PROMPT_KIND_PERMISSION &&
		kind <= leapmuxv1.SemanticControlPromptKind_SEMANTIC_CONTROL_PROMPT_KIND_DIALOG
}

func populateSemanticRequestHandles[F proto.Message](
	svc *Service,
	ctx context.Context,
	journal *eventjournal.Journal,
	frames []F,
	extract func(F) (semanticRequestHandleTarget, bool),
) error {
	var pending []semanticRequestHandleTarget
	for _, frame := range frames {
		target, ok := extract(frame)
		if ok && isAnswerableSemanticPromptKind(target.request.GetPromptKind()) {
			pending = append(pending, target)
		}
	}
	return svc.resolveSemanticRequestHandles(ctx, journal, pending)
}

func (svc *Service) resolveSemanticRequestHandles(ctx context.Context, journal *eventjournal.Journal, pending []semanticRequestHandleTarget) error {
	if len(pending) == 0 {
		return nil
	}
	tx, err := svc.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	queries := db.New(tx)
	canAnswerCache := make(map[string]bool, len(pending))
	for _, item := range pending {
		if !isAnswerableSemanticPromptKind(item.request.GetPromptKind()) {
			continue
		}
		canAnswer, cached := canAnswerCache[item.agentID]
		if !cached {
			canAnswer, err = queries.CanAnswerSemanticAgentRequest(ctx, item.agentID)
			if err != nil {
				return err
			}
			canAnswerCache[item.agentID] = canAnswer
		}
		if !canAnswer {
			continue
		}
		claim, found, err := svc.Output.controls.lookupClaim(ctx, queries, item.agentID, item.request.GetRequestId())
		if err != nil {
			return err
		}
		if !found || claim == "" || !hmac.Equal(item.request.GetRequestInstance(), journal.RequestInstance(item.agentID, claim)) {
			continue
		}
		// lookupClaim already proved the matching claim exists in the live request store.
		// Check only whether an answer record already moved this claim out of READY.
		_, err = storedControlResponseState(ctx, queries, item.agentID, item.request.GetRequestId(), claim)
		if errors.Is(err, sql.ErrNoRows) {
			item.setHandle(claim)
			continue
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func semanticHistoryReadGrant(tabType leapmuxv1.TabType) (leapmuxv1.Scope, string) {
	if tabType == leapmuxv1.TabType_TAB_TYPE_AGENT {
		return leapmuxv1.Scope_SCOPE_AGENT_READ, "agent:read"
	}
	return leapmuxv1.Scope_SCOPE_TERMINAL_READ, "terminal:read"
}

func sendSemanticWatchFrame(sender channel.ResponseWriter, frame proto.Message, end bool) bool {
	raw, err := proto.Marshal(frame)
	if err != nil {
		sendSemanticWatchError(sender, codes.Internal, "could not encode a semantic event")
		return false
	}
	if budget := sender.MaxPayloadBudget(); budget != 0 && len(raw) > budget {
		sendSemanticWatchError(sender, codes.ResourceExhausted, "a semantic event exceeds this connection's payload limit")
		return false
	}
	return sender.SendStream(&leapmuxv1.InnerStreamMessage{Payload: raw, End: end}) == nil
}

func reportSemanticWatchError(ctx context.Context, sender channel.ResponseWriter, err error) {
	if ctx.Err() != nil {
		return
	}
	if errors.Is(err, eventjournal.ErrCursor) {
		sendSemanticWatchError(sender, codes.FailedPrecondition, eventjournal.ErrCursor.Error())
		return
	}
	if errors.Is(err, eventjournal.ErrFeedCursor) {
		sendSemanticWatchError(sender, codes.FailedPrecondition, eventjournal.ErrFeedCursor.Error())
		return
	}
	if errors.Is(err, eventjournal.ErrAdmission) {
		sendSemanticWatchError(sender, codes.NotFound, eventjournal.ErrAdmission.Error())
		return
	}
	slog.Warn("read Worker event journal failed", "error", err)
	sendSemanticWatchError(sender, codes.Unavailable, eventjournal.ErrUnavailable.Error())
}

func sendSemanticWatchError(sender channel.ResponseWriter, code codes.Code, message string) {
	_ = sender.SendStream(channelwire.NewStreamError(int32(code), message))
}
