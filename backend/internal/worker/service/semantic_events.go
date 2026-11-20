package service

import (
	"context"
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
)

const semanticJournalDeadline = 5 * time.Second

type semanticEventCapture struct {
	mu               sync.Mutex
	enabled, stopped bool
	journal          atomic.Pointer[eventjournal.Journal]
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
	capture.mu.Lock()
	defer capture.mu.Unlock()
	owner := svc.RegisteredBy()
	if !capture.enabled || capture.stopped || owner.IsZero() {
		return
	}
	if journal := capture.journal.Load(); journal != nil {
		if !journal.OwnedBy(owner) {
			journal.Revoke()
		}
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), semanticJournalDeadline)
	defer cancel()
	journal, err := eventjournal.Start(ctx, svc.DB, svc.WorkerID, owner, svc.Clock)
	if err != nil {
		slog.Warn("start Worker event journal failed", "error", err)
		return
	}
	capture.journal.Store(journal)
}

func (svc *Service) recordTerminalFact(tabID string, event *leapmuxv1.TerminalEvent) {
	if journal := svc.semanticEvents.journal.Load(); journal != nil && journal.OwnedBy(svc.RegisteredBy()) {
		journal.Record(tabID, semanticevents.TerminalFact(event))
	}
}

func (svc *Service) stopSemanticEvents(producersStopped bool) {
	capture := &svc.semanticEvents
	capture.mu.Lock()
	defer capture.mu.Unlock()
	capture.stopped = true
	if journal := capture.journal.Load(); journal != nil {
		budget := svc.ShutdownDeadline.Cap(semanticJournalDeadline)
		ctx, cancel := context.WithTimeout(context.Background(), budget)
		defer cancel()
		if err := journal.Stop(ctx, producersStopped && budget > 0); err != nil {
			slog.Warn("stop Worker event journal failed", "error", err)
		}
	}
}

func registerSemanticEventsHandler(registrar ownerOnlyRegistrar, svc *Service) {
	registerOwnerGuarded(registrar, leapmuxv1connect.WorkerWatchServiceWatchSemanticEventsProcedure, dispatchPlain, svc.watchSemanticEvents)
}

type semanticWatchControl struct{ cancel context.CancelFunc }

func (c semanticWatchControl) OnClientFrame([]byte) { c.cancel() }
func (c semanticWatchControl) OnCancel()            { c.cancel() }

func (svc *Service) watchSemanticEvents(ctx context.Context, caller channel.Caller, req *leapmuxv1.WatchSemanticEventsRequest, sender channel.ResponseWriter) {
	tab := req.GetTab()
	if tab.GetTabType() != leapmuxv1.TabType_TAB_TYPE_TERMINAL || tab.GetTabId() == "" {
		sendSemanticWatchError(sender, codes.InvalidArgument, "a terminal tab is required")
		return
	}
	if !caller.Allows(leapmuxv1.Scope_SCOPE_TERMINAL_READ) {
		sendSemanticWatchError(sender, codes.PermissionDenied, "event history requires terminal:read")
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
	checkAccess := func() bool {
		if !checkOwner() {
			return false
		}
		allowed, err := svc.Queries.CanWatchTerminalHistory(ctx, tab.GetTabId())
		if err != nil {
			if ctx.Err() == nil {
				slog.Warn("check terminal event history access failed", "error", err)
				sendSemanticWatchError(sender, codes.Unavailable, "could not check access to terminal event history")
			}
			return false
		}
		if !allowed {
			sendSemanticWatchError(sender, codes.NotFound, "no terminal tab with retained access")
		}
		return allowed
	}
	if !checkAccess() {
		return
	}
	reader, ready, err := journal.Open(ctx, tab.GetTabId(), req.GetAfterCursor())
	if err != nil {
		reportSemanticWatchError(ctx, sender, err)
		return
	}
	if !checkAccess() {
		return
	}
	if !sendSemanticWatchFrame(sender, &leapmuxv1.WatchSemanticEventsResponse{Event: &leapmuxv1.WatchSemanticEventsResponse_Ready{Ready: ready}}) {
		return
	}
	after := ready.GetAfterSequence()
	// Closing an idle tab need not produce a notification. Recheck its
	// eligibility even if no publication wakes this reader.
	recheck := svc.Clock.NewTicker(5 * time.Second)
	defer recheck.Stop()
	for ctx.Err() == nil && checkAccess() {
		frames, changed, err := reader.Read(ctx, after)
		if err != nil {
			reportSemanticWatchError(ctx, sender, err)
			return
		}
		for _, frame := range frames {
			if ctx.Err() != nil || !checkOwner() || !sendSemanticWatchFrame(sender, frame) {
				return
			}
			if gap := frame.GetGap(); gap != nil {
				after = gap.GetResumeAfterSequence()
			} else {
				after = frame.GetRecord().GetSequence()
			}
		}
		if len(frames) == 0 {
			select {
			case <-ctx.Done():
			case <-changed:
			case <-recheck.C:
			}
		}
	}
}

func sendSemanticWatchFrame(sender channel.ResponseWriter, frame *leapmuxv1.WatchSemanticEventsResponse) bool {
	raw, err := proto.Marshal(frame)
	if err != nil {
		sendSemanticWatchError(sender, codes.Internal, "could not encode a terminal event")
		return false
	}
	if budget := sender.MaxPayloadBudget(); budget != 0 && len(raw) > budget {
		sendSemanticWatchError(sender, codes.ResourceExhausted, "a terminal event exceeds this connection's payload limit")
		return false
	}
	return sender.SendStream(&leapmuxv1.InnerStreamMessage{Payload: raw}) == nil
}

func reportSemanticWatchError(ctx context.Context, sender channel.ResponseWriter, err error) {
	if ctx.Err() != nil {
		return
	}
	if errors.Is(err, eventjournal.ErrCursor) {
		sendSemanticWatchError(sender, codes.FailedPrecondition, eventjournal.ErrCursor.Error())
		return
	}
	slog.Warn("read Worker event journal failed", "error", err)
	sendSemanticWatchError(sender, codes.Unavailable, eventjournal.ErrUnavailable.Error())
}

func sendSemanticWatchError(sender channel.ResponseWriter, code codes.Code, message string) {
	_ = sender.SendStream(channelwire.NewStreamError(int32(code), message))
}
