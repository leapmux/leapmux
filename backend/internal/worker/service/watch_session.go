package service

import (
	"context"
	"log/slog"
	"sync"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/channel"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/gitutil"
	"google.golang.org/protobuf/proto"
)

// watchSession owns one client's WatchEvents stream for the life of that
// stream: the interest it currently holds, the goroutine that applies revisions
// to it in order, and the registrations it leaves in the WatcherManager.
//
// It exists because the stream is now long-lived. A revision arrives on the
// session's receive goroutine, which must not block, while applying one does
// batched DB reads, git shell-outs and a replay burst -- so the frame handler
// only parks the request and the loop does the work.
type watchSession struct {
	svc       *Service
	channelID string
	sessionID uint64
	sender    channel.ResponseWriter
	ctx       context.Context
	cancel    context.CancelFunc

	// pending is a COALESCING SLOT, not a queue: every request states the whole
	// interest, so a newer one supersedes an older one outright.
	mu      sync.Mutex
	pending *leapmuxv1.WatchEventsRequest
	notify  chan struct{} // cap 1
}

func newWatchSession(svc *Service, sender channel.ResponseWriter) *watchSession {
	ctx, cancel := context.WithCancel(bgCtx())
	channelID := sender.ChannelID()
	return &watchSession{
		svc:       svc,
		channelID: channelID,
		sessionID: svc.Watchers.BeginSession(channelID),
		sender:    sender,
		ctx:       ctx,
		cancel:    cancel,
		notify:    make(chan struct{}, 1),
	}
}

// submit parks req as the newest pending revision and wakes the apply loop.
func (s *watchSession) submit(req *leapmuxv1.WatchEventsRequest) {
	s.mu.Lock()
	s.pending = req
	s.mu.Unlock()
	select {
	case s.notify <- struct{}{}:
	default:
	}
}

// OnClientFrame implements channel.StreamController. Unmarshal failure logs and
// drops -- a malformed revision must not kill a healthy subscription.
func (s *watchSession) OnClientFrame(payload []byte) {
	var req leapmuxv1.WatchEventsRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		slog.Warn("WatchEvents: dropping undecodable revision",
			"channel_id", s.channelID, "error", err)
		return
	}
	s.submit(&req)
}

// OnCancel implements channel.StreamController. Idempotent.
func (s *watchSession) OnCancel() {
	s.svc.Watchers.UnwatchSession(s.channelID, s.sessionID)
	s.cancel()
}

func (s *watchSession) run(release func()) {
	if release != nil {
		defer release()
	}
	defer s.svc.Watchers.UnwatchSession(s.channelID, s.sessionID)
	for {
		select {
		case <-s.ctx.Done():
			_ = s.sender.SendStream(&leapmuxv1.InnerStreamMessage{End: true})
			return
		case <-s.notify:
			s.mu.Lock()
			req := s.pending
			s.pending = nil
			s.mu.Unlock()
			if req == nil {
				continue
			}
			s.apply(req)
		}
	}
}

type resolveResult struct {
	applied    bool
	entries    []watchEntry
	rejections []*leapmuxv1.WatchRejection
	// Agent-only (set by resolveAgents; ignored for terminals).
	agentRows []db.Agent
	agentReqs []*leapmuxv1.WatchAgentEntry
	// Terminal-only (set by resolveTerminals; ignored for agents).
	termRows    []db.Terminal
	termIDs     []string
	termOffsets map[string]int64
}

func (s *watchSession) apply(r *leapmuxv1.WatchEventsRequest) {
	if s.ctx.Err() != nil {
		return
	}
	agentResult := s.resolveAgents(r.GetAgents())
	termResult := s.resolveTerminals(r.GetTerminals())

	// Re-check after DB work: OnCancel may have retired this session, or a
	// successor may own the channel — either way, do not re-register.
	if s.ctx.Err() != nil || !s.svc.Watchers.isOwner(s.channelID, s.sessionID) {
		return
	}

	// Snapshot prior modes from the registry BEFORE replacing watches so
	// promotion detection has a single source of truth (no session-local maps).
	prevAgentModes := s.svc.Watchers.AgentModesForChannel(s.channelID)
	prevTermModes := s.svc.Watchers.TerminalModesForChannel(s.channelID)

	if agentResult.applied {
		s.svc.Watchers.SetAgentWatchesForSession(s.channelID, s.sessionID, agentResult.entries, s.sender)
	}
	if termResult.applied {
		s.svc.Watchers.SetTerminalWatchesForSession(s.channelID, s.sessionID, termResult.entries, s.sender)
	}

	// Re-check ownership before the ack: SetAgentWatchesForSession /
	// SetTerminalWatchesForSession silently no-op when a successor has claimed
	// the channel, so an unconditional ack here would report "applied" for a
	// revision whose registrations were never installed — the client would
	// commit it as authority and believe a FULL interest the worker does not
	// hold. A superseded session sends nothing; the successor owns the ack.
	if s.ctx.Err() != nil || !s.svc.Watchers.isOwner(s.channelID, s.sessionID) {
		return
	}

	ack := &leapmuxv1.WatchUpdateAck{
		UpdateId:          r.GetUpdateId(),
		RejectedAgents:    agentResult.rejections,
		RejectedTerminals: termResult.rejections,
	}
	_ = broadcastWatchEvent(s.sender, &leapmuxv1.WatchEventsResponse{
		Event: &leapmuxv1.WatchEventsResponse_UpdateAck{UpdateAck: ack},
	})

	// Compute promotions for applied kinds only.
	var promotedAgents []*leapmuxv1.WatchAgentEntry
	var promotedAgentRows []db.Agent
	if agentResult.applied {
		for i, e := range agentResult.entries {
			if isModePromotion(e.mode, prevAgentModes[e.id]) {
				promotedAgents = append(promotedAgents, agentResult.agentReqs[i])
				promotedAgentRows = append(promotedAgentRows, agentResult.agentRows[i])
			}
		}
	}

	var promotedTermIDs []string
	var promotedTermRows []db.Terminal
	var promotedOffsets map[string]int64
	if termResult.applied {
		promotedOffsets = make(map[string]int64)
		for i, e := range termResult.entries {
			if isModePromotion(e.mode, prevTermModes[e.id]) {
				promotedTermIDs = append(promotedTermIDs, e.id)
				promotedTermRows = append(promotedTermRows, termResult.termRows[i])
				promotedOffsets[e.id] = termResult.termOffsets[e.id]
			}
		}
	}

	if len(promotedAgents) == 0 && len(promotedTermIDs) == 0 {
		return
	}

	// Re-check ownership before the (expensive, multi-send) catch-up burst:
	// OnCancel or a successor BeginSession can retire this session during the
	// resolve/register window above, and the replay loop only gates sends on
	// transport-death (replaySink.alive), not on ownership. Without this check
	// a superseded/cancelled session ships a full catch-up burst via a sender
	// the client no longer reads.
	if s.ctx.Err() != nil || !s.svc.Watchers.isOwner(s.channelID, s.sessionID) {
		return
	}

	sink := newReplaySink(s.sender)
	var replayGitStatuses []*leapmuxv1.AgentGitStatus
	if sink.alive() && len(promotedAgentRows) > 0 {
		dirs := make([]string, len(promotedAgentRows))
		for i, row := range promotedAgentRows {
			dirs[i] = row.WorkingDir
		}
		replayGitStatuses = gitutil.BatchGetGitStatus(bgCtx(), dirs)
	} else {
		replayGitStatuses = make([]*leapmuxv1.AgentGitStatus, len(promotedAgentRows))
	}

	// Terminal catch-up first so screen restore is not HOL-blocked behind
	// a multi-page agent message replay on the same revision.
	for i, termID := range promotedTermIDs {
		if !sink.alive() {
			break
		}
		s.svc.replayTerminalCatchUp(sink, termID, promotedOffsets[termID], promotedTermRows[i])
	}
	for i, agentEntry := range promotedAgents {
		if !sink.alive() {
			break
		}
		s.svc.replayAgentCatchUp(sink, agentEntry, promotedAgentRows[i], replayGitStatuses[i])
	}
	if !sink.alive() {
		s.cancel()
	}
}

func normalizeMode(mode leapmuxv1.WatchMode) leapmuxv1.WatchMode {
	if mode == leapmuxv1.WatchMode_WATCH_MODE_UNSPECIFIED {
		return leapmuxv1.WatchMode_WATCH_MODE_NOTIFY
	}
	return mode
}

func isModePromotion(newMode, oldMode leapmuxv1.WatchMode) bool {
	return modeIsFull(newMode) && !modeIsFull(oldMode)
}

// lookupFailedRejections builds a LOOKUP_FAILED rejection for every requested
// id. Shared by resolveAgents and resolveTerminals so the transient-failure
// ack shape is stated once — a List*ByIDs miss says nothing about whether an
// entity exists, so every requested id is reported with the transient reason
// and that kind's prior registrations are left untouched.
func lookupFailedRejections(ids []string) []*leapmuxv1.WatchRejection {
	rejections := make([]*leapmuxv1.WatchRejection, len(ids))
	for i, id := range ids {
		rejections[i] = &leapmuxv1.WatchRejection{
			EntityId: id,
			Reason:   leapmuxv1.WatchRejectionReason_WATCH_REJECTION_REASON_LOOKUP_FAILED,
		}
	}
	return rejections
}

func (s *watchSession) resolveAgents(agents []*leapmuxv1.WatchAgentEntry) resolveResult {
	requestedIDs := make([]string, 0, len(agents))
	entries := make([]*leapmuxv1.WatchAgentEntry, 0, len(agents))
	seen := make(map[string]struct{}, len(agents))
	for _, a := range agents {
		id := a.GetAgentId()
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		requestedIDs = append(requestedIDs, id)
		entries = append(entries, a)
	}

	if len(requestedIDs) == 0 {
		return resolveResult{applied: true}
	}

	rows, err := s.svc.Queries.ListAgentsByIDs(bgCtx(), requestedIDs)
	if err != nil {
		slog.Error("WatchEvents: ListAgentsByIDs failed", "error", err)
		return resolveResult{applied: false, rejections: lookupFailedRejections(requestedIDs)}
	}

	byID := make(map[string]db.Agent, len(rows))
	for _, row := range rows {
		byID[row.ID] = row
	}

	var (
		outEntries []watchEntry
		outReqs    []*leapmuxv1.WatchAgentEntry
		outRows    []db.Agent
		rejections []*leapmuxv1.WatchRejection
	)
	for _, a := range entries {
		id := a.GetAgentId()
		row, ok := byID[id]
		if !ok {
			rejections = append(rejections, &leapmuxv1.WatchRejection{
				EntityId: id,
				Reason:   leapmuxv1.WatchRejectionReason_WATCH_REJECTION_REASON_NOT_FOUND,
			})
			continue
		}
		mode := normalizeMode(a.GetMode())
		outEntries = append(outEntries, watchEntry{id: id, mode: mode})
		outReqs = append(outReqs, a)
		outRows = append(outRows, row)
	}
	return resolveResult{
		applied:    true,
		entries:    outEntries,
		agentReqs:  outReqs,
		agentRows:  outRows,
		rejections: rejections,
	}
}

func (s *watchSession) resolveTerminals(terminals []*leapmuxv1.WatchTerminalEntry) resolveResult {
	requestedIDs := make([]string, 0, len(terminals))
	offsets := make(map[string]int64, len(terminals))
	modes := make(map[string]leapmuxv1.WatchMode, len(terminals))
	for _, t := range terminals {
		id := t.GetTerminalId()
		if _, dup := offsets[id]; dup {
			continue
		}
		requestedIDs = append(requestedIDs, id)
		offsets[id] = t.GetAfterOffset()
		modes[id] = normalizeMode(t.GetMode())
	}

	if len(requestedIDs) == 0 {
		return resolveResult{applied: true, termOffsets: offsets}
	}

	rows, err := s.svc.Queries.ListTerminalsByIDs(bgCtx(), requestedIDs)
	if err != nil {
		slog.Warn("WatchEvents: ListTerminalsByIDs failed", "error", err)
		return resolveResult{applied: false, rejections: lookupFailedRejections(requestedIDs)}
	}

	byID := make(map[string]db.Terminal, len(rows))
	for _, row := range rows {
		byID[row.ID] = row
	}

	var (
		outEntries []watchEntry
		outRows    []db.Terminal
		outIDs     []string
		rejections []*leapmuxv1.WatchRejection
	)
	for _, id := range requestedIDs {
		row, ok := byID[id]
		if !ok {
			rejections = append(rejections, &leapmuxv1.WatchRejection{
				EntityId: id,
				Reason:   leapmuxv1.WatchRejectionReason_WATCH_REJECTION_REASON_NOT_FOUND,
			})
			continue
		}
		outEntries = append(outEntries, watchEntry{id: id, mode: modes[id]})
		outRows = append(outRows, row)
		outIDs = append(outIDs, id)
	}
	return resolveResult{
		applied:     true,
		entries:     outEntries,
		termRows:    outRows,
		termIDs:     outIDs,
		termOffsets: offsets,
		rejections:  rejections,
	}
}
