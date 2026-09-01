package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// Claude Code writes one JSONL transcript per session at
// `<config dir>/projects/<mangled cwd>/<session id>.jsonl`. There is no index,
// so this reader finds the project directory by mangling the working directory
// the same way, stats its files newest-first, and reads a capped window from
// each end of the newest few -- which is what Claude's own session lister does.

// claudeMangleMaxLength is the length at which Claude truncates a mangled path
// and appends a hash. Its own constant is 200, chosen to leave room for the
// suffix inside a 255-byte filesystem component.
const claudeMangleMaxLength = 200

// claudeProjectsDirName is the directory holding one subdirectory per working
// directory.
const claudeProjectsDirName = "projects"

// claudeConfigDir resolves `$CLAUDE_CONFIG_DIR`, default `~/.claude`.
func claudeConfigDir(q StoredSessionQuery) string {
	return homeDirFromEnv(q, "CLAUDE_CONFIG_DIR", ".claude")
}

// mangleClaudePath reproduces Claude's `sanitizePath`: every character outside
// [A-Za-z0-9] becomes a hyphen.
//
// One hyphen per UTF-16 CODE UNIT, not per rune. Claude's rule is the JavaScript
// `replace(/[^a-zA-Z0-9]/g, "-")`, and that regex carries no `u` flag, so it
// matches one UTF-16 code unit at a time. A character outside the Basic
// Multilingual Plane is two code units there and one rune here, so a working
// directory that holds an emoji produces two hyphens in the directory Claude
// wrote and would produce one here. The computed directory would then not
// exist, and every Claude session of that directory would be invisible with no
// error to say so.
//
// It returns the UNTRUNCATED form; claudeProjectDirs handles the long case.
func mangleClaudePath(path string) string {
	var b strings.Builder
	b.Grow(len(path))
	for _, r := range path {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
			if r > 0xFFFF {
				b.WriteByte('-')
			}
		}
	}
	return b.String()
}

// claudeProjectDirs lists the project directories that MAY hold sessions for
// `workingDir`.
//
// More than one, for two reasons, and both are why every candidate session is
// then verified against the `cwd` recorded inside it.
//
// The mangling is lossy: `/a/b-c`, `/a/b_c` and `/a/b.c` all become `-a-b-c`,
// so one directory can hold the sessions of several working directories.
//
// And a mangled path longer than the cap carries a hash this code cannot
// compute, so the long case matches every directory that starts with the
// truncated prefix. That set is small (it takes two working directories
// agreeing on 200 mangled characters to hold two entries) and the cwd check
// resolves it exactly.
func claudeProjectDirs(q StoredSessionQuery, workingDir string) ([]string, error) {
	configDir := claudeConfigDir(q)
	if configDir == "" {
		return nil, errSessionStoreAbsent
	}
	projects := filepath.Join(configDir, claudeProjectsDirName)
	mangled := mangleClaudePath(filepath.Clean(workingDir))
	if len(mangled) <= claudeMangleMaxLength {
		dir := filepath.Join(projects, mangled)
		if _, err := os.Stat(dir); err != nil {
			return nil, errSessionStoreAbsent
		}
		return []string{dir}, nil
	}

	prefix := mangled[:claudeMangleMaxLength] + "-"
	entries, err := os.ReadDir(projects)
	if err != nil {
		return nil, errSessionStoreAbsent
	}
	var dirs []string
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), prefix) {
			dirs = append(dirs, filepath.Join(projects, entry.Name()))
		}
	}
	if len(dirs) == 0 {
		return nil, errSessionStoreAbsent
	}
	return dirs, nil
}

// claudeTranscriptRecord is the union of the fields this reader takes from a
// transcript line. Every one is optional: the file holds several record types
// and each fills a different subset.
type claudeTranscriptRecord struct {
	Type        string `json:"type"`
	SessionID   string `json:"sessionId"`
	Cwd         string `json:"cwd"`
	IsSidechain bool   `json:"isSidechain"`
	// Title records. `ai-title` is what a current CLI writes; `summary` is the
	// legacy compaction record; `customTitle` is a title the user set.
	AITitle     string `json:"aiTitle"`
	CustomTitle string `json:"customTitle"`
	Summary     string `json:"summary"`
	LastPrompt  string `json:"lastPrompt"`
	Message     struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// claudeStoredSessions is Claude Code's Provider.ListStoredSessions.
func claudeStoredSessions(ctx context.Context, q StoredSessionQuery) ([]StoredSession, error) {
	workingDir := strings.TrimSpace(q.WorkingDir)
	if workingDir == "" {
		return nil, nil
	}
	dirs, err := claudeProjectDirs(q, workingDir)
	if err != nil {
		if errors.Is(err, errSessionStoreAbsent) {
			return nil, nil
		}
		return nil, err
	}

	limit := q.limit()
	var candidates []storeEntry
	for _, dir := range dirs {
		// UNCAPPED, and that is the point: only the `cwd` recorded INSIDE a
		// transcript says whether it belongs to this working directory, and the
		// mangling is lossy, so a cut taken here would spend the whole budget
		// on a colliding directory's newer sessions and report that this
		// directory has none. Copilot and Reasonix walk uncapped for the same
		// reason. `storedSessionScanCap` still limits each walk.
		found, err := newestEntries(dir, 0, entryItself(isClaudeTranscript))
		if err != nil {
			continue
		}
		candidates = append(candidates, found...)
	}
	// The merged list is only per-directory sorted, so it is ordered again
	// before the read: the loop below stops at `limit` ACCEPTED sessions, and
	// stopping over an unordered list would drop a newer session of the second
	// directory in favour of an older one of the first.
	candidates = sortAndCapEntries(candidates, 0)

	sessions := collectStoredSessions(ctx, candidates, limit, func(entry storeEntry) (StoredSession, bool) {
		return readClaudeSession(entry, workingDir)
	})
	return SortAndCapSessions(sessions, limit), nil
}

// isClaudeTranscript accepts the transcript FILES of a project directory.
//
// Directories are refused, which is what excludes the per-session sidecar tree:
// a session's subagent transcripts live in `<session id>/subagents/*.jsonl`, so
// a walk that took directories would offer a subagent as a resumable session.
func isClaudeTranscript(entry os.DirEntry) bool {
	return !entry.IsDir() && strings.HasSuffix(entry.Name(), ".jsonl")
}

// readClaudeSession derives one session from its transcript, and reports
// whether it belongs to `workingDir` at all.
//
// The cwd check is REQUIRED, not defensive: the project directory name is a
// lossy mangling of the working directory, so a directory legitimately holds
// sessions of other directories that mangle the same way.
func readClaudeSession(entry storeEntry, workingDir string) (StoredSession, bool) {
	head, atEOF, err := jsonlHead(entry.Path, jsonlProbeBytes)
	if err != nil || len(head) == 0 {
		return StoredSession{}, false
	}

	var (
		cwd         string
		sidechain   bool
		firstPrompt string
	)
	var headTitle claudeTitleCandidates
	for _, line := range head {
		var rec claudeTranscriptRecord
		if json.Unmarshal(line, &rec) != nil {
			continue
		}
		if cwd == "" && rec.Cwd != "" {
			cwd = rec.Cwd
		}
		if rec.IsSidechain {
			sidechain = true
		}
		headTitle.take(rec)
		if firstPrompt == "" && rec.Type == "user" {
			firstPrompt = contentBlockText(rec.Message.Content)
		}
	}
	// A transcript whose recorded cwd is another directory is another
	// directory's session. A transcript that records no cwd at all cannot be
	// placed, and offering it here would be a guess.
	if !sameDir(cwd, workingDir) {
		return StoredSession{}, false
	}
	// A sidechain transcript is a subagent's, not a session a user resumes.
	if sidechain {
		return StoredSession{}, false
	}

	// The newest title is at the END of the file: `ai-title` is appended again
	// every time the CLI regenerates it, so the head holds a stale one whenever
	// the title changed after the first 64 KB.
	//
	// Skipped when the head already reached the end of the file, because the
	// tail window would then re-open, re-read and re-parse the same bytes. The
	// result is identical either way -- `take` keeps the last value it sees for
	// each field, and over the same records it is idempotent.
	tailTitle := headTitle
	if !atEOF {
		if tail, err := jsonlTail(entry.Path, jsonlProbeBytes); err == nil {
			for _, line := range tail {
				var rec claudeTranscriptRecord
				if json.Unmarshal(line, &rec) != nil {
					continue
				}
				tailTitle.take(rec)
			}
		}
	}

	return StoredSession{
		// The file name is the session id, and it is the handle `--resume`
		// takes. Reading it from the name rather than from a record keeps a
		// transcript whose records this reader could not parse usable.
		Handle:    strings.TrimSuffix(entry.Name, ".jsonl"),
		Title:     trimTitle(tailTitle.best(firstPrompt)),
		UpdatedAt: entry.ModTime,
	}, true
}

// claudeTitleCandidates collects the title-bearing records seen so far. Each
// field keeps the LAST value seen, because the CLI appends a new record rather
// than rewriting the old one.
type claudeTitleCandidates struct {
	custom     string
	ai         string
	summary    string
	lastPrompt string
}

func (c *claudeTitleCandidates) take(rec claudeTranscriptRecord) {
	if rec.CustomTitle != "" {
		c.custom = rec.CustomTitle
	}
	if rec.AITitle != "" {
		c.ai = rec.AITitle
	}
	if rec.Summary != "" {
		c.summary = rec.Summary
	}
	if rec.LastPrompt != "" {
		c.lastPrompt = rec.LastPrompt
	}
}

// best states the title precedence: the title the user set, then the title the
// model wrote, then the legacy compaction summary, then the most recent prompt,
// then the first prompt of the session.
//
// The order is Claude's own, so the picker and the CLI's session list agree
// about what a session is called.
func (c claudeTitleCandidates) best(firstPrompt string) string {
	return firstNonBlank(c.custom, c.ai, c.summary, c.lastPrompt, firstPrompt)
}
