package kimi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

func (a *Agent) holdNativeTranscriptFrame(agentID string, frame kimiFrame) bool {
	run := a.run(agentID)
	linked := agentID == kimiMainAgentID
	if !linked {
		_, linked = a.children.get(agentID)
	}
	if linked && !run.pendingNativeRecovery {
		return false
	}
	if !linked && run.pendingNativeRecovery {
		return true
	}
	a.queueNativeTranscriptFrame(run, frame)
	if linked {
		a.startNativeTranscriptRecovery(run)
	}
	return true
}

func (a *Agent) replayPendingNativeTranscript(run *kimiRun) {
	if run.pendingNativeRecovery {
		a.startNativeTranscriptRecovery(run)
		return
	}
	pending := a.takePendingNativeFrames(run)
	for _, frame := range pending {
		if frame.Type == contracts.KimiEventTranscriptReset {
			a.handleTranscriptReset(frame)
		} else {
			a.handleTranscriptOperations(frame)
		}
	}
}

type kimiTranscriptPage struct {
	AgentID      string            `json:"agent_id"`
	Items        []json.RawMessage `json:"items"`
	HasMore      bool              `json:"has_more"`
	Interactions []json.RawMessage `json:"interactions"`
	Attachments  []json.RawMessage `json:"attachments"`
	Prompts      []json.RawMessage `json:"prompts"`
	Seq          *int64            `json:"seq"`
}

func (a *Agent) readNativeTranscript(ctx context.Context, sessionID, agentID string) (kimiTranscriptPage, error) {
	query := url.Values{"agent_id": {agentID}, "page_size": {"100"}}
	var snapshot kimiTranscriptPage
	if err := a.api.callQuery(ctx, "GET", kimiSessionPath(sessionID, "/transcript"), query, nil, &snapshot); err != nil {
		return snapshot, err
	}
	if snapshot.AgentID != agentID || snapshot.Items == nil || snapshot.Seq == nil || *snapshot.Seq < 0 {
		return snapshot, fmt.Errorf("the Kimi server supplied an invalid live transcript for agent %q", agentID)
	}
	page := snapshot
	pages := [][]json.RawMessage{snapshot.Items}
	itemCount := len(snapshot.Items)
	turns := make(map[string]struct{})
	rememberTurns := func(items []json.RawMessage) error {
		for _, raw := range items {
			var item struct {
				Kind   string `json:"kind"`
				TurnID string `json:"turnId"`
			}
			if json.Unmarshal(raw, &item) != nil || item.Kind != contracts.KimiTranscriptNodeTurn {
				continue
			}
			if item.TurnID == "" {
				return fmt.Errorf("the Kimi transcript has an empty turn identity")
			}
			if _, duplicate := turns[item.TurnID]; duplicate {
				return fmt.Errorf("the Kimi transcript repeats turn %q", item.TurnID)
			}
			turns[item.TurnID] = struct{}{}
		}
		return nil
	}
	if err := rememberTurns(snapshot.Items); err != nil {
		return snapshot, err
	}
	seen := make(map[string]struct{})
	for page.HasMore {
		before := ""
		for _, raw := range page.Items {
			var item struct {
				Kind   string `json:"kind"`
				TurnID string `json:"turnId"`
			}
			if json.Unmarshal(raw, &item) == nil && item.Kind == contracts.KimiTranscriptNodeTurn && item.TurnID != "" {
				before = item.TurnID
				break
			}
		}
		if _, repeated := seen[before]; before == "" || repeated {
			return snapshot, fmt.Errorf("the Kimi transcript pagination did not advance for agent %q", agentID)
		}
		seen[before] = struct{}{}
		query.Set("before_turn", before)
		page = kimiTranscriptPage{}
		if err := a.api.callQuery(ctx, "GET", kimiSessionPath(sessionID, "/transcript"), query, nil, &page); err != nil {
			return snapshot, err
		}
		if page.AgentID != agentID || page.Items == nil {
			return snapshot, fmt.Errorf("the Kimi server supplied an invalid transcript page for agent %q", agentID)
		}
		if err := rememberTurns(page.Items); err != nil {
			return snapshot, err
		}
		pages = append(pages, page.Items)
		itemCount += len(page.Items)
	}
	if len(pages) > 1 {
		snapshot.Items = make([]json.RawMessage, 0, itemCount)
		for index := len(pages) - 1; index >= 0; index-- {
			snapshot.Items = append(snapshot.Items, pages[index]...)
		}
	}
	snapshot.HasMore = false
	return snapshot, nil
}

func (a *Agent) finishNativeChildTurn(run *kimiRun, turnID, state string, source kimiFrame) {
	if run.transcript.activeTurn != "" && run.transcript.activeTurn != turnID {
		return
	}
	child, linked := a.children.get(run.agentID)
	if !linked {
		return
	}
	run.retry.Reset()
	run.transcript.activeTurn = ""
	a.Mu.Lock()
	wasActive := run.turnActive
	userTurn := run.userTurn
	run.turnActive = false
	run.userTurn = false
	a.Mu.Unlock()
	if source.Type != contracts.KimiEventTranscriptReset && wasActive {
		a.publishChildTurn(run, false)
	}
	status := kimiChildTurnStatus(state)
	if !userTurn && state == contracts.KimiTranscriptTurnCompleted {
		status = bgtask.StatusEndedWithUnknownOutcome
	}
	closeRetained := agent.OutputRetained(a.closeSubagentRegistryRow(run.agentID, child, status))
	// A reset publishes its final state and releases the child after all records apply.
	if source.Type != contracts.KimiEventTranscriptReset && closeRetained {
		a.queueChildCleanup(run)
	}
}

// restoreCompletedChildTurn prepares fresh completed history without publishing activity.
// A user turn owns its outcome. A non-user turn has no native coordinator join.
func (a *Agent) restoreCompletedChildTurn(run *kimiRun, turnID string, origin kimiTranscriptOrigin) bool {
	if !a.activateChildLifetime(run) || !a.reviveNativeChildRow(run) {
		return false
	}
	run.transcript.activeTurn = turnID
	a.Mu.Lock()
	run.userTurn = origin.Kind == contracts.KimiOriginUser
	a.Mu.Unlock()
	return true
}

func (a *Agent) reviveNativeChildRow(run *kimiRun) bool {
	child, linked := a.children.get(run.agentID)
	if !linked || run.childLifetime == nil {
		return false
	}
	if !child.ended {
		return true
	}
	if err := a.queueRegistryRevive(run.childLifetime.parent, child.rowKey); !agent.OutputRetained(err) {
		return false
	}
	a.children.update(run.agentID, func(current *kimiChild) { current.ended = false })
	return true
}
