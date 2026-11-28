package inputqueue

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"modernc.org/sqlite"
	sqlitelib "modernc.org/sqlite/lib"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	"github.com/leapmux/leapmux/internal/util/sqltime"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	workerdb "github.com/leapmux/leapmux/internal/worker/db"
	"github.com/leapmux/leapmux/internal/worker/db/dbtest"
	gendb "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// newQueueDatabase opens a migrated worker database that holds one open
// agent, "agent-1". Every queue fixture starts from it.
func newQueueDatabase(t *testing.T) *sql.DB {
	t.Helper()
	database, err := workerdb.Open(filepath.Join(t.TempDir(), "queue.sqlite"), sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = database.Close() })
	require.NoError(t, workerdb.Migrate(context.Background(), database))
	require.NoError(t, gendb.New(database).CreateAgent(context.Background(), gendb.CreateAgentParams{
		ID: "agent-1", WorkingDir: t.TempDir(),
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
	}))
	return database
}

func newStoreFixture(t *testing.T) (*sql.DB, *Store) {
	t.Helper()
	database := newQueueDatabase(t)
	return database, NewStore(database, nil)
}

func TestStoreAcceptKeepsTheProviderSteeringStateReportedDuringDispatch(t *testing.T) {
	for _, steerable := range []bool{false, true} {
		t.Run(fmt.Sprint(steerable), func(t *testing.T) {
			_, store := newStoreFixture(t)
			ctx := t.Context()
			for _, id := range []string{"first", "next"} {
				_, err := store.Enqueue(ctx, NewItem{ID: id, AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: id})
				require.NoError(t, err)
			}
			prepared, _, err := store.PrepareDispatch(ctx, "agent-1", false)
			require.NoError(t, err)
			require.NotNil(t, prepared)
			reported, changed, err := store.TurnStarted(ctx, "agent-1", steerable)
			require.NoError(t, err)
			assert.True(t, changed, "the provider report replaces provisional dispatch ownership")
			repeated, changed, err := store.TurnStarted(ctx, "agent-1", steerable)
			require.NoError(t, err)
			assert.False(t, changed)
			assert.Equal(t, reported.Revision, repeated.Revision)
			_, accepted, err := store.Accept(ctx, *prepared, DispatchResult{StartsTurn: true, TurnSteerable: !steerable})
			require.NoError(t, err)
			assert.True(t, accepted.ActiveTurn)
			assert.Equal(t, steerable, accepted.ActiveTurnSteerable)
			require.Len(t, accepted.Items, 1)
			assert.Equal(t, steerable, accepted.Items[0].CanSteer)
		})
	}
}

func prepareEchoSteer(t *testing.T, store *Store, id string) PreparedDispatch {
	t.Helper()
	_, _, err := store.TurnStarted(t.Context(), "agent-1", true)
	require.NoError(t, err)
	_, err = store.Enqueue(t.Context(), NewItem{ID: id, AgentID: "agent-1", Text: id,
		Kind:        leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
		Attachments: []Attachment{{Filename: "note.txt", MimeType: "text/plain", Data: []byte("keep this attachment")}},
	})
	require.NoError(t, err)
	prepared, _, err := store.PrepareSteer(t.Context(), "agent-1", id, true)
	require.NoError(t, err)
	require.NotNil(t, prepared)
	return *prepared
}

func holdEchoSteer(t *testing.T, store *Store, id string) {
	t.Helper()
	prepared := prepareEchoSteer(t, store, id)
	_, err := store.HoldForEcho(t.Context(), prepared)
	require.NoError(t, err)
}

func TestStoreFailDispatchRefusesAnItemThatIsNotDispatching(t *testing.T) {
	t.Parallel()
	for _, uncertain := range []bool{false, true} {
		t.Run(fmt.Sprint(uncertain), func(t *testing.T) {
			assertDispatchFailureStateGuard(t, func(store *Store, id string) error {
				_, err := store.FailDispatch(t.Context(), "agent-1", id, assert.AnError, uncertain)
				return err
			})
		})
	}
}

func TestStoreRequeueAndPauseRefusesAnItemThatIsNotDispatching(t *testing.T) {
	t.Parallel()
	assertDispatchFailureStateGuard(t, func(store *Store, id string) error {
		_, err := store.RequeueAndPause(t.Context(), "agent-1", id, assert.AnError,
			leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_AGENT_STOPPED)
		return err
	})
}

func assertDispatchFailureStateGuard(t *testing.T, mutate func(*Store, string) error) {
	t.Helper()
	for _, state := range []leapmuxv1.AgentInputState{
		leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED,
		leapmuxv1.AgentInputState_AGENT_INPUT_STATE_FAILED,
		leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DELIVERY_UNCERTAIN,
		leapmuxv1.AgentInputState_AGENT_INPUT_STATE_AWAITING_ECHO,
	} {
		t.Run(state.String(), func(t *testing.T) {
			database, store := newStoreFixture(t)
			holdEchoSteer(t, store, "steer")
			_, err := database.ExecContext(t.Context(), `UPDATE agent_input_queue_items SET state = ? WHERE id = ?`, state, "steer")
			require.NoError(t, err)
			before, err := store.Snapshot(t.Context(), "agent-1")
			require.NoError(t, err)
			assert.ErrorIs(t, mutate(store, "steer"), ErrConflict)
			after, err := store.Snapshot(t.Context(), "agent-1")
			require.NoError(t, err)
			assert.Equal(t, before, after)
			assert.ErrorIs(t, mutate(store, "missing"), ErrConflict)
			after, err = store.Snapshot(t.Context(), "agent-1")
			require.NoError(t, err)
			assert.Equal(t, before, after)
		})
	}
}

func queueSnapshotIDs(snapshot Snapshot) []string {
	var ids []string
	for _, item := range snapshot.Items {
		ids = append(ids, item.ID)
	}
	return ids
}

// A refused steer states its own reason. "active turn ended before steering"
// is the answer only when no turn runs.
func TestStorePrepareSteerStatesWhyItRefuses(t *testing.T) {
	t.Parallel()
	_, store := newStoreFixture(t)
	ctx := t.Context()
	_, err := store.Enqueue(ctx, NewItem{ID: "head", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "head"})
	require.NoError(t, err)

	prepared, _, err := store.PrepareSteer(ctx, "agent-1", "head", false)
	assert.ErrorIs(t, err, ErrTurnEnded, "no turn runs")
	assert.Nil(t, prepared)

	_, _, err = store.TurnStarted(ctx, "agent-1", true)
	require.NoError(t, err)
	_, _, _, err = store.BeginEdit(ctx, "agent-1", "head", "other-client", false)
	require.NoError(t, err)
	prepared, _, err = store.PrepareSteer(ctx, "agent-1", "head", false)
	assert.ErrorIs(t, err, ErrSteeringState, "another client edits the head while the turn runs")
	assert.NotErrorIs(t, err, ErrTurnEnded)
	assert.Nil(t, prepared)
}

func TestStorePrepareSteerForEchoReservesNoSequence(t *testing.T) {
	t.Parallel()
	database, store := newStoreFixture(t)
	prepared := prepareEchoSteer(t, store, "steer")
	assert.Zero(t, prepared.ReservedSeq)
	var sequence int64
	require.NoError(t, database.QueryRowContext(t.Context(), `SELECT message_seq_hwm FROM agents WHERE id = ?`, "agent-1").Scan(&sequence))
	assert.Zero(t, sequence)
	_, _, err := store.Accept(t.Context(), prepared, DispatchResult{
		StartsTurn: true,
		Steering:   true,
		NativeReceipt: agent.NativeInputReceipt{
			SessionID: "s1",
			InputID:   "i1",
			TurnID:    "t1",
		},
	})
	assert.ErrorIs(t, err, ErrConflict)
	require.NoError(t, database.QueryRowContext(t.Context(), `SELECT message_seq_hwm FROM agents WHERE id = ?`, "agent-1").Scan(&sequence))
	assert.Zero(t, sequence)
	var msgCount int
	require.NoError(t, database.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM messages`).Scan(&msgCount))
	assert.Zero(t, msgCount)
	var bindingCount int
	require.NoError(t, database.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM native_agent_inputs`).Scan(&bindingCount))
	assert.Zero(t, bindingCount)
	item, _, found, err := store.getItem(t.Context(), database, "agent-1", "steer", false)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DISPATCHING, item.State)
}

func TestStoreAcceptRejectsNonDispatchingItemAndNegativeReservedSeq(t *testing.T) {
	t.Parallel()

	t.Run("non-dispatching item", func(t *testing.T) {
		t.Parallel()
		for _, state := range []leapmuxv1.AgentInputState{
			leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED,
			leapmuxv1.AgentInputState_AGENT_INPUT_STATE_FAILED,
			leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DELIVERY_UNCERTAIN,
			leapmuxv1.AgentInputState_AGENT_INPUT_STATE_AWAITING_ECHO,
		} {
			t.Run(state.String(), func(t *testing.T) {
				t.Parallel()
				database, store := newStoreFixture(t)
				_, err := store.Enqueue(t.Context(), NewItem{
					ID: "in-1", AgentID: "agent-1",
					Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "hello",
				})
				require.NoError(t, err)
				prepared, _, err := store.PrepareDispatch(t.Context(), "agent-1", false)
				require.NoError(t, err)
				require.NotNil(t, prepared)
				_, err = database.ExecContext(t.Context(), `UPDATE agent_input_queue_items SET state = ? WHERE id = ?`, state, "in-1")
				require.NoError(t, err)
				before, err := store.Snapshot(t.Context(), "agent-1")
				require.NoError(t, err)

				_, _, err = store.Accept(t.Context(), *prepared, DispatchResult{StartsTurn: true})
				assert.ErrorIs(t, err, ErrConflict)
				after, err := store.Snapshot(t.Context(), "agent-1")
				require.NoError(t, err)
				assert.Equal(t, before, after)
				var msgCount int
				require.NoError(t, database.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM messages`).Scan(&msgCount))
				assert.Zero(t, msgCount)
			})
		}
	})

	t.Run("negative ReservedSeq", func(t *testing.T) {
		t.Parallel()
		database, store := newStoreFixture(t)
		_, err := store.Enqueue(t.Context(), NewItem{
			ID: "in-1", AgentID: "agent-1",
			Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "hello",
		})
		require.NoError(t, err)
		prepared, _, err := store.PrepareDispatch(t.Context(), "agent-1", false)
		require.NoError(t, err)
		require.NotNil(t, prepared)

		negative := *prepared
		negative.ReservedSeq = -1
		_, _, err = store.Accept(t.Context(), negative, DispatchResult{StartsTurn: true})
		assert.ErrorIs(t, err, ErrConflict)

		var msgCount int
		require.NoError(t, database.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM messages`).Scan(&msgCount))
		assert.Zero(t, msgCount)
		item, _, found, err := store.getItem(t.Context(), database, "agent-1", "in-1", false)
		require.NoError(t, err)
		require.True(t, found)
		assert.Equal(t, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DISPATCHING, item.State)
	})
}

func TestStoreHoldForEchoKeepsTheItemAndWritesNoRow(t *testing.T) {
	t.Parallel()
	database, store := newStoreFixture(t)
	holdEchoSteer(t, store, "steer")
	item, attachments, found, err := store.getItem(t.Context(), database, "agent-1", "steer", true)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_AWAITING_ECHO, item.State)
	assert.Zero(t, item.ReservedSeq)
	assert.Equal(t, "steer", item.Text)
	require.Len(t, attachments, 1)
	assert.Equal(t, []byte("keep this attachment"), attachments[0].Data)
	var count int
	require.NoError(t, database.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM messages`).Scan(&count))
	assert.Zero(t, count)
}

func TestStoreAcceptEchoedSteersIsFIFOAndReservesTheSequenceAtTheEcho(t *testing.T) {
	t.Parallel()
	database, store := newStoreFixture(t)
	holdEchoSteer(t, store, "first")
	holdEchoSteer(t, store, "second")
	otherSeq, err := gendb.New(database).ReserveAgentMessageSeq(t.Context(), "agent-1")
	require.NoError(t, err)
	rows, inFlight, snapshot, err := store.AcceptEchoedSteers(t.Context(), "agent-1", 2, `[{"span_id":"parent"}]`, "")
	require.NoError(t, err)
	assert.False(t, inFlight)
	require.Len(t, rows, 2)
	assert.Equal(t, "first", rows[0].ID)
	assert.Equal(t, "second", rows[1].ID)
	assert.Equal(t, otherSeq+1, rows[0].Seq)
	assert.Equal(t, otherSeq+2, rows[1].Seq)
	assert.Equal(t, `[{"span_id":"parent"}]`, rows[0].SpanLines)
	assert.Empty(t, snapshot.Items)
	var count int
	require.NoError(t, database.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM messages`).Scan(&count))
	assert.Equal(t, 2, count)
}

func TestStoreAcceptEchoedSteersTakesTheInFlightSteerLast(t *testing.T) {
	t.Parallel()
	_, store := newStoreFixture(t)
	holdEchoSteer(t, store, "first")
	prepared := prepareEchoSteer(t, store, "second")
	rows, inFlight, snapshot, err := store.AcceptEchoedSteers(t.Context(), "agent-1", 2, "", "second")
	require.NoError(t, err)
	require.Len(t, rows, 2)
	assert.Equal(t, "first", rows[0].ID)
	assert.Equal(t, "second", rows[1].ID)
	assert.True(t, inFlight)
	assert.Empty(t, snapshot.Items)
	_, err = store.HoldForEcho(t.Context(), prepared)
	assert.ErrorIs(t, err, ErrConflict)
}

func TestStoreAcceptEchoedSteersRollsBackTheWholeBatch(t *testing.T) {
	t.Parallel()
	database, store := newStoreFixture(t)
	holdEchoSteer(t, store, "first")
	holdEchoSteer(t, store, "second")
	_, err := database.ExecContext(t.Context(), `CREATE TRIGGER refuse_second BEFORE INSERT ON messages WHEN NEW.id = 'second' BEGIN SELECT RAISE(ABORT, 'refused'); END`)
	require.NoError(t, err)
	rows, _, _, err := store.AcceptEchoedSteers(t.Context(), "agent-1", 2, "", "")
	require.ErrorContains(t, err, "refused")
	assert.Empty(t, rows)
	snapshot, err := store.Snapshot(t.Context(), "agent-1")
	require.NoError(t, err)
	assert.Equal(t, []string{"first", "second"}, queueSnapshotIDs(snapshot))
	var count int
	require.NoError(t, database.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM messages`).Scan(&count))
	assert.Zero(t, count)
}

func TestStoreRequeueUnechoedSteersPutsThemAtTheHead(t *testing.T) {
	t.Parallel()
	database, store := newStoreFixture(t)
	holdEchoSteer(t, store, "first")
	holdEchoSteer(t, store, "second")
	original, originalAttachments, found, err := store.getItem(t.Context(), database, "agent-1", "first", true)
	require.NoError(t, err)
	require.True(t, found)
	_, err = store.Enqueue(t.Context(), NewItem{ID: "last", AgentID: "agent-1", Text: "last", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE})
	require.NoError(t, err)
	snapshot, count, err := store.RequeueUnechoedSteers(t.Context(), "agent-1", 1)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
	assert.Equal(t, []string{"second", "first", "last"}, queueSnapshotIDs(snapshot))
	assert.Equal(t, 1, snapshot.HeadIndex())
	assert.True(t, snapshot.Items[1].CanSteer)
	assert.False(t, snapshot.Items[0].CanSteer)
	returned, attachments, found, err := store.getItem(t.Context(), database, "agent-1", "first", true)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, original.Text, returned.Text)
	assert.Equal(t, originalAttachments, attachments)
	var rows int
	require.NoError(t, database.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM messages`).Scan(&rows))
	assert.Zero(t, rows, "a dropped steer creates no echo row")
	snapshot, count, err = store.RequeueUnechoedSteers(t.Context(), "agent-1", -1)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
	assert.Equal(t, []string{"second", "first", "last"}, queueSnapshotIDs(snapshot))
	assert.Equal(t, 0, snapshot.HeadIndex())
}

func TestStoreEchoNoOpCountsKeepTheQueueRevision(t *testing.T) {
	t.Parallel()
	_, store := newStoreFixture(t)
	holdEchoSteer(t, store, "steer")
	before, err := store.Snapshot(t.Context(), "agent-1")
	require.NoError(t, err)
	for _, count := range []int{0, -1} {
		rows, inFlight, after, err := store.AcceptEchoedSteers(t.Context(), "agent-1", count, "", "steer")
		require.NoError(t, err)
		assert.Empty(t, rows)
		assert.False(t, inFlight)
		assert.Equal(t, before, after)
	}
	after, count, err := store.RequeueUnechoedSteers(t.Context(), "agent-1", 0)
	require.NoError(t, err)
	assert.Zero(t, count)
	assert.Equal(t, before, after)
}

func TestStoreConcurrentEchoReportsWriteTheSteerOnce(t *testing.T) {
	t.Parallel()
	database, store := newStoreFixture(t)
	holdEchoSteer(t, store, "steer")
	type result struct {
		rows []AcceptedTranscript
		err  error
	}
	done := make(chan result, 2)
	for range 2 {
		go func() {
			rows, _, _, err := store.AcceptEchoedSteers(t.Context(), "agent-1", 1000, "", "")
			done <- result{rows: rows, err: err}
		}()
	}
	accepted := 0
	for range 2 {
		result := <-done
		require.NoError(t, result.err)
		accepted += len(result.rows)
	}
	assert.Equal(t, 1, accepted)
	var count int
	require.NoError(t, database.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM messages WHERE id = 'steer'`).Scan(&count))
	assert.Equal(t, 1, count)
}

func TestStoreTurnEndRequeuesUnechoedSteersWhenAsked(t *testing.T) {
	t.Parallel()
	_, store := newStoreFixture(t)
	holdEchoSteer(t, store, "first")
	holdEchoSteer(t, store, "second")
	snapshot, changed, err := store.TurnEnded(t.Context(), "agent-1", true)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.False(t, snapshot.ActiveTurn)
	assert.Equal(t, []string{"first", "second"}, queueSnapshotIDs(snapshot))
	for _, item := range snapshot.Items {
		assert.Equal(t, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED, item.State)
	}
}

func TestStoreResolveUnechoedSteersMarksThemUncertainAndPauses(t *testing.T) {
	t.Parallel()
	_, store := newStoreFixture(t)
	holdEchoSteer(t, store, "steer")
	snapshot, changed, inFlight, err := store.ResolveUnechoedSteers(t.Context(), "agent-1", -1, "")
	require.NoError(t, err)
	assert.Equal(t, 1, changed)
	assert.False(t, inFlight)
	assert.True(t, snapshot.Paused)
	assert.False(t, snapshot.ActiveTurn)
	assert.Equal(t, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_DELIVERY_UNCERTAIN, snapshot.PauseReason)
	require.Len(t, snapshot.Items, 1)
	assert.Equal(t, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DELIVERY_UNCERTAIN, snapshot.Items[0].State)
	assert.Equal(t, "the agent did not confirm whether the model read this message", snapshot.Items[0].Error)
	again, changed, _, err := store.ResolveUnechoedSteers(t.Context(), "agent-1", -1, "")
	require.NoError(t, err)
	assert.Zero(t, changed)
	assert.Equal(t, snapshot.Revision, again.Revision)
}

func TestStoreRecoverMarksAwaitingEchoUncertain(t *testing.T) {
	t.Parallel()
	_, store := newStoreFixture(t)
	holdEchoSteer(t, store, "steer")
	snapshots, err := store.Recover(t.Context(), nil)
	require.NoError(t, err)
	require.Len(t, snapshots, 1)
	assert.True(t, snapshots[0].Paused)
	assert.Equal(t, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DELIVERY_UNCERTAIN, snapshots[0].Items[0].State)
}

func TestStoreRefusesToEditDeleteOrMoveAnAwaitingEchoItem(t *testing.T) {
	t.Parallel()
	_, store := newStoreFixture(t)
	holdEchoSteer(t, store, "steer")
	_, _, _, err := store.BeginEdit(t.Context(), "agent-1", "steer", "client", true)
	assert.ErrorIs(t, err, ErrConflict)
	_, err = store.Delete(t.Context(), "agent-1", "steer")
	assert.ErrorIs(t, err, ErrConflict)
	_, err = store.Move(t.Context(), "agent-1", "steer", "")
	assert.ErrorIs(t, err, ErrConflict)
}

func TestStoreMoveRefusesToPlaceAnItemBeforeAnAwaitingEchoItem(t *testing.T) {
	t.Parallel()
	_, store := newStoreFixture(t)
	holdEchoSteer(t, store, "first")
	holdEchoSteer(t, store, "second")
	_, err := store.Enqueue(t.Context(), NewItem{ID: "last", AgentID: "agent-1", Text: "last", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE})
	require.NoError(t, err)
	for _, target := range []string{"first", "second"} {
		_, err = store.Move(t.Context(), "agent-1", "last", target)
		assert.ErrorIs(t, err, ErrConflict)
	}
}

func TestSnapshotHeadSkipsAwaitingEcho(t *testing.T) {
	t.Parallel()
	assert.Equal(t, -1, (Snapshot{}).HeadIndex())
	snapshot := Snapshot{Items: []SnapshotItem{
		{ItemMetadata: ItemMetadata{
			OrderingMetadata: OrderingMetadata{State: leapmuxv1.AgentInputState_AGENT_INPUT_STATE_AWAITING_ECHO},
		}},
	}}
	assert.Equal(t, -1, snapshot.HeadIndex())
	snapshot.Items = append(snapshot.Items, SnapshotItem{
		ItemMetadata: ItemMetadata{OrderingMetadata: OrderingMetadata{State: leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED}},
	})
	assert.Equal(t, 1, snapshot.HeadIndex())
}

func TestStoreDrainWaitsWhileASteerAwaitsItsEcho(t *testing.T) {
	t.Parallel()
	_, store := newStoreFixture(t)
	holdEchoSteer(t, store, "steer")
	_, err := store.Enqueue(t.Context(), NewItem{ID: "next", AgentID: "agent-1", Text: "next", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE})
	require.NoError(t, err)
	_, _, err = store.TurnEnded(t.Context(), "agent-1", false)
	require.NoError(t, err)
	prepared, snapshot, err := store.PrepareDispatch(t.Context(), "agent-1", false)
	require.NoError(t, err)
	assert.Nil(t, prepared)
	assert.Equal(t, []string{"steer", "next"}, queueSnapshotIDs(snapshot))
}

func TestInitialSchemaCreatesDurableInputQueue(t *testing.T) {
	t.Parallel()

	database, _ := newStoreFixture(t)
	ctx := context.Background()
	for _, object := range []string{
		"agent_input_queue_state",
		"agent_input_queue_items",
		"agent_input_queue_removed",
		"agent_input_queue_attachments",
		"idx_agent_input_queue_order",
		"idx_agent_input_queue_removed",
	} {
		var count int
		require.NoError(t, database.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM sqlite_master WHERE name = ?`, object).Scan(&count))
		assert.Equal(t, 1, count, object)
	}
	var editIndexCount int
	require.NoError(t, database.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE name = 'idx_agent_input_queue_one_edit'`).Scan(&editIndexCount))
	assert.Zero(t, editIndexCount, "edit locks live in memory only")
	for _, col := range []string{"restarting", "active_turn_steerable", "active_input_id", "bypass_background_work_input_id"} {
		var count int
		require.NoError(t, database.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM pragma_table_info('agent_input_queue_state') WHERE name = ?`, col).Scan(&count))
		assert.Zero(t, count, "ephemeral state column %s must not exist in SQLite", col)
	}
	for _, col := range []string{"edit_owner", "reserved_seq"} {
		var count int
		require.NoError(t, database.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM pragma_table_info('agent_input_queue_items') WHERE name = ?`, col).Scan(&count))
		assert.Zero(t, count, "ephemeral item column %s must not exist in SQLite", col)
	}
	var deliveryErrorColumns, legacyInputFingerprintColumns, legacyDedupKeyColumns, idempotencyKeyColumns, providerColumns int
	require.NoError(t, database.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('messages') WHERE name = 'delivery_error'`).Scan(&deliveryErrorColumns))
	require.NoError(t, database.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('messages') WHERE name = 'input_fingerprint'`).Scan(&legacyInputFingerprintColumns))
	require.NoError(t, database.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('messages') WHERE name = 'dedup_key'`).Scan(&legacyDedupKeyColumns))
	require.NoError(t, database.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('messages') WHERE name = 'idempotency_key'`).Scan(&idempotencyKeyColumns))
	require.NoError(t, database.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('messages') WHERE name = 'agent_provider'`).Scan(&providerColumns))
	assert.Zero(t, deliveryErrorColumns)
	assert.Zero(t, legacyInputFingerprintColumns)
	assert.Zero(t, legacyDedupKeyColumns)
	assert.Equal(t, 1, idempotencyKeyColumns)
	assert.Zero(t, providerColumns, "a message reports its agent's provider; no column stores a copy")
	var idempotencyIndexSQL string
	require.NoError(t, database.QueryRowContext(ctx,
		`SELECT sql FROM sqlite_master WHERE name = 'idx_messages_idempotency_key'`).Scan(&idempotencyIndexSQL))
	assert.Contains(t, idempotencyIndexSQL,
		"ON messages(agent_id, agent_session_id, idempotency_key) WHERE idempotency_key <> ''")
	var pauseOwnerColumns, archiveMarkerColumns, pausedColumns int
	require.NoError(t, database.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('agent_input_queue_state') WHERE name = 'pause_owner'`).Scan(&pauseOwnerColumns))
	require.NoError(t, database.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('agent_input_queue_state') WHERE name = 'paused_for_archive'`).Scan(&archiveMarkerColumns))
	require.NoError(t, database.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('agent_input_queue_state') WHERE name = 'paused'`).Scan(&pausedColumns))
	assert.Equal(t, 1, pauseOwnerColumns)
	assert.Zero(t, archiveMarkerColumns, "pause_owner replaced the single-cause archive flag")
	assert.Zero(t, pausedColumns, "the queue is paused exactly when pause_reason is set; no column stores a second copy")
	// The item id is the primary key, so a composite unique over (agent_id, id)
	// can never reject a row that the primary key admits.
	var itemsTableSQL string
	require.NoError(t, database.QueryRowContext(ctx,
		`SELECT sql FROM sqlite_master WHERE name = 'agent_input_queue_items'`).Scan(&itemsTableSQL))
	assert.NotContains(t, itemsTableSQL, "UNIQUE(agent_id, id)")
	// Order uniqueness is enforced directly on agent_input_queue_items because
	// removed input receipts live in agent_input_queue_removed.
	assert.NotContains(t, itemsTableSQL, "UNIQUE(agent_id, order_index)")
	var orderIndexSQL string
	require.NoError(t, database.QueryRowContext(ctx,
		`SELECT sql FROM sqlite_master WHERE name = 'idx_agent_input_queue_order'`).Scan(&orderIndexSQL))
	assert.Contains(t, orderIndexSQL, "UNIQUE INDEX")
	assert.Contains(t, orderIndexSQL, "(agent_id, order_index)")
	assert.NotContains(t, orderIndexSQL, "removed_at")
}

func TestStorePrepareDispatchScopesTabCreationCheckByOwnerAndWorker(t *testing.T) {
	t.Parallel()

	database, store := newStoreFixture(t)
	ctx := t.Context()
	store.SetCreationScope(func() (string, string, bool) {
		return "cur-user", "cur-worker", true
	})
	_, err := store.Enqueue(ctx, NewItem{
		ID: "input-1", AgentID: "agent-1",
		Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "hello",
	})
	require.NoError(t, err)

	insertCreation := func(userID, workerID string, state leapmuxv1.TabCreationState) {
		t.Helper()
		_, err := database.ExecContext(ctx, `
			INSERT INTO tab_creations (user_id, worker_id, tab_id, tab_type, request_hash, request_payload, publication_payload, runtime_payload, state)
			VALUES (?, ?, 'agent-1', ?, ?, X'', X'', X'', ?)`,
			userID, workerID, leapmuxv1.TabType_TAB_TYPE_AGENT, make([]byte, 32), state)
		require.NoError(t, err)
	}

	// A stale REJECTED creation row from a previous Worker registration must
	// not block dispatch for the active (user_id, worker_id) registration.
	insertCreation("old-user", "old-worker", leapmuxv1.TabCreationState_TAB_CREATION_STATE_REJECTED)
	insertCreation("cur-user", "cur-worker", leapmuxv1.TabCreationState_TAB_CREATION_STATE_COMMITTED)

	prepared, _, err := store.PrepareDispatch(ctx, "agent-1", false)
	require.NoError(t, err)
	require.NotNil(t, prepared, "a stale prior-registration creation row must not block dispatch")
	assert.Equal(t, "input-1", prepared.Item.ID)

	// Requeue and flip the active registration's creation row to PENDING:
	// now dispatch must be blocked.
	_, err = store.RequeuePrepared(ctx, "agent-1", "input-1")
	require.NoError(t, err)
	_, err = database.ExecContext(ctx, `
		UPDATE tab_creations SET state = ? WHERE user_id = 'cur-user' AND worker_id = 'cur-worker' AND tab_id = 'agent-1'`,
		leapmuxv1.TabCreationState_TAB_CREATION_STATE_PENDING)
	require.NoError(t, err)

	blocked, _, err := store.PrepareDispatch(ctx, "agent-1", false)
	require.NoError(t, err)
	assert.Nil(t, blocked, "a non-committed creation row for the active owner and Worker must block dispatch")

	// When the active registration commits again, a scope returning ok=false
	// (pre-registration) or nil must fall back to the unscoped check and still
	// see the stale non-committed row.
	_, err = database.ExecContext(ctx, `
		UPDATE tab_creations SET state = ? WHERE user_id = 'cur-user' AND worker_id = 'cur-worker' AND tab_id = 'agent-1'`,
		leapmuxv1.TabCreationState_TAB_CREATION_STATE_COMMITTED)
	require.NoError(t, err)
	store.SetCreationScope(func() (string, string, bool) {
		return "", "", false
	})
	blockedPreReg, _, err := store.PrepareDispatch(ctx, "agent-1", false)
	require.NoError(t, err)
	assert.Nil(t, blockedPreReg, "an unready scope must fall back to the unscoped tab_creations check")

	store.SetCreationScope(nil)
	blockedUnscoped, _, err := store.PrepareDispatch(ctx, "agent-1", false)
	require.NoError(t, err)
	assert.Nil(t, blockedUnscoped, "a nil scope must fall back to the unscoped tab_creations check")
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

	snapshot, receipt, err := store.enqueue(ctx, input, nil)
	require.NoError(t, err)
	assert.True(t, receipt.Added)
	require.Len(t, snapshot.Items, 1)

	snapshot, receipt, err = store.enqueue(ctx, input, nil)
	require.NoError(t, err)
	assert.False(t, receipt.Added, "a repeat of a queued item adds nothing")
	assert.Len(t, snapshot.Items, 1)

	conflicting := input
	conflicting.Text = "different"
	_, receipt, err = store.enqueue(ctx, conflicting, nil)
	require.ErrorIs(t, err, ErrConflict)
	assert.False(t, receipt.Added)

	mutationFailed := errors.New("the mutation failed")
	failing := NewItem{ID: "input-2", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "second"}
	_, receipt, err = store.enqueue(ctx, failing, func(*sql.Tx) error { return mutationFailed })
	require.ErrorIs(t, err, mutationFailed)
	assert.False(t, receipt.Added, "a failed mutation rolls the insert back")
	snapshot, err = store.Snapshot(ctx, "agent-1")
	require.NoError(t, err)
	require.Len(t, snapshot.Items, 1)
	assert.Equal(t, "input-1", snapshot.Items[0].ID)

	_, receipt, err = store.enqueue(ctx, failing, nil)
	require.NoError(t, err)
	assert.True(t, receipt.Added, "the item that the failed mutation rolled back is new")
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
				prepared, _, err := store.PrepareDispatch(ctx, "agent-1", false)
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

func TestQuestionAnswerPrecedesFutureInput(t *testing.T) {
	t.Parallel()
	for _, active := range []bool{false, true} {
		t.Run(fmt.Sprint(active), func(t *testing.T) {
			t.Parallel()
			_, store := newStoreFixture(t)
			ctx := t.Context()
			if active {
				_, err := store.Enqueue(ctx, NewItem{ID: "active", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "active request"})
				require.NoError(t, err)
				prepared, _, err := store.PrepareDispatch(ctx, "agent-1", false)
				require.NoError(t, err)
				require.NotNil(t, prepared)
			}
			_, err := store.Enqueue(ctx, NewItem{ID: "future", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "future task"})
			require.NoError(t, err)
			_, err = store.Enqueue(ctx, NewItem{ID: "answer", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_QUESTION_ANSWER, Text: "Red"})
			require.NoError(t, err)
			snapshot, err := store.Snapshot(ctx, "agent-1")
			require.NoError(t, err)
			var ids []string
			for _, item := range snapshot.Items {
				ids = append(ids, item.ID)
			}
			expected := []string{"answer", "future"}
			if active {
				expected = append([]string{"active"}, expected...)
				assert.Equal(t, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DISPATCHING, snapshot.Items[0].State)
			}
			assert.Equal(t, expected, ids, "the answer is the reply to the current request and precedes future work")
		})
	}
}

// TestStoreAcceptPreservesTheAnswerAndQuestionSource checks storage and input identity.
func TestStoreAcceptPreservesTheAnswerAndQuestionSource(t *testing.T) {
	t.Parallel()

	database, store := newStoreFixture(t)
	ctx := t.Context()
	input := NewItem{
		ID: "answer-1", AgentID: "agent-1",
		Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_QUESTION_ANSWER, Text: "Red",
	}
	_, err := store.Enqueue(ctx, input)
	require.NoError(t, err)
	prepared, _, err := store.PrepareDispatch(ctx, "agent-1", false)
	require.NoError(t, err)
	require.NotNil(t, prepared)
	transcript, _, err := store.Accept(ctx, *prepared, DispatchResult{
		StartsTurn: true, TurnSteerable: true, SupplementalContent: []byte(`{"provider":{"title":"Which color should I use?"}}`),
	})
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.MarkType_MARK_TYPE_CONTROL_RESPONSE, transcript.MarkType,
		"a control reply draws a scroll-rail jump dot")
	raw, err := msgcodec.Decompress(transcript.Content, transcript.ContentCompression)
	require.NoError(t, err)
	var body map[string]any
	require.NoError(t, json.Unmarshal(raw, &body))
	assert.Equal(t, "Red", body["content"], "the row preserves the original answer")
	source, err := msgcodec.Decompress(transcript.SupplementalContent, transcript.SupplementalContentCompression)
	require.NoError(t, err)
	assert.JSONEq(t, `{"provider":{"title":"Which color should I use?"}}`, string(source))
	var storedSource []byte
	var storedCompression leapmuxv1.ContentCompression
	require.NoError(t, database.QueryRowContext(ctx, `SELECT supplemental_content, supplemental_content_compression FROM messages WHERE id = ?`, input.ID).Scan(&storedSource, &storedCompression))
	assert.Equal(t, transcript.SupplementalContent, storedSource)
	assert.Equal(t, transcript.SupplementalContentCompression, storedCompression)

	var idempotencyKey string
	require.NoError(t, database.QueryRowContext(ctx,
		`SELECT idempotency_key FROM messages WHERE id = ?`, input.ID).Scan(&idempotencyKey))
	assert.Equal(t, inputIdempotencyKey(input), idempotencyKey, "the key uses the queued item text")
	withTranscript := input
	withTranscript.Text = "> Which color should I use?\n\nRed"
	assert.NotEqual(t, inputIdempotencyKey(withTranscript), idempotencyKey,
		"the transcript text must not reach the idempotency key")
}

// A question answer is steerable like a user message: the worker injects it into
// the turn that asked the question.
func TestStorePrepareSteerTakesAQuestionAnswerHead(t *testing.T) {
	t.Parallel()

	_, store := newStoreFixture(t)
	ctx := t.Context()
	_, err := store.Enqueue(ctx, NewItem{ID: "active", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "active request"})
	require.NoError(t, err)
	prepared, _, err := store.PrepareDispatch(ctx, "agent-1", false)
	require.NoError(t, err)
	require.NotNil(t, prepared)
	_, _, err = store.Accept(ctx, *prepared, DispatchResult{StartsTurn: true, TurnSteerable: true})
	require.NoError(t, err)
	_, err = store.Enqueue(ctx, NewItem{ID: "answer", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_QUESTION_ANSWER, Text: "Red"})
	require.NoError(t, err)

	snapshot, err := store.Snapshot(ctx, "agent-1")
	require.NoError(t, err)
	require.NotEmpty(t, snapshot.Items)
	assert.True(t, snapshot.Items[0].CanSteer, "the answer can enter the running turn")

	steer, _, err := store.PrepareSteer(ctx, "agent-1", "answer", false)
	require.NoError(t, err)
	require.NotNil(t, steer)
	assert.Equal(t, "answer", steer.Item.ID)
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
	assert.Less(t, len(snapshot.Items[0].TextPreview), len(fullText))

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
	prepared, _, err := store.PrepareDispatch(ctx, "agent-1", false)
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
		prepared, _, err := store.PrepareDispatch(ctx, "agent-1", false)
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
	prepared, _, err := store.PrepareDispatch(ctx, "agent-1", false)
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
	prepared, _, err := store.PrepareDispatch(ctx, "agent-1", false)
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
	_, err = store.Update(ctx, UpdateRequest{
		AgentID:         "agent-1",
		InputID:         "one",
		ClientID:        "client-b",
		ExpectedVersion: snapshot.Items[0].Version + 1,
		Text:            "stale",
		Attachments:     nil,
		Schedule:        ReplaceSchedule(nil),
	})
	assert.ErrorIs(t, err, ErrVersionConflict)
	snapshot, err = store.Update(ctx, UpdateRequest{
		AgentID:         "agent-1",
		InputID:         "one",
		ClientID:        "client-b",
		ExpectedVersion: snapshot.Items[0].Version,
		Text:            "/summarize",
		Attachments:     nil,
		Schedule:        ReplaceSchedule(nil),
	})
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
	snapshot, err = store.Update(ctx, UpdateRequest{
		AgentID:         "agent-1",
		InputID:         "generated",
		ClientID:        "client",
		ExpectedVersion: snapshot.Items[0].Version,
		Text:            "/compact",
		Attachments:     []Attachment{{Filename: "plan.txt", Data: []byte("changed")}},
		Schedule:        ReplaceSchedule(nil),
	})
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
	prepared, _, err := store.PrepareDispatch(ctx, "agent-1", false)
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
		prepared, _, err := store.PrepareDispatch(ctx, "agent-1", false)
		require.NoError(t, err)
		_, _, err = store.Accept(ctx, *prepared, DispatchResult{StartsTurn: true, TurnSteerable: true})
		require.NoError(t, err)
		_, err = store.Enqueue(ctx, NewItem{ID: "compact", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_COMPACT_CONTEXT, Text: "/compact"})
		require.NoError(t, err)

		_, _, err = store.PrepareSteer(ctx, "agent-1", "compact", false)
		assert.ErrorIs(t, err, ErrSteeringState)
	})

	t.Run("compaction turn", func(t *testing.T) {
		_, store := newStoreFixture(t)
		ctx := context.Background()
		_, err := store.Enqueue(ctx, NewItem{ID: "compact", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_COMPACT_CONTEXT, Text: "/compact"})
		require.NoError(t, err)
		prepared, _, err := store.PrepareDispatch(ctx, "agent-1", false)
		require.NoError(t, err)
		_, _, err = store.Accept(ctx, *prepared, DispatchResult{StartsTurn: true})
		require.NoError(t, err)
		_, err = store.Enqueue(ctx, NewItem{ID: "next", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "next"})
		require.NoError(t, err)

		_, _, err = store.PrepareSteer(ctx, "agent-1", "next", false)
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
	active, _, err := store.PrepareDispatch(ctx, "agent-1", false)
	require.NoError(t, err)
	require.NotNil(t, active)
	_, _, err = store.Accept(ctx, *active, DispatchResult{StartsTurn: true, TurnSteerable: true})
	require.NoError(t, err)
	_, err = store.Enqueue(ctx, NewItem{
		ID: "steer", AgentID: "agent-1", Text: "guide",
		Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
	})
	require.NoError(t, err)
	steer, _, err := store.PrepareSteer(ctx, "agent-1", "steer", false)
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
	_, _, err = store.TurnEnded(ctx, "agent-1", false)
	require.NoError(t, err)
	retried, _, err := store.PrepareDispatch(ctx, "agent-1", false)
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
	prepared, snapshot, err := store.PrepareDispatch(ctx, "agent-1", false)
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

func TestStoreAcceptKeepsTheQueueWhenTheNativeTurnConflicts(t *testing.T) {
	t.Parallel()
	database, store := newStoreFixture(t)
	ctx := t.Context()
	const agentID = "agent-1"
	_, err := store.Enqueue(ctx, NewItem{ID: "one", AgentID: agentID, Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "hello"})
	require.NoError(t, err)
	prepared, _, err := store.PrepareDispatch(ctx, agentID, false)
	require.NoError(t, err)
	require.NotNil(t, prepared)
	queries := gendb.New(database)
	bound, err := queries.BindNativeInputTurn(ctx, gendb.BindNativeInputTurnParams{
		AgentID: agentID, AgentSessionID: "session", NativeInputID: "native-input", NativeTurnID: "owned-turn",
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, bound)
	receipt := agent.NativeInputReceipt{SessionID: "session", InputID: "native-input", TurnID: "unrelated-turn"}
	_, _, err = store.Accept(ctx, *prepared, DispatchResult{StartsTurn: true, NativeReceipt: receipt})
	require.ErrorContains(t, err, "another native turn")

	receipt.TurnID = "owned-turn"
	row, snapshot, err := store.Accept(ctx, *prepared, DispatchResult{StartsTurn: true, NativeReceipt: receipt})
	require.NoError(t, err)
	assert.Equal(t, prepared.ReservedSeq, row.Seq)
	assert.Empty(t, snapshot.Items)
}

func TestStoreAcceptTakesWriteIntentBeforeItsReads(t *testing.T) {
	t.Parallel()
	database, store := newStoreFixture(t)
	ctx := t.Context()
	_, err := store.Enqueue(ctx, NewItem{ID: "one", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "hello"})
	require.NoError(t, err)
	prepared, _, err := store.PrepareDispatch(ctx, "agent-1", false)
	require.NoError(t, err)
	require.NotNil(t, prepared)
	competingConn, err := database.Conn(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, competingConn.Close()) })
	_, err = competingConn.ExecContext(ctx, `PRAGMA busy_timeout=0`)
	require.NoError(t, err)

	var competingErrors []error
	store.beforeAcceptWrite = func() {
		_, writeErr := competingConn.ExecContext(ctx, `UPDATE agents SET message_seq_hwm = message_seq_hwm + 1 WHERE id = ?`, "agent-1")
		competingErrors = append(competingErrors, writeErr)
	}
	transcript, snapshot, acceptErr := store.Accept(ctx, *prepared, DispatchResult{StartsTurn: true})
	require.NotEmpty(t, competingErrors, "a second writer must try to enter before Accept writes")
	for _, competingErr := range competingErrors {
		var sqliteErr *sqlite.Error
		require.ErrorAs(t, competingErr, &sqliteErr, "Accept must own write intent before it reads")
		assert.Equal(t, sqlitelib.SQLITE_BUSY, sqliteErr.Code())
	}
	require.NoError(t, acceptErr)
	assert.Equal(t, prepared.ReservedSeq, transcript.Seq)
	assert.Empty(t, snapshot.Items)
	var count int
	require.NoError(t, database.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE id = ?`, "one").Scan(&count))
	assert.Equal(t, 1, count)
}

func TestStoreAcceptRollsBackWhenItsContextIsCanceled(t *testing.T) {
	t.Parallel()
	database, store := newStoreFixture(t)
	database.SetMaxOpenConns(1)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	_, err := store.Enqueue(ctx, NewItem{ID: "one", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "hello"})
	require.NoError(t, err)
	prepared, _, err := store.PrepareDispatch(ctx, "agent-1", false)
	require.NoError(t, err)
	require.NotNil(t, prepared)
	storedPrepared, preparedAttachments, found, err := store.getItem(t.Context(), database, "agent-1", "one", true)
	require.NoError(t, err)
	require.True(t, found)
	store.beforeAcceptWrite = cancel

	_, _, err = store.Accept(ctx, *prepared, DispatchResult{StartsTurn: true})
	require.ErrorIs(t, err, context.Canceled)
	// The cancellation rollback can own the transaction after Accept returns.
	// The sole connection becomes available only after that rollback finishes.
	connection, err := database.Conn(t.Context())
	require.NoError(t, err)
	require.NoError(t, connection.Close())
	assert.Zero(t, database.Stats().InUse, "Accept must release its transaction connection")
	var count int
	require.NoError(t, database.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM messages WHERE id = ?`, "one").Scan(&count))
	assert.Zero(t, count)
	item, attachments, found, err := store.getItem(t.Context(), database, "agent-1", "one", true)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, storedPrepared, item)
	assert.Equal(t, preparedAttachments, attachments)
	assert.Equal(t, prepared.Item.Attachments, attachments)
	assert.Equal(t, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DISPATCHING, item.State)
	assert.Positive(t, prepared.ReservedSeq)
	assert.Equal(t, prepared.ReservedSeq, item.ReservedSeq)
}

func TestStoreAcceptRollsBackAfterADeferredCommitFailure(t *testing.T) {
	t.Parallel()
	database, store := newStoreFixture(t)
	ctx := t.Context()
	_, err := database.ExecContext(ctx, `CREATE TABLE accept_commit_guard (parent_id TEXT REFERENCES agents(id) DEFERRABLE INITIALLY DEFERRED)`)
	require.NoError(t, err)
	_, err = database.ExecContext(ctx, `CREATE TRIGGER accept_commit_failure AFTER INSERT ON messages BEGIN INSERT INTO accept_commit_guard(parent_id) VALUES ('missing-parent'); END`)
	require.NoError(t, err)
	_, err = store.Enqueue(ctx, NewItem{ID: "one", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "hello"})
	require.NoError(t, err)
	prepared, _, err := store.PrepareDispatch(ctx, "agent-1", false)
	require.NoError(t, err)
	require.NotNil(t, prepared)

	_, _, err = store.Accept(ctx, *prepared, DispatchResult{StartsTurn: true})
	require.ErrorContains(t, err, "FOREIGN KEY constraint failed")
	assert.Zero(t, database.Stats().InUse, "Accept must release its transaction connection")
	var transcriptCount, queuedCount int
	require.NoError(t, database.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE id = ?`, "one").Scan(&transcriptCount))
	require.NoError(t, database.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_input_queue_items WHERE id = ? AND state = ?`, "one", leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DISPATCHING).Scan(&queuedCount))
	assert.Zero(t, transcriptCount)
	assert.Equal(t, 1, queuedCount, "a failed commit leaves the prepared input for uncertain-delivery handling")
}

func TestStorePreparationReturnsTheCommittedItem(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{"dispatch", "retry", "steer"} {
		t.Run(operation, func(t *testing.T) {
			t.Parallel()
			database, store := newStoreFixture(t)
			ctx := t.Context()
			input := NewItem{
				ID: "one", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
				Text: "original input", Attachments: []Attachment{{Filename: "original.txt", MimeType: "text/plain", Data: []byte("original attachment")}},
			}
			_, err := store.Enqueue(ctx, input)
			require.NoError(t, err)
			first, _, err := store.PrepareDispatch(ctx, input.AgentID, false)
			require.NoError(t, err)
			require.NotNil(t, first)
			_, err = store.RequeueAndPause(ctx, input.AgentID, input.ID, errors.New("native input is not ready"),
				leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_AGENT_STOPPED)
			require.NoError(t, err)
			_, err = store.SetPaused(ctx, input.AgentID, false, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_UNSPECIFIED)
			require.NoError(t, err)
			switch operation {
			case "retry":
				_, err = store.SetPaused(ctx, input.AgentID, true, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_MANUAL)
				require.NoError(t, err)
			case "steer":
				_, _, err = store.TurnStarted(ctx, input.AgentID, true)
				require.NoError(t, err)
			}
			const oldTimestamp = "2001-02-03T04:05:06Z"
			_, err = database.ExecContext(ctx, "UPDATE agent_input_queue_items SET updated_at = ? WHERE agent_id = ? AND id = ?",
				oldTimestamp, input.AgentID, input.ID)
			require.NoError(t, err)
			queued, _, found, err := store.getItem(ctx, database, input.AgentID, input.ID, true)
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, "native input is not ready", queued.Error)
			require.Equal(t, oldTimestamp, queued.UpdatedAt)
			var prepared *PreparedDispatch
			var snapshot Snapshot
			switch operation {
			case "dispatch":
				prepared, snapshot, err = store.PrepareDispatch(ctx, input.AgentID, false)
			case "retry":
				prepared, snapshot, err = store.PrepareRetry(ctx, input.AgentID)
			case "steer":
				prepared, snapshot, err = store.PrepareSteer(ctx, input.AgentID, input.ID, false)
			}
			require.NoError(t, err)
			require.NotNil(t, prepared)
			stored, attachments, found, err := store.getItem(ctx, database, input.AgentID, input.ID, true)
			require.NoError(t, err)
			require.True(t, found)
			assert.Equal(t, stored, prepared.Item.StoredItem, "the returned reservation must describe its actual committed row")
			assert.Equal(t, attachments, prepared.Item.Attachments)
			assert.Equal(t, input.Attachments, attachments)
			assert.Empty(t, stored.Error)
			assert.NotEqual(t, oldTimestamp, stored.UpdatedAt)
			assert.Equal(t, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DISPATCHING, stored.State)
			assert.Equal(t, prepared.ReservedSeq, stored.ReservedSeq)
			assert.Greater(t, prepared.ReservedSeq, first.ReservedSeq)
			require.Len(t, snapshot.Items, 1)
			assert.Equal(t, stored.ItemMetadata, snapshot.Items[0].ItemMetadata)
			assert.Equal(t, stored.Text, snapshot.Items[0].TextPreview)
			assert.True(t, snapshot.ActiveTurn)
			assert.Equal(t, operation == "retry", snapshot.Paused)
			assert.Equal(t, operation == "steer", snapshot.ActiveTurnSteerable)
		})
	}
}

func TestStorePreparationKeepsFullTextAndCanonicalCommittedTimestamp(t *testing.T) {
	t.Parallel()
	database, store := newStoreFixture(t)
	ctx := t.Context()
	input := NewItem{
		ID: "one", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
		Text:        strings.Repeat("x", snapshotTextPreviewCharacters+128),
		Attachments: []Attachment{{Filename: "original.txt", MimeType: "text/plain", Data: []byte("original attachment")}},
	}
	_, err := store.Enqueue(ctx, input)
	require.NoError(t, err)
	_, err = database.ExecContext(ctx, fmt.Sprintf(`CREATE TRIGGER fixed_preparation_timestamp
AFTER UPDATE OF state ON agent_input_queue_items
WHEN NEW.id = 'one' AND NEW.state = %d
BEGIN
  UPDATE agent_input_queue_items SET updated_at = '2030-01-02T03:04:05.000Z' WHERE id = NEW.id;
END`, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DISPATCHING))
	require.NoError(t, err)
	prepared, snapshot, err := store.PrepareDispatch(ctx, input.AgentID, false)
	require.NoError(t, err)
	require.NotNil(t, prepared)
	stored, attachments, found, err := store.getItem(ctx, database, input.AgentID, input.ID, true)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, "2030-01-02T03:04:05Z", stored.UpdatedAt)
	assert.Equal(t, stored, prepared.Item.StoredItem)
	assert.Equal(t, input.Text, prepared.Item.Text)
	assert.Equal(t, input.Attachments, attachments)
	assert.Equal(t, attachments, prepared.Item.Attachments)
	require.Len(t, snapshot.Items, 1)
	assert.Equal(t, strings.Repeat("x", snapshotTextPreviewCharacters)+"…", snapshot.Items[0].TextPreview)
	assert.Equal(t, stored.UpdatedAt, snapshot.Items[0].UpdatedAt)
}

func TestStorePreparationRejectsAnAbsentCommittedItem(t *testing.T) {
	t.Parallel()
	database, store := newStoreFixture(t)
	ctx := t.Context()
	input := NewItem{ID: "one", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "original input"}
	before, err := store.Enqueue(ctx, input)
	require.NoError(t, err)
	_, err = database.ExecContext(ctx, fmt.Sprintf(`CREATE TRIGGER remove_prepared_item
AFTER UPDATE OF state ON agent_input_queue_items
WHEN NEW.id = 'one' AND NEW.state = %d
BEGIN
  DELETE FROM agent_input_queue_items WHERE id = NEW.id;
END`, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DISPATCHING))
	require.NoError(t, err)
	prepared, _, err := store.PrepareDispatch(ctx, input.AgentID, false)
	assert.ErrorIs(t, err, ErrConflict)
	assert.Nil(t, prepared)
	after, snapshotErr := store.Snapshot(ctx, input.AgentID)
	require.NoError(t, snapshotErr)
	assert.Equal(t, before, after, "an absent prepared item must roll back every queue and reservation field")
	stored, _, found, readErr := store.getItem(ctx, database, input.AgentID, input.ID, true)
	require.NoError(t, readErr)
	require.True(t, found)
	assert.Equal(t, input.Text, stored.Text)
	assert.Equal(t, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED, stored.State)
	assert.Zero(t, stored.ReservedSeq)
	var highWater int64
	require.NoError(t, database.QueryRowContext(ctx, "SELECT message_seq_hwm FROM agents WHERE id = ?", input.AgentID).Scan(&highWater))
	assert.Zero(t, highWater, "the refused preparation must release its uncommitted sequence")
}

func TestStoreAcceptRejectsAChangedReservedSequence(t *testing.T) {
	t.Parallel()
	database, store := newStoreFixture(t)
	ctx := t.Context()
	_, err := store.Enqueue(ctx, NewItem{ID: "one", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "hello"})
	require.NoError(t, err)
	prepared, _, err := store.PrepareDispatch(ctx, "agent-1", false)
	require.NoError(t, err)
	require.NotNil(t, prepared)
	prepared.ReservedSeq++

	_, _, err = store.Accept(ctx, *prepared, DispatchResult{StartsTurn: true})
	require.ErrorIs(t, err, ErrConflict)
	assert.Zero(t, database.Stats().InUse)
	var transcriptCount int
	require.NoError(t, database.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE id = ?`, "one").Scan(&transcriptCount))
	assert.Zero(t, transcriptCount)
}

func TestStoreAcceptRejectsAMissingPreparedItem(t *testing.T) {
	t.Parallel()
	database, store := newStoreFixture(t)
	ctx := t.Context()
	_, err := store.Enqueue(ctx, NewItem{ID: "one", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "hello"})
	require.NoError(t, err)
	prepared, _, err := store.PrepareDispatch(ctx, "agent-1", false)
	require.NoError(t, err)
	require.NotNil(t, prepared)
	_, err = database.ExecContext(ctx, `DELETE FROM agent_input_queue_items WHERE agent_id = ? AND id = ?`, "agent-1", "one")
	require.NoError(t, err)

	_, _, err = store.Accept(ctx, *prepared, DispatchResult{StartsTurn: true})
	require.ErrorIs(t, err, ErrConflict)
	assert.Zero(t, database.Stats().InUse)
	var transcriptCount int
	require.NoError(t, database.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE id = ?`, "one").Scan(&transcriptCount))
	assert.Zero(t, transcriptCount)
}

func TestStoreRecoveryDistinguishesUncertainAndInterrupted(t *testing.T) {
	t.Parallel()

	database, store := newStoreFixture(t)
	ctx := context.Background()
	_, err := store.Enqueue(ctx, NewItem{ID: "one", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "hello"})
	require.NoError(t, err)
	_, _, err = store.PrepareDispatch(ctx, "agent-1", false)
	require.NoError(t, err)
	_, err = store.SetPaused(ctx, "agent-1", true, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_MANUAL)
	require.NoError(t, err)
	before := readQueuePauseState(t, database)
	recovered := NewStore(database, nil)
	snapshots, err := recovered.Recover(ctx, nil)
	require.NoError(t, err)
	require.Len(t, snapshots, 1)
	assert.Equal(t, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DELIVERY_UNCERTAIN, snapshots[0].Items[0].State)
	assert.Equal(t, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_DELIVERY_UNCERTAIN, snapshots[0].PauseReason)
	assert.Equal(t, pauseOwnerRecovery, readQueuePauseState(t, database).owner)
	assert.EqualValues(t, before.revision+1, snapshots[0].Revision)
	assertQueueTurnEnded(t, database, recovered)

	database, store = newStoreFixture(t)
	_, err = store.Enqueue(ctx, NewItem{ID: "two", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "hello"})
	require.NoError(t, err)
	prepared, _, err := store.PrepareDispatch(ctx, "agent-1", false)
	require.NoError(t, err)
	_, _, err = store.Accept(ctx, *prepared, DispatchResult{StartsTurn: true, TurnSteerable: true})
	require.NoError(t, err)
	_, err = store.SetPaused(ctx, "agent-1", true, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_MANUAL)
	require.NoError(t, err)
	before = readQueuePauseState(t, database)
	recovered = NewStore(database, nil)
	snapshots, err = recovered.Recover(ctx, nil)
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_INTERRUPTED, snapshots[0].PauseReason)
	assert.Equal(t, pauseOwnerRecovery, readQueuePauseState(t, database).owner)
	assert.EqualValues(t, before.revision+1, snapshots[0].Revision)
	assertQueueTurnEnded(t, database, recovered)
}

func TestStoreFailDispatchReplacesThePauseAndClearsAllTurnState(t *testing.T) {
	t.Parallel()
	for _, uncertain := range []bool{false, true} {
		for _, providerReported := range []bool{false, true} {
			t.Run(fmt.Sprintf("uncertain=%v reported=%v", uncertain, providerReported), func(t *testing.T) {
				database, store := newStoreFixture(t)
				ctx := t.Context()
				_, err := store.Enqueue(ctx, NewItem{ID: "one", AgentID: "agent-1", Text: "hello", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE})
				require.NoError(t, err)
				_, _, err = store.PrepareDispatch(ctx, "agent-1", false)
				require.NoError(t, err)
				if providerReported {
					_, _, err = store.TurnStarted(ctx, "agent-1", true)
					require.NoError(t, err)
				}
				_, err = store.SetPaused(ctx, "agent-1", true, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_MANUAL)
				require.NoError(t, err)
				before := readQueuePauseState(t, database)
				snapshot, err := store.FailDispatch(ctx, "agent-1", "one", assert.AnError, uncertain)
				require.NoError(t, err)
				require.Len(t, snapshot.Items, 1)
				reason := leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_DELIVERY_FAILED
				state := leapmuxv1.AgentInputState_AGENT_INPUT_STATE_FAILED
				if uncertain {
					reason, state = leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_DELIVERY_UNCERTAIN, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DELIVERY_UNCERTAIN
				}
				assert.Equal(t, reason, snapshot.PauseReason)
				assert.Equal(t, state, snapshot.Items[0].State)
				assert.Equal(t, assert.AnError.Error(), snapshot.Items[0].Error)
				assert.Equal(t, pauseOwnerDelivery, readQueuePauseState(t, database).owner)
				assert.EqualValues(t, before.revision+1, snapshot.Revision)
				assertQueueTurnEnded(t, database, store)
			})
		}
	}
}

func TestStoreRecoveryKeepsAnIdlePauseAndRevision(t *testing.T) {
	t.Parallel()
	database, store := newStoreFixture(t)
	_, err := store.SetPaused(t.Context(), "agent-1", true, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_MANUAL)
	require.NoError(t, err)
	before := readQueuePauseState(t, database)
	_, err = store.Recover(t.Context(), nil)
	require.NoError(t, err)
	assert.Equal(t, before, readQueuePauseState(t, database))
}

func assertQueueTurnEnded(t *testing.T, database *sql.DB, store *Store) {
	t.Helper()
	var active bool
	require.NoError(t, database.QueryRowContext(t.Context(), `SELECT active_turn FROM agent_input_queue_state WHERE agent_id = 'agent-1'`).Scan(&active))
	ephem := store.ephemeralForTest("agent-1")
	assert.False(t, active)
	assert.False(t, ephem.activeTurnSteerable)
	assert.Empty(t, ephem.activeInputID)
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

	snapshots, err := NewStore(database, nil).Recover(ctx, nil)
	require.NoError(t, err)
	assert.Empty(t, snapshots)
}

func TestStoreRetryRequiresUncertainDeliveryConfirmation(t *testing.T) {
	t.Parallel()

	database, store := newStoreFixture(t)
	ctx := context.Background()
	_, err := store.Enqueue(ctx, NewItem{ID: "one", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "hello"})
	require.NoError(t, err)
	prepared, _, err := store.PrepareDispatch(ctx, "agent-1", false)
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
	_, _, err = store.PrepareDispatch(ctx, "agent-1", false)
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
			INSERT INTO agent_input_queue_items (id, agent_id, order_index, kind, text, idempotency_key)
			VALUES (?, 'agent-1', ?, 1, 'queued', ?)`, fmt.Sprintf("input-%d", i), i, fmt.Sprintf("key-%d", i))
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
			INSERT INTO agent_input_queue_items (id, agent_id, order_index, kind, text, idempotency_key)
			VALUES (?, 'agent-1', ?, 1, 'queued', ?)`, inputID, i, "key-"+inputID)
		require.NoError(t, err)
		_, err = database.ExecContext(ctx, `
			INSERT INTO agent_input_queue_attachments (item_id, position, filename, mime_type, data)
			VALUES (?, 0, 'blob.bin', 'application/octet-stream', zeroblob(?))`, inputID, MaxItemBytes)
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
	_, _, err := store.PrepareDispatch(ctx, "agent-1", false)
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
	_, _, err = store.PrepareDispatch(ctx, "agent-1", false)
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

	item := StoredItem{
		ItemMetadata: ItemMetadata{
			OrderingMetadata: OrderingMetadata{Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_PLAN_EXECUTION},
		},
		Text: "run it",
	}
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
	prepared, _, err := store.PrepareDispatch(ctx, "agent-1", false)
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

	prepared, _, err := store.PrepareSteer(ctx, "agent-1", "", false)
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
	blocked, _, err := store.PrepareDispatch(ctx, "agent-1", false)
	require.NoError(t, err)
	require.Nil(t, blocked, "an edited head blocks dispatch")

	// In-place recovery drops the abandoned edit lock on the current store.
	_, err = store.Recover(ctx, nil)
	require.NoError(t, err)
	prepared, _, err := store.PrepareDispatch(ctx, "agent-1", false)
	require.NoError(t, err)
	assert.NotNil(t, prepared, "recovery released the lock, so the head dispatches")

	// A simulated restart (fresh Store) starts with no edit lock either.
	_, err = store.RequeuePending(ctx, "agent-1", "one")
	require.NoError(t, err)
	_, _, _, err = store.BeginEdit(ctx, "agent-1", "one", "client-gone-2", false)
	require.NoError(t, err)
	restarted := NewStore(database, nil)
	_, err = restarted.Recover(ctx, nil)
	require.NoError(t, err)
	preparedAfterRestart, _, err := restarted.PrepareDispatch(ctx, "agent-1", false)
	require.NoError(t, err)
	assert.NotNil(t, preparedAfterRestart, "a restarted store holds no abandoned edit lock")
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

	prepared, _, err := store.PrepareDispatch(ctx, "agent-1", false)
	require.NoError(t, err)
	_, active, err := store.Accept(ctx, *prepared, DispatchResult{StartsTurn: true, TurnSteerable: true})
	require.NoError(t, err)
	require.Len(t, active.Items, 1)
	assert.True(t, active.Items[0].CanSteer)

	// A clear runs no regular turn, so the head is not steerable into it.
	_, _, err = store.TurnEnded(ctx, "agent-1", false)
	require.NoError(t, err)
	_, err = store.Enqueue(ctx, NewItem{ID: "clear", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_CLEAR_CONTEXT, Text: "/clear"})
	require.NoError(t, err)
	_, err = store.Move(ctx, "agent-1", "clear", "two")
	require.NoError(t, err)
	clearPrepared, _, err := store.PrepareDispatch(ctx, "agent-1", false)
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
	prepared, _, err := store.PrepareDispatch(ctx, "agent-1", false)
	require.NoError(t, err)
	require.NotNil(t, prepared)

	cleared, changed, err := store.TurnEnded(ctx, "agent-1", false)
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

	_, _, err = store.PrepareSteer(ctx, "agent-1", "one", false)
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

	_, _, err = store.TurnEnded(ctx, "agent-1", false)
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

	prepared, _, err := store.PrepareSteer(ctx, "agent-1", "one", false)
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
	prepared, _, err := store.PrepareDispatch(ctx, "agent-1", false)
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
			seed: func(t *testing.T, _ *sql.DB, store *Store) {
				_, _, _, err := store.BeginEdit(t.Context(), "agent-1", "head", "other-tab", false)
				require.NoError(t, err)
			},
			want: false,
		},
		{
			// A planned restart stops the old process, so a write reaches a closed pipe.
			name: "a planned restart in flight",
			seed: func(_ *testing.T, _ *sql.DB, store *Store) {
				store.setRestartingForTest("agent-1", true)
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
			_, _, err = store.TurnEnded(t.Context(), "agent-1", false)
			require.NoError(t, err)
			prepared, _, err := store.PrepareDispatch(t.Context(), "agent-1", false)
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

	for _, state := range []leapmuxv1.ControlDeliveryState{
		leapmuxv1.ControlDeliveryState_CONTROL_DELIVERY_STATE_PENDING,
		leapmuxv1.ControlDeliveryState_CONTROL_DELIVERY_STATE_DELIVERED,
		leapmuxv1.ControlDeliveryState_CONTROL_DELIVERY_STATE_COMPLETED,
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
			err = dbtest.StoreControlResponse(t.Context(), database, gendb.ControlResponseAnswer{
				AgentID: "agent-1", RequestID: "approval", ClaimToken: "claim", State: state,
				InputID: "plan", PlanApprovalSettings: []byte(`{}`),
			})
			require.NoError(t, err)

			snapshot, err := store.Snapshot(t.Context(), "agent-1")
			require.NoError(t, err)
			recorded := state == leapmuxv1.ControlDeliveryState_CONTROL_DELIVERY_STATE_COMPLETED
			assert.Equal(t, recorded, snapshot.Items[0].CanPreempt,
				"only a recorded approval has a turn worth cancelling")

			_, _, err = store.TurnEnded(t.Context(), "agent-1", false)
			require.NoError(t, err)
			prepared, _, err := store.PrepareDispatch(t.Context(), "agent-1", false)
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

	_, store := newStoreFixture(t)
	_, err := store.Enqueue(t.Context(), NewItem{
		ID: "head", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "next",
	})
	require.NoError(t, err)
	_, _, err = store.TurnStarted(t.Context(), "agent-1", true)
	require.NoError(t, err)

	snapshot, err := store.Snapshot(t.Context(), "agent-1")
	require.NoError(t, err)
	require.True(t, snapshot.Items[0].CanSteer, "a steerable turn offers a steer")

	store.setRestartingForTest("agent-1", true)

	snapshot, err = store.Snapshot(t.Context(), "agent-1")
	require.NoError(t, err)
	assert.False(t, snapshot.Items[0].CanSteer, "a restart in flight refuses the steer the offer promised")
	_, _, err = store.PrepareSteer(t.Context(), "agent-1", "head", false)
	assert.ErrorIs(t, err, ErrPlannedRestart)
}

func TestStoreUncertainSteersSelectOnlyTheOldestAndKeepAttachments(t *testing.T) {
	t.Parallel()
	database, store := newStoreFixture(t)
	holdEchoSteer(t, store, "first")
	holdEchoSteer(t, store, "second")
	current := prepareEchoSteer(t, store, "third")
	before, err := store.Snapshot(t.Context(), "agent-1")
	require.NoError(t, err)
	same, resolved, inFlight, err := store.ResolveUnechoedSteers(t.Context(), "agent-1", 0, current.Item.ID)
	require.NoError(t, err)
	assert.Zero(t, resolved)
	assert.False(t, inFlight)
	assert.Equal(t, before, same)
	snapshot, resolved, inFlight, err := store.ResolveUnechoedSteers(t.Context(), "agent-1", 1, current.Item.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, resolved)
	assert.False(t, inFlight)
	assert.Equal(t, []string{"second", "first", "third"}, queueSnapshotIDs(snapshot))
	assert.Equal(t, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_AWAITING_ECHO, snapshot.Items[0].State)
	assert.Equal(t, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DELIVERY_UNCERTAIN, snapshot.Items[1].State)
	assert.True(t, snapshot.Paused)
	item, attachments, found, err := store.getItem(t.Context(), database, "agent-1", "first", true)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, "first", item.Text)
	require.Len(t, attachments, 1)
	assert.Equal(t, []byte("keep this attachment"), attachments[0].Data)
	snapshot, resolved, inFlight, err = store.ResolveUnechoedSteers(t.Context(), "agent-1", 1000000, current.Item.ID)
	require.NoError(t, err)
	assert.Equal(t, 2, resolved)
	assert.True(t, inFlight)
	for _, item := range snapshot.Items {
		assert.Equal(t, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DELIVERY_UNCERTAIN, item.State)
	}
	var count int
	require.NoError(t, database.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM messages`).Scan(&count))
	assert.Zero(t, count)
}

func TestStoreUncertainSteerFailureRollsBackTheWholeReport(t *testing.T) {
	t.Parallel()
	database, store := newStoreFixture(t)
	holdEchoSteer(t, store, "first")
	holdEchoSteer(t, store, "second")
	before, err := store.Snapshot(t.Context(), "agent-1")
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), `CREATE TRIGGER refuse_uncertainty BEFORE UPDATE ON agent_input_queue_items WHEN NEW.state = 4 AND NEW.id = 'second' BEGIN SELECT RAISE(ABORT, 'refused uncertainty'); END`)
	require.NoError(t, err)
	_, _, _, err = store.ResolveUnechoedSteers(t.Context(), "agent-1", -1, "")
	require.ErrorContains(t, err, "refused uncertainty")
	after, err := store.Snapshot(t.Context(), "agent-1")
	require.NoError(t, err)
	assert.Equal(t, before, after)
}

// A question answer that an echo-policy provider steers mid-turn accepts its
// row at the steer: the prepare reserves the sequence, because the row is
// LeapMux's own record and never waits for an echo. A plain user steer under
// the same policy still holds its row for the echo and reserves nothing.
func TestStoreAutomaticQuestionSteeringAcceptsItsRowAtTheSteer(t *testing.T) {
	t.Parallel()
	_, store := newStoreFixture(t)
	_, err := store.Enqueue(t.Context(), NewItem{ID: "answer", AgentID: "agent-1",
		Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_QUESTION_ANSWER, Text: "Red"})
	require.NoError(t, err)
	_, _, err = store.TurnStarted(t.Context(), "agent-1", true)
	require.NoError(t, err)
	prepared, _, err := store.PrepareDispatch(t.Context(), "agent-1", true)
	require.NoError(t, err)
	require.NotNil(t, prepared)
	assert.True(t, prepared.Steering)
	assert.Positive(t, prepared.ReservedSeq, "the answer row is written at its own accept, not at a provider echo")

	_, store = newStoreFixture(t)
	_, err = store.Enqueue(t.Context(), NewItem{ID: "message", AgentID: "agent-1",
		Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "Hello"})
	require.NoError(t, err)
	_, _, err = store.TurnStarted(t.Context(), "agent-1", true)
	require.NoError(t, err)
	steer, _, err := store.PrepareSteer(t.Context(), "agent-1", "message", true)
	require.NoError(t, err)
	require.NotNil(t, steer)
	assert.Zero(t, steer.ReservedSeq, "a user steer holds its row for the provider's echo")
}

func TestStoreAutomaticQuestionAdmission(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name                                          string
		kind                                          leapmuxv1.AgentInputKind
		paused, active, steerable, admitted, steering bool
	}{
		{name: "idle question", kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_QUESTION_ANSWER, admitted: true},
		{name: "active steerable question", kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_QUESTION_ANSWER, active: true, steerable: true, admitted: true, steering: true},
		{name: "paused active question", kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_QUESTION_ANSWER, paused: true, active: true, steerable: true},
		{name: "paused idle question", kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_QUESTION_ANSWER, paused: true},
		{name: "active unsteerable question", kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_QUESTION_ANSWER, active: true},
		{name: "ordinary active message", kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, active: true, steerable: true},
		{name: "active feedback", kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_CONTROL_FEEDBACK, active: true, steerable: true},
		{name: "ordinary idle message", kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, admitted: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, store := newStoreFixture(t)
			_, err := store.Enqueue(t.Context(), NewItem{ID: "input", AgentID: "agent-1", Kind: test.kind, Text: "answer"})
			require.NoError(t, err)
			if test.active {
				_, _, err = store.TurnStarted(t.Context(), "agent-1", test.steerable)
				require.NoError(t, err)
			}
			if test.paused {
				_, err = store.SetPaused(t.Context(), "agent-1", true, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_MANUAL)
				require.NoError(t, err)
			}
			prepared, snapshot, err := store.PrepareDispatch(t.Context(), "agent-1", false)
			require.NoError(t, err)
			if !test.admitted {
				assert.Nil(t, prepared)
				require.Len(t, snapshot.Items, 1)
				assert.Equal(t, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED, snapshot.Items[0].State)
				assert.Equal(t, test.paused, snapshot.Paused)
				return
			}
			require.NotNil(t, prepared)
			assert.Equal(t, test.steering, prepared.Steering)
			assert.Positive(t, prepared.ReservedSeq)
			assert.Equal(t, "answer", prepared.Item.Text)
		})
	}
}

// queuePauseState is the pause half of agent-1's queue state row.
type queuePauseState struct {
	reason     leapmuxv1.AgentInputQueuePauseReason
	owner      pauseOwner
	activeTurn bool
	revision   int64
}

func readQueuePauseState(t *testing.T, database *sql.DB) queuePauseState {
	t.Helper()
	var reason, owner sql.NullInt64
	var state queuePauseState
	require.NoError(t, database.QueryRowContext(t.Context(),
		`SELECT pause_reason, pause_owner, active_turn, revision FROM agent_input_queue_state WHERE agent_id = 'agent-1'`).
		Scan(&reason, &owner, &state.activeTurn, &state.revision))
	state.reason = leapmuxv1.AgentInputQueuePauseReason(reason.Int64)
	state.owner = pauseOwner(owner.Int64)
	return state
}

// pauseAgentAndEndTurn runs pauseAndEndTurn for agent-1 in its own
// transaction and returns the number of rows it changed.
func pauseAgentAndEndTurn(t *testing.T, store *Store, reason leapmuxv1.AgentInputQueuePauseReason, owner pauseOwner) int64 {
	t.Helper()
	tx, err := store.db.BeginTx(t.Context(), nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	staged := store.cloneEphemeral("agent-1")
	result, err := pauseAndEndTurn(t.Context(), tx, "agent-1", reason, owner, &staged, time.Now())
	require.NoError(t, err)
	rows, err := result.RowsAffected()
	require.NoError(t, err)
	require.NoError(t, store.commitEphemeral(tx, "agent-1", staged))
	return rows
}

// TestPauseAndEndTurnKeepsTheEarlierPause pins the contract in the doc above
// pauseAndEndTurn, which now reads the pause from pause_reason alone: an
// existing pause keeps its reason and its owner, except that a pause a
// planned restart owns passes to the new cause with that cause's reason.
func TestPauseAndEndTurnKeepsTheEarlierPause(t *testing.T) {
	t.Parallel()

	t.Run("a manual pause keeps its reason and owner", func(t *testing.T) {
		t.Parallel()
		database, store := newStoreFixture(t)
		_, err := store.SetPaused(t.Context(), "agent-1", true, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_MANUAL)
		require.NoError(t, err)
		_, err = database.ExecContext(t.Context(), `UPDATE agent_input_queue_state SET active_turn = 1 WHERE agent_id = 'agent-1'`)
		require.NoError(t, err)

		changed := pauseAgentAndEndTurn(t, store,
			leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_AGENT_STOPPED, pauseOwnerAgentStopped)
		assert.Equal(t, int64(1), changed, "the active turn ends")
		after := readQueuePauseState(t, database)
		assert.Equal(t, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_MANUAL, after.reason)
		assert.Equal(t, pauseOwnerManual, after.owner)
		assert.False(t, after.activeTurn)

		// No turn is active and the pause is not a planned restart's, so the
		// WHERE clause matches nothing.
		changed = pauseAgentAndEndTurn(t, store,
			leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_AGENT_STOPPED, pauseOwnerAgentStopped)
		assert.Zero(t, changed)
		assert.Equal(t, after, readQueuePauseState(t, database), "a call that matches nothing changes nothing")
	})

	t.Run("a planned restart's pause passes to the new cause", func(t *testing.T) {
		t.Parallel()
		database, store := newStoreFixture(t)
		_, created, err := store.pauseForPlannedRestart(t.Context(), "agent-1")
		require.NoError(t, err)
		require.True(t, created)
		before := readQueuePauseState(t, database)
		require.Equal(t, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_AGENT_STOPPED, before.reason)
		require.Equal(t, pauseOwnerPlannedRestart, before.owner)

		changed := pauseAgentAndEndTurn(t, store,
			leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_INTERRUPTED, pauseOwnerArchive)
		assert.Equal(t, int64(1), changed)
		after := readQueuePauseState(t, database)
		assert.Equal(t, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_INTERRUPTED, after.reason,
			"the new owner records its own cause instead of the temporary restart reason")
		assert.Equal(t, pauseOwnerArchive, after.owner)

		// The restart no longer owns the pause, so its end cannot lift it.
		snapshot, _, err := store.finishPlannedRestart(t.Context(), "agent-1", true, true)
		require.NoError(t, err)
		assert.True(t, snapshot.Paused, "the archive's pause survives the end of the restart")
		final := readQueuePauseState(t, database)
		assert.Equal(t, pauseOwnerArchive, final.owner)
		assert.Equal(t, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_INTERRUPTED, final.reason)
	})
}

func insertBackgroundTaskRow(t *testing.T, database *sql.DB, ownerID, rowKey string, seq int64, kind leapmuxv1.BackgroundTaskKind, role bgtask.Role, childAgentID, parentAgentID string, status bgtask.Status) {
	t.Helper()
	_, err := database.ExecContext(t.Context(), `
		INSERT INTO agent_background_tasks
		(owner_agent_id, row_key, seq, kind, role, child_agent_id, parent_agent_id, status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		ownerID, rowKey, seq, kind, role, childAgentID, parentAgentID, status)
	require.NoError(t, err)
}

func createChildAgentRow(t *testing.T, database *sql.DB, id, parentID string) {
	t.Helper()
	_, err := database.ExecContext(t.Context(), `
		INSERT INTO agents (id, working_dir, agent_provider, parent_agent_id)
		VALUES (?, ?, ?, ?)`,
		id, t.TempDir(), leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, parentID)
	require.NoError(t, err)
}

func TestStoreBackgroundWorkHoldsRootDispatchAndReportsEmptyQueue(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	for _, status := range []bgtask.Status{bgtask.StatusPending, bgtask.StatusRunning} {
		t.Run("working status "+status.String(), func(t *testing.T) {
			t.Parallel()
			database, store := newStoreFixture(t)
			insertBackgroundTaskRow(t, database, "agent-1", "task-1", 1,
				leapmuxv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SHELL, bgtask.RoleTaskOutcome, "", "", status)

			// Empty queue (no state row yet, head < 0) still reports ActiveBackgroundWork.
			emptySnap, err := store.Snapshot(ctx, "agent-1")
			require.NoError(t, err)
			assert.True(t, emptySnap.ActiveBackgroundWork)

			// Ensure state row exists with empty queue (head < 0) and still reports ActiveBackgroundWork.
			_, _, err = store.TurnEnded(ctx, "agent-1", false)
			require.NoError(t, err)
			emptyWithState, err := store.Snapshot(ctx, "agent-1")
			require.NoError(t, err)
			assert.True(t, emptyWithState.ActiveBackgroundWork)

			snap, err := store.Enqueue(ctx, NewItem{
				ID: "in-1", AgentID: "agent-1",
				Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "hello",
			})
			require.NoError(t, err)
			assert.True(t, snap.ActiveBackgroundWork)
			require.Len(t, snap.Items, 1)
			assert.True(t, snap.Items[0].CanSteer)
			assert.True(t, snap.Items[0].CanPreempt)

			prepared, after, err := store.PrepareDispatch(ctx, "agent-1", false)
			require.NoError(t, err)
			assert.Nil(t, prepared, "working background task must hold automatic root dispatch")
			assert.True(t, after.ActiveBackgroundWork)
		})
	}

	for _, status := range []bgtask.Status{bgtask.StatusPaused, bgtask.StatusSucceeded} {
		t.Run("non-working status "+status.String(), func(t *testing.T) {
			t.Parallel()
			database, store := newStoreFixture(t)
			insertBackgroundTaskRow(t, database, "agent-1", "task-1", 1,
				leapmuxv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SHELL, bgtask.RoleTaskOutcome, "", "", status)

			_, err := store.Enqueue(ctx, NewItem{
				ID: "in-1", AgentID: "agent-1",
				Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "hello",
			})
			require.NoError(t, err)

			prepared, snap, err := store.PrepareDispatch(ctx, "agent-1", false)
			require.NoError(t, err)
			require.NotNil(t, prepared, "non-working background task must not hold automatic dispatch")
			assert.False(t, snap.ActiveBackgroundWork)
		})
	}
}

func TestStoreBackgroundWorkScopesChildToDescendantsOnly(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	t.Run("admits when sibling, root shell, own canonical row, or root row across empty child_agent_id shell run", func(t *testing.T) {
		t.Parallel()
		database, store := newStoreFixture(t)
		createChildAgentRow(t, database, "child-a", "agent-1")
		createChildAgentRow(t, database, "child-b", "agent-1")

		// (a) sibling Child B and root shell are RUNNING.
		insertBackgroundTaskRow(t, database, "agent-1", "sibling-b", 1,
			leapmuxv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SUBAGENT, bgtask.RoleChildTask, "child-b", "", bgtask.StatusRunning)
		insertBackgroundTaskRow(t, database, "agent-1", "root-shell", 2,
			leapmuxv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SHELL, bgtask.RoleTaskOutcome, "", "", bgtask.StatusRunning)
		// (b) Child A's own canonical activity row is RUNNING.
		insertBackgroundTaskRow(t, database, "agent-1", "self-a", 3,
			leapmuxv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SUBAGENT, bgtask.RoleChildTask, "child-a", "", bgtask.StatusRunning)
		// (c) Child A has a finished shell row with child_agent_id = '' while root-shell (parent_agent_id = '') is RUNNING.
		insertBackgroundTaskRow(t, database, "agent-1", "child-a-shell", 4,
			leapmuxv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SHELL, bgtask.RoleTaskOutcome, "", "child-a", bgtask.StatusSucceeded)

		_, err := store.Enqueue(ctx, NewItem{
			ID: "in-a", AgentID: "child-a",
			Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "hi child",
		})
		require.NoError(t, err)

		prepared, snap, err := store.PrepareDispatch(ctx, "child-a", false)
		require.NoError(t, err)
		require.NotNil(t, prepared, "child dispatch must not be held by sibling, own row, or root rows")
		assert.False(t, snap.ActiveBackgroundWork)
	})

	t.Run("holds when direct child under child-a is running", func(t *testing.T) {
		t.Parallel()
		database, store := newStoreFixture(t)
		createChildAgentRow(t, database, "child-a", "agent-1")
		insertBackgroundTaskRow(t, database, "agent-1", "child-a-shell", 1,
			leapmuxv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SHELL, bgtask.RoleTaskOutcome, "", "child-a", bgtask.StatusRunning)

		_, err := store.Enqueue(ctx, NewItem{
			ID: "in-a", AgentID: "child-a",
			Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "hi child",
		})
		require.NoError(t, err)

		prepared, snap, err := store.PrepareDispatch(ctx, "child-a", false)
		require.NoError(t, err)
		assert.Nil(t, prepared, "direct descendant running under child-a must hold child-a dispatch")
		assert.True(t, snap.ActiveBackgroundWork)
	})

	t.Run("holds when grandchild under child-a is running", func(t *testing.T) {
		t.Parallel()
		database, store := newStoreFixture(t)
		createChildAgentRow(t, database, "child-a", "agent-1")
		createChildAgentRow(t, database, "mid", "child-a")
		createChildAgentRow(t, database, "grandchild", "mid")
		insertBackgroundTaskRow(t, database, "agent-1", "mid-row", 1,
			leapmuxv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SUBAGENT, bgtask.RoleChildTask, "mid", "child-a", bgtask.StatusSucceeded)
		insertBackgroundTaskRow(t, database, "agent-1", "grand-row", 2,
			leapmuxv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SUBAGENT, bgtask.RoleChildTask, "grandchild", "mid", bgtask.StatusRunning)

		_, err := store.Enqueue(ctx, NewItem{
			ID: "in-a", AgentID: "child-a",
			Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "hi child",
		})
		require.NoError(t, err)

		prepared, snap, err := store.PrepareDispatch(ctx, "child-a", false)
		require.NoError(t, err)
		assert.Nil(t, prepared, "running grandchild must hold child-a dispatch")
		assert.True(t, snap.ActiveBackgroundWork)
	})
}

func TestStoreBackgroundWorkBypassAndSelectiveClearing(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	t.Run("prepareBypass admission and explicit refusals", func(t *testing.T) {
		t.Parallel()
		clock := testutil.NewQuartzMock(t)
		database := newQueueDatabase(t)
		store := NewStore(database, clock)
		insertBackgroundTaskRow(t, database, "agent-1", "task-1", 1,
			leapmuxv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SHELL, bgtask.RoleTaskOutcome, "", "", bgtask.StatusRunning)

		_, err := store.Enqueue(ctx, NewItem{
			ID: "in-1", AgentID: "agent-1",
			Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "first",
		})
		require.NoError(t, err)

		// Wrong or empty input ID -> ErrNotHead.
		_, _, err = store.prepareBypass(ctx, "agent-1", "", dispatchBypassSteer)
		assert.ErrorIs(t, err, ErrNotHead)
		_, _, err = store.prepareBypass(ctx, "agent-1", "other", dispatchBypassSteer)
		assert.ErrorIs(t, err, ErrNotHead)
		_, _, err = store.prepareBypass(ctx, "agent-1", "", dispatchBypassPreempt)
		assert.ErrorIs(t, err, ErrNotHead)

		// Active turn refuses with ErrSteeringState (Steer) / ErrPreemptionState (Preempt).
		_, _, err = store.TurnStarted(ctx, "agent-1", true)
		require.NoError(t, err)
		_, _, err = store.prepareBypass(ctx, "agent-1", "in-1", dispatchBypassSteer)
		assert.ErrorIs(t, err, ErrSteeringState)
		_, _, err = store.prepareBypass(ctx, "agent-1", "in-1", dispatchBypassPreempt)
		assert.ErrorIs(t, err, ErrPreemptionState)
		_, _, err = store.TurnEnded(ctx, "agent-1", false)
		require.NoError(t, err)

		// Editing item refuses explicitly.
		_, _, _, err = store.BeginEdit(ctx, "agent-1", "in-1", "client-1", false)
		require.NoError(t, err)
		_, _, err = store.prepareBypass(ctx, "agent-1", "in-1", dispatchBypassSteer)
		assert.ErrorIs(t, err, ErrSteeringState)
		_, _, err = store.prepareBypass(ctx, "agent-1", "in-1", dispatchBypassPreempt)
		assert.ErrorIs(t, err, ErrPreemptionState)
		_, err = store.CancelEdit(ctx, "agent-1", "in-1", "client-1")
		require.NoError(t, err)

		// Paused queue: dispatchBypassPreempt refuses with ErrPreemptionState; dispatchBypassSteer admits.
		_, err = store.SetPaused(ctx, "agent-1", true, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_MANUAL)
		require.NoError(t, err)
		_, _, err = store.prepareBypass(ctx, "agent-1", "in-1", dispatchBypassPreempt)
		assert.ErrorIs(t, err, ErrPreemptionState)

		prepared, snap, err := store.prepareBypass(ctx, "agent-1", "in-1", dispatchBypassSteer)
		require.NoError(t, err)
		require.NotNil(t, prepared)
		assert.False(t, prepared.Steering)
		assert.Positive(t, prepared.ReservedSeq)
		assert.True(t, snap.ActiveTurn)
		assert.True(t, snap.Paused)
		assert.Equal(t, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DISPATCHING, snap.Items[0].State)

		// Scheduled future head refuses with ErrScheduled in both bypass modes.
		future := clock.Now().UTC().Add(time.Minute)
		database2 := newQueueDatabase(t)
		store2 := NewStore(database2, clock)
		insertBackgroundTaskRow(t, database2, "agent-1", "task-1", 1,
			leapmuxv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SHELL, bgtask.RoleTaskOutcome, "", "", bgtask.StatusRunning)
		_, err = store2.Enqueue(ctx, NewItem{
			ID: "sched-1", AgentID: "agent-1",
			Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "later",
			ScheduledAt: &future,
		})
		require.NoError(t, err)
		_, _, err = store2.prepareBypass(ctx, "agent-1", "sched-1", dispatchBypassSteer)
		assert.ErrorIs(t, err, ErrScheduled)
		_, _, err = store2.prepareBypass(ctx, "agent-1", "sched-1", dispatchBypassPreempt)
		assert.ErrorIs(t, err, ErrScheduled)
	})

	t.Run("RecordPreemptBypass and ClearPreemptBypass", func(t *testing.T) {
		t.Parallel()
		_, store := newStoreFixture(t)
		_, err := store.Enqueue(ctx, NewItem{
			ID: "in-1", AgentID: "agent-1",
			Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "first",
		})
		require.NoError(t, err)

		// Idle turn refuses RecordPreemptBypass.
		_, err = store.RecordPreemptBypass(ctx, "agent-1", "in-1")
		assert.ErrorIs(t, err, ErrPreemptionState)

		// Active turn with NO background work still succeeds.
		_, _, err = store.TurnStarted(ctx, "agent-1", false)
		require.NoError(t, err)
		_, err = store.RecordPreemptBypass(ctx, "agent-1", "wrong")
		assert.ErrorIs(t, err, ErrNotHead)

		snap, err := store.RecordPreemptBypass(ctx, "agent-1", "in-1")
		require.NoError(t, err)
		assert.Equal(t, "in-1", snap.BypassBackgroundWorkInputID)

		// Non-matching ClearPreemptBypass preserves it.
		snap, err = store.ClearPreemptBypass(ctx, "agent-1", "other")
		require.NoError(t, err)
		assert.Equal(t, "in-1", snap.BypassBackgroundWorkInputID)

		// Matching ClearPreemptBypass clears it.
		snap, err = store.ClearPreemptBypass(ctx, "agent-1", "in-1")
		require.NoError(t, err)
		assert.Empty(t, snap.BypassBackgroundWorkInputID)
	})

	t.Run("operations preserving bypass_background_work_input_id", func(t *testing.T) {
		t.Parallel()
		clock := testutil.NewQuartzMock(t)
		database := newQueueDatabase(t)
		store := NewStore(database, clock)
		insertBackgroundTaskRow(t, database, "agent-1", "task-1", 1,
			leapmuxv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SHELL, bgtask.RoleTaskOutcome, "", "", bgtask.StatusRunning)

		_, err := store.Enqueue(ctx, NewItem{
			ID: "in-1", AgentID: "agent-1",
			Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "first",
		})
		require.NoError(t, err)
		future := clock.Now().UTC().Add(time.Minute)
		_, err = store.Enqueue(ctx, NewItem{
			ID: "in-sched", AgentID: "agent-1",
			Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "later",
			ScheduledAt: &future,
		})
		require.NoError(t, err)
		_, _, err = store.TurnStarted(ctx, "agent-1", false)
		require.NoError(t, err)
		_, err = store.RecordPreemptBypass(ctx, "agent-1", "in-1")
		require.NoError(t, err)

		// RefreshForBackgroundWork preserves bypass (and skips redundant bump when unchanged).
		snap, changed, err := store.RefreshForBackgroundWork(ctx, "agent-1")
		require.NoError(t, err)
		assert.False(t, changed)
		assert.Equal(t, "in-1", snap.BypassBackgroundWorkInputID)

		// RefreshSnapshot preserves bypass.
		snap, err = store.RefreshSnapshot(ctx, "agent-1")
		require.NoError(t, err)
		assert.Equal(t, "in-1", snap.BypassBackgroundWorkInputID)

		// Non-control Enqueue preserves bypass.
		snap, err = store.Enqueue(ctx, NewItem{
			ID: "in-2", AgentID: "agent-1",
			Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "second",
		})
		require.NoError(t, err)
		assert.Equal(t, "in-1", snap.BypassBackgroundWorkInputID)

		// scheduleAutoRetry and cancelAutoRetries preserve bypass.
		snap, err = store.scheduleAutoRetry(ctx, "agent-1", agent.AutoRetrySchedule{
			Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR,
		})
		require.NoError(t, err)
		assert.Equal(t, "in-1", snap.BypassBackgroundWorkInputID)
		snap, _, err = store.cancelAutoRetries(ctx, "agent-1", leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR)
		require.NoError(t, err)
		assert.Equal(t, "in-1", snap.BypassBackgroundWorkInputID)

		// MatureScheduled preserves bypass.
		clock.Advance(2 * time.Minute)
		snap, matured, err := store.MatureScheduled(ctx, "agent-1", future)
		require.NoError(t, err)
		assert.True(t, matured)
		assert.Equal(t, "in-1", snap.BypassBackgroundWorkInputID)

		// setTurnState (TurnEnded) preserves bypass so automatic drain admits in-1.
		snap, _, err = store.TurnEnded(ctx, "agent-1", false)
		require.NoError(t, err)
		assert.Equal(t, "in-1", snap.BypassBackgroundWorkInputID)

		prepared, _, err := store.PrepareDispatch(ctx, "agent-1", false)
		require.NoError(t, err)
		require.NotNil(t, prepared, "bypassed head must admit on turn end despite running background task")
		assert.Equal(t, "in-1", prepared.Item.ID)
	})

	t.Run("stale bypass clears on new turn start, cancelled auto-retry head, and matured head replacement", func(t *testing.T) {
		t.Parallel()

		// 1. TurnStarted (0 -> 1) clears a stale bypass left from a prior turn.
		t.Run("TurnStarted clears stale bypass from idle", func(t *testing.T) {
			t.Parallel()
			_, store := newStoreFixture(t)
			_, err := store.Enqueue(ctx, NewItem{
				ID: "in-1", AgentID: "agent-1",
				Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "first",
			})
			require.NoError(t, err)
			_, _, err = store.TurnStarted(ctx, "agent-1", false)
			require.NoError(t, err)
			_, err = store.RecordPreemptBypass(ctx, "agent-1", "in-1")
			require.NoError(t, err)
			snap, _, err := store.TurnEnded(ctx, "agent-1", false)
			require.NoError(t, err)
			require.Equal(t, "in-1", snap.BypassBackgroundWorkInputID)

			snap, changed, err := store.TurnStarted(ctx, "agent-1", false)
			require.NoError(t, err)
			assert.True(t, changed)
			assert.Empty(t, snap.BypassBackgroundWorkInputID, "starting a new turn from idle must clear a prior turn's bypass")
		})

		// 2. cancelAutoRetries deleting the bypassed head clears bypass.
		t.Run("cancelAutoRetries clears bypass when bypassed retry is deleted", func(t *testing.T) {
			t.Parallel()
			clock := testutil.NewQuartzMock(t)
			database := newQueueDatabase(t)
			store := NewStore(database, clock)
			snap, err := store.scheduleAutoRetry(ctx, "agent-1", agent.AutoRetrySchedule{
				Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR,
			})
			require.NoError(t, err)
			require.Len(t, snap.Items, 1)
			retryID := snap.Items[0].ID
			require.NotNil(t, snap.Items[0].ScheduledAt)
			clock.Advance(snap.Items[0].ScheduledAt.Sub(clock.Now()) + time.Second)
			_, _, err = store.TurnStarted(ctx, "agent-1", false)
			require.NoError(t, err)
			_, err = store.RecordPreemptBypass(ctx, "agent-1", retryID)
			require.NoError(t, err)

			snap, changed, err := store.cancelAutoRetries(ctx, "agent-1", leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR)
			require.NoError(t, err)
			assert.True(t, changed)
			assert.Empty(t, snap.BypassBackgroundWorkInputID, "cancelling the bypassed auto-retry must clear bypass")
		})

		// 3. MatureScheduled replacing the head clears bypass.
		t.Run("MatureScheduled clears bypass when matured item replaces head", func(t *testing.T) {
			t.Parallel()
			clock := testutil.NewQuartzMock(t)
			database := newQueueDatabase(t)
			store := NewStore(database, clock)
			future := clock.Now().UTC().Add(time.Minute)
			_, err := store.Enqueue(ctx, NewItem{
				ID: "in-sched", AgentID: "agent-1",
				Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "scheduled first",
				ScheduledAt: &future,
			})
			require.NoError(t, err)
			_, err = store.Enqueue(ctx, NewItem{
				ID: "in-imm", AgentID: "agent-1",
				Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "immediate second",
			})
			require.NoError(t, err)
			_, _, err = store.TurnStarted(ctx, "agent-1", false)
			require.NoError(t, err)
			// in-imm is the effective head while in-sched is still in the future.
			_, err = store.RecordPreemptBypass(ctx, "agent-1", "in-imm")
			require.NoError(t, err)

			clock.Advance(2 * time.Minute)
			snap, matured, err := store.MatureScheduled(ctx, "agent-1", future)
			require.NoError(t, err)
			assert.True(t, matured)
			require.Equal(t, "in-sched", snap.Items[0].ID)
			assert.Empty(t, snap.BypassBackgroundWorkInputID, "a matured item taking over the head must clear bypass for the displaced item")
		})
	})

	t.Run("operations clearing bypass_background_work_input_id", func(t *testing.T) {
		t.Parallel()
		setupWithBypass := func(t *testing.T) (*sql.DB, *Store) {
			t.Helper()
			database, store := newStoreFixture(t)
			_, err := store.Enqueue(ctx, NewItem{
				ID: "in-1", AgentID: "agent-1",
				Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "first",
			})
			require.NoError(t, err)
			_, err = store.Enqueue(ctx, NewItem{
				ID: "in-2", AgentID: "agent-1",
				Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "second",
			})
			require.NoError(t, err)
			_, _, err = store.TurnStarted(ctx, "agent-1", false)
			require.NoError(t, err)
			snap, err := store.RecordPreemptBypass(ctx, "agent-1", "in-1")
			require.NoError(t, err)
			require.Equal(t, "in-1", snap.BypassBackgroundWorkInputID)
			return database, store
		}
		assertCleared := func(t *testing.T, store *Store) {
			t.Helper()
			snap, err := store.Snapshot(ctx, "agent-1")
			require.NoError(t, err)
			assert.Empty(t, snap.BypassBackgroundWorkInputID)
		}

		t.Run("Accept clears unconditionally even when active_input_id changed", func(t *testing.T) {
			t.Parallel()
			database, store := setupWithBypass(t)
			insertBackgroundTaskRow(t, database, "agent-1", "task-1", 1,
				leapmuxv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SHELL, bgtask.RoleTaskOutcome, "", "", bgtask.StatusRunning)
			_, _, err := store.TurnEnded(ctx, "agent-1", false)
			require.NoError(t, err)
			prepared, _, err := store.PrepareDispatch(ctx, "agent-1", false)
			require.NoError(t, err)
			require.NotNil(t, prepared)
			// Simulate provider ending turn before Accept so active_input_id no longer matches.
			_, _, err = store.TurnEnded(ctx, "agent-1", false)
			require.NoError(t, err)
			_, snap, err := store.Accept(ctx, *prepared, DispatchResult{StartsTurn: true})
			require.NoError(t, err)
			assert.Empty(t, snap.BypassBackgroundWorkInputID)
		})

		t.Run("Delete", func(t *testing.T) {
			t.Parallel()
			_, store := setupWithBypass(t)
			_, err := store.Delete(ctx, "agent-1", "in-2")
			require.NoError(t, err)
			assertCleared(t, store)
		})

		t.Run("BeginEdit and Update", func(t *testing.T) {
			t.Parallel()
			_, store := setupWithBypass(t)
			_, _, _, err := store.BeginEdit(ctx, "agent-1", "in-1", "c1", false)
			require.NoError(t, err)
			assertCleared(t, store)
			// Re-set bypass directly to test Update also clears it.
			store.setBypassForTest("agent-1", "in-1")
			_, err = store.Update(ctx, UpdateRequest{
				AgentID: "agent-1", InputID: "in-1", ClientID: "c1", ExpectedVersion: 1, Text: "edited",
			})
			require.NoError(t, err)
			assertCleared(t, store)
		})

		t.Run("Move", func(t *testing.T) {
			t.Parallel()
			_, store := setupWithBypass(t)
			_, err := store.Move(ctx, "agent-1", "in-2", "in-1")
			require.NoError(t, err)
			assertCleared(t, store)
		})

		t.Run("RequeueBusy", func(t *testing.T) {
			t.Parallel()
			_, store := setupWithBypass(t)
			_, _, err := store.TurnEnded(ctx, "agent-1", false)
			require.NoError(t, err)
			prepared, _, err := store.PrepareDispatch(ctx, "agent-1", false)
			require.NoError(t, err)
			require.NotNil(t, prepared)
			_, err = store.RequeueBusy(ctx, "agent-1", prepared.Item.ID, false)
			require.NoError(t, err)
			assertCleared(t, store)
		})

		t.Run("RequeueAndPause", func(t *testing.T) {
			t.Parallel()
			_, store := setupWithBypass(t)
			_, _, err := store.TurnEnded(ctx, "agent-1", false)
			require.NoError(t, err)
			prepared, _, err := store.PrepareDispatch(ctx, "agent-1", false)
			require.NoError(t, err)
			require.NotNil(t, prepared)
			_, err = store.RequeueAndPause(ctx, "agent-1", prepared.Item.ID, assert.AnError,
				leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_AGENT_STOPPED)
			require.NoError(t, err)
			assertCleared(t, store)
		})

		t.Run("FailDispatch", func(t *testing.T) {
			t.Parallel()
			_, store := setupWithBypass(t)
			_, _, err := store.TurnEnded(ctx, "agent-1", false)
			require.NoError(t, err)
			prepared, _, err := store.PrepareDispatch(ctx, "agent-1", false)
			require.NoError(t, err)
			require.NotNil(t, prepared)
			_, err = store.FailDispatch(ctx, "agent-1", prepared.Item.ID, assert.AnError, false)
			require.NoError(t, err)
			assertCleared(t, store)
		})

		t.Run("SetPaused(true)", func(t *testing.T) {
			t.Parallel()
			_, store := setupWithBypass(t)
			_, err := store.SetPaused(ctx, "agent-1", true, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_MANUAL)
			require.NoError(t, err)
			assertCleared(t, store)
		})

		t.Run("pauseAndEndTurn and replacePauseAndEndTurn", func(t *testing.T) {
			t.Parallel()
			database, store := setupWithBypass(t)
			pauseAgentAndEndTurn(t, store, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_AGENT_STOPPED, pauseOwnerAgentStopped)
			assertCleared(t, store)

			store.setBypassForTest("agent-1", "in-1")
			tx, err := database.BeginTx(ctx, nil)
			require.NoError(t, err)
			staged := store.cloneEphemeral("agent-1")
			require.NoError(t, replacePauseAndEndTurn(ctx, tx, "agent-1", leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_DELIVERY_FAILED, pauseOwnerDelivery, &staged, time.Now()))
			require.NoError(t, store.commitEphemeral(tx, "agent-1", staged))
			assertCleared(t, store)
		})

		t.Run("pauseForPlannedRestart and finishPlannedRestart", func(t *testing.T) {
			t.Parallel()
			_, store := setupWithBypass(t)
			_, _, err := store.pauseForPlannedRestart(ctx, "agent-1")
			require.NoError(t, err)
			assertCleared(t, store)

			store.setBypassForTest("agent-1", "in-1")
			_, _, err = store.finishPlannedRestart(ctx, "agent-1", false, false)
			require.NoError(t, err)
			assertCleared(t, store)
		})

		t.Run("pauseForInputRecovery and finishInputRecovery", func(t *testing.T) {
			t.Parallel()
			_, store := setupWithBypass(t)
			_, prev, err := store.pauseForInputRecovery(ctx, "agent-1")
			require.NoError(t, err)
			assertCleared(t, store)

			store.setBypassForTest("agent-1", "in-1")
			_, _, err = store.finishInputRecovery(ctx, "agent-1", prev, true, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_UNSPECIFIED)
			require.NoError(t, err)
			assertCleared(t, store)
		})

		t.Run("placeControlReply", func(t *testing.T) {
			t.Parallel()
			_, store := setupWithBypass(t)
			_, err := store.Enqueue(ctx, NewItem{
				ID: "reply-1", AgentID: "agent-1",
				Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_QUESTION_ANSWER, Text: "yes",
			})
			require.NoError(t, err)
			assertCleared(t, store)
		})

		t.Run("Recover clears unconditionally even when idle and no unconfirmed items", func(t *testing.T) {
			t.Parallel()
			_, store := setupWithBypass(t)
			_, _, err := store.TurnEnded(ctx, "agent-1", false)
			require.NoError(t, err)
			before, err := store.Snapshot(ctx, "agent-1")
			require.NoError(t, err)
			require.False(t, before.ActiveTurn)
			require.Equal(t, "in-1", before.BypassBackgroundWorkInputID)

			snaps, err := store.Recover(ctx, nil)
			require.NoError(t, err)
			require.Len(t, snaps, 1)
			assert.Empty(t, snaps[0].BypassBackgroundWorkInputID)
			assertCleared(t, store)
		})
	})
}

func TestStoreBackgroundWorkRefreshTreeAndExemptions(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	t.Run("RefreshForBackgroundWork skips empty queue and unchanged work, bumps on state transition", func(t *testing.T) {
		t.Parallel()
		database, store := newStoreFixture(t)
		_, _, err := store.TurnEnded(ctx, "agent-1", false)
		require.NoError(t, err)
		before, err := store.Snapshot(ctx, "agent-1")
		require.NoError(t, err)

		snap, changed, err := store.RefreshForBackgroundWork(ctx, "agent-1")
		require.NoError(t, err)
		assert.False(t, changed)
		assert.Equal(t, before.Revision, snap.Revision)

		enqueued, err := store.Enqueue(ctx, NewItem{
			ID: "in-1", AgentID: "agent-1",
			Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "hello",
		})
		require.NoError(t, err)

		// Unchanged ActiveBackgroundWork (false -> false) skips write and revision bump.
		unchanged, changed, err := store.RefreshForBackgroundWork(ctx, "agent-1")
		require.NoError(t, err)
		assert.False(t, changed)
		assert.Equal(t, enqueued.Revision, unchanged.Revision)

		// Transition false -> true bumps revision and broadcasts.
		insertBackgroundTaskRow(t, database, "agent-1", "task-1", 1,
			leapmuxv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SHELL, bgtask.RoleTaskOutcome, "", "", bgtask.StatusRunning)
		refreshed, changed, err := store.RefreshForBackgroundWork(ctx, "agent-1")
		require.NoError(t, err)
		assert.True(t, changed)
		assert.True(t, refreshed.ActiveBackgroundWork)
		assert.Equal(t, enqueued.Revision+1, refreshed.Revision)

		// A second task starting while still active (true -> true) skips write and revision bump.
		insertBackgroundTaskRow(t, database, "agent-1", "task-2", 2,
			leapmuxv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SHELL, bgtask.RoleTaskOutcome, "", "", bgtask.StatusRunning)
		stillActive, changed, err := store.RefreshForBackgroundWork(ctx, "agent-1")
		require.NoError(t, err)
		assert.False(t, changed)
		assert.Equal(t, refreshed.Revision, stillActive.Revision)

		// Transition true -> false bumps revision and clears ActiveBackgroundWork.
		_, err = database.ExecContext(ctx,
			`UPDATE agent_background_tasks SET status = ? WHERE owner_agent_id = 'agent-1' AND row_key IN ('task-1', 'task-2')`,
			bgtask.StatusSucceeded)
		require.NoError(t, err)
		cleared, changed, err := store.RefreshForBackgroundWork(ctx, "agent-1")
		require.NoError(t, err)
		assert.True(t, changed)
		assert.False(t, cleared.ActiveBackgroundWork)
		assert.Equal(t, stillActive.Revision+1, cleared.Revision)

		// A second call after transition (false -> false) skips write and leaves revision unchanged.
		stillCleared, changed, err := store.RefreshForBackgroundWork(ctx, "agent-1")
		require.NoError(t, err)
		assert.False(t, changed)
		assert.Equal(t, cleared.Revision, stillCleared.Revision)
	})

	t.Run("RefreshForBackgroundWork refreshes on cold cache then deduplicates", func(t *testing.T) {
		t.Parallel()
		database, store := newStoreFixture(t)
		enqueued, err := store.Enqueue(ctx, NewItem{
			ID: "in-1", AgentID: "agent-1",
			Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "hello",
		})
		require.NoError(t, err)

		coldStore := NewStore(database, nil)
		_, known := coldStore.lastBackgroundWork("agent-1")
		require.False(t, known)

		first, changed, err := coldStore.RefreshForBackgroundWork(ctx, "agent-1")
		require.NoError(t, err)
		assert.True(t, changed)
		assert.Equal(t, enqueued.Revision+1, first.Revision)

		second, changed, err := coldStore.RefreshForBackgroundWork(ctx, "agent-1")
		require.NoError(t, err)
		assert.False(t, changed)
		assert.Equal(t, first.Revision, second.Revision)

		coldStore.ClearEphemeralState("agent-1")
		_, known = coldStore.lastBackgroundWork("agent-1")
		require.False(t, known)

		afterClear, changed, err := coldStore.RefreshForBackgroundWork(ctx, "agent-1")
		require.NoError(t, err)
		assert.True(t, changed)
		assert.Equal(t, second.Revision+1, afterClear.Revision)

		afterClearAgain, changed, err := coldStore.RefreshForBackgroundWork(ctx, "agent-1")
		require.NoError(t, err)
		assert.False(t, changed)
		assert.Equal(t, afterClear.Revision, afterClearAgain.Revision)
	})

	t.Run("RefreshForBackgroundWork and ListQueuedAgentIDsInTree reject invalid agent IDs", func(t *testing.T) {
		t.Parallel()
		_, store := newStoreFixture(t)

		for _, invalidID := range []string{"", "a\x00b"} {
			_, changed, err := store.RefreshForBackgroundWork(ctx, invalidID)
			assert.ErrorIs(t, err, ErrInvalidInput)
			assert.False(t, changed)
		}

		ids, err := store.ListQueuedAgentIDsInTree(ctx, "")
		assert.ErrorIs(t, err, ErrInvalidInput)
		assert.Nil(t, ids)
	})

	t.Run("ListQueuedAgentIDsInTree finds queued root, child, and closed child while excluding empty queues", func(t *testing.T) {
		t.Parallel()
		database, store := newStoreFixture(t)
		createChildAgentRow(t, database, "child-queued", "agent-1")
		createChildAgentRow(t, database, "child-empty", "agent-1")
		createChildAgentRow(t, database, "child-closed", "child-empty")
		_, err := database.ExecContext(ctx, `UPDATE agents SET closed_at = '2026-01-01T00:00:00Z' WHERE id = 'child-closed'`)
		require.NoError(t, err)

		for _, agentID := range []string{"agent-1", "child-queued", "child-closed"} {
			_, err := store.Enqueue(ctx, NewItem{
				ID: "in-" + agentID, AgentID: agentID,
				Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "hi",
			})
			require.NoError(t, err)
		}

		ids, err := store.ListQueuedAgentIDsInTree(ctx, "agent-1")
		require.NoError(t, err)
		assert.Equal(t, []string{"agent-1", "child-closed", "child-queued"}, ids)
	})

	t.Run("control replies, approved context replacement, and PrepareRetry admit while background work runs", func(t *testing.T) {
		t.Parallel()
		for _, kind := range []leapmuxv1.AgentInputKind{
			leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_QUESTION_ANSWER,
			leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_CONTROL_FEEDBACK,
		} {
			database, store := newStoreFixture(t)
			insertBackgroundTaskRow(t, database, "agent-1", "task-1", 1,
				leapmuxv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SHELL, bgtask.RoleTaskOutcome, "", "", bgtask.StatusRunning)
			_, err := store.Enqueue(ctx, NewItem{ID: "reply", AgentID: "agent-1", Kind: kind, Text: "ok"})
			require.NoError(t, err)
			prepared, _, err := store.PrepareDispatch(ctx, "agent-1", false)
			require.NoError(t, err)
			require.NotNil(t, prepared, "control reply kind %v must admit despite active background work", kind)
		}

		// Approved requiresContextReplacement admits despite active background work.
		database, store := newStoreFixture(t)
		insertBackgroundTaskRow(t, database, "agent-1", "task-1", 1,
			leapmuxv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SHELL, bgtask.RoleTaskOutcome, "", "", bgtask.StatusRunning)
		_, err := store.Enqueue(ctx, NewItem{
			ID: "plan", AgentID: "agent-1",
			Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_PLAN_EXECUTION, Text: "execute", PrepareContext: true,
		})
		require.NoError(t, err)
		require.NoError(t, dbtest.StoreControlResponse(ctx, database, gendb.ControlResponseAnswer{
			AgentID: "agent-1", RequestID: "req-1", ClaimToken: "claim-1",
			State:   leapmuxv1.ControlDeliveryState_CONTROL_DELIVERY_STATE_COMPLETED,
			InputID: "plan", PlanApprovalSettings: []byte(`{}`),
		}))
		prepared, _, err := store.PrepareDispatch(ctx, "agent-1", false)
		require.NoError(t, err)
		require.NotNil(t, prepared, "approved context replacement must admit despite active background work")

		// PrepareRetry admits despite active background work.
		database2, store2 := newStoreFixture(t)
		insertBackgroundTaskRow(t, database2, "agent-1", "task-1", 1,
			leapmuxv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SHELL, bgtask.RoleTaskOutcome, "", "", bgtask.StatusRunning)
		_, err = store2.Enqueue(ctx, NewItem{
			ID: "retry-1", AgentID: "agent-1",
			Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "try again",
		})
		require.NoError(t, err)
		prepared, _, err = store2.PrepareRetry(ctx, "agent-1")
		require.NoError(t, err)
		require.NotNil(t, prepared, "PrepareRetry must admit despite active background work")
	})

	t.Run("scheduled future head stays refused behind active background work", func(t *testing.T) {
		t.Parallel()
		clock := testutil.NewQuartzMock(t)
		database := newQueueDatabase(t)
		store := NewStore(database, clock)
		insertBackgroundTaskRow(t, database, "agent-1", "task-1", 1,
			leapmuxv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SHELL, bgtask.RoleTaskOutcome, "", "", bgtask.StatusRunning)

		future := clock.Now().UTC().Add(time.Minute)
		snap, err := store.Enqueue(ctx, NewItem{
			ID: "future", AgentID: "agent-1",
			Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "wait",
			ScheduledAt: &future,
		})
		require.NoError(t, err)
		require.Len(t, snap.Items, 1)
		assert.False(t, snap.Items[0].CanSteer)
		assert.False(t, snap.Items[0].CanPreempt)

		prepared, _, err := store.PrepareDispatch(ctx, "agent-1", false)
		require.NoError(t, err)
		assert.Nil(t, prepared)
	})
}

func TestStoreRequeueBusyIncrementsRevisionOnce(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	_, store := newStoreFixture(t)

	_, err := store.Enqueue(ctx, NewItem{
		ID: "in-1", AgentID: "agent-1",
		Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "first",
	})
	require.NoError(t, err)
	prepared, before, err := store.PrepareDispatch(ctx, "agent-1", false)
	require.NoError(t, err)
	require.NotNil(t, prepared)

	after, err := store.RequeueBusy(ctx, "agent-1", prepared.Item.ID, true)
	require.NoError(t, err)
	assert.Equal(t, before.Revision+1, after.Revision, "RequeueBusy must increment revision by exactly 1")
	assert.True(t, after.ActiveTurnSteerable)
}

func TestStoreRecoveryPausesQueuedAgentHeldByActiveBackgroundWork(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	database, store := newStoreFixture(t)
	insertBackgroundTaskRow(t, database, "agent-1", "bg-1", 1,
		leapmuxv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SHELL, bgtask.RoleTaskOutcome, "", "", bgtask.StatusRunning)

	_, err := store.Enqueue(ctx, NewItem{
		ID: "in-1", AgentID: "agent-1",
		Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "wait for bg",
	})
	require.NoError(t, err)

	snapshots, err := NewStore(database, nil).Recover(ctx, nil)
	require.NoError(t, err)
	require.Len(t, snapshots, 1)
	assert.True(t, snapshots[0].Paused)
	assert.Equal(t, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_INTERRUPTED, snapshots[0].PauseReason)
	assert.Equal(t, pauseOwnerRecovery, readQueuePauseState(t, database).owner)
}

func TestStoreBackgroundWorkQueriesUseCoveringIndexes(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	database, _ := newStoreFixture(t)

	explain := func(query string, args ...any) string {
		t.Helper()
		rows, err := database.QueryContext(ctx, "EXPLAIN QUERY PLAN "+query, args...)
		require.NoError(t, err)
		defer func() { assert.NoError(t, rows.Close()) }()
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

	rootPlan := explain(`
		SELECT EXISTS (
			SELECT 1 FROM agent_background_tasks
			WHERE owner_agent_id = ? AND status IN (?, ?)
		)`, "agent-1", bgtask.StatusPending, bgtask.StatusRunning)
	assert.Contains(t, rootPlan, "COVERING INDEX idx_agent_background_tasks_owner_status")

	childPlan := explain(`
		WITH RECURSIVE descendants(child_agent_id, status) AS (
			SELECT child_agent_id, status
			FROM agent_background_tasks
			WHERE parent_agent_id = ?
			UNION
			SELECT b.child_agent_id, b.status
			FROM agent_background_tasks b
			JOIN descendants d ON d.child_agent_id <> '' AND b.parent_agent_id = d.child_agent_id
		)
		SELECT EXISTS (SELECT 1 FROM descendants WHERE status IN (?, ?))`,
		"child-1", bgtask.StatusPending, bgtask.StatusRunning)
	assert.Contains(t, childPlan, "COVERING INDEX idx_agent_background_tasks_parent_child_status")
}

func (s *Store) ephemeralForTest(agentID string) agentEphemeralState {
	return s.cloneEphemeral(agentID)
}

func (s *Store) isRestartingForTest(agentID string) bool {
	return s.cloneEphemeral(agentID).restarting
}

func (s *Store) setRestartingForTest(agentID string, restarting bool) {
	s.ephemeralMu.Lock()
	defer s.ephemeralMu.Unlock()
	state := s.ephemeral[agentID].clone()
	state.restarting = restarting
	if state.isZero() {
		delete(s.ephemeral, agentID)
	} else {
		s.ephemeral[agentID] = state
	}
}

func (s *Store) setBypassForTest(agentID, inputID string) {
	s.ephemeralMu.Lock()
	defer s.ephemeralMu.Unlock()
	state := s.ephemeral[agentID].clone()
	state.bypassBackgroundWorkInputID = inputID
	if state.isZero() {
		delete(s.ephemeral, agentID)
	} else {
		s.ephemeral[agentID] = state
	}
}

func TestStoreRecoverClearsStaleTurnMetadataWhenTurnAlreadyInactive(t *testing.T) {
	t.Parallel()
	_, store := newStoreFixture(t)
	ctx := t.Context()

	_, err := store.SetPaused(ctx, "agent-1", false, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_UNSPECIFIED)
	require.NoError(t, err)

	store.ephemeralMu.Lock()
	store.ephemeral["agent-1"] = agentEphemeralState{
		activeTurnSteerable: true,
		activeInputID:       "stale-in",
	}
	store.ephemeralMu.Unlock()

	_, err = store.Recover(ctx, nil)
	require.NoError(t, err)

	ephem := store.ephemeralForTest("agent-1")
	assert.False(t, ephem.activeTurnSteerable)
	assert.Empty(t, ephem.activeInputID)
}

func TestStoreFailDispatchClearsReservedSeq(t *testing.T) {
	t.Parallel()
	_, store := newStoreFixture(t)
	ctx := t.Context()

	_, err := store.Enqueue(ctx, NewItem{
		ID: "item-1", AgentID: "agent-1",
		Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "hello",
	})
	require.NoError(t, err)
	prepared, _, err := store.PrepareDispatch(ctx, "agent-1", false)
	require.NoError(t, err)
	require.NotNil(t, prepared)
	require.Positive(t, store.ephemeralForTest("agent-1").reservedSeqs["item-1"])

	snapshot, err := store.FailDispatch(ctx, "agent-1", "item-1", errors.New("boom"), false)
	require.NoError(t, err)
	require.Len(t, snapshot.Items, 1)
	assert.Equal(t, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_FAILED, snapshot.Items[0].State)
	assert.Zero(t, store.ephemeralForTest("agent-1").reservedSeqs["item-1"])
}

func TestStoreFinishPlannedRestartClearsStaleTurnFieldsWhenPauseUnchanged(t *testing.T) {
	t.Parallel()

	t.Run("inactive turn with unchanged pause clears stale metadata", func(t *testing.T) {
		t.Parallel()
		_, store := newStoreFixture(t)
		ctx := t.Context()

		_, err := store.SetPaused(ctx, "agent-1", true, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_MANUAL)
		require.NoError(t, err)
		_, created, err := store.pauseForPlannedRestart(ctx, "agent-1")
		require.NoError(t, err)
		require.False(t, created)

		store.ephemeralMu.Lock()
		state := store.ephemeral["agent-1"].clone()
		state.activeTurnSteerable = true
		state.activeInputID = "stale-in"
		store.ephemeral["agent-1"] = state
		store.ephemeralMu.Unlock()

		snapshot, changed, err := store.finishPlannedRestart(ctx, "agent-1", true, true)
		require.NoError(t, err)
		assert.False(t, changed)
		assert.True(t, snapshot.Paused)
		assert.Equal(t, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_MANUAL, snapshot.PauseReason)
		ephem := store.ephemeralForTest("agent-1")
		assert.False(t, ephem.restarting)
		assert.False(t, ephem.activeTurnSteerable)
		assert.Empty(t, ephem.activeInputID)
	})

	t.Run("active turn that continues preserves live metadata", func(t *testing.T) {
		t.Parallel()
		_, store := newStoreFixture(t)
		ctx := t.Context()

		_, _, err := store.TurnStarted(ctx, "agent-1", true)
		require.NoError(t, err)
		store.ephemeralMu.Lock()
		state := store.ephemeral["agent-1"].clone()
		state.activeInputID = "live-in"
		store.ephemeral["agent-1"] = state
		store.ephemeralMu.Unlock()

		_, _, err = store.pauseForPlannedRestart(ctx, "agent-1")
		require.NoError(t, err)
		snapshot, _, err := store.finishPlannedRestart(ctx, "agent-1", false, false)
		require.NoError(t, err)
		assert.True(t, snapshot.ActiveTurn)
		ephem := store.ephemeralForTest("agent-1")
		assert.False(t, ephem.restarting)
		assert.True(t, ephem.activeTurnSteerable)
		assert.Equal(t, "live-in", ephem.activeInputID)
	})
}

func TestStoreCancelAutoRetriesClearsEditLockOnDeletedItem(t *testing.T) {
	t.Parallel()
	_, store := newStoreFixture(t)
	ctx := t.Context()

	retrySnap, err := store.scheduleAutoRetry(ctx, "agent-1", agent.AutoRetrySchedule{
		Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR,
	})
	require.NoError(t, err)
	require.Len(t, retrySnap.Items, 1)
	retryID := retrySnap.Items[0].ID

	_, err = store.Enqueue(ctx, NewItem{
		ID: "ordinary-1", AgentID: "agent-1",
		Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "ordinary",
	})
	require.NoError(t, err)

	_, _, _, err = store.BeginEdit(ctx, "agent-1", retryID, "client-1", false)
	require.NoError(t, err)

	_, deleted, err := store.cancelAutoRetries(ctx, "agent-1", leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR)
	require.NoError(t, err)
	require.True(t, deleted)

	_, _, _, err = store.BeginEdit(ctx, "agent-1", "ordinary-1", "client-2", false)
	require.NoError(t, err, "deleting the edited auto-retry item must release the edit lock")
}

func TestStoreClearEphemeralStateOnRetireAgent(t *testing.T) {
	t.Parallel()
	_, store := newStoreFixture(t)
	ctx := t.Context()

	_, err := store.Enqueue(ctx, NewItem{
		ID: "in-1", AgentID: "agent-1",
		Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "hello",
	})
	require.NoError(t, err)
	_, _, _, err = store.BeginEdit(ctx, "agent-1", "in-1", "client-1", false)
	require.NoError(t, err)
	store.setRestartingForTest("agent-1", true)
	store.setBypassForTest("agent-1", "in-1")
	store.ephemeralMu.Lock()
	state := store.ephemeral["agent-1"].clone()
	state.setReservedSeq("in-1", 7)
	store.ephemeral["agent-1"] = state
	store.ephemeralMu.Unlock()

	_, knownBefore := store.lastBackgroundWork("agent-1")
	require.True(t, knownBefore)

	manager := NewManager(store, &recordingDispatcher{}, &recordingObserver{})
	t.Cleanup(manager.StopAndWait)
	require.NoError(t, manager.RetireAgent(ctx, "agent-1"))

	assert.True(t, store.ephemeralForTest("agent-1").isZero())
	store.ephemeralMu.RLock()
	_, exists := store.ephemeral["agent-1"]
	store.ephemeralMu.RUnlock()
	assert.False(t, exists)
	_, known := store.lastBackgroundWork("agent-1")
	assert.False(t, known)
}

func TestStoreFreshInstanceAfterRestartStartsWithCleanEphemeralState(t *testing.T) {
	t.Parallel()
	database, store1 := newStoreFixture(t)
	ctx := t.Context()

	_, err := store1.Enqueue(ctx, NewItem{
		ID: "in-1", AgentID: "agent-1",
		Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "queued",
	})
	require.NoError(t, err)
	_, _, err = store1.TurnStarted(ctx, "agent-1", true)
	require.NoError(t, err)
	_, _, _, err = store1.BeginEdit(ctx, "agent-1", "in-1", "client-1", false)
	require.NoError(t, err)
	store1.setRestartingForTest("agent-1", true)
	store1.setBypassForTest("agent-1", "in-1")

	store2 := NewStore(database, nil)
	snapshots, err := store2.Recover(ctx, nil)
	require.NoError(t, err)
	require.Len(t, snapshots, 1)
	assert.True(t, snapshots[0].Paused)
	assert.Equal(t, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_INTERRUPTED, snapshots[0].PauseReason)
	ephem := store2.ephemeralForTest("agent-1")
	assert.False(t, ephem.restarting)
	assert.Empty(t, ephem.editItemID)
	assert.Empty(t, ephem.bypassBackgroundWorkInputID)
	assert.Empty(t, ephem.reservedSeqs)
}

func (s *Store) getItem(ctx context.Context, tx gendb.DBTX, agentID, inputID string, loadAttachments bool) (StoredItem, []Attachment, bool, error) {
	ephem := s.cloneEphemeral(agentID)
	return getItemWithEphemeral(ctx, tx, agentID, inputID, loadAttachments, &ephem)
}

func TestStoreAtomicSteerAndBypassPreconditions(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	t.Run("PrepareSteer atomically bypasses active background work when idle", func(t *testing.T) {
		t.Parallel()
		database, store := newStoreFixture(t)
		insertBackgroundTaskRow(t, database, "agent-1", "task-1", 1,
			leapmuxv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SHELL, bgtask.RoleTaskOutcome, "", "", bgtask.StatusRunning)
		_, err := store.Enqueue(ctx, NewItem{
			ID: "in-1", AgentID: "agent-1",
			Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "first",
		})
		require.NoError(t, err)

		prepared, snap, err := store.PrepareSteer(ctx, "agent-1", "in-1", true)
		require.NoError(t, err)
		require.NotNil(t, prepared)
		assert.False(t, prepared.Steering, "idle background-work bypass must prepare an ordinary dispatch, not a mid-turn steer")
		assert.Positive(t, prepared.ReservedSeq)
		assert.True(t, snap.ActiveTurn)
	})

	t.Run("prepareBypass refuses paused idle queue without active background work", func(t *testing.T) {
		t.Parallel()
		_, store := newStoreFixture(t)
		_, err := store.Enqueue(ctx, NewItem{
			ID: "in-1", AgentID: "agent-1",
			Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "first",
		})
		require.NoError(t, err)
		_, err = store.SetPaused(ctx, "agent-1", true, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_MANUAL)
		require.NoError(t, err)

		_, _, err = store.prepareBypass(ctx, "agent-1", "in-1", dispatchBypassSteer)
		assert.ErrorIs(t, err, ErrSteeringState, "without active background work, bypass steer must not dispatch through a manual pause")
	})

	t.Run("PrepareSteer on paused idle queue admits only with active background work", func(t *testing.T) {
		t.Parallel()
		database, store := newStoreFixture(t)
		_, err := store.Enqueue(ctx, NewItem{
			ID: "in-1", AgentID: "agent-1",
			Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "first",
		})
		require.NoError(t, err)
		_, err = store.SetPaused(ctx, "agent-1", true, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_MANUAL)
		require.NoError(t, err)

		// Without active background work, idle PrepareSteer returns ErrTurnEnded.
		_, _, err = store.PrepareSteer(ctx, "agent-1", "in-1", true)
		assert.ErrorIs(t, err, ErrTurnEnded)

		// With active background work, PrepareSteer bypasses while preserving the pause.
		insertBackgroundTaskRow(t, database, "agent-1", "task-1", 1,
			leapmuxv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SHELL, bgtask.RoleTaskOutcome, "", "", bgtask.StatusRunning)
		prepared, snap, err := store.PrepareSteer(ctx, "agent-1", "in-1", true)
		require.NoError(t, err)
		require.NotNil(t, prepared)
		assert.False(t, prepared.Steering)
		assert.True(t, snap.Paused)
		assert.Positive(t, prepared.ReservedSeq)
	})

	t.Run("PrepareSteer returns ErrScheduled for future head on idle queue with active background work", func(t *testing.T) {
		t.Parallel()
		clock := testutil.NewQuartzMock(t)
		database := newQueueDatabase(t)
		store := NewStore(database, clock)
		insertBackgroundTaskRow(t, database, "agent-1", "task-1", 1,
			leapmuxv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SHELL, bgtask.RoleTaskOutcome, "", "", bgtask.StatusRunning)

		future := clock.Now().UTC().Add(time.Minute)
		_, err := store.Enqueue(ctx, NewItem{
			ID: "future", AgentID: "agent-1",
			Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "wait",
			ScheduledAt: &future,
		})
		require.NoError(t, err)

		prepared, _, err := store.PrepareSteer(ctx, "agent-1", "future", false)
		assert.ErrorIs(t, err, ErrScheduled)
		assert.Nil(t, prepared)
	})

	t.Run("prepareBypass preempt refuses unpaused idle queue without active background work", func(t *testing.T) {
		t.Parallel()
		_, store := newStoreFixture(t)
		_, err := store.Enqueue(ctx, NewItem{
			ID: "in-1", AgentID: "agent-1",
			Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "first",
		})
		require.NoError(t, err)

		prepared, _, err := store.prepareBypass(ctx, "agent-1", "in-1", dispatchBypassPreempt)
		assert.ErrorIs(t, err, ErrPreemptionState)
		assert.Nil(t, prepared)
	})

	t.Run("PreparePreempt refuses future head, control replies on idle background work, and items behind AWAITING_ECHO", func(t *testing.T) {
		t.Parallel()

		t.Run("scheduled future head during active turn or active background work", func(t *testing.T) {
			t.Parallel()
			clock := testutil.NewQuartzMock(t)
			database := newQueueDatabase(t)
			store := NewStore(database, clock)

			future := clock.Now().UTC().Add(time.Minute)
			_, err := store.Enqueue(ctx, NewItem{
				ID: "future", AgentID: "agent-1",
				Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "later",
				ScheduledAt: &future,
			})
			require.NoError(t, err)

			_, _, err = store.TurnStarted(ctx, "agent-1", false)
			require.NoError(t, err)
			prepared, snap, err := store.PreparePreempt(ctx, "agent-1", "future")
			assert.ErrorIs(t, err, ErrPreemptionState)
			assert.Nil(t, prepared)
			assert.Equal(t, "agent-1", snap.AgentID)
			require.Len(t, snap.Items, 1)

			_, _, err = store.TurnEnded(ctx, "agent-1", false)
			require.NoError(t, err)
			insertBackgroundTaskRow(t, database, "agent-1", "task-1", 1,
				leapmuxv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SHELL, bgtask.RoleTaskOutcome, "", "", bgtask.StatusRunning)
			prepared, snap, err = store.PreparePreempt(ctx, "agent-1", "future")
			assert.ErrorIs(t, err, ErrPreemptionState)
			assert.Nil(t, prepared)
			assert.Equal(t, "agent-1", snap.AgentID)
			require.Len(t, snap.Items, 1)
		})

		t.Run("control replies while idle with active background work", func(t *testing.T) {
			t.Parallel()
			for _, kind := range []leapmuxv1.AgentInputKind{
				leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_QUESTION_ANSWER,
				leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_CONTROL_FEEDBACK,
			} {
				database, store := newStoreFixture(t)
				insertBackgroundTaskRow(t, database, "agent-1", "task-1", 1,
					leapmuxv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SHELL, bgtask.RoleTaskOutcome, "", "", bgtask.StatusRunning)
				_, err := store.Enqueue(ctx, NewItem{
					ID: "reply", AgentID: "agent-1", Kind: kind, Text: "ok",
				})
				require.NoError(t, err)

				prepared, snap, err := store.PreparePreempt(ctx, "agent-1", "reply")
				assert.ErrorIs(t, err, ErrPreemptionState, "kind %v already bypasses idle background work", kind)
				assert.Nil(t, prepared)
				assert.Equal(t, "agent-1", snap.AgentID)
				require.Len(t, snap.Items, 1)
			}
		})

		t.Run("head behind an AWAITING_ECHO row during an active turn", func(t *testing.T) {
			t.Parallel()
			_, store := newStoreFixture(t)
			_, _, err := store.TurnStarted(ctx, "agent-1", true)
			require.NoError(t, err)
			_, err = store.Enqueue(ctx, NewItem{
				ID: "echo-1", AgentID: "agent-1",
				Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "first",
			})
			require.NoError(t, err)
			_, err = store.Enqueue(ctx, NewItem{
				ID: "in-2", AgentID: "agent-1",
				Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "second",
			})
			require.NoError(t, err)

			steered, _, err := store.PrepareSteer(ctx, "agent-1", "echo-1", true)
			require.NoError(t, err)
			require.NotNil(t, steered)
			_, err = store.HoldForEcho(ctx, *steered)
			require.NoError(t, err)

			prepared, snap, err := store.PreparePreempt(ctx, "agent-1", "in-2")
			assert.ErrorIs(t, err, ErrPreemptionState)
			assert.Nil(t, prepared)
			assert.Equal(t, "agent-1", snap.AgentID)
			require.Len(t, snap.Items, 2)
		})
	})

	t.Run("PreparePreempt atomically validates preconditions and selects bypass or interrupt", func(t *testing.T) {
		t.Parallel()
		clock := testutil.NewQuartzMock(t)
		database := newQueueDatabase(t)
		store := NewStore(database, clock)

		// Empty queue -> ErrPreemptionState with snapshot.
		prepared, snap, err := store.PreparePreempt(ctx, "agent-1", "in-1")
		assert.ErrorIs(t, err, ErrPreemptionState)
		assert.Nil(t, prepared)
		assert.Equal(t, "agent-1", snap.AgentID)

		_, err = store.Enqueue(ctx, NewItem{
			ID: "in-1", AgentID: "agent-1",
			Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "first",
		})
		require.NoError(t, err)

		// Idle without background work -> ErrPreemptionState.
		_, snap, err = store.PreparePreempt(ctx, "agent-1", "in-1")
		assert.ErrorIs(t, err, ErrPreemptionState)
		assert.Len(t, snap.Items, 1)

		// Wrong or empty ID -> ErrNotHead with snapshot.
		_, snap, err = store.PreparePreempt(ctx, "agent-1", "other")
		assert.ErrorIs(t, err, ErrNotHead)
		assert.Len(t, snap.Items, 1)
		_, _, err = store.PreparePreempt(ctx, "agent-1", "")
		assert.ErrorIs(t, err, ErrNotHead)

		// Active turn while paused or being edited -> ErrPreemptionState.
		_, _, err = store.TurnStarted(ctx, "agent-1", false)
		require.NoError(t, err)
		_, err = store.SetPaused(ctx, "agent-1", true, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_MANUAL)
		require.NoError(t, err)
		_, _, err = store.PreparePreempt(ctx, "agent-1", "in-1")
		assert.ErrorIs(t, err, ErrPreemptionState)
		_, err = store.RecordPreemptBypass(ctx, "agent-1", "in-1")
		assert.ErrorIs(t, err, ErrPreemptionState)
		_, err = store.SetPaused(ctx, "agent-1", false, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_UNSPECIFIED)
		require.NoError(t, err)

		_, _, _, err = store.BeginEdit(ctx, "agent-1", "in-1", "client-1", false)
		require.NoError(t, err)
		_, _, err = store.PreparePreempt(ctx, "agent-1", "in-1")
		assert.ErrorIs(t, err, ErrPreemptionState)
		_, err = store.CancelEdit(ctx, "agent-1", "in-1", "client-1")
		require.NoError(t, err)

		// Active turn, unpaused, unedited -> records bypass without reserving dispatch.
		prepared, snap, err = store.PreparePreempt(ctx, "agent-1", "in-1")
		require.NoError(t, err)
		assert.Nil(t, prepared)
		assert.Equal(t, "in-1", snap.BypassBackgroundWorkInputID)

		// Idle with active background work -> reserves ordinary dispatch atomically.
		_, err = store.ClearPreemptBypass(ctx, "agent-1", "in-1")
		require.NoError(t, err)
		_, _, err = store.TurnEnded(ctx, "agent-1", false)
		require.NoError(t, err)
		insertBackgroundTaskRow(t, database, "agent-1", "task-1", 1,
			leapmuxv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SHELL, bgtask.RoleTaskOutcome, "", "", bgtask.StatusRunning)

		prepared, snap, err = store.PreparePreempt(ctx, "agent-1", "in-1")
		require.NoError(t, err)
		require.NotNil(t, prepared)
		assert.False(t, prepared.Steering)
		assert.Positive(t, prepared.ReservedSeq)
		assert.True(t, snap.ActiveTurn)
		assert.Equal(t, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DISPATCHING, snap.Items[0].State)
	})
}

func TestStoreRecoverAtomicBeforeRecover(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	markAllInterrupted := func(txCtx context.Context, tx *sql.Tx) error {
		now := time.Now().UTC().Truncate(time.Millisecond)
		_, err := gendb.New(tx).MarkAllActiveAgentBackgroundTasksInterrupted(txCtx, gendb.MarkAllActiveAgentBackgroundTasksInterruptedParams{
			Status:         leapmuxv1.BackgroundTaskStatus(bgtask.StatusInterrupted),
			MinFinalStatus: leapmuxv1.BackgroundTaskStatus(bgtask.MinFinalStatus),
			EndedAt:        sqltime.SQLiteNullTimeOf(now),
			UpdatedAt:      sqltime.NewSQLiteTime(now),
		})
		return err
	}

	t.Run("pauses queues held by active background work and snapshots post-sweep ActiveBackgroundWork false", func(t *testing.T) {
		t.Parallel()
		database, store := newStoreFixture(t)
		createChildAgentRow(t, database, "child-1", "agent-1")
		createChildAgentRow(t, database, "grandchild-1", "child-1")
		require.NoError(t, gendb.New(database).CreateAgent(ctx, gendb.CreateAgentParams{
			ID: "agent-clean", WorkingDir: t.TempDir(),
			AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
		}))

		// Root agent-1 has an active shell task; child-1 has an active descendant task.
		insertBackgroundTaskRow(t, database, "agent-1", "root-shell", 1,
			leapmuxv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SHELL, bgtask.RoleTaskOutcome, "", "", bgtask.StatusRunning)
		insertBackgroundTaskRow(t, database, "agent-1", "child-subagent", 2,
			leapmuxv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SUBAGENT, bgtask.RoleChildTask, "grandchild-1", "child-1", bgtask.StatusPending)

		for _, agentID := range []string{"agent-1", "child-1", "agent-clean"} {
			_, err := store.Enqueue(ctx, NewItem{
				ID: "in-" + agentID, AgentID: agentID,
				Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "queued",
			})
			require.NoError(t, err)
		}
		_, _, _, err := store.BeginEdit(ctx, "agent-1", "in-agent-1", "client-1", false)
		require.NoError(t, err)
		store.setRestartingForTest("child-1", true)
		store.setBypassForTest("agent-clean", "in-agent-clean")

		snapshots, err := store.Recover(ctx, markAllInterrupted)
		require.NoError(t, err)
		byAgent := make(map[string]Snapshot, len(snapshots))
		for _, snap := range snapshots {
			byAgent[snap.AgentID] = snap
		}
		require.Len(t, byAgent, 3)

		// Both agent-1 and child-1 were waiting on active background work before the sweep,
		// so both pause with INTERRUPTED while reporting ActiveBackgroundWork == false.
		for _, agentID := range []string{"agent-1", "child-1"} {
			snap := byAgent[agentID]
			assert.True(t, snap.Paused, "%s must pause after interrupted background work", agentID)
			assert.Equal(t, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_INTERRUPTED, snap.PauseReason)
			assert.False(t, snap.ActiveBackgroundWork, "%s snapshot must reflect post-sweep state", agentID)
			lastActive, known := store.lastBackgroundWork(agentID)
			assert.True(t, known)
			assert.False(t, lastActive)
			assert.True(t, store.ephemeralForTest(agentID).isZero())
		}

		// agent-clean had no active background work, so it remains unpaused and its ephemeral bypass is cleared.
		clean := byAgent["agent-clean"]
		assert.False(t, clean.Paused)
		assert.False(t, clean.ActiveBackgroundWork)
		assert.Empty(t, clean.BypassBackgroundWorkInputID)
		assert.True(t, store.ephemeralForTest("agent-clean").isZero())

		// Both background tasks are now interrupted in SQLite.
		var remaining int
		require.NoError(t, database.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_background_tasks WHERE status IN (?, ?)`,
			bgtask.StatusPending, bgtask.StatusRunning).Scan(&remaining))
		assert.Zero(t, remaining)
	})

	t.Run("recursive descendant walk pauses multi-hop child and skips self-running sibling and empty queue", func(t *testing.T) {
		t.Parallel()
		database, store := newStoreFixture(t)
		createChildAgentRow(t, database, "child-1", "agent-1")
		createChildAgentRow(t, database, "mid", "child-1")
		createChildAgentRow(t, database, "grandchild", "mid")
		createChildAgentRow(t, database, "child-sibling", "agent-1")
		require.NoError(t, gendb.New(database).CreateAgent(ctx, gendb.CreateAgentParams{
			ID: "agent-empty", WorkingDir: t.TempDir(),
			AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
		}))

		// child-1 -> mid (finished) -> grandchild (running) exercises the recursive CTE step.
		insertBackgroundTaskRow(t, database, "agent-1", "mid-row", 1,
			leapmuxv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SUBAGENT, bgtask.RoleChildTask, "mid", "child-1", bgtask.StatusSucceeded)
		insertBackgroundTaskRow(t, database, "agent-1", "grandchild-row", 2,
			leapmuxv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SUBAGENT, bgtask.RoleChildTask, "grandchild", "mid", bgtask.StatusRunning)
		// child-sibling has only its own running canonical row and no active descendants.
		insertBackgroundTaskRow(t, database, "agent-1", "sibling-row", 3,
			leapmuxv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SUBAGENT, bgtask.RoleChildTask, "child-sibling", "agent-1", bgtask.StatusRunning)
		// agent-empty has an active shell task and a queue state row, but zero queued items.
		insertBackgroundTaskRow(t, database, "agent-empty", "empty-shell", 1,
			leapmuxv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SHELL, bgtask.RoleTaskOutcome, "", "", bgtask.StatusRunning)

		for _, agentID := range []string{"child-1", "child-sibling"} {
			_, err := store.Enqueue(ctx, NewItem{
				ID: "in-" + agentID, AgentID: agentID,
				Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "queued",
			})
			require.NoError(t, err)
		}
		_, _, err := store.TurnEnded(ctx, "agent-empty", false)
		require.NoError(t, err)

		snapshots, err := store.Recover(ctx, markAllInterrupted)
		require.NoError(t, err)
		byAgent := make(map[string]Snapshot, len(snapshots))
		for _, snap := range snapshots {
			byAgent[snap.AgentID] = snap
		}

		childSnap := byAgent["child-1"]
		assert.True(t, childSnap.Paused)
		assert.Equal(t, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_INTERRUPTED, childSnap.PauseReason)
		assert.False(t, childSnap.ActiveBackgroundWork)

		siblingSnap := byAgent["child-sibling"]
		assert.False(t, siblingSnap.Paused)
		assert.False(t, siblingSnap.ActiveBackgroundWork)

		emptySnap := byAgent["agent-empty"]
		assert.False(t, emptySnap.Paused)
		assert.False(t, emptySnap.ActiveBackgroundWork)
	})

	t.Run("beforeRecover error rolls back sweep, queue state, and ephemeral state", func(t *testing.T) {
		t.Parallel()
		database, store := newStoreFixture(t)
		insertBackgroundTaskRow(t, database, "agent-1", "root-shell", 1,
			leapmuxv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SHELL, bgtask.RoleTaskOutcome, "", "", bgtask.StatusRunning)
		_, err := store.Enqueue(ctx, NewItem{
			ID: "in-1", AgentID: "agent-1",
			Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "queued",
		})
		require.NoError(t, err)
		prepared, _, err := store.PrepareSteer(ctx, "agent-1", "in-1", false)
		require.NoError(t, err)
		require.NotNil(t, prepared)
		store.setRestartingForTest("agent-1", true)
		beforeEphem := store.ephemeralForTest("agent-1")
		beforePause := readQueuePauseState(t, database)

		wantErr := errors.New("boot sweep failed")
		snapshots, err := store.Recover(ctx, func(txCtx context.Context, tx *sql.Tx) error {
			if sweepErr := markAllInterrupted(txCtx, tx); sweepErr != nil {
				return sweepErr
			}
			return wantErr
		})
		require.ErrorIs(t, err, wantErr)
		assert.Nil(t, snapshots)

		// The background task sweep rolled back.
		var status bgtask.Status
		require.NoError(t, database.QueryRowContext(ctx, `SELECT status FROM agent_background_tasks WHERE owner_agent_id = 'agent-1' AND row_key = 'root-shell'`).Scan(&status))
		assert.Equal(t, bgtask.StatusRunning, status)

		// Queue state, item state, and in-memory ephemeral state did not change.
		assert.Equal(t, beforePause, readQueuePauseState(t, database))
		item, _, found, err := store.getItem(ctx, database, "agent-1", "in-1", false)
		require.NoError(t, err)
		require.True(t, found)
		assert.Equal(t, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DISPATCHING, item.State)
		assert.Equal(t, beforeEphem, store.ephemeralForTest("agent-1"))
	})

	t.Run("commit failure rolls back every agent and beforeRecover atomically", func(t *testing.T) {
		t.Parallel()
		database, store := newStoreFixture(t)
		require.NoError(t, gendb.New(database).CreateAgent(ctx, gendb.CreateAgentParams{
			ID: "agent-2", WorkingDir: t.TempDir(),
			AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
		}))
		insertBackgroundTaskRow(t, database, "agent-1", "root-shell", 1,
			leapmuxv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SHELL, bgtask.RoleTaskOutcome, "", "", bgtask.StatusRunning)
		_, err := store.Enqueue(ctx, NewItem{
			ID: "in-1", AgentID: "agent-1",
			Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "queued-1",
		})
		require.NoError(t, err)
		_, err = store.Enqueue(ctx, NewItem{
			ID: "in-2", AgentID: "agent-2",
			Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "queued-2",
		})
		require.NoError(t, err)
		_, _, err = store.PrepareDispatch(ctx, "agent-2", false)
		require.NoError(t, err)
		store.setRestartingForTest("agent-1", true)
		store.setRestartingForTest("agent-2", true)
		beforeEphem1 := store.ephemeralForTest("agent-1")
		beforeEphem2 := store.ephemeralForTest("agent-2")

		commitErr := errors.New("commit refused")
		store.ephemeralMu.Lock()
		store.beforeCommit = func() error { return commitErr }
		store.ephemeralMu.Unlock()
		t.Cleanup(func() {
			store.ephemeralMu.Lock()
			store.beforeCommit = nil
			store.ephemeralMu.Unlock()
		})

		snapshots, err := store.Recover(ctx, markAllInterrupted)
		require.ErrorIs(t, err, commitErr)
		assert.Nil(t, snapshots)

		// Neither the background task sweep nor either agent's queue recovery committed.
		var status bgtask.Status
		require.NoError(t, database.QueryRowContext(ctx, `SELECT status FROM agent_background_tasks WHERE owner_agent_id = 'agent-1' AND row_key = 'root-shell'`).Scan(&status))
		assert.Equal(t, bgtask.StatusRunning, status)

		var paused1, paused2 bool
		require.NoError(t, database.QueryRowContext(ctx, `SELECT pause_reason IS NOT NULL FROM agent_input_queue_state WHERE agent_id = 'agent-1'`).Scan(&paused1))
		require.NoError(t, database.QueryRowContext(ctx, `SELECT pause_reason IS NOT NULL FROM agent_input_queue_state WHERE agent_id = 'agent-2'`).Scan(&paused2))
		assert.False(t, paused1)
		assert.False(t, paused2)

		item2, _, found, err := store.getItem(ctx, database, "agent-2", "in-2", false)
		require.NoError(t, err)
		require.True(t, found)
		assert.Equal(t, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DISPATCHING, item2.State)
		assert.Equal(t, beforeEphem1, store.ephemeralForTest("agent-1"))
		assert.Equal(t, beforeEphem2, store.ephemeralForTest("agent-2"))
	})
}
