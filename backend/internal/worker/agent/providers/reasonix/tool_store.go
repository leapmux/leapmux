package reasonix

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
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/tooltranscript"
)

var reasonixResultClip = regexp.MustCompile(`\n…\(([0-9]+) more chars truncated\)$`)

// reasonixToolRecordHeader identifies the tool call one stored message answers.
//
// Every line of the transcript decodes through this type, so it holds the two fields
// that select a line and nothing else. The full record below decodes the selected
// lines alone.
type reasonixToolRecordHeader struct {
	Role       string `json:"role"`
	ToolCallID string `json:"tool_call_id"`
}

// reasonixToolRecord is one tool record of Reasonix's own transcript. The browser
// reads the same record back out of the supplement.
//
// A Go struct tag takes a LITERAL, so these tags cannot spell the generated contract
// constants that the browser reads. TestReasonixToolRecordTagsMatchTheContract pins
// each tag to its constant instead.
type reasonixToolRecord struct {
	reasonixToolRecordHeader
	Name       string `json:"name"`
	Content    string `json:"content"`
	RawContent string `json:"raw_content"`
}

// reasonixSessionIDIsSafe refuses an id that would escape the session roots.
func reasonixSessionIDIsSafe(sessionID string) bool {
	return sessionID != "" && sessionID != "." && sessionID != ".." && !strings.ContainsAny(sessionID, `/\`)
}

// reasonixDefaultToolStorePath states where a session's transcript lives when no
// sidecar redirects it.
//
// It reads NO file. The tool transcript asks for the store location on every
// agent message and uses it only when a tool result waits for its record, so the
// probe below runs where that record is read rather than here.
func reasonixDefaultToolStorePath(q agent.StoredSessionQuery, sessionID, workingDir string) string {
	if !reasonixSessionIDIsSafe(sessionID) {
		return ""
	}
	roots := reasonixSessionRoots(q, workingDir)
	if len(roots) == 0 {
		return ""
	}
	return filepath.Join(roots[len(roots)-1], sessionID+".jsonl")
}

// reasonixToolStorePath resolves the transcript Reasonix writes for one session.
//
// The home directory comes from the query, not from the process environment. A
// resumed agent carries the home directory of the row it resumed, and the two
// differ whenever the worker runs for a different user than the one that started
// the session.
func reasonixToolStorePath(q agent.StoredSessionQuery, sessionID, workingDir string) string {
	if !reasonixSessionIDIsSafe(sessionID) {
		return ""
	}
	roots := reasonixSessionRoots(q, workingDir)
	for _, root := range roots {
		path := filepath.Join(root, sessionID+".jsonl")
		var meta struct {
			SessionID        string `json:"sessionId"`
			ActiveTranscript string `json:"activeTranscript"`
		}
		data, err := os.ReadFile(filepath.Join(root, sessionID+reasonixACPSuffix))
		if err == nil && json.Unmarshal(data, &meta) == nil && meta.SessionID == sessionID &&
			meta.ActiveTranscript != "" && filepath.Base(meta.ActiveTranscript) == meta.ActiveTranscript &&
			strings.HasSuffix(meta.ActiveTranscript, ".jsonl") {
			target := filepath.Join(root, meta.ActiveTranscript)
			targetData, err := os.ReadFile(strings.TrimSuffix(target, ".jsonl") + reasonixACPSuffix)
			var targetMeta reasonixACPMeta
			if err == nil && json.Unmarshal(targetData, &targetMeta) == nil && targetMeta.SessionID == sessionID {
				if info, err := os.Stat(target); err == nil && info.Mode().IsRegular() {
					return target
				}
			}
		}
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			return path
		}
		if info, err := os.Stat(strings.TrimSuffix(path, ".jsonl") + ".events.jsonl"); err == nil && info.Mode().IsRegular() {
			return path
		}
	}
	if len(roots) == 0 {
		return ""
	}
	return filepath.Join(roots[len(roots)-1], sessionID+".jsonl")
}

// reasonixToolSource reads Reasonix's tool records out of the session transcript that
// Reasonix writes.
type reasonixToolSource struct {
	tooltranscript.SourceDefaults
	// sessionID reports the ACP session that runs now.
	sessionID  func() string
	query      agent.StoredSessionQuery
	workingDir string

	// mu guards cache alone. ReadSupplements runs on the supplement worker and
	// ResetRecords runs on the reader goroutine, so the two swap the POINTER under
	// this mutex: a read already in flight keeps the cache that it started with, and
	// the next read starts from the empty one.
	mu    sync.Mutex
	cache *reasonixEventCache
}

func newReasonixToolTranscript(ctx context.Context, services agent.ProviderServices, sessionID func() string, query agent.StoredSessionQuery, workingDir string) *tooltranscript.Transcript {
	source := &reasonixToolSource{sessionID: sessionID, query: query, workingDir: workingDir, cache: &reasonixEventCache{}}
	return tooltranscript.New(ctx, services, source)
}

func (r *reasonixToolSource) ProviderName() string { return "Reasonix" }

func (r *reasonixToolSource) Locate(string) tooltranscript.Location {
	id := r.sessionID()
	path := reasonixDefaultToolStorePath(r.query, id, r.workingDir)
	return tooltranscript.Location{SessionKey: id, Path: path, Ready: path != ""}
}

func (r *reasonixToolSource) ToolCallID(original []byte) string { return acp.ToolCallID(original) }

func (r *reasonixToolSource) ResetRecords() {
	r.mu.Lock()
	r.cache = &reasonixEventCache{}
	r.mu.Unlock()
}

// ReadSupplements resolves the transcript that Reasonix actually writes.
//
// The location that locate reports is the session's DEFAULT transcript. Only this path
// reads the store, and the sidecar that it reads can point the session at a different
// file.
func (r *reasonixToolSource) ReadSupplements(ctx context.Context, _ string, pending map[string]agent.MessageContent, _ bool) (map[string][]byte, error) {
	path := reasonixToolStorePath(r.query, r.sessionID(), r.workingDir)
	r.mu.Lock()
	cache := r.cache
	r.mu.Unlock()
	return readReasonixToolSupplements(ctx, path, pending, cache)
}

// reasonixResultMatches verifies that the saved result produced the protocol text.
func reasonixResultMatches(original, stored string) bool {
	if original == stored {
		return true
	}
	match := reasonixResultClip.FindStringSubmatchIndex(original)
	if match == nil {
		return false
	}
	omitted, err := strconv.Atoi(original[match[2]:match[3]])
	return err == nil && omitted > 0 && len(stored) >= match[0] &&
		len(stored)-match[0] == omitted && strings.HasPrefix(stored, original[:match[0]])
}

// ACP sends the first error line. The native transcript also retains the tool's details.
func reasonixStoredResultMatches(original, stored string, failed bool) bool {
	if reasonixResultMatches(original, stored) {
		return true
	}
	if !failed {
		return false
	}
	firstLine, _, hasDetails := strings.Cut(stored, "\n")
	headline, hasPrefix := strings.CutPrefix(firstLine, "error: ")
	return hasDetails && hasPrefix && reasonixResultMatches(original, headline)
}

// readReasonixToolSupplements builds the supplement of every pending tool call
// the stored transcript answers.
//
// It reports the error of the read and the supplements it built at the same
// time. One event the reader cannot place must not cost the reader every result
// in the session, so the caller logs the error and enriches what came back.
func readReasonixToolSupplements(ctx context.Context, path string, pending map[string]agent.MessageContent, cache *reasonixEventCache) (map[string][]byte, error) {
	resolved := make(map[string][]byte, len(pending))
	for id, content := range pending {
		resolved[id] = acp.ResolveMessageContent(content)
	}
	records, readErr := readReasonixToolRecords(ctx, path, resolved, cache)
	out := make(map[string][]byte, len(records))
	for id, raw := range records {
		var record reasonixToolRecord
		if json.Unmarshal(raw, &record) != nil || record.Role != contracts.ReasonixToolRecordToolRole || record.ToolCallID != id {
			continue
		}
		var original struct {
			Status  string `json:"status"`
			Content []struct {
				Content struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"content"`
		}
		if json.Unmarshal(resolved[id], &original) != nil || len(original.Content) != 1 || original.Content[0].Content.Type != "text" {
			continue
		}
		text := original.Content[0].Content.Text
		failed := original.Status == "failed"
		if !reasonixStoredResultMatches(text, record.Content, failed) && (record.RawContent == "" || !reasonixStoredResultMatches(text, record.RawContent, failed)) {
			continue
		}
		var originalFields map[string]json.RawMessage
		if err := json.Unmarshal(pending[id].Original, &originalFields); err != nil {
			readErr = errors.Join(readErr, err)
			continue
		}
		supplement := acp.NewToolSupplement(originalFields)
		if err := supplement.SetRawOutput(map[string]json.RawMessage{contracts.ReasonixToolRecordEnvelope: raw}); err != nil {
			readErr = errors.Join(readErr, err)
			continue
		}
		encoded, err := json.Marshal(supplement)
		if err != nil {
			readErr = errors.Join(readErr, err)
			continue
		}
		out[id] = encoded
	}
	return out, readErr
}

// Read the native event log first. A checkpoint alone can describe an older branch.
func readReasonixToolRecords(ctx context.Context, path string, pending map[string][]byte, cache *reasonixEventCache) (records map[string]json.RawMessage, err error) {
	eventPath := strings.TrimSuffix(path, ".jsonl") + ".events.jsonl"
	file, err := os.Open(eventPath)
	if err == nil {
		defer func() { err = errors.Join(err, file.Close()) }()
		return cache.read(ctx, eventPath, file, pending)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	file, err = os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("the Reasonix transcript is not a regular file")
	}
	out := make(map[string]json.RawMessage)
	_, err = readReasonixJSONL(ctx, file, 0, info.Size(), func(raw json.RawMessage, _ int64, _ int) error {
		id, record := reasonixSelectedRecord(raw, pending)
		if id != "" {
			out[id] = record
		}
		return nil
	})
	return out, err
}

// reasonixToolCallID reports the tool call one stored message answers. It is ""
// for every other message.
func reasonixToolCallID(raw json.RawMessage) string {
	var header reasonixToolRecordHeader
	if json.Unmarshal(raw, &header) != nil || header.Role != contracts.ReasonixToolRecordToolRole {
		return ""
	}
	return header.ToolCallID
}

func reasonixSelectedRecord(raw json.RawMessage, pending map[string][]byte) (string, json.RawMessage) {
	id := reasonixToolCallID(raw)
	if id == "" {
		return "", nil
	}
	if _, found := pending[id]; !found {
		return "", nil
	}
	return id, raw
}

// readReasonixJSONL visits each complete line of `file` between `offset` and
// `size`, and reports where the next read must start.
//
// The returned offset is the first byte after the last line it visited, so a
// caller that keeps it applies each line exactly once as the log grows.
//
// `size` is the size the caller captured, not the size the file has now. A log
// the agent still appends to cannot otherwise keep this read active.
//
// The LAST line can be torn. Reasonix appends to this log while LeapMux reads
// it, so a read can land in the middle of a write. A trailing line that carries
// no terminator and does not parse is therefore left where it is, with no error:
// the next read sees the whole line. A line in the MIDDLE that does not parse is
// a real corruption and still fails the read.
func readReasonixJSONL(ctx context.Context, file *os.File, offset, size int64, visit func(raw json.RawMessage, offset int64, length int) error) (int64, error) {
	if offset >= size {
		return offset, ctx.Err()
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return offset, err
	}
	maximum := agent.LiveMaxMessageSize()
	reader := bufio.NewReader(io.LimitReader(file, size-offset))
	position := offset
	for {
		if err := ctx.Err(); err != nil {
			return position, err
		}
		line, terminated, err := readReasonixLine(reader, maximum)
		if err != nil && !errors.Is(err, io.EOF) {
			return position, err
		}
		raw := bytes.TrimRight(line, "\r\n")
		if len(bytes.TrimSpace(raw)) == 0 {
			if !terminated {
				return position, nil
			}
			position += int64(len(line))
			continue
		}
		if !json.Valid(raw) {
			if !terminated {
				return position, nil
			}
			return position, fmt.Errorf("the Reasonix transcript contains invalid JSON")
		}
		if err := visit(append(json.RawMessage(nil), raw...), position, len(raw)); err != nil {
			return position, err
		}
		position += int64(len(line))
		if !terminated {
			return position, nil
		}
	}
}

// readReasonixLine reads one line and reports whether a terminator ended it.
//
// It caps the line at the worker's payload budget, the same ceiling the live
// stdout scanner applies, so one unterminated megabyte cannot be buffered here.
func readReasonixLine(reader *bufio.Reader, maximum int) ([]byte, bool, error) {
	var line []byte
	for {
		chunk, err := reader.ReadSlice('\n')
		if len(line)+len(chunk) > maximum {
			return line, false, fmt.Errorf("a Reasonix transcript line exceeds the size limit")
		}
		line = append(line, chunk...)
		if err == nil {
			return line, true, nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return line, false, err
	}
}
