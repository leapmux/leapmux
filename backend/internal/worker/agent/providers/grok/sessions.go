package grok

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/sessionstore"
)

// Grok keeps each session in a directory of its own, grouped by the working
// directory that the session ran in:
//
//	$GROK_HOME/sessions/<encoded cwd>/<session id>/summary.json
//
// The group name is the percent-encoded cwd. A name longer than 255 bytes is
// replaced by `<slug>-<hash>`, and a `.cwd` file in the group then states the
// cwd. summary.json states the id, the cwd, the titles and the times.

// grokSummaryFile is the listing record of one session.
const grokSummaryFile = "summary.json"

// grokCwdMarkerFile states the cwd of a group whose name is a hash.
const grokCwdMarkerFile = ".cwd"

// grokMaxGroupNameBytes is the limit of one path component, past which Grok
// hashes the group name.
const grokMaxGroupNameBytes = 255

// grokSummary is the part of summary.json that the picker reads.
type grokSummary struct {
	Info struct {
		ID  string `json:"id"`
		Cwd string `json:"cwd"`
	} `json:"info"`
	SessionKind    string `json:"session_kind"`
	Hidden         *bool  `json:"hidden"`
	NumMessages    int    `json:"num_messages"`
	WorktreeLabel  string `json:"worktree_label"`
	GeneratedTitle string `json:"generated_title"`
	SessionSummary string `json:"session_summary"`
	LastActiveAt   string `json:"last_active_at"`
	UpdatedAt      string `json:"updated_at"`
}

// grokHome resolves Grok's state directory: `$GROK_HOME`, else `~/.grok`.
func grokHome(q agent.StoredSessionQuery) string {
	return sessionstore.HomeDirFromEnv(q, "GROK_HOME", ".grok")
}

// grokEncodeCwd reproduces the `urlencoding` crate that gives a group its
// directory name: every byte except an RFC 3986 unreserved character becomes an
// uppercase `%XX`.
func grokEncodeCwd(cwd string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	b.Grow(len(cwd) * 3)
	for i := 0; i < len(cwd); i++ {
		c := cwd[i]
		if ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z') || ('0' <= c && c <= '9') ||
			c == '-' || c == '.' || c == '_' || c == '~' {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&0x0f])
	}
	return b.String()
}

// grokSessionGroups lists the group directories that can hold the sessions of
// workingDir.
//
// The plain name is a pure function of the cwd. The hashed name is not
// reproduced: Grok hashes with BLAKE3, which the worker does not link. The
// groups are few, so the reader instead finds a hashed group by the cwd that its
// `.cwd` marker states.
func grokSessionGroups(root, workingDir string) []string {
	encoded := grokEncodeCwd(workingDir)
	if len(encoded) <= grokMaxGroupNameBytes {
		return []string{filepath.Join(root, encoded)}
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	groups := make([]string, 0, 1)
	for _, entry := range entries {
		// A plain name starts with the encoded `/` or drive letter, and never
		// carries a marker. A symlink is not a group that Grok wrote.
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), "%") {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		var marker string
		if err := sessionstore.ReadSidecarFile(filepath.Join(dir, grokCwdMarkerFile), int64(len(workingDir))+64, func(data []byte) error {
			marker = strings.TrimSpace(string(data))
			return nil
		}); err != nil {
			continue
		}
		if marker == workingDir {
			groups = append(groups, dir)
		}
	}
	return groups
}

// grokStoredSessions is Grok's Provider.ListStoredSessions.
func grokStoredSessions(ctx context.Context, q agent.StoredSessionQuery) ([]agent.StoredSession, error) {
	workingDir := grokTrimCwd(q.WorkingDir)
	if workingDir == "" {
		return nil, nil
	}
	home := grokHome(q)
	if home == "" {
		return nil, nil
	}
	root := filepath.Join(home, "sessions")
	limit := q.EffectiveLimit()
	// Grok's own picker also lists the sessions of the resolved path. This one
	// does not: Grok resumes a session only under the cwd string that it stored,
	// and LeapMux resumes under the working directory, so such a session would
	// fail to open.
	seen := make(map[string]struct{}, limit)
	sessions := make([]agent.StoredSession, 0, limit)
	var firstErr error
	for _, group := range grokSessionGroups(root, workingDir) {
		if ctx.Err() != nil {
			break
		}
		entries, err := sessionstore.NewestEntries(group, 0, sessionstore.NamedFileInside(grokSummaryFile))
		if err != nil {
			if !errors.Is(err, sessionstore.ErrAbsent) && firstErr == nil {
				firstErr = err
			}
			continue
		}
		// One id in two groups is a copy; Grok's own picker skips the second.
		for _, session := range sessionstore.Collect(ctx, entries, limit, func(entry sessionstore.Entry) (agent.StoredSession, bool) {
			return readGrokSession(entry, workingDir)
		}) {
			if _, dup := seen[session.Handle]; dup {
				continue
			}
			seen[session.Handle] = struct{}{}
			sessions = append(sessions, session)
		}
	}
	if len(sessions) == 0 && firstErr != nil {
		return nil, firstErr
	}
	return agent.SortAndCapSessions(sessions, limit), nil
}

// readGrokSession derives one session from its summary, or rejects it.
func readGrokSession(entry sessionstore.Entry, workingDir string) (agent.StoredSession, bool) {
	// A dot-name is Grok's own scratch, never a session.
	if strings.HasPrefix(entry.Name, ".") {
		return agent.StoredSession{}, false
	}
	var summary grokSummary
	if err := sessionstore.ReadSidecarFile(filepath.Join(entry.Path, grokSummaryFile), sessionstore.MaxSidecarBytes, func(data []byte) error {
		return json.Unmarshal(data, &summary)
	}); err != nil {
		return agent.StoredSession{}, false
	}
	id := strings.TrimSpace(summary.Info.ID)
	// Grok resumes a session only under the cwd string that it stored, so a
	// group that merely holds the session is not enough.
	if id == "" || !sessionstore.SameDir(grokTrimCwd(summary.Info.Cwd), workingDir) || grokSessionHidden(summary) {
		return agent.StoredSession{}, false
	}
	title := strings.TrimSpace(sessionstore.FirstNonBlank(summary.GeneratedTitle, summary.SessionSummary))
	// A session that never ran a turn is a husk: Grok's own picker drops it,
	// unless it is a fork or a worktree that the user made on purpose.
	if summary.NumMessages == 0 && title == "" && summary.WorktreeLabel == "" &&
		summary.SessionKind != "worktree" && summary.SessionKind != "fork" {
		return agent.StoredSession{}, false
	}
	return agent.StoredSession{
		Handle:    id,
		Title:     sessionstore.TrimTitle(title),
		UpdatedAt: grokSessionTime(summary, entry),
	}, true
}

// grokTrimCwd drops the trailing separators that Grok trims before it groups a
// session, and keeps a root directory whole. It does not clean the path any
// further, because Grok does not.
func grokTrimCwd(cwd string) string {
	cwd = strings.TrimSpace(cwd)
	trimmed := strings.TrimRight(cwd, string(os.PathSeparator))
	if trimmed == "" && cwd != "" {
		return cwd[:1]
	}
	return trimmed
}

// grokSessionHidden applies Grok's own rule. An explicit `hidden` wins. Without
// one, a subagent's session (`subagent`, `subagent_fork`, `subagent_resume`)
// is hidden. A one-shot `grok -p` session is hidden too, as the TUI picker
// hides it: it has no conversation that a user continues.
func grokSessionHidden(summary grokSummary) bool {
	if summary.Hidden != nil {
		return *summary.Hidden
	}
	return strings.HasPrefix(summary.SessionKind, "subagent") || summary.SessionKind == "headless"
}

// grokSessionTime is a session's last activity: its last turn, then its last
// summary write, then the summary file's modification time.
func grokSessionTime(summary grokSummary, entry sessionstore.Entry) time.Time {
	for _, candidate := range []string{summary.LastActiveAt, summary.UpdatedAt} {
		if ts := sessionstore.ParseRFC3339(candidate); !ts.IsZero() {
			return ts
		}
	}
	return entry.ModTime
}
