package service

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type failedSessionComparisonStore struct {
	db.DBTX
	agentID string
	failed  atomic.Bool
}

func (store *failedSessionComparisonStore) QueryRowContext(ctx context.Context, query string, arguments ...any) *sql.Row {
	if strings.HasPrefix(query, "-- name: GetAgentByID :one") && len(arguments) == 1 && arguments[0] == store.agentID && store.failed.CompareAndSwap(false, true) {
		return store.DBTX.QueryRowContext(ctx, "SELECT missing_session_comparison_column")
	}
	return store.DBTX.QueryRowContext(ctx, query, arguments...)
}

func TestSessionUpdateLogsReleaseTheMutationBeforeCallbacks(t *testing.T) {
	for _, boundary := range []string{"comparison read", "session update", "successful update"} {
		t.Run(boundary, func(t *testing.T) {
			const agentID = "session-log-owner"
			svc, services := setupRootSink(t, agentID)
			sink := requireRootOutputSink(t, svc.Output, agentID)
			before, err := svc.Queries.GetAgentByID(t.Context(), agentID)
			require.NoError(t, err)
			originalFact := sink.currentMessageSessionFact()
			message := "agent session ID updated"
			level := slog.LevelInfo
			switch boundary {
			case "comparison read":
				message = "failed to fetch agent for session ID comparison"
				level = slog.LevelError
				svc.Output.queries = db.New(&failedSessionComparisonStore{DBTX: svc.DB, agentID: agentID})
			case "session update":
				message = "failed to store agent session ID"
				level = slog.LevelError
				_, err := svc.DB.ExecContext(t.Context(), `CREATE TRIGGER refuse_logged_session BEFORE UPDATE OF agent_session_id ON agents
WHEN NEW.id = 'session-log-owner' BEGIN SELECT RAISE(ABORT, 'session update refused'); END`)
				require.NoError(t, err)
			}
			svc.Output.WaitActivityRefreshes()
			mutation := svc.Output.transcriptMutationMutex(agentID)
			var logs int
			var mutexFree bool
			var replacement *agentOutputSink
			previousLogger := slog.Default()
			slog.SetDefault(slog.New(controlFailureLogObserver{Handler: slog.NewTextHandler(io.Discard, nil), observe: func(record slog.Record) {
				if record.Message != message || record.Level != level {
					return
				}
				belongs := false
				record.Attrs(func(attribute slog.Attr) bool {
					belongs = belongs || attribute.Key == "agent_id" && attribute.Value.String() == agentID
					return true
				})
				if !belongs {
					return
				}
				logs++
				mutexFree = mutation.TryLock()
				if !mutexFree {
					return
				}
				mutation.Unlock()
				svc.Output.NewSink(agentID, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
				replacement = requireRootOutputSink(t, svc.Output, agentID)
			}}))
			defer slog.SetDefault(previousLogger)
			services.UpdateSessionID("requested-native-session")
			assert.Equal(t, 1, logs)
			assert.True(t, mutexFree, "the session log must release the root mutation before its handler")
			fact := sink.currentMessageSessionFact()
			assert.NotSame(t, originalFact, fact)
			assert.Equal(t, "requested-native-session", fact.id, "the original fact replacement precedes a failed storage operation")
			after, err := svc.Queries.GetAgentByID(t.Context(), agentID)
			require.NoError(t, err)
			expected := before
			if boundary == "successful update" {
				expected.AgentSessionID = "requested-native-session"
			}
			assert.Equal(t, expected, after)
			if assert.NotNil(t, replacement, "the actual logger callback must replace the root synchronously") {
				assert.NotSame(t, sink, replacement)
				assert.Equal(t, after.AgentSessionID, replacement.currentMessageSessionID())
				assert.Same(t, replacement, svc.Output.sinkForAgent(agentID))
			}
		})
	}
}
