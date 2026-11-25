package eventjournal

import (
	"context"
	"fmt"
	"slices"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

var supportedTabTypes = [...]leapmuxv1.TabType{
	leapmuxv1.TabType_TAB_TYPE_AGENT,
	leapmuxv1.TabType_TAB_TYPE_TERMINAL,
}

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
	return slices.Contains(supportedTabTypes[:], tabType)
}

func (t Tab) closed(ctx context.Context, q *db.Queries) (bool, error) {
	switch t.Type {
	case leapmuxv1.TabType_TAB_TYPE_AGENT:
		return q.IsClosedAgentHistoryResource(ctx, t.ID)
	case leapmuxv1.TabType_TAB_TYPE_TERMINAL:
		return q.IsClosedTerminalHistoryResource(ctx, t.ID)
	default:
		return false, fmt.Errorf("unsupported semantic event tab type %v", t.Type)
	}
}

func (t Tab) live(ctx context.Context, q *db.Queries) (bool, error) {
	switch t.Type {
	case leapmuxv1.TabType_TAB_TYPE_AGENT:
		return q.CanWatchAgentHistory(ctx, t.ID)
	case leapmuxv1.TabType_TAB_TYPE_TERMINAL:
		return q.CanWatchTerminalHistory(ctx, t.ID)
	default:
		return false, fmt.Errorf("unsupported semantic event tab type %v", t.Type)
	}
}
