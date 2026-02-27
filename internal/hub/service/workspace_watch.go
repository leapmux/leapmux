package service

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sync"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/agentmgr"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/generated/db"
	"github.com/leapmux/leapmux/internal/hub/terminalmgr"
)

// watchEventsMerged is the internal type sent through the merged channel
// by per-watcher goroutines in WatchEvents.
type watchEventsMerged struct {
	agentEvent    *leapmuxv1.AgentEvent
	terminalEvent *leapmuxv1.TerminalEvent
}

// watchEventsCore contains the core WatchEvents logic decoupled from transport.
// send is called for each WatchEventsResponse; return an error to stop the stream.
func (s *WorkspaceService) watchEventsCore(
	ctx context.Context,
	user *auth.UserInfo,
	req *leapmuxv1.WatchEventsRequest,
	send func(*leapmuxv1.WatchEventsResponse) error,
) error {
	ws, err := s.getVisibleWorkspace(ctx, user, req.GetOrgId(), req.GetWorkspaceId())
	if err != nil {
		return err
	}

	// Collect entries, limiting total to 32.
	const maxEntries = 32
	agentEntries := req.GetAgents()
	terminalEntries := req.GetTerminals()
	total := len(agentEntries) + len(terminalEntries)
	if total > maxEntries {
		// Truncate terminal entries first, then agent entries.
		if len(terminalEntries) > maxEntries-len(agentEntries) {
			if len(agentEntries) >= maxEntries {
				agentEntries = agentEntries[:maxEntries]
				terminalEntries = nil
			} else {
				terminalEntries = terminalEntries[:maxEntries-len(agentEntries)]
			}
		}
	}

	// Deduplicate agent IDs.
	seenAgents := make(map[string]struct{}, len(agentEntries))
	var dedupAgents []*leapmuxv1.WatchAgentEntry
	for _, e := range agentEntries {
		aid := e.GetAgentId()
		if _, ok := seenAgents[aid]; ok {
			continue
		}
		seenAgents[aid] = struct{}{}
		dedupAgents = append(dedupAgents, e)
	}
	agentEntries = dedupAgents

	// Deduplicate terminal IDs.
	seenTerminals := make(map[string]struct{}, len(terminalEntries))
	var dedupTerminals []*leapmuxv1.WatchTerminalEntry
	for _, e := range terminalEntries {
		tid := e.GetTerminalId()
		if _, ok := seenTerminals[tid]; ok {
			continue
		}
		seenTerminals[tid] = struct{}{}
		dedupTerminals = append(dedupTerminals, e)
	}
	terminalEntries = dedupTerminals

	// --- Register watchers before replay to capture live events ---
	type agentWatcher struct {
		agentID string
		watcher *agentmgr.Watcher
	}
	type termWatcher struct {
		terminalID string
		watcher    *terminalmgr.Watcher
	}

	var agentWatchers []agentWatcher
	for _, entry := range agentEntries {
		agentID := entry.GetAgentId()
		w := s.agentMgr.Watch(agentID)
		agentWatchers = append(agentWatchers, agentWatcher{agentID: agentID, watcher: w})
	}
	defer func() {
		for _, aw := range agentWatchers {
			s.agentMgr.Unwatch(aw.agentID, aw.watcher)
		}
	}()

	var termWatchers []termWatcher
	for _, entry := range terminalEntries {
		terminalID := entry.GetTerminalId()
		w := s.termMgr.Watch(terminalID)
		termWatchers = append(termWatchers, termWatcher{terminalID: terminalID, watcher: w})
	}
	defer func() {
		for _, tw := range termWatchers {
			s.termMgr.Unwatch(tw.terminalID, tw.watcher)
		}
	}()

	// Track replayed control request IDs per agent so we can deduplicate
	// against live events that arrived in the watcher channel during replay.
	replayedCRs := make(map[string]map[string]struct{}) // agentID -> set of requestIDs

	// --- Replay historical data and send snapshots for agents ---
	for _, entry := range agentEntries {
		agentID := entry.GetAgentId()
		agent, err := s.queries.GetAgentByID(ctx, agentID)
		if err != nil {
			if err == sql.ErrNoRows {
				slog.Debug("watchEventsCore: agent not found, skipping", "agent_id", agentID)
				continue
			}
			return fmt.Errorf("get agent %s: %w", agentID, err)
		}
		if agent.WorkspaceID != ws.ID {
			slog.Debug("watchEventsCore: agent not in workspace, skipping", "agent_id", agentID, "workspace_id", ws.ID)
			continue
		}

		// Send historical messages if requested (afterSeq >= 0).
		afterSeq := entry.GetAfterSeq()
		if afterSeq >= 0 {
			msgs, err := s.queries.ListMessagesByAgentID(ctx, db.ListMessagesByAgentIDParams{
				AgentID: agentID,
				Seq:     afterSeq,
				Limit:   50,
			})
			if err != nil {
				return fmt.Errorf("list messages for agent %s: %w", agentID, err)
			}
			for i := range msgs {
				if err := send(&leapmuxv1.WatchEventsResponse{
					Event: &leapmuxv1.WatchEventsResponse_AgentEvent{
						AgentEvent: &leapmuxv1.AgentEvent{
							AgentId: agentID,
							Event: &leapmuxv1.AgentEvent_AgentMessage{
								AgentMessage: MessageToProto(&msgs[i]),
							},
						},
					},
				}); err != nil {
					return err
				}
			}
		}

		// Send current status snapshot (check worker online per-agent).
		workerOnline := s.workerMgr.IsOnline(agent.WorkerID)
		catchupSc := AgentStatusChange(&agent, workerOnline)
		if s.agentSvc != nil {
			catchupSc.GitStatus = s.agentSvc.GetGitStatus(agentID)
		}
		if err := send(&leapmuxv1.WatchEventsResponse{
			Event: &leapmuxv1.WatchEventsResponse_AgentEvent{
				AgentEvent: &leapmuxv1.AgentEvent{
					AgentId: agentID,
					Event: &leapmuxv1.AgentEvent_StatusChange{
						StatusChange: catchupSc,
					},
				},
			},
		}); err != nil {
			return err
		}

		// Replay pending control requests and track their IDs.
		pendingCRs, err := s.queries.ListControlRequestsByAgentID(ctx, agentID)
		if err != nil {
			slog.Warn("watchEventsCore: list pending control requests", "agent_id", agentID, "error", err)
		} else {
			for _, cr := range pendingCRs {
				if replayedCRs[agentID] == nil {
					replayedCRs[agentID] = make(map[string]struct{})
				}
				replayedCRs[agentID][cr.RequestID] = struct{}{}

				if err := send(&leapmuxv1.WatchEventsResponse{
					Event: &leapmuxv1.WatchEventsResponse_AgentEvent{
						AgentEvent: &leapmuxv1.AgentEvent{
							AgentId: agentID,
							Event: &leapmuxv1.AgentEvent_ControlRequest{
								ControlRequest: &leapmuxv1.AgentControlRequest{
									AgentId:   cr.AgentID,
									RequestId: cr.RequestID,
									Payload:   cr.Payload,
								},
							},
						},
					},
				}); err != nil {
					return err
				}
			}
		}
	}

	// --- Drain buffered watcher events that arrived during replay ---
	// Any controlRequest events whose requestId was already replayed are
	// duplicates and must be skipped; all other events are forwarded.
	for _, aw := range agentWatchers {
		crSet := replayedCRs[aw.agentID]
	drain:
		for {
			select {
			case ev := <-aw.watcher.C():
				if cr := ev.GetControlRequest(); cr != nil && crSet != nil {
					if _, dup := crSet[cr.GetRequestId()]; dup {
						continue // skip duplicate
					}
				}
				if err := send(&leapmuxv1.WatchEventsResponse{
					Event: &leapmuxv1.WatchEventsResponse_AgentEvent{
						AgentEvent: ev,
					},
				}); err != nil {
					return err
				}
			default:
				break drain
			}
		}
	}

	// Merged channel collects events from all watchers.
	merged := make(chan watchEventsMerged, 64)

	var wg sync.WaitGroup

	// Spawn goroutines that forward agent watcher events into merged.
	for _, aw := range agentWatchers {
		wg.Add(1)
		go func(aw agentWatcher) {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case ev, ok := <-aw.watcher.C():
					if !ok {
						return
					}
					select {
					case merged <- watchEventsMerged{agentEvent: ev}:
					case <-ctx.Done():
						return
					}
				}
			}
		}(aw)
	}

	// Spawn goroutines that forward terminal watcher events into merged.
	for _, tw := range termWatchers {
		wg.Add(1)
		go func(tw termWatcher) {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case ev, ok := <-tw.watcher.C():
					if !ok {
						return
					}
					select {
					case merged <- watchEventsMerged{terminalEvent: ev}:
					case <-ctx.Done():
						return
					}
				}
			}
		}(tw)
	}

	// Close merged channel once all forwarders exit.
	go func() {
		wg.Wait()
		close(merged)
	}()

	// Stream loop: read from merged, wrap and send.
	for {
		select {
		case <-ctx.Done():
			return nil
		case m, ok := <-merged:
			if !ok {
				return nil
			}
			var resp leapmuxv1.WatchEventsResponse
			if m.agentEvent != nil {
				resp.Event = &leapmuxv1.WatchEventsResponse_AgentEvent{
					AgentEvent: m.agentEvent,
				}
			} else if m.terminalEvent != nil {
				resp.Event = &leapmuxv1.WatchEventsResponse_TerminalEvent{
					TerminalEvent: m.terminalEvent,
				}
			}
			if err := send(&resp); err != nil {
				return err
			}
		}
	}
}
