package inputqueue

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
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
	var deliveryErrorColumns, fingerprintColumns int
	require.NoError(t, database.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('messages') WHERE name = 'delivery_error'`).Scan(&deliveryErrorColumns))
	require.NoError(t, database.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('messages') WHERE name = 'input_fingerprint'`).Scan(&fingerprintColumns))
	assert.Zero(t, deliveryErrorColumns)
	assert.Equal(t, 1, fingerprintColumns)
	var editIndexSQL string
	require.NoError(t, database.QueryRowContext(ctx,
		`SELECT sql FROM sqlite_master WHERE name = 'idx_agent_input_queue_one_edit'`).Scan(&editIndexSQL))
	assert.Contains(t, editIndexSQL, "WHERE edit_owner <> ''")
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

func TestStoreSnapshotsBoundTextButEditReturnsFullText(t *testing.T) {
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

	_, store := newStoreFixture(t)
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
	_, _, err = store.Accept(ctx, *prepared, DispatchResult{StartsTurn: true})
	require.NoError(t, err)

	snapshot, err := store.Enqueue(ctx, input)
	require.NoError(t, err)
	assert.Empty(t, snapshot.Items)
	conflicting := input
	conflicting.Attachments = []Attachment{{Filename: "note.txt", MimeType: "text/plain", Data: []byte("different")}}
	_, err = store.Enqueue(ctx, conflicting)
	assert.ErrorIs(t, err, ErrConflict)
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
	_, _, err = store.Accept(ctx, *prepared, DispatchResult{StartsTurn: true})
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
	accepted, _, err := store.Accept(ctx, *prepared, DispatchResult{StartsTurn: true})
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
		_, _, err = store.Accept(ctx, *prepared, DispatchResult{StartsTurn: true})
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
	_, _, err = store.Accept(ctx, *active, DispatchResult{StartsTurn: true})
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
		VALUES ('turn-end', 'agent-1', ?, ?, '{}', 0)`,
		turnEndSeq, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT)
	require.NoError(t, err)
	_, err = store.TurnEnded(ctx, "agent-1")
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
	assert.Equal(t, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, snapshot.ActiveTurnKind)
	var transcriptCount int
	require.NoError(t, database.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE agent_id = 'agent-1'`).Scan(&transcriptCount))
	assert.Zero(t, transcriptCount)

	transcript, snapshot, err := store.Accept(ctx, *prepared, DispatchResult{StartsTurn: true, SpanLines: `[{"span_id":"tool-1"}]`})
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
	_, _, err = store.Accept(ctx, *prepared, DispatchResult{StartsTurn: true})
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

	_, store := newStoreFixture(t)
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
	retried, _, err := store.PrepareRetry(ctx, "agent-1")
	require.NoError(t, err)
	require.NotNil(t, retried)
	assert.Equal(t, prepared.ReservedSeq, retried.ReservedSeq)
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

func TestStoreRejectsUnboundedAttachmentMetadata(t *testing.T) {
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
