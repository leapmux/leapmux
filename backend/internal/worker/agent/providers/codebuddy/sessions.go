package codebuddy

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/sessionstore"
)

// errControlTimeout reports a control request that got no answer in time.
var errControlTimeout = errors.New("timeout waiting for agent to respond")

// errString wraps a control error message as an error.
func errString(msg string) error {
	if msg == "" {
		return errors.New("control request failed")
	}
	return errors.New(msg)
}

// shortID generates a short correlation id for a control request.
func shortID() string { return id.Short() }

// CodeBuddy writes one JSONL transcript per session at
// `<config root>/projects/<mangled cwd>/<session-id>.jsonl`. The mangling
// replaces every `/` with `-`. There is no index, so this reader finds the
// project directory the same way and reads the newest transcripts.

const codebuddyProjectsDirName = "projects"

// codebuddyConfigDir resolves `$CODEBUDDY_CONFIG_DIR`, default `~/.codebuddy`.
func codebuddyConfigDir(q agent.StoredSessionQuery) string {
	return sessionstore.HomeDirFromEnv(q, "CODEBUDDY_CONFIG_DIR", ".codebuddy")
}

// mangleCodebuddyPath reproduces CodeBuddy's project slug: every path
// separator becomes a hyphen. The research report states the rule as "the cwd
// path with `/` replaced by `-`".
func mangleCodebuddyPath(path string) string {
	return strings.ReplaceAll(filepath.Clean(path), string(filepath.Separator), "-")
}

// codebuddyTranscriptRecord is the union of the fields this reader takes from a
// transcript line.
type codebuddyTranscriptRecord struct {
	SessionID string `json:"sessionId"`
	Cwd       string `json:"cwd"`
	// Title records the CLI writes.
	AITitle     string `json:"aiTitle"`
	CustomTitle string `json:"customTitle"`
	LastPrompt  string `json:"lastPrompt"`
	Message     struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// codebuddyStoredSessions is codebuddyProvider.ListStoredSessions.
func codebuddyStoredSessions(ctx context.Context, q agent.StoredSessionQuery) ([]agent.StoredSession, error) {
	workingDir := strings.TrimSpace(q.WorkingDir)
	if workingDir == "" {
		return nil, nil
	}
	configDir := codebuddyConfigDir(q)
	if configDir == "" {
		return nil, nil
	}
	projects := filepath.Join(configDir, codebuddyProjectsDirName)
	dir := filepath.Join(projects, mangleCodebuddyPath(workingDir))
	if _, err := os.Stat(dir); err != nil {
		return nil, nil
	}

	limit := q.EffectiveLimit()
	candidates, err := sessionstore.NewestEntries(dir, 0, sessionstore.EntryItself(isCodebuddyTranscript))
	if err != nil {
		return nil, nil
	}
	sessions := sessionstore.Collect(ctx, candidates, limit, func(entry sessionstore.Entry) (agent.StoredSession, bool) {
		return readCodebuddySession(entry, workingDir)
	})
	return agent.SortAndCapSessions(sessions, limit), nil
}

// isCodebuddyTranscript accepts the transcript files of a project directory.
func isCodebuddyTranscript(entry os.DirEntry) bool {
	return !entry.IsDir() && strings.HasSuffix(entry.Name(), ".jsonl")
}

// readCodebuddySession derives one session from its transcript and reports
// whether it belongs to workingDir.
func readCodebuddySession(entry sessionstore.Entry, workingDir string) (agent.StoredSession, bool) {
	head, atEOF, err := sessionstore.JSONLHead(entry.Path, sessionstore.JSONLProbeBytes)
	if err != nil || len(head) == 0 {
		return agent.StoredSession{}, false
	}
	var cwd, firstPrompt string
	var title codebuddyTitleCandidates
	for _, line := range head {
		var rec codebuddyTranscriptRecord
		if json.Unmarshal(line, &rec) != nil {
			continue
		}
		if cwd == "" && rec.Cwd != "" {
			cwd = rec.Cwd
		}
		title.take(rec)
		if firstPrompt == "" && rec.Message.Role == "user" {
			firstPrompt = sessionstore.ContentBlockText(rec.Message.Content)
		}
	}
	if !sessionstore.SameDir(cwd, workingDir) {
		return agent.StoredSession{}, false
	}
	if !atEOF {
		if tail, err := sessionstore.JSONLTail(entry.Path, sessionstore.JSONLProbeBytes); err == nil {
			for _, line := range tail {
				var rec codebuddyTranscriptRecord
				if json.Unmarshal(line, &rec) != nil {
					continue
				}
				title.take(rec)
			}
		}
	}
	return agent.StoredSession{
		Handle:    strings.TrimSuffix(entry.Name, ".jsonl"),
		Title:     sessionstore.TrimTitle(title.best(firstPrompt)),
		UpdatedAt: entry.ModTime,
	}, true
}

// codebuddyTitleCandidates collects the title-bearing records seen so far.
type codebuddyTitleCandidates struct {
	custom     string
	ai         string
	lastPrompt string
}

func (c *codebuddyTitleCandidates) take(rec codebuddyTranscriptRecord) {
	if rec.CustomTitle != "" {
		c.custom = rec.CustomTitle
	}
	if rec.AITitle != "" {
		c.ai = rec.AITitle
	}
	if rec.LastPrompt != "" {
		c.lastPrompt = rec.LastPrompt
	}
}

// best states the title precedence: the title the user set, then the title the
// model wrote, then the most recent prompt, then the first prompt.
func (c codebuddyTitleCandidates) best(firstPrompt string) string {
	return sessionstore.FirstNonBlank(c.custom, c.ai, c.lastPrompt, firstPrompt)
}
