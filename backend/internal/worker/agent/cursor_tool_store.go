package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

type cursorBlobReference struct {
	id       string
	position int
}

type cursorToolRecord struct {
	arguments json.RawMessage
	result    map[string]json.RawMessage
	content   json.RawMessage
}

type cursorToolContent struct {
	Type       string          `json:"type"`
	ToolCallID string          `json:"toolCallId"`
	Args       json.RawMessage `json:"args"`
	raw        json.RawMessage
}

// cursorToolStore maps tool calls to the content hashes in one Cursor session.
// It reads record identifiers on each scan and decodes only records with new hashes.
// Cursor can delete and replace blobs, so a rowid alone cannot track new records.
type cursorToolStore struct {
	mu       sync.Mutex
	path     string
	file     os.FileInfo
	seen     map[string]struct{}
	requests map[string]cursorBlobReference
	results  map[string]cursorBlobReference
}

func cursorACPStorePath(sessionID string) string {
	if sessionID == "" || sessionID == "." || sessionID == ".." || filepath.Base(sessionID) != sessionID {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".cursor", "acp-sessions", sessionID, cursorStoreFileName)
}

func (s *cursorToolStore) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resetLocked()
}

func (s *cursorToolStore) resetLocked() {
	s.path = ""
	s.file = nil
	s.seen = nil
	s.requests = nil
	s.results = nil
}

// read returns only the requested tool records from a consistent database snapshot.
func (s *cursorToolStore) read(ctx context.Context, path string, toolIDs []string) (map[string]cursorToolRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if path == "" {
		return nil, nil
	}
	file, err := os.Stat(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if s.path != path || s.file == nil || !os.SameFile(s.file, file) {
		s.resetLocked()
		s.path, s.file = path, file
		s.requests = make(map[string]cursorBlobReference)
		s.results = make(map[string]cursorBlobReference)
	}
	db, err := openSessionStoreDB(ctx, path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.scan(ctx, tx); err != nil {
		return nil, err
	}
	found := make(map[string]cursorToolRecord)
	for _, toolID := range toolIDs {
		resultRef, exists := s.results[toolID]
		if !exists {
			continue
		}
		result, blocks, err := readCursorBlob(ctx, tx, resultRef.id)
		if err != nil {
			return nil, err
		}
		if resultRef.position >= len(blocks) || blocks[resultRef.position].ToolCallID != toolID {
			continue
		}
		record := cursorToolRecord{result: result, content: blocks[resultRef.position].raw}
		if requestRef, exists := s.requests[toolID]; exists {
			_, requests, err := readCursorBlob(ctx, tx, requestRef.id)
			if err != nil {
				return nil, err
			}
			if requestRef.position < len(requests) && requests[requestRef.position].ToolCallID == toolID {
				record.arguments = requests[requestRef.position].Args
			}
		}
		found[toolID] = record
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return found, nil
}

func (s *cursorToolStore) scan(ctx context.Context, tx *sql.Tx) error {
	// SQLite can serve these two fields from its primary-key index without reading blob data.
	rows, err := tx.QueryContext(ctx, `SELECT rowid, id FROM blobs`)
	if err != nil {
		return err
	}
	type newBlob struct {
		rowID int64
		id    string
	}
	var added []newBlob
	seen := make(map[string]struct{}, len(s.seen))
	for rows.Next() {
		var blob newBlob
		if err := rows.Scan(&blob.rowID, &blob.id); err != nil {
			_ = rows.Close()
			return err
		}
		seen[blob.id] = struct{}{}
		if _, exists := s.seen[blob.id]; !exists {
			added = append(added, blob)
		}
	}
	err = rows.Err()
	closeErr := rows.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	sort.Slice(added, func(i, j int) bool { return added[i].rowID < added[j].rowID })
	for _, blob := range added {
		_, content, err := readCursorBlob(ctx, tx, blob.id)
		if err != nil {
			return err
		}
		for index, block := range content {
			if block.ToolCallID == "" {
				continue
			}
			switch block.Type {
			case "tool-call":
				s.requests[block.ToolCallID] = cursorBlobReference{id: blob.id, position: index}
			case "tool-result":
				s.results[block.ToolCallID] = cursorBlobReference{id: blob.id, position: index}
			}
		}
	}
	for _, index := range []map[string]cursorBlobReference{s.requests, s.results} {
		for toolID, ref := range index {
			if _, exists := seen[ref.id]; !exists {
				delete(index, toolID)
			}
		}
	}
	s.seen = seen
	return nil
}

func readCursorBlob(ctx context.Context, tx *sql.Tx, id string) (map[string]json.RawMessage, []cursorToolContent, error) {
	var data []byte
	if err := tx.QueryRowContext(ctx, `SELECT data FROM blobs WHERE id = ?`, id).Scan(&data); err != nil {
		return nil, nil, err
	}
	var record map[string]json.RawMessage
	if json.Unmarshal(data, &record) != nil {
		return nil, nil, nil
	}
	var role string
	if json.Unmarshal(record["role"], &role) != nil || (role != "assistant" && role != "tool") {
		return nil, nil, nil
	}
	var rawContent []json.RawMessage
	if json.Unmarshal(record["content"], &rawContent) != nil {
		return nil, nil, nil
	}
	content := make([]cursorToolContent, len(rawContent))
	for index, raw := range rawContent {
		var block cursorToolContent
		if json.Unmarshal(raw, &block) != nil {
			continue
		}
		block.raw = raw
		content[index] = block
	}
	return record, content, nil
}
