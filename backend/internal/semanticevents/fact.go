package semanticevents

import leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"

// FactTabType reports which tab kind owns fact's event variant.
func FactTabType(fact *leapmuxv1.SemanticTabFact) leapmuxv1.TabType {
	switch fact.GetEvent().(type) {
	case *leapmuxv1.SemanticTabFact_AgentActivity,
		*leapmuxv1.SemanticTabFact_AgentStatus,
		*leapmuxv1.SemanticTabFact_AgentTurnEnd,
		*leapmuxv1.SemanticTabFact_AgentRequest,
		*leapmuxv1.SemanticTabFact_AgentRequestEnded,
		*leapmuxv1.SemanticTabFact_AgentTodos,
		*leapmuxv1.SemanticTabFact_AgentBackgroundTasks:
		return leapmuxv1.TabType_TAB_TYPE_AGENT
	case *leapmuxv1.SemanticTabFact_TerminalStatus,
		*leapmuxv1.SemanticTabFact_TerminalExit,
		*leapmuxv1.SemanticTabFact_TerminalBell,
		*leapmuxv1.SemanticTabFact_TerminalNotification,
		*leapmuxv1.SemanticTabFact_TerminalTitle,
		*leapmuxv1.SemanticTabFact_TerminalProgress:
		return leapmuxv1.TabType_TAB_TYPE_TERMINAL
	default:
		return leapmuxv1.TabType_TAB_TYPE_UNSPECIFIED
	}
}
