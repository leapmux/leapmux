package service

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	"github.com/leapmux/leapmux/internal/util/sqltime"
	"github.com/leapmux/leapmux/internal/util/timefmt"
	"github.com/leapmux/leapmux/internal/worker/agent"
	workerdb "github.com/leapmux/leapmux/internal/worker/db"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

type notificationWrite struct {
	reference *notifThreadRef
	message   *leapmuxv1.AgentChatMessage
}

type notificationWriteMode uint8

const (
	notificationThreaded notificationWriteMode = iota + 1
	notificationStandalone
	notificationReport
)

func (h *OutputHandler) persistNotificationThreaded(agentID string, provider leapmuxv1.AgentProvider, plugin agent.Provider, source leapmuxv1.MessageSource, content agent.MessageContent) (bool, error) {
	return h.writeNotification(agentID, provider, plugin, source, content, notificationThreaded)
}

func (h *OutputHandler) writeNotification(agentID string, provider leapmuxv1.AgentProvider, plugin agent.Provider, source leapmuxv1.MessageSource, content agent.MessageContent, mode notificationWriteMode) (bool, error) {
	if mode != notificationThreaded && mode != notificationStandalone && mode != notificationReport {
		return false, errors.New("the notification write mode is invalid")
	}
	standalone := mode != notificationThreaded
	if h.db == nil {
		return false, errors.New("the notification store has no database")
	}
	if err := agent.ValidateMessageCompletion(content); err != nil {
		return false, err
	}
	var owner *transcriptOwner
	rootID := h.resolveRoot(agentID, "")
	if content.Publication == nil {
		if sink := h.sinkForAgent(agentID); sink != nil {
			content = sink.CaptureMessage(content, agent.SpanInfo{})
		}
	}
	if content.Publication != nil {
		captured, err := readCapturedOwner(content)
		if err != nil {
			return false, err
		}
		if captured.sink.h != h || captured.sink.agentID != agentID || captured.sink.agentProvider != provider {
			return false, errors.New("the captured notification owner does not match its destination")
		}
		owner = captured
		rootID = captured.sink.rootAgentID
	}
	if content.Publication != nil && owner == nil {
		return false, errors.New("the captured notification has no matching destination")
	}
	if content.AgentSessionID == "" && content.Publication == nil {
		content.AgentSessionID = h.messageSessionID(agentID)
	}
	content = content.Clone()
	entry, err := agent.NewNotificationEntry(provider, source, content)
	if err != nil {
		return false, err
	}
	mutation := h.transcriptMutationMutex(rootID)
	mutex := h.notifMutex(agentID)
	for {
		prepared, err := h.prepareNotification(agentID, provider, plugin, source, content, owner, standalone)
		if err != nil {
			return false, err
		}
		mutation.RLock()
		mutex.Lock()
		write, err := h.writeNotificationLocked(agentID, provider, source, content, entry, owner, mode, prepared)
		if err == nil && write.reference != nil {
			if owner != nil {
				owner.thread.reference = write.reference
			}
			if owner == nil || owner.IsCurrent() {
				h.lastNotifThread.Store(agentID, write.reference)
			}
			if write.message != nil {
				content.WriteReceipt.RecordStoredWrite(true)
				if owner != nil {
					owner.enqueueMessage(write.message)
				} else {
					h.enqueueMessage(agentID, write.message)
				}
			}
		}
		mutex.Unlock()
		mutation.RUnlock()
		if errors.Is(err, errNotificationPreparationChanged) {
			continue
		}
		h.watcher.DrainAgentEvents(agentID)
		return write.message != nil && err == nil, err
	}
}

var errNotificationPreparationChanged = errors.New("the notification parent changed during preparation")

type notificationPreparation struct {
	parent  *db.Message
	wrapper *notifThreadWrapper
	state   agent.NotificationReductionState
}

func (h *OutputHandler) notificationReference(agentID string, provider leapmuxv1.AgentProvider, source leapmuxv1.MessageSource, content agent.MessageContent, owner *transcriptOwner, standalone bool) *notifThreadRef {
	if standalone {
		return nil
	}
	var reference *notifThreadRef
	if owner != nil {
		reference = owner.thread.reference
	}
	if reference == nil && (owner == nil || owner.IsCurrent()) {
		if cached, present := h.lastNotifThread.Load(agentID); present {
			reference = cached.(*notifThreadRef)
		}
	}
	if reference == nil || reference.source != source || reference.sessionID != content.AgentSessionID || reference.provider != provider {
		return nil
	}
	copy := *reference
	return &copy
}

func (h *OutputHandler) readNotificationParent(queries *db.Queries, agentID string, provider leapmuxv1.AgentProvider, source leapmuxv1.MessageSource, content agent.MessageContent, owner *transcriptOwner, standalone bool, reference *notifThreadRef) (*db.Message, error) {
	if standalone || (reference == nil && owner != nil && !owner.IsCurrent()) {
		return nil, nil
	}
	var row db.Message
	var err error
	if reference != nil {
		row, err = queries.GetMessageByAgentAndID(bgCtx(), db.GetMessageByAgentAndIDParams{AgentID: agentID, ID: reference.msgID})
	} else {
		row, err = queries.GetLatestMessageByAgentAndSession(bgCtx(), db.GetLatestMessageByAgentAndSessionParams{AgentID: agentID, AgentSessionID: content.AgentSessionID})
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if row.Source != source || row.AgentProvider != provider || row.AgentSessionID != content.AgentSessionID {
		if reference != nil {
			return nil, errors.New("the notification aggregate does not match its entry owner")
		}
		return nil, nil
	}
	if (owner == nil || owner.IsCurrent()) && row.TranscriptOnly {
		return nil, nil
	}
	data, err := msgcodec.Decompress(row.Content, row.ContentCompression)
	if err != nil {
		return nil, err
	}
	wrapper, err := unwrapNotifContent(data)
	if err != nil || wrapper.Type != notifThreadWrapperType {
		if reference != nil {
			return nil, errors.New("the notification aggregate has an invalid wrapper")
		}
		return nil, nil
	}
	return &row, nil
}

func (h *OutputHandler) prepareNotification(agentID string, provider leapmuxv1.AgentProvider, plugin agent.Provider, source leapmuxv1.MessageSource, content agent.MessageContent, owner *transcriptOwner, standalone bool) (notificationPreparation, error) {
	if err := agent.ValidateIncomingMessageMetadata(content.Metadata); err != nil {
		return notificationPreparation{}, err
	}
	mutex := h.notifMutex(agentID)
	mutex.Lock()
	reference := h.notificationReference(agentID, provider, source, content, owner, standalone)
	mutex.Unlock()
	parent, err := h.readNotificationParent(h.queries, agentID, provider, source, content, owner, standalone, reference)
	if err != nil {
		return notificationPreparation{}, err
	}
	prepared := notificationPreparation{parent: parent, wrapper: &notifThreadWrapper{Type: notifThreadWrapperType, Messages: []json.RawMessage{}}}
	if parent != nil {
		data, err := msgcodec.Decompress(parent.Content, parent.ContentCompression)
		if err != nil {
			return notificationPreparation{}, err
		}
		prepared.wrapper, err = unwrapNotifContent(data)
		if err != nil {
			return notificationPreparation{}, err
		}
		supplement, err := msgcodec.Decompress(parent.SupplementalContent, parent.SupplementalContentCompression)
		if err != nil {
			return notificationPreparation{}, err
		}
		parsed, err := agent.ParseStoredMessageSupplement(supplement)
		if err != nil {
			return notificationPreparation{}, err
		}
		prepared.state, err = parsed.NotificationReduction(len(prepared.wrapper.Messages))
		if err != nil {
			return notificationPreparation{}, err
		}
	}
	prepared.wrapper.Messages, prepared.state, err = reduceNotificationThread(prepared.wrapper.Messages, prepared.state, content.Original, plugin)
	return prepared, err
}

func notificationParentEqual(first, second *db.Message) bool {
	if first == nil || second == nil {
		return first == second
	}
	return first.ID == second.ID && first.AgentID == second.AgentID && first.AgentSessionID == second.AgentSessionID &&
		first.Source == second.Source && first.AgentProvider == second.AgentProvider && first.Seq == second.Seq &&
		first.ContentCompression == second.ContentCompression && bytes.Equal(first.Content, second.Content) &&
		first.SupplementalContentCompression == second.SupplementalContentCompression && bytes.Equal(first.SupplementalContent, second.SupplementalContent) &&
		first.SupplementalRevision == second.SupplementalRevision && first.TranscriptOnly == second.TranscriptOnly &&
		workerdb.StorageEnumValue(first.Completion) == workerdb.StorageEnumValue(second.Completion) && first.SpanLines == second.SpanLines
}

func (h *OutputHandler) writeNotificationLocked(agentID string, provider leapmuxv1.AgentProvider, source leapmuxv1.MessageSource, content agent.MessageContent, entry agent.NotificationEntry, owner *transcriptOwner, mode notificationWriteMode, prepared notificationPreparation) (notificationWrite, error) {
	standalone := mode != notificationThreaded
	ctx := bgCtx()
	connection, err := h.db.Conn(ctx)
	if err != nil {
		return notificationWrite{}, err
	}
	// The only Close error means that the connection already returned to the pool.
	// That state needs no further cleanup.
	defer func() { _ = connection.Close() }()
	if _, err := connection.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return notificationWrite{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = connection.ExecContext(ctx, "ROLLBACK")
		}
	}()
	queries := db.New(connection)
	_, completion := agent.MessageMetadata(content)
	if content.IdempotencyKey != "" {
		duplicate, err := findNotificationDuplicate(queries, agentID, provider, source, content.AgentSessionID, entry, mode)
		if err != nil || duplicate {
			return notificationWrite{}, err
		}
	}
	reference := h.notificationReference(agentID, provider, source, content, owner, standalone)
	parent, err := h.readNotificationParent(queries, agentID, provider, source, content, owner, standalone, reference)
	if err != nil {
		return notificationWrite{}, err
	}
	if !notificationParentEqual(prepared.parent, parent) {
		return notificationWrite{}, errNotificationPreparationChanged
	}
	stale := owner != nil && !owner.IsCurrent()
	lines := h.snapshotPassthroughSpanLines(agentID)
	if owner != nil {
		lines = owner.passthroughLines
	}
	write, err := h.storePreparedNotification(queries, agentID, provider, source, content, entry, completion, stale, lines, standalone, prepared)
	if err != nil || write.reference == nil {
		return notificationWrite{}, err
	}
	if _, err := connection.ExecContext(ctx, "COMMIT"); err != nil {
		return notificationWrite{}, err
	}
	committed = true
	return write, nil
}

func findNotificationDuplicate(queries *db.Queries, agentID string, provider leapmuxv1.AgentProvider, source leapmuxv1.MessageSource, sessionID string, expected agent.NotificationEntry, mode notificationWriteMode) (bool, error) {
	rows, err := queries.ListMessageSupplementsByAgentAndSession(bgCtx(), db.ListMessageSupplementsByAgentAndSessionParams{
		AgentID: agentID, AgentSessionID: sessionID,
	})
	if err != nil {
		return false, err
	}
	for _, row := range rows {
		supplement, err := msgcodec.Decompress(row.SupplementalContent, row.SupplementalContentCompression)
		if err != nil {
			return false, err
		}
		entries, err := agent.DecodeNotificationJournal(supplement)
		if err != nil {
			return false, err
		}
		for _, entry := range entries {
			if entry.IdempotencyKey != expected.IdempotencyKey {
				continue
			}
			if row.Source != source || row.AgentProvider != provider || (mode != notificationReport && entry.Fingerprint != expected.Fingerprint) {
				return false, fmt.Errorf("notification identity conflict for key %q", expected.IdempotencyKey)
			}
			return true, nil
		}
	}
	return false, nil
}

func (h *OutputHandler) storePreparedNotification(queries *db.Queries, agentID string, provider leapmuxv1.AgentProvider, source leapmuxv1.MessageSource, content agent.MessageContent, entry agent.NotificationEntry, completion leapmuxv1.MessageCompletion, stale bool, lines string, standalone bool, prepared notificationPreparation) (notificationWrite, error) {
	ctx := bgCtx()
	var previous []byte
	if prepared.parent != nil {
		var err error
		previous, err = msgcodec.Decompress(prepared.parent.SupplementalContent, prepared.parent.SupplementalContentCompression)
		if err != nil {
			return notificationWrite{}, err
		}
	}
	supplement, err := agent.AppendNotificationJournal(previous, content, entry)
	if err != nil {
		return notificationWrite{}, err
	}
	supplement, err = agent.WithNotificationReduction(supplement, prepared.state, len(prepared.wrapper.Messages))
	if err != nil {
		return notificationWrite{}, err
	}
	rendering, err := agent.ProjectMessageSupplement(supplement, len(prepared.wrapper.Messages))
	if err != nil {
		return notificationWrite{}, err
	}
	supplementalCompressed, supplementalCompression := compressOptionalSupplement(supplement)
	renderingCompressed, renderingCompression := compressOptionalSupplement(rendering)
	wrapper := *prepared.wrapper
	wrapper.OldSeqs = slices.Clone(prepared.wrapper.OldSeqs)
	merged, err := json.Marshal(wrapper)
	if err != nil {
		return notificationWrite{}, fmt.Errorf("encode the notification aggregate: %w", err)
	}
	if prepared.parent == nil {
		now := nowMillis()
		messageID := id.Generate()
		compressed, compression := msgcodec.Compress(merged)
		key := ""
		if standalone {
			key = content.IdempotencyKey
		}
		seq, err := createMessageRow(ctx, queries, db.CreateMessageParams{
			ID: messageID, AgentID: agentID, AgentSessionID: content.AgentSessionID, IdempotencyKey: key,
			Source: source, AgentProvider: provider, Content: compressed, ContentCompression: compression,
			SupplementalContent: supplementalCompressed, SupplementalContentCompression: supplementalCompression,
			Completion: workerdb.OptionalStorageEnum(completion), TranscriptOnly: stale, SpanLines: lines, CreatedAt: sqltime.NewSQLiteTime(now),
		})
		if errors.Is(err, sql.ErrNoRows) && standalone && key != "" {
			return notificationWrite{}, nil
		}
		if err != nil {
			return notificationWrite{}, err
		}
		return notificationWrite{
			reference: &notifThreadRef{msgID: messageID, seq: seq, source: source, sessionID: content.AgentSessionID, provider: provider},
			message: &leapmuxv1.AgentChatMessage{Id: messageID, Source: source, AgentSessionId: content.AgentSessionID, AgentProvider: provider,
				Content: compressed, ContentCompression: compression, SupplementalContent: renderingCompressed,
				SupplementalContentCompression: renderingCompression, Completion: completion, TranscriptOnly: stale,
				Seq: seq, SpanLines: lines, CreatedAt: timefmt.Format(now)},
		}, nil
	}
	parent := prepared.parent
	data, err := msgcodec.Decompress(parent.Content, parent.ContentCompression)
	if err != nil {
		return notificationWrite{}, err
	}
	previousWrapper, err := unwrapNotifContent(data)
	if err != nil {
		return notificationWrite{}, err
	}
	previousRendering, err := agent.ProjectMessageSupplement(previous, len(previousWrapper.Messages))
	if err != nil {
		return notificationWrite{}, err
	}
	reference := &notifThreadRef{msgID: parent.ID, seq: parent.Seq, source: source, sessionID: content.AgentSessionID, provider: provider}
	if agent.JSONCanonicalEqual(data, merged) && agent.JSONCanonicalEqual(previousRendering, rendering) && workerdb.StorageEnumValue(parent.Completion) == completion && parent.SpanLines == lines {
		if !bytes.Equal(previous, supplement) || parent.TranscriptOnly != stale {
			rows, err := queries.UpdateNotificationJournal(ctx, db.UpdateNotificationJournalParams{
				TranscriptOnly: stale, ID: parent.ID, AgentID: agentID,
				SupplementalContent: supplementalCompressed, SupplementalContentCompression: supplementalCompression,
			})
			if err != nil {
				return notificationWrite{}, err
			}
			if rows != 1 {
				return notificationWrite{}, errors.New("the notification aggregate disappeared during its identity append")
			}
		}
		return notificationWrite{reference: reference}, nil
	}
	wrapper.OldSeqs = append(wrapper.OldSeqs, parent.Seq)
	if len(wrapper.OldSeqs) > 16 {
		wrapper.OldSeqs = wrapper.OldSeqs[len(wrapper.OldSeqs)-16:]
	}
	merged, err = json.Marshal(wrapper)
	if err != nil {
		return notificationWrite{}, fmt.Errorf("encode the moved notification aggregate: %w", err)
	}
	compressed, compression := msgcodec.Compress(merged)
	prospective := *parent
	prospective.Content, prospective.ContentCompression, prospective.SpanLines = compressed, compression, lines
	prospective.SupplementalContent, prospective.SupplementalContentCompression = supplementalCompressed, supplementalCompression
	prospective.Completion, prospective.TranscriptOnly = workerdb.OptionalStorageEnum(completion), stale
	message, err := messageToProto(&prospective)
	if err != nil {
		return notificationWrite{}, err
	}
	seq, err := queries.UpdateNotificationThread(ctx, db.UpdateNotificationThreadParams{
		ID: parent.ID, AgentID: agentID, Content: compressed, ContentCompression: compression, SpanLines: lines,
		TranscriptOnly: stale, SupplementalContent: supplementalCompressed, SupplementalContentCompression: supplementalCompression,
		Completion: workerdb.OptionalStorageEnum(completion),
	})
	if err != nil {
		return notificationWrite{}, err
	}
	message.Seq, message.PreviousSeq = seq, parent.Seq
	reference.seq = seq
	return notificationWrite{reference: reference, message: message}, nil
}

func compressOptionalSupplement(supplement []byte) ([]byte, leapmuxv1.ContentCompression) {
	if len(supplement) == 0 {
		return nil, leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE
	}
	return msgcodec.Compress(supplement)
}
