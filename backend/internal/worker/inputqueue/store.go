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
	"strings"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

type Store struct {
	db         *sql.DB
	classifier CommandClassifier
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db, classifier: ExactCommandClassifier{}}
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
		kind <= leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_CONTROL_FEEDBACK
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

func validateContent(kind leapmuxv1.AgentInputKind, text string, attachments []Attachment) error {
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
		kind == leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_CONTINUE ||
		kind == leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_CONTROL_FEEDBACK
}

func regularTurnKind(kind leapmuxv1.AgentInputKind) bool {
	return steerableInputKind(kind) || kind == leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_PLAN_EXECUTION
}

func (s *Store) Enqueue(ctx context.Context, input NewItem) (Snapshot, error) {
	if !validateIdentity(input.ID) || !validateIdentity(input.AgentID) {
		return Snapshot{}, fmt.Errorf("%w: agent and input IDs are required", ErrInvalidInput)
	}
	input.Kind = s.classifier.Classify(input.Kind, input.Text)
	if err := validateContent(input.Kind, input.Text, input.Attachments); err != nil {
		return Snapshot{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Snapshot{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := ensureState(ctx, tx, input.AgentID); err != nil {
		return Snapshot{}, err
	}

	existing, existingAttachments, found, err := getItem(ctx, tx, input.AgentID, input.ID, true)
	if err != nil {
		return Snapshot{}, err
	}
	if found {
		if existing.Kind != input.Kind || existing.Text != input.Text || existing.TargetMode != input.TargetMode ||
			existing.PrepareContext != input.PrepareContext || existing.ReclassifyOnEdit != input.ReclassifyOnEdit ||
			!attachmentsEqual(existingAttachments, input.Attachments) {
			return Snapshot{}, ErrConflict
		}
		snapshot, err := snapshotTx(ctx, tx, input.AgentID)
		if err != nil {
			return Snapshot{}, err
		}
		if err := tx.Commit(); err != nil {
			return Snapshot{}, err
		}
		return snapshot, nil
	}
	var acceptedAgentID, acceptedFingerprint string
	err = tx.QueryRowContext(ctx, `SELECT agent_id, input_fingerprint FROM messages WHERE id = ?`, input.ID).Scan(&acceptedAgentID, &acceptedFingerprint)
	if err == nil {
		if acceptedAgentID != input.AgentID || acceptedFingerprint == "" || acceptedFingerprint != inputFingerprint(input) {
			return Snapshot{}, ErrConflict
		}
		snapshot, snapshotErr := snapshotTx(ctx, tx, input.AgentID)
		if snapshotErr != nil {
			return Snapshot{}, snapshotErr
		}
		if err := tx.Commit(); err != nil {
			return Snapshot{}, err
		}
		return snapshot, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Snapshot{}, err
	}

	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_input_queue_items WHERE agent_id = ?`, input.AgentID).Scan(&count); err != nil {
		return Snapshot{}, err
	}
	if count >= MaxItems {
		return Snapshot{}, ErrQueueFull
	}
	var aggregate int64
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(a.size), 0)
		FROM agent_input_queue_attachments a
		JOIN agent_input_queue_items i ON i.id = a.item_id
		WHERE i.agent_id = ?`, input.AgentID).Scan(&aggregate); err != nil {
		return Snapshot{}, err
	}
	if aggregate+attachmentBytes(input.Attachments) > MaxQueueAttachmentBytes {
		return Snapshot{}, ErrQueueAttachmentsLarge
	}
	var orderIndex int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(order_index), 0) + 1 FROM agent_input_queue_items WHERE agent_id = ?`, input.AgentID).Scan(&orderIndex); err != nil {
		return Snapshot{}, err
	}
	now := nowText()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO agent_input_queue_items
		(id, agent_id, order_index, kind, text, target_mode, prepare_context, reclassify_on_edit, state, version, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?)`,
		input.ID, input.AgentID, orderIndex, input.Kind, input.Text, input.TargetMode, input.PrepareContext, input.ReclassifyOnEdit,
		leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED, now, now); err != nil {
		return Snapshot{}, normalizeConstraintError(err)
	}
	if err := replaceAttachments(ctx, tx, input.ID, input.Attachments); err != nil {
		return Snapshot{}, err
	}
	if err := bumpRevision(ctx, tx, input.AgentID); err != nil {
		return Snapshot{}, err
	}
	return commitSnapshot(ctx, tx, input.AgentID)
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
	snapshot, err := snapshotTx(ctx, tx, agentID)
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
	item, attachments, found, err := getItem(ctx, tx, agentID, inputID, true)
	if err != nil {
		return Snapshot{}, "", nil, err
	}
	if !found {
		return Snapshot{}, "", nil, ErrNotFound
	}
	if item.State == leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DISPATCHING {
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
	snapshot, err := snapshotTx(ctx, tx, agentID)
	if err != nil {
		return Snapshot{}, "", nil, err
	}
	if err := tx.Commit(); err != nil {
		return Snapshot{}, "", nil, err
	}
	return snapshot, item.Text, attachments, nil
}

func (s *Store) Update(ctx context.Context, agentID, inputID, clientID string, expectedVersion uint64, text string, attachments []Attachment) (Snapshot, error) {
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
	if item.Version != expectedVersion {
		return Snapshot{}, ErrVersionConflict
	}
	kind := item.Kind
	if item.ReclassifyOnEdit {
		kind = s.classifier.Classify(leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE, text)
	}
	if err := validateContent(kind, text, attachments); err != nil {
		return Snapshot{}, err
	}
	var aggregate int64
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(a.size), 0)
		FROM agent_input_queue_attachments a
		JOIN agent_input_queue_items i ON i.id = a.item_id
		WHERE i.agent_id = ? AND i.id <> ?`, agentID, inputID).Scan(&aggregate); err != nil {
		return Snapshot{}, err
	}
	if aggregate+attachmentBytes(attachments) > MaxQueueAttachmentBytes {
		return Snapshot{}, ErrQueueAttachmentsLarge
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE agent_input_queue_items
		SET kind = ?, text = ?, state = ?, reserved_seq = 0, error = '', edit_owner = '', version = version + 1, updated_at = ?
		WHERE agent_id = ? AND id = ? AND version = ?`,
		kind, text, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED, nowText(), agentID, inputID, expectedVersion)
	if err != nil {
		return Snapshot{}, err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return Snapshot{}, ErrVersionConflict
	}
	if err := replaceAttachments(ctx, tx, inputID, attachments); err != nil {
		return Snapshot{}, err
	}
	if err := bumpRevision(ctx, tx, agentID); err != nil {
		return Snapshot{}, err
	}
	return commitSnapshot(ctx, tx, agentID)
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
	return commitSnapshot(ctx, tx, agentID)
}

func (s *Store) Delete(ctx context.Context, agentID, inputID string) (Snapshot, error) {
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
	if item.State == leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DISPATCHING {
		return Snapshot{}, ErrConflict
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM agent_input_queue_items WHERE agent_id = ? AND id = ?`, agentID, inputID); err != nil {
		return Snapshot{}, err
	}
	if err := compactOrder(ctx, tx, agentID); err != nil {
		return Snapshot{}, err
	}
	if err := bumpRevision(ctx, tx, agentID); err != nil {
		return Snapshot{}, err
	}
	return commitSnapshot(ctx, tx, agentID)
}

func (s *Store) Move(ctx context.Context, agentID, inputID, beforeInputID string) (Snapshot, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Snapshot{}, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `SELECT id, state FROM agent_input_queue_items WHERE agent_id = ? ORDER BY order_index`, agentID)
	if err != nil {
		return Snapshot{}, err
	}
	type ordered struct {
		id    string
		state leapmuxv1.AgentInputState
	}
	var items []ordered
	for rows.Next() {
		var item ordered
		if err := rows.Scan(&item.id, &item.state); err != nil {
			_ = rows.Close()
			return Snapshot{}, err
		}
		items = append(items, item)
	}
	if err := rows.Close(); err != nil {
		return Snapshot{}, err
	}
	from := -1
	for i := range items {
		if items[i].id == inputID {
			from = i
			break
		}
	}
	if from < 0 {
		return Snapshot{}, ErrNotFound
	}
	if items[from].state == leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DISPATCHING {
		return Snapshot{}, ErrConflict
	}
	if beforeInputID == inputID {
		snapshot, err := snapshotTx(ctx, tx, agentID)
		if err != nil {
			return Snapshot{}, err
		}
		if err := tx.Commit(); err != nil {
			return Snapshot{}, err
		}
		return snapshot, nil
	}
	if len(items) > 0 && items[0].state == leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DISPATCHING && beforeInputID == items[0].id {
		return Snapshot{}, ErrConflict
	}
	moved := items[from]
	items = append(items[:from], items[from+1:]...)
	to := len(items)
	if beforeInputID != "" {
		to = -1
		for i := range items {
			if items[i].id == beforeInputID {
				to = i
				break
			}
		}
		if to < 0 {
			return Snapshot{}, ErrNotFound
		}
	}
	items = append(items, ordered{})
	copy(items[to+1:], items[to:])
	items[to] = moved
	orderedIDs := make([]string, len(items))
	for i := range items {
		orderedIDs[i] = items[i].id
	}
	if err := writeOrder(ctx, tx, agentID, orderedIDs); err != nil {
		return Snapshot{}, err
	}
	if err := bumpRevision(ctx, tx, agentID); err != nil {
		return Snapshot{}, err
	}
	return commitSnapshot(ctx, tx, agentID)
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
	if _, err := tx.ExecContext(ctx, `UPDATE agent_input_queue_state SET paused = ?, pause_reason = ?, pause_owner = ?, revision = revision + 1, updated_at = ? WHERE agent_id = ?`, paused, reason, owner, nowText(), agentID); err != nil {
		return Snapshot{}, err
	}
	return commitSnapshot(ctx, tx, agentID)
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
	snapshot, err := commitSnapshot(ctx, tx, agentID)
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
	snapshot, err := commitSnapshot(ctx, tx, agentID)
	return snapshot, changed == 1, err
}

func (s *Store) finishPlannedRestart(ctx context.Context, agentID string, processReplaced, resumeQueue bool) (Snapshot, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Snapshot{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := ensureState(ctx, tx, agentID); err != nil {
		return Snapshot{}, false, err
	}
	var paused, active bool
	var reason leapmuxv1.AgentInputQueuePauseReason
	var owner int
	if err := tx.QueryRowContext(ctx, `
		SELECT paused, pause_reason, pause_owner, active_turn
		FROM agent_input_queue_state WHERE agent_id = ?`, agentID).Scan(&paused, &reason, &owner, &active); err != nil {
		return Snapshot{}, false, err
	}
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
	if processReplaced {
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
				active_turn_kind = CASE WHEN ? THEN active_turn_kind ELSE 0 END,
				active_input_id = CASE WHEN ? THEN active_input_id ELSE '' END,
				revision = revision + 1, updated_at = ?
			WHERE agent_id = ?`,
			nextPaused, nextReason, nextOwner, nextActive, nextActive, nextActive, nowText(), agentID); err != nil {
			return Snapshot{}, false, err
		}
	}
	snapshot, err := commitSnapshot(ctx, tx, agentID)
	return snapshot, changed, err
}

func (s *Store) PrepareDispatch(ctx context.Context, agentID string) (*PreparedDispatch, Snapshot, error) {
	return s.prepare(ctx, agentID, "", false, false)
}

func (s *Store) PrepareRetry(ctx context.Context, agentID string) (*PreparedDispatch, Snapshot, error) {
	return s.prepare(ctx, agentID, "", true, false)
}

func (s *Store) PrepareSteer(ctx context.Context, agentID, inputID string) (*PreparedDispatch, Snapshot, error) {
	// Steering always addresses one item. An empty ID would skip the head test
	// below and reserve whatever sits at the head, and the manager's recovery
	// paths would then requeue by that same empty ID and match no row, so the
	// item would stay DISPATCHING until the Worker restarts.
	if !validateIdentity(inputID) {
		return nil, Snapshot{}, ErrNotHead
	}
	return s.prepare(ctx, agentID, inputID, true, true)
}

func (s *Store) prepare(ctx context.Context, agentID, expectedInputID string, allowPaused, requireActive bool) (*PreparedDispatch, Snapshot, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, Snapshot{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := ensureState(ctx, tx, agentID); err != nil {
		return nil, Snapshot{}, err
	}
	var paused, active, restarting bool
	var activeKind leapmuxv1.AgentInputKind
	if err := tx.QueryRowContext(ctx, `SELECT paused, restarting, active_turn, active_turn_kind FROM agent_input_queue_state WHERE agent_id = ?`, agentID).Scan(&paused, &restarting, &active, &activeKind); err != nil {
		return nil, Snapshot{}, err
	}
	// allowPaused lets retry and steering dispatch through a pause. A restart
	// in flight is what they must still respect: the old process is stopping,
	// so a write reaches a closed pipe and fails the item permanently.
	if allowPaused && restarting {
		return nil, Snapshot{}, ErrPlannedRestart
	}
	// Read the metadata only. The guards below reject most calls, and the head
	// item can carry up to MaxItemBytes of attachment data that this call then
	// discards. The dispatch path loads the data after the guards pass.
	item, _, found, err := headItem(ctx, tx, agentID, false)
	if err != nil {
		return nil, Snapshot{}, err
	}
	if expectedInputID != "" && (!found || item.ID != expectedInputID) {
		return nil, Snapshot{}, ErrNotHead
	}
	if requireActive && active && (!steerableInputKind(item.Kind) || !regularTurnKind(activeKind)) {
		return nil, Snapshot{}, ErrSteeringState
	}
	blockedByTurn := active
	if requireActive {
		blockedByTurn = !active
	}
	if !found || (paused && !allowPaused) || blockedByTurn || item.EditOwner != "" || item.State != leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED {
		snapshot, err := snapshotTx(ctx, tx, agentID)
		if err != nil {
			return nil, Snapshot{}, err
		}
		if err := tx.Commit(); err != nil {
			return nil, Snapshot{}, err
		}
		return nil, snapshot, nil
	}
	attachments, metadata, err := loadItemAttachments(ctx, tx, item.ID, true)
	if err != nil {
		return nil, Snapshot{}, err
	}
	item.Metadata = metadata
	var reservedSeq int64
	if item.ReservedSeq > 0 {
		reservedSeq = item.ReservedSeq
	} else if reservedSeq, err = reserveMessageSeq(ctx, tx, agentID); err != nil {
		return nil, Snapshot{}, err
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
	stateUpdate := `UPDATE agent_input_queue_state SET revision = revision + 1, updated_at = ? WHERE agent_id = ?`
	stateArgs := []any{nowText(), agentID}
	if !requireActive {
		stateUpdate = `UPDATE agent_input_queue_state SET active_turn = 1, active_turn_kind = ?, active_input_id = ?, revision = revision + 1, updated_at = ? WHERE agent_id = ?`
		stateArgs = []any{item.Kind, item.ID, nowText(), agentID}
	}
	if _, err := tx.ExecContext(ctx, stateUpdate, stateArgs...); err != nil {
		return nil, Snapshot{}, err
	}
	item.Attachments = attachments
	item.State = leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DISPATCHING
	item.ReservedSeq = reservedSeq
	snapshot, err := snapshotTx(ctx, tx, agentID)
	if err != nil {
		return nil, Snapshot{}, err
	}
	if err := tx.Commit(); err != nil {
		return nil, Snapshot{}, err
	}
	return &PreparedDispatch{Item: item, ReservedSeq: reservedSeq}, snapshot, nil
}

// RequeuePrepared returns a prepared item to the queue. The steer path uses it,
// and PrepareSteer claims no turn identity, so there is none to release.
func (s *Store) RequeuePrepared(ctx context.Context, agentID, inputID string) (Snapshot, error) {
	return s.requeuePrepared(ctx, agentID, inputID, false)
}

// RequeueBusy returns a prepared item to the queue after the provider refused
// it because a turn of its own is in flight. The queue stays OPEN and the item
// keeps no error text: nothing failed, and the end of that turn delivers it.
//
// It also releases the turn identity that PrepareDispatch claimed. That claim
// describes THIS dispatch, and this dispatch never reached the provider, so the
// turn that stands is the provider's own -- which carries no input id, and no
// kind that the queue can know. A claim left behind makes Snapshot.CanSteer and
// PrepareSteer judge the running turn by the refused item's kind, and each of
// them then answers a steer request for the wrong turn.
//
// active_turn itself stays as it is. The refusal proves that a turn ran when
// the provider read its flag, and the same refusal publishes that flag. But a
// turn that ended inside the provider call already committed its clear, and a
// write here would restore a turn that no envelope ever ends again.
func (s *Store) RequeueBusy(ctx context.Context, agentID, inputID string) (Snapshot, error) {
	return s.requeuePrepared(ctx, agentID, inputID, true)
}

func (s *Store) requeuePrepared(ctx context.Context, agentID, inputID string, releaseTurnIdentity bool) (Snapshot, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Snapshot{}, err
	}
	defer func() { _ = tx.Rollback() }()
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
		if _, err := tx.ExecContext(ctx, `UPDATE agent_input_queue_state SET active_turn_kind = 0, active_input_id = '', revision = revision + 1, updated_at = ? WHERE agent_id = ? AND active_input_id = ?`, nowText(), agentID, inputID); err != nil {
			return Snapshot{}, err
		}
	}
	if err := bumpRevision(ctx, tx, agentID); err != nil {
		return Snapshot{}, err
	}
	return commitSnapshot(ctx, tx, agentID)
}

// RequeueAndPause returns a prepared item to the queue and pauses the queue.
// It records the reason on the item without a failed state, so the queue
// resumes the same input when the agent can take it again.
func (s *Store) RequeueAndPause(ctx context.Context, agentID, inputID string, dispatchErr error) (Snapshot, error) {
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
	if _, err := tx.ExecContext(ctx, `UPDATE agent_input_queue_items SET state = ?, reserved_seq = 0, error = ?, updated_at = ? WHERE agent_id = ? AND id = ?`,
		leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED, errText, nowText(), agentID, inputID); err != nil {
		return Snapshot{}, err
	}
	if _, err := pauseAndEndTurn(ctx, tx, agentID,
		leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_AGENT_STOPPED, pauseOwnerAgentStopped); err != nil {
		return Snapshot{}, err
	}
	return commitSnapshot(ctx, tx, agentID)
}

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
	if !found || item.State != leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DISPATCHING || item.ReservedSeq != prepared.ReservedSeq {
		return AcceptedTranscript{}, Snapshot{}, ErrConflict
	}
	content, err := transcriptContent(item, attachments)
	if err != nil {
		return AcceptedTranscript{}, Snapshot{}, err
	}
	compressed, compression := msgcodec.Compress(content)
	var provider leapmuxv1.AgentProvider
	if err := tx.QueryRowContext(ctx, `SELECT agent_provider FROM agents WHERE id = ?`, item.AgentID).Scan(&provider); err != nil {
		return AcceptedTranscript{}, Snapshot{}, err
	}
	mark := leapmuxv1.MarkType_MARK_TYPE_UNSPECIFIED
	if item.ReclassifyOnEdit || item.Kind == leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE {
		mark = leapmuxv1.MarkType_MARK_TYPE_USER_MESSAGE
	} else if item.Kind == leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_CONTROL_FEEDBACK {
		mark = leapmuxv1.MarkType_MARK_TYPE_CONTROL_RESPONSE
	}
	spanLines := result.SpanLines
	if spanLines == "" {
		spanLines = "[]"
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO messages
		(id, agent_id, seq, source, content, content_compression, input_fingerprint, depth, span_id,
		 parent_span_id, span_type, span_lines, span_color, agent_provider, mark_type, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 0, '', '', '', ?, 0, ?, ?, ?)`,
		item.ID, item.AgentID, item.ReservedSeq, leapmuxv1.MessageSource_MESSAGE_SOURCE_USER,
		compressed, compression, inputFingerprint(NewItem{
			ID: item.ID, AgentID: item.AgentID, Kind: item.Kind, Text: item.Text,
			TargetMode: item.TargetMode, PrepareContext: item.PrepareContext,
			ReclassifyOnEdit: item.ReclassifyOnEdit, Attachments: attachments,
		}), spanLines, provider, mark, item.CreatedAt); err != nil {
		return AcceptedTranscript{}, Snapshot{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM agent_input_queue_items WHERE agent_id = ? AND id = ?`, item.AgentID, item.ID); err != nil {
		return AcceptedTranscript{}, Snapshot{}, err
	}
	if err := compactOrder(ctx, tx, item.AgentID); err != nil {
		return AcceptedTranscript{}, Snapshot{}, err
	}
	stateUpdate := `UPDATE agent_input_queue_state SET active_turn = 0, active_turn_kind = 0, active_input_id = '', revision = revision + 1, updated_at = ? WHERE agent_id = ?`
	stateArgs := []any{nowText(), item.AgentID}
	if result.StartsTurn && !result.Steering {
		// The turn is asserted only while the state still holds THIS dispatch.
		// PrepareDispatch opened the turn and recorded the input id before the
		// provider call, and the manager releases the coordinator lock across
		// that call -- so a turn that ends inside it clears both fields first.
		// A write of the turn here would restore one that no envelope ever ends
		// again, and the queue would then hold every later message for the life
		// of the process.
		stateUpdate = `UPDATE agent_input_queue_state SET active_turn = 1, active_turn_kind = ?, active_input_id = ?, revision = revision + 1, updated_at = ? WHERE agent_id = ? AND active_input_id = ?`
		stateArgs = []any{item.Kind, item.ID, nowText(), item.AgentID, item.ID}
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
	snapshot, err := snapshotTx(ctx, tx, item.AgentID)
	if err != nil {
		return AcceptedTranscript{}, Snapshot{}, err
	}
	if err := tx.Commit(); err != nil {
		return AcceptedTranscript{}, Snapshot{}, err
	}
	return AcceptedTranscript{
		ID: item.ID, AgentID: item.AgentID, Seq: item.ReservedSeq,
		Content: compressed, ContentCompression: compression,
		AgentProvider: provider, MarkType: mark, SpanLines: spanLines,
		CreatedAt: item.CreatedAt,
	}, snapshot, nil
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
	if _, err := tx.ExecContext(ctx, `UPDATE agent_input_queue_items SET state = ?, error = ?, updated_at = ? WHERE agent_id = ? AND id = ?`, state, errText, nowText(), agentID, inputID); err != nil {
		return Snapshot{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_input_queue_state SET active_turn = 0, active_turn_kind = 0, active_input_id = '', paused = 1, pause_reason = ?, pause_owner = ?, revision = revision + 1, updated_at = ? WHERE agent_id = ?`, reason, pauseOwnerDelivery, nowText(), agentID); err != nil {
		return Snapshot{}, err
	}
	return commitSnapshot(ctx, tx, agentID)
}

// TurnEnded clears the active turn.
//
// The clear carries no turn identity, because the provider signal that reports
// a turn end carries none either: OutputSink.SetTurnActive identifies only the
// agent. Order is what keeps the clear correct. Every provider publishes its
// turn flag on its own single reader goroutine, in wire order, and drain never
// starts the next turn before Accept commits, so a turn end for turn N always
// precedes the turn start for N+1. Two rules preserve that order, and both are
// load-bearing: no call site may publish a turn end from a NEW goroutine (the
// Codex turn/completed handler once did, to escape the coordinator lock, and
// reordered the two reports), and drain must not hold the coordinator lock
// across a provider call, which is what made that escape look necessary.
//
// One case stays open: a turn end that an agent process emits AFTER the Worker
// replaced it clears the new process's turn. Only a provider generation
// threaded through SetTurnActiveFunc can reject that, and the signal does not
// carry one today.
//
// The Boolean reports whether the durable state moved. Every publish of the
// provider's turn flag reaches here, and a provider republishes the unchanged
// value freely, so a call that changes nothing must leave the revision alone.
func (s *Store) TurnEnded(ctx context.Context, agentID string) (Snapshot, bool, error) {
	return s.setTurnState(ctx, agentID, false)
}

// TurnStarted records a turn that the agent process began on its own. It keeps
// the turn that a dispatch already recorded: that one identifies its input, and
// this call carries no identity to replace it with.
//
// The Boolean reports whether the durable state moved. See TurnEnded.
func (s *Store) TurnStarted(ctx context.Context, agentID string) (Snapshot, bool, error) {
	return s.setTurnState(ctx, agentID, true)
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
		`UPDATE agent_input_queue_state SET active_turn = 0, active_turn_kind = 0, revision = revision + 1, updated_at = ? WHERE agent_id = ? AND active_input_id = '' AND (active_turn = 1 OR active_turn_kind <> 0)`,
		[]any{nowText(), agentID})
}

// setTurnState reconciles the durable turn flag with the state the provider
// reports. It is idempotent in both directions, so the caller may report the
// same state as often as the provider publishes it.
//
// The rising edge records the turn with NO kind and NO input id, because the
// signal supplies neither. AGENT_INPUT_KIND_UNSPECIFIED is what the queue
// really knows, and regularTurnKind refuses it, so Snapshot.CanSteer and
// PrepareSteer both refuse a steer into a turn that the queue did not dispatch.
// A concrete kind here would be an invention: the store cannot tell an ordinary
// reply from an auto-compaction, and both predicates trust the column.
func (s *Store) setTurnState(ctx context.Context, agentID string, active bool) (Snapshot, bool, error) {
	statement := `UPDATE agent_input_queue_state SET active_turn = 0, active_turn_kind = 0, active_input_id = '', revision = revision + 1, updated_at = ? WHERE agent_id = ? AND (active_turn = 1 OR active_turn_kind <> 0 OR active_input_id <> '')`
	args := []any{nowText(), agentID}
	if active {
		statement = `UPDATE agent_input_queue_state SET active_turn = 1, active_turn_kind = ?, active_input_id = '', revision = revision + 1, updated_at = ? WHERE agent_id = ? AND active_turn = 0`
		args = []any{leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_UNSPECIFIED, nowText(), agentID}
	}
	return s.execTurnState(ctx, agentID, statement, args)
}

// execTurnState runs one guarded write against the turn columns and reports
// whether it moved a row. Every turn write shares this shape: the state row must
// exist, the guard decides whether anything changes, and the caller needs the
// committed snapshot either way.
func (s *Store) execTurnState(ctx context.Context, agentID, statement string, args []any) (Snapshot, bool, error) {
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
	snapshot, err := snapshotTx(ctx, tx, agentID)
	if err != nil {
		return Snapshot{}, false, err
	}
	if err := tx.Commit(); err != nil {
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
	return commitSnapshot(ctx, tx, agentID)
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
func pauseAndEndTurn(ctx context.Context, tx *sql.Tx, agentID string, reason leapmuxv1.AgentInputQueuePauseReason, owner int) (sql.Result, error) {
	return tx.ExecContext(ctx, `
		UPDATE agent_input_queue_state
		SET active_turn = 0, active_turn_kind = 0, active_input_id = '',
			paused = 1,
			pause_reason = CASE WHEN paused = 1 THEN pause_reason ELSE ? END,
			pause_owner = CASE WHEN paused = 0 OR pause_owner = ? THEN ? ELSE pause_owner END,
			revision = revision + 1, updated_at = ?
		WHERE agent_id = ? AND (active_turn = 1 OR paused = 0 OR pause_owner = ?)`,
		reason, pauseOwnerPlannedRestart, owner, nowText(), agentID, pauseOwnerPlannedRestart)
}

// resumeOwnedPause lifts a pause only when the given cause still owns it.
// The result reports whether the row changed.
func resumeOwnedPause(ctx context.Context, tx *sql.Tx, agentID string, owner int) (sql.Result, error) {
	return tx.ExecContext(ctx, `
		UPDATE agent_input_queue_state
		SET paused = 0, pause_reason = 0, pause_owner = 0,
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
	snapshot, err := commitSnapshot(ctx, tx, agentID)
	return snapshot, changed == 1, err
}

func (s *Store) Retry(ctx context.Context, agentID, inputID string, confirmUncertain bool) (Snapshot, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Snapshot{}, err
	}
	defer func() { _ = tx.Rollback() }()
	item, _, found, err := headItem(ctx, tx, agentID, false)
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
	// The item returns to QUEUED, so it releases its reservation. A stale
	// reservation makes the retried message sort before an agent message that
	// the transcript took after the failure.
	if _, err := tx.ExecContext(ctx, `UPDATE agent_input_queue_items SET state = ?, reserved_seq = 0, error = '', updated_at = ? WHERE agent_id = ? AND id = ?`, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED, nowText(), agentID, inputID); err != nil {
		return Snapshot{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_input_queue_state SET active_turn = 0, active_turn_kind = 0, active_input_id = '', revision = revision + 1, updated_at = ? WHERE agent_id = ?`, nowText(), agentID); err != nil {
		return Snapshot{}, err
	}
	// Lift the pause that the failure created, so the items behind the retried
	// head dispatch again. A pause that the user or an archive created keeps
	// its own owner and survives.
	if _, err := resumeOwnedPause(ctx, tx, agentID, pauseOwnerDelivery); err != nil {
		return Snapshot{}, err
	}
	return commitSnapshot(ctx, tx, agentID)
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
		var dispatching int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_input_queue_items WHERE agent_id = ? AND state = ?`, agentID, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DISPATCHING).Scan(&dispatching); err != nil {
			_ = tx.Rollback()
			return nil, err
		}
		var active bool
		if err := tx.QueryRowContext(ctx, `SELECT active_turn FROM agent_input_queue_state WHERE agent_id = ?`, agentID).Scan(&active); err != nil {
			_ = tx.Rollback()
			return nil, err
		}
		if dispatching > 0 {
			if _, err := tx.ExecContext(ctx, `UPDATE agent_input_queue_items SET state = ?, error = 'Worker restarted before delivery was confirmed', updated_at = ? WHERE agent_id = ? AND state = ?`, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DELIVERY_UNCERTAIN, nowText(), agentID, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DISPATCHING); err != nil {
				_ = tx.Rollback()
				return nil, err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE agent_input_queue_state SET active_turn = 0, active_turn_kind = 0, active_input_id = '', paused = 1, pause_reason = ?, pause_owner = ?, revision = revision + 1, updated_at = ? WHERE agent_id = ?`, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_DELIVERY_UNCERTAIN, pauseOwnerRecovery, nowText(), agentID); err != nil {
				_ = tx.Rollback()
				return nil, err
			}
		} else if active {
			if _, err := tx.ExecContext(ctx, `UPDATE agent_input_queue_state SET active_turn = 0, active_turn_kind = 0, active_input_id = '', paused = 1, pause_reason = ?, pause_owner = ?, revision = revision + 1, updated_at = ? WHERE agent_id = ?`, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_INTERRUPTED, pauseOwnerRecovery, nowText(), agentID); err != nil {
				_ = tx.Rollback()
				return nil, err
			}
		}
		snapshot, err := snapshotTx(ctx, tx, agentID)
		if err != nil {
			_ = tx.Rollback()
			return nil, err
		}
		if err := tx.Commit(); err != nil {
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

func bumpRevision(ctx context.Context, tx *sql.Tx, agentID string) error {
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

func getItem(ctx context.Context, tx *sql.Tx, agentID, inputID string, loadAttachments bool) (Item, []Attachment, bool, error) {
	var item Item
	var kind, state int32
	var version int64
	err := tx.QueryRowContext(ctx, `
		SELECT id, agent_id, kind, text, target_mode, prepare_context, reclassify_on_edit, order_index, state, error, edit_owner, version, reserved_seq, created_at, updated_at
		FROM agent_input_queue_items WHERE agent_id = ? AND id = ?`, agentID, inputID).Scan(
		&item.ID, &item.AgentID, &kind, &item.Text, &item.TargetMode, &item.PrepareContext, &item.ReclassifyOnEdit, &item.Order, &state, &item.Error,
		&item.EditOwner, &version, &item.ReservedSeq, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Item{}, nil, false, nil
	}
	if err != nil {
		return Item{}, nil, false, err
	}
	item.Kind = leapmuxv1.AgentInputKind(kind)
	item.State = leapmuxv1.AgentInputState(state)
	item.Version = uint64(version)
	if !loadAttachments {
		return item, nil, true, nil
	}
	attachments, metadata, err := loadItemAttachments(ctx, tx, inputID, true)
	item.Metadata = metadata
	return item, attachments, true, err
}

// headItem reads the first item in queue order. loadAttachments controls
// whether it also reads every attachment's DATA, which one item can carry up
// to MaxItemBytes of; a caller that only tests the item's state passes false.
func headItem(ctx context.Context, tx *sql.Tx, agentID string, loadAttachments bool) (Item, []Attachment, bool, error) {
	var inputID string
	err := tx.QueryRowContext(ctx, `SELECT id FROM agent_input_queue_items WHERE agent_id = ? ORDER BY order_index LIMIT 1`, agentID).Scan(&inputID)
	if errors.Is(err, sql.ErrNoRows) {
		return Item{}, nil, false, nil
	}
	if err != nil {
		return Item{}, nil, false, err
	}
	return getItem(ctx, tx, agentID, inputID, loadAttachments)
}

func loadItemAttachments(ctx context.Context, tx *sql.Tx, inputID string, loadData bool) ([]Attachment, []AttachmentMetadata, error) {
	query := `SELECT filename, mime_type, size FROM agent_input_queue_attachments WHERE item_id = ? ORDER BY position`
	if loadData {
		query = `SELECT filename, mime_type, data, size FROM agent_input_queue_attachments WHERE item_id = ? ORDER BY position`
	}
	rows, err := tx.QueryContext(ctx, query, inputID)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = rows.Close() }()
	var attachments []Attachment
	var metadata []AttachmentMetadata
	for rows.Next() {
		var filename, mimeType string
		var data []byte
		var size int64
		if loadData {
			err = rows.Scan(&filename, &mimeType, &data, &size)
		} else {
			err = rows.Scan(&filename, &mimeType, &size)
		}
		if err != nil {
			return nil, nil, err
		}
		order := int32(len(metadata))
		metadata = append(metadata, AttachmentMetadata{Filename: filename, MimeType: mimeType, Size: size, Order: order})
		if loadData {
			attachments = append(attachments, Attachment{Filename: filename, MimeType: mimeType, Data: append([]byte(nil), data...)})
		}
	}
	return attachments, metadata, rows.Err()
}

func snapshotTx(ctx context.Context, tx *sql.Tx, agentID string) (Snapshot, error) {
	var snapshot Snapshot
	var revision int64
	var reason int32
	var activeKind int32
	err := tx.QueryRowContext(ctx, `SELECT agent_id, revision, paused, pause_reason, active_turn, active_turn_kind FROM agent_input_queue_state WHERE agent_id = ?`, agentID).Scan(&snapshot.AgentID, &revision, &snapshot.Paused, &reason, &snapshot.ActiveTurn, &activeKind)
	if errors.Is(err, sql.ErrNoRows) {
		return Snapshot{AgentID: agentID}, nil
	}
	if err != nil {
		return Snapshot{}, err
	}
	snapshot.Revision = uint64(revision)
	snapshot.PauseReason = leapmuxv1.AgentInputQueuePauseReason(reason)
	snapshot.ActiveTurnKind = leapmuxv1.AgentInputKind(activeKind)
	rows, err := tx.QueryContext(ctx, `
		SELECT id, kind,
			CASE WHEN length(text) > ? THEN substr(text, 1, ?) || '…' ELSE text END,
			target_mode, prepare_context, reclassify_on_edit, order_index, state, error, edit_owner, version, reserved_seq, created_at, updated_at
		FROM agent_input_queue_items WHERE agent_id = ? ORDER BY order_index`,
		snapshotTextPreviewCharacters, snapshotTextPreviewCharacters, agentID)
	if err != nil {
		return Snapshot{}, err
	}
	for rows.Next() {
		var item Item
		var kind, state int32
		var version int64
		item.AgentID = agentID
		if err := rows.Scan(&item.ID, &kind, &item.Text, &item.TargetMode, &item.PrepareContext, &item.ReclassifyOnEdit, &item.Order, &state, &item.Error, &item.EditOwner, &version, &item.ReservedSeq, &item.CreatedAt, &item.UpdatedAt); err != nil {
			_ = rows.Close()
			return Snapshot{}, err
		}
		item.Kind = leapmuxv1.AgentInputKind(kind)
		item.State = leapmuxv1.AgentInputState(state)
		item.Version = uint64(version)
		snapshot.Items = append(snapshot.Items, item)
	}
	if err := rows.Close(); err != nil {
		return Snapshot{}, err
	}
	// One query for every item's attachment metadata. A per-item query costs
	// one round trip for each of up to MaxItems entries, inside the write
	// transaction that every queue mutation already holds.
	metadata, err := loadQueueAttachmentMetadata(ctx, tx, agentID)
	if err != nil {
		return Snapshot{}, err
	}
	for i := range snapshot.Items {
		snapshot.Items[i].Metadata = metadata[snapshot.Items[i].ID]
		// Answer the steering precondition where the store already knows it,
		// from the same predicate that prepare applies. A browser that derives
		// this from the kind alone offers a Steer the Worker then refuses.
		snapshot.Items[i].CanSteer = i == 0 &&
			snapshot.Items[i].State == leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED &&
			snapshot.Items[i].EditOwner == "" &&
			snapshot.ActiveTurn &&
			steerableInputKind(snapshot.Items[i].Kind) &&
			regularTurnKind(snapshot.ActiveTurnKind)
	}
	return snapshot, nil
}

// loadQueueAttachmentMetadata reads every attachment's metadata for one
// agent's queue, keyed by item ID. It never reads the attachment data.
func loadQueueAttachmentMetadata(ctx context.Context, tx *sql.Tx, agentID string) (map[string][]AttachmentMetadata, error) {
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

func commitSnapshot(ctx context.Context, tx *sql.Tx, agentID string) (Snapshot, error) {
	snapshot, err := snapshotTx(ctx, tx, agentID)
	if err != nil {
		return Snapshot{}, err
	}
	if err := tx.Commit(); err != nil {
		return Snapshot{}, err
	}
	return snapshot, nil
}

func compactOrder(ctx context.Context, tx *sql.Tx, agentID string) error {
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
	if err := rows.Close(); err != nil {
		return err
	}
	return writeOrder(ctx, tx, agentID, itemIDs)
}

func writeOrder(ctx context.Context, tx *sql.Tx, agentID string, itemIDs []string) error {
	// Move all rows out of the active key space first. This avoids transient
	// UNIQUE(agent_id, order_index) conflicts while rows exchange positions.
	if _, err := tx.ExecContext(ctx, `UPDATE agent_input_queue_items SET order_index = order_index + 1000000 WHERE agent_id = ?`, agentID); err != nil {
		return err
	}
	for i := range itemIDs {
		if _, err := tx.ExecContext(ctx, `UPDATE agent_input_queue_items SET order_index = ? WHERE agent_id = ? AND id = ?`, i+1, agentID, itemIDs[i]); err != nil {
			return err
		}
	}
	return nil
}

// transcriptContent builds the stored message body. Every optional part is
// independent: a plan-execution input keeps its marker whether or not it
// carries attachments, because each renderer keys the collapsible bubble off
// that marker alone.
func transcriptContent(item Item, attachments []Attachment) ([]byte, error) {
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

func inputFingerprint(input NewItem) string {
	type fingerprintAttachment struct {
		Filename string `json:"filename"`
		MimeType string `json:"mime_type"`
		Data     []byte `json:"data"`
	}
	type fingerprintPayload struct {
		ID               string                   `json:"id"`
		AgentID          string                   `json:"agent_id"`
		Kind             leapmuxv1.AgentInputKind `json:"kind"`
		Text             string                   `json:"text"`
		TargetMode       string                   `json:"target_mode"`
		PrepareContext   bool                     `json:"prepare_context"`
		ReclassifyOnEdit bool                     `json:"reclassify_on_edit"`
		Attachments      []fingerprintAttachment  `json:"attachments"`
	}
	attachments := make([]fingerprintAttachment, len(input.Attachments))
	for index := range input.Attachments {
		attachments[index] = fingerprintAttachment(input.Attachments[index])
	}
	data, err := json.Marshal(fingerprintPayload{
		ID: input.ID, AgentID: input.AgentID, Kind: input.Kind, Text: input.Text,
		TargetMode: input.TargetMode, PrepareContext: input.PrepareContext,
		ReclassifyOnEdit: input.ReclassifyOnEdit, Attachments: attachments,
	})
	if err != nil {
		panic(fmt.Sprintf("marshal queued input fingerprint: %v", err))
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func normalizeConstraintError(err error) error {
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "unique constraint") {
		return ErrConflict
	}
	return err
}
