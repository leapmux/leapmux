package zcode

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/sessionstore"
)

type zcodeToolStoreLocation struct {
	databasePath string
	artifactRoot string
	sessionID    string
}

type zcodeToolLookup struct {
	messageID string
	toolName  string
	sessionID string
	callID    string
}

type zcodeToolRecord struct {
	native    contracts.ZCodeStoredTool
	artifacts map[string]string
	ready     bool
}

type zcodeArtifactReference struct {
	id   string
	uri  string
	mime string
}

// zcodeToolStore holds the reader's handle on ZCode's session database, and the
// artifacts it already read from beside it.
//
// The transcript reads that database once for each agent message while a tool
// result waits for its record. A handle opened and closed for each of those
// reads paid an open, a ping and a close every time, and the read has 100 ms for
// all of it -- so the reads that ran out of time enriched nothing at all.
//
// One store serves the agent's own transcript and each child transcript it
// opens. They read different sessions out of the SAME database file, and the
// mutex below is what lets them share the one handle.
type zcodeToolStore struct {
	mu   sync.Mutex
	path string
	// file identifies the open database by INODE, not by name. ZCode's database
	// path never changes within one agent, so a path comparison alone could never
	// notice a store the runtime deleted and recreated at that same path -- and the
	// handle would then answer every later read from the unlinked file. Cursor and
	// Reasonix already compared this way; see sessionstore.Moved.
	file os.FileInfo
	db   *sql.DB
}

// zcodeArtifactCache maps an artifact URI to the data URI already built for it.
//
// ZCode writes an artifact file once and never rewrites it, so a record that still
// waits for a SECOND artifact no longer re-reads the first one, and a session whose
// artifacts are all cached reads no directory.
//
// ONE TRANSCRIPT'S TURN owns it, and that is why it does not live on the store beside
// the handle. The handle is per AGENT: the agent's transcript and every child
// transcript share it, deliberately, so they open one file. The cache is per TURN, and
// drop empties it at the turn end and at a session reset -- each entry is a fully
// decoded data URI as large as LiveMaxMessageSize (16 MiB), and only a pass of the
// SAME turn can read one, because the turn end clears the pending set a later pass
// would ask about. Held for the life of the agent instead, a session of computer-use
// screenshots retained every screenshot it ever took, which is the growth Reasonix's
// own event graph refuses for the same reason.
//
// Sharing ONE cache across the parent and its children was the same mistake in the
// other direction: a subagent reaches its turn end the instant its Agent result lands,
// which is mid-turn for the parent, so one subagent finishing emptied every artifact
// the parent and every sibling had already decoded.
type zcodeArtifactCache struct {
	mu      sync.Mutex
	entries map[string]string
}

func (c *zcodeArtifactCache) lookup(uri string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.entries[uri]
}

func (c *zcodeArtifactCache) remember(uri, data string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[string]string)
	}
	c.entries[uri] = data
}

// drop empties the cache. It touches no database handle, so a turn end waits for no
// read that another transcript runs.
func (c *zcodeArtifactCache) drop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	clear(c.entries)
}

// handle returns the open database for `path`, opening one when the file moved.
func (s *zcodeToolStore) handle(ctx context.Context, path string) (*sql.DB, error) {
	// A stat failure is not answered here: sessionstore.OpenDB below states the
	// canonical sessionstore.ErrAbsent for a store that is not there, and every
	// caller tests for that.
	current, statErr := os.Stat(path)
	if statErr != nil {
		// The stat is how a REPLACED store is noticed, so a stat that failed answers
		// nothing about the handle already open. Closing on it would discard a working
		// handle for a rename window or an EIO, and sessionstore.OpenDB below would then
		// fail too and report sessionstore.ErrAbsent for a store that is present. Keep
		// the handle and let the next pass ask again; Cursor and Reasonix keep theirs
		// for the same reason.
		if s.db != nil && s.path == path {
			return s.db, nil
		}
		s.closeLocked()
		return nil, sessionstore.ErrAbsent
	}
	if s.db != nil && !sessionstore.Moved(s.path, s.file, path, current) {
		return s.db, nil
	}
	s.closeLocked()
	db, err := sessionstore.OpenDB(ctx, path)
	if err != nil {
		return nil, err
	}
	s.path, s.file, s.db = path, current, db
	return db, nil
}

// close releases the handle. The agent's context ending is what calls it.
func (s *zcodeToolStore) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeLocked()
}

func (s *zcodeToolStore) closeLocked() {
	if s.db != nil {
		if err := s.db.Close(); err != nil {
			slog.Debug("Close the ZCode session store", "error", err)
		}
		s.db = nil
	}
	s.path, s.file = "", nil
}

// The scheduled request supplies messageID, which selects an existing index in ZCode's database.
// A missing request uses the session index and still requires an unambiguous tool-call ID.
func readZCodeToolRecords(ctx context.Context, store *zcodeToolStore, artifacts *zcodeArtifactCache, location zcodeToolStoreLocation, requests map[string]zcodeToolLookup) (out map[string]zcodeToolRecord, resultErr error) {
	if location.sessionID == "" || len(requests) == 0 {
		return nil, nil
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	db, err := store.handle(ctx, location.databasePath)
	if errors.Is(err, sessionstore.ErrAbsent) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	maximum := agent.LiveMaxMessageSize()
	out = make(map[string]zcodeToolRecord)
	references := make(map[string][]zcodeArtifactReference)
	sessions := map[string]bool{location.sessionID: true}
	for id, request := range requests {
		sessionID := request.sessionID
		if sessionID == "" {
			sessionID = location.sessionID
		}
		allowed, checked := sessions[sessionID]
		if !checked {
			allowed, err = zcodeSessionDescendsFrom(ctx, db, sessionID, location.sessionID)
			if err != nil {
				return out, err
			}
			sessions[sessionID] = allowed
		}
		if !allowed {
			continue
		}
		callID := request.callID
		if callID == "" {
			callID = id
		}
		query := `SELECT id, session_id, message_id, data FROM part WHERE session_id = ?`
		args := []any{sessionID}
		if request.messageID != "" {
			query += ` AND message_id = ?`
			args = append(args, request.messageID)
		}
		query += ` AND CASE WHEN length(CAST(data AS BLOB)) <= ? THEN CASE WHEN json_valid(data) THEN CASE WHEN json_extract(data, '$.type') = 'tool' THEN json_extract(data, '$.callID') END END END = ? LIMIT 2`
		args = append(args, maximum, callID)
		rows, err := db.QueryContext(ctx, query, args...)
		if err != nil {
			return out, err
		}
		var candidates []contracts.ZCodeStoredTool
		for rows.Next() {
			var stored contracts.ZCodeStoredTool
			var data []byte
			if err := rows.Scan(&stored.ID, &stored.SessionID, &stored.MessageID, &data); err != nil {
				return out, errors.Join(err, rows.Close())
			}
			stored.Data = data
			candidates = append(candidates, stored)
		}
		err = errors.Join(rows.Err(), rows.Close())
		if err != nil {
			return out, err
		}
		if len(candidates) != 1 {
			continue
		}
		native := candidates[0]
		var part contracts.ZCodeStoredPart
		if json.Unmarshal(native.Data, &part) != nil || part.Type != contracts.ZCodeStoredPartTypeTool ||
			part.CallID != callID || part.Tool == "" ||
			(request.toolName != "" && part.Tool != request.toolName) ||
			(part.State.Status != contracts.ZCodeStoredPartStatusCompleted && part.State.Status != contracts.ZCodeStoredPartStatusError) {
			continue
		}
		for _, rawAttachment := range part.State.Attachments {
			var attachment contracts.ZCodeStoredAttachment
			if json.Unmarshal(rawAttachment, &attachment) != nil {
				continue
			}
			if attachment.Type != contracts.ZCodeStoredAttachmentTypeFile || attachment.SessionID != native.SessionID || attachment.MessageID != native.MessageID {
				continue
			}
			uri := attachment.Metadata.ArtifactURI
			if uri == "" {
				uri = attachment.URL
			}
			artifactID := zcodeArtifactID(uri, native.SessionID)
			if artifactID != "" {
				references[id] = append(references[id], zcodeArtifactReference{id: artifactID, uri: uri, mime: attachment.Mime})
			}
		}
		out[id] = zcodeToolRecord{native: native, artifacts: make(map[string]string), ready: len(references[id]) == 0}
	}
	if len(references) == 0 {
		return out, nil
	}
	groups := make(map[string]map[string][]zcodeArtifactReference)
	for id, refs := range references {
		sessionID := out[id].native.SessionID
		if groups[sessionID] == nil {
			groups[sessionID] = make(map[string][]zcodeArtifactReference)
		}
		groups[sessionID][id] = refs
	}
	var failures []error
	for sessionID, refs := range groups {
		childLocation := location
		childLocation.sessionID = sessionID
		if err := readZCodeArtifacts(ctx, store, artifacts, childLocation, refs, out, maximum); err != nil {
			failures = append(failures, err)
		}
	}
	return out, errors.Join(failures...)
}

func zcodeSessionDescendsFrom(ctx context.Context, db *sql.DB, child, parent string) (bool, error) {
	var found int
	err := db.QueryRowContext(ctx, `WITH RECURSIVE ancestors(id, parent_id) AS (
 SELECT id, parent_id FROM session WHERE id = ?
 UNION
 SELECT session.id, session.parent_id FROM session JOIN ancestors ON session.id = ancestors.parent_id
) SELECT 1 FROM ancestors WHERE id = ? LIMIT 1`, child, parent).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

var zcodeArtifactIDPattern = regexp.MustCompile(`^tool-result-[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
var zcodeArtifactFilePattern = regexp.MustCompile(`(?:^|-)(tool-result-[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})\.[a-zA-Z0-9]+$`)

func zcodeArtifactID(rawURI, sessionID string) string {
	if sessionID == "" || sessionID == "." || sessionID == ".." {
		return ""
	}
	parsed, err := url.Parse(rawURI)
	if err != nil || parsed.Scheme != "zcode-artifact" || parsed.Host != sessionID || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.Opaque != "" {
		return ""
	}
	id := strings.TrimPrefix(parsed.Path, "/")
	if parsed.Path != "/"+id || !zcodeArtifactIDPattern.MatchString(id) {
		return ""
	}
	return id
}

// ZCode replaces each non-ASCII UTF-16 code unit and caps the resulting segment at 120 characters.
func zcodeArtifactSegment(value string) string {
	var out strings.Builder
	for _, char := range value {
		if out.Len() >= 120 {
			break
		}
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || strings.ContainsRune("._-", char) {
			out.WriteRune(char)
		} else {
			out.WriteByte('_')
			if char > 0xffff && out.Len() < 120 {
				out.WriteByte('_')
			}
		}
	}
	if out.Len() == 0 {
		return "unknown"
	}
	return out.String()
}

// readZCodeArtifacts fills each record with the artifact bodies its tool produced.
//
// It answers from the store's cache first. A record that still waits for one
// artifact is re-read on every agent message until that artifact appears, and
// without the cache each of those reads swept the whole session directory and
// read every artifact the record already held.
func readZCodeArtifacts(ctx context.Context, store *zcodeToolStore, artifacts *zcodeArtifactCache, location zcodeToolStoreLocation, references map[string][]zcodeArtifactReference, records map[string]zcodeToolRecord, maximum int) (resultErr error) {
	if adoptCachedZCodeArtifacts(artifacts, references, records) {
		return nil
	}
	root, err := os.OpenRoot(location.artifactRoot)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, root.Close()) }()
	session, err := root.OpenRoot(zcodeArtifactSegment(location.sessionID))
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, session.Close()) }()
	directory, err := session.Open(".")
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, directory.Close()) }()
	// Only the artifacts no record holds yet. A cached one needs no directory entry.
	wanted := make(map[string][]string)
	for id, refs := range references {
		for _, ref := range refs {
			if records[id].artifacts[ref.uri] == "" {
				wanted[ref.id] = nil
			}
		}
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		entries, err := directory.ReadDir(256)
		for _, entry := range entries {
			match := zcodeArtifactFilePattern.FindStringSubmatch(entry.Name())
			if len(match) == 0 || !entry.Type().IsRegular() {
				continue
			}
			if files, ok := wanted[match[1]]; ok && len(files) < 2 {
				wanted[match[1]] = append(files, entry.Name())
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
	}
	var failures []error
	for id, refs := range references {
		record := records[id]
		remaining := maximum - len(record.native.Data) - 1024
		// The artifacts the cache already supplied count against the same budget.
		for _, data := range record.artifacts {
			remaining -= len(data)
		}
		for _, ref := range refs {
			if err := ctx.Err(); err != nil {
				return errors.Join(append(failures, err)...)
			}
			files := wanted[ref.id]
			if len(files) != 1 || remaining <= 0 || record.artifacts[ref.uri] != "" {
				continue
			}
			data, err := readZCodeArtifact(session, files[0], ref.mime, remaining)
			if err != nil {
				failures = append(failures, err)
				continue
			}
			record.artifacts[ref.uri] = data
			artifacts.remember(ref.uri, data)
			remaining -= len(data)
		}
		record.ready = true
		for _, ref := range refs {
			if record.artifacts[ref.uri] == "" {
				record.ready = false
				break
			}
		}
		records[id] = record
	}
	return errors.Join(failures...)
}

// adoptCachedZCodeArtifacts copies what this transcript already read into each record,
// and reports whether every reference is now answered.
func adoptCachedZCodeArtifacts(artifacts *zcodeArtifactCache, references map[string][]zcodeArtifactReference, records map[string]zcodeToolRecord) bool {
	complete := true
	for id, refs := range references {
		record := records[id]
		record.ready = true
		for _, ref := range refs {
			if record.artifacts[ref.uri] != "" {
				continue
			}
			if cached := artifacts.lookup(ref.uri); cached != "" {
				record.artifacts[ref.uri] = cached
				continue
			}
			record.ready = false
			complete = false
		}
		records[id] = record
	}
	return complete
}

func readZCodeArtifact(root *os.Root, name, mime string, maximum int) (value string, resultErr error) {
	file, err := root.Open(name)
	if err != nil {
		return "", err
	}
	defer func() { resultErr = errors.Join(resultErr, file.Close()) }()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() > int64(maximum) {
		return "", fmt.Errorf("ZCode artifact is not a regular file within the size limit")
	}
	data, err := io.ReadAll(io.LimitReader(file, int64(maximum)+1))
	if err != nil {
		return "", err
	}
	if len(data) == 0 {
		return "", fmt.Errorf("ZCode artifact is empty")
	}
	if len(data) >= 5 && strings.EqualFold(string(data[:5]), "data:") {
		value = string(data)
	} else {
		value = providerkit.EncodeDataURI(mime, data)
	}
	if len(value) > maximum {
		return "", fmt.Errorf("ZCode artifact exceeds the size limit")
	}
	return value, nil
}
