package service

import (
	"context"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

var testAgentInputSequence atomic.Uint64

func newTestAgentInputID() string {
	return "test-input-" + strconv.FormatUint(testAgentInputSequence.Add(1), 10)
}

func waitForMessageCount(t *testing.T, svc *Service, agentID string, count int) []db.Message {
	t.Helper()
	var rows []db.Message
	require.Eventually(t, func() bool {
		var err error
		// Over-fetch by one. A LIMIT of exactly `count` truncates the result,
		// so the equality could never fail upward: a queue that wrote the
		// transcript TWICE would still satisfy every caller of this helper.
		rows, err = svc.Queries.ListMessagesByAgentID(context.Background(), db.ListMessagesByAgentIDParams{
			AgentID: agentID, Seq: 0, Limit: int64(count + 1),
		})
		return err == nil && len(rows) == count
	}, time.Second, 10*time.Millisecond)
	return rows
}
