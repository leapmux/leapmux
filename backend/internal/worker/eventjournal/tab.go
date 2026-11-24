package eventjournal

import (
	"context"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// Tab identifies one source whose facts the Worker publishes.
type Tab struct {
	Type leapmuxv1.TabType
	ID   string
}

func (t Tab) Valid() bool {
	return t.ID != "" && SupportsTabType(t.Type)
}

// SupportsTabType reports which tab kinds have retained Worker events.
func SupportsTabType(tabType leapmuxv1.TabType) bool {
	return tabType == leapmuxv1.TabType_TAB_TYPE_AGENT || tabType == leapmuxv1.TabType_TAB_TYPE_TERMINAL
}

func semanticFactTabType(fact *leapmuxv1.SemanticTabFact) leapmuxv1.TabType {
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

func (t Tab) closed(ctx context.Context, q *db.Queries) (bool, error) {
	if t.Type == leapmuxv1.TabType_TAB_TYPE_AGENT {
		return q.IsClosedAgentHistoryResource(ctx, t.ID)
	}
	return q.IsClosedTerminalHistoryResource(ctx, t.ID)
}

func (t Tab) live(ctx context.Context, q *db.Queries) (bool, error) {
	if t.Type == leapmuxv1.TabType_TAB_TYPE_AGENT {
		return q.CanWatchAgentHistory(ctx, t.ID)
	}
	return q.CanWatchTerminalHistory(ctx, t.ID)
}
