package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"strings"
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

type zcodeStoredTool struct {
	ID        string          `json:"id"`
	SessionID string          `json:"sessionId"`
	MessageID string          `json:"messageId"`
	Data      json.RawMessage `json:"data"`
}

type zcodeToolRecord struct {
	native    zcodeStoredTool
	artifacts map[string]string
	ready     bool
}

type zcodeStoredAttachment struct {
	Type      string `json:"type"`
	SessionID string `json:"sessionID"`
	MessageID string `json:"messageID"`
	Mime      string `json:"mime"`
	URL       string `json:"url"`
	Metadata  struct {
		ArtifactURI string `json:"artifactUri"`
	} `json:"metadata"`
}

type zcodeArtifactReference struct {
	id   string
	uri  string
	mime string
}

// The scheduled request supplies messageID, which selects an existing index in ZCode's database.
// A missing request uses the session index and still requires an unambiguous tool-call ID.
func readZCodeToolRecords(ctx context.Context, location zcodeToolStoreLocation, requests map[string]zcodeToolLookup) (out map[string]zcodeToolRecord, resultErr error) {
	if location.sessionID == "" || len(requests) == 0 {
		return nil, nil
	}
	db, err := openSessionStoreDB(ctx, location.databasePath)
	if errors.Is(err, errSessionStoreAbsent) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { resultErr = errors.Join(resultErr, db.Close()) }()
	maximum := liveStdoutMaxTokenSize()
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
		var candidates []zcodeStoredTool
		for rows.Next() {
			var stored zcodeStoredTool
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
		var part struct {
			Type   string `json:"type"`
			CallID string `json:"callID"`
			Tool   string `json:"tool"`
			State  struct {
				Status      string            `json:"status"`
				Attachments []json.RawMessage `json:"attachments"`
			} `json:"state"`
		}
		if json.Unmarshal(native.Data, &part) != nil || part.Type != "tool" || part.CallID != callID || part.Tool == "" ||
			(request.toolName != "" && part.Tool != request.toolName) ||
			(part.State.Status != "completed" && part.State.Status != "error") {
			continue
		}
		for _, rawAttachment := range part.State.Attachments {
			var attachment zcodeStoredAttachment
			if json.Unmarshal(rawAttachment, &attachment) != nil {
				continue
			}
			if attachment.Type != "file" || attachment.SessionID != native.SessionID || attachment.MessageID != native.MessageID {
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
		if err := readZCodeArtifacts(ctx, childLocation, refs, out, maximum); err != nil {
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

func readZCodeArtifacts(ctx context.Context, location zcodeToolStoreLocation, references map[string][]zcodeArtifactReference, records map[string]zcodeToolRecord, maximum int) (resultErr error) {
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
	wanted := make(map[string][]string)
	for _, refs := range references {
		for _, ref := range refs {
			wanted[ref.id] = nil
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
		value = encodeDataURI(mime, data)
	}
	if len(value) > maximum {
		return "", fmt.Errorf("ZCode artifact exceeds the size limit")
	}
	return value, nil
}
