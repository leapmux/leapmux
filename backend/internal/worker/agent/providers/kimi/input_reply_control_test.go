package kimi

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/leapmux/leapmux/generated/contracts"
)

func TestExactControlInputsRequiresAProvenPositionAfterConsumption(t *testing.T) {
	t.Parallel()
	const sessionID, turnID = "session", "turn"
	stepOne, stepTwo, stepThree := int64(1), int64(2), int64(3)
	item := func(kind string, step *int64) *kimiConversationItemState {
		return &kimiConversationItemState{identity: kimiConversationItemIdentity{kind: kind, stepIndex: step}}
	}
	document := newKimiConversationState(sessionID, kimiMainAgentID)
	turn := &kimiConversationTurnState{
		header: kimiConversationTurnHeader{TurnID: turnID, Kind: contracts.KimiConversationTurnKindAgent, Status: contracts.KimiConversationStatusRunning},
		items: map[string]*kimiConversationItemState{
			"opener":       item(contracts.KimiConversationItemKindUserMessage, nil),
			"later-input":  item(contracts.KimiConversationItemKindUserMessage, &stepTwo),
			"earlier-tool": item(contracts.KimiConversationItemKindToolCall, &stepOne),
			"later-tool":   item(contracts.KimiConversationItemKindToolCall, &stepThree),
			"unknown-step": item(contracts.KimiConversationItemKindToolCall, nil),
		},
	}
	document.turns[turnID] = turn
	output := &kimiConversationOutput{document: document, turns: map[string]*kimiConversationTurnOutput{
		turnID: {replyInputs: map[string]*kimiPromptSubmission{
			"a": {id: "a", sessionID: sessionID, receiver: kimiMainAgentID, turnID: turnID, itemID: "opener", consumingOpener: true},
			"b": {id: "b", sessionID: sessionID, receiver: kimiMainAgentID, turnID: turnID, itemID: "later-input"},
		}},
	}}
	control := &kimiPendingControl{sessionID: sessionID, agentID: kimiMainAgentID}
	assert.Empty(t, exactControlInputs(output, control))
	control.proofToolCallID = "earlier-tool"
	assert.Equal(t, []string{"a"}, exactControlInputs(output, control))
	control.proofToolCallID = "later-tool"
	assert.Equal(t, []string{"a", "b"}, exactControlInputs(output, control))
	control.proofToolCallID = "unknown-step"
	assert.Equal(t, []string{"a"}, exactControlInputs(output, control))
	control.proofToolCallID = "later-tool"
	control.agentID = "another-agent"
	assert.Empty(t, exactControlInputs(output, control))
	control.agentID = kimiMainAgentID
	control.toolIdentityConflict = true
	assert.Empty(t, exactControlInputs(output, control))
	control.toolIdentityConflict = false
	document.turns["other-turn"] = &kimiConversationTurnState{items: map[string]*kimiConversationItemState{
		"later-tool": item(contracts.KimiConversationItemKindToolCall, &stepThree),
	}}
	assert.Empty(t, exactControlInputs(output, control))
	delete(document.turns, "other-turn")
	turn.header.Status = contracts.KimiConversationStatusEnded
	assert.Empty(t, exactControlInputs(output, control))
}
