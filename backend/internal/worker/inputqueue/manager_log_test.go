package inputqueue

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type queueFailureLogHandler struct {
	slog.Handler
	observe func(slog.Record)
}

func (handler queueFailureLogHandler) Handle(ctx context.Context, record slog.Record) error {
	handler.observe(record)
	return handler.Handler.Handle(ctx, record)
}

func TestManagerFailureLogsReleaseCoordinatorBeforeCallbacks(t *testing.T) {
	// slog.SetDefault changes process state, so this test runs without t.Parallel.
	for _, test := range []struct {
		name         string
		message      string
		trigger      string
		dispatchErr  error
		expectedLogs []string
	}{
		{
			name:    "preparation failure",
			message: "agent input queue prepare failed",
			trigger: `CREATE TRIGGER refuse_queue_preparation BEFORE INSERT ON agent_input_queue_state
WHEN NEW.agent_id = 'agent-1' BEGIN SELECT RAISE(ABORT, 'queue preparation refused'); END`,
			expectedLogs: []string{"queue preparation refused"},
		},
		{
			name:    "failure persistence failure",
			message: "agent input queue failure persistence failed",
			trigger: `CREATE TRIGGER refuse_queue_failure BEFORE UPDATE ON agent_input_queue_items
WHEN NEW.agent_id = 'agent-1' AND NEW.id = 'queued-log-input' AND NEW.error <> ''
BEGIN SELECT RAISE(ABORT, 'queue failure state refused'); END`,
			dispatchErr:  errors.New("native delivery refused"),
			expectedLogs: []string{"queue failure state refused"},
		},
		{
			name:    "acceptance persistence failure",
			message: "agent input queue acceptance persistence failed",
			trigger: fmt.Sprintf(`CREATE TRIGGER refuse_queue_transcript BEFORE INSERT ON messages
WHEN NEW.agent_id = 'agent-1' BEGIN SELECT RAISE(ABORT, 'queue transcript refused'); END;
CREATE TRIGGER refuse_queue_uncertainty BEFORE UPDATE ON agent_input_queue_items
WHEN NEW.agent_id = 'agent-1' AND NEW.id = 'queued-log-input' AND NEW.state = %d
AND NEW.error LIKE 'provider accepted input but transcript persistence failed: %%'
BEGIN SELECT RAISE(ABORT, 'queue uncertainty state refused'); END`,
				leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DELIVERY_UNCERTAIN),
			expectedLogs: []string{"queue transcript refused", "queue uncertainty state refused"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			database, store := newStoreFixture(t)
			ctx := t.Context()
			originalAttachments := []Attachment{{Filename: "original.txt", MimeType: "text/plain", Data: []byte("original attachment")}}
			initial, err := store.Enqueue(ctx, NewItem{
				ID: "queued-log-input", AgentID: "agent-1",
				Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "original queued input",
				Attachments: originalAttachments,
			})
			require.NoError(t, err)
			_, err = database.ExecContext(ctx, test.trigger)
			require.NoError(t, err)
			var afterAccepts int
			dispatcher := &afterAcceptDispatcher{afterAccept: func() { afterAccepts++ }}
			dispatcher.fail = test.dispatchErr
			observer := &recordingObserver{}
			manager := NewManager(store, dispatcher, observer)
			t.Cleanup(manager.StopAndWait)
			coordinator := manager.coordinator("agent-1")
			completed := make(chan struct{})
			var completeOnce sync.Once
			var logs, callbacks int
			var mutexFree bool
			var loggedError, callbackError error
			var callbackSnapshot Snapshot
			var loggedInputID string
			previousLogger := slog.Default()
			slog.SetDefault(slog.New(queueFailureLogHandler{
				Handler: slog.NewTextHandler(io.Discard, nil),
				observe: func(record slog.Record) {
					if record.Message != test.message || record.Level != slog.LevelError {
						return
					}
					var agentID, inputID string
					var recordError error
					record.Attrs(func(attribute slog.Attr) bool {
						switch attribute.Key {
						case "agent_id":
							agentID = attribute.Value.String()
						case "input_id":
							inputID = attribute.Value.String()
						case "error":
							recordError, _ = attribute.Value.Any().(error)
						}
						return true
					})
					if agentID != "agent-1" {
						return
					}
					defer completeOnce.Do(func() { close(completed) })
					logs++
					loggedInputID, loggedError = inputID, recordError
					mutexFree = coordinator.mu.TryLock()
					if !mutexFree {
						return
					}
					coordinator.mu.Unlock()
					callbacks++
					callbackSnapshot, callbackError = manager.SetPaused(ctx, "agent-1", true)
				},
			}))
			defer slog.SetDefault(previousLogger)
			manager.NotifyDependencyReady("agent-1")
			timer := time.NewTimer(eventuallyWait)
			defer timer.Stop()
			select {
			case <-completed:
			case <-timer.C:
				t.Fatal("the queue failure handler did not complete")
			}
			manager.StopAndWait()
			assert.Equal(t, 1, logs)
			assert.True(t, mutexFree, "the queue failure must release the coordinator mutex before its handler")
			assert.Equal(t, 1, callbacks, "the actual handler must call SetPaused synchronously")
			for _, expected := range test.expectedLogs {
				assert.ErrorContains(t, loggedError, expected)
			}
			snapshot, err := store.Snapshot(ctx, "agent-1")
			require.NoError(t, err)
			require.Len(t, snapshot.Items, 1)
			storedItem, attachments, found, err := getItem(ctx, database, "agent-1", "queued-log-input", true)
			require.NoError(t, err)
			require.True(t, found)
			assert.Equal(t, "original queued input", storedItem.Text)
			assert.Equal(t, originalAttachments, attachments, "the failures must retain every queued attachment byte")
			var messages, highWater int64
			require.NoError(t, database.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE agent_id = 'agent-1'`).Scan(&messages))
			require.NoError(t, database.QueryRowContext(ctx, `SELECT message_seq_hwm FROM agents WHERE id = 'agent-1'`).Scan(&highWater))
			assert.Zero(t, messages, "a refused transcript must create no message")
			assert.Empty(t, observer.accepted)
			assert.Zero(t, afterAccepts)
			if test.name == "preparation failure" {
				assert.Empty(t, loggedInputID)
				assert.ErrorContains(t, callbackError, "queue preparation refused")
				assert.Equal(t, initial, snapshot, "the refused preparation and pause must retain every stored queue fact")
				assert.Empty(t, dispatcher.dispatches())
				assert.Empty(t, observer.snapshots)
				assert.Zero(t, highWater)
				return
			}
			assert.Equal(t, "queued-log-input", loggedInputID)
			assert.NoError(t, callbackError)
			assert.Equal(t, []string{"queued-log-input"}, dispatcher.dispatches())
			assert.Equal(t, int64(1), highWater, "only the original preparation reserves a sequence")
			assert.True(t, snapshot.Paused)
			assert.Equal(t, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_MANUAL, snapshot.PauseReason)
			assert.True(t, snapshot.ActiveTurn)
			assert.Equal(t, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DISPATCHING, snapshot.Items[0].State)
			assert.Equal(t, int64(1), snapshot.Items[0].ReservedSeq)
			assert.Empty(t, snapshot.Items[0].Error, "the failed storage mutation must not invent a delivered outcome")
			if assert.Len(t, observer.snapshots, 2) {
				prepared := observer.snapshots[0]
				require.Len(t, prepared.Items, 1)
				assert.False(t, prepared.Paused)
				assert.True(t, prepared.ActiveTurn)
				assert.Equal(t, prepared.Items, snapshot.Items, "the callback must retain the exact prepared item")
				assert.Equal(t, prepared.Revision+1, snapshot.Revision)
				assert.Equal(t, callbackSnapshot, snapshot)
				assert.Equal(t, snapshot, observer.snapshots[1], "the FIFO must publish the callback's committed pause after preparation")
			}
		})
	}
}
