package service

import (
	"io"
	"log/slog"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMessageTodoFailureLogsReleaseTheMutationBeforeCallbacks(t *testing.T) {
	for _, operation := range []string{"message", "divider"} {
		t.Run(operation, func(t *testing.T) {
			const agentID = "message-log-owner"
			providerID := leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE
			provider := mutationLogTodoProvider{Provider: testRegistry.Plugin(providerID)}
			svc, _, _ := setupTestService(t, withRegistry(registryWithPlugin(t, providerID, provider)))
			require.NoError(t, svc.Queries.CreateAgent(t.Context(), db.CreateAgentParams{ID: agentID, WorkingDir: t.TempDir(), HomeDir: t.TempDir(), AgentProvider: providerID}))
			services := svc.Output.NewSink(agentID, providerID)
			services.UpdateSessionID("original-native-session")
			sink := requireRootOutputSink(t, svc.Output, agentID)
			_, err := svc.DB.ExecContext(t.Context(), `CREATE TRIGGER refuse_message_todo BEFORE INSERT ON agent_todos
WHEN NEW.agent_id = 'message-log-owner' BEGIN SELECT RAISE(ABORT, 'to-do insert refused'); END`)
			require.NoError(t, err)
			original := []byte(`{"create_task":true,"native":"original bytes"}`)
			span := agent.SpanInfo{SpanID: "original-call", Closing: true}
			receipt := agent.NewTranscriptWriteReceipt()
			content := services.CaptureMessage(agent.MessageContent{Original: original, IdempotencyKey: "original-key", WriteReceipt: receipt}, span)
			writer := &testResponseWriter{channelID: "message-log-wire"}
			registerAgentWatch(svc, writer.channelID, agentID, leapmuxv1.WatchMode_WATCH_MODE_FULL, writer)
			svc.Output.WaitActivityRefreshes()
			mutation := svc.Output.transcriptMutationMutex(agentID)
			var logs int
			var mutexFree bool
			var replacement *agentOutputSink
			previousLogger := slog.Default()
			slog.SetDefault(slog.New(controlFailureLogObserver{Handler: slog.NewTextHandler(io.Discard, nil), observe: func(record slog.Record) {
				if record.Message != "apply todo event" || record.Level != slog.LevelWarn {
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
				sequence, committed := receipt.StoredMessageSequence()
				assert.True(t, committed, "the accepted row must fix its sequence before the failure log")
				assert.Positive(t, sequence)
				mutexFree = mutation.TryLock()
				if !mutexFree {
					return
				}
				mutation.Unlock()
				svc.Output.NewSink(agentID, providerID)
				replacement = requireRootOutputSink(t, svc.Output, agentID)
			}}))
			defer slog.SetDefault(previousLogger)
			if operation == "divider" {
				err = services.PersistTurnEnd(content, span)
			} else {
				err = services.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content, span)
			}
			require.NoError(t, err, "a to-do failure must retain the accepted transcript write")
			assert.Equal(t, 1, logs)
			assert.True(t, mutexFree, "the transcript warning must release every admission mutex before its handler")
			sequence, committed := receipt.StoredMessageSequence()
			require.True(t, committed)
			row, err := svc.Queries.GetMessageByAgentIDAndSeq(t.Context(), db.GetMessageByAgentIDAndSeqParams{AgentID: agentID, Seq: sequence})
			require.NoError(t, err)
			stored, err := msgcodec.Decompress(row.Content, row.ContentCompression)
			require.NoError(t, err)
			assert.Equal(t, original, stored)
			assert.Equal(t, content.AgentSessionID, row.AgentSessionID)
			assert.Equal(t, content.IdempotencyKey, row.IdempotencyKey)
			var count int
			require.NoError(t, svc.DB.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM messages WHERE agent_id = ?", agentID).Scan(&count))
			assert.Equal(t, 1, count)
			todos, err := svc.Queries.ListAgentTodosNewestFirst(t.Context(), db.ListAgentTodosNewestFirstParams{AgentID: agentID, Limit: 100})
			require.NoError(t, err)
			assert.Empty(t, todos)
			var messages, completions int
			var order []string
			for _, event := range decodeAgentEvents(writer) {
				if message := event.GetAgentMessage(); message != nil && message.Id == row.ID {
					messages++
					order = append(order, "message")
					assert.Equal(t, sequence, message.Seq)
				}
				if event.GetTurnEnd() != nil {
					completions++
					order = append(order, "completion")
				}
			}
			assert.Equal(t, 1, messages)
			if operation == "divider" {
				assert.Equal(t, 1, completions)
				assert.Equal(t, []string{"message", "completion"}, order)
			} else {
				assert.Zero(t, completions)
			}
			if assert.NotNil(t, replacement, "the actual logger callback must replace the root synchronously") {
				assert.NotSame(t, sink, replacement)
				assert.Equal(t, "original-native-session", replacement.currentMessageSessionID())
			}
		})
	}
}
