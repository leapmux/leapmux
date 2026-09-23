package pi

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

	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/sessionstore"
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

// piGoalSession indexes every entry of a native session by its ID, with the parent
// link and the focus record of each one. focus walks the parent chain from a leaf
// that the caller supplies, and the provider can switch to a branch that the
// current one does not reach, so no entry is safe to discard.
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

// read reads native parent links without a large get_entries response over stdout.
// It resumes at the offset that the previous call reached when the file is the same
// one and only grew, and re-reads from the header otherwise.
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
		if header.Type != "session" || header.ID != sessionID || (header.Cwd != "" && !sessionstore.SameDir(header.Cwd, workingDir)) {
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

// piGoalFileReader finds the focused goal's canonical file. The pool snapshot is a
// disposable cache. Only the refresh goroutine reads or changes the reader.
//
// A refresh runs for every goal hint, so an uncached reader re-reads and re-parses
// each goal file every time. The reader keeps the parsed record of each file and
// reuses it while the file's identity, size and modification time are unchanged,
// exactly as piGoalSessionReader reuses its index.
type piGoalFileReader struct {
	directory string
	files     map[string]piGoalFileEntry
}

// piGoalFileEntry is one cached goal file. A record of nil marks a file that does
// not parse as a goal, so a re-read cannot help until the file itself changes.
type piGoalFileEntry struct {
	info   os.FileInfo
	record *piGoalRecord
}

// read returns the record of the focused goal, or nil when no file carries it. It
// walks every goal file, because two files that state the same goal ID are a
// conflict that the caller must see.
func (reader *piGoalFileReader) read(ctx context.Context, workingDir, goalID string) (record *piGoalRecord, err error) {
	defer func() {
		if err != nil {
			*reader = piGoalFileReader{}
		}
	}()
	directory := filepath.Join(workingDir, ".pi", "goals")
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, err
	}
	if reader.directory != directory {
		*reader = piGoalFileReader{directory: directory}
	}
	// A deleted file must leave the cache, so each read builds the map again.
	files := make(map[string]piGoalFileEntry, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		cached, err := reader.load(directory, entry.Name())
		if err != nil {
			return nil, err
		}
		files[entry.Name()] = cached
		if cached.record == nil || cached.record.ID != goalID {
			continue
		}
		if record != nil {
			return nil, fmt.Errorf("multiple Pi goal files contain the focused goal")
		}
		// Hand the caller its own copy. The cache keeps the parsed record for the next
		// read, and a caller that changed one field would corrupt every read after it.
		copied := *cached.record
		record = &copied
	}
	reader.files = files
	return record, nil
}

// load returns the cached entry for one goal file, and re-reads and re-parses the
// file when the cache does not match what is on disk now.
func (reader *piGoalFileReader) load(directory, name string) (entry piGoalFileEntry, err error) {
	file, err := os.Open(filepath.Join(directory, name))
	if err != nil {
		return piGoalFileEntry{}, err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	info, err := file.Stat()
	if err != nil {
		return piGoalFileEntry{}, err
	}
	if !info.Mode().IsRegular() {
		return piGoalFileEntry{}, fmt.Errorf("a Pi goal file is not a regular file")
	}
	if cached, found := reader.files[name]; found && cached.info != nil && os.SameFile(cached.info, info) &&
		cached.info.Size() == info.Size() && cached.info.ModTime().Equal(info.ModTime()) {
		return cached, nil
	}
	content, err := io.ReadAll(io.LimitReader(file, piGoalFileByteLimit+1))
	if err != nil {
		return piGoalFileEntry{}, err
	}
	if len(content) > piGoalFileByteLimit {
		return piGoalFileEntry{}, fmt.Errorf("a Pi goal file exceeds the size limit")
	}
	// The directory holds files that are not goals. One that does not parse is
	// cached as an absent record rather than as a failure of the whole read.
	record, parseErr := parsePiGoalFile(content)
	if parseErr != nil {
		record = nil
	}
	return piGoalFileEntry{info: info, record: record}, nil
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
	// The progress log is a record of the work, not a part of the objective. Cut it
	// first, so a file that carries no "# Goal Prompt" heading also drops it.
	for end, line := range lines {
		if strings.TrimSpace(line) == "## Progress" {
			lines = lines[:end]
			break
		}
	}
	for index, line := range lines {
		if strings.TrimSpace(line) == "# Goal Prompt" {
			lines = lines[index+1:]
			break
		}
	}
	if objective := strings.TrimSpace(strings.Join(lines, "\n")); objective != "" {
		file.Objective = objective
	}
	if strings.TrimSpace(file.Objective) == "" {
		return nil, fmt.Errorf("the Pi goal has no objective")
	}
	return &file.piGoalRecord, nil
}
