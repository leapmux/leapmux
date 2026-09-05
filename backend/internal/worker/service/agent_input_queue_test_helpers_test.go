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
		rows, err = svc.Queries.ListMessagesByAgentID(context.Background(), db.ListMessagesByAgentIDParams{
			AgentID: agentID, Seq: 0, Limit: int64(max(count, 1)),
		})
		return err == nil && len(rows) == count
	}, time.Second, 10*time.Millisecond)
	return rows
}
