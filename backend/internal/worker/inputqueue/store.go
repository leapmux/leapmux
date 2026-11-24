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
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coder/quartz"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	"github.com/leapmux/leapmux/internal/util/ptrconv"
	"github.com/leapmux/leapmux/internal/util/sqltime"
	"github.com/leapmux/leapmux/internal/util/timefmt"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	workerdb "github.com/leapmux/leapmux/internal/worker/db"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

const inputIdempotencyKeyPrefix = "input:"

type agentEphemeralState struct {
	restarting                  bool
	activeTurnSteerable         bool
	activeInputID               string
	bypassBackgroundWorkInputID string
	editItemID                  string
	editOwner                   string
	reservedSeqs                map[string]int64
}

func (e agentEphemeralState) isZero() bool {
	return !e.restarting && !e.activeTurnSteerable && e.activeInputID == "" &&
		e.bypassBackgroundWorkInputID == "" && e.editItemID == "" && e.editOwner == "" &&
		len(e.reservedSeqs) == 0
}

func (e agentEphemeralState) clone() agentEphemeralState {
	cloned := e
	if len(e.reservedSeqs) > 0 {
		cloned.reservedSeqs = maps.Clone(e.reservedSeqs)
	} else {
		cloned.reservedSeqs = nil
	}
	return cloned
}

func (e *agentEphemeralState) editOwnerOf(itemID string) string {
	if e != nil && e.editItemID == itemID {
		return e.editOwner
	}
	return ""
}

func (e *agentEphemeralState) reservedSeqOf(itemID string) int64 {
	if e != nil {
		return e.reservedSeqs[itemID]
	}
	return 0
}

func (e *agentEphemeralState) setReservedSeq(itemID string, seq int64) {
	if seq <= 0 {
		if e.reservedSeqs != nil {
			delete(e.reservedSeqs, itemID)
			if len(e.reservedSeqs) == 0 {
				e.reservedSeqs = nil
			}
		}
		return
	}
	if e.reservedSeqs == nil {
		e.reservedSeqs = make(map[string]int64)
	}
	e.reservedSeqs[itemID] = seq
}

func (e *agentEphemeralState) clearEditIf(itemID string) {
	if e.editItemID == itemID {
		e.editItemID = ""
		e.editOwner = ""
	}
}

func (e *agentEphemeralState) clearItem(itemID string) {
	e.clearEditIf(itemID)
	e.setReservedSeq(itemID, 0)
}

type Store struct {
	db         *sql.DB
	classifier CommandClassifier
	// clock supplies the instant every schedule decision reads. Priority is
	// computed from scheduled_at and this clock at read time -- never frozen
	// into order_index -- so a future item joins the immediate group the
	// moment its deadline passes.
	clock quartz.Clock
	// ephemeralMu protects ephemeral and serializes commits with read snapshots.
	// Global lock order places ephemeralMu strictly after SQLite's write lock:
	// never acquire ephemeralMu before BeginTx.
	ephemeralMu sync.RWMutex
	ephemeral   map[string]agentEphemeralState
	// beforeAcceptWrite runs inside Accept, after every read and before the first
	// write. TESTS ONLY, and nil everywhere else: a second connection tries to
	// write here to prove Accept already owns write intent.
	beforeAcceptWrite func()
	// beforeCommit runs inside commitEphemeral right before tx.Commit().
	// TESTS ONLY, and nil everywhere else.
	beforeCommit func() error
}

// NewStore builds a queue store over db. A nil clock installs the real one.
func NewStore(db *sql.DB, clock quartz.Clock) *Store {
	if clock == nil {
		clock = quartz.NewReal()
	}
	return &Store{
		db:         db,
		classifier: ExactCommandClassifier{},
		clock:      clock,
		ephemeral:  make(map[string]agentEphemeralState),
	}
}

func (s *Store) cloneEphemeral(agentID string) agentEphemeralState {
	s.ephemeralMu.RLock()
	defer s.ephemeralMu.RUnlock()
	return s.ephemeral[agentID].clone()
}

// ClearEphemeralState drops all in-memory ephemeral queue state for agentID.
func (s *Store) ClearEphemeralState(agentID string) {
	s.ephemeralMu.Lock()
	delete(s.ephemeral, agentID)
	s.ephemeralMu.Unlock()
}

func (s *Store) commitEphemeral(tx *sql.Tx, agentID string, staged agentEphemeralState) error {
	s.ephemeralMu.RLock()
	beforeCommit := s.beforeCommit
	s.ephemeralMu.RUnlock()
	if beforeCommit != nil {
		if err := beforeCommit(); err != nil {
			return err
		}
	}
	s.ephemeralMu.Lock()
	defer s.ephemeralMu.Unlock()
	if err := tx.Commit(); err != nil {
		return err
	}
	if staged.isZero() {
		delete(s.ephemeral, agentID)
	} else {
		s.ephemeral[agentID] = staged.clone()
	}
	return nil
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
	staged := s.cloneEphemeral(input.AgentID)
	now := s.now()
	commit := func() (Snapshot, error) {
		if apply != nil {
			if err := apply(tx); err != nil {
				return Snapshot{}, err
			}
		}
		return s.commitSnapshot(ctx, tx, input.AgentID, now, staged)
	}
	if err := ensureState(ctx, tx, input.AgentID); err != nil {
		return Snapshot{}, false, err
	}

	existing, existingAttachments, found, err := getItemWithEphemeral(ctx, tx, input.AgentID, input.ID, true, &staged)
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
		if err := placeControlReply(ctx, tx, input.AgentID, input.ID, &staged); err != nil {
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
	// Hold ephemeralMu.RLock across the initial SELECT so SQLite's WAL read
	// snapshot and the copied ephemeral state match the exact same commit point.
	s.ephemeralMu.RLock()
	stateRow, found, err := readQueueStateRow(ctx, tx, agentID)
	ephem := s.ephemeral[agentID].clone()
	s.ephemeralMu.RUnlock()
	if err != nil {
		return Snapshot{}, err
	}
	snapshot, err := snapshotTxWithState(ctx, tx, agentID, s.now(), &ephem, stateRow, found)
	if err != nil {
		return Snapshot{}, err
	}
	if err := tx.Commit(); err != nil {
		return Snapshot{}, err
	}
	return snapshot, nil
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
	staged := s.cloneEphemeral(agentID)
	item, attachments, found, err := getItemWithEphemeral(ctx, tx, agentID, inputID, true, &staged)
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
	if staged.editItemID != "" && staged.editItemID != inputID && !takeover {
		return Snapshot{}, "", nil, ErrEditOwned
	}
	staged.editItemID = inputID
	staged.editOwner = clientID
	staged.bypassBackgroundWorkInputID = ""
	if err := bumpRevision(ctx, tx, agentID); err != nil {
		return Snapshot{}, "", nil, err
	}
	snapshot, err := s.commitSnapshot(ctx, tx, agentID, s.now(), staged)
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
	staged := s.cloneEphemeral(request.AgentID)
	item, _, found, err := getItemWithEphemeral(ctx, tx, request.AgentID, request.InputID, false, &staged)
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
	if err := dropQueuedAutoRetry(ctx, tx, request.AgentID, request.InputID, kind, &staged); err != nil {
		return Snapshot{}, err
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE agent_input_queue_items
		SET kind = ?, text = ?, scheduled_at = CASE WHEN ? THEN ? ELSE scheduled_at END, state = ?, error = '', version = version + 1, updated_at = ?
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
	staged.clearItem(request.InputID)
	staged.bypassBackgroundWorkInputID = ""
	if err := bumpRevision(ctx, tx, request.AgentID); err != nil {
		return Snapshot{}, err
	}
	return s.commitSnapshot(ctx, tx, request.AgentID, s.now(), staged)
}

func (s *Store) CancelEdit(ctx context.Context, agentID, inputID, clientID string) (Snapshot, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Snapshot{}, err
	}
	defer func() { _ = tx.Rollback() }()
	staged := s.cloneEphemeral(agentID)
	item, _, found, err := getItemWithEphemeral(ctx, tx, agentID, inputID, false, &staged)
	if err != nil {
		return Snapshot{}, err
	}
	if !found {
		return Snapshot{}, ErrNotFound
	}
	if item.EditOwner != clientID || clientID == "" {
		return Snapshot{}, ErrEditOwnerMismatch
	}
	staged.clearEditIf(inputID)
	if err := bumpRevision(ctx, tx, agentID); err != nil {
		return Snapshot{}, err
	}
	return s.commitSnapshot(ctx, tx, agentID, s.now(), staged)
}

func (s *Store) Delete(ctx context.Context, agentID, inputID string) (Snapshot, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Snapshot{}, err
	}
	defer func() { _ = tx.Rollback() }()
	staged := s.cloneEphemeral(agentID)
	item, _, found, err := getItemWithEphemeral(ctx, tx, agentID, inputID, false, &staged)
	if err != nil {
		return Snapshot{}, err
	}
	if !found {
		return Snapshot{}, ErrNotFound
	}
	if lockedInputState(item.State) {
		return Snapshot{}, ErrConflict
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM agent_input_queue_items WHERE agent_id = ? AND id = ?`, agentID, inputID); err != nil {
		return Snapshot{}, err
	}
	// Deleting a server-error retry ends its cycle by hand, so the backoff it
	// accumulated resets with it, unless a pending retry of the kind remains:
	// a failed or uncertain row beside it is a stale attempt, and the pending
	// row still carries the live cycle and its backoff.
	if item.Kind == leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR {
		pendingID, err := queuedAutoRetryID(ctx, tx, agentID, item.Kind)
		if err != nil {
			return Snapshot{}, err
		}
		if pendingID == "" {
			if _, err := resetServerErrorDelay(ctx, tx, agentID); err != nil {
				return Snapshot{}, err
			}
		}
	}
	if err := compactOrder(ctx, tx, agentID); err != nil {
		return Snapshot{}, err
	}
	staged.clearItem(inputID)
	staged.bypassBackgroundWorkInputID = ""
	if err := bumpRevision(ctx, tx, agentID); err != nil {
		return Snapshot{}, err
	}
	return s.commitSnapshot(ctx, tx, agentID, s.now(), staged)
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
	staged := s.cloneEphemeral(agentID)
	now := s.now()
	items, err := readOrderingRows(ctx, tx, agentID, &staged)
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
		return s.commitSnapshot(ctx, tx, agentID, now, staged)
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
	staged.bypassBackgroundWorkInputID = ""
	if err := bumpRevision(ctx, tx, agentID); err != nil {
		return Snapshot{}, err
	}
	return s.commitSnapshot(ctx, tx, agentID, now, staged)
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
	staged := s.cloneEphemeral(agentID)
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
		staged.bypassBackgroundWorkInputID = ""
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_input_queue_state SET pause_reason = ?, pause_owner = ?, revision = revision + 1, updated_at = ? WHERE agent_id = ?`, workerdb.OptionalStorageEnum(reason), workerdb.OptionalStorageEnum(leapmuxv1.AgentInputQueuePauseOwner(owner)), nowText(), agentID); err != nil {
		return Snapshot{}, err
	}
	return s.commitSnapshot(ctx, tx, agentID, s.now(), staged)
}

// resumeWithNoRestart clears the pause and any marker a failed finish left.
// The manager holds its coordinator lock and has no active restart guard.
// Failed and uncertain inputs keep their outcomes until an explicit retry.
func (s *Store) resumeWithNoRestart(ctx context.Context, agentID string) (Snapshot, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Snapshot{}, err
	}
	defer func() { _ = tx.Rollback() }()
	staged := s.cloneEphemeral(agentID)
	if err := ensureState(ctx, tx, agentID); err != nil {
		return Snapshot{}, err
	}
	staged.restarting = false
	if _, err := tx.ExecContext(ctx, `UPDATE agent_input_queue_state
SET pause_reason = NULL, pause_owner = NULL, revision = revision + 1, updated_at = ?
WHERE agent_id = ?`, nowText(), agentID); err != nil {
		return Snapshot{}, err
	}
	return s.commitSnapshot(ctx, tx, agentID, s.now(), staged)
}

// PauseForArchive stops the active turn and records an archive-created pause.
// The Boolean reports whether the durable state changed.
func (s *Store) PauseForArchive(ctx context.Context, agentID string) (Snapshot, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Snapshot{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	staged := s.cloneEphemeral(agentID)
	if err := ensureState(ctx, tx, agentID); err != nil {
		return Snapshot{}, false, err
	}
	result, err := pauseAndEndTurn(ctx, tx, agentID,
		leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_AGENT_STOPPED, pauseOwnerArchive, &staged)
	if err != nil {
		return Snapshot{}, false, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return Snapshot{}, false, err
	}
	snapshot, err := s.commitSnapshot(ctx, tx, agentID, s.now(), staged)
	return snapshot, changed == 1, err
}

func (s *Store) pauseForPlannedRestart(ctx context.Context, agentID string) (Snapshot, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Snapshot{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	staged := s.cloneEphemeral(agentID)
	if err := ensureState(ctx, tx, agentID); err != nil {
		return Snapshot{}, false, err
	}
	// The pause and the marker answer different questions, so they are written
	// separately. The pause holds only when this restart created it, which is
	// what the returned Boolean reports; the marker holds either way, because
	// a write to a stopping process is unsafe however the queue came to pause.
	result, err := tx.ExecContext(ctx, `
		UPDATE agent_input_queue_state
		SET pause_reason = ?, pause_owner = ?, revision = revision + 1, updated_at = ?
		WHERE agent_id = ? AND pause_reason IS NULL`,
		leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_AGENT_STOPPED, pauseOwnerPlannedRestart, nowText(), agentID)
	if err != nil {
		return Snapshot{}, false, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return Snapshot{}, false, err
	}
	staged.restarting = true
	staged.bypassBackgroundWorkInputID = ""
	snapshot, err := s.commitSnapshot(ctx, tx, agentID, s.now(), staged)
	return snapshot, changed == 1, err
}

type inputRecoveryPause struct {
	reason leapmuxv1.AgentInputQueuePauseReason
	owner  pauseOwner
}

// pauseForInputRecovery runs with the manager's exclusive restart guard.
// A stored marker then belongs to an already completed guard. Adopt eligible
// process pauses, and retain previous authority for a refused approval.
func (s *Store) pauseForInputRecovery(ctx context.Context, agentID string) (Snapshot, inputRecoveryPause, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Snapshot{}, inputRecoveryPause{}, err
	}
	defer func() { _ = tx.Rollback() }()
	staged := s.cloneEphemeral(agentID)
	if err := ensureState(ctx, tx, agentID); err != nil {
		return Snapshot{}, inputRecoveryPause{}, err
	}
	var reason *leapmuxv1.AgentInputQueuePauseReason
	var owner *leapmuxv1.AgentInputQueuePauseOwner
	if err := tx.QueryRowContext(ctx, `SELECT pause_reason, pause_owner FROM agent_input_queue_state WHERE agent_id = ?`, agentID).Scan(&reason, &owner); err != nil {
		return Snapshot{}, inputRecoveryPause{}, err
	}
	previous := inputRecoveryPause{reason: workerdb.StorageEnumValue(reason), owner: pauseOwner(workerdb.StorageEnumValue(owner))}
	if previous.owner == pauseOwnerPlannedRestart {
		previous.owner = pauseOwnerAgentStopped
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_input_queue_state
SET pause_reason = CASE WHEN pause_reason IS NULL OR (pause_owner IN (?, ?) AND pause_reason IN (?, ?)) THEN ? ELSE pause_reason END,
    pause_owner = CASE WHEN pause_reason IS NULL OR (pause_owner IN (?, ?) AND pause_reason IN (?, ?)) THEN ? ELSE pause_owner END,
    revision = revision + 1, updated_at = ?
WHERE agent_id = ?`,
		pauseOwnerAgentStopped, pauseOwnerPlannedRestart,
		leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_PROCESS_RECOVERY,
		leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_AGENT_STOPPED,
		leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_PROCESS_RECOVERY,
		pauseOwnerAgentStopped, pauseOwnerPlannedRestart,
		leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_PROCESS_RECOVERY,
		leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_AGENT_STOPPED,
		pauseOwnerPlannedRestart,
		nowText(), agentID); err != nil {
		return Snapshot{}, inputRecoveryPause{}, err
	}
	staged.restarting = true
	staged.bypassBackgroundWorkInputID = ""
	snapshot, err := s.commitSnapshot(ctx, tx, agentID, s.now(), staged)
	return snapshot, previous, err
}

// finishInputRecovery releases only the pause this recovery still owns. The
// old exit already ended its turn; preserve the replacement's published turn.
// An unspecified failure reason means the replacement prepared successfully.
func (s *Store) finishInputRecovery(ctx context.Context, agentID string, previous inputRecoveryPause, oldProcessEnded bool, failureReason leapmuxv1.AgentInputQueuePauseReason) (Snapshot, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Snapshot{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	staged := s.cloneEphemeral(agentID)
	if err := ensureState(ctx, tx, agentID); err != nil {
		return Snapshot{}, false, err
	}
	reason, owner := previous.reason, previous.owner
	if oldProcessEnded {
		reason = failureReason
		owner = pauseOwnerAgentStopped
		if failureReason == leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_UNSPECIFIED {
			owner = pauseOwnerNone
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_input_queue_state
SET pause_reason = CASE WHEN pause_owner = ? THEN ? ELSE pause_reason END,
    pause_owner = CASE WHEN pause_owner = ? THEN ? ELSE pause_owner END,
    revision = revision + 1, updated_at = ?
WHERE agent_id = ?`,
		pauseOwnerPlannedRestart, workerdb.OptionalStorageEnum(reason),
		pauseOwnerPlannedRestart, workerdb.OptionalStorageEnum(leapmuxv1.AgentInputQueuePauseOwner(owner)),
		nowText(), agentID); err != nil {
		return Snapshot{}, false, err
	}
	if oldProcessEnded && failureReason == leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_UNSPECIFIED {
		if _, err := tx.ExecContext(ctx, `UPDATE agent_input_queue_items SET error = '', updated_at = ? WHERE agent_id = ? AND state = ? AND error <> ''`,
			nowText(), agentID, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED); err != nil {
			return Snapshot{}, false, err
		}
	}
	staged.restarting = false
	staged.bypassBackgroundWorkInputID = ""
	snapshot, err := s.commitSnapshot(ctx, tx, agentID, s.now(), staged)
	return snapshot, true, err
}

func (s *Store) finishPlannedRestart(ctx context.Context, agentID string, oldTurnEnded, resumeQueue bool) (Snapshot, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Snapshot{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	staged := s.cloneEphemeral(agentID)
	if err := ensureState(ctx, tx, agentID); err != nil {
		return Snapshot{}, false, err
	}
	var active bool
	var storedReason *leapmuxv1.AgentInputQueuePauseReason
	var storedOwner *leapmuxv1.AgentInputQueuePauseOwner
	if err := tx.QueryRowContext(ctx, `
		SELECT pause_reason, pause_owner, active_turn
		FROM agent_input_queue_state WHERE agent_id = ?`, agentID).Scan(&storedReason, &storedOwner, &active); err != nil {
		return Snapshot{}, false, err
	}
	// The queue is paused exactly when it records a pause reason.
	paused := storedReason != nil
	reason := workerdb.StorageEnumValue(storedReason)
	owner := pauseOwner(workerdb.StorageEnumValue(storedOwner))
	nextReason := reason
	nextOwner := owner
	// Resume only the pause that this restart created. A crash, an archive, or
	// the user takes pause_owner over, so their pause survives the restart.
	if resumeQueue && paused && owner == pauseOwnerPlannedRestart {
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
	staged.restarting = false
	staged.bypassBackgroundWorkInputID = ""
	if !nextActive {
		staged.activeTurnSteerable = false
		staged.activeInputID = ""
	}
	changed := nextReason != reason || nextOwner != owner || nextActive != active
	if changed {
		if _, err := tx.ExecContext(ctx, `
			UPDATE agent_input_queue_state
			SET pause_reason = ?, pause_owner = ?, active_turn = ?,
				revision = revision + 1, updated_at = ?
			WHERE agent_id = ?`,
			workerdb.OptionalStorageEnum(nextReason), workerdb.OptionalStorageEnum(leapmuxv1.AgentInputQueuePauseOwner(nextOwner)), nextActive, nowText(), agentID); err != nil {
			return Snapshot{}, false, err
		}
	}
	snapshot, err := s.commitSnapshot(ctx, tx, agentID, s.now(), staged)
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

// PrepareBypassSteer reserves inputID for an ordinary dispatch triggered by
// Steer when no turn runs and active background work holds automatic dispatch.
func (s *Store) PrepareBypassSteer(ctx context.Context, agentID, inputID string) (*PreparedDispatch, Snapshot, error) {
	return s.prepareBypass(ctx, agentID, inputID, dispatchBypassSteer)
}

// PrepareBypassPreempt reserves inputID for an ordinary dispatch triggered by
// Preempt when no turn runs and active background work holds automatic dispatch.
func (s *Store) PrepareBypassPreempt(ctx context.Context, agentID, inputID string) (*PreparedDispatch, Snapshot, error) {
	return s.prepareBypass(ctx, agentID, inputID, dispatchBypassPreempt)
}

func (s *Store) prepareBypass(ctx context.Context, agentID, inputID string, mode dispatchMode) (*PreparedDispatch, Snapshot, error) {
	if !validateIdentity(inputID) {
		return nil, Snapshot{}, ErrNotHead
	}
	return s.prepare(ctx, agentID, inputID, mode, false)
}

// RecordPreemptBypass records that Preempt interrupted an active turn for
// inputID so the turn-end drain dispatches it even if background work continues.
func (s *Store) RecordPreemptBypass(ctx context.Context, agentID, inputID string) (Snapshot, error) {
	if !validateIdentity(inputID) {
		return Snapshot{}, ErrNotHead
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Snapshot{}, err
	}
	defer func() { _ = tx.Rollback() }()
	staged := s.cloneEphemeral(agentID)
	if err := ensureState(ctx, tx, agentID); err != nil {
		return Snapshot{}, err
	}
	now := s.now()
	gate, item, _, err := readDispatchGate(ctx, tx, agentID, now, &staged)
	if err != nil {
		return Snapshot{}, err
	}
	if !gate.HeadFound || item.ID != inputID {
		return Snapshot{}, ErrNotHead
	}
	if !gate.ActiveTurn {
		return Snapshot{}, ErrPreemptionState
	}
	staged.bypassBackgroundWorkInputID = inputID
	if err := bumpRevision(ctx, tx, agentID); err != nil {
		return Snapshot{}, err
	}
	return s.commitSnapshot(ctx, tx, agentID, now, staged)
}

// ClearPreemptBypass clears a recorded preemption bypass when its interrupt
// failed before ending the turn.
func (s *Store) ClearPreemptBypass(ctx context.Context, agentID, inputID string) (Snapshot, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Snapshot{}, err
	}
	defer func() { _ = tx.Rollback() }()
	staged := s.cloneEphemeral(agentID)
	if err := ensureState(ctx, tx, agentID); err != nil {
		return Snapshot{}, err
	}
	if inputID != "" && staged.bypassBackgroundWorkInputID == inputID {
		staged.bypassBackgroundWorkInputID = ""
		if err := bumpRevision(ctx, tx, agentID); err != nil {
			return Snapshot{}, err
		}
	}
	return s.commitSnapshot(ctx, tx, agentID, s.now(), staged)
}

// RefreshForBackgroundWork bumps the queue revision when the agent has queued
// items so watchers receive updated CanSteer/CanPreempt flags and the manager
// drains if background work finished. An empty queue does not write to SQLite.
func (s *Store) RefreshForBackgroundWork(ctx context.Context, agentID string) (Snapshot, bool, error) {
	if !validateIdentity(agentID) {
		return Snapshot{}, false, fmt.Errorf("%w: agent ID is required", ErrInvalidInput)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Snapshot{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	staged := s.cloneEphemeral(agentID)
	var hasItems bool
	if err := tx.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM agent_input_queue_items WHERE agent_id = ?)`, agentID).Scan(&hasItems); err != nil {
		return Snapshot{}, false, err
	}
	now := s.now()
	if !hasItems {
		snapshot, err := snapshotTx(ctx, tx, agentID, now, &staged)
		return snapshot, false, err
	}
	if err := bumpRevision(ctx, tx, agentID); err != nil {
		return Snapshot{}, false, err
	}
	snapshot, err := s.commitSnapshot(ctx, tx, agentID, now, staged)
	return snapshot, err == nil, err
}

// ListQueuedAgentIDsInTree returns distinct agent IDs in rootAgentID's agent
// tree that currently have at least one queued item.
func (s *Store) ListQueuedAgentIDsInTree(ctx context.Context, rootAgentID string) ([]string, error) {
	if !validateIdentity(rootAgentID) {
		return nil, fmt.Errorf("%w: agent ID is required", ErrInvalidInput)
	}
	rows, err := s.db.QueryContext(ctx, `
		WITH RECURSIVE tree(agent_id) AS (
			SELECT ?
			UNION
			SELECT a.id FROM agents a JOIN tree t ON a.parent_agent_id = t.agent_id
		)
		SELECT DISTINCT t.agent_id
		FROM tree t
		JOIN agent_input_queue_items i ON i.agent_id = t.agent_id
		ORDER BY t.agent_id`, rootAgentID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var agentID string
		if err := rows.Scan(&agentID); err != nil {
			return nil, err
		}
		ids = append(ids, agentID)
	}
	return ids, rows.Err()
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
	PauseReason                 leapmuxv1.AgentInputQueuePauseReason
	Paused                      bool
	Restarting                  bool
	ActiveTurn                  bool
	TurnSteerable               bool
	ActiveBackgroundWork        bool
	BypassBackgroundWorkInputID string
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

// blockedByBackgroundWork reports whether active background work holds
// automatic dispatch for head. Context replacements, control replies, and the
// item carrying an explicit preemption bypass are exempt.
func blockedByBackgroundWork(activeBackgroundWork bool, bypassInputID string, head OrderingMetadata) bool {
	if !activeBackgroundWork {
		return false
	}
	if bypassInputID != "" && bypassInputID == head.ID {
		return false
	}
	return !head.requiresContextReplacement() && !controlReplyKind(head.Kind)
}

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
	blockedByTurn := g.ActiveTurn || blockedByBackgroundWork(g.ActiveBackgroundWork, g.BypassBackgroundWorkInputID, g.Head)
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

func readQueueOrdering(ctx context.Context, tx db.DBTX, agentID string, now time.Time, ephem *agentEphemeralState) (queueOrdering, error) {
	raw, err := readOrderingRows(ctx, tx, agentID, ephem)
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
func readDispatchGate(ctx context.Context, tx db.DBTX, agentID string, now time.Time, ephem *agentEphemeralState) (dispatchGate, StoredItem, queueOrdering, error) {
	ordering, err := readQueueOrdering(ctx, tx, agentID, now, ephem)
	if err != nil {
		return dispatchGate{}, StoredItem{}, queueOrdering{}, err
	}
	head, found, err := headItemFromOrdering(ctx, tx, agentID, ordering, ephem)
	if err != nil {
		return dispatchGate{}, StoredItem{}, queueOrdering{}, err
	}
	gate, err := readDispatchGateFor(ctx, tx, agentID, now, head.OrderingMetadata, found, ephem)
	return gate, head, ordering, err
}

// hasActiveBackgroundWork reports whether any working (PENDING or RUNNING)
// background task holds automatic dispatch for agentID. For a root agent, any
// working row under owner_agent_id holds; for a child agent, only strict
// descendants hold so the child's own canonical activity row and unrelated root
// or sibling rows never block it.
func hasActiveBackgroundWork(ctx context.Context, tx db.DBTX, agentID string, isChild bool) (bool, error) {
	var active bool
	var err error
	if isChild {
		err = tx.QueryRowContext(ctx, `
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
			agentID, bgtask.StatusPending, bgtask.StatusRunning).Scan(&active)
	} else {
		err = tx.QueryRowContext(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM agent_background_tasks
				WHERE owner_agent_id = ? AND status IN (?, ?)
			)`,
			agentID, bgtask.StatusPending, bgtask.StatusRunning).Scan(&active)
	}
	return active, err
}

func readActiveBackgroundWork(ctx context.Context, tx db.DBTX, agentID string) (bool, error) {
	var isChild bool
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE((SELECT parent_agent_id IS NOT NULL FROM agents WHERE id = ?), 0)`,
		agentID).Scan(&isChild); err != nil {
		return false, err
	}
	return hasActiveBackgroundWork(ctx, tx, agentID, isChild)
}

// readDispatchGateFor reads the queue state and the approval rows of a head
// that the caller already selected.
func readDispatchGateFor(ctx context.Context, tx db.DBTX, agentID string, now time.Time, head OrderingMetadata, headFound bool, ephem *agentEphemeralState) (dispatchGate, error) {
	gate := dispatchGate{Now: now, ApprovalsComplete: true}
	var isChild bool
	if err := tx.QueryRowContext(ctx, `SELECT pause_reason IS NOT NULL, active_turn,
		(SELECT COUNT(*) FROM agent_input_queue_items WHERE agent_id = ? AND state = ?),
		COALESCE((SELECT closed_at IS NOT NULL FROM agents WHERE id = ?), 1),
		COALESCE((SELECT parent_agent_id IS NOT NULL FROM agents WHERE id = ?), 0),
		COALESCE(pause_reason, 0)
		FROM agent_input_queue_state WHERE agent_id = ?`, agentID, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_AWAITING_ECHO, agentID, agentID, agentID).
		Scan(&gate.Paused, &gate.ActiveTurn, &gate.AwaitingEcho, &gate.Closed, &isChild, &gate.PauseReason); err != nil {
		return dispatchGate{}, err
	}
	if ephem != nil {
		gate.Restarting = ephem.restarting
		gate.TurnSteerable = ephem.activeTurnSteerable
		gate.BypassBackgroundWorkInputID = ephem.bypassBackgroundWorkInputID
	}
	activeBackgroundWork, err := hasActiveBackgroundWork(ctx, tx, agentID, isChild)
	if err != nil {
		return dispatchGate{}, err
	}
	gate.ActiveBackgroundWork = activeBackgroundWork
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
	dispatchBypassSteer
	dispatchBypassPreempt
)

func (s *Store) prepare(ctx context.Context, agentID, expectedInputID string, mode dispatchMode, rowAtEcho bool) (*PreparedDispatch, Snapshot, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, Snapshot{}, err
	}
	defer func() { _ = tx.Rollback() }()
	staged := s.cloneEphemeral(agentID)
	if err := ensureState(ctx, tx, agentID); err != nil {
		return nil, Snapshot{}, err
	}
	now := s.now()
	gate, item, ordering, err := readDispatchGate(ctx, tx, agentID, now, &staged)
	if err != nil {
		return nil, Snapshot{}, err
	}
	// Automatic question delivery uses the current transaction's pause and turn state.
	requireActive := mode == dispatchSteering || mode == dispatchAutomatic && gate.ActiveTurn && gate.TurnSteerable &&
		automaticSteeringKind(item.Kind)
	var allowPaused bool
	switch mode {
	case dispatchAutomatic:
		allowPaused = requireActive && gate.PauseReason == leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_PROCESS_RECOVERY
	case dispatchRetry, dispatchSteering, dispatchBypassSteer:
		allowPaused = true
	case dispatchBypassPreempt:
		allowPaused = false
	}
	if mode == dispatchRetry || mode == dispatchBypassSteer || mode == dispatchBypassPreempt {
		gate.ActiveBackgroundWork = false
	}
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
		// A steer or background-work bypass is the user's request, so it states
		// why it cannot proceed.
		switch mode {
		case dispatchSteering:
			return nil, Snapshot{}, steeringRefusal(refusal, gate.ActiveTurn)
		case dispatchBypassSteer:
			if refusal == dispatchRefusedScheduled {
				return nil, Snapshot{}, ErrScheduled
			}
			return nil, Snapshot{}, ErrSteeringState
		case dispatchBypassPreempt:
			if refusal == dispatchRefusedScheduled {
				return nil, Snapshot{}, ErrScheduled
			}
			return nil, Snapshot{}, ErrPreemptionState
		case dispatchAutomatic, dispatchRetry:
			snapshot, err := s.commitSnapshot(ctx, tx, agentID, now, staged)
			return nil, snapshot, err
		}
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
		if reservedSeq, err = reserveMessageSeq(ctx, tx, agentID); err != nil {
			return nil, Snapshot{}, err
		}
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE agent_input_queue_items
		SET state = ?, error = '', updated_at = ?
		WHERE agent_id = ? AND id = ? AND state = ?`,
		leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DISPATCHING, nowText(), agentID, item.ID,
		leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED)
	if err != nil {
		return nil, Snapshot{}, err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return nil, Snapshot{}, ErrConflict
	}
	staged.setReservedSeq(item.ID, reservedSeq)
	if err := placeDispatchedItem(ctx, tx, agentID, item.ID, ordering); err != nil {
		return nil, Snapshot{}, err
	}
	stateUpdate := `UPDATE agent_input_queue_state SET revision = revision + 1, updated_at = ? WHERE agent_id = ?`
	if !requireActive {
		stateUpdate = `UPDATE agent_input_queue_state SET active_turn = 1, revision = revision + 1, updated_at = ? WHERE agent_id = ?`
		staged.activeTurnSteerable = false
		staged.activeInputID = item.ID
	}
	if _, err := tx.ExecContext(ctx, stateUpdate, nowText(), agentID); err != nil {
		return nil, Snapshot{}, err
	}
	item.State = leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DISPATCHING
	item.ReservedSeq = reservedSeq
	snapshot, err := snapshotTx(ctx, tx, agentID, now, &staged)
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
	if err := s.commitEphemeral(tx, agentID, staged); err != nil {
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
	return s.requeuePrepared(ctx, agentID, inputID, requeuePreserveTurn, false)
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
	return s.requeuePrepared(ctx, agentID, inputID, requeueNativeTurn, activeTurnSteerable)
}

type requeueTurnDisposition uint8

const (
	requeuePreserveTurn requeueTurnDisposition = iota
	requeueNativeTurn
	requeuePendingInput
)

// RequeuePending waits for native admission without inventing a running turn.
// The item and its provisional turn claim change in the same transaction.
func (s *Store) RequeuePending(ctx context.Context, agentID, inputID string) (Snapshot, error) {
	return s.requeuePrepared(ctx, agentID, inputID, requeuePendingInput, false)
}

func (s *Store) requeuePrepared(ctx context.Context, agentID, inputID string, disposition requeueTurnDisposition, activeTurnSteerable bool) (Snapshot, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Snapshot{}, err
	}
	defer func() { _ = tx.Rollback() }()
	staged := s.cloneEphemeral(agentID)
	if _, err := absorbQueuedAutoRetry(ctx, tx, agentID, inputID, &staged); err != nil {
		return Snapshot{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE agent_input_queue_items SET state = ?, error = '', updated_at = ? WHERE agent_id = ? AND id = ? AND state = ?`,
		leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED, nowText(), agentID, inputID,
		leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DISPATCHING)
	if err != nil {
		return Snapshot{}, err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return Snapshot{}, ErrConflict
	}
	staged.setReservedSeq(inputID, 0)
	switch disposition {
	case requeueNativeTurn:
		if staged.activeInputID == inputID {
			staged.activeTurnSteerable = activeTurnSteerable
			staged.activeInputID = ""
		}
		staged.bypassBackgroundWorkInputID = ""
		if err := bumpRevision(ctx, tx, agentID); err != nil {
			return Snapshot{}, err
		}
	case requeuePendingInput:
		if staged.activeInputID == inputID {
			staged.activeTurnSteerable = false
			staged.activeInputID = ""
			if _, err := tx.ExecContext(ctx, `UPDATE agent_input_queue_state SET active_turn = 0, revision = revision + 1, updated_at = ? WHERE agent_id = ?`, nowText(), agentID); err != nil {
				return Snapshot{}, err
			}
		} else if err := bumpRevision(ctx, tx, agentID); err != nil {
			return Snapshot{}, err
		}
	default:
		if err := bumpRevision(ctx, tx, agentID); err != nil {
			return Snapshot{}, err
		}
	}
	return s.commitSnapshot(ctx, tx, agentID, s.now(), staged)
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
	staged := s.cloneEphemeral(agentID)
	if err := ensureState(ctx, tx, agentID); err != nil {
		return Snapshot{}, err
	}
	if _, err := absorbQueuedAutoRetry(ctx, tx, agentID, inputID, &staged); err != nil {
		return Snapshot{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE agent_input_queue_items SET state = ?, error = ?, updated_at = ? WHERE agent_id = ? AND id = ? AND state = ?`,
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
	staged.setReservedSeq(inputID, 0)
	ownsTurn := staged.activeInputID == inputID
	if ownsTurn {
		staged.activeTurnSteerable = false
		staged.activeInputID = ""
	}
	staged.bypassBackgroundWorkInputID = ""
	// Release this dispatch's provisional turn, but keep any turn the provider
	// published independently. A refusal of input does not end native work.
	if _, err := tx.ExecContext(ctx, `UPDATE agent_input_queue_state
SET active_turn = CASE WHEN ? THEN 0 ELSE active_turn END,
    pause_reason = CASE WHEN pause_reason IS NOT NULL THEN pause_reason ELSE ? END,
    pause_owner = CASE WHEN pause_reason IS NULL OR pause_owner = ? THEN ? ELSE pause_owner END,
    revision = revision + 1, updated_at = ?
WHERE agent_id = ?`, ownsTurn, reason, pauseOwnerPlannedRestart, pauseOwnerAgentStopped, nowText(), agentID); err != nil {
		return Snapshot{}, err
	}
	return s.commitSnapshot(ctx, tx, agentID, s.now(), staged)
}

// RefreshSnapshot publishes metadata that changed outside the queue store.
func (s *Store) RefreshSnapshot(ctx context.Context, agentID string) (Snapshot, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Snapshot{}, err
	}
	defer func() { _ = tx.Rollback() }()
	staged := s.cloneEphemeral(agentID)
	if err := ensureState(ctx, tx, agentID); err != nil {
		return Snapshot{}, err
	}
	if err := bumpRevision(ctx, tx, agentID); err != nil {
		return Snapshot{}, err
	}
	return s.commitSnapshot(ctx, tx, agentID, s.now(), staged)
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
	staged := s.cloneEphemeral(prepared.Item.AgentID)
	item, attachments, found, err := getItemWithEphemeral(ctx, tx, prepared.Item.AgentID, prepared.Item.ID, true, &staged)
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
	staged.clearItem(item.ID)
	staged.bypassBackgroundWorkInputID = ""
	if result.StartsTurn && !result.Steering {
		// The turn is asserted only while the state still holds THIS dispatch.
		// PrepareDispatch opened the turn and recorded the input id before the
		// provider call, and the manager releases the coordinator lock across
		// that call -- so a turn that ends inside it clears both fields first.
		// A write of the turn here would restore one that no envelope ever ends
		// again, and the queue would then hold every later message for the life
		// of the process.
		if staged.activeInputID == item.ID {
			staged.activeTurnSteerable = result.TurnSteerable
			if _, err := tx.ExecContext(ctx, `UPDATE agent_input_queue_state SET active_turn = 1, revision = revision + 1, updated_at = ? WHERE agent_id = ?`, nowText(), item.AgentID); err != nil {
				return AcceptedTranscript{}, Snapshot{}, err
			}
		} else if err := bumpRevision(ctx, tx, item.AgentID); err != nil {
			return AcceptedTranscript{}, Snapshot{}, err
		}
	} else if result.Steering {
		if err := bumpRevision(ctx, tx, item.AgentID); err != nil {
			return AcceptedTranscript{}, Snapshot{}, err
		}
	} else if staged.activeInputID == item.ID {
		staged.activeTurnSteerable = false
		staged.activeInputID = ""
		if _, err := tx.ExecContext(ctx, `UPDATE agent_input_queue_state SET active_turn = 0, revision = revision + 1, updated_at = ? WHERE agent_id = ?`, nowText(), item.AgentID); err != nil {
			return AcceptedTranscript{}, Snapshot{}, err
		}
	} else if err := bumpRevision(ctx, tx, item.AgentID); err != nil {
		return AcceptedTranscript{}, Snapshot{}, err
	}
	snapshot, err := s.commitSnapshot(ctx, tx, item.AgentID, s.now(), staged)
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
		 parent_span_id, span_type, span_lines, span_color, mark_type, created_at,
		 supplemental_content, supplemental_content_compression)
		VALUES (?, ?, ?, ?, ?, ?, ?, 0, '', '', '', ?, 0, ?, ?, COALESCE(?, X''), ?)`,
		item.ID, item.AgentID, seq, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER,
		compressed, compression, inputIdempotencyKey(NewItem{
			ID: item.ID, AgentID: item.AgentID, Kind: item.Kind, Text: item.Text,
			TargetMode: item.TargetMode, PrepareContext: item.PrepareContext,
			ReclassifyOnEdit: item.ReclassifyOnEdit, ScheduledAt: item.ScheduledAt,
			SourcePayload: item.SourcePayload, Attachments: attachments,
		}), spanLines, workerdb.OptionalStorageEnum(mark), item.CreatedAt, supplementalContent, supplementalCompression); err != nil {
		return AcceptedTranscript{}, err
	}
	return AcceptedTranscript{
		ID: item.ID, AgentID: item.AgentID, Seq: seq,
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
	staged := s.cloneEphemeral(prepared.Item.AgentID)
	if staged.reservedSeqOf(prepared.Item.ID) != 0 {
		return Snapshot{}, ErrConflict
	}
	result, err := tx.ExecContext(ctx, `UPDATE agent_input_queue_items SET state = ?, updated_at = ?
		WHERE agent_id = ? AND id = ? AND state = ?`,
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
	return s.commitSnapshot(ctx, tx, prepared.Item.AgentID, s.now(), staged)
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
func pendingSteerIDs(ctx context.Context, tx db.DBTX, agentID string, count int, inFlightID string, ephem *agentEphemeralState) ([]string, bool, error) {
	ids, err := awaitingEchoIDs(ctx, tx, agentID, count)
	if err != nil {
		return nil, false, err
	}
	if (count >= 0 && len(ids) >= count) || inFlightID == "" {
		return ids, false, nil
	}
	item, _, found, err := getItemWithEphemeral(ctx, tx, agentID, inFlightID, false, ephem)
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
	staged := s.cloneEphemeral(agentID)
	if err := ensureState(ctx, tx, agentID); err != nil {
		return nil, false, Snapshot{}, err
	}
	if count <= 0 {
		snapshot, err := s.commitSnapshot(ctx, tx, agentID, s.now(), staged)
		return nil, false, snapshot, err
	}
	ids, acceptedInFlight, err := pendingSteerIDs(ctx, tx, agentID, count, inFlightID, &staged)
	if err != nil {
		return nil, false, Snapshot{}, err
	}
	var accepted []AcceptedTranscript
	for _, id := range ids {
		item, attachments, found, err := getItemWithEphemeral(ctx, tx, agentID, id, true, &staged)
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
		staged.clearItem(id)
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
	snapshot, err := s.commitSnapshot(ctx, tx, agentID, s.now(), staged)
	if err != nil {
		return nil, false, Snapshot{}, err
	}
	return accepted, acceptedInFlight, snapshot, nil
}

// requeueUnechoedSteers returns selected steers to the head behind the remaining pending echoes.
// The caller owns the transaction and its revision update. Two steers of one
// retry kind merge into one row (absorbQueuedAutoRetry), and the count
// includes both, because the provider dropped both.
func requeueUnechoedSteers(ctx context.Context, tx db.DBTX, agentID string, count int, staged *agentEphemeralState) (int, error) {
	ids, err := awaitingEchoIDs(ctx, tx, agentID, count)
	if err != nil || len(ids) == 0 {
		return 0, err
	}
	absorbed := make(map[string]bool)
	for _, id := range ids {
		peerID, err := absorbQueuedAutoRetry(ctx, tx, agentID, id, staged)
		if err != nil {
			return 0, err
		}
		if peerID != "" {
			absorbed[peerID] = true
		}
		if _, err := tx.ExecContext(ctx, `UPDATE agent_input_queue_items SET state = ?, error = '', updated_at = ? WHERE agent_id = ? AND id = ?`,
			leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED, nowText(), agentID, id); err != nil {
			return 0, err
		}
		staged.setReservedSeq(id, 0)
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
	staged := s.cloneEphemeral(agentID)
	if err := ensureState(ctx, tx, agentID); err != nil {
		return Snapshot{}, 0, err
	}
	requeued, err := requeueUnechoedSteers(ctx, tx, agentID, count, &staged)
	if err != nil {
		return Snapshot{}, 0, err
	}
	if requeued > 0 {
		if err := bumpRevision(ctx, tx, agentID); err != nil {
			return Snapshot{}, 0, err
		}
	}
	snapshot, err := s.commitSnapshot(ctx, tx, agentID, s.now(), staged)
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
	staged := s.cloneEphemeral(agentID)
	if err := ensureState(ctx, tx, agentID); err != nil {
		return Snapshot{}, 0, false, err
	}
	ids, inFlight, err := pendingSteerIDs(ctx, tx, agentID, count, inFlightID, &staged)
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
		staged.setReservedSeq(id, 0)
	}
	if len(ids) > 0 {
		if err := placeResolvedSteers(ctx, tx, agentID, ids); err != nil {
			return Snapshot{}, 0, false, err
		}
		paused, err := pauseAndEndTurn(ctx, tx, agentID, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_DELIVERY_UNCERTAIN, pauseOwnerDelivery, &staged)
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
	snapshot, err := s.commitSnapshot(ctx, tx, agentID, s.now(), staged)
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
	staged := s.cloneEphemeral(agentID)
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
	staged.setReservedSeq(inputID, 0)
	if err := replacePauseAndEndTurn(ctx, tx, agentID, reason, pauseOwnerDelivery, &staged); err != nil {
		return Snapshot{}, err
	}
	return s.commitSnapshot(ctx, tx, agentID, s.now(), staged)
}

// replacePauseAndEndTurn replaces any existing pause and clears its turn.
// The caller owns the transaction and any item-state validation.
func replacePauseAndEndTurn(ctx context.Context, tx *sql.Tx, agentID string, reason leapmuxv1.AgentInputQueuePauseReason, owner pauseOwner, staged *agentEphemeralState) error {
	if staged != nil {
		staged.activeTurnSteerable = false
		staged.activeInputID = ""
		staged.bypassBackgroundWorkInputID = ""
	}
	_, err := tx.ExecContext(ctx, `UPDATE agent_input_queue_state SET active_turn = 0, pause_reason = ?, pause_owner = ?, revision = revision + 1, updated_at = ? WHERE agent_id = ?`, reason, owner, nowText(), agentID)
	return err
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
	return s.execTurnState(ctx, agentID, false, func(staged *agentEphemeralState) (string, []any, bool) {
		if staged.activeInputID != "" {
			return "", nil, true
		}
		ephemChanged := staged.activeTurnSteerable
		staged.activeTurnSteerable = false
		return `UPDATE agent_input_queue_state SET active_turn = 0, revision = revision + 1, updated_at = ? WHERE agent_id = ? AND (active_turn = 1 OR ?)`,
			[]any{nowText(), agentID, ephemChanged}, false
	})
}

// setTurnState records the provider's turn state and clears provisional dispatch ownership.
// Accept then keeps this report instead of replacing it with the dispatch result.
// After ownership clears, a repeated report leaves the revision unchanged.
func (s *Store) setTurnState(ctx context.Context, agentID string, active, steerable, requeueUnechoed bool) (Snapshot, bool, error) {
	return s.execTurnState(ctx, agentID, !active && requeueUnechoed, func(staged *agentEphemeralState) (string, []any, bool) {
		if active {
			ephemChanged := staged.activeTurnSteerable != steerable || staged.activeInputID != ""
			staged.activeTurnSteerable = steerable
			staged.activeInputID = ""
			return `UPDATE agent_input_queue_state SET active_turn = 1, revision = revision + 1, updated_at = ? WHERE agent_id = ? AND (active_turn = 0 OR ?)`,
				[]any{nowText(), agentID, ephemChanged}, false
		}
		ephemChanged := staged.activeTurnSteerable || staged.activeInputID != ""
		staged.activeTurnSteerable = false
		staged.activeInputID = ""
		return `UPDATE agent_input_queue_state SET active_turn = 0, revision = revision + 1, updated_at = ? WHERE agent_id = ? AND (active_turn = 1 OR ?)`,
			[]any{nowText(), agentID, ephemChanged}, false
	})
}

// execTurnState runs one guarded write against the turn columns and reports
// whether it moved a row. Every turn write shares this shape: the state row must
// exist, the guard decides whether anything changes, and the caller needs the
// committed snapshot either way.
func (s *Store) execTurnState(ctx context.Context, agentID string, requeueUnechoed bool, prepare func(*agentEphemeralState) (string, []any, bool)) (Snapshot, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Snapshot{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	staged := s.cloneEphemeral(agentID)
	if err := ensureState(ctx, tx, agentID); err != nil {
		return Snapshot{}, false, err
	}
	var rows int64
	statement, args, skip := prepare(&staged)
	if !skip {
		result, err := tx.ExecContext(ctx, statement, args...)
		if err != nil {
			return Snapshot{}, false, err
		}
		rows, err = result.RowsAffected()
		if err != nil {
			return Snapshot{}, false, err
		}
	}
	if requeueUnechoed {
		count, err := requeueUnechoedSteers(ctx, tx, agentID, -1, &staged)
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
	snapshot, err := s.commitSnapshot(ctx, tx, agentID, s.now(), staged)
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
	staged := s.cloneEphemeral(agentID)
	if err := ensureState(ctx, tx, agentID); err != nil {
		return Snapshot{}, err
	}
	if _, err := pauseAndEndTurn(ctx, tx, agentID, reason, pauseOwnerAgentStopped, &staged); err != nil {
		return Snapshot{}, err
	}
	return s.commitSnapshot(ctx, tx, agentID, s.now(), staged)
}

// pauseAndEndTurn pauses the queue and ends the active turn in one statement.
// It keeps an earlier pause's reason and owner, except when a planned restart
// owns that pause. A later failure then supplies its own reason and owner.
//
// One case takes ownership from an existing pause: a pause that a planned
// restart owns. That pause is temporary by definition -- the restart lifts it
// when the new process runs -- so a crash or an archive that arrives during
// the restart must claim it, or the restart resumes a queue that a real
// failure had stopped.
//
// The result reports whether the row changed.
func pauseAndEndTurn(ctx context.Context, tx *sql.Tx, agentID string, reason leapmuxv1.AgentInputQueuePauseReason, owner pauseOwner, staged *agentEphemeralState) (sql.Result, error) {
	hadBypass := false
	if staged != nil {
		hadBypass = staged.bypassBackgroundWorkInputID != ""
		staged.activeTurnSteerable = false
		staged.activeInputID = ""
		staged.bypassBackgroundWorkInputID = ""
	}
	return tx.ExecContext(ctx, `
		UPDATE agent_input_queue_state
		SET active_turn = 0,
			pause_reason = CASE WHEN pause_reason IS NULL OR pause_owner = ? THEN ? ELSE pause_reason END,
			pause_owner = CASE WHEN pause_reason IS NULL OR pause_owner = ? THEN ? ELSE pause_owner END,
			revision = revision + 1, updated_at = ?
		WHERE agent_id = ? AND (active_turn = 1 OR pause_reason IS NULL OR pause_owner = ? OR ?)`,
		pauseOwnerPlannedRestart, reason, pauseOwnerPlannedRestart, owner, nowText(), agentID, pauseOwnerPlannedRestart, hadBypass)
}

// resumeOwnedPause lifts a pause only when the given cause still owns it.
// The result reports whether the row changed.
func resumeOwnedPause(ctx context.Context, tx *sql.Tx, agentID string, owner pauseOwner) (sql.Result, error) {
	return tx.ExecContext(ctx, `
		UPDATE agent_input_queue_state
		SET pause_reason = NULL, pause_owner = NULL,
			revision = revision + 1, updated_at = ?
		WHERE agent_id = ? AND pause_owner = ?`, nowText(), agentID, owner)
}

// ResumeAfterArchive resumes only the pause that PauseForArchive created. The
// Boolean reports whether the durable state changed.
func (s *Store) ResumeAfterArchive(ctx context.Context, agentID string) (Snapshot, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Snapshot{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	staged := s.cloneEphemeral(agentID)
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
	snapshot, err := s.commitSnapshot(ctx, tx, agentID, s.now(), staged)
	return snapshot, changed == 1, err
}

func (s *Store) Retry(ctx context.Context, agentID, inputID string, confirmUncertain bool) (Snapshot, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Snapshot{}, err
	}
	defer func() { _ = tx.Rollback() }()
	staged := s.cloneEphemeral(agentID)
	now := s.now()
	item, found, err := headItem(ctx, tx, agentID, now, &staged)
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
	if staged.restarting {
		return Snapshot{}, ErrPlannedRestart
	}
	// The user's Retry of a failed automatic retry returns it to QUEUED,
	// beside the newer retry of its kind that a report may have queued.
	if err := dropQueuedAutoRetry(ctx, tx, agentID, inputID, item.Kind, &staged); err != nil {
		return Snapshot{}, err
	}
	// The item returns to QUEUED, so it releases its reservation. A stale
	// reservation makes the retried message sort before an agent message that
	// the transcript took after the failure.
	if _, err := tx.ExecContext(ctx, `UPDATE agent_input_queue_items SET state = ?, error = '', updated_at = ? WHERE agent_id = ? AND id = ?`, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED, nowText(), agentID, inputID); err != nil {
		return Snapshot{}, err
	}
	staged.setReservedSeq(inputID, 0)
	staged.activeTurnSteerable = false
	staged.activeInputID = ""
	staged.bypassBackgroundWorkInputID = ""
	if _, err := tx.ExecContext(ctx, `UPDATE agent_input_queue_state SET active_turn = 0, revision = revision + 1, updated_at = ? WHERE agent_id = ?`, nowText(), agentID); err != nil {
		return Snapshot{}, err
	}
	// Lift the pause that the failure created, so the items behind the retried
	// head dispatch again. A pause that the user or an archive created keeps
	// its own owner and survives.
	if _, err := resumeOwnedPause(ctx, tx, agentID, pauseOwnerDelivery); err != nil {
		return Snapshot{}, err
	}
	return s.commitSnapshot(ctx, tx, agentID, now, staged)
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
	staged := s.cloneEphemeral(agentID)
	now := s.now()
	items, err := readOrderingRows(ctx, tx, agentID, &staged)
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
	snapshot, err := s.commitSnapshot(ctx, tx, agentID, now, staged)
	return snapshot, due, err
}

// scheduleAutoRetry creates or updates the one pending queue item of one
// automatic-retry kind, atomically with the server-error backoff state. A new
// retry cycle takes a fresh ID; rescheduling the pending item of the kind
// retains its ID and its raw queue position. Validation, queue-full, and
// database failures leave the previous durable item untouched, because the
// transaction rolls back.
func (s *Store) scheduleAutoRetry(ctx context.Context, agentID string, schedule agent.AutoRetrySchedule) (Snapshot, error) {
	if !validateIdentity(agentID) {
		return Snapshot{}, fmt.Errorf("%w: agent ID is required", ErrInvalidInput)
	}
	if !IsAutoRetryKind(schedule.Kind) {
		return Snapshot{}, fmt.Errorf("%w: not an automatic-retry kind", ErrInvalidInput)
	}
	if err := validateSchedule(schedule.Kind, schedule.SourcePayload, nil); err != nil {
		return Snapshot{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Snapshot{}, err
	}
	defer func() { _ = tx.Rollback() }()
	staged := s.cloneEphemeral(agentID)
	now := s.now()
	// A closed or archived agent never gains a retry: late provider output
	// must not resurrect a schedule that lifecycle cleanup removed. The check
	// precedes ensureState, whose state row carries a foreign key to agents.
	var open int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agents WHERE id = ? AND closed_at IS NULL AND workspace_archived = 0`, agentID).Scan(&open); err != nil {
		return Snapshot{}, err
	}
	if open == 0 {
		return Snapshot{}, fmt.Errorf("%w: agent is closed or archived", ErrConflict)
	}
	if err := ensureState(ctx, tx, agentID); err != nil {
		return Snapshot{}, err
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
			return Snapshot{}, err
		}
		var nextDelay time.Duration
		dueAt, nextDelay = serverErrorDeadline(delay, now)
		if err := writeServerErrorDelay(ctx, tx, agentID, nextDelay); err != nil {
			return Snapshot{}, err
		}
	}

	existingID, err := queuedAutoRetryID(ctx, tx, agentID, schedule.Kind)
	if err != nil {
		return Snapshot{}, err
	}
	if existingID == "" {
		if err := insertQueuedItem(ctx, tx, NewItem{
			ID: id.Generate(), AgentID: agentID, Kind: schedule.Kind, Text: retryText,
			ScheduledAt: &dueAt, SourcePayload: schedule.SourcePayload,
		}); err != nil {
			return Snapshot{}, err
		}
	} else if _, err := tx.ExecContext(ctx, `
		UPDATE agent_input_queue_items
		SET scheduled_at = ?, source_payload = ?, updated_at = ? WHERE agent_id = ? AND id = ?`,
		sqltime.SQLiteNullTimeOf(dueAt), ptrconv.OrEmpty(schedule.SourcePayload), nowText(), agentID, existingID); err != nil {
		return Snapshot{}, err
	}
	if err := bumpRevision(ctx, tx, agentID); err != nil {
		return Snapshot{}, err
	}
	return s.commitSnapshot(ctx, tx, agentID, now, staged)
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
// snapshot with false. The Boolean reports whether a row left the queue; the
// backoff is not part of the snapshot.
func (s *Store) cancelAutoRetries(ctx context.Context, agentID string, kinds ...leapmuxv1.AgentInputKind) (Snapshot, bool, error) {
	if len(kinds) == 0 {
		return Snapshot{}, false, nil
	}
	for _, kind := range kinds {
		if !IsAutoRetryKind(kind) {
			return Snapshot{}, false, fmt.Errorf("%w: not an automatic-retry kind", ErrInvalidInput)
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
		return Snapshot{}, false, err
	}
	if !work {
		return Snapshot{}, false, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Snapshot{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	staged := s.cloneEphemeral(agentID)
	rows, err := tx.QueryContext(ctx, `DELETE FROM agent_input_queue_items WHERE agent_id = ? AND state = ? AND kind IN (`+kindList+`) RETURNING id`,
		append([]any{agentID, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED}, kindArgs...)...)
	if err != nil {
		return Snapshot{}, false, err
	}
	var deletedIDs []string
	for rows.Next() {
		var deletedID string
		if err := rows.Scan(&deletedID); err != nil {
			_ = rows.Close()
			return Snapshot{}, false, err
		}
		deletedIDs = append(deletedIDs, deletedID)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return Snapshot{}, false, err
	}
	for _, deletedID := range deletedIDs {
		staged.clearItem(deletedID)
	}
	if resetBackoff {
		if _, err := resetServerErrorDelay(ctx, tx, agentID); err != nil {
			return Snapshot{}, false, err
		}
	}
	if len(deletedIDs) > 0 {
		if err := compactOrder(ctx, tx, agentID); err != nil {
			return Snapshot{}, false, err
		}
		if err := bumpRevision(ctx, tx, agentID); err != nil {
			return Snapshot{}, false, err
		}
	}
	snapshot, err := s.commitSnapshot(ctx, tx, agentID, s.now(), staged)
	return snapshot, len(deletedIDs) > 0, err
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

// queuedAutoRetryID returns the ID of the pending (QUEUED) retry of one kind,
// or "". idx_agent_input_queue_one_auto_retry_kind admits at most one.
func queuedAutoRetryID(ctx context.Context, tx db.DBTX, agentID string, kind leapmuxv1.AgentInputKind) (string, error) {
	var id string
	err := tx.QueryRowContext(ctx, `SELECT id FROM agent_input_queue_items WHERE agent_id = ? AND kind = ? AND state = ?`,
		agentID, kind, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
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
func absorbQueuedAutoRetry(ctx context.Context, tx db.DBTX, agentID, inputID string, staged *agentEphemeralState) (string, error) {
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
	if err := deleteQueuedAutoRetryPeer(ctx, tx, agentID, peerID, staged); err != nil {
		return "", err
	}
	return peerID, nil
}

// dropQueuedAutoRetry removes the newer pending retry of kind beside inputID,
// which the user returns to QUEUED. kind is the kind that the returning row
// takes, which an edit can reclassify. The caller already validated the item,
// and an ordinary input needs no additional read.
func dropQueuedAutoRetry(ctx context.Context, tx db.DBTX, agentID, inputID string, kind leapmuxv1.AgentInputKind, staged *agentEphemeralState) error {
	if !IsAutoRetryKind(kind) {
		return nil
	}
	peerID, err := queuedAutoRetryID(ctx, tx, agentID, kind)
	if err != nil || peerID == "" || peerID == inputID {
		return err
	}
	return deleteQueuedAutoRetryPeer(ctx, tx, agentID, peerID, staged)
}

func deleteQueuedAutoRetryPeer(ctx context.Context, tx db.DBTX, agentID, peerID string, staged *agentEphemeralState) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM agent_input_queue_items WHERE agent_id = ? AND id = ?`, agentID, peerID); err != nil {
		return err
	}
	if staged != nil {
		staged.clearItem(peerID)
	}
	return compactOrder(ctx, tx, agentID)
}

// ListQueuedAgentsWithActiveBackgroundWork returns open agent IDs that hold
// queued input and currently have active background work. RestoreState calls it
// before sweeping crashed background tasks so Recover pauses those queues as
// interrupted even after the sweep marks their tasks finished.
func (s *Store) ListQueuedAgentsWithActiveBackgroundWork(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT q.agent_id
		FROM agent_input_queue_state q
		JOIN agents a ON a.id = q.agent_id
		JOIN agent_input_queue_items i ON i.agent_id = q.agent_id
		WHERE a.closed_at IS NULL
		ORDER BY q.agent_id`)
	if err != nil {
		return nil, err
	}
	var candidates []string
	for rows.Next() {
		var agentID string
		if err := rows.Scan(&agentID); err != nil {
			_ = rows.Close()
			return nil, err
		}
		candidates = append(candidates, agentID)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	var affected []string
	for _, agentID := range candidates {
		active, err := readActiveBackgroundWork(ctx, s.db, agentID)
		if err != nil {
			return nil, err
		}
		if active {
			affected = append(affected, agentID)
		}
	}
	return affected, nil
}

func (s *Store) Recover(ctx context.Context, interruptedBackgroundAgents ...string) ([]Snapshot, error) {
	interruptedSet := make(map[string]struct{}, len(interruptedBackgroundAgents))
	for _, id := range interruptedBackgroundAgents {
		interruptedSet[id] = struct{}{}
	}
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
		// Recovery resets all in-memory ephemeral state for this agent: edit locks,
		// restarting markers, preemption bypasses, reserved sequences, and turn
		// metadata cannot survive a Worker restart.
		staged := agentEphemeralState{}
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
		now := s.now()
		recoveredItems, err := readOrderingRows(ctx, tx, agentID, &staged)
		if err != nil {
			_ = tx.Rollback()
			return nil, err
		}
		activeBackgroundWork := false
		if len(recoveredItems) > 0 {
			if _, ok := interruptedSet[agentID]; ok {
				activeBackgroundWork = true
			} else {
				activeBackgroundWork, err = readActiveBackgroundWork(ctx, tx, agentID)
				if err != nil {
					_ = tx.Rollback()
					return nil, err
				}
			}
		}
		reason := leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_INTERRUPTED
		if unconfirmed > 0 {
			if _, err := tx.ExecContext(ctx, `UPDATE agent_input_queue_items SET state = ?, error = 'Worker restarted before delivery was confirmed', updated_at = ? WHERE agent_id = ? AND state IN (?, ?)`, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DELIVERY_UNCERTAIN, nowText(), agentID, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DISPATCHING, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_AWAITING_ECHO); err != nil {
				_ = tx.Rollback()
				return nil, err
			}
			reason = leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_DELIVERY_UNCERTAIN
		}
		if unconfirmed > 0 || active || activeBackgroundWork {
			if err := replacePauseAndEndTurn(ctx, tx, agentID, reason, pauseOwnerRecovery, &staged); err != nil {
				_ = tx.Rollback()
				return nil, err
			}
		}
		// A deadline that passed while the Worker was down matured its item
		// with no timer to publish the change. One revision bump records it,
		// so an older equal-revision snapshot cannot overwrite the matured
		// order once the queue reaches its owner. The same instant decides
		// the bump and the snapshot, so no item matures between the two.
		if slices.ContainsFunc(recoveredItems, func(item OrderingMetadata) bool {
			return item.ScheduledAt != nil && !scheduledFuture(item, now)
		}) {
			if err := bumpRevision(ctx, tx, agentID); err != nil {
				_ = tx.Rollback()
				return nil, err
			}
		}
		snapshot, err := s.commitSnapshot(ctx, tx, agentID, now, staged)
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
		if _, err := tx.ExecContext(ctx, `INSERT INTO agent_input_queue_attachments (item_id, position, filename, mime_type, data) VALUES (?, ?, ?, ?, ?)`, itemID, i, attachments[i].Filename, attachments[i].MimeType, attachments[i].Data); err != nil {
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

func (s *Store) getItem(ctx context.Context, tx db.DBTX, agentID, inputID string, loadAttachments bool) (StoredItem, []Attachment, bool, error) {
	ephem := s.cloneEphemeral(agentID)
	return getItemWithEphemeral(ctx, tx, agentID, inputID, loadAttachments, &ephem)
}

func getItemWithEphemeral(ctx context.Context, tx db.DBTX, agentID, inputID string, loadAttachments bool, ephem *agentEphemeralState) (StoredItem, []Attachment, bool, error) {
	var item StoredItem
	var kind, state int32
	var version int64
	var scheduled sqltime.SQLiteNullTime
	err := tx.QueryRowContext(ctx, `
		SELECT id, agent_id, kind, text, target_mode, prepare_context, reclassify_on_edit, order_index,
			scheduled_at, source_payload, state, error, version, created_at, updated_at
		FROM agent_input_queue_items WHERE agent_id = ? AND id = ?`, agentID, inputID).Scan(
		&item.ID, &item.AgentID, &kind, &item.Text, &item.TargetMode, &item.PrepareContext, &item.ReclassifyOnEdit, &item.Order,
		&scheduled, &item.SourcePayload, &state, &item.Error,
		&version, &item.CreatedAt, &item.UpdatedAt)
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
	item.EditOwner = ephem.editOwnerOf(item.ID)
	item.ReservedSeq = ephem.reservedSeqOf(item.ID)
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
func readOrderingRows(ctx context.Context, tx db.DBTX, agentID string, ephem *agentEphemeralState) ([]OrderingMetadata, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, agent_id, kind, order_index, scheduled_at, state, prepare_context
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
		if err := rows.Scan(&item.ID, &item.AgentID, &kind, &item.Order, &scheduled, &state, &item.PrepareContext); err != nil {
			return nil, err
		}
		item.Kind = leapmuxv1.AgentInputKind(kind)
		item.State = leapmuxv1.AgentInputState(state)
		item.ScheduledAt = scheduled.Ptr()
		item.EditOwner = ephem.editOwnerOf(item.ID)
		items = append(items, item)
	}
	return items, rows.Err()
}

// headItem reads the full row of the prioritized first item that does not
// await an echo. It is the one definition of the head for every reader that
// does not already hold a sorted snapshot.
func headItem(ctx context.Context, tx db.DBTX, agentID string, now time.Time, ephem *agentEphemeralState) (StoredItem, bool, error) {
	ordering, err := readQueueOrdering(ctx, tx, agentID, now, ephem)
	if err != nil {
		return StoredItem{}, false, err
	}
	return headItemFromOrdering(ctx, tx, agentID, ordering, ephem)
}

func headItemFromOrdering(ctx context.Context, tx db.DBTX, agentID string, ordering queueOrdering, ephem *agentEphemeralState) (StoredItem, bool, error) {
	for _, head := range ordering.effective {
		if head.State == leapmuxv1.AgentInputState_AGENT_INPUT_STATE_AWAITING_ECHO {
			continue
		}
		item, _, found, err := getItemWithEphemeral(ctx, tx, agentID, head.ID, false, ephem)
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

func snapshotWithActiveBackgroundWork(ctx context.Context, tx db.DBTX, snapshot Snapshot) (Snapshot, error) {
	activeBackgroundWork, err := readActiveBackgroundWork(ctx, tx, snapshot.AgentID)
	if err != nil {
		return Snapshot{}, err
	}
	snapshot.ActiveBackgroundWork = activeBackgroundWork
	return snapshot, nil
}

type queueStateRow struct {
	revision   int64
	paused     bool
	reason     *leapmuxv1.AgentInputQueuePauseReason
	activeTurn bool
}

func readQueueStateRow(ctx context.Context, tx db.DBTX, agentID string) (queueStateRow, bool, error) {
	var row queueStateRow
	err := tx.QueryRowContext(ctx, `SELECT revision, pause_reason IS NOT NULL, pause_reason, active_turn FROM agent_input_queue_state WHERE agent_id = ?`, agentID).
		Scan(&row.revision, &row.paused, &row.reason, &row.activeTurn)
	if errors.Is(err, sql.ErrNoRows) {
		return queueStateRow{}, false, nil
	}
	if err != nil {
		return queueStateRow{}, false, err
	}
	return row, true, nil
}

// snapshotTx reads one agent's queue in effective order at the instant now.
// The items carry a text preview and no source payload: no client reads the
// payload, and a row can hold megabytes of it. The dispatch path reads the
// full row through getItemWithEphemeral.
func snapshotTx(ctx context.Context, tx db.DBTX, agentID string, now time.Time, ephem *agentEphemeralState) (Snapshot, error) {
	stateRow, found, err := readQueueStateRow(ctx, tx, agentID)
	if err != nil {
		return Snapshot{}, err
	}
	return snapshotTxWithState(ctx, tx, agentID, now, ephem, stateRow, found)
}

func snapshotTxWithState(ctx context.Context, tx db.DBTX, agentID string, now time.Time, ephem *agentEphemeralState, stateRow queueStateRow, found bool) (Snapshot, error) {
	snapshot := Snapshot{AgentID: agentID}
	if !found {
		return snapshotWithActiveBackgroundWork(ctx, tx, snapshot)
	}
	snapshot.Revision = uint64(stateRow.revision)
	snapshot.Paused = stateRow.paused
	snapshot.PauseReason = workerdb.StorageEnumValue(stateRow.reason)
	snapshot.ActiveTurn = stateRow.activeTurn
	if ephem != nil {
		snapshot.ActiveTurnSteerable = ephem.activeTurnSteerable
		snapshot.BypassBackgroundWorkInputID = ephem.bypassBackgroundWorkInputID
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT id, kind,
			CASE WHEN length(text) > ? THEN substr(text, 1, ?) || '…' ELSE text END,
			target_mode, prepare_context, reclassify_on_edit, order_index, scheduled_at, state, error, version, created_at, updated_at
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
		if err := rows.Scan(&item.ID, &kind, &item.TextPreview, &item.TargetMode, &item.PrepareContext, &item.ReclassifyOnEdit, &item.Order, &scheduled, &state, &item.Error, &version, &item.CreatedAt, &item.UpdatedAt); err != nil {
			_ = rows.Close()
			return Snapshot{}, err
		}
		item.Kind = leapmuxv1.AgentInputKind(kind)
		item.State = leapmuxv1.AgentInputState(state)
		item.Version = uint64(version)
		item.ScheduledAt = scheduled.Ptr()
		item.EditOwner = ephem.editOwnerOf(item.ID)
		item.ReservedSeq = ephem.reservedSeqOf(item.ID)
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
		return snapshotWithActiveBackgroundWork(ctx, tx, snapshot)
	}
	// Only the head can be steered or preempted, so only the head asks the
	// gate. The snapshot already sorted it, so the gate reads no rows again.
	gate, err := readDispatchGateFor(ctx, tx, agentID, now, snapshot.Items[head].OrderingMetadata, true, ephem)
	if err != nil {
		return Snapshot{}, err
	}
	snapshot.ActiveBackgroundWork = gate.ActiveBackgroundWork
	snapshot.BypassBackgroundWorkInputID = gate.BypassBackgroundWorkInputID
	// Steering dispatches THROUGH the running turn (requireActive) or bypasses
	// active background work when idle, and dispatches through a pause
	// (allowPaused).
	bypassGate := gate
	bypassGate.ActiveBackgroundWork = false
	snapshot.Items[head].CanSteer = gate.admits(true, true) == dispatchAdmitted ||
		(!gate.ActiveTurn && gate.ActiveBackgroundWork && bypassGate.admits(true, false) == dispatchAdmitted)
	// Preemption asks two questions of the SAME guard, which is why it needs no terms
	// of its own. Would the drain deliver this head once the turn and background-work
	// hold are cleared? And does it need the bypass at all -- a plan approval that
	// REPLACES the turn or an already-admitted item dispatches without preemption.
	//
	// Preemption borrows no kind filter. Steering injects into the running turn, so it
	// admits only a kind the model can take mid-turn; preemption injects nothing, and
	// the drain dispatches every kind once the turn is over (or immediately when only
	// background work holds it).
	afterBypass := bypassGate
	afterBypass.ActiveTurn = false
	snapshot.Items[head].CanPreempt = (gate.ActiveTurn || gate.ActiveBackgroundWork) &&
		afterBypass.admits(false, false) == dispatchAdmitted &&
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

func (s *Store) commitSnapshot(ctx context.Context, tx *sql.Tx, agentID string, now time.Time, staged agentEphemeralState) (Snapshot, error) {
	snapshot, err := snapshotTx(ctx, tx, agentID, now, &staged)
	if err != nil {
		return Snapshot{}, err
	}
	if err := s.commitEphemeral(tx, agentID, staged); err != nil {
		return Snapshot{}, err
	}
	return snapshot, nil
}

// placeControlReply keeps the current request's reply before future queued work.
// Existing replies and a dispatch or failure at the front retain their order.
func placeControlReply(ctx context.Context, tx *sql.Tx, agentID, inputID string, staged *agentEphemeralState) error {
	if staged != nil {
		staged.bypassBackgroundWorkInputID = ""
	}
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
