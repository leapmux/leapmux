package kimi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"time"
)

const (
	kimiConversationRecoveryTimerTag = "kimi.conversation.recovery"
	kimiMaxPendingEvents             = 256
	kimiPendingAgentBytes            = 16 << 20
	kimiPendingSessionBytes          = 64 << 20
	kimiPendingFrameOverhead         = 256
)

var errKimiConversationRecoveryReplaced = errors.New("the Kimi conversation recovery was replaced")

// The recovery pointer is the input-admission state. The dispatcher owns its
// contents; the reader copies the captured page before it performs network I/O.
type kimiConversationRecovery struct {
	ctx         context.Context
	cancel      context.CancelFunc
	page        *kimiConversationFrame
	frames      []kimiConversationFrame
	bytes       int64
	invalidated bool
}

type kimiConversationPage struct {
	Turns   []kimiConversationTurn `json:"turns"`
	HasMore *bool                  `json:"has_more"`
}

func validateKimiConversationPage(turns []kimiConversationTurn, hasMore *bool) error {
	if turns == nil || hasMore == nil || *hasMore && len(turns) == 0 {
		return fmt.Errorf("the Kimi conversation snapshot has no complete turn page")
	}
	identities := make(map[string]struct{}, len(turns))
	var previous *int64
	for _, turn := range turns {
		if err := turn.validate(); err != nil {
			return err
		}
		if _, duplicate := identities[turn.TurnID]; duplicate || previous != nil && *previous >= *turn.Ordinal {
			return fmt.Errorf("the Kimi conversation snapshot repeats or misorders a turn")
		}
		identities[turn.TurnID] = struct{}{}
		previous = turn.Ordinal
	}
	return nil
}

// readConversationHistory starts from the exact WebSocket page. REST supplies
// only older ordinals, so later buffered deltas cannot duplicate page contents.
func (a *Agent) readConversationHistory(ctx context.Context, latest kimiConversationFrame, retain func(int64) error) ([]kimiConversationTurn, error) {
	if err := validateKimiConversationPage(latest.Turns, latest.HasMore); err != nil {
		return nil, err
	}
	if err := kimiCheckID("session", latest.SessionID); err != nil {
		return nil, err
	}
	if err := kimiCheckID("agent", latest.AgentID); err != nil {
		return nil, err
	}
	pages := [][]kimiConversationTurn{latest.Turns}
	seen := make(map[string]struct{}, len(latest.Turns))
	for _, turn := range latest.Turns {
		seen[turn.TurnID] = struct{}{}
	}
	more := *latest.HasMore
	count := len(latest.Turns)
	for more {
		oldestPage := pages[len(pages)-1]
		before := *oldestPage[0].Ordinal
		if before <= 0 {
			return nil, fmt.Errorf("the Kimi conversation page cannot advance to an older ordinal")
		}
		query := url.Values{"before_ordinal": {strconv.FormatInt(before, 10)}, "page_size": {"200"}}
		path := kimiSessionPath(latest.SessionID, "/agents/"+latest.AgentID+"/turns")
		var raw json.RawMessage
		if err := a.api.callQuery(ctx, "GET", path, query, nil, &raw); err != nil {
			return nil, err
		}
		if err := retain(int64(len(raw)) + kimiPendingFrameOverhead); err != nil {
			return nil, err
		}
		var page kimiConversationPage
		if err := json.Unmarshal(raw, &page); err != nil {
			return nil, fmt.Errorf("decode the Kimi conversation page: %w", err)
		}
		if err := validateKimiConversationPage(page.Turns, page.HasMore); err != nil {
			return nil, err
		}
		for _, turn := range page.Turns {
			if *turn.Ordinal >= before {
				return nil, fmt.Errorf("the Kimi conversation page overlaps its newer page")
			}
			if _, duplicate := seen[turn.TurnID]; duplicate {
				return nil, fmt.Errorf("the Kimi conversation history repeats a turn identity")
			}
			seen[turn.TurnID] = struct{}{}
		}
		more = *page.HasMore
		if len(page.Turns) == 0 {
			break
		}
		pages = append(pages, page.Turns)
		count += len(page.Turns)
	}
	turns := make([]kimiConversationTurn, 0, count)
	for index := len(pages) - 1; index >= 0; index-- {
		turns = append(turns, pages[index]...)
	}
	return turns, nil
}

// replaceConversationRecovery runs under dispatchMu and never publishes an
// intermediate ready state. The caller publishes after projection or admission.
func (a *Agent) replaceConversationRecovery(run *kimiRun, next *kimiConversationRecovery) {
	previous := run.conversationRecovery.Swap(next)
	if previous != nil {
		previous.cancel()
		a.pendingNativeBytes -= previous.bytes
		previous.frames, previous.page, previous.bytes = nil, nil, 0
	}
}

func (a *Agent) publishConversationRecoveryState(run *kimiRun) {
	if run.conversation != nil && run.conversationRecovery.Load() == nil {
		for id, publication := range run.conversation.turns {
			if turn := run.conversation.document.turns[id]; turn != nil && !turn.projectionPending {
				a.scheduleExactReply(run, turn, publication)
			}
		}
	}
	if run.agentID == kimiMainAgentID {
		a.PublishTurnActive()
		return
	}
	a.publishChildInputState(run)
	a.queueIdleStateCleanup(run)
}

func (a *Agent) awaitConversationSnapshot(run *kimiRun) *kimiConversationRecovery {
	ctx, cancel := context.WithCancel(a.Context())
	if run.conversation != nil {
		run.conversation.snapshotReady = false
	}
	recovery := &kimiConversationRecovery{ctx: ctx, cancel: cancel}
	a.replaceConversationRecovery(run, recovery)
	a.publishConversationRecoveryState(run)
	return recovery
}

func (a *Agent) holdStartupConversationSnapshot(sessionID string) {
	a.dispatchMu.Lock()
	defer a.finishDispatch()
	if !a.ownsNativeSession(sessionID) {
		return
	}
	run := a.run(kimiMainAgentID)
	output := a.conversationOutput(run, sessionID)
	if !output.snapshotReady && run.conversationRecovery.Load() == nil {
		a.awaitConversationSnapshot(run)
	}
}

func (a *Agent) startConversationRecovery(run *kimiRun, page kimiConversationFrame, size int64) {
	ctx, cancel := context.WithCancel(a.Context())
	recovery := &kimiConversationRecovery{ctx: ctx, cancel: cancel, page: &page}
	if run.conversation != nil {
		run.conversation.snapshotReady = false
	}
	a.replaceConversationRecovery(run, recovery)
	if size <= 0 {
		raw, _ := json.Marshal(page)
		size = int64(len(raw))
	}
	if !a.chargeConversationRecovery(recovery, size+kimiPendingFrameOverhead) {
		recovery.invalidated, recovery.page = true, nil
		cancel()
		a.requestConversationResubscription(run, recovery)
	}
	a.publishConversationRecoveryState(run)
	if recovery.invalidated || a.api == nil {
		return
	}
	key := fmt.Sprintf("conversation/%q/%p", run.agentID, run)
	a.transcriptRecoveryWork.Run(key, func() { a.recoverConversation(run) })
}

func (a *Agent) chargeConversationRecovery(recovery *kimiConversationRecovery, size int64) bool {
	if size < 0 || size > kimiPendingAgentBytes-recovery.bytes || size > kimiPendingSessionBytes-a.pendingNativeBytes {
		return false
	}
	recovery.bytes += size
	a.pendingNativeBytes += size
	return true
}

func (a *Agent) invalidateConversationRecovery(recovery *kimiConversationRecovery) {
	recovery.invalidated = true
	recovery.cancel()
	a.pendingNativeBytes -= recovery.bytes
	recovery.frames, recovery.page, recovery.bytes = nil, nil, 0
}

func (a *Agent) retainConversationRecoveryFrame(run *kimiRun, recovery *kimiConversationRecovery, frame kimiConversationFrame, size int64) {
	if recovery.invalidated {
		return
	}
	if size <= 0 {
		raw, _ := json.Marshal(frame)
		size = int64(len(raw))
	}
	if len(recovery.frames) >= kimiMaxPendingEvents || !a.chargeConversationRecovery(recovery, size+kimiPendingFrameOverhead) {
		a.invalidateConversationRecovery(recovery)
		a.requestConversationResubscription(run, recovery)
		return
	}
	recovery.frames = append(recovery.frames, frame)
}

func (a *Agent) requestConversationResubscription(run *kimiRun, recovery *kimiConversationRecovery) {
	if run.conversationRecovery.Load() != recovery || a.stream == nil {
		return
	}
	// Closing this owned socket makes the tracked reader establish a fresh
	// subscription boundary. A latest-page REST read cannot supply that boundary.
	a.stream.mu.Lock()
	connection := a.stream.conn
	a.stream.mu.Unlock()
	if connection != nil {
		_ = connection.CloseNow()
	}
}

func (a *Agent) conversationRecoveryCurrent(run *kimiRun, recovery *kimiConversationRecovery) bool {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	return !a.StoppedLocked() && a.Context().Err() == nil && a.runs[run.agentID] == run &&
		run.conversationRecovery.Load() == recovery && !recovery.invalidated && recovery.page != nil && a.sessionID == recovery.page.SessionID
}

func (a *Agent) recoverConversation(run *kimiRun) {
	backoff := time.Second
	for {
		a.dispatchMu.Lock()
		recovery := run.conversationRecovery.Load()
		if recovery == nil || !a.conversationRecoveryCurrent(run, recovery) {
			a.finishDispatch()
			return
		}
		page := *recovery.page
		a.finishDispatch()
		var attemptBytes int64
		turns, err := a.readConversationHistory(recovery.ctx, page, func(size int64) error {
			a.dispatchMu.Lock()
			defer a.finishDispatch()
			if !a.conversationRecoveryCurrent(run, recovery) {
				return errKimiConversationRecoveryReplaced
			}
			if !a.chargeConversationRecovery(recovery, size) {
				return fmt.Errorf("the Kimi conversation history exceeds its retention limit")
			}
			attemptBytes += size
			return nil
		})
		if err != nil {
			a.dispatchMu.Lock()
			if run.conversationRecovery.Load() == recovery && !recovery.invalidated {
				recovery.bytes -= attemptBytes
				a.pendingNativeBytes -= attemptBytes
			}
			a.finishDispatch()
			if recovery.ctx.Err() != nil || errors.Is(err, errKimiConversationRecoveryReplaced) {
				return
			}
			slog.Warn("read the older Kimi conversation pages", "agent_id", a.AgentID(), "error", err)
			timer := a.clock.NewTimer(backoff, kimiConversationRecoveryTimerTag)
			select {
			case <-recovery.ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			backoff = min(backoff*2, 30*time.Second)
			continue
		}
		a.dispatchMu.Lock()
		if !a.conversationRecoveryCurrent(run, recovery) {
			a.finishDispatch()
			return
		}
		complete := false
		page.Turns, page.HasMore = turns, &complete
		output := a.conversationOutput(run, page.SessionID)
		err = a.applyConversationFrame(run, output, page)
		if err == nil {
			for _, pending := range recovery.frames {
				if err = a.applyConversationFrame(run, output, pending); err != nil {
					break
				}
			}
		}
		if err != nil {
			a.invalidateConversationRecovery(recovery)
			a.requestConversationResubscription(run, recovery)
			slog.Warn("apply the recovered Kimi conversation", "agent_id", a.AgentID(), "error", err)
		} else {
			a.replaceConversationRecovery(run, nil)
			a.publishConversationRecoveryState(run)
		}
		a.finishDispatch()
		return
	}
}
