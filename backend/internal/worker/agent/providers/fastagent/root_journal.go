package fastagent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// One row can contain native bytes, a recovered supplement, and worker metadata.
// The native archive limit applies separately to each of those collections.
const rootJournalRowByteLimit = 3 * fastagentRootHistoryLimit
const rootJournalEncodedByteLimit = 2 * rootJournalRowByteLimit

type rootJournalStore interface {
	Write(*rootRecord) (string, error)
	Read(string) (*rootRecord, error)
	Cleanup() error
	Retain() error
	Path() string
}

type rootJournal struct {
	mu     sync.Mutex
	root   *os.Root
	path   string
	next   uint64
	closed bool
}

type rootJournalDocument struct {
	Kind    rootRecordKind
	Source  int32
	Content agent.MessageContent
	Span    agent.SpanInfo
}

func newRootJournal(home, sessionID, agentID, captureID string) (rootJournalStore, error) {
	if home == "" {
		return nil, errors.New("the Fast Agent buffer has no storage directory")
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return nil, err
	}
	path, err := os.MkdirTemp(home, ".leapmux-root-turn-")
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, errors.Join(err, os.Remove(path))
	}
	journal := &rootJournal{root: root, path: path}
	manifest, err := root.OpenFile("manifest.json", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err == nil {
		err = json.NewEncoder(manifest).Encode(map[string]string{"format": "leapmux/fastagent-root-turn/v1", "sessionId": sessionID, "agentId": agentID, "captureId": captureID})
		err = errors.Join(err, manifest.Sync(), manifest.Close())
	}
	if err != nil {
		return nil, errors.Join(err, journal.Cleanup())
	}
	return journal, nil
}

func (journal *rootJournal) Path() string { return journal.path }

// Retain closes the handle while keeping accepted recovery files.
func (journal *rootJournal) Retain() error {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	if journal.closed {
		return nil
	}
	journal.closed = true
	return journal.root.Close()
}

// Write returns only after the immutable private record reaches its file.
// A new file preserves earlier records if this write fails partway through.
func (journal *rootJournal) Write(record *rootRecord) (string, error) {
	if len(record.content.Original)+len(record.content.Supplemental)+len(record.content.Metadata) > rootJournalRowByteLimit {
		return "", errors.New("the Fast Agent buffered row exceeds its size limit")
	}
	content := record.content
	if content.Publication != nil {
		return "", errors.New("the Fast Agent buffer cannot serialize captured authority")
	}
	content.WriteReceipt = nil
	journal.mu.Lock()
	defer journal.mu.Unlock()
	if journal.closed {
		return "", errors.New("the Fast Agent buffer is closed")
	}
	name := fmt.Sprintf("%020d.json", journal.next)
	journal.next++
	file, err := journal.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	err = json.NewEncoder(file).Encode(rootJournalDocument{Kind: record.kind, Source: int32(record.source), Content: content, Span: record.span})
	err = errors.Join(err, file.Sync(), file.Close())
	if err != nil {
		return "", err
	}
	return name, nil
}

func (journal *rootJournal) Read(name string) (*rootRecord, error) {
	raw, err := readFastagentRegularFile(journal.root, name, rootJournalEncodedByteLimit)
	if err != nil {
		return nil, err
	}
	var document rootJournalDocument
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, errors.New("the Fast Agent buffer contains trailing data")
	}
	if document.Kind > rootRecordTurnEnd {
		return nil, errors.New("the Fast Agent buffered operation is invalid")
	}
	source := leapmuxv1.MessageSource(document.Source)
	if _, known := leapmuxv1.MessageSource_name[document.Source]; !known || source == leapmuxv1.MessageSource_MESSAGE_SOURCE_UNSPECIFIED {
		return nil, errors.New("the Fast Agent buffered source is invalid")
	}
	if document.Kind == rootRecordWorkerNotification && source != leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX {
		return nil, errors.New("the Fast Agent worker notification has an invalid source")
	}
	if document.Kind == rootRecordTurnEnd && source != leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT {
		return nil, errors.New("the Fast Agent divider has an invalid source")
	}
	content := document.Content
	if content.Publication != nil || content.WriteReceipt != nil {
		return nil, errors.New("the Fast Agent buffer contains captured authority")
	}
	if len(content.Original)+len(content.Supplemental)+len(content.Metadata) > rootJournalRowByteLimit {
		return nil, errors.New("the Fast Agent buffered row exceeds its size limit")
	}
	return &rootRecord{kind: document.Kind, source: leapmuxv1.MessageSource(document.Source), content: content, span: document.Span}, nil
}

// Cleanup runs only after every selected row persisted. A failure leaves the
// journal owned by the queue, so its next retry can finish cleanup.
func (journal *rootJournal) Cleanup() error {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	if journal.closed {
		if err := os.Remove(journal.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	directory, err := journal.root.Open(".")
	if err != nil {
		return err
	}
	for {
		entries, readErr := directory.ReadDir(100)
		for _, entry := range entries {
			if entry.IsDir() {
				err = errors.New("the Fast Agent buffer contains an unexpected directory")
				break
			}
			if removeErr := journal.root.Remove(entry.Name()); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				err = removeErr
				break
			}
		}
		if err != nil || readErr != nil {
			if readErr != nil && !errors.Is(readErr, io.EOF) {
				err = errors.Join(err, readErr)
			}
			break
		}
	}
	err = errors.Join(err, directory.Close())
	if err != nil {
		return err
	}
	journal.closed = true
	if err := journal.root.Close(); err != nil {
		return err
	}
	if err := os.Remove(journal.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
