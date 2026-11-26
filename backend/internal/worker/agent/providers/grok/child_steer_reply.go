package grok

import (
	"encoding/json"
	"slices"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/sessionstore"
)

type grokChildReplyState struct {
	rowKey string
	live   grokLiveModel
	active *grokSteerReply
	sink   agent.ProviderServices
}

type grokChildSteerSubmission struct {
	state *grokChildReplyState
	turn  *grokSteerReply
	token string
}

// The caller holds replies.mu. An old prompt cannot own a live child control.
func (replies *grokSteerReplies) activeChildTurn(sessionID string) *grokChildReplyState {
	child := replies.children[sessionID]
	if child == nil || child.active == nil || child.active.promptID != child.live.promptID {
		return nil
	}
	return child
}

// The caller holds the reply state mutex. The session dispatcher already
// selected the registered child row before it offers a model update.
func (replies *grokSteerReplies) childState(sessionID, rowKey string) *grokChildReplyState {
	state := replies.children[sessionID]
	if state == nil {
		if replies.children == nil {
			replies.children = make(map[string]*grokChildReplyState)
		}
		state = &grokChildReplyState{rowKey: rowKey}
		replies.children[sessionID] = state
	}
	return state
}

func (a *Agent) observeGrokChildPrompt(sessionID, rowKey, promptID string) {
	replies := &a.steerReplies
	replies.mu.Lock()
	defer replies.mu.Unlock()
	if !replies.closed {
		child := replies.childState(sessionID, rowKey)
		if child.rowKey == rowKey && child.live.promptID != promptID {
			child.live = grokLiveModel{promptID: promptID}
		}
	}
}

func (a *Agent) observeGrokChildUpdate(sessionID, rowKey, updateType string, metadata map[string]json.RawMessage) string {
	model, known := readGrokReplyModel(sessionID, updateType, metadata)
	replies := &a.steerReplies
	replies.mu.Lock()
	defer replies.mu.Unlock()
	if replies.closed {
		return ""
	}
	state := replies.children[sessionID]
	if state != nil && state.rowKey != rowKey {
		return ""
	}
	if !known {
		if state != nil && state.active != nil && updateType == contracts.ACPUpdateAgentMessageChunk {
			state.active.invalid = true
		}
		return ""
	}
	if state == nil {
		state = replies.childState(sessionID, rowKey)
	}
	if state.live.promptID != "" && state.live.promptID != model.promptID {
		return ""
	}
	state.live = model
	current := state.active
	if current == nil || current.promptID != model.promptID {
		return ""
	}
	current.recordModel(model)
	if updateType == contracts.ACPUpdateAgentMessageChunk {
		return grokStreamMessageID(sessionID, model.promptID, model.start)
	}
	return ""
}

func (a *Agent) prepareGrokChildJournal(childSession string) (string, sessionstore.JournalCursor) {
	if childSession == "" {
		return "", sessionstore.JournalCursor{}
	}
	replies := &a.steerReplies
	replies.mu.Lock()
	state := replies.children[childSession]
	skip := replies.closed || state == nil || state.live.promptID == "" || state.active != nil
	replies.mu.Unlock()
	if skip {
		return "", sessionstore.JournalCursor{}
	}
	return replies.findJournal(childSession)
}

func (a *Agent) admitGrokChildSteerWithJournal(child linkedSubagent, sink agent.ProviderServices, text, group string, baseline sessionstore.JournalCursor) grokChildSteerSubmission {
	replies := &a.steerReplies
	replies.mu.Lock()
	state := replies.children[child.childSession]
	if replies.closed || state == nil || state.rowKey != child.rowKey || state.live.promptID == "" {
		replies.mu.Unlock()
		return grokChildSteerSubmission{}
	}
	promptID := state.live.promptID
	if current := state.active; current != nil {
		defer replies.mu.Unlock()
		if current.promptID != promptID {
			return grokChildSteerSubmission{}
		}
		return addGrokChildSubmission(state, current, text)
	}
	if group != "" {
		current := &grokSteerReply{
			sessionID: child.childSession, promptID: promptID, turnID: promptID, group: group,
			childMessages: true, baseline: baseline,
		}
		state.sink, state.active = sink, current
		defer replies.mu.Unlock()
		return addGrokChildSubmission(state, current, text)
	}
	replies.mu.Unlock()

	group, baseline = replies.findJournal(child.childSession)
	if group == "" {
		return grokChildSteerSubmission{}
	}
	replies.mu.Lock()
	defer replies.mu.Unlock()
	if replies.closed || replies.children[child.childSession] != state || state.live.promptID != promptID {
		return grokChildSteerSubmission{}
	}
	if current := state.active; current != nil {
		if current.promptID != promptID {
			return grokChildSteerSubmission{}
		}
		return addGrokChildSubmission(state, current, text)
	}
	current := &grokSteerReply{
		sessionID: child.childSession, promptID: promptID, turnID: promptID, group: group,
		childMessages: true, baseline: baseline,
	}
	state.sink, state.active = sink, current
	return addGrokChildSubmission(state, current, text)
}

func addGrokChildSubmission(state *grokChildReplyState, current *grokSteerReply, text string) grokChildSteerSubmission {
	token := id.Generate()
	current.inputs = append(current.inputs, grokSteerAdmission{
		id: token, payload: grokInterjectionPayload{text: text}, afterStream: state.live.start,
	})
	return grokChildSteerSubmission{state: state, turn: current, token: token}
}

func (a *Agent) settleGrokChildSubmission(submission grokChildSteerSubmission, nativeID string) bool {
	if submission.state == nil {
		return false
	}
	a.steerReplies.mu.Lock()
	defer a.steerReplies.mu.Unlock()
	if a.steerReplies.children[submission.turn.sessionID] != submission.state {
		return false
	}
	current := submission.state.active
	if current != submission.turn {
		return false
	}
	for index, input := range current.inputs {
		if input.id != submission.token {
			continue
		}
		if nativeID == "" {
			current.inputs = slices.Delete(current.inputs, index, index+1)
			if len(current.inputs) == 0 {
				submission.state.active = nil
			}
			return false
		}
		for _, accepted := range current.inputs {
			if accepted.echoed && accepted.id == nativeID {
				current.invalid = true
			}
		}
		current.inputs[index].id = nativeID
		current.inputs[index].echoed = true
		return true
	}
	return false
}

func (a *Agent) finishGrokChildSteers(sessionID, promptID string, outcome leapmuxv1.AgentInputTurnOutcome) {
	replies := &a.steerReplies
	replies.mu.Lock()
	state := replies.children[sessionID]
	if replies.closed || state == nil || state.active == nil || state.active.promptID != promptID {
		replies.mu.Unlock()
		return
	}
	proof := detachGrokChildProof(state)
	sink := state.sink
	replies.jobs.Add(1)
	replies.mu.Unlock()
	go func() {
		defer replies.jobs.Done()
		a.publishGrokSteerRepliesTo(sink, proof, outcome)
	}()
}

func detachGrokChildProof(state *grokChildReplyState) grokSteerReply {
	if state.active.cancelControlProbe != nil {
		state.active.cancelControlProbe()
	}
	proof := state.active.snapshot()
	// A request with no receipt has no accepted transcript row.
	proof.inputs = slices.DeleteFunc(proof.inputs, func(input grokSteerAdmission) bool { return !input.echoed })
	state.active = nil
	return proof
}

func (a *Agent) abandonGrokChildSteers(sessionID string) {
	replies := &a.steerReplies
	replies.mu.Lock()
	state := replies.children[sessionID]
	delete(replies.children, sessionID)
	var proof grokSteerReply
	if state != nil && state.active != nil {
		proof = detachGrokChildProof(state)
	}
	replies.mu.Unlock()
	if len(proof.inputs) != 0 {
		a.publishGrokSteerRepliesTo(state.sink, proof, leapmuxv1.AgentInputTurnOutcome_AGENT_INPUT_TURN_OUTCOME_UNSPECIFIED)
	}
}
