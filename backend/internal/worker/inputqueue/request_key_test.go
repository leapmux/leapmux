package inputqueue

import (
	"database/sql"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	gendb "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// inputPlace is where an input is when a retry repeats the request that
// queued it.
type inputPlace string

const (
	// placeQueued: the queue holds the input.
	placeQueued inputPlace = "queued"
	// placeAccepted: the agent took the input in a dispatch, and its
	// transcript row records it.
	placeAccepted inputPlace = "accepted"
	// placeAcceptedAtEcho: the agent took the input in a steer, and the echo
	// of the steer wrote its transcript row.
	placeAcceptedAtEcho inputPlace = "accepted at its echo"
	// placeRemoved: someone removed the input from the queue.
	placeRemoved inputPlace = "removed"
)

var inputPlaces = []inputPlace{placeQueued, placeAccepted, placeAcceptedAtEcho, placeRemoved}

// moveInputTo takes the input inputID of agent-1, the head of its queue, to
// place.
func moveInputTo(t *testing.T, store *Store, inputID string, place inputPlace) {
	t.Helper()
	ctx := t.Context()
	switch place {
	case placeQueued:
	case placeAccepted:
		prepared, _, err := store.PrepareDispatch(ctx, "agent-1", false)
		require.NoError(t, err)
		require.NotNil(t, prepared)
		require.Equal(t, inputID, prepared.Item.ID)
		_, _, err = store.Accept(ctx, *prepared, DispatchResult{StartsTurn: true})
		require.NoError(t, err)
	case placeAcceptedAtEcho:
		_, _, err := store.TurnStarted(ctx, "agent-1", true)
		require.NoError(t, err)
		prepared, _, err := store.PrepareSteer(ctx, "agent-1", inputID, true)
		require.NoError(t, err)
		require.NotNil(t, prepared)
		_, err = store.HoldForEcho(ctx, *prepared)
		require.NoError(t, err)
		accepted, _, _, err := store.AcceptEchoedSteers(ctx, "agent-1", 1, "", "")
		require.NoError(t, err)
		require.Len(t, accepted, 1)
		require.Equal(t, inputID, accepted[0].ID)
	case placeRemoved:
		_, err := store.Delete(ctx, "agent-1", inputID)
		require.NoError(t, err)
	default:
		require.FailNow(t, "unknown place", "%q", place)
	}
}

// editQueuedInput saves an edit of the queued input inputID of agent-1 as a
// browser does: it takes the edit, then saves the text, the attachments and
// the schedule.
func editQueuedInput(t *testing.T, store *Store, inputID, text string, attachments []Attachment, schedule ScheduleEdit) Snapshot {
	t.Helper()
	ctx := t.Context()
	snapshot, _, _, err := store.BeginEdit(ctx, "agent-1", inputID, "browser", false)
	require.NoError(t, err)
	index := slices.IndexFunc(snapshot.Items, func(item SnapshotItem) bool { return item.ID == inputID })
	require.GreaterOrEqual(t, index, 0, "the queue holds input %s", inputID)
	snapshot, err = store.Update(ctx, UpdateRequest{
		AgentID: "agent-1", InputID: inputID, ClientID: "browser",
		ExpectedVersion: snapshot.Items[index].Version,
		Text:            text, Attachments: attachments, Schedule: schedule,
	})
	require.NoError(t, err)
	return snapshot
}

// assertRepeatAddsNothing sends request again and asserts that the store
// takes it as the request that queued the input: it adds nothing, and it
// changes nothing.
func assertRepeatAddsNothing(t *testing.T, store *Store, request NewItem) Snapshot {
	t.Helper()
	before, err := store.Snapshot(t.Context(), request.AgentID)
	require.NoError(t, err)
	after, receipt, err := store.enqueue(t.Context(), request, nil)
	require.NoError(t, err, "a retry of the request that queued the input matches it")
	assert.False(t, receipt.Added, "a retry adds nothing")
	assert.Equal(t, before, after, "a retry changes nothing")
	return after
}

// assertRepeatConflicts sends request, which reuses the input id of another
// request, and asserts that the store refuses it and changes nothing.
func assertRepeatConflicts(t *testing.T, store *Store, request NewItem) {
	t.Helper()
	before, err := store.Snapshot(t.Context(), request.AgentID)
	require.NoError(t, err)
	_, receipt, err := store.enqueue(t.Context(), request, nil)
	require.ErrorIs(t, err, ErrConflict)
	assert.False(t, receipt.Added)
	after, err := store.Snapshot(t.Context(), request.AgentID)
	require.NoError(t, err)
	assert.Equal(t, before, after, "a refused request changes nothing")
}

// queuedRequestKeyOf reads the idempotency key of the queue row of inputID.
func queuedRequestKeyOf(t *testing.T, database *sql.DB, inputID string) string {
	t.Helper()
	var key string
	require.NoError(t, database.QueryRowContext(t.Context(),
		`SELECT idempotency_key FROM agent_input_queue_items WHERE id = ?`, inputID).Scan(&key))
	return key
}

// transcriptRow reads the text and the idempotency key of the transcript row
// of inputID.
func transcriptRow(t *testing.T, database *sql.DB, inputID string) (text, key string) {
	t.Helper()
	var content []byte
	var compression leapmuxv1.ContentCompression
	require.NoError(t, database.QueryRowContext(t.Context(),
		`SELECT content, content_compression, idempotency_key FROM messages WHERE id = ?`, inputID).
		Scan(&content, &compression, &key))
	raw, err := msgcodec.Decompress(content, compression)
	require.NoError(t, err)
	var decoded struct {
		Content string `json:"content"`
	}
	require.NoError(t, json.Unmarshal(raw, &decoded))
	return decoded.Content, key
}

// releaseRequest is the request that the tests of this file send and retry. It
// carries an attachment, so a retry repeats the attachments too.
func releaseRequest() NewItem {
	return NewItem{
		ID: "request", AgentID: "agent-1",
		Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
		Text: "Run the release checklist", ReclassifyOnEdit: true,
		Attachments: []Attachment{{Filename: "notes.txt", MimeType: "text/plain", Data: []byte("release notes")}},
	}
}

// A retry repeats the request that queued the input, so it matches the input
// after someone edited it in the queue, wherever the input is now. The input
// keeps the edit. A request that sends the edited content under the same
// input id is another request, and it conflicts.
func TestStoreEnqueueRetryAfterAnEditMatchesTheFirstRequest(t *testing.T) {
	t.Parallel()

	const editedText = "Run the release checklist, then tag the release"
	for _, place := range inputPlaces {
		t.Run(string(place), func(t *testing.T) {
			t.Parallel()
			database, store := newStoreFixture(t)
			request := releaseRequest()
			_, err := store.Enqueue(t.Context(), request)
			require.NoError(t, err)
			editQueuedInput(t, store, request.ID, editedText, nil, KeepSchedule())
			moveInputTo(t, store, request.ID, place)

			retried := assertRepeatAddsNothing(t, store, request)
			switch place {
			case placeQueued:
				require.Len(t, retried.Items, 1)
				assert.Equal(t, editedText, retried.Items[0].TextPreview, "the queue keeps the edit")
				assert.Empty(t, retried.Items[0].Metadata, "the queue keeps the edit, which removed the attachment")
			case placeAccepted, placeAcceptedAtEcho:
				text, key := transcriptRow(t, database, request.ID)
				assert.Equal(t, editedText, text, "the agent read the edited input")
				assert.Equal(t, inputIdempotencyKey(request), key, "the transcript row keeps the key of the request that queued the input")
			case placeRemoved:
				assert.Empty(t, retried.Items, "the removed input stays out of the queue")
			}

			edited := request
			edited.Text, edited.Attachments = editedText, nil
			assertRepeatConflicts(t, store, edited)
		})
	}
}

// An input id identifies one input across the Worker, so the same request for
// another agent conflicts with the input of the first agent, wherever that
// input is now, and it queues nothing for the second agent.
func TestStoreEnqueueOfAnInputIDThatAnotherAgentTookConflicts(t *testing.T) {
	t.Parallel()

	for _, place := range inputPlaces {
		t.Run(string(place), func(t *testing.T) {
			t.Parallel()
			database, store := newStoreFixture(t)
			require.NoError(t, gendb.New(database).CreateAgent(t.Context(), gendb.CreateAgentParams{
				ID: "agent-2", WorkingDir: t.TempDir(), AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
			}))
			request := releaseRequest()
			_, err := store.Enqueue(t.Context(), request)
			require.NoError(t, err)
			moveInputTo(t, store, request.ID, place)

			forAnotherAgent := request
			forAnotherAgent.AgentID = "agent-2"
			assertRepeatConflicts(t, store, forAnotherAgent)
			snapshot, err := store.Snapshot(t.Context(), "agent-2")
			require.NoError(t, err)
			assert.Empty(t, snapshot.Items)
		})
	}
}

// The queue row computes its key once, from the request that queued it, and
// no change of the input rewrites it. A retry of that request matches after
// each change, and the transcript row of the accepted input takes the key
// over.
func TestStoreKeepsTheRequestKeyThroughEveryChangeOfTheInput(t *testing.T) {
	t.Parallel()

	clock, database, store := newPriorityFixture(t)
	ctx := t.Context()
	request := releaseRequest()
	wantKey := inputIdempotencyKey(request)
	_, err := store.Enqueue(ctx, request)
	require.NoError(t, err)
	_, err = store.Enqueue(ctx, NewItem{ID: "other", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, Text: "other"})
	require.NoError(t, err)
	assertKeyKept := func(change string) {
		t.Helper()
		assert.Equal(t, wantKey, queuedRequestKeyOf(t, database, request.ID), "the key after %s", change)
		assertRepeatAddsNothing(t, store, request)
	}
	assertKeyKept("the enqueue")

	snapshot := editQueuedInput(t, store, request.ID, "/compact", nil, KeepSchedule())
	require.Equal(t, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_COMPACT_CONTEXT, snapshot.Items[0].Kind, "the edit reclassifies the input")
	assertKeyKept("an edit that reclassifies the input")

	snapshot, err = store.Move(ctx, "agent-1", request.ID, "")
	require.NoError(t, err)
	require.Equal(t, []string{"other", request.ID}, queueSnapshotIDs(snapshot))
	snapshot, err = store.Move(ctx, "agent-1", request.ID, "other")
	require.NoError(t, err)
	require.Equal(t, []string{request.ID, "other"}, queueSnapshotIDs(snapshot))
	assertKeyKept("two moves")

	future := clock.Now().Add(time.Hour)
	snapshot = editQueuedInput(t, store, request.ID, "Run the release checklist, then tag the release", nil, ReplaceSchedule(&future))
	require.Equal(t, []string{"other", request.ID}, queueSnapshotIDs(snapshot), "the schedule holds the input behind the immediate one")
	assertKeyKept("an edit that schedules the input")
	clock.Set(future).MustWait(ctx)
	snapshot, err = store.Snapshot(ctx, "agent-1")
	require.NoError(t, err)
	require.Equal(t, []string{request.ID, "other"}, queueSnapshotIDs(snapshot), "the arrived input takes its slot at the head again")
	assertKeyKept("the arrival of its schedule")

	_, _, err = store.TurnStarted(ctx, "agent-1", true)
	require.NoError(t, err)
	prepared, _, err := store.PrepareSteer(ctx, "agent-1", request.ID, true)
	require.NoError(t, err)
	require.NotNil(t, prepared)
	_, err = store.HoldForEcho(ctx, *prepared)
	require.NoError(t, err)
	_, requeued, err := store.RequeueUnechoedSteers(ctx, "agent-1", -1)
	require.NoError(t, err)
	require.Equal(t, 1, requeued)
	assertKeyKept("a steer that the agent dropped")

	_, _, err = store.TurnEnded(ctx, "agent-1", false)
	require.NoError(t, err)
	prepared, _, err = store.PrepareDispatch(ctx, "agent-1", false)
	require.NoError(t, err)
	require.NotNil(t, prepared)
	_, err = store.FailDispatch(ctx, "agent-1", request.ID, assert.AnError, false)
	require.NoError(t, err)
	_, err = store.Retry(ctx, "agent-1", request.ID, false)
	require.NoError(t, err)
	assertKeyKept("a failed delivery that the user retried")

	moveInputTo(t, store, request.ID, placeAccepted)
	_, key := transcriptRow(t, database, request.ID)
	assert.Equal(t, wantKey, key, "the transcript row takes the key over")
	assertRepeatAddsNothing(t, store, request)
}

// The queue stores a schedule at millisecond precision, and a retry compares
// the schedule at that precision: a retry of a request whose schedule holds
// less than a millisecond matches it, and so does a retry that states the same
// instant in another zone. A schedule one millisecond later is another
// request.
func TestStoreEnqueueRetryComparesTheScheduleAtTheStoredPrecision(t *testing.T) {
	t.Parallel()

	for _, place := range []inputPlace{placeQueued, placeAccepted, placeRemoved} {
		t.Run(string(place), func(t *testing.T) {
			t.Parallel()
			clock, _, store := newPriorityFixture(t)
			at := priorityBaseTime().Add(time.Hour + 1500*time.Microsecond + 7)
			request := NewItem{
				ID: "scheduled", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
				Text: "Check the deploy", ScheduledAt: &at,
			}
			_, err := store.Enqueue(t.Context(), request)
			require.NoError(t, err)
			if place == placeAccepted {
				clock.Set(at.Add(time.Second)).MustWait(t.Context())
			}
			moveInputTo(t, store, request.ID, place)

			assertRepeatAddsNothing(t, store, request)
			inTokyo := at.In(time.FixedZone("UTC+9", 9*60*60))
			sameInstant := request
			sameInstant.ScheduledAt = &inTokyo
			assertRepeatAddsNothing(t, store, sameInstant)
			later := at.Add(time.Millisecond)
			other := request
			other.ScheduledAt = &later
			assertRepeatConflicts(t, store, other)
		})
	}
}

// An attachment with no data compares equal whether its data is nil or an
// empty slice, as bytes.Equal compares them.
func TestStoreEnqueueRetryTreatsNilAndEmptyAttachmentDataAsEqual(t *testing.T) {
	t.Parallel()

	for _, place := range inputPlaces {
		t.Run(string(place), func(t *testing.T) {
			t.Parallel()
			_, store := newStoreFixture(t)
			request := NewItem{
				ID: "empty-file", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
				Text: "Read the empty file", Attachments: []Attachment{{Filename: "empty.txt", MimeType: "text/plain", Data: []byte{}}},
			}
			_, err := store.Enqueue(t.Context(), request)
			require.NoError(t, err)
			moveInputTo(t, store, request.ID, place)

			nilData := request
			nilData.Attachments = []Attachment{{Filename: "empty.txt", MimeType: "text/plain"}}
			assertRepeatAddsNothing(t, store, nilData)
			assertRepeatAddsNothing(t, store, request)
		})
	}
}

// The schema refuses a queue row without the key of the request that queued
// it, so an insert that forgets the key fails instead of storing a row that no
// retry can match.
func TestQueueRowRequiresTheRequestKey(t *testing.T) {
	t.Parallel()

	database := newQueueDatabase(t)
	insert := func(id string, key ...any) error {
		t.Helper()
		columns, values := "id, agent_id, order_index, kind, text", "?, 'agent-1', 1, 1, 'hi'"
		args := []any{id}
		if len(key) > 0 {
			columns, values = columns+", idempotency_key", values+", ?"
			args = append(args, key...)
		}
		_, err := database.ExecContext(t.Context(), `INSERT INTO agent_input_queue_items (`+columns+`) VALUES (`+values+`)`, args...)
		return err
	}
	require.ErrorContains(t, insert("no-key"), "NOT NULL constraint failed: agent_input_queue_items.idempotency_key")
	require.ErrorContains(t, insert("empty-key", ""), "CHECK constraint failed: idempotency_key <> ''")
	require.NoError(t, insert("keyed", "input:key"))
}

// The transcript row of an input takes the key of the request that queued
// it, so a row without one is a broken item, which the write refuses instead
// of recording a row that no retry can match.
func TestInsertInputTranscriptRefusesAnItemWithoutItsRequestKey(t *testing.T) {
	t.Parallel()

	database := newQueueDatabase(t)
	tx, err := database.BeginTx(t.Context(), nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	item := StoredItem{
		ItemMetadata: ItemMetadata{OrderingMetadata: OrderingMetadata{
			ID: "keyless", AgentID: "agent-1", Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
		}},
		Text: "hi",
	}
	_, err = insertInputTranscript(t.Context(), tx, item, nil, 1, DispatchResult{})
	require.ErrorContains(t, err, "the input keyless holds no idempotency key")
	var rows int
	require.NoError(t, tx.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM messages WHERE id = 'keyless'`).Scan(&rows))
	assert.Zero(t, rows)
}
