package inputqueue

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/coder/quartz"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	"github.com/leapmux/leapmux/internal/util/ptrconv"
	"github.com/leapmux/leapmux/internal/util/sqltime"
	"github.com/leapmux/leapmux/internal/util/timefmt"
	"github.com/leapmux/leapmux/internal/worker/agent"
	workerdb "github.com/leapmux/leapmux/internal/worker/db"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

const inputIdempotencyKeyPrefix = "input:"

type Store struct {
	db         *sql.DB
	classifier CommandClassifier
	// clock supplies the instant every schedule decision reads. Priority is
	// computed from scheduled_at and this clock at read time -- never frozen
	// into order_index -- so a future item joins the immediate group the
	// moment its deadline passes.
	clock quartz.Clock
	// beforeAcceptWrite runs inside Accept, after every read and before the first
	// write. TESTS ONLY, and nil everywhere else: a second connection tries to
	// write here to prove Accept already owns write intent.
	beforeAcceptWrite func()
}

// NewStore builds a queue store over db. A nil clock installs the real one.
func NewStore(db *sql.DB, clock quartz.Clock) *Store {
	if clock == nil {
		clock = quartz.NewReal()
	}
	return &Store{db: db, classifier: ExactCommandClassifier{}, clock: clock}
}

// now reads the instant for one store operation. An operation reads it once,
// after its transaction holds the write lock, and passes it to every schedule
// decision it makes, so a single operation cannot straddle a deadline.
func (s *Store) now() time.Time {
	return s.clock.Now().UTC()
}

// Classify answers the kind that this text takes. The Manager asks BEFORE it
// calls a mutation, so it can refuse a kind the agent cannot accept without a
// database read inside the mutation's own write transaction.
func (s *Store) Classify(kind leapmuxv1.AgentInputKind, text string) leapmuxv1.AgentInputKind {
	return s.classifier.Classify(kind, text)
}

// KindAfterEdit answers the kind that an existing item takes once its text
// becomes newText. An item that does not reclassify keeps the kind it holds.
// This is a short read on its own connection, so a caller may run it before it
// opens the write transaction that applies the edit.
func (s *Store) KindAfterEdit(ctx context.Context, agentID, inputID, newText string) (leapmuxv1.AgentInputKind, error) {
	var kind leapmuxv1.AgentInputKind
	var reclassify bool
	err := s.db.QueryRowContext(ctx,
		`SELECT kind, reclassify_on_edit FROM agent_input_queue_items WHERE agent_id = ? AND id = ?`,
		agentID, inputID).Scan(&kind, &reclassify)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	if reclassify {
		return s.classifier.Classify(leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, newText), nil
	}
	return kind, nil
}

func validKind(kind leapmuxv1.AgentInputKind) bool {
	return kind >= leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE &&
		kind <= leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR
}

func validateIdentity(value string) bool {
	return value != "" && len(value) <= maxQueueIdentityByteCount && !strings.ContainsRune(value, '\x00')
}

func attachmentBytes(attachments []Attachment) int64 {
	var total int64
	for i := range attachments {
		total += int64(len(attachments[i].Data))
	}
	return total
}

// ValidateContent checks the permanent content restrictions before an input is recorded.
func ValidateContent(kind leapmuxv1.AgentInputKind, text string, attachments []Attachment) error {
	if !validKind(kind) {
		return fmt.Errorf("%w: input kind is required", ErrInvalidInput)
	}
	if strings.ContainsRune(text, '\x00') {
		return fmt.Errorf("%w: input text contains a null character", ErrInvalidInput)
	}
	if len(attachments) > MaxAttachmentsPerItem {
		return fmt.Errorf("%w: an input accepts at most %d attachments", ErrInvalidInput, MaxAttachmentsPerItem)
	}
	if kind == leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_CLEAR_CONTEXT ||
		kind == leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_COMPACT_CONTEXT {
		if len(attachments) != 0 {
			return fmt.Errorf("%w: clear and compact inputs do not accept attachments", ErrInvalidInput)
		}
	}
	if strings.TrimSpace(text) == "" && len(attachments) == 0 {
		return fmt.Errorf("%w: text or an attachment is required", ErrInvalidInput)
	}
	for i := range attachments {
		filename, mimeType := attachments[i].Filename, attachments[i].MimeType
		if filename == "" || len(filename) > MaxAttachmentFilenameBytes || len(mimeType) > MaxAttachmentMIMETypeBytes ||
			strings.ContainsRune(filename, '\x00') || strings.ContainsRune(mimeType, '\x00') {
			return fmt.Errorf("%w: attachment metadata is invalid", ErrInvalidInput)
		}
	}
	if int64(len(text))+attachmentBytes(attachments) > int64(MaxItemBytes) {
		return ErrItemTooLarge
	}
	return nil
}

func steerableInputKind(kind leapmuxv1.AgentInputKind) bool {
	return kind == leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE ||
		IsAutoRetryKind(kind) || controlReplyKind(kind)
}

// controlReplyKind reports whether the input is the user's reply to a control
// request: rejection feedback, or the answer to a question that LeapMux owns.
// Both take the same place before future queued work.
func controlReplyKind(kind leapmuxv1.AgentInputKind) bool {
	return kind == leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_CONTROL_FEEDBACK ||
		kind == leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_QUESTION_ANSWER
}

// automaticSteeringKind selects inputs that enter an active turn without a separate Steer action.
func automaticSteeringKind(kind leapmuxv1.AgentInputKind) bool {
	return kind == leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_QUESTION_ANSWER
}

// lockedInputState protects an input that the provider can already read.
func lockedInputState(state leapmuxv1.AgentInputState) bool {
	return state == leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DISPATCHING || state == leapmuxv1.AgentInputState_AGENT_INPUT_STATE_AWAITING_ECHO
}

func (s *Store) Enqueue(ctx context.Context, input NewItem) (Snapshot, error) {
	snapshot, _, err := s.enqueue(ctx, input, nil)
	return snapshot, err
}

// enqueue queues input, applies the extra database mutation, and commits both
// with the snapshot. It reports whether this call added the item. A repeat of an
// item that the queue holds, or that it already delivered, adds nothing: it
// answers the current snapshot and false.
func (s *Store) enqueue(ctx context.Context, input NewItem, apply func(*sql.Tx) error) (Snapshot, bool, error) {
	if !validateIdentity(input.ID) || !validateIdentity(input.AgentID) {
		return Snapshot{}, false, fmt.Errorf("%w: agent and input IDs are required", ErrInvalidInput)
	}
	input.Kind = s.classifier.Classify(input.Kind, input.Text)
	// Only the Worker creates automatic-retry items, through scheduleAutoRetry.
	// An enqueue of one would race the retry cycle's own upsert.
	if IsAutoRetryKind(input.Kind) {
		return Snapshot{}, false, fmt.Errorf("%w: the Worker schedules automatic retries", ErrInvalidInput)
	}
	if err := ValidateContent(input.Kind, input.Text, input.Attachments); err != nil {
		return Snapshot{}, false, err
	}
	if err := validateSchedule(input.Kind, input.SourcePayload, input.Attachments); err != nil {
		return Snapshot{}, false, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Snapshot{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	now := s.now()
	commit := func() (Snapshot, error) {
		if apply != nil {
			if err := apply(tx); err != nil {
				return Snapshot{}, err
			}
		}
		return commitSnapshot(ctx, tx, input.AgentID, now)
	}
	if err := ensureState(ctx, tx, input.AgentID); err != nil {
		return Snapshot{}, false, err
	}

	existing, existingAttachments, found, err := getItem(ctx, tx, input.AgentID, input.ID, true)
	if err != nil {
		return Snapshot{}, false, err
	}
	if found {
		if existing.Kind != input.Kind || existing.Text != input.Text || existing.TargetMode != input.TargetMode ||
			existing.PrepareContext != input.PrepareContext || existing.ReclassifyOnEdit != input.ReclassifyOnEdit ||
			!schedulesEqual(existing.ScheduledAt, input.ScheduledAt) ||
			!bytes.Equal(existing.SourcePayload, input.SourcePayload) ||
			!attachmentsEqual(existingAttachments, input.Attachments) {
			return Snapshot{}, false, ErrConflict
		}
		snapshot, err := commit()
		return snapshot, false, err
	}
	var acceptedAgentID, acceptedIdempotencyKey string
	err = tx.QueryRowContext(ctx, `SELECT agent_id, idempotency_key FROM messages WHERE id = ?`, input.ID).Scan(&acceptedAgentID, &acceptedIdempotencyKey)
	if err == nil {
		if acceptedAgentID != input.AgentID || acceptedIdempotencyKey == "" ||
			acceptedIdempotencyKey != inputIdempotencyKey(input) {
			return Snapshot{}, false, ErrConflict
		}
		snapshot, err := commit()
		return snapshot, false, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Snapshot{}, false, err
	}

	if err := insertQueuedItem(ctx, tx, input); err != nil {
		return Snapshot{}, false, err
	}
	if err := replaceAttachments(ctx, tx, input.ID, input.Attachments); err != nil {
		return Snapshot{}, false, err
	}
	if controlReplyKind(input.Kind) {
		if err := placeControlReply(ctx, tx, input.AgentID, input.ID); err != nil {
			return Snapshot{}, false, err
		}
	}
	if err := bumpRevision(ctx, tx, input.AgentID); err != nil {
		return Snapshot{}, false, err
	}
	snapshot, err := commit()
	return snapshot, err == nil, err
}

func (s *Store) Snapshot(ctx context.Context, agentID string) (Snapshot, error) {
	if !validateIdentity(agentID) {
		return Snapshot{}, fmt.Errorf("%w: agent ID is required", ErrInvalidInput)
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Snapshot{}, err
	}
	defer func() { _ = tx.Rollback() }()
	return commitSnapshot(ctx, tx, agentID, s.now())
}

func (s *Store) BeginEdit(ctx context.Context, agentID, inputID, clientID string, takeover bool) (Snapshot, string, []Attachment, error) {
	if !validateIdentity(clientID) {
		return Snapshot{}, "", nil, fmt.Errorf("%w: client ID is required", ErrInvalidInput)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Snapshot{}, "", nil, err
	}
	defer func() { _ = tx.Rollback() }()
	item, attachments, found, err := getItem(ctx, tx, agentID, inputID, true)
	if err != nil {
		return Snapshot{}, "", nil, err
	}
	if !found {
		return Snapshot{}, "", nil, ErrNotFound
	}
	if lockedInputState(item.State) {
		return Snapshot{}, "", nil, ErrConflict
	}
	if item.EditOwner != "" && item.EditOwner != clientID && !takeover {
		return Snapshot{}, "", nil, ErrEditOwned
	}
	var otherEditedID string
	err = tx.QueryRowContext(ctx, `SELECT id FROM agent_input_queue_items WHERE agent_id = ? AND edit_owner <> '' AND id <> ? LIMIT 1`, agentID, inputID).Scan(&otherEditedID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Snapshot{}, "", nil, err
	}
	if otherEditedID != "" && !takeover {
		return Snapshot{}, "", nil, ErrEditOwned
	}
	if takeover {
		if _, err := tx.ExecContext(ctx, `UPDATE agent_input_queue_items SET edit_owner = '', updated_at = ? WHERE agent_id = ? AND id <> ? AND edit_owner <> ''`, nowText(), agentID, inputID); err != nil {
			return Snapshot{}, "", nil, err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_input_queue_items SET edit_owner = ?, updated_at = ? WHERE agent_id = ? AND id = ?`, clientID, nowText(), agentID, inputID); err != nil {
		return Snapshot{}, "", nil, normalizeConstraintError(err)
	}
	if err := bumpRevision(ctx, tx, agentID); err != nil {
		return Snapshot{}, "", nil, err
	}
	snapshot, err := commitSnapshot(ctx, tx, agentID, s.now())
	if err != nil {
		return Snapshot{}, "", nil, err
	}
	return snapshot, item.Text, attachments, nil
}

func (s *Store) Update(ctx context.Context, request UpdateRequest) (Snapshot, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Snapshot{}, err
	}
	defer func() { _ = tx.Rollback() }()
	item, _, found, err := getItem(ctx, tx, request.AgentID, request.InputID, false)
	if err != nil {
		return Snapshot{}, err
	}
	if !found {
		return Snapshot{}, ErrNotFound
	}
	if item.EditOwner != request.ClientID || request.ClientID == "" {
		return Snapshot{}, ErrEditOwnerMismatch
	}
	if item.Version != request.ExpectedVersion {
		return Snapshot{}, ErrVersionConflict
	}
	kind := item.Kind
	if item.ReclassifyOnEdit {
		kind = s.classifier.Classify(leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, request.Text)
	}
	if err := ValidateContent(kind, request.Text, request.Attachments); err != nil {
		return Snapshot{}, err
	}
	// An edit preserves the item's source payload: it is the provider source
	// the item was created from, which no text edit rewrites. The schedule is
	// the caller's to keep, change, or clear.
	if err := validateSchedule(kind, item.SourcePayload, request.Attachments); err != nil {
		return Snapshot{}, err
	}
	if err := validateQueueAttachmentCapacity(ctx, tx, request.AgentID, request.InputID, request.Attachments); err != nil {
		return Snapshot{}, err
	}

	// A save of a failed or uncertain retry returns it to QUEUED, beside the
	// newer retry of its kind that a report may have queued meanwhile.
	if err := dropQueuedAutoRetry(ctx, tx, request.AgentID, request.InputID, kind); err != nil {
		return Snapshot{}, err
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE agent_input_queue_items
		SET kind = ?, text = ?, scheduled_at = CASE WHEN ? THEN ? ELSE scheduled_at END, state = ?, reserved_seq = 0, error = '', edit_owner = '', version = version + 1, updated_at = ?
		WHERE agent_id = ? AND id = ? AND version = ?`,
		kind, request.Text, request.Schedule.Replace, sqltime.NewSQLiteNullTime(request.Schedule.At), leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED, nowText(), request.AgentID, request.InputID, request.ExpectedVersion)
	if err != nil {
		return Snapshot{}, err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return Snapshot{}, ErrVersionConflict
	}
	if err := replaceAttachments(ctx, tx, request.InputID, request.Attachments); err != nil {
		return Snapshot{}, err
	}
	if err := bumpRevision(ctx, tx, request.AgentID); err != nil {
		return Snapshot{}, err
	}
	return commitSnapshot(ctx, tx, request.AgentID, s.now())
}

func (s *Store) CancelEdit(ctx context.Context, agentID, inputID, clientID string) (Snapshot, error) {
	return s.changeOwnedEdit(ctx, agentID, inputID, clientID, `UPDATE agent_input_queue_items SET edit_owner = '', updated_at = ? WHERE agent_id = ? AND id = ?`)
}

func (s *Store) changeOwnedEdit(ctx context.Context, agentID, inputID, clientID, statement string) (Snapshot, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Snapshot{}, err
	}
	defer func() { _ = tx.Rollback() }()
	item, _, found, err := getItem(ctx, tx, agentID, inputID, false)
	if err != nil {
		return Snapshot{}, err
	}
	if !found {
		return Snapshot{}, ErrNotFound
	}
	if item.EditOwner != clientID || clientID == "" {
		return Snapshot{}, ErrEditOwnerMismatch
	}
	if _, err := tx.ExecContext(ctx, statement, nowText(), agentID, inputID); err != nil {
		return Snapshot{}, err
	}
	if err := bumpRevision(ctx, tx, agentID); err != nil {
		return Snapshot{}, err
	}
	return commitSnapshot(ctx, tx, agentID, s.now())
}

// Delete removes one item that no dispatch holds, and returns the item as it
// was stored.
func (s *Store) Delete(ctx context.Context, agentID, inputID string) (Snapshot, StoredItem, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Snapshot{}, StoredItem{}, err
	}
	defer func() { _ = tx.Rollback() }()
	item, _, found, err := getItem(ctx, tx, agentID, inputID, false)
	if err != nil {
		return Snapshot{}, StoredItem{}, err
	}
	if !found {
		return Snapshot{}, StoredItem{}, ErrNotFound
	}
	if lockedInputState(item.State) {
		return Snapshot{}, StoredItem{}, ErrConflict
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM agent_input_queue_items WHERE agent_id = ? AND id = ?`, agentID, inputID); err != nil {
		return Snapshot{}, StoredItem{}, err
	}
	// Deleting a server-error retry ends its cycle by hand, so the backoff it
	// accumulated resets with it, unless a pending retry of the kind remains:
	// a failed or uncertain row beside it is a stale attempt, and the pending
	// row still carries the live cycle and its backoff.
	if item.Kind == leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR {
		pendingID, err := queuedAutoRetryID(ctx, tx, agentID, item.Kind)
		if err != nil {
			return Snapshot{}, StoredItem{}, err
		}
		if pendingID == "" {
			if _, err := resetServerErrorDelay(ctx, tx, agentID); err != nil {
				return Snapshot{}, StoredItem{}, err
			}
		}
	}
	if err := compactOrder(ctx, tx, agentID); err != nil {
		return Snapshot{}, StoredItem{}, err
	}
	if err := bumpRevision(ctx, tx, agentID); err != nil {
		return Snapshot{}, StoredItem{}, err
	}
	snapshot, err := commitSnapshot(ctx, tx, agentID, s.now())
	if err != nil {
		return Snapshot{}, StoredItem{}, err
	}
	return snapshot, item, nil
}

// Move reorders one item inside its reorder group. The destination must share
// the source's group and sit with it in one contiguous unlocked segment: an
// immediate item and a future one never trade places, and a locked row (a
// dispatch in flight, an awaiting-echo steer) is a boundary no move crosses.
//
// An empty beforeInputID moves the item to the end of its compatible,
// unlocked segment -- not the global end of the queue. Only the raw
// order_index slots that segment occupies are reassigned; every other group
// and locked row keeps its slots untouched.
func (s *Store) Move(ctx context.Context, agentID, inputID, beforeInputID string) (Snapshot, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Snapshot{}, err
	}
	defer func() { _ = tx.Rollback() }()
	now := s.now()
	items, err := readOrderingRows(ctx, tx, agentID)
	if err != nil {
		return Snapshot{}, err
	}
	items = prioritizedItems(items, now)
	from := indexOfItem(items, inputID)
	if from < 0 {
		return Snapshot{}, ErrNotFound
	}
	if lockedInputState(items[from].State) {
		return Snapshot{}, ErrConflict
	}
	if beforeInputID == inputID {
		return commitSnapshot(ctx, tx, agentID, now)
	}
	group := reorderGroup(items[from], now)
	// The source's segment: the run of items that a move may traverse. The
	// source is unlocked, so it has one.
	segments := reorderSegments(items, now)
	segStart := slices.Index(segments, segments[from])
	segEnd := segStart
	for segEnd+1 < len(items) && segments[segEnd+1] == segments[from] {
		segEnd++
	}
	if beforeInputID != "" {
		to := indexOfItem(items, beforeInputID)
		if to < 0 {
			return Snapshot{}, ErrNotFound
		}
		if lockedInputState(items[to].State) {
			return Snapshot{}, ErrConflict
		}
		if reorderGroup(items[to], now) != group {
			return Snapshot{}, ErrScheduleOrder
		}
		if segments[to] != segments[from] {
			// The destination shares the group but a locked row separates it
			// from the source, so no unlocked segment spans both.
			return Snapshot{}, ErrConflict
		}
	}
	// The segment's slots, in the ascending order one group's effective order
	// already holds them (the comparator falls back to order_index inside a
	// group). The move reassigns exactly these slots and touches no other row.
	segment := items[segStart : segEnd+1]
	slots := make([]int64, len(segment))
	order := make([]string, 0, len(segment))
	for i := range segment {
		slots[i] = segment[i].Order
		if segment[i].ID != inputID {
			order = append(order, segment[i].ID)
		}
	}
	if beforeInputID == "" {
		order = append(order, inputID)
	} else {
		order = slices.Insert(order, slices.Index(order, beforeInputID), inputID)
	}
	if err := writeOrderSlots(ctx, tx, agentID, order, slots); err != nil {
		return Snapshot{}, err
	}
	if err := bumpRevision(ctx, tx, agentID); err != nil {
		return Snapshot{}, err
	}
	return commitSnapshot(ctx, tx, agentID, now)
}

func indexOfItem[T priorityItem](items []T, id string) int {
	return slices.IndexFunc(items, func(item T) bool { return item.priorityMetadata().ID == id })
}

func (s *Store) SetPaused(ctx context.Context, agentID string, paused bool, reason leapmuxv1.AgentInputQueuePauseReason) (Snapshot, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Snapshot{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := ensureState(ctx, tx, agentID); err != nil {
		return Snapshot{}, err
	}
	if !paused {
		reason = leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_UNSPECIFIED
	} else if reason == leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_UNSPECIFIED {
		reason = leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_MANUAL
	}
	owner := pauseOwnerNone
	if paused {
		owner = pauseOwnerManual
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_input_queue_state SET paused = ?, pause_reason = ?, pause_owner = ?, revision = revision + 1, updated_at = ? WHERE agent_id = ?`, paused, workerdb.OptionalStorageEnum(reason), workerdb.OptionalStorageEnum(leapmuxv1.AgentInputQueuePauseOwner(owner)), nowText(), agentID); err != nil {
		return Snapshot{}, err
	}
	return commitSnapshot(ctx, tx, agentID, s.now())
}

// PauseForArchive stops the active turn and records an archive-created pause.
// The Boolean reports whether the durable state changed.
func (s *Store) PauseForArchive(ctx context.Context, agentID string) (Snapshot, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Snapshot{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := ensureState(ctx, tx, agentID); err != nil {
		return Snapshot{}, false, err
	}
	result, err := pauseAndEndTurn(ctx, tx, agentID,
		leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_AGENT_STOPPED, pauseOwnerArchive)
	if err != nil {
		return Snapshot{}, false, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return Snapshot{}, false, err
	}
	snapshot, err := commitSnapshot(ctx, tx, agentID, s.now())
	return snapshot, changed == 1, err
}

func (s *Store) pauseForPlannedRestart(ctx context.Context, agentID string) (Snapshot, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Snapshot{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := ensureState(ctx, tx, agentID); err != nil {
		return Snapshot{}, false, err
	}
	// The pause and the marker answer different questions, so they are written
	// separately. The pause holds only when this restart created it, which is
	// what the returned Boolean reports; the marker holds either way, because
	// a write to a stopping process is unsafe however the queue came to pause.
	result, err := tx.ExecContext(ctx, `
		UPDATE agent_input_queue_state
		SET paused = 1, pause_reason = ?, pause_owner = ?, revision = revision + 1, updated_at = ?
		WHERE agent_id = ? AND paused = 0`,
		leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_AGENT_STOPPED, pauseOwnerPlannedRestart, nowText(), agentID)
	if err != nil {
		return Snapshot{}, false, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return Snapshot{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_input_queue_state SET restarting = 1, updated_at = ? WHERE agent_id = ?`, nowText(), agentID); err != nil {
		return Snapshot{}, false, err
	}
	snapshot, err := commitSnapshot(ctx, tx, agentID, s.now())
	return snapshot, changed == 1, err
}

func (s *Store) finishPlannedRestart(ctx context.Context, agentID string, oldTurnEnded, resumeQueue bool) (Snapshot, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Snapshot{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := ensureState(ctx, tx, agentID); err != nil {
		return Snapshot{}, false, err
	}
	var paused, active bool
	var storedReason *leapmuxv1.AgentInputQueuePauseReason
	var storedOwner *leapmuxv1.AgentInputQueuePauseOwner
	if err := tx.QueryRowContext(ctx, `
		SELECT paused, pause_reason, pause_owner, active_turn
		FROM agent_input_queue_state WHERE agent_id = ?`, agentID).Scan(&paused, &storedReason, &storedOwner, &active); err != nil {
		return Snapshot{}, false, err
	}
	reason := workerdb.StorageEnumValue(storedReason)
	owner := pauseOwner(workerdb.StorageEnumValue(storedOwner))
	nextPaused := paused
	nextReason := reason
	nextOwner := owner
	// Resume only the pause that this restart created. A crash, an archive, or
	// the user overwrites pause_owner, so their pause survives the restart.
	if resumeQueue && paused && owner == pauseOwnerPlannedRestart {
		nextPaused = false
		nextReason = leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_UNSPECIFIED
		nextOwner = pauseOwnerNone
	} else if paused && owner == pauseOwnerPlannedRestart {
		// The restart keeps the pause. It no longer owns it, so a later resume
		// of a different cause cannot mistake this pause for its own.
		nextOwner = pauseOwnerAgentStopped
	}
	nextActive := active
	if oldTurnEnded {
		nextActive = false
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_input_queue_state SET restarting = 0, updated_at = ? WHERE agent_id = ?`, nowText(), agentID); err != nil {
		return Snapshot{}, false, err
	}
	changed := nextPaused != paused || nextReason != reason || nextOwner != owner || nextActive != active
	if changed {
		if _, err := tx.ExecContext(ctx, `
			UPDATE agent_input_queue_state
			SET paused = ?, pause_reason = ?, pause_owner = ?, active_turn = ?,
				active_turn_steerable = CASE WHEN ? THEN active_turn_steerable ELSE 0 END,
				active_input_id = CASE WHEN ? THEN active_input_id ELSE '' END,
				revision = revision + 1, updated_at = ?
			WHERE agent_id = ?`,
			nextPaused, workerdb.OptionalStorageEnum(nextReason), workerdb.OptionalStorageEnum(leapmuxv1.AgentInputQueuePauseOwner(nextOwner)), nextActive, nextActive, nextActive, nowText(), agentID); err != nil {
			return Snapshot{}, false, err
		}
	}
	snapshot, err := commitSnapshot(ctx, tx, agentID, s.now())
	return snapshot, changed, err
}

// PrepareDispatch reads the head for the drain loop. steerRowAtEcho is the
// provider's steer-row policy: a question answer that an active turn steers
// holds its row for the echo, so prepare reserves no sequence for it.
func (s *Store) PrepareDispatch(ctx context.Context, agentID string, steerRowAtEcho bool) (*PreparedDispatch, Snapshot, error) {
	return s.prepare(ctx, agentID, "", dispatchAutomatic, steerRowAtEcho)
}

func (s *Store) PrepareRetry(ctx context.Context, agentID string) (*PreparedDispatch, Snapshot, error) {
	return s.prepare(ctx, agentID, "", dispatchRetry, false)
}

func (s *Store) PrepareSteer(ctx context.Context, agentID, inputID string, rowAtEcho bool) (*PreparedDispatch, Snapshot, error) {
	// Steering always addresses one item. An empty ID would skip the head test
	// below and reserve whatever sits at the head, and the manager's recovery
	// paths would then requeue by that same empty ID and match no row, so the
	// item would stay DISPATCHING until the Worker restarts.
	if !validateIdentity(inputID) {
		return nil, Snapshot{}, ErrNotHead
	}
	return s.prepare(ctx, agentID, inputID, dispatchSteering, rowAtEcho)
}

// dispatchGate holds every input the dispatch guard reads, so ONE predicate answers
// for the drain and for the snapshot.
//
// The snapshot's CanSteer and CanPreempt state what the Worker will do, and the browser
// draws a button from each. They used to RESTATE a hand-written subset of prepare's
// terms, and the two drifted every time prepare gained one: CanPreempt borrowed
// steering's kind filter and hid the button from a queued clear, then answered true for
// a paused queue and for a head whose approval is not recorded -- so pressing Preempt
// destroyed the running turn and the drain then refused the item in silence, leaving
// the reader with neither the turn nor the message.
type dispatchGate struct {
	Paused        bool
	Restarting    bool
	ActiveTurn    bool
	TurnSteerable bool
	// Closed reports that the agent closed. A closed agent takes no new
	// process, so a dispatch would only fail the item and pause the queue.
	Closed bool
	// Now is the instant this gate captured. A head scheduled after it is
	// refused in every dispatch mode, so nothing -- automatic dispatch, retry,
	// steer, preempt -- delivers early.
	Now          time.Time
	Head         OrderingMetadata
	HeadFound    bool
	AwaitingEcho int
	// ApprovalSources counts the control-response rows that claim the head, and
	// ApprovalsComplete reports that every one of them is COMPLETED.
	ApprovalSources   int
	ApprovalsComplete bool
}

// dispatchRefusal states why the gate refuses. The zero value admits.
type dispatchRefusal int

const (
	dispatchAdmitted dispatchRefusal = iota
	dispatchRefusedPlannedRestart
	dispatchRefusedSteeringState
	// dispatchRefusedScheduled refuses a head whose scheduled instant has not
	// arrived. A drain and a retry wait for it quietly, as for
	// dispatchRefusedNotReady; a steer states it (ErrScheduled).
	dispatchRefusedScheduled
	dispatchRefusedNotReady
)

// admits reports whether prepare would hand this head to its caller.
//
// The two parameters are prepare's own dispensations, spelled the same way:
// allowPaused lets retry and steering dispatch through a pause, and requireActive
// selects the steering path over the drain path.
func (g dispatchGate) admits(allowPaused, requireActive bool) dispatchRefusal {
	if !g.HeadFound || g.Closed {
		return dispatchRefusedNotReady
	}
	// A head whose scheduled instant has not arrived refuses every mode. The
	// queue's maturity timer is the only edge that turns it dispatchable.
	if scheduledFuture(g.Head, g.Now) {
		return dispatchRefusedScheduled
	}
	if !requireActive && g.AwaitingEcho > 0 {
		return dispatchRefusedNotReady
	}
	// A restart in flight is what a pause dispensation must still respect: the old
	// process is stopping, so a write reaches a closed pipe and fails the item
	// permanently.
	if allowPaused && g.Restarting {
		return dispatchRefusedPlannedRestart
	}
	if requireActive && g.ActiveTurn && (!steerableInputKind(g.Head.Kind) || !g.TurnSteerable) {
		return dispatchRefusedSteeringState
	}
	blockedByTurn := g.ActiveTurn
	if requireActive {
		blockedByTurn = !g.ActiveTurn
	}
	canReplaceTurn := !requireActive && g.Head.requiresContextReplacement()
	if (g.Paused && !allowPaused) || (blockedByTurn && !canReplaceTurn) ||
		g.Head.EditOwner != "" || g.Head.State != leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED {
		return dispatchRefusedNotReady
	}
	// A recorded approval permits the context replacement that the old turn waits for.
	// Ordinary input and unconfirmed approvals still wait for that turn to end.
	if !g.ApprovalsComplete || g.ApprovalSources > 1 {
		return dispatchRefusedNotReady
	}
	// A head that REPLACES the turn needs exactly one recorded approval, whether or not
	// a turn is running now. The qualifier used to be "while the turn blocks it", which
	// says the same thing for every head the turn blocks -- the term above it already
	// refuses a blocked head that cannot replace the turn -- and says nothing once the
	// turn ends. A replacement that reached the drain with no approval was dispatched
	// and then failed PERMANENTLY at resolveControlInput, with "the context replacement
	// has no recorded approval"; refusing it here leaves it queued for the approval
	// instead, and keeps the Preempt button off an item that would fail either way.
	if g.Head.requiresContextReplacement() && g.ApprovalSources != 1 {
		return dispatchRefusedNotReady
	}
	return dispatchAdmitted
}

// queueOrdering retains the raw slots and effective priority of one read.
type queueOrdering struct {
	raw       []OrderingMetadata
	effective []OrderingMetadata
}

func readQueueOrdering(ctx context.Context, tx db.DBTX, agentID string, now time.Time) (queueOrdering, error) {
	raw, err := readOrderingRows(ctx, tx, agentID)
	if err != nil {
		return queueOrdering{}, err
	}
	return queueOrdering{raw: raw, effective: prioritizedItems(slices.Clone(raw), now)}, nil
}

// readDispatchGate reads the queue state, the prioritized head and the head's
// approval rows.
//
// It is the ONE reader of those three, so prepare and snapshotTx cannot disagree about
// what the guard sees. headItem selects the head with the same comparator every read
// uses and loads its full row, because the dispatch path hands that row to the
// provider. snapshotTx already holds its sorted head and asks readDispatchGateFor
// directly.
func readDispatchGate(ctx context.Context, tx db.DBTX, agentID string, now time.Time) (dispatchGate, StoredItem, queueOrdering, error) {
	ordering, err := readQueueOrdering(ctx, tx, agentID, now)
	if err != nil {
		return dispatchGate{}, StoredItem{}, queueOrdering{}, err
	}
	head, found, err := headItemFromOrdering(ctx, tx, agentID, ordering)
	if err != nil {
		return dispatchGate{}, StoredItem{}, queueOrdering{}, err
	}
	gate, err := readDispatchGateFor(ctx, tx, agentID, now, head.OrderingMetadata, found)
	return gate, head, ordering, err
}

// readDispatchGateFor reads the queue state and the approval rows of a head
// that the caller already selected.
func readDispatchGateFor(ctx context.Context, tx db.DBTX, agentID string, now time.Time, head OrderingMetadata, headFound bool) (dispatchGate, error) {
	gate := dispatchGate{Now: now, ApprovalsComplete: true}
	if err := tx.QueryRowContext(ctx, `SELECT paused, restarting, active_turn, active_turn_steerable,
		(SELECT COUNT(*) FROM agent_input_queue_items WHERE agent_id = ? AND state = ?),
		COALESCE((SELECT closed_at IS NOT NULL FROM agents WHERE id = ?), 1)
		FROM agent_input_queue_state WHERE agent_id = ?`, agentID, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_AWAITING_ECHO, agentID, agentID).
		Scan(&gate.Paused, &gate.Restarting, &gate.ActiveTurn, &gate.TurnSteerable, &gate.AwaitingEcho, &gate.Closed); err != nil {
		return dispatchGate{}, err
	}
	if !headFound {
		return gate, nil
	}
	gate.Head, gate.HeadFound = head, true
	sources, err := db.New(tx).GetControlResponseSourcesForInput(ctx, db.GetControlResponseSourcesForInputParams{
		AgentID: agentID, InputID: head.ID,
	})
	if err != nil {
		return dispatchGate{}, err
	}
	gate.ApprovalSources = len(sources)
	for _, source := range sources {
		if source.State != leapmuxv1.ControlDeliveryState_CONTROL_DELIVERY_STATE_COMPLETED {
			gate.ApprovalsComplete = false
		}
	}
	return gate, nil
}

// steeringRefusal states why the gate refuses a steer. "active turn ended
// before steering" is true only when no turn runs. A future head, a head that
// another client edits, a head that is not QUEUED, and a head whose approvals
// are incomplete each wait while the turn runs.
func steeringRefusal(refusal dispatchRefusal, activeTurn bool) error {
	switch {
	case refusal == dispatchRefusedScheduled:
		return ErrScheduled
	case !activeTurn:
		return ErrTurnEnded
	default:
		return ErrSteeringState
	}
}

type dispatchMode uint8

const (
	dispatchAutomatic dispatchMode = iota
	dispatchRetry
	dispatchSteering
)

func (s *Store) prepare(ctx context.Context, agentID, expectedInputID string, mode dispatchMode, rowAtEcho bool) (*PreparedDispatch, Snapshot, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, Snapshot{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := ensureState(ctx, tx, agentID); err != nil {
		return nil, Snapshot{}, err
	}
	now := s.now()
	gate, item, ordering, err := readDispatchGate(ctx, tx, agentID, now)
	if err != nil {
		return nil, Snapshot{}, err
	}
	allowPaused := mode != dispatchAutomatic
	// Automatic question delivery uses the current transaction's pause and turn state.
	requireActive := mode == dispatchSteering || mode == dispatchAutomatic && gate.ActiveTurn && gate.TurnSteerable &&
		automaticSteeringKind(item.Kind)
	// ErrNotHead is about the CALLER's identity, not about the guard, so it stays here
	// and the gate never sees expectedInputID.
	if expectedInputID != "" && (!gate.HeadFound || item.ID != expectedInputID) {
		return nil, Snapshot{}, ErrNotHead
	}
	switch refusal := gate.admits(allowPaused, requireActive); refusal {
	case dispatchRefusedPlannedRestart:
		return nil, Snapshot{}, ErrPlannedRestart
	case dispatchRefusedSteeringState:
		return nil, Snapshot{}, ErrSteeringState
	case dispatchRefusedScheduled, dispatchRefusedNotReady:
		// A drain and a retry find nothing to send and wait for the next edge.
		// A steer is the user's request, so it states why it cannot proceed.
		if mode == dispatchSteering {
			return nil, Snapshot{}, steeringRefusal(refusal, gate.ActiveTurn)
		}
		snapshot, err := commitSnapshot(ctx, tx, agentID, now)
		return nil, snapshot, err
	case dispatchAdmitted:
		// Fall through to the dispatch below.
	}
	attachments, err := loadItemAttachments(ctx, tx, item.ID)
	if err != nil {
		return nil, Snapshot{}, err
	}
	var reservedSeq int64
	// A row held for the provider's echo reserves no sequence: the echo's
	// accept reserves one, in order with every other echoed row. Only a
	// steered dispatch holds its row (requireActive), so an ordinary dispatch
	// reserves even when the provider's steer policy writes rows at echoes.
	// A control reply's row is LeapMux's own record and never waits for an
	// echo -- the answer text, not the provider's repetition of it, is the
	// row -- so a control reply reserves like any ordinary dispatch.
	if !rowAtEcho || !requireActive || controlReplyKind(item.Kind) {
		if item.ReservedSeq > 0 {
			reservedSeq = item.ReservedSeq
		} else if reservedSeq, err = reserveMessageSeq(ctx, tx, agentID); err != nil {
			return nil, Snapshot{}, err
		}
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE agent_input_queue_items
		SET state = ?, reserved_seq = ?, error = '', updated_at = ?
		WHERE agent_id = ? AND id = ? AND state = ?`,
		leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DISPATCHING, reservedSeq, nowText(), agentID, item.ID,
		leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED)
	if err != nil {
		return nil, Snapshot{}, err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return nil, Snapshot{}, ErrConflict
	}
	if err := placeDispatchedItem(ctx, tx, agentID, item.ID, ordering); err != nil {
		return nil, Snapshot{}, err
	}
	stateUpdate := `UPDATE agent_input_queue_state SET revision = revision + 1, updated_at = ? WHERE agent_id = ?`
	stateArgs := []any{nowText(), agentID}
	if !requireActive {
		stateUpdate = `UPDATE agent_input_queue_state SET active_turn = 1, active_turn_steerable = 0, active_input_id = ?, revision = revision + 1, updated_at = ? WHERE agent_id = ?`
		stateArgs = []any{item.ID, nowText(), agentID}
	}
	if _, err := tx.ExecContext(ctx, stateUpdate, stateArgs...); err != nil {
		return nil, Snapshot{}, err
	}
	item.State = leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DISPATCHING
	item.ReservedSeq = reservedSeq
	snapshot, err := snapshotTx(ctx, tx, agentID, now)
	if err != nil {
		return nil, Snapshot{}, err
	}
	found := false
	for _, stored := range snapshot.Items {
		if stored.ID != item.ID {
			continue
		}
		// The snapshot supplies updated fields but keeps only a text preview.
		// Copy these fields separately to preserve the complete dispatch text.
		// The order slot is the one that placeDispatchedItem wrote.
		item.Error, item.UpdatedAt, item.Order = stored.Error, stored.UpdatedAt, stored.Order
		found = true
		break
	}
	if !found {
		return nil, Snapshot{}, ErrConflict
	}
	if err := tx.Commit(); err != nil {
		return nil, Snapshot{}, err
	}
	return &PreparedDispatch{
		Item:        DispatchItem{StoredItem: item, Attachments: attachments},
		ReservedSeq: reservedSeq, Steering: requireActive,
	}, snapshot, nil
}

// RequeuePrepared returns a prepared item to the queue. The steer path uses it,
// and PrepareSteer claims no turn identity, so there is none to release.
func (s *Store) RequeuePrepared(ctx context.Context, agentID, inputID string) (Snapshot, error) {
	return s.requeuePrepared(ctx, agentID, inputID, false, false)
}

// RequeueBusy returns a prepared item to the queue after the provider refused
// it because a turn of its own is in flight. The queue stays OPEN and the item
// keeps no error text: nothing failed, and the end of that turn delivers it.
//
// It also releases the turn identity that PrepareDispatch claimed. That claim
// describes THIS dispatch, and this dispatch never reached the provider, so the
// turn that stands is the provider's own. activeTurnKind carries its
// classification when the provider supplies one. A claim left behind makes
// Snapshot.CanSteer and PrepareSteer judge the running turn by the refused
// item's kind, and each then answers for the wrong turn.
//
// active_turn itself stays as it is. The refusal proves that a turn ran when
// the provider read its flag, and the same refusal publishes that flag. But a
// turn that ended inside the provider call already committed its clear, and a
// write here would restore a turn that no envelope ever ends again.
func (s *Store) RequeueBusy(ctx context.Context, agentID, inputID string, activeTurnSteerable bool) (Snapshot, error) {
	return s.requeuePrepared(ctx, agentID, inputID, true, activeTurnSteerable)
}

func (s *Store) requeuePrepared(ctx context.Context, agentID, inputID string, releaseTurnIdentity bool, activeTurnSteerable bool) (Snapshot, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Snapshot{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := absorbQueuedAutoRetry(ctx, tx, agentID, inputID); err != nil {
		return Snapshot{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE agent_input_queue_items SET state = ?, reserved_seq = 0, error = '', updated_at = ? WHERE agent_id = ? AND id = ? AND state = ?`,
		leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED, nowText(), agentID, inputID,
		leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DISPATCHING)
	if err != nil {
		return Snapshot{}, err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return Snapshot{}, ErrConflict
	}
	if releaseTurnIdentity {
		if _, err := tx.ExecContext(ctx, `UPDATE agent_input_queue_state SET active_turn_steerable = ?, active_input_id = '', revision = revision + 1, updated_at = ? WHERE agent_id = ? AND active_input_id = ?`, activeTurnSteerable, nowText(), agentID, inputID); err != nil {
			return Snapshot{}, err
		}
	}
	if err := bumpRevision(ctx, tx, agentID); err != nil {
		return Snapshot{}, err
	}
	return commitSnapshot(ctx, tx, agentID, s.now())
}

// RequeueAndPause returns a prepared item to the queue and pauses the queue.
// It records the reason on the item without a failed state, so the queue
// resumes the same input when the agent can take it again.
func (s *Store) RequeueAndPause(ctx context.Context, agentID, inputID string, dispatchErr error, reason leapmuxv1.AgentInputQueuePauseReason) (Snapshot, error) {
	errText := "agent cannot accept input yet"
	if dispatchErr != nil {
		errText = dispatchErr.Error()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Snapshot{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := ensureState(ctx, tx, agentID); err != nil {
		return Snapshot{}, err
	}
	if _, err := absorbQueuedAutoRetry(ctx, tx, agentID, inputID); err != nil {
		return Snapshot{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE agent_input_queue_items SET state = ?, reserved_seq = 0, error = ?, updated_at = ? WHERE agent_id = ? AND id = ? AND state = ?`,
		leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED, errText, nowText(), agentID, inputID,
		leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DISPATCHING)
	if err != nil {
		return Snapshot{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return Snapshot{}, err
	}
	if changed != 1 {
		return Snapshot{}, ErrConflict
	}
	if _, err := pauseAndEndTurn(ctx, tx, agentID, reason, pauseOwnerAgentStopped); err != nil {
		return Snapshot{}, err
	}
	return commitSnapshot(ctx, tx, agentID, s.now())
}

// Accept records the transcript for a dispatch that reached the agent.
//
// It holds write intent before it reads the prepared item: the transaction
// takes the write lock at its BEGIN, as every write transaction of a database
// that `sqlitedb.Open` opened does. The provider already accepted the input, so
// a concurrent writer must not invalidate a read snapshot and leave its
// transcript uncertain. TestStoreAcceptTakesWriteIntentBeforeItsReads pins this.
func (s *Store) Accept(ctx context.Context, prepared PreparedDispatch, result DispatchResult) (AcceptedTranscript, Snapshot, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AcceptedTranscript{}, Snapshot{}, err
	}
	defer func() { _ = tx.Rollback() }()
	item, attachments, found, err := getItem(ctx, tx, prepared.Item.AgentID, prepared.Item.ID, true)
	if err != nil {
		return AcceptedTranscript{}, Snapshot{}, err
	}
	if !found || prepared.ReservedSeq <= 0 || item.State != leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DISPATCHING || item.ReservedSeq != prepared.ReservedSeq {
		return AcceptedTranscript{}, Snapshot{}, ErrConflict
	}
	if s.beforeAcceptWrite != nil {
		s.beforeAcceptWrite()
	}
	accepted, err := insertInputTranscript(ctx, tx, item, attachments, item.ReservedSeq, result.SpanLines, result.SupplementalContent)
	if err != nil {
		return AcceptedTranscript{}, Snapshot{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM agent_input_queue_items WHERE agent_id = ? AND id = ?`, item.AgentID, item.ID); err != nil {
		return AcceptedTranscript{}, Snapshot{}, err
	}
	if err := compactOrder(ctx, tx, item.AgentID); err != nil {
		return AcceptedTranscript{}, Snapshot{}, err
	}
	stateUpdate := `UPDATE agent_input_queue_state SET active_turn = 0, active_turn_steerable = 0, active_input_id = '', revision = revision + 1, updated_at = ? WHERE agent_id = ?`
	stateArgs := []any{nowText(), item.AgentID}
	if result.StartsTurn && !result.Steering {
		// The turn is asserted only while the state still holds THIS dispatch.
		// PrepareDispatch opened the turn and recorded the input id before the
		// provider call, and the manager releases the coordinator lock across
		// that call -- so a turn that ends inside it clears both fields first.
		// A write of the turn here would restore one that no envelope ever ends
		// again, and the queue would then hold every later message for the life
		// of the process.
		stateUpdate = `UPDATE agent_input_queue_state SET active_turn = 1, active_turn_steerable = ?, active_input_id = ?, revision = revision + 1, updated_at = ? WHERE agent_id = ? AND active_input_id = ?`
		stateArgs = []any{result.TurnSteerable, item.ID, nowText(), item.AgentID, item.ID}
	} else if result.Steering {
		stateUpdate = `UPDATE agent_input_queue_state SET revision = revision + 1, updated_at = ? WHERE agent_id = ?`
	}
	stateResult, err := tx.ExecContext(ctx, stateUpdate, stateArgs...)
	if err != nil {
		return AcceptedTranscript{}, Snapshot{}, err
	}
	// The guarded statement above is the one that can match no row. The item
	// still left the queue, so the snapshot still moves and owes every watcher
	// a revision it can order against.
	if rows, err := stateResult.RowsAffected(); err != nil {
		return AcceptedTranscript{}, Snapshot{}, err
	} else if rows == 0 {
		if err := bumpRevision(ctx, tx, item.AgentID); err != nil {
			return AcceptedTranscript{}, Snapshot{}, err
		}
	}
	snapshot, err := commitSnapshot(ctx, tx, item.AgentID, s.now())
	if err != nil {
		return AcceptedTranscript{}, Snapshot{}, err
	}
	return accepted, snapshot, nil
}

// insertInputTranscript writes one accepted input at the sequence that its caller reserved.
// supplemental is the encoded supplement that the dispatcher resolved for the row, or nil
// for none; the row stores it compressed. An automatic retry's supplement is its own
// provider source, which the queue encodes here, so the dispatched and the echo-accepted
// rows of a retry carry the same supplement.
func insertInputTranscript(ctx context.Context, tx *sql.Tx, item StoredItem, attachments []Attachment, seq int64, spanLines string, supplemental []byte) (AcceptedTranscript, error) {
	content, err := transcriptContent(item, attachments)
	if err != nil {
		return AcceptedTranscript{}, err
	}
	if len(item.SourcePayload) > 0 {
		// validateSchedule admits a source payload only on a retry kind, whose
		// dispatcher resolves no supplement of its own.
		if supplemental, err = agent.EncodeMessageSupplement(agent.MessageContent{Supplemental: item.SourcePayload}); err != nil {
			return AcceptedTranscript{}, fmt.Errorf("encode the automatic retry source: %w", err)
		}
	}
	compressed, compression := msgcodec.Compress(content)
	supplementalContent, supplementalCompression := []byte(nil), leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE
	if len(supplemental) > 0 {
		supplementalContent, supplementalCompression = msgcodec.Compress(supplemental)
	}
	var provider leapmuxv1.AgentProvider
	if err := tx.QueryRowContext(ctx, `SELECT agent_provider FROM agents WHERE id = ?`, item.AgentID).Scan(&provider); err != nil {
		return AcceptedTranscript{}, err
	}
	mark := leapmuxv1.MarkType_MARK_TYPE_UNSPECIFIED
	if item.ReclassifyOnEdit || item.Kind == leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE {
		mark = leapmuxv1.MarkType_MARK_TYPE_USER_MESSAGE
	} else if controlReplyKind(item.Kind) {
		mark = leapmuxv1.MarkType_MARK_TYPE_CONTROL_RESPONSE
	}
	if spanLines == "" {
		spanLines = "[]"
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO messages
		(id, agent_id, seq, source, content, content_compression, idempotency_key, depth, span_id,
		 parent_span_id, span_type, span_lines, span_color, agent_provider, mark_type, created_at,
		 supplemental_content, supplemental_content_compression)
		VALUES (?, ?, ?, ?, ?, ?, ?, 0, '', '', '', ?, 0, ?, ?, ?, COALESCE(?, X''), ?)`,
		item.ID, item.AgentID, seq, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER,
		compressed, compression, inputIdempotencyKey(NewItem{
			ID: item.ID, AgentID: item.AgentID, Kind: item.Kind, Text: item.Text,
			TargetMode: item.TargetMode, PrepareContext: item.PrepareContext,
			ReclassifyOnEdit: item.ReclassifyOnEdit, ScheduledAt: item.ScheduledAt,
			SourcePayload: item.SourcePayload, Attachments: attachments,
		}), spanLines, provider, workerdb.OptionalStorageEnum(mark), item.CreatedAt, supplementalContent, supplementalCompression); err != nil {
		return AcceptedTranscript{}, err
	}
	return AcceptedTranscript{
		ID: item.ID, AgentID: item.AgentID, Kind: item.Kind, Seq: seq,
		Content: compressed, ContentCompression: compression,
		SupplementalContent: supplementalContent, SupplementalContentCompression: supplementalCompression,
		AgentProvider: provider, MarkType: mark, SpanLines: spanLines,
		CreatedAt: item.CreatedAt,
	}, nil
}

// HoldForEcho retains an accepted steer without reserving or writing its transcript row.
func (s *Store) HoldForEcho(ctx context.Context, prepared PreparedDispatch) (Snapshot, error) {
	if prepared.ReservedSeq != 0 {
		return Snapshot{}, ErrConflict
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Snapshot{}, err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `UPDATE agent_input_queue_items SET state = ?, updated_at = ?
		WHERE agent_id = ? AND id = ? AND state = ? AND reserved_seq = 0`,
		leapmuxv1.AgentInputState_AGENT_INPUT_STATE_AWAITING_ECHO, nowText(), prepared.Item.AgentID, prepared.Item.ID,
		leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DISPATCHING)
	if err != nil {
		return Snapshot{}, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return Snapshot{}, err
	}
	if rows != 1 {
		return Snapshot{}, ErrConflict
	}
	if err := bumpRevision(ctx, tx, prepared.Item.AgentID); err != nil {
		return Snapshot{}, err
	}
	return commitSnapshot(ctx, tx, prepared.Item.AgentID, s.now())
}

// awaitingEchoIDs reads the oldest pending steers. A negative limit reads all of them.
func awaitingEchoIDs(ctx context.Context, tx db.DBTX, agentID string, count int) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM agent_input_queue_items WHERE agent_id = ? AND state = ? ORDER BY order_index LIMIT ?`,
		agentID, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_AWAITING_ECHO, count)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// pendingSteerIDs selects waiting echoes first and the current dispatch last.
func pendingSteerIDs(ctx context.Context, tx db.DBTX, agentID string, count int, inFlightID string) ([]string, bool, error) {
	ids, err := awaitingEchoIDs(ctx, tx, agentID, count)
	if err != nil {
		return nil, false, err
	}
	if (count >= 0 && len(ids) >= count) || inFlightID == "" {
		return ids, false, nil
	}
	item, _, found, err := getItem(ctx, tx, agentID, inFlightID, false)
	if err != nil {
		return nil, false, err
	}
	if !found || item.State != leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DISPATCHING || item.ReservedSeq != 0 {
		return ids, false, nil
	}
	return append(ids, inFlightID), true, nil
}

// AcceptEchoedSteers writes the oldest echoed steers in one transaction.
// The Boolean reports whether the transaction accepted the in-flight steer also.
func (s *Store) AcceptEchoedSteers(ctx context.Context, agentID string, count int, spanLines, inFlightID string) ([]AcceptedTranscript, bool, Snapshot, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, Snapshot{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := ensureState(ctx, tx, agentID); err != nil {
		return nil, false, Snapshot{}, err
	}
	if count <= 0 {
		snapshot, err := commitSnapshot(ctx, tx, agentID, s.now())
		return nil, false, snapshot, err
	}
	ids, acceptedInFlight, err := pendingSteerIDs(ctx, tx, agentID, count, inFlightID)
	if err != nil {
		return nil, false, Snapshot{}, err
	}
	var accepted []AcceptedTranscript
	for _, id := range ids {
		item, attachments, found, err := getItem(ctx, tx, agentID, id, true)
		if err != nil {
			return nil, false, Snapshot{}, err
		}
		if !found {
			return nil, false, Snapshot{}, ErrConflict
		}
		seq, err := reserveMessageSeq(ctx, tx, agentID)
		if err != nil {
			return nil, false, Snapshot{}, err
		}
		row, err := insertInputTranscript(ctx, tx, item, attachments, seq, spanLines, nil)
		if err != nil {
			return nil, false, Snapshot{}, err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM agent_input_queue_items WHERE agent_id = ? AND id = ?`, agentID, id); err != nil {
			return nil, false, Snapshot{}, err
		}
		accepted = append(accepted, row)
	}
	if len(accepted) > 0 {
		if err := compactOrder(ctx, tx, agentID); err != nil {
			return nil, false, Snapshot{}, err
		}
		if err := bumpRevision(ctx, tx, agentID); err != nil {
			return nil, false, Snapshot{}, err
		}
	}
	snapshot, err := commitSnapshot(ctx, tx, agentID, s.now())
	if err != nil {
		return nil, false, Snapshot{}, err
	}
	return accepted, acceptedInFlight, snapshot, nil
}

// requeueUnechoedSteers returns selected steers to the head behind the remaining pending echoes.
// The caller owns the transaction and its revision update. Two steers of one
// retry kind merge into one row (absorbQueuedAutoRetry), and the count
// includes both, because the provider dropped both.
func requeueUnechoedSteers(ctx context.Context, tx db.DBTX, agentID string, count int) (int, error) {
	ids, err := awaitingEchoIDs(ctx, tx, agentID, count)
	if err != nil || len(ids) == 0 {
		return 0, err
	}
	absorbed := make(map[string]bool)
	for _, id := range ids {
		peerID, err := absorbQueuedAutoRetry(ctx, tx, agentID, id)
		if err != nil {
			return 0, err
		}
		if peerID != "" {
			absorbed[peerID] = true
		}
		if _, err := tx.ExecContext(ctx, `UPDATE agent_input_queue_items SET state = ?, error = '', reserved_seq = 0, updated_at = ? WHERE agent_id = ? AND id = ?`,
			leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED, nowText(), agentID, id); err != nil {
			return 0, err
		}
	}
	remaining := slices.DeleteFunc(slices.Clone(ids), func(id string) bool { return absorbed[id] })
	if err := placeResolvedSteers(ctx, tx, agentID, remaining); err != nil {
		return 0, err
	}
	return len(ids), nil
}

// placeResolvedSteers keeps unresolved echoes first, then the selected inputs, then the remaining inputs.
func placeResolvedSteers(ctx context.Context, tx db.DBTX, agentID string, ids []string) error {
	selected := make(map[string]bool, len(ids))
	for _, id := range ids {
		selected[id] = true
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, state FROM agent_input_queue_items WHERE agent_id = ? ORDER BY order_index`, agentID)
	if err != nil {
		return err
	}
	var pending, rest []string
	for rows.Next() {
		var id string
		var state leapmuxv1.AgentInputState
		if err := rows.Scan(&id, &state); err != nil {
			_ = rows.Close()
			return err
		}
		if state == leapmuxv1.AgentInputState_AGENT_INPUT_STATE_AWAITING_ECHO {
			pending = append(pending, id)
		} else if !selected[id] {
			rest = append(rest, id)
		}
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	order := append(append(pending, ids...), rest...)
	if err := writeOrder(ctx, tx, agentID, order); err != nil {
		return err
	}
	return nil
}

// RequeueUnechoedSteers requeues the oldest count steers. A negative count selects all of them.
func (s *Store) RequeueUnechoedSteers(ctx context.Context, agentID string, count int) (Snapshot, int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Snapshot{}, 0, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := ensureState(ctx, tx, agentID); err != nil {
		return Snapshot{}, 0, err
	}
	requeued, err := requeueUnechoedSteers(ctx, tx, agentID, count)
	if err != nil {
		return Snapshot{}, 0, err
	}
	if requeued > 0 {
		if err := bumpRevision(ctx, tx, agentID); err != nil {
			return Snapshot{}, 0, err
		}
	}
	snapshot, err := commitSnapshot(ctx, tx, agentID, s.now())
	if err != nil {
		return Snapshot{}, 0, err
	}
	return snapshot, requeued, nil
}

// ResolveUnechoedSteers marks the oldest count inputs uncertain. A negative count selects all pending inputs.
// The Boolean states whether the transaction resolved the current dispatch also.
func (s *Store) ResolveUnechoedSteers(ctx context.Context, agentID string, count int, inFlightID string) (Snapshot, int, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Snapshot{}, 0, false, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := ensureState(ctx, tx, agentID); err != nil {
		return Snapshot{}, 0, false, err
	}
	ids, inFlight, err := pendingSteerIDs(ctx, tx, agentID, count, inFlightID)
	if err != nil {
		return Snapshot{}, 0, false, err
	}
	for _, id := range ids {
		_, err := tx.ExecContext(ctx, `UPDATE agent_input_queue_items SET state = ?, error = ?, updated_at = ? WHERE agent_id = ? AND id = ?`,
			leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DELIVERY_UNCERTAIN,
			"the agent did not confirm whether the model read this message", nowText(), agentID, id)
		if err != nil {
			return Snapshot{}, 0, false, err
		}
	}
	if len(ids) > 0 {
		if err := placeResolvedSteers(ctx, tx, agentID, ids); err != nil {
			return Snapshot{}, 0, false, err
		}
		paused, err := pauseAndEndTurn(ctx, tx, agentID, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_DELIVERY_UNCERTAIN, pauseOwnerDelivery)
		if err != nil {
			return Snapshot{}, 0, false, err
		}
		rows, err := paused.RowsAffected()
		if err != nil {
			return Snapshot{}, 0, false, err
		}
		if rows == 0 {
			if err := bumpRevision(ctx, tx, agentID); err != nil {
				return Snapshot{}, 0, false, err
			}
		}
	}
	snapshot, err := commitSnapshot(ctx, tx, agentID, s.now())
	if err != nil {
		return Snapshot{}, 0, false, err
	}
	return snapshot, len(ids), inFlight, nil
}

func (s *Store) FailDispatch(ctx context.Context, agentID, inputID string, deliveryErr error, uncertain bool) (Snapshot, error) {
	state := leapmuxv1.AgentInputState_AGENT_INPUT_STATE_FAILED
	reason := leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_DELIVERY_FAILED
	if uncertain {
		state = leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DELIVERY_UNCERTAIN
		reason = leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_DELIVERY_UNCERTAIN
	}
	errText := "input delivery failed"
	if deliveryErr != nil {
		errText = deliveryErr.Error()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Snapshot{}, err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `UPDATE agent_input_queue_items SET state = ?, error = ?, updated_at = ? WHERE agent_id = ? AND id = ? AND state = ?`,
		state, errText, nowText(), agentID, inputID, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DISPATCHING)
	if err != nil {
		return Snapshot{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return Snapshot{}, err
	}
	if changed != 1 {
		return Snapshot{}, ErrConflict
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_input_queue_state SET active_turn = 0, active_turn_steerable = 0, active_input_id = '', paused = 1, pause_reason = ?, pause_owner = ?, revision = revision + 1, updated_at = ? WHERE agent_id = ?`, reason, pauseOwnerDelivery, nowText(), agentID); err != nil {
		return Snapshot{}, err
	}
	return commitSnapshot(ctx, tx, agentID, s.now())
}

// TurnEnded clears the active turn.
// It requeues unechoed steers in the same transaction when requeueUnechoed is true.
//
// This direct operation carries no provider owner.
// Production provider reports use Manager.ReconcileProviderTurn instead.
// That method validates the original owner before it calls setTurnState.
// The Boolean reports whether the durable state changed.
// An unchanged report leaves the revision unchanged.
func (s *Store) TurnEnded(ctx context.Context, agentID string, requeueUnechoed bool) (Snapshot, bool, error) {
	return s.setTurnState(ctx, agentID, false, false, requeueUnechoed)
}

// TurnStarted records the provider's active turn and steering availability.
// A provider report replaces provisional dispatch state, also when both flags already match.
// Production provider reports use Manager.ReconcileProviderTurn to validate their original owner.
//
// The Boolean reports whether the durable state moved. See TurnEnded.
func (s *Store) TurnStarted(ctx context.Context, agentID string, steerable bool) (Snapshot, bool, error) {
	return s.setTurnState(ctx, agentID, true, steerable, false)
}

// AbandonUnownedTurn clears a turn that no dispatch owns. A process boundary
// calls it, because a process that starts or exits runs no turn: the old one
// died with its process, and the new one never began.
//
// Without it a turn the AGENT PROCESS reported has no end at all on two paths.
// A provider that reports a turn and then neither ends it nor exits (a stalled
// CLI) holds one; and an explicit stop skips the AGENT_STOPPED pause on
// purpose, so nothing clears one there either. The queue then holds every later
// message with no pause, no error, and no Retry -- and stopping the agent, the
// user's only escape, does not help.
//
// active_input_id is the guard, and it is what makes the two cases separable. A
// turn that a dispatch opened belongs to that dispatch, which records the input
// id before the provider call; Accept or the failure path resolves it, and a
// clear here would make Accept's own identity guard miss and drop the turn the
// dispatch just started. A turn a provider reported carries no input id, and it
// is exactly the one a boundary must clear.
func (s *Store) AbandonUnownedTurn(ctx context.Context, agentID string) (Snapshot, bool, error) {
	return s.execTurnState(ctx, agentID,
		`UPDATE agent_input_queue_state SET active_turn = 0, active_turn_steerable = 0, revision = revision + 1, updated_at = ? WHERE agent_id = ? AND active_input_id = '' AND (active_turn = 1 OR active_turn_steerable <> 0)`,
		[]any{nowText(), agentID}, false)
}

// setTurnState records the provider's turn state and clears provisional dispatch ownership.
// Accept then keeps this report instead of replacing it with the dispatch result.
// After ownership clears, a repeated report leaves the revision unchanged.
func (s *Store) setTurnState(ctx context.Context, agentID string, active, steerable, requeueUnechoed bool) (Snapshot, bool, error) {
	statement := `UPDATE agent_input_queue_state SET active_turn = 0, active_turn_steerable = 0, active_input_id = '', revision = revision + 1, updated_at = ? WHERE agent_id = ? AND (active_turn = 1 OR active_turn_steerable <> 0 OR active_input_id <> '')`
	args := []any{nowText(), agentID}
	if active {
		statement = `UPDATE agent_input_queue_state SET active_turn = 1, active_turn_steerable = ?, active_input_id = '', revision = revision + 1, updated_at = ? WHERE agent_id = ? AND (active_turn = 0 OR active_turn_steerable <> ? OR active_input_id <> '')`
		args = []any{steerable, nowText(), agentID, steerable}
	}
	return s.execTurnState(ctx, agentID, statement, args, !active && requeueUnechoed)
}

// execTurnState runs one guarded write against the turn columns and reports
// whether it moved a row. Every turn write shares this shape: the state row must
// exist, the guard decides whether anything changes, and the caller needs the
// committed snapshot either way.
func (s *Store) execTurnState(ctx context.Context, agentID, statement string, args []any, requeueUnechoed bool) (Snapshot, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Snapshot{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := ensureState(ctx, tx, agentID); err != nil {
		return Snapshot{}, false, err
	}
	result, err := tx.ExecContext(ctx, statement, args...)
	if err != nil {
		return Snapshot{}, false, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return Snapshot{}, false, err
	}
	if requeueUnechoed {
		count, err := requeueUnechoedSteers(ctx, tx, agentID, -1)
		if err != nil {
			return Snapshot{}, false, err
		}
		if count > 0 && rows == 0 {
			if err := bumpRevision(ctx, tx, agentID); err != nil {
				return Snapshot{}, false, err
			}
			rows = 1
		}
	}
	snapshot, err := commitSnapshot(ctx, tx, agentID, s.now())
	if err != nil {
		return Snapshot{}, false, err
	}
	return snapshot, rows == 1, nil
}

func (s *Store) Pause(ctx context.Context, agentID string, reason leapmuxv1.AgentInputQueuePauseReason) (Snapshot, error) {
	if reason != leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_AGENT_STOPPED {
		return s.SetPaused(ctx, agentID, true, reason)
	}
	// A stopped agent also ends the active turn, and it keeps the reason that
	// an earlier pause recorded.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Snapshot{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := ensureState(ctx, tx, agentID); err != nil {
		return Snapshot{}, err
	}
	if _, err := pauseAndEndTurn(ctx, tx, agentID, reason, pauseOwnerAgentStopped); err != nil {
		return Snapshot{}, err
	}
	return commitSnapshot(ctx, tx, agentID, s.now())
}

// pauseAndEndTurn pauses the queue and ends the active turn in one statement.
// It keeps the reason and the owner that an earlier pause recorded, so a
// resume by THIS cause cannot lift a pause the user or a crash created.
//
// One case takes ownership from an existing pause: a pause that a planned
// restart owns. That pause is temporary by definition -- the restart lifts it
// when the new process runs -- so a crash or an archive that arrives during
// the restart must claim it, or the restart resumes a queue that a real
// failure had stopped.
//
// The result reports whether the row changed.
func pauseAndEndTurn(ctx context.Context, tx *sql.Tx, agentID string, reason leapmuxv1.AgentInputQueuePauseReason, owner pauseOwner) (sql.Result, error) {
	return tx.ExecContext(ctx, `
		UPDATE agent_input_queue_state
		SET active_turn = 0, active_turn_steerable = 0, active_input_id = '',
			paused = 1,
			pause_reason = CASE WHEN paused = 1 THEN pause_reason ELSE ? END,
			pause_owner = CASE WHEN paused = 0 OR pause_owner = ? THEN ? ELSE pause_owner END,
			revision = revision + 1, updated_at = ?
		WHERE agent_id = ? AND (active_turn = 1 OR paused = 0 OR pause_owner = ?)`,
		reason, pauseOwnerPlannedRestart, owner, nowText(), agentID, pauseOwnerPlannedRestart)
}

// resumeOwnedPause lifts a pause only when the given cause still owns it.
// The result reports whether the row changed.
func resumeOwnedPause(ctx context.Context, tx *sql.Tx, agentID string, owner pauseOwner) (sql.Result, error) {
	return tx.ExecContext(ctx, `
		UPDATE agent_input_queue_state
		SET paused = 0, pause_reason = NULL, pause_owner = NULL,
			revision = revision + 1, updated_at = ?
		WHERE agent_id = ? AND paused = 1 AND pause_owner = ?`, nowText(), agentID, owner)
}

// ResumeAfterArchive resumes only the pause that PauseForArchive created. The
// Boolean reports whether the durable state changed.
func (s *Store) ResumeAfterArchive(ctx context.Context, agentID string) (Snapshot, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Snapshot{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := ensureState(ctx, tx, agentID); err != nil {
		return Snapshot{}, false, err
	}
	result, err := resumeOwnedPause(ctx, tx, agentID, pauseOwnerArchive)
	if err != nil {
		return Snapshot{}, false, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return Snapshot{}, false, err
	}
	snapshot, err := commitSnapshot(ctx, tx, agentID, s.now())
	return snapshot, changed == 1, err
}

func (s *Store) Retry(ctx context.Context, agentID, inputID string, confirmUncertain bool) (Snapshot, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Snapshot{}, err
	}
	defer func() { _ = tx.Rollback() }()
	now := s.now()
	item, found, err := headItem(ctx, tx, agentID, now)
	if err != nil {
		return Snapshot{}, err
	}
	if !found || item.ID != inputID {
		return Snapshot{}, ErrNotHead
	}
	if item.State != leapmuxv1.AgentInputState_AGENT_INPUT_STATE_FAILED && item.State != leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DELIVERY_UNCERTAIN {
		return Snapshot{}, ErrRetryState
	}
	if item.EditOwner != "" {
		return Snapshot{}, ErrEditOwned
	}
	if item.State == leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DELIVERY_UNCERTAIN && !confirmUncertain {
		return Snapshot{}, ErrUncertainConfirmation
	}
	var restarting bool
	if err := tx.QueryRowContext(ctx, `SELECT restarting FROM agent_input_queue_state WHERE agent_id = ?`, agentID).Scan(&restarting); err != nil {
		return Snapshot{}, err
	}
	if restarting {
		return Snapshot{}, ErrPlannedRestart
	}
	// The user's Retry of a failed automatic retry returns it to QUEUED,
	// beside the newer retry of its kind that a report may have queued.
	if err := dropQueuedAutoRetry(ctx, tx, agentID, inputID, item.Kind); err != nil {
		return Snapshot{}, err
	}
	// The item returns to QUEUED, so it releases its reservation. A stale
	// reservation makes the retried message sort before an agent message that
	// the transcript took after the failure.
	if _, err := tx.ExecContext(ctx, `UPDATE agent_input_queue_items SET state = ?, reserved_seq = 0, error = '', updated_at = ? WHERE agent_id = ? AND id = ?`, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED, nowText(), agentID, inputID); err != nil {
		return Snapshot{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_input_queue_state SET active_turn = 0, active_turn_steerable = 0, active_input_id = '', revision = revision + 1, updated_at = ? WHERE agent_id = ?`, nowText(), agentID); err != nil {
		return Snapshot{}, err
	}
	// Lift the pause that the failure created, so the items behind the retried
	// head dispatch again. A pause that the user or an archive created keeps
	// its own owner and survives.
	if _, err := resumeOwnedPause(ctx, tx, agentID, pauseOwnerDelivery); err != nil {
		return Snapshot{}, err
	}
	return commitSnapshot(ctx, tx, agentID, now)
}

// MatureScheduled records that one armed deadline arrived: every item that
// held it crossed from future to immediate, so the effective order and every
// reorder group changed. The revision bump is what makes that change visible:
// without it, an older equal-revision snapshot could overwrite the matured
// order. The Boolean reports whether the deadline is due by the store's clock
// and still belongs to an item; a reschedule, a move, or a deletion that beat
// the timer here answers false and changes nothing.
func (s *Store) MatureScheduled(ctx context.Context, agentID string, expected time.Time) (Snapshot, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Snapshot{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	now := s.now()
	items, err := readOrderingRows(ctx, tx, agentID)
	if err != nil {
		return Snapshot{}, false, err
	}
	due := !expected.After(now) && slices.ContainsFunc(items, func(item OrderingMetadata) bool {
		return item.ScheduledAt != nil && item.ScheduledAt.Equal(expected)
	})
	if due {
		if err := bumpRevision(ctx, tx, agentID); err != nil {
			return Snapshot{}, false, err
		}
	}
	snapshot, err := commitSnapshot(ctx, tx, agentID, now)
	return snapshot, due, err
}

// scheduleAutoRetry creates or updates the one pending queue item of one
// automatic-retry kind, atomically with the server-error backoff state. A new
// retry cycle takes a fresh ID; rescheduling the pending item of the kind
// retains its ID and its raw queue position. Validation, queue-full, and
// database failures leave the previous durable item untouched, because the
// transaction rolls back. The result states whether the schedule created the
// row or moved it, for the log.
func (s *Store) scheduleAutoRetry(ctx context.Context, agentID string, schedule agent.AutoRetrySchedule) (Snapshot, scheduledAutoRetry, error) {
	if !validateIdentity(agentID) {
		return Snapshot{}, scheduledAutoRetry{}, fmt.Errorf("%w: agent ID is required", ErrInvalidInput)
	}
	if !IsAutoRetryKind(schedule.Kind) {
		return Snapshot{}, scheduledAutoRetry{}, fmt.Errorf("%w: not an automatic-retry kind", ErrInvalidInput)
	}
	if err := validateSchedule(schedule.Kind, schedule.SourcePayload, nil); err != nil {
		return Snapshot{}, scheduledAutoRetry{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Snapshot{}, scheduledAutoRetry{}, err
	}
	defer func() { _ = tx.Rollback() }()
	now := s.now()
	// A closed or archived agent never gains a retry: late provider output
	// must not resurrect a schedule that lifecycle cleanup removed. The check
	// precedes ensureState, whose state row carries a foreign key to agents.
	var open int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agents WHERE id = ? AND closed_at IS NULL AND workspace_archived = 0`, agentID).Scan(&open); err != nil {
		return Snapshot{}, scheduledAutoRetry{}, err
	}
	if open == 0 {
		return Snapshot{}, scheduledAutoRetry{}, fmt.Errorf("%w: agent is closed or archived", ErrConflict)
	}
	if err := ensureState(ctx, tx, agentID); err != nil {
		return Snapshot{}, scheduledAutoRetry{}, err
	}
	var dueAt time.Time
	var retryText string
	if schedule.Kind == leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_USAGE_LIMIT {
		dueAt = usageLimitDeadline(schedule.ResetAt, now)
		retryText = autoRetryUsageLimitText
	} else {
		retryText = autoRetryServerErrorText
		delay, err := readServerErrorDelay(ctx, tx, agentID)
		if err != nil {
			return Snapshot{}, scheduledAutoRetry{}, err
		}
		var nextDelay time.Duration
		dueAt, nextDelay = serverErrorDeadline(delay, now)
		if err := writeServerErrorDelay(ctx, tx, agentID, nextDelay); err != nil {
			return Snapshot{}, scheduledAutoRetry{}, err
		}
	}

	existing, found, err := queuedAutoRetry(ctx, tx, agentID, schedule.Kind)
	if err != nil {
		return Snapshot{}, scheduledAutoRetry{}, err
	}
	scheduled := scheduledAutoRetry{row: autoRetryRow{
		id: existing.id, kind: schedule.Kind, state: leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED, scheduledAt: &dueAt,
	}}
	if !found {
		scheduled.row.id = id.Generate()
		if err := insertQueuedItem(ctx, tx, NewItem{
			ID: scheduled.row.id, AgentID: agentID, Kind: schedule.Kind, Text: retryText,
			ScheduledAt: &dueAt, SourcePayload: schedule.SourcePayload,
		}); err != nil {
			return Snapshot{}, scheduledAutoRetry{}, err
		}
	} else {
		scheduled.rescheduled, scheduled.previousScheduledAt = true, existing.scheduledAt
		if _, err := tx.ExecContext(ctx, `
		UPDATE agent_input_queue_items
		SET scheduled_at = ?, source_payload = ?, updated_at = ? WHERE agent_id = ? AND id = ?`,
			sqltime.SQLiteNullTimeOf(dueAt), ptrconv.OrEmpty(schedule.SourcePayload), nowText(), agentID, existing.id); err != nil {
			return Snapshot{}, scheduledAutoRetry{}, err
		}
	}
	if err := bumpRevision(ctx, tx, agentID); err != nil {
		return Snapshot{}, scheduledAutoRetry{}, err
	}
	snapshot, err := commitSnapshot(ctx, tx, agentID, now)
	if err != nil {
		return Snapshot{}, scheduledAutoRetry{}, err
	}
	return snapshot, scheduled, nil
}

// cancelAutoRetries deletes the pending queue item of each given retry kind,
// and resets the server-error backoff when that kind is among them, in one
// transaction. It never touches a dispatching, awaiting-echo, failed, or
// delivery-uncertain row: those describe a delivery already attempted. The
// provider cancels a server-error retry exactly when the cycle ended without
// another retryable failure.
//
// Most calls have nothing to do: every turn end of every provider cancels.
// Such a call reads once, opens no write transaction, and returns an empty
// snapshot and no rows. The rows are those that left the queue, ordered by
// order_index, for the log; the backoff is not part of the snapshot.
func (s *Store) cancelAutoRetries(ctx context.Context, agentID string, kinds ...leapmuxv1.AgentInputKind) (Snapshot, []autoRetryRow, error) {
	if len(kinds) == 0 {
		return Snapshot{}, nil, nil
	}
	for _, kind := range kinds {
		if !IsAutoRetryKind(kind) {
			return Snapshot{}, nil, fmt.Errorf("%w: not an automatic-retry kind", ErrInvalidInput)
		}
	}
	resetBackoff := slices.Contains(kinds, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR)
	kindList := "?" + strings.Repeat(", ?", len(kinds)-1)
	kindArgs := make([]any, len(kinds))
	for i := range kinds {
		kindArgs[i] = kinds[i]
	}
	var work bool
	if err := s.db.QueryRowContext(ctx, `
		SELECT EXISTS(SELECT 1 FROM agent_input_queue_items WHERE agent_id = ? AND state = ? AND kind IN (`+kindList+`))
			OR (? AND EXISTS(SELECT 1 FROM agent_input_queue_state WHERE agent_id = ? AND auto_retry_server_error_delay_ms <> ?))`,
		append(append([]any{agentID, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED}, kindArgs...),
			resetBackoff, agentID, autoRetryInitialDelay.Milliseconds())...).Scan(&work); err != nil {
		return Snapshot{}, nil, err
	}
	if !work {
		return Snapshot{}, nil, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Snapshot{}, nil, err
	}
	defer func() { _ = tx.Rollback() }()
	// The transaction holds the write lock from its BEGIN, so the rows read
	// here are the rows deleted below, and the log states exactly those.
	removed, err := queuedAutoRetries(ctx, tx, agentID, kindList, kindArgs)
	if err != nil {
		return Snapshot{}, nil, err
	}
	for _, row := range removed {
		if _, err := tx.ExecContext(ctx, `DELETE FROM agent_input_queue_items WHERE agent_id = ? AND id = ?`, agentID, row.id); err != nil {
			return Snapshot{}, nil, err
		}
	}
	if resetBackoff {
		if _, err := resetServerErrorDelay(ctx, tx, agentID); err != nil {
			return Snapshot{}, nil, err
		}
	}
	if len(removed) > 0 {
		if err := compactOrder(ctx, tx, agentID); err != nil {
			return Snapshot{}, nil, err
		}
		if err := bumpRevision(ctx, tx, agentID); err != nil {
			return Snapshot{}, nil, err
		}
	}
	snapshot, err := commitSnapshot(ctx, tx, agentID, s.now())
	if err != nil {
		return Snapshot{}, nil, err
	}
	return snapshot, removed, nil
}

// queuedAutoRetries reads the pending (QUEUED) retry of each kind in kindArgs,
// ordered by order_index. kindList holds one placeholder for each kind.
func queuedAutoRetries(ctx context.Context, tx db.DBTX, agentID, kindList string, kindArgs []any) ([]autoRetryRow, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, kind, scheduled_at FROM agent_input_queue_items
		WHERE agent_id = ? AND state = ? AND kind IN (`+kindList+`) ORDER BY order_index`,
		append([]any{agentID, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED}, kindArgs...)...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var queued []autoRetryRow
	for rows.Next() {
		row := autoRetryRow{state: leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED}
		var kind int32
		var scheduled sqltime.SQLiteNullTime
		if err := rows.Scan(&row.id, &kind, &scheduled); err != nil {
			return nil, err
		}
		row.kind, row.scheduledAt = leapmuxv1.AgentInputKind(kind), scheduled.Ptr()
		queued = append(queued, row)
	}
	return queued, rows.Err()
}

// readServerErrorDelay reads the durable server-error backoff. The caller
// ensured the state row.
func readServerErrorDelay(ctx context.Context, tx db.DBTX, agentID string) (time.Duration, error) {
	var delayMs int64
	if err := tx.QueryRowContext(ctx, `SELECT auto_retry_server_error_delay_ms FROM agent_input_queue_state WHERE agent_id = ?`, agentID).Scan(&delayMs); err != nil {
		return 0, err
	}
	return time.Duration(delayMs) * time.Millisecond, nil
}

// writeServerErrorDelay stores the backoff for the next server-error retry.
func writeServerErrorDelay(ctx context.Context, tx db.DBTX, agentID string, delay time.Duration) error {
	_, err := tx.ExecContext(ctx, `UPDATE agent_input_queue_state SET auto_retry_server_error_delay_ms = ?, updated_at = ? WHERE agent_id = ?`,
		delay.Milliseconds(), nowText(), agentID)
	return err
}

// resetServerErrorDelay returns the server-error backoff to its initial
// delay and reports whether it changed. An agent without a state row has no
// backoff to reset, which is no error.
func resetServerErrorDelay(ctx context.Context, tx db.DBTX, agentID string) (bool, error) {
	initial := autoRetryInitialDelay.Milliseconds()
	result, err := tx.ExecContext(ctx, `UPDATE agent_input_queue_state SET auto_retry_server_error_delay_ms = ?, updated_at = ? WHERE agent_id = ? AND auto_retry_server_error_delay_ms <> ?`,
		initial, nowText(), agentID, initial)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	return rows > 0, err
}

// queuedAutoRetry reads the pending (QUEUED) retry of one kind, and reports
// whether it exists. idx_agent_input_queue_one_auto_retry_kind admits at most
// one.
func queuedAutoRetry(ctx context.Context, tx db.DBTX, agentID string, kind leapmuxv1.AgentInputKind) (autoRetryRow, bool, error) {
	row := autoRetryRow{kind: kind, state: leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED}
	var scheduled sqltime.SQLiteNullTime
	err := tx.QueryRowContext(ctx, `SELECT id, scheduled_at FROM agent_input_queue_items WHERE agent_id = ? AND kind = ? AND state = ?`,
		agentID, kind, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED).Scan(&row.id, &scheduled)
	if errors.Is(err, sql.ErrNoRows) {
		return autoRetryRow{}, false, nil
	}
	if err != nil {
		return autoRetryRow{}, false, err
	}
	row.scheduledAt = scheduled.Ptr()
	return row, true, nil
}

// queuedAutoRetryID returns the ID of the pending (QUEUED) retry of one kind,
// or "".
func queuedAutoRetryID(ctx context.Context, tx db.DBTX, agentID string, kind leapmuxv1.AgentInputKind) (string, error) {
	row, _, err := queuedAutoRetry(ctx, tx, agentID, kind)
	return row.id, err
}

// A retry that leaves QUEUED for a dispatch is no longer the pending retry of
// its kind, so a newer report of the kind schedules a second row beside it.
// idx_agent_input_queue_one_auto_retry_kind then refuses the first row's
// return to QUEUED. Before each such return, one of two helpers removes the
// newer row, and who returns the row decides which:
//
//   - absorbQueuedAutoRetry serves an automatic return: a busy refusal, a
//     requeue, a dropped steer. None of them delivered the input, so the
//     returning row stands for the same retry as the newer one.
//   - dropQueuedAutoRetry serves the user's Retry and edit save. The user's
//     action is explicit, so the returning row keeps its own content.
//
// Both run inside the caller's transaction, before its UPDATE to QUEUED, so a
// stale call rolls the removal back with the refused update. Neither resets
// the server-error backoff: the newer row's schedule already wrote the delay
// of the live cycle.

// absorbQueuedAutoRetry merges the newer pending retry of inputID's kind into
// inputID. The returning row keeps its ID and the raw slot that its dispatch
// took, as a reschedule does, and takes the later deadline of the two rows
// with that row's source payload. That is the state that a reschedule after
// the return would produce. The later deadline is the newer schedule: a row
// leaves QUEUED only after its deadline passed or when it has none, and a
// server-error deadline always lies after the instant it is scheduled. A
// usage-limit reset that the provider reports in the past can give the newer
// row the earlier deadline, but then both deadlines passed and the retry is
// immediate either way. It returns the ID of the row that left the queue, or
// "".
func absorbQueuedAutoRetry(ctx context.Context, tx db.DBTX, agentID, inputID string) (string, error) {
	var kind leapmuxv1.AgentInputKind
	var scheduled sqltime.SQLiteNullTime
	err := tx.QueryRowContext(ctx, `SELECT kind, scheduled_at FROM agent_input_queue_items WHERE agent_id = ? AND id = ?`, agentID, inputID).Scan(&kind, &scheduled)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !IsAutoRetryKind(kind) {
		return "", nil
	}
	peerID, err := queuedAutoRetryID(ctx, tx, agentID, kind)
	if err != nil || peerID == "" || peerID == inputID {
		return "", err
	}
	var peerAt sqltime.SQLiteNullTime
	var source []byte
	if err := tx.QueryRowContext(ctx, `SELECT scheduled_at, source_payload FROM agent_input_queue_items WHERE agent_id = ? AND id = ?`, agentID, peerID).Scan(&peerAt, &source); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrConflict
		}
		return "", err
	}
	if peerAt.Valid && (!scheduled.Valid || peerAt.Time.After(scheduled.Time)) {
		if _, err := tx.ExecContext(ctx, `UPDATE agent_input_queue_items SET scheduled_at = ?, source_payload = ?, updated_at = ? WHERE agent_id = ? AND id = ?`, peerAt, ptrconv.OrEmpty(source), nowText(), agentID, inputID); err != nil {
			return "", err
		}
	}
	if err := deleteQueuedAutoRetryPeer(ctx, tx, agentID, peerID); err != nil {
		return "", err
	}
	return peerID, nil
}

// dropQueuedAutoRetry removes the newer pending retry of kind beside inputID,
// which the user returns to QUEUED. kind is the kind that the returning row
// takes, which an edit can reclassify. The caller already validated the item,
// and an ordinary input needs no additional read.
func dropQueuedAutoRetry(ctx context.Context, tx db.DBTX, agentID, inputID string, kind leapmuxv1.AgentInputKind) error {
	if !IsAutoRetryKind(kind) {
		return nil
	}
	peerID, err := queuedAutoRetryID(ctx, tx, agentID, kind)
	if err != nil || peerID == "" || peerID == inputID {
		return err
	}
	return deleteQueuedAutoRetryPeer(ctx, tx, agentID, peerID)
}

func deleteQueuedAutoRetryPeer(ctx context.Context, tx db.DBTX, agentID, peerID string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM agent_input_queue_items WHERE agent_id = ? AND id = ?`, agentID, peerID); err != nil {
		return err
	}
	return compactOrder(ctx, tx, agentID)
}

func (s *Store) Recover(ctx context.Context) ([]Snapshot, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT q.agent_id
		FROM agent_input_queue_state q
		JOIN agents a ON a.id = q.agent_id
		WHERE a.closed_at IS NULL
		ORDER BY q.agent_id`)
	if err != nil {
		return nil, err
	}
	var agentIDs []string
	for rows.Next() {
		var agentID string
		if err := rows.Scan(&agentID); err != nil {
			_ = rows.Close()
			return nil, err
		}
		agentIDs = append(agentIDs, agentID)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	result := make([]Snapshot, 0, len(agentIDs))
	for _, agentID := range agentIDs {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return nil, err
		}
		// A Worker restart invalidates every client session, so no edit lock
		// can still have an owner. A lock left behind refuses the head in
		// prepare forever, and the queue then stops with no pause and no
		// stated reason.
		if _, err := tx.ExecContext(ctx, `UPDATE agent_input_queue_items SET edit_owner = '', updated_at = ? WHERE agent_id = ? AND edit_owner <> ''`, nowText(), agentID); err != nil {
			_ = tx.Rollback()
			return nil, err
		}
		// A Worker restart ends every agent restart it had in flight. A marker
		// left behind would refuse every retry for the life of the queue.
		if _, err := tx.ExecContext(ctx, `UPDATE agent_input_queue_state SET restarting = 0, updated_at = ? WHERE agent_id = ? AND restarting = 1`, nowText(), agentID); err != nil {
			_ = tx.Rollback()
			return nil, err
		}
		var unconfirmed int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_input_queue_items WHERE agent_id = ? AND state IN (?, ?)`, agentID, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DISPATCHING, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_AWAITING_ECHO).Scan(&unconfirmed); err != nil {
			_ = tx.Rollback()
			return nil, err
		}
		var active bool
		if err := tx.QueryRowContext(ctx, `SELECT active_turn FROM agent_input_queue_state WHERE agent_id = ?`, agentID).Scan(&active); err != nil {
			_ = tx.Rollback()
			return nil, err
		}
		if unconfirmed > 0 {
			if _, err := tx.ExecContext(ctx, `UPDATE agent_input_queue_items SET state = ?, error = 'Worker restarted before delivery was confirmed', updated_at = ? WHERE agent_id = ? AND state IN (?, ?)`, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DELIVERY_UNCERTAIN, nowText(), agentID, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DISPATCHING, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_AWAITING_ECHO); err != nil {
				_ = tx.Rollback()
				return nil, err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE agent_input_queue_state SET active_turn = 0, active_turn_steerable = 0, active_input_id = '', paused = 1, pause_reason = ?, pause_owner = ?, revision = revision + 1, updated_at = ? WHERE agent_id = ?`, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_DELIVERY_UNCERTAIN, pauseOwnerRecovery, nowText(), agentID); err != nil {
				_ = tx.Rollback()
				return nil, err
			}
		} else if active {
			if _, err := tx.ExecContext(ctx, `UPDATE agent_input_queue_state SET active_turn = 0, active_turn_steerable = 0, active_input_id = '', paused = 1, pause_reason = ?, pause_owner = ?, revision = revision + 1, updated_at = ? WHERE agent_id = ?`, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_INTERRUPTED, pauseOwnerRecovery, nowText(), agentID); err != nil {
				_ = tx.Rollback()
				return nil, err
			}
		}
		// A deadline that passed while the Worker was down matured its item
		// with no timer to publish the change. One revision bump records it,
		// so an older equal-revision snapshot cannot overwrite the matured
		// order once the queue reaches its owner. The same instant decides
		// the bump and the snapshot, so no item matures between the two.
		now := s.now()
		recoveredItems, err := readOrderingRows(ctx, tx, agentID)
		if err != nil {
			_ = tx.Rollback()
			return nil, err
		}
		if slices.ContainsFunc(recoveredItems, func(item OrderingMetadata) bool {
			return item.ScheduledAt != nil && !scheduledFuture(item, now)
		}) {
			if err := bumpRevision(ctx, tx, agentID); err != nil {
				_ = tx.Rollback()
				return nil, err
			}
		}
		snapshot, err := commitSnapshot(ctx, tx, agentID, now)
		if err != nil {
			_ = tx.Rollback()
			return nil, err
		}
		result = append(result, snapshot)
	}
	return result, nil
}

// ensureState and reserveMessageSeq run the generated statements, so sqlc
// checks them against the schema at generation time. Every other statement in
// this file is hand-written because a generated SELECT * cannot express what
// it needs -- a truncated preview, or a join across the whole queue.
func ensureState(ctx context.Context, tx *sql.Tx, agentID string) error {
	return db.New(tx).EnsureAgentInputQueueState(ctx, agentID)
}

func reserveMessageSeq(ctx context.Context, tx *sql.Tx, agentID string) (int64, error) {
	return db.New(tx).ReserveAgentMessageSeq(ctx, agentID)
}

func bumpRevision(ctx context.Context, tx db.DBTX, agentID string) error {
	_, err := tx.ExecContext(ctx, `UPDATE agent_input_queue_state SET revision = revision + 1, updated_at = ? WHERE agent_id = ?`, nowText(), agentID)
	return err
}

func replaceAttachments(ctx context.Context, tx *sql.Tx, itemID string, attachments []Attachment) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM agent_input_queue_attachments WHERE item_id = ?`, itemID); err != nil {
		return err
	}
	for i := range attachments {
		if _, err := tx.ExecContext(ctx, `INSERT INTO agent_input_queue_attachments (item_id, position, filename, mime_type, data, size) VALUES (?, ?, ?, ?, ?, ?)`, itemID, i, attachments[i].Filename, attachments[i].MimeType, attachments[i].Data, len(attachments[i].Data)); err != nil {
			return err
		}
	}
	return nil
}

func attachmentsEqual(a, b []Attachment) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Filename != b[i].Filename || a[i].MimeType != b[i].MimeType || !bytes.Equal(a[i].Data, b[i].Data) {
			return false
		}
	}
	return true
}

func getItem(ctx context.Context, tx db.DBTX, agentID, inputID string, loadAttachments bool) (StoredItem, []Attachment, bool, error) {
	var item StoredItem
	var kind, state int32
	var version int64
	var scheduled sqltime.SQLiteNullTime
	err := tx.QueryRowContext(ctx, `
		SELECT id, agent_id, kind, text, target_mode, prepare_context, reclassify_on_edit, order_index,
			scheduled_at, source_payload, state, error, edit_owner, version, reserved_seq, created_at, updated_at
		FROM agent_input_queue_items WHERE agent_id = ? AND id = ?`, agentID, inputID).Scan(
		&item.ID, &item.AgentID, &kind, &item.Text, &item.TargetMode, &item.PrepareContext, &item.ReclassifyOnEdit, &item.Order,
		&scheduled, &item.SourcePayload, &state, &item.Error,
		&item.EditOwner, &version, &item.ReservedSeq, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return StoredItem{}, nil, false, nil
	}
	if err != nil {
		return StoredItem{}, nil, false, err
	}
	item.Kind = leapmuxv1.AgentInputKind(kind)
	item.State = leapmuxv1.AgentInputState(state)
	item.Version = uint64(version)
	item.ScheduledAt = scheduled.Ptr()
	if len(item.SourcePayload) == 0 {
		// nil states "no source"; the driver scans an empty BLOB as an empty
		// slice, which would make two equal rows compare unequal.
		item.SourcePayload = nil
	}
	if !loadAttachments {
		return item, nil, true, nil
	}
	attachments, err := loadItemAttachments(ctx, tx, inputID)
	return item, attachments, true, err
}

// readOrderingRows reads every item's ordering metadata -- no text, no source
// payload, no attachment data -- which is all the priority comparator and the
// reorder-group rules read.
func readOrderingRows(ctx context.Context, tx db.DBTX, agentID string) ([]OrderingMetadata, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, agent_id, kind, order_index, scheduled_at, state, edit_owner, prepare_context
		FROM agent_input_queue_items WHERE agent_id = ? ORDER BY order_index`, agentID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var items []OrderingMetadata
	for rows.Next() {
		var item OrderingMetadata
		var kind, state int32
		var scheduled sqltime.SQLiteNullTime
		if err := rows.Scan(&item.ID, &item.AgentID, &kind, &item.Order, &scheduled, &state, &item.EditOwner, &item.PrepareContext); err != nil {
			return nil, err
		}
		item.Kind = leapmuxv1.AgentInputKind(kind)
		item.State = leapmuxv1.AgentInputState(state)
		item.ScheduledAt = scheduled.Ptr()
		items = append(items, item)
	}
	return items, rows.Err()
}

// headItem reads the full row of the prioritized first item that does not
// await an echo. It is the one definition of the head for every reader that
// does not already hold a sorted snapshot.
func headItem(ctx context.Context, tx db.DBTX, agentID string, now time.Time) (StoredItem, bool, error) {
	ordering, err := readQueueOrdering(ctx, tx, agentID, now)
	if err != nil {
		return StoredItem{}, false, err
	}
	return headItemFromOrdering(ctx, tx, agentID, ordering)
}

func headItemFromOrdering(ctx context.Context, tx db.DBTX, agentID string, ordering queueOrdering) (StoredItem, bool, error) {
	for _, head := range ordering.effective {
		if head.State == leapmuxv1.AgentInputState_AGENT_INPUT_STATE_AWAITING_ECHO {
			continue
		}
		item, _, found, err := getItem(ctx, tx, agentID, head.ID, false)
		if err != nil {
			return StoredItem{}, false, err
		}
		if !found {
			// The ordering read and this read share one transaction.
			return StoredItem{}, false, ErrConflict
		}
		return item, true, nil
	}
	return StoredItem{}, false, nil
}

func loadItemAttachments(ctx context.Context, tx db.DBTX, inputID string) ([]Attachment, error) {
	rows, err := tx.QueryContext(ctx, `SELECT filename, mime_type, data FROM agent_input_queue_attachments WHERE item_id = ? ORDER BY position`, inputID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var attachments []Attachment
	for rows.Next() {
		var item Attachment
		if err := rows.Scan(&item.Filename, &item.MimeType, &item.Data); err != nil {
			return nil, err
		}
		item.Data = slices.Clone(item.Data)
		attachments = append(attachments, item)
	}
	return attachments, rows.Err()
}

// snapshotTx reads one agent's queue in effective order at the instant now.
// The items carry a text preview and no source payload: no client reads the
// payload, and a row can hold megabytes of it. The dispatch path reads the
// full row through getItem.
func snapshotTx(ctx context.Context, tx db.DBTX, agentID string, now time.Time) (Snapshot, error) {
	snapshot := Snapshot{AgentID: agentID}
	var revision int64
	var reason *leapmuxv1.AgentInputQueuePauseReason
	err := tx.QueryRowContext(ctx, `SELECT revision, paused, pause_reason, active_turn, active_turn_steerable FROM agent_input_queue_state WHERE agent_id = ?`, agentID).Scan(&revision, &snapshot.Paused, &reason, &snapshot.ActiveTurn, &snapshot.ActiveTurnSteerable)
	if errors.Is(err, sql.ErrNoRows) {
		return snapshot, nil
	}
	if err != nil {
		return Snapshot{}, err
	}
	snapshot.Revision = uint64(revision)
	snapshot.PauseReason = workerdb.StorageEnumValue(reason)
	rows, err := tx.QueryContext(ctx, `
		SELECT id, kind,
			CASE WHEN length(text) > ? THEN substr(text, 1, ?) || '…' ELSE text END,
			target_mode, prepare_context, reclassify_on_edit, order_index, scheduled_at, state, error, edit_owner, version, reserved_seq, created_at, updated_at
		FROM agent_input_queue_items WHERE agent_id = ? ORDER BY order_index`,
		snapshotTextPreviewCharacters, snapshotTextPreviewCharacters, agentID)
	if err != nil {
		return Snapshot{}, err
	}
	var items []SnapshotItem
	for rows.Next() {
		var item SnapshotItem
		var kind, state int32
		var version int64
		var scheduled sqltime.SQLiteNullTime
		item.AgentID = agentID
		if err := rows.Scan(&item.ID, &kind, &item.TextPreview, &item.TargetMode, &item.PrepareContext, &item.ReclassifyOnEdit, &item.Order, &scheduled, &state, &item.Error, &item.EditOwner, &version, &item.ReservedSeq, &item.CreatedAt, &item.UpdatedAt); err != nil {
			_ = rows.Close()
			return Snapshot{}, err
		}
		item.Kind = leapmuxv1.AgentInputKind(kind)
		item.State = leapmuxv1.AgentInputState(state)
		item.Version = uint64(version)
		item.ScheduledAt = scheduled.Ptr()
		items = append(items, item)
	}
	if err := rows.Close(); err != nil {
		return Snapshot{}, err
	}
	// ONE priority rule: the snapshot sorts with the same comparator every
	// read uses, so what the client sees is what the dispatch guard admits.
	// The public position is the effective one, not the raw order_index
	// slot, because a future item with a small slot must not render first.
	items = prioritizedItems(items, now)
	segments := reorderSegments(items, now)
	for i := range items {
		item := items[i]
		item.Position = int64(i + 1)
		item.ReorderGroup = reorderGroup(item, now)
		item.ReorderSegment = segments[i]
		snapshot.Items = append(snapshot.Items, item)
	}
	snapshot.NextDeadline = nextScheduledAt(items, now)
	// One query for every item's attachment metadata. A per-item query costs
	// one round trip for each of up to MaxItems entries, inside the write
	// transaction that every queue mutation already holds.
	metadata, err := loadQueueAttachmentMetadata(ctx, tx, agentID)
	if err != nil {
		return Snapshot{}, err
	}
	for i := range snapshot.Items {
		snapshot.Items[i].Metadata = metadata[snapshot.Items[i].ID]
	}
	head := snapshot.HeadIndex()
	if head < 0 {
		return snapshot, nil
	}
	// Only the head can be steered or preempted, so only the head asks the
	// gate. The snapshot already sorted it, so the gate reads no rows again.
	gate, err := readDispatchGateFor(ctx, tx, agentID, now, snapshot.Items[head].OrderingMetadata, true)
	if err != nil {
		return Snapshot{}, err
	}
	// Steering dispatches THROUGH the running turn, which is prepare's requireActive
	// path, and it dispatches through a pause, which is allowPaused.
	snapshot.Items[head].CanSteer = gate.admits(true, true) == dispatchAdmitted
	// Preemption asks two questions of the SAME guard, which is why it needs no terms
	// of its own. Would the turn-end drain deliver this head once the turn is
	// cancelled? And does it need the cancel at all -- a plan approval that REPLACES
	// the turn already dispatches through it, so cancelling would destroy work for
	// nothing.
	//
	// Preemption borrows no kind filter. Steering injects into the running turn, so it
	// admits only a kind the model can take mid-turn; preemption injects nothing, and
	// the drain dispatches every kind once the turn is over.
	afterInterrupt := gate
	afterInterrupt.ActiveTurn = false
	snapshot.Items[head].CanPreempt = snapshot.ActiveTurn &&
		afterInterrupt.admits(false, false) == dispatchAdmitted &&
		gate.admits(false, false) != dispatchAdmitted
	return snapshot, nil
}

// loadQueueAttachmentMetadata reads every attachment's metadata for one
// agent's queue, keyed by item ID. It never reads the attachment data.
func loadQueueAttachmentMetadata(ctx context.Context, tx db.DBTX, agentID string) (map[string][]AttachmentMetadata, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT a.item_id, a.filename, a.mime_type, a.size
		FROM agent_input_queue_attachments a
		JOIN agent_input_queue_items i ON i.id = a.item_id
		WHERE i.agent_id = ?
		ORDER BY a.item_id, a.position`, agentID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	byItem := make(map[string][]AttachmentMetadata)
	for rows.Next() {
		var itemID, filename, mimeType string
		var size int64
		if err := rows.Scan(&itemID, &filename, &mimeType, &size); err != nil {
			return nil, err
		}
		byItem[itemID] = append(byItem[itemID], AttachmentMetadata{
			Filename: filename, MimeType: mimeType, Size: size,
			Order: int32(len(byItem[itemID])),
		})
	}
	return byItem, rows.Err()
}

func commitSnapshot(ctx context.Context, tx *sql.Tx, agentID string, now time.Time) (Snapshot, error) {
	snapshot, err := snapshotTx(ctx, tx, agentID, now)
	if err != nil {
		return Snapshot{}, err
	}
	if err := tx.Commit(); err != nil {
		return Snapshot{}, err
	}
	return snapshot, nil
}

// placeControlReply keeps the current request's reply before future queued work.
// Existing replies and a dispatch or failure at the front retain their order.
func placeControlReply(ctx context.Context, tx *sql.Tx, agentID, inputID string) error {
	rows, err := tx.QueryContext(ctx, `SELECT id, kind, state FROM agent_input_queue_items WHERE agent_id = ? ORDER BY order_index`, agentID)
	if err != nil {
		return err
	}
	var ids []string
	position := -1
	for rows.Next() {
		var id string
		var kind leapmuxv1.AgentInputKind
		var state leapmuxv1.AgentInputState
		if err := rows.Scan(&id, &kind, &state); err != nil {
			_ = rows.Close()
			return err
		}
		if id == inputID {
			continue
		}
		if position < 0 && !controlReplyKind(kind) && state == leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED {
			position = len(ids)
		}
		ids = append(ids, id)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	if position < 0 {
		return nil
	}
	return writeOrder(ctx, tx, agentID, slices.Insert(ids, position, inputID))
}

// placeDispatchedItem writes a dispatched item's effective position into its
// raw order_index slot. The item moves directly behind the rows that sort
// ahead of it, which are the steers that await an echo, and ahead of every
// other row. Every other row keeps its relative raw order.
//
// The comparator orders the immediate group by raw slot. Without this write,
// a future item with a smaller slot sorts ahead of the dispatched item once
// its deadline passes. The dispatched item then loses the head in every state
// that it reaches from DISPATCHING: AWAITING_ECHO, FAILED, and
// DELIVERY_UNCERTAIN. Retry refuses it with ErrNotHead, the matured item
// offers a Preempt that cancels the dispatch's own turn, and the matured item
// dispatches ahead of an unconfirmed delivery. The slot also survives a return
// to QUEUED (a busy refusal, a requeue, a Retry), so the queue resumes the
// same input. prepare is the only write into DISPATCHING, so this one call
// covers every attempted state.
//
// The item takes its effective position, not a place behind every attempted
// row: a user may move queued input ahead of a FAILED row on purpose, and that
// input must stay the head while it dispatches. A queue whose effective order
// equals its raw order, as every queue without a future item does, writes
// nothing.
func placeDispatchedItem(ctx context.Context, tx db.DBTX, agentID, inputID string, ordering queueOrdering) error {
	raw, effective := ordering.raw, ordering.effective
	at := indexOfItem(effective, inputID)
	if at < 0 {
		return ErrConflict
	}
	ahead := make(map[string]bool, at)
	order := make([]string, 0, len(raw))
	for _, item := range effective[:at] {
		ahead[item.ID] = true
		order = append(order, item.ID)
	}
	order = append(order, inputID)
	for _, item := range raw {
		if item.ID != inputID && !ahead[item.ID] {
			order = append(order, item.ID)
		}
	}
	if slices.EqualFunc(order, raw, func(id string, item OrderingMetadata) bool { return id == item.ID }) {
		return nil
	}
	return writeOrder(ctx, tx, agentID, order)
}

func compactOrder(ctx context.Context, tx db.DBTX, agentID string) error {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM agent_input_queue_items WHERE agent_id = ? ORDER BY order_index`, agentID)
	if err != nil {
		return err
	}
	var itemIDs []string
	for rows.Next() {
		var itemID string
		if err := rows.Scan(&itemID); err != nil {
			_ = rows.Close()
			return err
		}
		itemIDs = append(itemIDs, itemID)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	return writeOrder(ctx, tx, agentID, itemIDs)
}

// orderScratchOffset lifts a row out of the active order_index key space
// while rows exchange slots, which avoids a transient UNIQUE(agent_id,
// order_index) conflict. Every real slot is far below it, because a queue
// holds at most MaxItems rows.
const orderScratchOffset = 1000000

// writeOrder renumbers every row of the agent to 1..n in the given order.
func writeOrder(ctx context.Context, tx db.DBTX, agentID string, itemIDs []string) error {
	if _, err := tx.ExecContext(ctx, `UPDATE agent_input_queue_items SET order_index = order_index + ? WHERE agent_id = ?`, orderScratchOffset, agentID); err != nil {
		return err
	}
	for i := range itemIDs {
		if _, err := tx.ExecContext(ctx, `UPDATE agent_input_queue_items SET order_index = ? WHERE agent_id = ? AND id = ?`, i+1, agentID, itemIDs[i]); err != nil {
			return err
		}
	}
	return nil
}

// writeOrderSlots reassigns the given slots, in order, to the given rows. It
// touches only those rows, so every other group and locked row keeps its
// slots.
func writeOrderSlots(ctx context.Context, tx db.DBTX, agentID string, itemIDs []string, slots []int64) error {
	if len(itemIDs) != len(slots) {
		return fmt.Errorf("%w: reorder segment mismatch", ErrInvalidInput)
	}
	for i := range itemIDs {
		if _, err := tx.ExecContext(ctx, `UPDATE agent_input_queue_items SET order_index = ? WHERE agent_id = ? AND id = ?`, slots[i]+orderScratchOffset, agentID, itemIDs[i]); err != nil {
			return err
		}
	}
	for i := range itemIDs {
		if _, err := tx.ExecContext(ctx, `UPDATE agent_input_queue_items SET order_index = ? WHERE agent_id = ? AND id = ?`, slots[i], agentID, itemIDs[i]); err != nil {
			return err
		}
	}
	return nil
}

// validateQueueAttachmentCapacity checks the total after insertion or replacement.
func validateQueueAttachmentCapacity(ctx context.Context, tx *sql.Tx, agentID, excludedInputID string, attachments []Attachment) error {
	var aggregate int64
	if err := tx.QueryRowContext(ctx, `
  SELECT COALESCE(SUM(a.size), 0)
  FROM agent_input_queue_attachments a
  JOIN agent_input_queue_items i ON i.id = a.item_id
  WHERE i.agent_id = ? AND (? = '' OR i.id <> ?)`, agentID, excludedInputID, excludedInputID).Scan(&aggregate); err != nil {
		return err
	}
	if aggregate+attachmentBytes(attachments) > MaxQueueAttachmentBytes {
		return ErrQueueAttachmentsLarge
	}
	return nil
}

// insertQueuedItem appends one QUEUED row at the end of the raw order. It
// owns the queue's capacity rules and the order-index allocation, so every
// creation path -- user input and automatic retries -- keeps both.
func insertQueuedItem(ctx context.Context, tx *sql.Tx, input NewItem) error {
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_input_queue_items WHERE agent_id = ?`, input.AgentID).Scan(&count); err != nil {
		return err
	}
	if count >= MaxItems {
		return ErrQueueFull
	}
	if len(input.Attachments) > 0 {
		if err := validateQueueAttachmentCapacity(ctx, tx, input.AgentID, "", input.Attachments); err != nil {
			return err
		}
	}

	var orderIndex int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(order_index), 0) + 1 FROM agent_input_queue_items WHERE agent_id = ?`, input.AgentID).Scan(&orderIndex); err != nil {
		return err
	}
	created := nowText()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO agent_input_queue_items
		(id, agent_id, order_index, kind, text, target_mode, prepare_context, reclassify_on_edit, scheduled_at, source_payload, state, version, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?)`,
		input.ID, input.AgentID, orderIndex, input.Kind, input.Text, input.TargetMode, input.PrepareContext, input.ReclassifyOnEdit,
		sqltime.NewSQLiteNullTime(input.ScheduledAt), ptrconv.OrEmpty(input.SourcePayload),
		leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED, created, created); err != nil {
		return normalizeConstraintError(err)
	}
	return nil
}

// schedulesEqual compares two optional instants by instant, not by pointer or
// by location, so a UTC and a +09:00 representation of one deadline match.
func schedulesEqual(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}

// transcriptContent stores the original input text and independent optional fields.
// The plan-execution marker remains present with or without attachments.
func transcriptContent(item StoredItem, attachments []Attachment) ([]byte, error) {
	content := map[string]any{"content": item.Text}
	if item.Kind == leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_PLAN_EXECUTION {
		content["planExecution"] = true
	}
	if len(attachments) != 0 {
		type metadata struct {
			Filename string `json:"filename"`
			MimeType string `json:"mime_type"`
		}
		meta := make([]metadata, len(attachments))
		for i := range attachments {
			meta[i] = metadata{Filename: attachments[i].Filename, MimeType: attachments[i].MimeType}
		}
		content["attachments"] = meta
	}
	return json.Marshal(content)
}

// inputIdempotencyKey includes the input identity and content. A changed retry
// gets a different key and conflicts with the existing message identifier.
// The schedule and the retry source payload are content too: the same input
// id re-sent with a different schedule must not reuse the accepted row.
func inputIdempotencyKey(input NewItem) string {
	type idempotencyAttachment struct {
		Filename string `json:"filename"`
		MimeType string `json:"mime_type"`
		Data     []byte `json:"data"`
	}
	type idempotencyPayload struct {
		ID               string                   `json:"id"`
		AgentID          string                   `json:"agent_id"`
		Kind             leapmuxv1.AgentInputKind `json:"kind"`
		Text             string                   `json:"text"`
		TargetMode       string                   `json:"target_mode"`
		PrepareContext   bool                     `json:"prepare_context"`
		ReclassifyOnEdit bool                     `json:"reclassify_on_edit"`
		ScheduledAt      *string                  `json:"scheduled_at,omitempty"`
		SourcePayload    []byte                   `json:"source_payload,omitempty"`
		Attachments      []idempotencyAttachment  `json:"attachments"`
	}
	attachments := make([]idempotencyAttachment, len(input.Attachments))
	for index := range input.Attachments {
		attachments[index] = idempotencyAttachment(input.Attachments[index])
	}
	var scheduled *string
	if input.ScheduledAt != nil {
		text := timefmt.Format(*input.ScheduledAt)
		scheduled = &text
	}
	data, err := json.Marshal(idempotencyPayload{
		ID: input.ID, AgentID: input.AgentID, Kind: input.Kind, Text: input.Text,
		TargetMode: input.TargetMode, PrepareContext: input.PrepareContext,
		ReclassifyOnEdit: input.ReclassifyOnEdit, ScheduledAt: scheduled,
		SourcePayload: input.SourcePayload, Attachments: attachments,
	})
	if err != nil {
		panic(fmt.Sprintf("marshal queued input idempotency key: %v", err))
	}
	sum := sha256.Sum256(data)
	return inputIdempotencyKeyPrefix + hex.EncodeToString(sum[:])
}

func normalizeConstraintError(err error) error {
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "unique constraint") {
		return ErrConflict
	}
	return err
}
