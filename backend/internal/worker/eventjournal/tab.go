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
	return t.ID != "" && (t.Type == leapmuxv1.TabType_TAB_TYPE_AGENT || t.Type == leapmuxv1.TabType_TAB_TYPE_TERMINAL)
}

func (t Tab) live(ctx context.Context, q *db.Queries) (bool, error) {
	if t.Type == leapmuxv1.TabType_TAB_TYPE_AGENT {
		return q.CanWatchAgentHistory(ctx, t.ID)
	}
	return q.CanWatchTerminalHistory(ctx, t.ID)
}
