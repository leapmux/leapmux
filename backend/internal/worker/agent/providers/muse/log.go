package muse

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
)

type nativeRecord struct {
	ID       string       `json:"id"`
	Sequence int64        `json:"sequence"`
	Stream   nativeStream `json:"stream"`
	Payload  struct {
		Kind  string `json:"kind"`
		RunID string `json:"run_id"`
		Event struct {
			Kind      string `json:"kind"`
			MessageID string `json:"message_id"`
			BatchID   string `json:"batch_id"`
			Calls     []*struct {
				CallID *string `json:"call_id"`
				Name   *string `json:"name"`
				Args   *string `json:"args"`
			} `json:"tool_calls"`
			Results []*struct {
				Index  *int    `json:"tool_call_index"`
				CallID *string `json:"tool_call_id"`
				Text   *string `json:"text"`
			} `json:"results"`
		} `json:"event"`
	} `json:"payload"`
}

type storedRecord struct {
	record nativeRecord
	raw    json.RawMessage
}
type nativeLog struct {
	sessionID      string
	subscriptionID int64
	records        map[string]storedRecord
	sequences      map[int64]string
	invalid        map[string]bool
	unavailable    string
	batchIndex     *nativeBatchIndex
}

func newNativeLog(id string) *nativeLog {
	return &nativeLog{sessionID: id, records: make(map[string]storedRecord), sequences: make(map[int64]string), invalid: make(map[string]bool)}
}

func decodeNativeRecord(raw json.RawMessage, sessionID string) (nativeRecord, error) {
	var record nativeRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return record, err
	}
	if record.ID == "" || record.Sequence <= 0 || record.Stream.Kind != contracts.MuseStreamKindSession || record.Stream.ID != sessionID {
		return record, fmt.Errorf("the native Muse record belongs to an invalid stream or position")
	}
	event := record.Payload.Event
	switch event.Kind {
	case contracts.MuseLogEventAssistantToolCallsCommitted:
		if record.Payload.Kind != "run" || record.Payload.RunID == "" || event.MessageID == "" || event.Calls == nil {
			return record, fmt.Errorf("the native Muse call record is invalid")
		}
		for _, call := range event.Calls {
			if call == nil || call.CallID == nil || *call.CallID == "" || call.Name == nil || *call.Name == "" || call.Args == nil {
				return record, fmt.Errorf("the native Muse call array contains an invalid entry")
			}
		}
	case contracts.MuseLogEventToolResultBatchCommitted:
		if record.Payload.Kind != "run" || record.Payload.RunID == "" || event.BatchID == "" || event.Results == nil {
			return record, fmt.Errorf("the native Muse result record is invalid")
		}
		for _, result := range event.Results {
			if result == nil || result.Index == nil || *result.Index < 0 || result.CallID == nil || *result.CallID == "" || result.Text == nil {
				return record, fmt.Errorf("the native Muse result array contains an invalid entry")
			}
		}
	}
	return record, nil
}

// add retains native bytes and rejects contradictory identities.
func (l *nativeLog) add(raw json.RawMessage) error {
	_, err := l.addRecord(raw)
	return err
}

func (l *nativeLog) addRecord(raw json.RawMessage) (nativeRecord, error) {
	record, err := decodeNativeRecord(raw, l.sessionID)
	if err != nil {
		if record.Stream.Kind == contracts.MuseStreamKindSession && record.Stream.ID == l.sessionID {
			if _, exists := l.records[record.ID]; exists {
				l.invalid[record.ID] = true
			}
		}
		return record, err
	}
	if previous, ok := l.records[record.ID]; ok {
		if !bytes.Equal(previous.raw, raw) {
			l.invalid[record.ID] = true
			return record, fmt.Errorf("the Muse host repeated a record ID with different content")
		}
		return record, nil
	}
	if id, ok := l.sequences[record.Sequence]; ok && id != record.ID {
		l.invalid[id] = true
		l.invalid[record.ID] = true
		return record, fmt.Errorf("the Muse host repeated a record sequence with another ID")
	}
	l.sequences[record.Sequence] = record.ID
	l.records[record.ID] = storedRecord{record: record, raw: slices.Clone(raw)}
	l.ensureNativeBatchIndex().add(record)
	return record, nil
}

func sameItemIdentity(left, right itemParams) bool {
	return left.SessionID == right.SessionID && left.Item.TurnID != nil && right.Item.TurnID != nil && *left.Item.TurnID == *right.Item.TurnID && left.Item.CallID == right.Item.CallID && left.SourceRange.Stream == right.SourceRange.Stream && left.SourceRange.First == right.SourceRange.First
}

type nativeResultAssociation struct {
	records []json.RawMessage
	reason  string
	pending bool
}

// match validates the exact native origin and batch.
// One native item and one native call must match the complete identity.
func (l *nativeLog) match(item itemParams, items []itemParams) nativeResultAssociation {
	if item.SessionID != l.sessionID || item.Item.TurnID == nil || item.Item.CallID == "" || item.SourceRange.Stream.Kind != contracts.MuseStreamKindSession || item.SourceRange.Stream.ID != l.sessionID || item.SourceRange.First.ID == "" || item.SourceRange.First.Sequence <= 0 {
		return nativeResultAssociation{reason: "The native item supplies no exact result identity."}
	}
	matches := 0
	for _, candidate := range items {
		if sameItemIdentity(item, candidate) {
			matches++
		}
	}
	if matches != 1 {
		return nativeResultAssociation{reason: "The native result matches more than one item."}
	}
	origin, ok := l.records[item.SourceRange.First.ID]
	if !ok {
		return nativeResultAssociation{reason: "The native origin record is unavailable.", pending: true}
	}
	if l.invalid[origin.record.ID] {
		return nativeResultAssociation{reason: "The native origin record is unavailable."}
	}
	if origin.record.Sequence != item.SourceRange.First.Sequence || origin.record.Stream != item.SourceRange.Stream {
		return nativeResultAssociation{reason: "The native origin record does not match the cited position."}
	}
	event := origin.record.Payload.Event
	if origin.record.Payload.RunID != *item.Item.TurnID || event.Kind != contracts.MuseLogEventAssistantToolCallsCommitted || event.MessageID == "" {
		return nativeResultAssociation{reason: "The native origin record does not match this turn."}
	}
	index := -1
	for candidate, call := range event.Calls {
		if *call.CallID == item.Item.CallID {
			if index != -1 || *call.Name != item.Item.Tool || *call.Args != item.Item.Args {
				return nativeResultAssociation{reason: "The native call identity is ambiguous."}
			}
			index = candidate
		}
	}
	if index < 0 {
		return nativeResultAssociation{reason: "The native origin record contains no matching call."}
	}
	// SourceRange.Last ends the item's fold. The model result batch can follow it.
	var result *storedRecord
	batchKey := nativeBatchIdentity{stream: origin.record.Stream, turnID: *item.Item.TurnID, batch: event.MessageID}
	for recordID := range l.ensureNativeBatchIndex().results[batchKey] {
		candidate := l.records[recordID]
		record := candidate.record
		if record.Payload.RunID != *item.Item.TurnID || record.Payload.Event.Kind != contracts.MuseLogEventToolResultBatchCommitted || record.Payload.Event.BatchID != event.MessageID {
			continue
		}
		if l.invalid[record.ID] || result != nil {
			return nativeResultAssociation{reason: "The native result batch is ambiguous."}
		}
		count := 0
		for _, entry := range record.Payload.Event.Results {
			if *entry.CallID == item.Item.CallID && *entry.Index == index {
				count++
			}
		}
		if count != 1 {
			return nativeResultAssociation{reason: "The native result batch contains no unique matching result."}
		}
		copy := candidate
		result = &copy
	}
	if result == nil {
		return nativeResultAssociation{reason: "The native result batch is unavailable.", pending: true}
	}
	return nativeResultAssociation{records: []json.RawMessage{slices.Clone(origin.raw), slices.Clone(result.raw)}}
}

// readLogPages reads a contiguous native prefix through the exact captured tail.
func (a *Agent) readLogPages(id string, from *nativePosition, tail nativePosition, timeout time.Duration) ([]json.RawMessage, error) {
	stream := nativeStream{Kind: contracts.MuseStreamKindSession, ID: id}
	var records []json.RawMessage
	for {
		if err := a.Context().Err(); err != nil {
			return records, err
		}
		params := map[string]any{"sessionId": id, "stream": stream, "limit": 1000}
		if from != nil {
			params["from"] = *from
		}
		raw, err := a.request(methodLogPage, params, timeout, nil)
		if err != nil {
			return records, err
		}
		var page struct {
			Records *[]json.RawMessage `json:"records"`
			Cursor  json.RawMessage    `json:"nextCursor"`
		}
		if json.Unmarshal(raw, &page) != nil || page.Records == nil || len(page.Cursor) == 0 {
			return records, fmt.Errorf("the Muse log page is invalid")
		}
		if len(*page.Records) == 0 {
			return records, fmt.Errorf("the Muse log page ended before the captured tail")
		}
		var cursor struct {
			Stream nativeStream   `json:"stream"`
			After  nativePosition `json:"after"`
		}
		if json.Unmarshal(page.Cursor, &cursor) != nil || cursor.Stream != stream || cursor.After.ID == "" || cursor.After.Sequence <= 0 {
			return records, fmt.Errorf("the Muse log page cursor is invalid")
		}
		expected := int64(1)
		if from != nil {
			expected = from.Sequence + 1
		}
		var last nativePosition
		reached := false
		for _, source := range *page.Records {
			record, err := decodeNativeRecord(source, id)
			if err != nil || record.Sequence != expected {
				return records, fmt.Errorf("the Muse log page contains an invalid record or a sequence gap")
			}
			last = nativePosition{ID: record.ID, Sequence: record.Sequence}
			if record.Sequence == tail.Sequence {
				if record.ID != tail.ID {
					return records, fmt.Errorf("the Muse log page conflicts with the captured tail")
				}
				reached = true
			}
			records = append(records, slices.Clone(source))
			expected++
		}
		if cursor.After != last {
			return records, fmt.Errorf("the Muse log page cursor differs from its last record")
		}
		if reached {
			return records, nil
		}
		if last.Sequence >= tail.Sequence {
			return records, fmt.Errorf("the Muse log page passed an absent captured tail")
		}
		from = &last
	}
}
