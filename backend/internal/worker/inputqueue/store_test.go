package inputqueue

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	workerdb "github.com/leapmux/leapmux/internal/worker/db"
	gendb "github.com/leapmux/leapmux/internal/worker/generated/db"
)

func newStoreFixture(t *testing.T) (*sql.DB, *Store) {
	t.Helper()
	database, err := workerdb.Open(filepath.Join(t.TempDir(), "queue.sqlite"), sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = database.Close() })
	require.NoError(t, workerdb.Migrate(context.Background(), database))
	require.NoError(t, gendb.New(database).CreateAgent(context.Background(), gendb.CreateAgentParams{
		ID: "agent-1", WorkingDir: t.TempDir(), HomeDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))
	return database, NewStore(database)
}

func TestInitialSchemaCreatesDurableInputQueue(t *testing.T) {
	t.Parallel()

	database, _ := newStoreFixture(t)
	ctx := context.Background()
	for _, object := range []string{
		"agent_input_queue_state",
		"agent_input_queue_items",
		"agent_input_queue_attachments",
		"idx_agent_input_queue_one_edit",
	} {
		var count int
		require.NoError(t, database.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM sqlite_master WHERE name = ?`, object).Scan(&count))
		assert.Equal(t, 1, count, object)
	}
	var deliveryErrorColumns, legacyInputFingerprintColumns, legacyDedupKeyColumns, idempotencyKeyColumns int
	require.NoError(t, database.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('messages') WHERE name = 'delivery_error'`).Scan(&deliveryErrorColumns))
	require.NoError(t, database.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('messages') WHERE name = 'input_fingerprint'`).Scan(&legacyInputFingerprintColumns))
	require.NoError(t, database.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('messages') WHERE name = 'dedup_key'`).Scan(&legacyDedupKeyColumns))
	require.NoError(t, database.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('messages') WHERE name = 'idempotency_key'`).Scan(&idempotencyKeyColumns))
	assert.Zero(t, deliveryErrorColumns)
	assert.Zero(t, legacyInputFingerprintColumns)
	assert.Zero(t, legacyDedupKeyColumns)
	assert.Equal(t, 1, idempotencyKeyColumns)
	var idempotencyIndexSQL string
	require.NoError(t, database.QueryRowContext(ctx,
		`SELECT sql FROM sqlite_master WHERE name = 'idx_messages_idempotency_key'`).Scan(&idempotencyIndexSQL))
	assert.Contains(t, idempotencyIndexSQL,
		"ON messages(agent_id, agent_session_id, idempotency_key) WHERE idempotency_key <> ''")
	var editIndexSQL string
	require.NoError(t, database.QueryRowContext(ctx,
		`SELECT sql FROM sqlite_master WHERE name = 'idx_agent_input_queue_one_edit'`).Scan(&editIndexSQL))
	assert.Contains(t, editIndexSQL, "WHERE edit_owner <> ''")
	var pauseOwnerColumns, archiveMarkerColumns int
	require.NoError(t, database.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('agent_input_queue_state') WHERE name = 'pause_owner'`).Scan(&pauseOwnerColumns))
	require.NoError(t, database.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('agent_input_queue_state') WHERE name = 'paused_for_archive'`).Scan(&archiveMarkerColumns))
	assert.Equal(t, 1, pauseOwnerColumns)
	assert.Zero(t, archiveMarkerColumns, "pause_owner replaced the single-cause archive flag")
	// The item id is the primary key, so a composite unique over (agent_id, id)
	// can never reject a row that the primary key admits.
	var itemsTableSQL string
	require.NoError(t, database.QueryRowContext(ctx,
		`SELECT sql FROM sqlite_master WHERE name = 'agent_input_queue_items'`).Scan(&itemsTableSQL))
	assert.NotContains(t, itemsTableSQL, "UNIQUE(agent_id, id)")
}

func TestStoreEnqueueRoundTripAndIdempotency(t *testing.T) {
	t.Parallel()

	_, store := newStoreFixture(t)
	ctx := context.Background()
	input := NewItem{
		ID: "input-1", AgentID: "agent-1",
		Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
		Text: "hello", Attachments: []Attachment{{Filename: "note.txt", MimeType: "text/plain", Data: []byte("body")}},
	}
	first, err := store.Enqueue(ctx, input)
	require.NoError(t, err)
	require.Len(t, first.Items, 1)
	assert.Equal(t, uint64(1), first.Revision)
	assert.Equal(t, int64(4), first.Items[0].Metadata[0].Size)

	again, err := store.Enqueue(ctx, input)
	require.NoError(t, err)
	assert.Equal(t, first.Revision, again.Revision)
	_, err = store.Enqueue(ctx, NewItem{ID: input.ID, AgentID: input.AgentID, Kind: input.Kind, Text: "different"})
	assert.ErrorIs(t, err, ErrConflict)
}

// TestStoreEnqueueReportsWhetherItAddedTheItem pins the answer of the store
// itself: true only for the call that inserts the row, and false for a repeat,
// a conflict, and a mutation that fails, which rolls the insert back.
func TestStoreEnqueueReportsWhetherItAddedTheItem(t *testing.T) {
	t.Parallel()

	_, store := newStoreFixture(t)
	ctx := context.Background()
	input := NewItem{ID: "input-1", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "hello"}

	snapshot, added, err := store.enqueue(ctx, input, nil)
	require.NoError(t, err)
	assert.True(t, added)
	require.Len(t, snapshot.Items, 1)

	snapshot, added, err = store.enqueue(ctx, input, nil)
	require.NoError(t, err)
	assert.False(t, added, "a repeat of a queued item adds nothing")
	assert.Len(t, snapshot.Items, 1)

	conflicting := input
	conflicting.Text = "different"
	_, added, err = store.enqueue(ctx, conflicting, nil)
	require.ErrorIs(t, err, ErrConflict)
	assert.False(t, added)

	mutationFailed := errors.New("the mutation failed")
	failing := NewItem{ID: "input-2", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "second"}
	_, added, err = store.enqueue(ctx, failing, func(*sql.Tx) error { return mutationFailed })
	require.ErrorIs(t, err, mutationFailed)
	assert.False(t, added, "a failed mutation rolls the insert back")
	snapshot, err = store.Snapshot(ctx, "agent-1")
	require.NoError(t, err)
	require.Len(t, snapshot.Items, 1)
	assert.Equal(t, "input-1", snapshot.Items[0].ID)

	_, added, err = store.enqueue(ctx, failing, nil)
	require.NoError(t, err)
	assert.True(t, added, "the item that the failed mutation rolled back is new")
}

func TestControlFeedbackPrecedesFutureInput(t *testing.T) {
	t.Parallel()
	for _, active := range []bool{false, true} {
		t.Run(fmt.Sprint(active), func(t *testing.T) {
			t.Parallel()
			_, store := newStoreFixture(t)
			ctx := t.Context()
			if active {
				_, err := store.Enqueue(ctx, NewItem{ID: "active", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "active request"})
				require.NoError(t, err)
				prepared, _, err := store.PrepareDispatch(ctx, "agent-1")
				require.NoError(t, err)
				require.NotNil(t, prepared)
			}
			_, err := store.Enqueue(ctx, NewItem{ID: "future", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "future task"})
			require.NoError(t, err)
			for _, id := range []string{"feedback-1", "feedback-2"} {
				_, err = store.Enqueue(ctx, NewItem{ID: id, AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_CONTROL_FEEDBACK, Text: "feedback"})
				require.NoError(t, err)
			}
			snapshot, err := store.Snapshot(ctx, "agent-1")
			require.NoError(t, err)
			var ids []string
			for _, item := range snapshot.Items {
				ids = append(ids, item.ID)
			}
			expected := []string{"feedback-1", "feedback-2", "future"}
			if active {
				expected = append([]string{"active"}, expected...)
				assert.Equal(t, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DISPATCHING, snapshot.Items[0].State)
			}
			assert.Equal(t, expected, ids)
		})
	}
}

func TestStoreTruncatesSnapshotTextButEditReturnsFullText(t *testing.T) {
	t.Parallel()

	_, store := newStoreFixture(t)
	ctx := context.Background()
	fullText := strings.Repeat("한", 5000)
	snapshot, err := store.Enqueue(ctx, NewItem{
		ID: "input-1", AgentID: "agent-1", Text: fullText,
		Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
	})
	require.NoError(t, err)
	require.Len(t, snapshot.Items, 1)
	assert.Less(t, len(snapshot.Items[0].Text), len(fullText))

	_, editText, _, err := store.BeginEdit(ctx, "agent-1", "input-1", "client", false)
	require.NoError(t, err)
	assert.Equal(t, fullText, editText)
}

func TestStoreEnqueueRetryRemainsIdempotentAfterAcceptance(t *testing.T) {
	t.Parallel()

	database, store := newStoreFixture(t)
	ctx := context.Background()
	input := NewItem{
		ID: "input-1", AgentID: "agent-1",
		Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
		Text: "hello", Attachments: []Attachment{{Filename: "note.txt", MimeType: "text/plain", Data: []byte("body")}},
	}
	_, err := store.Enqueue(ctx, input)
	require.NoError(t, err)
	prepared, _, err := store.PrepareDispatch(ctx, "agent-1")
	require.NoError(t, err)
	_, _, err = store.Accept(ctx, *prepared, DispatchResult{StartsTurn: true, TurnSteerable: true})
	require.NoError(t, err)
	var idempotencyKey string
	require.NoError(t, database.QueryRowContext(ctx,
		`SELECT idempotency_key FROM messages WHERE id = ?`, input.ID).Scan(&idempotencyKey))
	assert.Regexp(t, `^input:[0-9a-f]{64}$`, idempotencyKey)

	snapshot, err := store.Enqueue(ctx, input)
	require.NoError(t, err)
	assert.Empty(t, snapshot.Items)
	conflicting := input
	conflicting.Attachments = []Attachment{{Filename: "note.txt", MimeType: "text/plain", Data: []byte("different")}}
	_, err = store.Enqueue(ctx, conflicting)
	assert.ErrorIs(t, err, ErrConflict)
}

func TestStoreAcceptKeepsIdenticalContentFromDistinctInputs(t *testing.T) {
	t.Parallel()

	database, store := newStoreFixture(t)
	ctx := context.Background()
	for _, inputID := range []string{"input-1", "input-2"} {
		_, err := store.Enqueue(ctx, NewItem{
			ID: inputID, AgentID: "agent-1",
			Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
			Text: "same content",
		})
		require.NoError(t, err)
		prepared, _, err := store.PrepareDispatch(ctx, "agent-1")
		require.NoError(t, err)
		require.NotNil(t, prepared)
		_, _, err = store.Accept(ctx, *prepared, DispatchResult{})
		require.NoError(t, err)
	}

	rows, err := database.QueryContext(ctx, `SELECT idempotency_key FROM messages ORDER BY id`)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	var keys []string
	for rows.Next() {
		var key string
		require.NoError(t, rows.Scan(&key))
		keys = append(keys, key)
	}
	require.NoError(t, rows.Err())
	require.Len(t, keys, 2)
	assert.Regexp(t, `^input:[0-9a-f]{64}$`, keys[0])
	assert.Regexp(t, `^input:[0-9a-f]{64}$`, keys[1])
	assert.NotEqual(t, keys[0], keys[1])
}

func TestStoreTextOnlyRetryTreatsEmptyAndNilAttachmentsAsEqual(t *testing.T) {
	t.Parallel()

	_, store := newStoreFixture(t)
	ctx := context.Background()
	input := NewItem{
		ID: "input-1", AgentID: "agent-1",
		Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
		Text: "hello", Attachments: []Attachment{},
	}
	_, err := store.Enqueue(ctx, input)
	require.NoError(t, err)
	prepared, _, err := store.PrepareDispatch(ctx, "agent-1")
	require.NoError(t, err)
	_, _, err = store.Accept(ctx, *prepared, DispatchResult{StartsTurn: true, TurnSteerable: true})
	require.NoError(t, err)

	snapshot, err := store.Enqueue(ctx, input)
	require.NoError(t, err)
	assert.Empty(t, snapshot.Items)
	input.Attachments = nil
	snapshot, err = store.Enqueue(ctx, input)
	require.NoError(t, err)
	assert.Empty(t, snapshot.Items)
}

func TestStoreClassifiesCommandsAndRejectsAttachments(t *testing.T) {
	t.Parallel()

	_, store := newStoreFixture(t)
	ctx := context.Background()
	snapshot, err := store.Enqueue(ctx, NewItem{
		ID: "compact", AgentID: "agent-1",
		Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "  /compact\n",
		ReclassifyOnEdit: true,
	})
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_COMPACT_CONTEXT, snapshot.Items[0].Kind)
	prepared, _, err := store.PrepareDispatch(ctx, "agent-1")
	require.NoError(t, err)
	accepted, _, err := store.Accept(ctx, *prepared, DispatchResult{StartsTurn: true, TurnSteerable: true})
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.MarkType_MARK_TYPE_USER_MESSAGE, accepted.MarkType)
	for _, command := range []string{"/clear", "/compact"} {
		_, err = store.Enqueue(ctx, NewItem{
			ID: "attached-" + command, AgentID: "agent-1",
			Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: command,
			Attachments: []Attachment{{Filename: "x", Data: []byte("x")}},
		})
		assert.ErrorIs(t, err, ErrInvalidInput)
	}
}

func TestExactCommandClassifierUsesTrimmedCaseSensitiveMatches(t *testing.T) {
	t.Parallel()

	classifier := ExactCommandClassifier{}
	for _, test := range []struct {
		text string
		want leapmuxv1.AgentInputKind
	}{
		{text: " /clear\n", want: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_CLEAR_CONTEXT},
		{text: "/reset", want: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_CLEAR_CONTEXT},
		{text: "/new", want: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_CLEAR_CONTEXT},
		{text: "\t/compact ", want: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_COMPACT_CONTEXT},
		{text: "/summarize", want: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_COMPACT_CONTEXT},
		{text: "/Compact", want: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE},
		{text: "/compact now", want: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE},
	} {
		assert.Equal(t, test.want, classifier.Classify(leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, test.text), test.text)
	}
}

func TestStoreEditBarrierTakeoverAndAtomicUpdate(t *testing.T) {
	t.Parallel()

	_, store := newStoreFixture(t)
	ctx := context.Background()
	_, err := store.Enqueue(ctx, NewItem{ID: "one", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "one", ReclassifyOnEdit: true})
	require.NoError(t, err)
	_, err = store.Enqueue(ctx, NewItem{ID: "two", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "two"})
	require.NoError(t, err)

	snapshot, _, _, err := store.BeginEdit(ctx, "agent-1", "one", "client-a", false)
	require.NoError(t, err)
	assert.Equal(t, "client-a", snapshot.Items[0].EditOwner)
	_, _, _, err = store.BeginEdit(ctx, "agent-1", "one", "client-b", false)
	assert.ErrorIs(t, err, ErrEditOwned)
	snapshot, _, _, err = store.BeginEdit(ctx, "agent-1", "one", "client-b", true)
	require.NoError(t, err)
	_, err = store.Update(ctx, "agent-1", "one", "client-b", snapshot.Items[0].Version+1, "stale", nil)
	assert.ErrorIs(t, err, ErrVersionConflict)
	snapshot, err = store.Update(ctx, "agent-1", "one", "client-b", snapshot.Items[0].Version, "/summarize", nil)
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_COMPACT_CONTEXT, snapshot.Items[0].Kind)
	assert.Empty(t, snapshot.Items[0].EditOwner)
	assert.Equal(t, uint64(2), snapshot.Items[0].Version)
}

func TestStoreEnforcesItemSizeCap(t *testing.T) {
	t.Parallel()

	_, store := newStoreFixture(t)
	ctx := context.Background()
	_, err := store.Enqueue(ctx, NewItem{
		ID: "at-cap", AgentID: "agent-1", Text: strings.Repeat("x", MaxItemBytes),
		Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
	})
	require.NoError(t, err)
	_, err = store.Enqueue(ctx, NewItem{
		ID: "over-cap", AgentID: "agent-1", Text: strings.Repeat("x", MaxItemBytes+1),
		Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
	})
	assert.ErrorIs(t, err, ErrItemTooLarge)
	_, err = store.Enqueue(ctx, NewItem{
		ID: "mixed-over-cap", AgentID: "agent-1", Text: strings.Repeat("x", MaxItemBytes),
		Kind:        leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
		Attachments: []Attachment{{Filename: "one.bin", Data: []byte{1}}},
	})
	assert.ErrorIs(t, err, ErrItemTooLarge)
	_, err = store.Enqueue(ctx, NewItem{
		ID: "null-text", AgentID: "agent-1", Text: "before\x00after",
		Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
	})
	assert.ErrorIs(t, err, ErrInvalidInput)
}

func TestStoreEditPreservesGeneratedOperationKind(t *testing.T) {
	t.Parallel()

	_, store := newStoreFixture(t)
	ctx := context.Background()
	_, err := store.Enqueue(ctx, NewItem{
		ID: "generated", AgentID: "agent-1",
		Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_PLAN_EXECUTION, Text: "Implement the plan.",
	})
	require.NoError(t, err)
	snapshot, _, _, err := store.BeginEdit(ctx, "agent-1", "generated", "client", false)
	require.NoError(t, err)
	snapshot, err = store.Update(ctx, "agent-1", "generated", "client", snapshot.Items[0].Version, "/compact", []Attachment{{Filename: "plan.txt", Data: []byte("changed")}})
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_PLAN_EXECUTION, snapshot.Items[0].Kind)
	require.Len(t, snapshot.Items[0].Metadata, 1)
	assert.Equal(t, "plan.txt", snapshot.Items[0].Metadata[0].Filename)
}

func TestStoreMoveDeletePauseAndResume(t *testing.T) {
	t.Parallel()

	_, store := newStoreFixture(t)
	ctx := context.Background()
	for _, inputID := range []string{"one", "two", "three"} {
		_, err := store.Enqueue(ctx, NewItem{ID: inputID, AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: inputID})
		require.NoError(t, err)
	}
	snapshot, err := store.Move(ctx, "agent-1", "three", "one")
	require.NoError(t, err)
	assert.Equal(t, []string{"three", "one", "two"}, []string{snapshot.Items[0].ID, snapshot.Items[1].ID, snapshot.Items[2].ID})
	snapshot, err = store.Delete(ctx, "agent-1", "one")
	require.NoError(t, err)
	assert.Equal(t, int64(1), snapshot.Items[0].Order)
	assert.Equal(t, int64(2), snapshot.Items[1].Order)
	snapshot, err = store.SetPaused(ctx, "agent-1", true, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_MANUAL)
	require.NoError(t, err)
	assert.True(t, snapshot.Paused)
	snapshot, err = store.SetPaused(ctx, "agent-1", false, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_UNSPECIFIED)
	require.NoError(t, err)
	assert.False(t, snapshot.Paused)
}

func TestStoreMoveBeforeSelfIsNoOp(t *testing.T) {
	t.Parallel()

	_, store := newStoreFixture(t)
	ctx := context.Background()
	snapshot, err := store.Enqueue(ctx, NewItem{ID: "one", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "one"})
	require.NoError(t, err)
	revision := snapshot.Revision

	snapshot, err = store.Move(ctx, "agent-1", "one", "one")
	require.NoError(t, err)
	require.Len(t, snapshot.Items, 1)
	assert.Equal(t, "one", snapshot.Items[0].ID)
	assert.Equal(t, revision, snapshot.Revision)
}

func TestStoreMoveCannotCrossDispatchingHead(t *testing.T) {
	t.Parallel()

	_, store := newStoreFixture(t)
	ctx := context.Background()
	for _, inputID := range []string{"one", "two", "three"} {
		_, err := store.Enqueue(ctx, NewItem{ID: inputID, AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: inputID})
		require.NoError(t, err)
	}
	prepared, _, err := store.PrepareDispatch(ctx, "agent-1")
	require.NoError(t, err)
	require.NotNil(t, prepared)

	_, err = store.Move(ctx, "agent-1", "three", "one")
	assert.ErrorIs(t, err, ErrConflict)
}

func TestStoreSteerRejectsOperationHeadAndCompactionTurn(t *testing.T) {
	t.Parallel()

	t.Run("operation head", func(t *testing.T) {
		_, store := newStoreFixture(t)
		ctx := context.Background()
		_, err := store.Enqueue(ctx, NewItem{ID: "active", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "active"})
		require.NoError(t, err)
		prepared, _, err := store.PrepareDispatch(ctx, "agent-1")
		require.NoError(t, err)
		_, _, err = store.Accept(ctx, *prepared, DispatchResult{StartsTurn: true, TurnSteerable: true})
		require.NoError(t, err)
		_, err = store.Enqueue(ctx, NewItem{ID: "compact", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_COMPACT_CONTEXT, Text: "/compact"})
		require.NoError(t, err)

		_, _, err = store.PrepareSteer(ctx, "agent-1", "compact")
		assert.ErrorIs(t, err, ErrSteeringState)
	})

	t.Run("compaction turn", func(t *testing.T) {
		_, store := newStoreFixture(t)
		ctx := context.Background()
		_, err := store.Enqueue(ctx, NewItem{ID: "compact", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_COMPACT_CONTEXT, Text: "/compact"})
		require.NoError(t, err)
		prepared, _, err := store.PrepareDispatch(ctx, "agent-1")
		require.NoError(t, err)
		_, _, err = store.Accept(ctx, *prepared, DispatchResult{StartsTurn: true})
		require.NoError(t, err)
		_, err = store.Enqueue(ctx, NewItem{ID: "next", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "next"})
		require.NoError(t, err)

		_, _, err = store.PrepareSteer(ctx, "agent-1", "next")
		assert.ErrorIs(t, err, ErrSteeringState)
	})
}

func TestStoreRequeuedSteerReservesASequenceAfterTheEndedTurn(t *testing.T) {
	t.Parallel()

	database, store := newStoreFixture(t)
	ctx := context.Background()
	_, err := store.Enqueue(ctx, NewItem{
		ID: "active", AgentID: "agent-1", Text: "active",
		Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
	})
	require.NoError(t, err)
	active, _, err := store.PrepareDispatch(ctx, "agent-1")
	require.NoError(t, err)
	require.NotNil(t, active)
	_, _, err = store.Accept(ctx, *active, DispatchResult{StartsTurn: true, TurnSteerable: true})
	require.NoError(t, err)
	_, err = store.Enqueue(ctx, NewItem{
		ID: "steer", AgentID: "agent-1", Text: "guide",
		Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
	})
	require.NoError(t, err)
	steer, _, err := store.PrepareSteer(ctx, "agent-1", "steer")
	require.NoError(t, err)
	require.NotNil(t, steer)

	snapshot, err := store.RequeuePrepared(ctx, "agent-1", "steer")
	require.NoError(t, err)
	require.Len(t, snapshot.Items, 1)
	assert.Zero(t, snapshot.Items[0].ReservedSeq)
	var turnEndSeq int64
	require.NoError(t, database.QueryRowContext(ctx, `
		UPDATE agents SET message_seq_hwm = message_seq_hwm + 1
		WHERE id = 'agent-1' RETURNING message_seq_hwm`).Scan(&turnEndSeq))
	_, err = database.ExecContext(ctx, `
		INSERT INTO messages (id, agent_id, seq, source, content, content_compression)
		VALUES ('turn-end', 'agent-1', ?, ?, '{}', ?)`,
		turnEndSeq, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE)
	require.NoError(t, err)
	_, _, err = store.TurnEnded(ctx, "agent-1")
	require.NoError(t, err)
	retried, _, err := store.PrepareDispatch(ctx, "agent-1")
	require.NoError(t, err)
	require.NotNil(t, retried)
	assert.Greater(t, turnEndSeq, steer.ReservedSeq)
	assert.Greater(t, retried.ReservedSeq, turnEndSeq)
}

func TestStoreReservesPositiveSequenceAndCommitsAfterAcceptance(t *testing.T) {
	t.Parallel()

	database, store := newStoreFixture(t)
	ctx := context.Background()
	_, err := store.Enqueue(ctx, NewItem{ID: "one", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "hello"})
	require.NoError(t, err)
	prepared, snapshot, err := store.PrepareDispatch(ctx, "agent-1")
	require.NoError(t, err)
	require.NotNil(t, prepared)
	assert.Positive(t, prepared.ReservedSeq)
	assert.True(t, snapshot.ActiveTurn)
	assert.False(t, snapshot.ActiveTurnSteerable, "dispatch preparation does not assume provider steering support")
	var transcriptCount int
	require.NoError(t, database.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE agent_id = 'agent-1'`).Scan(&transcriptCount))
	assert.Zero(t, transcriptCount)

	transcript, snapshot, err := store.Accept(ctx, *prepared, DispatchResult{StartsTurn: true, TurnSteerable: true, SpanLines: `[{"span_id":"tool-1"}]`})
	require.NoError(t, err)
	assert.Equal(t, prepared.ReservedSeq, transcript.Seq)
	assert.Empty(t, snapshot.Items)
	assert.True(t, snapshot.ActiveTurn)
	var seq int64
	var spanLines string
	require.NoError(t, database.QueryRowContext(ctx, `SELECT seq, span_lines FROM messages WHERE id = 'one'`).Scan(&seq, &spanLines))
	assert.Equal(t, prepared.ReservedSeq, seq)
	assert.JSONEq(t, `[{"span_id":"tool-1"}]`, spanLines)
}

func TestStoreAcceptRetriesAfterAConcurrentWriterInvalidatesItsSnapshot(t *testing.T) {
	t.Parallel()

	database, store := newStoreFixture(t)
	ctx := t.Context()
	_, err := store.Enqueue(ctx, NewItem{ID: "one", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "hello"})
	require.NoError(t, err)
	prepared, _, err := store.PrepareDispatch(ctx, "agent-1")
	require.NoError(t, err)
	require.NotNil(t, prepared)

	readComplete := make(chan struct{})
	allowWrite := make(chan struct{})
	var once sync.Once
	store.beforeAcceptWrite = func() {
		once.Do(func() {
			close(readComplete)
			<-allowWrite
		})
	}
	type acceptResult struct {
		transcript AcceptedTranscript
		err        error
	}
	accepted := make(chan acceptResult, 1)
	go func() {
		transcript, _, err := store.Accept(ctx, *prepared, DispatchResult{StartsTurn: true})
		accepted <- acceptResult{transcript: transcript, err: err}
	}()
	<-readComplete
	_, err = database.ExecContext(ctx, `UPDATE agents SET message_seq_hwm = message_seq_hwm + 1 WHERE id = ?`, "agent-1")
	require.NoError(t, err)
	close(allowWrite)

	result := <-accepted
	require.NoError(t, result.err)
	assert.Equal(t, prepared.ReservedSeq, result.transcript.Seq)
	var count int
	require.NoError(t, database.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE id = ?`, "one").Scan(&count))
	assert.Equal(t, 1, count)
}

func TestStoreRecoveryDistinguishesUncertainAndInterrupted(t *testing.T) {
	t.Parallel()

	database, store := newStoreFixture(t)
	ctx := context.Background()
	_, err := store.Enqueue(ctx, NewItem{ID: "one", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "hello"})
	require.NoError(t, err)
	_, _, err = store.PrepareDispatch(ctx, "agent-1")
	require.NoError(t, err)
	snapshots, err := NewStore(database).Recover(ctx)
	require.NoError(t, err)
	require.Len(t, snapshots, 1)
	assert.Equal(t, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DELIVERY_UNCERTAIN, snapshots[0].Items[0].State)
	assert.Equal(t, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_DELIVERY_UNCERTAIN, snapshots[0].PauseReason)

	database, store = newStoreFixture(t)
	_, err = store.Enqueue(ctx, NewItem{ID: "two", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "hello"})
	require.NoError(t, err)
	prepared, _, err := store.PrepareDispatch(ctx, "agent-1")
	require.NoError(t, err)
	_, _, err = store.Accept(ctx, *prepared, DispatchResult{StartsTurn: true, TurnSteerable: true})
	require.NoError(t, err)
	snapshots, err = NewStore(database).Recover(ctx)
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_INTERRUPTED, snapshots[0].PauseReason)
}

func TestStoreRecoveryIgnoresClosedAgents(t *testing.T) {
	t.Parallel()

	database, store := newStoreFixture(t)
	ctx := context.Background()
	_, err := store.SetPaused(ctx, "agent-1", true, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_MANUAL)
	require.NoError(t, err)
	_, err = store.Enqueue(ctx, NewItem{
		ID: "queued", AgentID: "agent-1", Text: "never dispatch",
		Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
	})
	require.NoError(t, err)
	_, err = database.ExecContext(ctx, `UPDATE agents SET closed_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now') WHERE id = 'agent-1'`)
	require.NoError(t, err)

	snapshots, err := NewStore(database).Recover(ctx)
	require.NoError(t, err)
	assert.Empty(t, snapshots)
}

func TestStoreRetryRequiresUncertainDeliveryConfirmation(t *testing.T) {
	t.Parallel()

	database, store := newStoreFixture(t)
	ctx := context.Background()
	_, err := store.Enqueue(ctx, NewItem{ID: "one", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "hello"})
	require.NoError(t, err)
	prepared, _, err := store.PrepareDispatch(ctx, "agent-1")
	require.NoError(t, err)
	_, err = store.FailDispatch(ctx, "agent-1", "one", assert.AnError, true)
	require.NoError(t, err)

	_, err = store.Retry(ctx, "agent-1", "one", false)
	assert.ErrorIs(t, err, ErrUncertainConfirmation)
	snapshot, err := store.Retry(ctx, "agent-1", "one", true)
	require.NoError(t, err)
	require.Len(t, snapshot.Items, 1)
	assert.Equal(t, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED, snapshot.Items[0].State)
	// The item returned to QUEUED, so it released its reservation. Any message
	// the transcript took between the failure and the retry holds a higher seq
	// than the stale reservation, so reusing it would sort the retried message
	// above rows that already precede it.
	_, err = database.ExecContext(ctx, `UPDATE agents SET message_seq_hwm = message_seq_hwm + 5 WHERE id = ?`, "agent-1")
	require.NoError(t, err)
	retried, _, err := store.PrepareRetry(ctx, "agent-1")
	require.NoError(t, err)
	require.NotNil(t, retried)
	assert.Greater(t, retried.ReservedSeq, prepared.ReservedSeq)
}

func TestStoreRetryRejectsEditedFailedHead(t *testing.T) {
	t.Parallel()

	_, store := newStoreFixture(t)
	ctx := context.Background()
	_, err := store.Enqueue(ctx, NewItem{ID: "one", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "hello"})
	require.NoError(t, err)
	_, _, err = store.PrepareDispatch(ctx, "agent-1")
	require.NoError(t, err)
	_, err = store.FailDispatch(ctx, "agent-1", "one", assert.AnError, false)
	require.NoError(t, err)
	_, _, _, err = store.BeginEdit(ctx, "agent-1", "one", "client", false)
	require.NoError(t, err)

	_, err = store.Retry(ctx, "agent-1", "one", false)
	assert.ErrorIs(t, err, ErrEditOwned)
}

func TestStoreEnforcesQueueItemCap(t *testing.T) {
	database, store := newStoreFixture(t)
	ctx := context.Background()
	_, err := database.ExecContext(ctx, `INSERT INTO agent_input_queue_state (agent_id) VALUES ('agent-1')`)
	require.NoError(t, err)
	for i := 1; i <= MaxItems; i++ {
		_, err := database.ExecContext(ctx, `
			INSERT INTO agent_input_queue_items (id, agent_id, order_index, kind, text)
			VALUES (?, 'agent-1', ?, 1, 'queued')`, fmt.Sprintf("input-%d", i), i)
		require.NoError(t, err)
	}
	_, err = store.Enqueue(ctx, NewItem{ID: "overflow", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "overflow"})
	assert.ErrorIs(t, err, ErrQueueFull)
}

func TestStoreRejectsAttachmentMetadataOverTheLimits(t *testing.T) {
	t.Parallel()

	_, store := newStoreFixture(t)
	ctx := context.Background()
	tooMany := make([]Attachment, MaxAttachmentsPerItem+1)
	for i := range tooMany {
		tooMany[i] = Attachment{Filename: fmt.Sprintf("file-%d", i)}
	}
	_, err := store.Enqueue(ctx, NewItem{
		ID: "too-many", AgentID: "agent-1", Text: "attachments",
		Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Attachments: tooMany,
	})
	assert.ErrorIs(t, err, ErrInvalidInput)

	_, err = store.Enqueue(ctx, NewItem{
		ID: "long-name", AgentID: "agent-1", Text: "attachment",
		Kind:        leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
		Attachments: []Attachment{{Filename: strings.Repeat("x", MaxAttachmentFilenameBytes+1)}},
	})
	assert.ErrorIs(t, err, ErrInvalidInput)

	_, err = store.Enqueue(ctx, NewItem{
		ID: "long-mime", AgentID: "agent-1", Text: "attachment",
		Kind:        leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
		Attachments: []Attachment{{Filename: "file", MimeType: strings.Repeat("x", MaxAttachmentMIMETypeBytes+1)}},
	})
	assert.ErrorIs(t, err, ErrInvalidInput)
}

func TestStoreEnforcesAggregateAttachmentCap(t *testing.T) {
	database, store := newStoreFixture(t)
	ctx := context.Background()
	_, err := database.ExecContext(ctx, `INSERT INTO agent_input_queue_state (agent_id) VALUES ('agent-1')`)
	require.NoError(t, err)
	for i := 1; i <= 10; i++ {
		inputID := fmt.Sprintf("input-%d", i)
		_, err := database.ExecContext(ctx, `
			INSERT INTO agent_input_queue_items (id, agent_id, order_index, kind, text)
			VALUES (?, 'agent-1', ?, 1, 'queued')`, inputID, i)
		require.NoError(t, err)
		_, err = database.ExecContext(ctx, `
			INSERT INTO agent_input_queue_attachments (item_id, position, filename, mime_type, data, size)
			VALUES (?, 0, 'blob.bin', 'application/octet-stream', zeroblob(?), ?)`, inputID, MaxItemBytes, MaxItemBytes)
		require.NoError(t, err)
	}
	_, err = store.Enqueue(ctx, NewItem{
		ID: "overflow", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
		Attachments: []Attachment{{Filename: "one.bin", Data: []byte{1}}},
	})
	assert.ErrorIs(t, err, ErrQueueAttachmentsLarge)
}

// The head returns to the queue and the pause it caused lifts, so the items
// behind it dispatch again. Retry that left the pause in place stalled the
// whole queue with no visible reason: the failure the user retried had already
// succeeded, and only a manual resume moved the queue again.
func TestStoreRetryLiftsTheDeliveryPause(t *testing.T) {
	t.Parallel()

	_, store := newStoreFixture(t)
	ctx := context.Background()
	for _, id := range []string{"one", "two"} {
		_, err := store.Enqueue(ctx, NewItem{ID: id, AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: id})
		require.NoError(t, err)
	}
	_, _, err := store.PrepareDispatch(ctx, "agent-1")
	require.NoError(t, err)
	failed, err := store.FailDispatch(ctx, "agent-1", "one", assert.AnError, false)
	require.NoError(t, err)
	require.True(t, failed.Paused)

	retried, err := store.Retry(ctx, "agent-1", "one", false)
	require.NoError(t, err)
	assert.False(t, retried.Paused, "the retried failure no longer holds the queue")
	assert.Equal(t, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_UNSPECIFIED, retried.PauseReason)
}

// A pause the USER created is not the delivery pause, so a retry leaves it.
func TestStoreRetryKeepsAManualPause(t *testing.T) {
	t.Parallel()

	_, store := newStoreFixture(t)
	ctx := context.Background()
	_, err := store.Enqueue(ctx, NewItem{ID: "one", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "hello"})
	require.NoError(t, err)
	_, _, err = store.PrepareDispatch(ctx, "agent-1")
	require.NoError(t, err)
	_, err = store.FailDispatch(ctx, "agent-1", "one", assert.AnError, false)
	require.NoError(t, err)
	_, err = store.SetPaused(ctx, "agent-1", true, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_MANUAL)
	require.NoError(t, err)

	retried, err := store.Retry(ctx, "agent-1", "one", false)
	require.NoError(t, err)
	assert.True(t, retried.Paused, "the user's pause outlives a retry it did not cause")
	assert.Equal(t, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_MANUAL, retried.PauseReason)
}

// A plan-execution input keeps its marker whatever else it carries. The
// renderers key the collapsible "Execute plan" bubble off that marker alone,
// so an attachment used to turn the row into an ordinary user message.
func TestStoreAcceptKeepsThePlanMarkerWithAttachments(t *testing.T) {
	t.Parallel()

	item := StoredItem{Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_PLAN_EXECUTION, Text: "run it"}
	withAttachment, err := transcriptContent(item, []Attachment{{Filename: "a.png", MimeType: "image/png"}})
	require.NoError(t, err)
	withoutAttachment, err := transcriptContent(item, nil)
	require.NoError(t, err)

	var parsedWith, parsedWithout map[string]any
	require.NoError(t, json.Unmarshal(withAttachment, &parsedWith))
	require.NoError(t, json.Unmarshal(withoutAttachment, &parsedWithout))
	assert.Equal(t, true, parsedWith["planExecution"])
	assert.Equal(t, true, parsedWithout["planExecution"])
	assert.Len(t, parsedWith["attachments"], 1)
	assert.NotContains(t, parsedWithout, "attachments")
}

// The persisted row carries the passthrough span column, and the live
// broadcast must repeat it. Without it the same bubble renders with no bars
// now and with them after a reload.
func TestStoreAcceptReportsTheSpanColumn(t *testing.T) {
	t.Parallel()

	_, store := newStoreFixture(t)
	ctx := context.Background()
	_, err := store.Enqueue(ctx, NewItem{ID: "one", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "hello"})
	require.NoError(t, err)
	prepared, _, err := store.PrepareDispatch(ctx, "agent-1")
	require.NoError(t, err)
	transcript, _, err := store.Accept(ctx, *prepared, DispatchResult{StartsTurn: true, TurnSteerable: true, SpanLines: `[{"color":2}]`})
	require.NoError(t, err)
	assert.Equal(t, `[{"color":2}]`, transcript.SpanLines)
}

// Steering always addresses one item. An empty ID skipped the head test and
// reserved whatever sat at the head; the recovery paths then requeued by that
// same empty ID, matched no row, and left the item DISPATCHING until the
// Worker restarted -- a queue that stops forever.
func TestStorePrepareSteerRefusesAnEmptyInputID(t *testing.T) {
	t.Parallel()

	_, store := newStoreFixture(t)
	ctx := context.Background()
	_, err := store.Enqueue(ctx, NewItem{ID: "one", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "hello"})
	require.NoError(t, err)

	prepared, _, err := store.PrepareSteer(ctx, "agent-1", "")
	assert.ErrorIs(t, err, ErrNotHead)
	assert.Nil(t, prepared)
	snapshot, err := store.Snapshot(ctx, "agent-1")
	require.NoError(t, err)
	require.Len(t, snapshot.Items, 1)
	assert.Equal(t, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED, snapshot.Items[0].State)
}

// A client that closes its tab abandons its edit lock, and the client ID lives
// in session storage, so it never returns. Recovery releases every lock: a
// stale one refuses the head forever, and the queue then stops with no pause
// and nothing on screen to explain it.
func TestStoreRecoveryReleasesAnAbandonedEditLock(t *testing.T) {
	t.Parallel()

	database, store := newStoreFixture(t)
	ctx := context.Background()
	_, err := store.Enqueue(ctx, NewItem{ID: "one", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "hello"})
	require.NoError(t, err)
	_, _, _, err = store.BeginEdit(ctx, "agent-1", "one", "client-gone", false)
	require.NoError(t, err)
	blocked, _, err := store.PrepareDispatch(ctx, "agent-1")
	require.NoError(t, err)
	require.Nil(t, blocked, "an edited head blocks dispatch")

	_, err = NewStore(database).Recover(ctx)
	require.NoError(t, err)
	prepared, _, err := store.PrepareDispatch(ctx, "agent-1")
	require.NoError(t, err)
	assert.NotNil(t, prepared, "recovery released the lock, so the head dispatches")
}

// The store answers the steering precondition, so a client never offers a
// Steer the Worker refuses.
func TestStoreSnapshotAnswersTheSteerPrecondition(t *testing.T) {
	t.Parallel()

	_, store := newStoreFixture(t)
	ctx := context.Background()
	for _, id := range []string{"one", "two"} {
		_, err := store.Enqueue(ctx, NewItem{ID: id, AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: id})
		require.NoError(t, err)
	}
	idle, err := store.Snapshot(ctx, "agent-1")
	require.NoError(t, err)
	assert.False(t, idle.Items[0].CanSteer, "with no active turn there is nothing to steer into")

	prepared, _, err := store.PrepareDispatch(ctx, "agent-1")
	require.NoError(t, err)
	_, active, err := store.Accept(ctx, *prepared, DispatchResult{StartsTurn: true, TurnSteerable: true})
	require.NoError(t, err)
	require.Len(t, active.Items, 1)
	assert.True(t, active.Items[0].CanSteer)

	// A clear runs no regular turn, so the head is not steerable into it.
	_, _, err = store.TurnEnded(ctx, "agent-1")
	require.NoError(t, err)
	_, err = store.Enqueue(ctx, NewItem{ID: "clear", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_CLEAR_CONTEXT, Text: "/clear"})
	require.NoError(t, err)
	_, err = store.Move(ctx, "agent-1", "clear", "two")
	require.NoError(t, err)
	clearPrepared, _, err := store.PrepareDispatch(ctx, "agent-1")
	require.NoError(t, err)
	require.NotNil(t, clearPrepared)
	_, clearing, err := store.Accept(ctx, *clearPrepared, DispatchResult{StartsTurn: true})
	require.NoError(t, err)
	require.NotEmpty(t, clearing.Items)
	assert.False(t, clearing.Items[0].CanSteer, "a clear is not a regular turn")
}

func TestStoreAcceptKeepsTheRevisionMovingWhenTheTurnEndedMidDispatch(t *testing.T) {
	t.Parallel()

	// The manager releases the coordinator lock across the provider call, so a
	// turn end can commit its clear between PrepareDispatch and Accept. Accept
	// must not write the turn back -- no envelope ever ends it again -- but the
	// item DID leave the queue, so every watcher still owes a revision it can
	// order that removal against.
	_, store := newStoreFixture(t)
	ctx := context.Background()
	_, err := store.Enqueue(ctx, NewItem{
		ID: "one", AgentID: "agent-1", Text: "one",
		Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
	})
	require.NoError(t, err)
	prepared, _, err := store.PrepareDispatch(ctx, "agent-1")
	require.NoError(t, err)
	require.NotNil(t, prepared)

	cleared, changed, err := store.TurnEnded(ctx, "agent-1")
	require.NoError(t, err)
	require.True(t, changed, "the dispatch opened a turn, so the clear moves the state")

	_, snapshot, err := store.Accept(ctx, *prepared, DispatchResult{StartsTurn: true, TurnSteerable: true})
	require.NoError(t, err)
	assert.False(t, snapshot.ActiveTurn, "a turn that ended inside the dispatch is not restored")
	assert.Empty(t, snapshot.Items, "the item still left the queue")
	assert.Greater(t, snapshot.Revision, cleared.Revision,
		"the removal needs a revision, although the guarded turn write matched no row")
}

func TestStoreUnclassifiedProviderTurnRefusesASteer(t *testing.T) {
	t.Parallel()

	// This provider supplies no kind, so the store records none. The steer
	// predicate reads that column. A fabricated USER_MESSAGE would offer a
	// steer into whatever the agent process started on its own -- an
	// auto-compaction, a plan the CLI resumed, a background turn.
	_, store := newStoreFixture(t)
	ctx := context.Background()
	snapshot, changed, err := store.TurnStarted(ctx, "agent-1", false)
	require.NoError(t, err)
	require.True(t, changed)
	require.True(t, snapshot.ActiveTurn)
	assert.False(t, snapshot.ActiveTurnSteerable,
		"the signal states no kind, so the store invents none")

	_, err = store.Enqueue(ctx, NewItem{
		ID: "one", AgentID: "agent-1", Text: "steer me",
		Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
	})
	require.NoError(t, err)
	snapshot, err = store.Snapshot(ctx, "agent-1")
	require.NoError(t, err)
	require.Len(t, snapshot.Items, 1)
	assert.False(t, snapshot.Items[0].CanSteer, "the browser is offered no steer it cannot make")

	_, _, err = store.PrepareSteer(ctx, "agent-1", "one")
	assert.ErrorIs(t, err, ErrSteeringState, "and the Worker refuses one that arrives anyway")
}

// TestStoreSnapshotAnswersThePreemptPrecondition is preemption's counterpart of
// the steer precondition: the same head/kind/turn terms, minus the steerable
// turn -- an unclassified turn is exactly the turn Preempt cancels.
func TestStoreSnapshotAnswersThePreemptPrecondition(t *testing.T) {
	t.Parallel()

	_, store := newStoreFixture(t)
	ctx := context.Background()
	for _, id := range []string{"one", "two"} {
		_, err := store.Enqueue(ctx, NewItem{ID: id, AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: id})
		require.NoError(t, err)
	}
	idle, err := store.Snapshot(ctx, "agent-1")
	require.NoError(t, err)
	assert.False(t, idle.Items[0].CanPreempt, "with no active turn there is nothing to cancel")

	snapshot, _, err := store.TurnStarted(ctx, "agent-1", false)
	require.NoError(t, err)
	assert.False(t, snapshot.ActiveTurnSteerable)
	active, err := store.Snapshot(ctx, "agent-1")
	require.NoError(t, err)
	assert.True(t, active.Items[0].CanPreempt,
		"an unsteerable active turn is the turn Preempt exists to cancel")
	assert.False(t, active.Items[0].CanSteer)
	assert.False(t, active.Items[1].CanPreempt, "only the head may pre-empt")

	_, _, err = store.TurnEnded(ctx, "agent-1")
	require.NoError(t, err)
	ended, err := store.Snapshot(ctx, "agent-1")
	require.NoError(t, err)
	assert.False(t, ended.Items[0].CanPreempt)
}

// Preemption's kind term is its OWN, not steering's. Steering injects into the
// running turn, so it admits only a kind the model can take mid-turn; preemption
// injects nothing -- it cancels the turn and lets the ordinary drain deliver, and
// that drain dispatches every kind. A queued clear behind a running turn therefore
// has a turn worth cancelling, and the reader's only other route is Stop, which
// pauses the queue under an owner they must lift by hand.
func TestStoreSnapshotPreemptsAKindSteeringRefuses(t *testing.T) {
	t.Parallel()

	_, store := newStoreFixture(t)
	ctx := context.Background()
	_, err := store.Enqueue(ctx, NewItem{ID: "clear", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_CLEAR_CONTEXT, Text: "/clear"})
	require.NoError(t, err)
	_, _, err = store.TurnStarted(ctx, "agent-1", false)
	require.NoError(t, err)

	snapshot, err := store.Snapshot(ctx, "agent-1")
	require.NoError(t, err)
	assert.True(t, snapshot.Items[0].CanPreempt, "a cancel can carry a clear that the drain then dispatches")
	assert.False(t, snapshot.Items[0].CanSteer, "steering still refuses it: the model cannot take a clear mid-turn")
}

// The plan approval that REPLACES the turn is the one exclusion. The store already
// dispatches that head THROUGH the running turn, so cancelling destroys work for
// nothing.
func TestStoreSnapshotRefusesToPreemptAHeadThatReplacesTheTurn(t *testing.T) {
	t.Parallel()

	_, store := newStoreFixture(t)
	ctx := context.Background()
	_, err := store.Enqueue(ctx, NewItem{
		ID: "plan", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_PLAN_EXECUTION,
		Text: "execute", PrepareContext: true,
	})
	require.NoError(t, err)
	_, _, err = store.TurnStarted(ctx, "agent-1", false)
	require.NoError(t, err)

	snapshot, err := store.Snapshot(ctx, "agent-1")
	require.NoError(t, err)
	assert.False(t, snapshot.Items[0].CanPreempt)
}

func TestStoreClassifiedProviderTurnOffersASteer(t *testing.T) {
	t.Parallel()

	// Codex accepts turn/steer for every turn/started notification. Such a turn
	// can start without a queue dispatch, so the provider's classification is
	// the only fact that lets the queue offer the operation.
	_, store := newStoreFixture(t)
	ctx := context.Background()
	snapshot, changed, err := store.TurnStarted(ctx, "agent-1", true)
	require.NoError(t, err)
	require.True(t, changed)
	assert.True(t, snapshot.ActiveTurnSteerable)

	_, err = store.Enqueue(ctx, NewItem{
		ID: "one", AgentID: "agent-1", Text: "steer me",
		Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
	})
	require.NoError(t, err)
	snapshot, err = store.Snapshot(ctx, "agent-1")
	require.NoError(t, err)
	require.Len(t, snapshot.Items, 1)
	assert.True(t, snapshot.Items[0].CanSteer)

	prepared, _, err := store.PrepareSteer(ctx, "agent-1", "one")
	require.NoError(t, err)
	require.NotNil(t, prepared)
}

func TestStoreTurnStartAddsALateProviderClassification(t *testing.T) {
	t.Parallel()

	// An unknown future enum is unclassified. A later classified start is
	// richer than that first start, so the store must keep it.
	_, store := newStoreFixture(t)
	ctx := context.Background()
	first, changed, err := store.TurnStarted(ctx, "agent-1", false)
	require.NoError(t, err)
	require.True(t, changed)
	assert.False(t, first.ActiveTurnSteerable)

	classified, changed, err := store.TurnStarted(ctx, "agent-1", true)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Greater(t, classified.Revision, first.Revision)
	assert.True(t, classified.ActiveTurnSteerable)

	repeated, changed, err := store.TurnStarted(ctx, "agent-1", true)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, classified.Revision, repeated.Revision)
}

func TestStoreAbandonUnownedTurnClearsAProviderReportedTurnOnly(t *testing.T) {
	t.Parallel()

	// A process boundary runs no turn. The one a provider reported has no other
	// end once that process is gone -- a stalled CLI never sends its result, and
	// an explicit stop skips the AGENT_STOPPED pause on purpose -- so the queue
	// would hold every later message with no pause and no error.
	_, store := newStoreFixture(t)
	ctx := context.Background()
	_, _, err := store.TurnStarted(ctx, "agent-1", false)
	require.NoError(t, err)

	snapshot, changed, err := store.AbandonUnownedTurn(ctx, "agent-1")
	require.NoError(t, err)
	assert.True(t, changed)
	assert.False(t, snapshot.ActiveTurn, "no process, no turn")

	_, changed, err = store.AbandonUnownedTurn(ctx, "agent-1")
	require.NoError(t, err)
	assert.False(t, changed, "a boundary that clears nothing moves no revision")
}

func TestStoreAbandonUnownedTurnKeepsTheTurnADispatchOwns(t *testing.T) {
	t.Parallel()

	// ensureAgentRunning starts the process from INSIDE a dispatch, so a
	// boundary fires while that dispatch is still in flight. Clearing there
	// would wipe the input id Accept's own identity guard matches on, and the
	// turn the dispatch just started would be dropped.
	_, store := newStoreFixture(t)
	ctx := context.Background()
	_, err := store.Enqueue(ctx, NewItem{
		ID: "one", AgentID: "agent-1", Text: "one",
		Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
	})
	require.NoError(t, err)
	prepared, _, err := store.PrepareDispatch(ctx, "agent-1")
	require.NoError(t, err)
	require.NotNil(t, prepared)

	snapshot, changed, err := store.AbandonUnownedTurn(ctx, "agent-1")
	require.NoError(t, err)
	assert.False(t, changed, "the dispatch owns this turn")
	assert.True(t, snapshot.ActiveTurn)

	_, accepted, err := store.Accept(ctx, *prepared, DispatchResult{StartsTurn: true, TurnSteerable: true})
	require.NoError(t, err)
	assert.True(t, accepted.ActiveTurn, "the dispatched turn survives the boundary")
	assert.True(t, accepted.ActiveTurnSteerable)
}

// CanPreempt must answer for the SAME guard the turn-end drain runs through.
//
// It used to restate a hand-written subset of that guard, and the subset omitted two
// terms. Pressing Preempt then cancelled the running turn and the drain refused the
// item in silence, so the reader lost the turn AND the message and saw no error.
func TestStoreSnapshotRefusesToPreemptWhatTheDrainWouldRefuse(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		// seed puts the queue into the state under test, with one QUEUED head and one
		// running turn already in place.
		seed func(t *testing.T, database *sql.DB, store *Store)
		want bool
	}{
		{
			name: "a plain head behind a running turn",
			seed: func(*testing.T, *sql.DB, *Store) {},
			want: true,
		},
		{
			// SetPaused leaves active_turn set, so a manual pause during a running turn
			// is exactly this state. The drain returns at `if snapshot.Paused`, so the
			// cancel bought nothing.
			name: "a paused queue",
			seed: func(t *testing.T, _ *sql.DB, store *Store) {
				_, err := store.SetPaused(t.Context(), "agent-1", true, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_MANUAL)
				require.NoError(t, err)
			},
			want: false,
		},
		{
			// A head another tab is editing is not this reader's to send.
			name: "a head under an edit owner",
			seed: func(t *testing.T, database *sql.DB, _ *Store) {
				_, err := database.ExecContext(t.Context(),
					`UPDATE agent_input_queue_items SET edit_owner = 'other-tab' WHERE id = 'head'`)
				require.NoError(t, err)
			},
			want: false,
		},
		{
			// A planned restart stops the old process, so a write reaches a closed pipe.
			name: "a planned restart in flight",
			seed: func(t *testing.T, database *sql.DB, _ *Store) {
				_, err := database.ExecContext(t.Context(),
					`UPDATE agent_input_queue_state SET restarting = 1 WHERE agent_id = 'agent-1'`)
				require.NoError(t, err)
			},
			want: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			database, store := newStoreFixture(t)
			_, err := store.Enqueue(t.Context(), NewItem{
				ID: "head", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "next",
			})
			require.NoError(t, err)
			_, _, err = store.TurnStarted(t.Context(), "agent-1", false)
			require.NoError(t, err)
			tc.seed(t, database, store)

			snapshot, err := store.Snapshot(t.Context(), "agent-1")
			require.NoError(t, err)
			assert.Equal(t, tc.want, snapshot.Items[0].CanPreempt)

			// The offer and the drain must agree: whatever the snapshot promises, the
			// drain must do once the turn ends.
			_, _, err = store.TurnEnded(t.Context(), "agent-1")
			require.NoError(t, err)
			prepared, _, err := store.PrepareDispatch(t.Context(), "agent-1")
			require.NoError(t, err)
			assert.Equal(t, tc.want, prepared != nil,
				"a Preempt the snapshot offers must be a dispatch the drain then performs")
		})
	}
}

// A plan approval whose control-response row is not COMPLETED must offer no Preempt.
//
// The reader would lose the running turn and the item would stay queued: prepare
// refuses an unrecorded approval both before and after the interrupt.
func TestStoreSnapshotRefusesToPreemptAnApprovalThatIsNotRecorded(t *testing.T) {
	t.Parallel()

	for _, state := range []leapmuxv1.ControlResponseState{
		leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_PENDING,
		leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_DELIVERED,
		leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_COMPLETED,
	} {
		t.Run(state.String(), func(t *testing.T) {
			t.Parallel()
			database, store := newStoreFixture(t)
			// PrepareContext is UNSET, so this head does NOT replace the turn: it waits
			// for the turn to end like ordinary input, and a cancel would carry it.
			_, err := store.Enqueue(t.Context(), NewItem{
				ID: "plan", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_PLAN_EXECUTION,
				Text: "Execute the approved plan.",
			})
			require.NoError(t, err)
			_, _, err = store.TurnStarted(t.Context(), "agent-1", false)
			require.NoError(t, err)
			_, err = database.ExecContext(t.Context(), `INSERT INTO control_response_answers
                (agent_id, request_id, claim_token, state, input_id, plan_approval_settings, agent_provider)
                VALUES ('agent-1','approval','claim',?,'plan',?,?)`,
				int64(state), []byte(`{}`), int64(leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX))
			require.NoError(t, err)

			snapshot, err := store.Snapshot(t.Context(), "agent-1")
			require.NoError(t, err)
			recorded := state == leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_COMPLETED
			assert.Equal(t, recorded, snapshot.Items[0].CanPreempt,
				"only a recorded approval has a turn worth cancelling")

			_, _, err = store.TurnEnded(t.Context(), "agent-1")
			require.NoError(t, err)
			prepared, _, err := store.PrepareDispatch(t.Context(), "agent-1")
			require.NoError(t, err)
			assert.Equal(t, recorded, prepared != nil,
				"a Preempt the snapshot offers must be a dispatch the drain then performs")
		})
	}
}

// CanSteer answers for PrepareSteer, which refuses a planned restart with
// ErrPlannedRestart. The offer used to ignore that term, so the button stayed on a
// queue whose provider process was already stopping.
func TestStoreSnapshotRefusesToSteerDuringAPlannedRestart(t *testing.T) {
	t.Parallel()

	database, store := newStoreFixture(t)
	_, err := store.Enqueue(t.Context(), NewItem{
		ID: "head", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "next",
	})
	require.NoError(t, err)
	_, _, err = store.TurnStarted(t.Context(), "agent-1", true)
	require.NoError(t, err)

	snapshot, err := store.Snapshot(t.Context(), "agent-1")
	require.NoError(t, err)
	require.True(t, snapshot.Items[0].CanSteer, "a steerable turn offers a steer")

	_, err = database.ExecContext(t.Context(), `UPDATE agent_input_queue_state SET restarting = 1 WHERE agent_id = 'agent-1'`)
	require.NoError(t, err)

	snapshot, err = store.Snapshot(t.Context(), "agent-1")
	require.NoError(t, err)
	assert.False(t, snapshot.Items[0].CanSteer, "a restart in flight refuses the steer the offer promised")
	_, _, err = store.PrepareSteer(t.Context(), "agent-1", "head")
	assert.ErrorIs(t, err, ErrPlannedRestart)
}
