package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	piGoalFileByteLimit          = 16 * 1024 * 1024
	piGoalSessionRecordByteLimit = 64 * 1024 * 1024
)

type piGoalFocus struct {
	Version       int     `json:"version"`
	FocusedGoalID *string `json:"focusedGoalId"`
}

type piGoalSessionEntry struct {
	ParentID string
	Focus    *piGoalFocus
}

// piGoalSession keeps only the parent links and focus records needed for the current branch.
type piGoalSession struct {
	Entries map[string]piGoalSessionEntry
	LastID  string
}

// piGoalSessionReader indexes an append-only native session. A replacement discards the index.
type piGoalSessionReader struct {
	path       string
	workingDir string
	sessionID  string
	info       os.FileInfo
	offset     int64
	session    piGoalSession
}

func (session *piGoalSession) add(raw json.RawMessage) error {
	var entry struct {
		ID         string          `json:"id"`
		ParentID   string          `json:"parentId"`
		Type       string          `json:"type"`
		CustomType string          `json:"customType"`
		Data       json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &entry); err != nil {
		return fmt.Errorf("read Pi session entry: %w", err)
	}
	if entry.ID == "" {
		return fmt.Errorf("the Pi session entry has no ID")
	}
	if _, duplicate := session.Entries[entry.ID]; duplicate {
		return fmt.Errorf("the Pi session repeats an entry ID")
	}
	value := piGoalSessionEntry{ParentID: entry.ParentID}
	if entry.Type == "custom" && entry.CustomType == "pi-goal-focus" {
		var fields map[string]json.RawMessage
		var focus piGoalFocus
		if json.Unmarshal(entry.Data, &focus) == nil && focus.Version == 1 && json.Unmarshal(entry.Data, &fields) == nil {
			if _, present := fields["focusedGoalId"]; present {
				value.Focus = &focus
			}
		}
	}
	if session.Entries == nil {
		session.Entries = make(map[string]piGoalSessionEntry)
	}
	session.Entries[entry.ID] = value
	session.LastID = entry.ID
	return nil
}

// focus follows the provider's active branch. The last file entry can belong to another branch.
func (session piGoalSession) focus(leafID string, updates map[string]piGoalSessionEntry) (string, bool) {
	visited := make(map[string]struct{})
	for current := leafID; current != ""; {
		if _, duplicate := visited[current]; duplicate {
			return "", false
		}
		visited[current] = struct{}{}
		entry, found := updates[current]
		if !found {
			entry, found = session.Entries[current]
		}
		if !found {
			return "", false
		}
		if entry.Focus != nil {
			if entry.Focus.FocusedGoalID == nil {
				return "", true
			}
			return *entry.Focus.FocusedGoalID, true
		}
		current = entry.ParentID
	}
	return "", true
}

// readPiGoalSession reads native parent links without a large get_entries response over stdout.
func readPiGoalSession(ctx context.Context, path, workingDir, sessionID string) (session piGoalSession, err error) {
	var reader piGoalSessionReader
	return reader.read(ctx, path, workingDir, sessionID)
}

func (reader *piGoalSessionReader) read(ctx context.Context, path, workingDir, sessionID string) (session piGoalSession, err error) {
	defer func() {
		if err != nil {
			*reader = piGoalSessionReader{}
		}
	}()
	file, err := os.Open(path)
	if err != nil {
		return session, err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	info, err := file.Stat()
	if err != nil {
		return session, err
	}
	if !info.Mode().IsRegular() {
		return session, fmt.Errorf("the Pi session is not a regular file")
	}
	reuse := reader.path == path && reader.workingDir == workingDir && reader.sessionID == sessionID && reader.info != nil &&
		os.SameFile(reader.info, info) && info.Size() >= reader.info.Size() &&
		(info.Size() > reader.info.Size() || info.ModTime().Equal(reader.info.ModTime()))
	if !reuse {
		*reader = piGoalSessionReader{path: path, workingDir: workingDir, sessionID: sessionID}
	}
	if reader.offset == info.Size() && reuse {
		return reader.session, ctx.Err()
	}
	scanner := bufio.NewScanner(io.NewSectionReader(file, reader.offset, info.Size()-reader.offset))
	scanner.Buffer(make([]byte, 64*1024), piGoalSessionRecordByteLimit)
	// Pi appends complete JSON lines. Leave an unfinished final record for the next read.
	scanner.Split(func(data []byte, atEOF bool) (int, []byte, error) {
		if atEOF && !bytes.Contains(data, []byte{'\n'}) {
			return 0, nil, nil
		}
		advance, token, err := bufio.ScanLines(data, atEOF)
		reader.offset += int64(advance)
		return advance, token, err
	})
	if !reuse {
		if !scanner.Scan() {
			return session, errors.Join(fmt.Errorf("the Pi session has no header"), scanner.Err())
		}
		var header piSessionHeader
		if err := json.Unmarshal(scanner.Bytes(), &header); err != nil {
			return session, err
		}
		if header.Type != "session" || header.ID != sessionID || (header.Cwd != "" && !sameDir(header.Cwd, workingDir)) {
			return session, fmt.Errorf("the Pi session header does not match the running session")
		}
	}
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return session, err
		}
		if err := reader.session.add(scanner.Bytes()); err != nil {
			return session, err
		}
	}
	reader.info = info
	return reader.session, errors.Join(scanner.Err(), ctx.Err())
}

// readPiGoalFile reads the focused goal's canonical file. The pool snapshot is a disposable cache.
func readPiGoalFile(ctx context.Context, workingDir, goalID string) (record *piGoalRecord, err error) {
	directory := filepath.Join(workingDir, ".pi", "goals")
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		file, err := os.Open(filepath.Join(directory, entry.Name()))
		if err != nil {
			return nil, err
		}
		content, readErr := io.ReadAll(io.LimitReader(file, piGoalFileByteLimit+1))
		if err := errors.Join(readErr, file.Close()); err != nil {
			return nil, err
		}
		if len(content) > piGoalFileByteLimit {
			return nil, fmt.Errorf("a Pi goal file exceeds the size limit")
		}
		parsed, err := parsePiGoalFile(content)
		if err != nil || parsed.ID != goalID {
			continue
		}
		if record != nil {
			return nil, fmt.Errorf("multiple Pi goal files contain the focused goal")
		}
		record = parsed
	}
	return record, nil
}

func parsePiGoalFile(content []byte) (*piGoalRecord, error) {
	var file struct {
		Version int `json:"version"`
		piGoalRecord
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	if err := decoder.Decode(&file); err != nil {
		return nil, err
	}
	if file.Version != 3 || file.ID == "" {
		return nil, fmt.Errorf("the Pi goal file has an unsupported shape")
	}
	body := strings.TrimSpace(string(content[decoder.InputOffset():]))
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	for index, line := range lines {
		if strings.TrimSpace(line) != "# Goal Prompt" {
			continue
		}
		lines = lines[index+1:]
		for end, line := range lines {
			if strings.TrimSpace(line) == "## Progress" {
				lines = lines[:end]
				break
			}
		}
		break
	}
	if objective := strings.TrimSpace(strings.Join(lines, "\n")); objective != "" {
		file.Objective = objective
	}
	if strings.TrimSpace(file.Objective) == "" {
		return nil, fmt.Errorf("the Pi goal has no objective")
	}
	return &file.piGoalRecord, nil
}
