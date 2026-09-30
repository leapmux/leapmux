package qoder

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/sessionstore"
)

// Qoder writes one JSONL transcript per session at
// `<config>/projects/<project-slug>/<session-uuid>.jsonl`.

const qoderProjectsDirName = "projects"

// qoderConfigDir resolves the config root from --config-dir semantics: the
// query's home under `.qoder`, or QODER_CONFIG_DIR.
func qoderConfigDir(q agent.StoredSessionQuery) string {
	return sessionstore.HomeDirFromEnv(q, "QODER_CONFIG_DIR", ".qoder")
}

// qoderProjectSlug matches the installed CLI's ASCII replacement and 200-unit
// cap. The CLI hashes UTF-16 units and replaces both halves of an astral rune.
func qoderProjectSlug(path string) string {
	path = filepath.Clean(path)
	var builder strings.Builder
	builder.Grow(len(path))
	for _, ch := range path {
		switch {
		case ch >= 'a' && ch <= 'z', ch >= 'A' && ch <= 'Z', ch >= '0' && ch <= '9':
			builder.WriteRune(ch)
		case ch > 0xFFFF:
			builder.WriteString("--")
		default:
			builder.WriteByte('-')
		}
	}
	slug := builder.String()
	if len(slug) <= 200 {
		return slug
	}
	hash := uint32(5381)
	for _, unit := range utf16.Encode([]rune(path)) {
		hash = hash*33 ^ uint32(unit)
	}
	signed := int64(int32(hash))
	if signed < 0 {
		signed = -signed
	}
	return slug[:200] + "-" + strconv.FormatInt(signed, 36)
}

// qoderTranscriptRecord is the union of the fields this reader takes.
type qoderTranscriptRecord struct {
	SessionID   string `json:"sessionId"`
	Cwd         string `json:"cwd"`
	IsSidechain bool   `json:"isSidechain"`
	LastPrompt  string `json:"lastPrompt"`
	Message     struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// qoderStoredSessions is qoderProvider.ListStoredSessions.
func qoderStoredSessions(ctx context.Context, q agent.StoredSessionQuery) ([]agent.StoredSession, error) {
	workingDir := strings.TrimSpace(q.WorkingDir)
	if workingDir == "" {
		return nil, nil
	}
	configDir := qoderConfigDir(q)
	if configDir == "" {
		return nil, nil
	}
	projects := filepath.Join(configDir, qoderProjectsDirName)
	dir := filepath.Join(projects, qoderProjectSlug(workingDir))
	if _, err := os.Stat(dir); err != nil {
		return nil, nil
	}

	limit := q.EffectiveLimit()
	candidates, err := sessionstore.NewestEntries(dir, 0, sessionstore.EntryItself(isQoderTranscript))
	if err != nil {
		return nil, nil
	}
	sessions := sessionstore.Collect(ctx, candidates, limit, func(entry sessionstore.Entry) (agent.StoredSession, bool) {
		return readQoderSession(entry, workingDir)
	})
	return agent.SortAndCapSessions(sessions, limit), nil
}

// isQoderTranscript accepts the transcript files of a project directory.
func isQoderTranscript(entry os.DirEntry) bool {
	return !entry.IsDir() && strings.HasSuffix(entry.Name(), ".jsonl")
}

// readQoderSession derives one session from its transcript and reports whether
// it belongs to workingDir.
func readQoderSession(entry sessionstore.Entry, workingDir string) (agent.StoredSession, bool) {
	head, _, err := sessionstore.JSONLHead(entry.Path, sessionstore.JSONLProbeBytes)
	if err != nil || len(head) == 0 {
		return agent.StoredSession{}, false
	}
	var cwd, firstPrompt, lastPrompt string
	var sidechain bool
	for _, line := range head {
		var rec qoderTranscriptRecord
		if json.Unmarshal(line, &rec) != nil {
			continue
		}
		if cwd == "" && rec.Cwd != "" {
			cwd = rec.Cwd
		}
		if rec.IsSidechain {
			sidechain = true
		}
		if rec.LastPrompt != "" {
			lastPrompt = rec.LastPrompt
		}
		if firstPrompt == "" && rec.Message.Role == "user" {
			firstPrompt = sessionstore.ContentBlockText(rec.Message.Content)
		}
	}
	if !sessionstore.SameDir(cwd, workingDir) {
		return agent.StoredSession{}, false
	}
	if sidechain {
		return agent.StoredSession{}, false
	}
	return agent.StoredSession{
		Handle:    strings.TrimSuffix(entry.Name, ".jsonl"),
		Title:     sessionstore.TrimTitle(sessionstore.FirstNonBlank(lastPrompt, firstPrompt)),
		UpdatedAt: entry.ModTime,
	}, true
}
