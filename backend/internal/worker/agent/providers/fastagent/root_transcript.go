package fastagent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"slices"
	"sort"
	"sync"

	"github.com/google/uuid"
	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
)

type rootRecordKind uint8

const (
	rootRecordMessage rootRecordKind = iota
	rootRecordNotification
	rootRecordWorkerNotification
	rootRecordTurnEnd
)

// Planned records retain immutable native bytes. One scope captures the selected
// representation in local order after every admission has completed.
type rootRecord struct {
	kind    rootRecordKind
	source  leapmuxv1.MessageSource
	content agent.MessageContent
	span    agent.SpanInfo
}
type rootCapturePosition struct {
	agent.TranscriptServices
	group *rootTranscriptGroup
	root  *rootTranscript
	order uint64
	rows  int
	ready bool
	once  sync.Once
}

type rootJournalEntry struct {
	order    uint64
	row      int
	root     *rootTranscript
	kind     rootRecordKind
	file     string
	receipt  *agent.TranscriptWriteReceipt
	revision int64
	memory   *rootRecord
}

type rootTranscriptGroup struct {
	mu                      sync.Mutex
	owner                   *Agent
	delegate                agent.ProviderServices
	batch                   *agent.CapturedTranscriptBatch
	start                   acp.RootTranscriptStart
	fresh                   bool
	logical                 int
	hasKeptTurn             bool
	eligible, sealed        bool
	overlap                 bool
	epoch                   uint64
	nextPosition            uint64
	openPositions           int
	maxRecords              int
	entries                 []*rootJournalEntry
	journal                 rootJournalStore
	fatalErr                error
	preparingBytes          int
	abortOnce               sync.Once
	pending                 map[*agent.TranscriptWriteReceipt]*rootJournalEntry
	prepare                 sync.Once
	prepareErr, baselineErr error
	scope                   agent.TranscriptScope
	anchor                  agent.CapturedTranscript
	before                  *fastagentRootReceipt
	captureID               string
	inputPrepared           bool
	inputDigest             [32]byte
	inputErr                error
}
type rootTranscript struct {
	group   *rootTranscriptGroup
	once    sync.Once
	discard bool
}

func (a *Agent) openRootTranscript(start acp.RootTranscriptStart, delegate agent.ProviderServices) acp.RootTranscript {
	if !a.nativeCommands {
		return nil
	}
	if previous, ok := start.Preceding.(*rootTranscript); ok {
		group := previous.group
		group.mu.Lock()
		if !group.sealed {
			group.logical++
			group.overlap = true
			group.eligible = false
			group.mu.Unlock()
			return &rootTranscript{group: group}
		}
		group.mu.Unlock()
	}
	batch, err := agent.ReserveCapturedTranscriptBatch(delegate)
	if err != nil {
		panic(fmt.Sprintf("reserve the Fast Agent root transcript: %v", err))
	}
	a.rootMu.Lock()
	fresh := a.rootFresh && (a.rootSession == "" || a.rootSession == start.SessionID)
	a.rootSession = start.SessionID
	a.rootEpoch++
	epoch := a.rootEpoch
	a.rootFresh = false
	a.rootMu.Unlock()
	group := &rootTranscriptGroup{owner: a, delegate: delegate, batch: batch, start: start, fresh: fresh, logical: 1, maxRecords: fastagentRootElementLimit, epoch: epoch, eligible: !start.AgentInitiated, pending: make(map[*agent.TranscriptWriteReceipt]*rootJournalEntry)}
	return &rootTranscript{group: group}
}

func (root *rootTranscript) Prepare() error {
	group := root.group
	group.prepare.Do(func() {
		group.captureID = uuid.NewString()
		group.anchor = agent.CaptureTranscript(group.delegate, agent.MessageContent{AgentSessionID: group.start.SessionID}, agent.SpanInfo{})
		group.scope, group.prepareErr = group.delegate.TranscriptScopeFor(group.anchor)
		if group.prepareErr != nil {
			return
		}
		journal, journalErr := newRootJournal(group.owner.home, group.start.SessionID, group.owner.AgentID(), group.captureID)
		if journalErr != nil {
			group.prepareErr = journalErr
			return
		}
		group.mu.Lock()
		group.journal = journal
		group.mu.Unlock()
		group.prepareErr = group.migratePreparingEntries(journal)
		if group.prepareErr != nil {
			return
		}
		group.mu.Lock()
		eligible := group.eligible
		group.mu.Unlock()
		if !eligible {
			return
		}
		archive, err := readFastagentRootArchive(group.owner.home, group.start.SessionID, group.owner.workingDir)
		if group.fresh && errors.Is(err, os.ErrNotExist) {
			group.before, group.baselineErr = freshFastagentRootReceipt(group.start.SessionID, group.start.NativeModeID)
		} else if err != nil {
			group.baselineErr = err
		} else {
			group.before, group.baselineErr = captureFastagentRootReceipt(archive)
		}
	})
	return group.prepareErr
}

// Capture can reenter before preparation creates its journal. Move the accepted
// prefix to disk without holding the allocation-only mutex across I/O.
func (group *rootTranscriptGroup) migratePreparingEntries(journal rootJournalStore) error {
	group.mu.Lock()
	pending := slices.Clone(group.entries)
	group.mu.Unlock()
	for _, entry := range pending {
		group.mu.Lock()
		memory := entry.memory
		group.mu.Unlock()
		if memory == nil {
			continue
		}
		file, err := journal.Write(memory)
		if err != nil {
			group.abort(err)
			return err
		}
		group.mu.Lock()
		if entry.memory == memory {
			entry.file = file
			entry.memory = nil
			group.preparingBytes -= len(memory.content.Original) + len(memory.content.Supplemental) + len(memory.content.Metadata)
		}
		group.mu.Unlock()
	}
	return nil
}

// PrepareInput records the final packet after every ACP preparation hook.
func (root *rootTranscript) PrepareInput(blocks []json.RawMessage) error {
	digest, err := fastagentInputDigest(blocks, true)
	group := root.group
	group.mu.Lock()
	defer group.mu.Unlock()
	if group.sealed {
		return errors.New("the Fast Agent root transcript is sealed")
	}
	if group.inputPrepared {
		if group.inputErr != nil || err != nil || group.inputDigest != digest {
			return errors.New("the Fast Agent dispatch input changed")
		}
		return nil
	}
	group.inputPrepared = true
	group.inputDigest = digest
	group.inputErr = err
	return nil
}

func (root *rootTranscript) Reserve() acp.RootTranscriptCapture {
	group := root.group
	group.mu.Lock()
	defer group.mu.Unlock()
	if group.sealed {
		panic("Fast Agent reserved a sealed root transcript")
	}
	position := &rootCapturePosition{TranscriptServices: group.delegate, group: group, root: root, order: group.nextPosition}
	group.nextPosition++
	group.openPositions++
	return position
}
func (position *rootCapturePosition) Writer() agent.TranscriptServices { return position }
func (position *rootCapturePosition) Complete() {
	position.once.Do(func() {
		position.group.mu.Lock()
		position.ready = true
		position.group.openPositions--
		position.group.mu.Unlock()
	})
}
func (position *rootCapturePosition) OutputQueue() *agent.OutputQueue {
	return agent.OutputQueueFor(position.group.delegate)
}

func (position *rootCapturePosition) append(kind rootRecordKind, source leapmuxv1.MessageSource, content agent.MessageContent, span agent.SpanInfo) error {
	if source == leapmuxv1.MessageSource_MESSAGE_SOURCE_UNSPECIFIED {
		return errors.New("the Fast Agent row has no message source")
	}
	if _, known := leapmuxv1.MessageSource_name[int32(source)]; !known {
		return errors.New("the Fast Agent row has an unknown message source")
	}
	if err := agent.ValidateMessageCompletion(content); err != nil {
		return err
	}
	if _, err := agent.EncodeMessageSupplement(content); err != nil {
		return err
	}
	group := position.group
	group.mu.Lock()
	if group.sealed || position.ready {
		group.mu.Unlock()
		return errors.New("the Fast Agent transcript position is closed")
	}
	if group.fatalErr != nil {
		err := group.fatalErr
		group.mu.Unlock()
		return err
	}
	if content.AgentSessionID != "" && content.AgentSessionID != group.start.SessionID {
		group.mu.Unlock()
		return errors.New("the Fast Agent row belongs to another session")
	}
	if content.Publication != nil {
		group.mu.Unlock()
		return errors.New("the Fast Agent local row was already captured")
	}
	if len(group.entries) >= group.maxRecords {
		group.mu.Unlock()
		err := errors.New("the Fast Agent root transcript exceeds its record limit")
		group.abort(err)
		return err
	}
	journal := group.journal
	if journal == nil && len(content.Original)+len(content.Supplemental)+len(content.Metadata) > 8<<20-group.preparingBytes {
		group.mu.Unlock()
		err := errors.New("the Fast Agent preparation buffer exceeds its memory limit")
		group.abort(err)
		return err
	}
	if journal == nil {
		group.preparingBytes += len(content.Original) + len(content.Supplemental) + len(content.Metadata)
	}
	entry := &rootJournalEntry{order: position.order, row: position.rows, root: position.root, kind: kind, receipt: content.WriteReceipt}
	position.rows++
	group.entries = append(group.entries, entry)
	if kind != rootRecordMessage && kind != rootRecordTurnEnd {
		group.eligible = false
	}
	group.mu.Unlock()
	record := &rootRecord{kind: kind, source: source, content: content.Clone(), span: span}
	if journal == nil {
		group.mu.Lock()
		entry.memory = record
		if kind == rootRecordMessage && span.SpanID != "" && !span.Closing && content.WriteReceipt != nil {
			group.pending[content.WriteReceipt] = entry
		}
		group.mu.Unlock()
		return nil
	}
	file, err := journal.Write(record)
	group.mu.Lock()
	if err != nil {
		entry.memory = record
		group.preparingBytes += len(record.content.Original) + len(record.content.Supplemental) + len(record.content.Metadata)
	} else {
		entry.file = file
	}
	if kind == rootRecordMessage && span.SpanID != "" && !span.Closing && content.WriteReceipt != nil {
		group.pending[content.WriteReceipt] = entry
	}
	group.mu.Unlock()
	if err != nil {
		// Retain this accepted row in memory, stop further production, and keep
		// every earlier private record for recovery. Do not continue dropping rows.
		group.abort(fmt.Errorf("retain the Fast Agent root transcript: %w", err))
	}
	return nil
}
func (position *rootCapturePosition) PersistMessage(source leapmuxv1.MessageSource, content agent.MessageContent, span agent.SpanInfo) error {
	return position.append(rootRecordMessage, source, content, span)
}
func (position *rootCapturePosition) PersistPreparedMessage(source leapmuxv1.MessageSource, template agent.MessageContent, span agent.SpanInfo, prepare func() []byte) error {
	if prepare == nil {
		return errors.New("the Fast Agent row has no preparation function")
	}
	template = template.Clone()
	selected := prepare()
	if selected == nil {
		return nil
	}
	template.Original = slices.Clone(selected)
	return position.append(rootRecordMessage, source, template, span)
}
func (position *rootCapturePosition) PersistNotification(source leapmuxv1.MessageSource, content agent.MessageContent) (bool, error) {
	return false, position.append(rootRecordNotification, source, content, agent.SpanInfo{})
}
func (position *rootCapturePosition) PersistWorkerNotification(content agent.MessageContent) (bool, error) {
	return false, position.append(rootRecordWorkerNotification, leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX, content, agent.SpanInfo{})
}
func (position *rootCapturePosition) PersistTurnEnd(content agent.MessageContent, span agent.SpanInfo) error {
	return position.append(rootRecordTurnEnd, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content, span)
}

func (position *rootCapturePosition) ReadPendingToolRequest(receipt *agent.TranscriptWriteReceipt) (acp.PendingToolRequest, bool, error) {
	group := position.group
	group.mu.Lock()
	entry := group.pending[receipt]
	if group.sealed || entry == nil {
		group.mu.Unlock()
		return acp.PendingToolRequest{}, false, nil
	}
	snapshot := *entry
	group.mu.Unlock()
	record, err := group.readEntry(&snapshot)
	if err != nil {
		return acp.PendingToolRequest{}, false, err
	}
	return acp.PendingToolRequest{Content: record.content.Clone(), Revision: snapshot.revision}, true, nil
}
func (position *rootCapturePosition) EnrichPendingToolRequest(receipt *agent.TranscriptWriteReceipt, revision int64, original, supplemental []byte) (bool, error) {
	group := position.group
	group.mu.Lock()
	entry := group.pending[receipt]
	if group.sealed || entry == nil || entry.revision != revision || revision == math.MaxInt64 {
		group.mu.Unlock()
		return false, nil
	}
	snapshot := *entry
	group.mu.Unlock()
	record, err := group.readEntry(&snapshot)
	if err != nil {
		return false, err
	}
	if !bytes.Equal(record.content.Original, original) || bytes.Equal(record.content.Supplemental, supplemental) {
		return false, nil
	}
	record.content.Supplemental = slices.Clone(supplemental)
	if _, err := agent.EncodeMessageSupplement(record.content); err != nil {
		return false, err
	}
	group.mu.Lock()
	if group.sealed || group.pending[receipt] != entry || entry.revision != revision {
		group.mu.Unlock()
		return false, nil
	}
	journal := group.journal
	if journal == nil {
		oldSize := 0
		if entry.memory != nil {
			oldSize = len(entry.memory.content.Original) + len(entry.memory.content.Supplemental) + len(entry.memory.content.Metadata)
		}
		newSize := len(record.content.Original) + len(record.content.Supplemental) + len(record.content.Metadata)
		if newSize > 8<<20-(group.preparingBytes-oldSize) {
			group.mu.Unlock()
			return false, errors.New("the Fast Agent preparation buffer exceeds its memory limit")
		}
		group.preparingBytes += newSize - oldSize
		entry.memory = record
		entry.revision++
		group.mu.Unlock()
		return true, nil
	}
	group.mu.Unlock()
	file, err := journal.Write(record)
	if err != nil {
		group.abort(err)
		return false, err
	}
	group.mu.Lock()
	defer group.mu.Unlock()
	if group.sealed || group.pending[receipt] != entry || entry.revision != revision {
		return false, nil
	}
	if entry.memory != nil {
		group.preparingBytes -= len(entry.memory.content.Original) + len(entry.memory.content.Supplemental) + len(entry.memory.content.Metadata)
	}
	entry.file = file
	entry.memory = nil
	entry.revision++
	return true, nil
}

func (root *rootTranscript) Finish(completion acp.RootTranscriptCompletion) error {
	var finishErr error
	root.once.Do(func() {
		group := root.group
		group.mu.Lock()
		root.discard = completion.Discard
		group.hasKeptTurn = group.hasKeptTurn || !completion.Discard
		if completion.Discard || completion.Reason != acp.RootTranscriptPromptEnded || (completion.Completion != agent.MessageCompletionComplete && completion.Completion != agent.MessageCompletionInterrupted) {
			group.eligible = false
		}
		if completion.Reason == acp.RootTranscriptPromptNotSent && group.fresh && !group.overlap {
			group.owner.rootMu.Lock()
			if group.owner.rootSession == group.start.SessionID && group.owner.rootEpoch == group.epoch {
				group.owner.rootFresh = true
			}
			group.owner.rootMu.Unlock()
		}
		group.logical--
		if group.logical > 0 {
			group.mu.Unlock()
			return
		}
		if group.openPositions != 0 {
			group.mu.Unlock()
			panic("Fast Agent sealed an unfinished transcript position")
		}
		group.sealed = true
		entries := slices.Clone(group.entries)
		sort.Slice(entries, func(i, j int) bool {
			if entries[i].order != entries[j].order {
				return entries[i].order < entries[j].order
			}
			return entries[i].row < entries[j].row
		})
		entries = slices.DeleteFunc(entries, func(entry *rootJournalEntry) bool { return entry.root.discard })
		eligible := group.eligible && group.fatalErr == nil && group.prepareErr == nil
		if eligible && (!group.inputPrepared || group.inputErr != nil) {
			if group.inputErr == nil {
				group.inputErr = errors.New("the native dispatch input could not be proved")
			}
			eligible = false
		}
		group.pending = nil
		if group.prepareErr != nil && len(entries) == 0 {
			group.mu.Unlock()
			group.batch.Discard()
			return
		}
		group.mu.Unlock()
		group.owner.rootMu.Lock()
		if group.owner.rootClosing {
			group.owner.rootMu.Unlock()
			finishErr = errors.New("the Fast Agent archive worker is closed")
			return
		}
		group.owner.rootArchiveWG.Add(1)
		group.owner.rootMu.Unlock()
		go func() {
			defer group.owner.rootArchiveWG.Done()
			group.publish(entries, eligible, completion.Completion)
		}()
	})
	return finishErr
}

func (group *rootTranscriptGroup) publish(live []*rootJournalEntry, eligible bool, completion agent.MessageCompletion) {
	selected := live
	var archiveErr error
	if eligible {
		archiveErr = group.baselineErr
		if archiveErr == nil {
			archive, err := readFastagentRootArchive(group.owner.home, group.start.SessionID, group.owner.workingDir)
			archiveErr = err
			if archiveErr == nil {
				projection, err := projectFastagentRootArchive(group.before, archive, group.captureID, group.inputDigest, completion)
				archiveErr = err
				if archiveErr == nil && completion == agent.MessageCompletionInterrupted {
					archiveErr = group.validateInterruptedModelCoverage(live, projection)
				}
				if archiveErr == nil {
					candidate := make([]*rootJournalEntry, 0, len(projection.rows)+1)
					for _, row := range projection.rows {
						record := &rootRecord{kind: rootRecordMessage, source: row.source, content: row.content, span: row.span}
						file, err := group.journal.Write(record)
						if err != nil {
							archiveErr = err
							break
						}
						candidate = append(candidate, &rootJournalEntry{kind: rootRecordMessage, file: file})
					}
					if archiveErr == nil {
						for _, entry := range live {
							if entry.kind == rootRecordTurnEnd {
								candidate = append(candidate, entry)
							}
						}
						selected = candidate
					}
				}
			}
		}
	}
	reason := archiveErr
	group.mu.Lock()
	if reason == nil {
		reason = group.inputErr
	}
	if reason == nil {
		reason = group.prepareErr
	}
	fatal := group.fatalErr
	kept := group.hasKeptTurn
	group.mu.Unlock()
	if fatal != nil {
		reason = fatal
	}
	if reason != nil && kept {
		noticeType := contracts.NotificationTypeAgentStatus
		text := "Fast Agent used its complete live transcript because the native archive could not be verified: " + reason.Error()
		if fatal != nil {
			noticeType = contracts.NotificationTypeAgentError
			text = "Fast Agent stopped because its transcript buffer failed: " + reason.Error()
			if group.journal != nil {
				text += ". Recovery files: " + group.journal.Path()
			} else {
				text += ". Accepted output remains in the worker output queue."
			}
		}
		raw, err := json.Marshal(map[string]string{contracts.NotificationFieldType: noticeType, contracts.NotificationFieldText: text})
		if err == nil {
			record := &rootRecord{kind: rootRecordWorkerNotification, source: leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX, content: agent.MessageContent{Original: raw}}
			var file string
			writeErr := errors.New("the Fast Agent transcript journal is unavailable")
			if group.journal != nil {
				file, writeErr = group.journal.Write(record)
			}
			entry := &rootJournalEntry{kind: rootRecordWorkerNotification, file: file}
			if writeErr != nil {
				entry.memory = record
			}
			index := len(selected)
			for i := len(selected) - 1; i >= 0; i-- {
				if selected[i].kind == rootRecordTurnEnd {
					index = i
					break
				}
			}
			selected = slices.Insert(slices.Clone(selected), index, entry)
		}
	}
	stream := &rootTranscriptStream{group: group, entries: selected}
	if err := group.batch.PrepareStream(stream.next); err != nil && !agent.OutputRetained(err) {
		directory := ""
		if group.journal != nil {
			directory = group.journal.Path()
		}
		slog.Error("publish the Fast Agent root transcript", "session_id", group.start.SessionID, "recovery_directory", directory, "error", err)
	}
}

func (group *rootTranscriptGroup) readEntry(entry *rootJournalEntry) (*rootRecord, error) {
	var record *rootRecord
	var err error
	if entry.memory != nil {
		copy := *entry.memory
		copy.content = copy.content.Clone()
		record = &copy
	} else {
		if group.journal == nil {
			return nil, errors.New("the Fast Agent transcript record has no retained data")
		}
		record, err = group.journal.Read(entry.file)
	}
	if err != nil {
		return nil, err
	}
	record.content.WriteReceipt = entry.receipt
	return record, nil
}

func (group *rootTranscriptGroup) abort(err error) {
	group.mu.Lock()
	if group.fatalErr == nil {
		group.fatalErr = err
	}
	group.eligible = false
	group.mu.Unlock()
	group.abortOnce.Do(func() {
		slog.Error("stop Fast Agent after its transcript buffer failed", "session_id", group.start.SessionID, "error", err)
		group.owner.rootFailureWork.Run(group.captureID, func() { group.owner.Stop() })
	})
}

func (group *rootTranscriptGroup) captureOne(record *rootRecord) (agent.CapturedTranscriptOperation, error) {
	if group.scope == nil {
		var err error
		group.scope, err = group.delegate.TranscriptScopeFor(group.anchor)
		if err != nil {
			return agent.CapturedTranscriptOperation{}, err
		}
	}
	scope := group.scope
	span := record.span
	opening := record.kind == rootRecordMessage && span.SpanID != "" && !span.Closing
	if opening && !span.NoSpan {
		span.SpanColor = scope.ReserveSpanColor(span.SpanID, span.ParentSpanID)
	}
	captured, err := scope.CaptureTranscript(record.content, span)
	if err != nil {
		return agent.CapturedTranscriptOperation{}, err
	}
	var operation agent.CapturedTranscriptOperation
	switch record.kind {
	case rootRecordMessage:
		operation = captured.MessageOperation(record.source)
	case rootRecordNotification:
		operation = captured.NotificationOperation(record.source)
	case rootRecordWorkerNotification:
		operation = captured.WorkerNotificationOperation()
	case rootRecordTurnEnd:
		operation = captured.TurnEndOperation()
	default:
		return agent.CapturedTranscriptOperation{}, errors.New("the Fast Agent buffered operation is invalid")
	}
	if opening {
		scope.SetSpanType(span.SpanID, span.SpanType)
		if !span.NoSpan {
			scope.OpenSpan(span.SpanID, span.ParentSpanID)
		}
	}
	if span.Closing {
		scope.CloseSpan(span.SpanID)
	}
	if record.kind == rootRecordTurnEnd {
		scope.ResetSpans()
	}
	return operation, nil
}

type rootTranscriptStream struct {
	group   *rootTranscriptGroup
	entries []*rootJournalEntry
	index   int
	current *rootRecord
}

func (stream *rootTranscriptStream) next() (agent.CapturedTranscriptOperation, bool, error) {
	if stream.index == len(stream.entries) {
		if stream.group.journal != nil {
			stream.group.mu.Lock()
			failed := stream.group.fatalErr != nil
			stream.group.mu.Unlock()
			var err error
			if failed {
				err = stream.group.journal.Retain()
			} else {
				err = stream.group.journal.Cleanup()
			}
			if err != nil {
				return agent.CapturedTranscriptOperation{}, false, err
			}
		}
		stream.group.mu.Lock()
		stream.group.entries = nil
		stream.group.mu.Unlock()
		stream.entries = nil
		return agent.CapturedTranscriptOperation{}, true, nil
	}
	if stream.current == nil {
		record, err := stream.group.readEntry(stream.entries[stream.index])
		if err != nil {
			return agent.CapturedTranscriptOperation{}, false, err
		}
		stream.current = record
	}
	operation, err := stream.group.captureOne(stream.current)
	if err != nil {
		return agent.CapturedTranscriptOperation{}, false, err
	}
	stream.current = nil
	stream.index++
	return operation, false, nil
}
