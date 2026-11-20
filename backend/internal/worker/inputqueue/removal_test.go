package inputqueue

import (
	"database/sql"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/quartz"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/sqltime"
	"github.com/leapmux/leapmux/internal/util/timefmt"
	"github.com/leapmux/leapmux/internal/worker/agent"
	gendb "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// removalFixtureInput is the input that the removal tests queue, remove and
// send again. It carries an attachment, so the key that a repeat compares
// covers the attachments of the removed input too.
func removalFixtureInput() NewItem {
	return NewItem{
		ID: "removed", AgentID: "agent-1",
		Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
		Text: "Run the release checklist", ReclassifyOnEdit: true,
		Attachments: []Attachment{{Filename: "notes.txt", MimeType: "text/plain", Data: []byte("release notes")}},
	}
}

// A removal keeps the row of the input, so a retry of the request that queued
// it -- EnqueueAgentInput with the same input id and the same content -- finds
// the row and queues nothing. Without the row, the retry would queue the
// message again behind the back of the user who removed it.
func TestStoreEnqueueRepeatOfARemovedInputAddsNothing(t *testing.T) {
	t.Parallel()

	_, store := newStoreFixture(t)
	ctx := t.Context()
	input := removalFixtureInput()
	_, err := store.Enqueue(ctx, input)
	require.NoError(t, err)
	removed, err := store.Delete(ctx, "agent-1", input.ID)
	require.NoError(t, err)
	require.Empty(t, removed.Items)

	repeated, receipt, err := store.enqueue(ctx, input, nil)
	require.NoError(t, err)
	assert.False(t, receipt.Added, "a repeat of a removed input adds nothing")
	assert.Zero(t, receipt.AcceptedSeq)
	assert.Empty(t, repeated.Items, "the removed input stays out of the queue")
	assert.Equal(t, removed.Revision, repeated.Revision, "a repeat changes nothing, so the revision stays")

	applied := false
	_, receipt, err = store.enqueue(ctx, input, func(*sql.Tx) error { applied = true; return nil })
	require.NoError(t, err)
	assert.False(t, receipt.Added)
	assert.True(t, applied, "a repeat still commits the caller's mutation, as for a repeat of a queued input")
}

// The same input id with other content is another request, so it conflicts
// with the removed input, as it conflicts with a queued or a delivered one.
// The removed row keeps the key of the request that queued it until the
// purge, and the key covers the attachments, so a change of an attachment
// alone conflicts too.
func TestStoreEnqueueOfARemovedInputIDWithOtherContentConflicts(t *testing.T) {
	t.Parallel()

	later := priorityBaseTime().Add(time.Hour)
	for _, c := range []struct {
		name   string
		change func(*NewItem)
	}{
		{"text", func(input *NewItem) { input.Text = "Run the linter" }},
		{"attachment data", func(input *NewItem) {
			input.Attachments = []Attachment{{Filename: "notes.txt", MimeType: "text/plain", Data: []byte("other notes")}}
		}},
		{"no attachment", func(input *NewItem) { input.Attachments = nil }},
		{"schedule", func(input *NewItem) { input.ScheduledAt = &later }},
		{"kind", func(input *NewItem) {
			input.Kind = leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_CLEAR_CONTEXT
			input.Text, input.Attachments = "/clear", nil
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			_, store := newStoreFixture(t)
			ctx := t.Context()
			input := removalFixtureInput()
			_, err := store.Enqueue(ctx, input)
			require.NoError(t, err)
			removed, err := store.Delete(ctx, "agent-1", input.ID)
			require.NoError(t, err)

			changed := removalFixtureInput()
			c.change(&changed)
			_, err = store.Enqueue(ctx, changed)
			require.ErrorIs(t, err, ErrConflict)
			after, err := store.Snapshot(ctx, "agent-1")
			require.NoError(t, err)
			assert.Equal(t, removed, after, "a refused repeat leaves the queue as the removal left it")
		})
	}
}

// The Manager reports a repeat of a removed input as no add, so a caller that
// acts once for each added input does not act, and the drain never delivers
// the input.
func TestManagerEnqueueReportingAddedReportsNothingForARemovedInput(t *testing.T) {
	t.Parallel()

	_, store := newStoreFixture(t)
	dispatcher := &recordingDispatcher{}
	manager := NewManager(store, dispatcher, &recordingObserver{})
	t.Cleanup(manager.StopAndWait)
	ctx := t.Context()
	// The running turn keeps the input in the queue until the removal.
	_, err := manager.TurnStarted(ctx, "agent-1", false)
	require.NoError(t, err)
	input := removalFixtureInput()
	_, added, err := manager.EnqueueReportingAdded(ctx, input)
	require.NoError(t, err)
	require.True(t, added)
	_, err = manager.Delete(ctx, "agent-1", input.ID)
	require.NoError(t, err)

	snapshot, added, err := manager.EnqueueReportingAdded(ctx, input)
	require.NoError(t, err)
	assert.False(t, added)
	assert.Empty(t, snapshot.Items)

	// The end of the turn drains the queue. The marker input proves that the
	// drain ran, and that it delivered nothing before it.
	_, err = manager.Enqueue(ctx, NewItem{ID: "marker", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "marker"})
	require.NoError(t, err)
	_, err = manager.TurnEnded(ctx, "agent-1")
	require.NoError(t, err)
	require.Eventually(t, func() bool { return len(dispatcher.dispatches()) == 1 }, eventuallyWait, 10*time.Millisecond)
	assert.Equal(t, []string{"marker"}, dispatcher.dispatches())
}

// removedRow is what the table holds for one removed input.
type removedRow struct {
	removedAt   sql.NullString
	order       int64
	state       leapmuxv1.AgentInputState
	editOwner   string
	reservedSeq int64
	text        string
	payloadSize int
	attachments int
}

func readRemovedRow(t *testing.T, database *sql.DB, id string) removedRow {
	t.Helper()
	var row removedRow
	require.NoError(t, database.QueryRowContext(t.Context(), `
		SELECT CAST(removed_at AS TEXT), order_index, state, edit_owner, reserved_seq, text, length(source_payload),
			(SELECT COUNT(*) FROM agent_input_queue_attachments WHERE item_id = i.id)
		FROM agent_input_queue_items i WHERE id = ?`, id).
		Scan(&row.removedAt, &row.order, &row.state, &row.editOwner, &row.reservedSeq, &row.text, &row.payloadSize, &row.attachments))
	return row
}

// A removal stamps removed_at from the store's clock, in the canonical layout
// that the purge compares. The row keeps the key of the request that queued
// it for the repeat check. It gives up what only an input that the queue holds
// can own -- its edit and its reserved transcript sequence -- and its content:
// nothing reads the text, the source payload or the attachments of a removed
// input, and the attachments alone can hold MaxQueueAttachmentBytes until the
// purge. A second removal finds no input.
func TestStoreDeleteKeepsTheRowOfARemovedInput(t *testing.T) {
	t.Parallel()

	clock, database, store := newPriorityFixture(t)
	ctx := t.Context()
	input := removalFixtureInput()
	_, err := store.Enqueue(ctx, input)
	require.NoError(t, err)
	_, _, _, err = store.BeginEdit(ctx, "agent-1", input.ID, "editor", false)
	require.NoError(t, err)
	// An automatic retry carries a source payload; the row gets one by hand.
	_, err = database.ExecContext(ctx, `UPDATE agent_input_queue_items SET reserved_seq = 7, source_payload = X'0102' WHERE id = ?`, input.ID)
	require.NoError(t, err)
	clock.Advance(1500 * time.Millisecond).MustWait(ctx)

	_, err = store.Delete(ctx, "agent-1", input.ID)
	require.NoError(t, err)
	row := readRemovedRow(t, database, input.ID)
	assert.Equal(t, sql.NullString{String: timefmt.Format(clock.Now()), Valid: true}, row.removedAt)
	assert.Empty(t, row.editOwner, "a removed input holds no edit")
	assert.Zero(t, row.reservedSeq, "a removed input reserves no transcript sequence")
	assert.Empty(t, row.text, "a removed input keeps no text")
	assert.Zero(t, row.payloadSize, "a removed input keeps no source payload")
	assert.Zero(t, row.attachments, "a removed input keeps no attachment")
	assert.Equal(t, inputIdempotencyKey(input), queuedRequestKeyOf(t, database, input.ID), "the repeat check compares the key")

	_, err = store.Delete(ctx, "agent-1", input.ID)
	require.ErrorIs(t, err, ErrNotFound)
	assert.Equal(t, row, readRemovedRow(t, database, input.ID), "a second removal changes nothing")
}

// removalFixture is a queue that holds "head" and "next", and the row of
// "ghost", an input with an attachment that someone removed before them. The
// removal freed the first slot, and "head" took it while "ghost" keeps it, so
// a write that did not skip the removed row would collide with it.
type removalFixture struct {
	database *sql.DB
	store    *Store
	clock    *quartz.Mock
}

func newRemovalFixture(t *testing.T) removalFixture {
	t.Helper()
	clock, database, store := newPriorityFixture(t)
	ctx := t.Context()
	ghost := removalFixtureInput()
	ghost.ID = "ghost"
	_, err := store.Enqueue(ctx, ghost)
	require.NoError(t, err)
	for _, id := range []string{"head", "next"} {
		_, err := store.Enqueue(ctx, NewItem{ID: id, AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: id})
		require.NoError(t, err)
	}
	snapshot, err := store.Delete(ctx, "agent-1", "ghost")
	require.NoError(t, err)
	require.Equal(t, []string{"head", "next"}, queueSnapshotIDs(snapshot))
	require.Equal(t, int64(1), snapshot.Items[0].Order)
	require.Equal(t, int64(1), readRemovedRow(t, database, "ghost").order, "the removed row keeps the slot that the head took")
	return removalFixture{database: database, store: store, clock: clock}
}

// shapeGhost writes columns of the removed row, so that a read that found the
// row would answer differently: a state, an edit or a schedule.
func (f removalFixture) shapeGhost(t *testing.T, assignments string, args ...any) {
	t.Helper()
	result, err := f.database.ExecContext(t.Context(),
		`UPDATE agent_input_queue_items SET `+assignments+` WHERE id = 'ghost' AND removed_at IS NOT NULL`, args...)
	require.NoError(t, err)
	rows, err := result.RowsAffected()
	require.NoError(t, err)
	require.Equal(t, int64(1), rows)
}

func (f removalFixture) snapshot(t *testing.T) Snapshot {
	t.Helper()
	snapshot, err := f.store.Snapshot(t.Context(), "agent-1")
	require.NoError(t, err)
	return snapshot
}

// assertQueueOrder asserts the inputs of the queue in effective order, and
// that their raw slots are 1..n.
func assertQueueOrder(t *testing.T, snapshot Snapshot, ids ...string) {
	t.Helper()
	assert.Equal(t, ids, queueSnapshotIDs(snapshot))
	for i, item := range snapshot.Items {
		assert.Equal(t, int64(i+1), item.Order, "input %s holds slot %d", item.ID, item.Order)
		assert.Equal(t, int64(i+1), item.Position, "input %s holds position %d", item.ID, item.Position)
	}
}

// Every read and write of the queue skips a removed row, whatever the row
// holds. Each case gives the row of "ghost" a shape that would change the
// answer of one path if that path found it, and asserts the answer without
// it.
func TestStoreReadsAndWritesSkipARemovedInput(t *testing.T) {
	t.Parallel()

	ghostRow := func(t *testing.T, f removalFixture) removedRow {
		t.Helper()
		return readRemovedRow(t, f.database, "ghost")
	}
	for _, c := range []struct {
		name string
		run  func(t *testing.T, f removalFixture)
	}{
		{"the snapshot lists neither the input nor its attachments", func(t *testing.T, f removalFixture) {
			snapshot := f.snapshot(t)
			assertQueueOrder(t, snapshot, "head", "next")
			for _, item := range snapshot.Items {
				assert.Empty(t, item.Metadata, "input %s carries no attachment of the removed input", item.ID)
			}
		}},
		{"the snapshot arms no deadline of a removed future input", func(t *testing.T, f removalFixture) {
			deadline := priorityBaseTime().Add(time.Hour)
			f.shapeGhost(t, "scheduled_at = ?", sqltime.SQLiteNullTimeOf(deadline))
			assert.True(t, f.snapshot(t).NextDeadline.IsZero())
			f.clock.Set(deadline).MustWait(t.Context())
			_, due, err := f.store.MatureScheduled(t.Context(), "agent-1", deadline)
			require.NoError(t, err)
			assert.False(t, due, "no input of the queue holds the deadline")
		}},
		{"the item limit counts the inputs that the queue holds", func(t *testing.T, f removalFixture) {
			for i := 3; i < MaxItems; i++ {
				_, err := f.database.ExecContext(t.Context(), `
					INSERT INTO agent_input_queue_items (id, agent_id, order_index, kind, text, idempotency_key)
					VALUES (?, 'agent-1', ?, 1, 'queued', ?)`, fmt.Sprintf("filler-%d", i), i, fmt.Sprintf("key-%d", i))
				require.NoError(t, err)
			}
			snapshot, err := f.store.Enqueue(t.Context(), NewItem{ID: "last", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "last"})
			require.NoError(t, err, "the queue holds %d inputs, and the removed one does not count", MaxItems-1)
			assert.Len(t, snapshot.Items, MaxItems)
			_, err = f.store.Enqueue(t.Context(), NewItem{ID: "overflow", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "overflow"})
			assert.ErrorIs(t, err, ErrQueueFull, "the limit still holds for the inputs that the queue holds")
		}},
		{"the attachment limit counts the inputs that the queue holds", func(t *testing.T, f removalFixture) {
			// A removal drops the attachments, so the row of "ghost" gets one
			// that fills the limit by hand.
			_, err := f.database.ExecContext(t.Context(),
				`INSERT INTO agent_input_queue_attachments (item_id, position, filename, mime_type, data) VALUES ('ghost', 0, 'full.bin', '', zeroblob(?))`, MaxQueueAttachmentBytes)
			require.NoError(t, err)
			_, err = f.store.Enqueue(t.Context(), NewItem{
				ID: "attached", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
				Attachments: []Attachment{{Filename: "one.bin", Data: []byte{1}}},
			})
			assert.NoError(t, err)
		}},
		{"a new input and a move take slots beside the removed row", func(t *testing.T, f removalFixture) {
			// The highest slot, so a new input that counted it would take the
			// slot after it.
			f.shapeGhost(t, "order_index = 50")
			_, err := f.store.Enqueue(t.Context(), NewItem{ID: "last", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "last"})
			require.NoError(t, err)
			moved, err := f.store.Move(t.Context(), "agent-1", "last", "head")
			require.NoError(t, err)
			assertQueueOrder(t, moved, "last", "head", "next")
			assert.Equal(t, int64(50), ghostRow(t, f).order, "no write moved the removed row")
		}},
		{"a control reply takes its place among the inputs that the queue holds", func(t *testing.T, f removalFixture) {
			snapshot, err := f.store.Enqueue(t.Context(), NewItem{ID: "feedback", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_CONTROL_FEEDBACK, Text: "Use the cache."})
			require.NoError(t, err)
			assertQueueOrder(t, snapshot, "feedback", "head", "next")
		}},
		{"a dispatch takes the head that the queue holds", func(t *testing.T, f removalFixture) {
			prepared, _, err := f.store.PrepareDispatch(t.Context(), "agent-1", false)
			require.NoError(t, err)
			require.NotNil(t, prepared)
			assert.Equal(t, "head", prepared.Item.ID)
		}},
		{"a dispatch waits for no echo of a removed steer", func(t *testing.T, f removalFixture) {
			f.shapeGhost(t, "state = ?", leapmuxv1.AgentInputState_AGENT_INPUT_STATE_AWAITING_ECHO)
			prepared, _, err := f.store.PrepareDispatch(t.Context(), "agent-1", false)
			require.NoError(t, err)
			require.NotNil(t, prepared, "a removed row awaits no echo")
			assert.Equal(t, "head", prepared.Item.ID)
		}},
		{"the echo paths find no removed steer", func(t *testing.T, f removalFixture) {
			f.shapeGhost(t, "state = ?", leapmuxv1.AgentInputState_AGENT_INPUT_STATE_AWAITING_ECHO)
			ctx := t.Context()
			accepted, _, _, err := f.store.AcceptEchoedSteers(ctx, "agent-1", 1, "", "")
			require.NoError(t, err)
			assert.Empty(t, accepted)
			_, requeued, err := f.store.RequeueUnechoedSteers(ctx, "agent-1", -1)
			require.NoError(t, err)
			assert.Zero(t, requeued)
			snapshot, resolved, _, err := f.store.ResolveUnechoedSteers(ctx, "agent-1", -1, "")
			require.NoError(t, err)
			assert.Zero(t, resolved)
			assert.False(t, snapshot.Paused)
			assert.Equal(t, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_AWAITING_ECHO, ghostRow(t, f).state)
		}},
		{"the dispatch outcomes find no removed input", func(t *testing.T, f removalFixture) {
			f.shapeGhost(t, "state = ?", leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DISPATCHING)
			ctx := t.Context()
			_, err := f.store.FailDispatch(ctx, "agent-1", "ghost", assert.AnError, false)
			assert.ErrorIs(t, err, ErrConflict)
			_, err = f.store.RequeuePrepared(ctx, "agent-1", "ghost")
			assert.ErrorIs(t, err, ErrConflict)
			_, err = f.store.RequeueAndPause(ctx, "agent-1", "ghost", assert.AnError,
				leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_AGENT_STOPPED)
			assert.ErrorIs(t, err, ErrConflict)
			assert.Equal(t, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DISPATCHING, ghostRow(t, f).state)
		}},
		{"a recovery finds no removed delivery to doubt", func(t *testing.T, f removalFixture) {
			f.shapeGhost(t, "state = ?", leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DISPATCHING)
			snapshots, err := f.store.Recover(t.Context())
			require.NoError(t, err)
			require.Len(t, snapshots, 1)
			assert.False(t, snapshots[0].Paused, "no input of the queue was in flight")
			assert.Equal(t, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DISPATCHING, ghostRow(t, f).state)
		}},
		{"a removed input holds no edit that another edit waits for", func(t *testing.T, f removalFixture) {
			_, err := f.database.ExecContext(t.Context(),
				`UPDATE agent_input_queue_items SET edit_owner = 'other-client' WHERE id = 'ghost' AND removed_at IS NOT NULL`)
			require.ErrorContains(t, err, "CHECK constraint failed: removed_at IS NULL OR edit_owner = ''",
				"the schema refuses an editor on a removed input")
			assert.Empty(t, readRemovedRow(t, f.database, "ghost").editOwner)
			_, _, _, err = f.store.BeginEdit(t.Context(), "agent-1", "head", "client", false)
			assert.NoError(t, err)
		}},
		{"the edit paths find no removed input", func(t *testing.T, f removalFixture) {
			// A path that found the removed row would answer with something
			// other than ErrNotFound: a kind, or an edit owner mismatch.
			ctx := t.Context()
			_, _, _, err := f.store.BeginEdit(ctx, "agent-1", "ghost", "client", true)
			assert.ErrorIs(t, err, ErrNotFound)
			_, err = f.store.KindAfterEdit(ctx, "agent-1", "ghost", "/clear")
			assert.ErrorIs(t, err, ErrNotFound)
			_, err = f.store.Update(ctx, UpdateRequest{AgentID: "agent-1", InputID: "ghost", ClientID: "client", ExpectedVersion: 1, Text: "edited"})
			assert.ErrorIs(t, err, ErrNotFound)
			_, err = f.store.CancelEdit(ctx, "agent-1", "ghost", "client")
			assert.ErrorIs(t, err, ErrNotFound)
		}},
		{"a move, a removal and a retry find no removed input", func(t *testing.T, f removalFixture) {
			ctx := t.Context()
			_, err := f.store.Move(ctx, "agent-1", "ghost", "")
			assert.ErrorIs(t, err, ErrNotFound)
			_, err = f.store.Move(ctx, "agent-1", "next", "ghost")
			assert.ErrorIs(t, err, ErrNotFound)
			_, err = f.store.Delete(ctx, "agent-1", "ghost")
			assert.ErrorIs(t, err, ErrNotFound)
			f.shapeGhost(t, "state = ?", leapmuxv1.AgentInputState_AGENT_INPUT_STATE_FAILED)
			_, err = f.store.Retry(ctx, "agent-1", "ghost", false)
			assert.ErrorIs(t, err, ErrNotHead)
		}},
		{"a steer finds no removed input", func(t *testing.T, f removalFixture) {
			_, _, err := f.store.TurnStarted(t.Context(), "agent-1", true)
			require.NoError(t, err)
			_, _, err = f.store.PrepareSteer(t.Context(), "agent-1", "ghost", false)
			assert.ErrorIs(t, err, ErrNotHead)
		}},
		{"an automatic retry ignores a removed pending retry of its kind", func(t *testing.T, f removalFixture) {
			serverError := leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR
			f.shapeGhost(t, "kind = ?, text = 'Continue.', reclassify_on_edit = 0", serverError)
			ctx := t.Context()
			snapshot, err := f.store.scheduleAutoRetry(ctx, "agent-1", agent.AutoRetrySchedule{Kind: serverError})
			require.NoError(t, err)
			require.Len(t, snapshot.Items, 3, "a new pending retry joins beside the removed one")
			_, cancelled, err := f.store.cancelAutoRetries(ctx, "agent-1", serverError)
			require.NoError(t, err)
			assert.True(t, cancelled)
			assertQueueOrder(t, f.snapshot(t), "head", "next")
			assert.True(t, ghostRow(t, f).removedAt.Valid, "the cancellation leaves the removed row to the purge")
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			c.run(t, newRemovalFixture(t))
		})
	}
}

// A preemption addresses the head of the queue, and a removed input is never
// the head, so a preemption of one interrupts nothing.
func TestManagerPreemptFindsNoRemovedInput(t *testing.T) {
	t.Parallel()

	f := newRemovalFixture(t)
	dispatcher := &recordingDispatcher{preemption: true}
	manager := NewManager(f.store, dispatcher, &recordingObserver{})
	t.Cleanup(manager.StopAndWait)
	_, err := manager.TurnStarted(t.Context(), "agent-1", false)
	require.NoError(t, err)

	_, err = manager.Preempt(t.Context(), "agent-1", "ghost")
	require.ErrorIs(t, err, ErrNotHead)
	assert.Empty(t, dispatcher.interrupts())
}

// insertRemovedRows writes count rows of removed inputs of agentID, each
// removed at removedAt.
func insertRemovedRows(t *testing.T, database *sql.DB, agentID string, count int, removedAt time.Time) {
	t.Helper()
	tx, err := database.BeginTx(t.Context(), nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	for i := range count {
		id := fmt.Sprintf("%s-removed-%d", agentID, i)
		_, err := tx.ExecContext(t.Context(), `
			INSERT INTO agent_input_queue_items (id, agent_id, order_index, kind, text, idempotency_key, removed_at)
			VALUES (?, ?, ?, 1, 'removed', ?, ?)`,
			id, agentID, i+1, "key-"+id, sqltime.SQLiteNullTimeOf(removedAt))
		require.NoError(t, err)
	}
	require.NoError(t, tx.Commit())
}

func purgeRemoved(t *testing.T, store *Store) int64 {
	t.Helper()
	result, err := store.PurgeRemoved(t.Context())
	require.NoError(t, err)
	purged, err := result.RowsAffected()
	require.NoError(t, err)
	return purged
}

func countRows(t *testing.T, database *sql.DB, table, where string, args ...any) int {
	t.Helper()
	var count int
	require.NoError(t, database.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM `+table+` WHERE `+where, args...).Scan(&count))
	return count
}

// The purge keeps a removed row until RemovedInputRetention passed since the
// removal, to the millisecond, and deletes it after. Until then a retry of
// the request queues nothing. The attachments go with the row, and a retry
// after the purge queues the input as a new one.
func TestStorePurgeRemovedDeletesARowOnlyAfterTheRetention(t *testing.T) {
	t.Parallel()

	clock, database, store := newPriorityFixture(t)
	ctx := t.Context()
	input := removalFixtureInput()
	_, err := store.Enqueue(ctx, input)
	require.NoError(t, err)
	removedAt := clock.Now()
	_, err = store.Delete(ctx, "agent-1", input.ID)
	require.NoError(t, err)

	for _, offset := range []time.Duration{RemovedInputRetention - time.Millisecond, RemovedInputRetention} {
		clock.Set(removedAt.Add(offset)).MustWait(ctx)
		assert.Zero(t, purgeRemoved(t, store), "a row removed %s ago stays", offset)
		_, receipt, err := store.enqueue(ctx, input, nil)
		require.NoError(t, err)
		assert.False(t, receipt.Added, "a retry %s after the removal queues nothing", offset)
	}

	clock.Set(removedAt.Add(RemovedInputRetention + time.Millisecond)).MustWait(ctx)
	assert.Equal(t, int64(1), purgeRemoved(t, store))
	assert.Zero(t, countRows(t, database, "agent_input_queue_items", "id = ?", input.ID))
	assert.Zero(t, countRows(t, database, "agent_input_queue_attachments", "item_id = ?", input.ID),
		"the attachments of the row go with it")
	assert.Zero(t, purgeRemoved(t, store), "a second pass finds nothing")

	snapshot, receipt, err := store.enqueue(ctx, input, nil)
	require.NoError(t, err)
	assert.True(t, receipt.Added, "after the purge, the input is a new one")
	assert.Equal(t, []string{input.ID}, queueSnapshotIDs(snapshot))
}

// The purge deletes removed rows alone: an input that the queue holds stays,
// however old it is.
func TestStorePurgeRemovedKeepsTheInputsThatTheQueueHolds(t *testing.T) {
	t.Parallel()

	clock, _, store := newPriorityFixture(t)
	ctx := t.Context()
	for _, id := range []string{"kept", "removed"} {
		_, err := store.Enqueue(ctx, NewItem{ID: id, AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: id})
		require.NoError(t, err)
	}
	_, err := store.Delete(ctx, "agent-1", "removed")
	require.NoError(t, err)

	clock.Advance(10 * RemovedInputRetention).MustWait(ctx)
	assert.Equal(t, int64(1), purgeRemoved(t, store))
	snapshot, err := store.Snapshot(ctx, "agent-1")
	require.NoError(t, err)
	assertQueueOrder(t, snapshot, "kept")
}

// One call deletes at most removedInputPurgeBatch rows, across every agent,
// so one call holds the write lock briefly. The cleanup loop calls again
// until a call deletes nothing.
func TestStorePurgeRemovedDeletesOneBatchPerCall(t *testing.T) {
	t.Parallel()

	clock, database, store := newPriorityFixture(t)
	require.NoError(t, gendb.New(database).CreateAgent(t.Context(), gendb.CreateAgentParams{
		ID: "agent-2", WorkingDir: t.TempDir(), AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))
	old := clock.Now().Add(-RemovedInputRetention - time.Millisecond)
	insertRemovedRows(t, database, "agent-1", removedInputPurgeBatch, old)
	insertRemovedRows(t, database, "agent-2", 1, old)

	assert.Equal(t, int64(removedInputPurgeBatch), purgeRemoved(t, store))
	assert.Equal(t, int64(1), purgeRemoved(t, store))
	assert.Zero(t, purgeRemoved(t, store))
	assert.Zero(t, countRows(t, database, "agent_input_queue_items", "removed_at IS NOT NULL"))
}

// The live reads of the queue and the purge carry the terms of the partial
// indexes that serve them. SQLite matches a partial index syntactically, so a
// statement without the term reads and sorts the whole table, with no error.
func TestQueueStatementsUseThePartialIndexes(t *testing.T) {
	t.Parallel()

	database := newQueueDatabase(t)
	plan := func(statement string, args ...any) string {
		rows, err := database.QueryContext(t.Context(), "EXPLAIN QUERY PLAN "+statement, args...)
		require.NoError(t, err)
		defer func() { require.NoError(t, rows.Close()) }()
		var details []string
		for rows.Next() {
			var id, parent, unused int
			var detail string
			require.NoError(t, rows.Scan(&id, &parent, &unused, &detail))
			details = append(details, detail)
		}
		require.NoError(t, rows.Err())
		return strings.Join(details, "\n")
	}

	ordering := plan(orderingRowsQuery, "agent-1")
	assert.Regexp(t, `USING INDEX idx_agent_input_queue_(order|pending) \(agent_id=\?\)`, ordering)
	assert.NotContains(t, ordering, "TEMP B-TREE", "the index already holds the order")

	purge := plan(purgeRemovedStatement, "2026-01-01T00:00:00.000Z", removedInputPurgeBatch)
	assert.Contains(t, purge, "USING COVERING INDEX idx_agent_input_queue_removed (removed_at<?)")
}

// queueTable is the table whose every statement must say whether it sees a
// removed input.
const queueTable = "agent_input_queue_items"

// sqlcStatementMarker matches the line that opens each statement of a sqlc
// query file.
var sqlcStatementMarker = regexp.MustCompile(`(?m)^--\s*name:\s*\w+`)

// Every statement of the Worker that reads or writes the queue table states
// removed_at, so none can see a removed input by omission. A live statement
// carries `removed_at IS NULL`; the repeat check and the purge state the
// removed rows that they want. An INSERT creates an input that the queue
// holds, so it needs no term.
//
// The sweep reads every Go string literal of the Worker outside the tests and
// the generated code, and every sqlc statement of the Worker. A statement
// that a Go literal splits keeps the term in the literal that holds the table.
func TestEveryQueueStatementStatesRemovedAt(t *testing.T) {
	t.Parallel()

	checked := 0
	check := func(where, statement string) {
		if !strings.Contains(statement, queueTable) {
			return
		}
		if strings.HasPrefix(strings.TrimSpace(statement), "INSERT INTO "+queueTable) {
			return
		}
		checked++
		assert.Containsf(t, statement, "removed_at",
			"%s reads or writes %s without a removed_at term, so it sees an input that someone removed:\n%s",
			where, queueTable, statement)
	}

	worker, err := filepath.Abs("..")
	require.NoError(t, err)
	fset := token.NewFileSet()
	require.NoError(t, filepath.WalkDir(worker, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == "generated" || entry.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil || !strings.Contains(string(content), queueTable) {
			return err
		}
		file, err := parser.ParseFile(fset, path, content, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(node ast.Node) bool {
			literal, ok := node.(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				return true
			}
			value, err := strconv.Unquote(literal.Value)
			require.NoError(t, err)
			check(fset.Position(literal.Pos()).String(), value)
			return true
		})
		return nil
	}))

	queries, err := filepath.Glob(filepath.Join(worker, "db", "queries", "*.sql"))
	require.NoError(t, err)
	require.NotEmpty(t, queries, "the Worker declares sqlc queries; a scan that finds none is broken")
	for _, path := range queries {
		content, err := os.ReadFile(path)
		require.NoError(t, err)
		text := string(content)
		markers := sqlcStatementMarker.FindAllStringIndex(text, -1)
		for i, marker := range markers {
			end := len(text)
			if i+1 < len(markers) {
				end = markers[i+1][0]
			}
			check(filepath.Base(path), text[marker[0]:end])
		}
	}
	require.Greater(t, checked, 40, "the sweep found too few statements of the queue; the scan is broken")
}
