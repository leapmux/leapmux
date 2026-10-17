package reasonix

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/sessionstore"
	"github.com/leapmux/leapmux/util/pathutil"
)

// Reasonix writes one JSONL transcript per session and states that session's
// identity in sidecars beside it:
//
//   - `<id>.jsonl.meta` -- the listing record Reasonix's own picker reads.
//   - `<id>.acp.json`   -- present for a session started over ACP, which is how
//     LeapMux starts every one of them. It carries the cwd
//     that the `.meta` may omit.
//
// Sessions live under two roots, and both are read: a global
// `<home>/sessions`, and a per-workspace
// `<home>/projects/<workspace slug>/sessions`.

// reasonixSessionMetaSuffix is the listing sidecar's suffix.
const reasonixSessionMetaSuffix = ".jsonl.meta"

// reasonixACPSuffix is the ACP sidecar's suffix.
const reasonixACPSuffix = ".acp.json"

// reasonixSlugMaxLen is the byte budget Reasonix gives one path component
// before it truncates and appends a hash.
const reasonixSlugMaxLen = 255

// reasonixBranchMeta is the subset of Reasonix's `BranchMeta` this reader takes.
type reasonixBranchMeta struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	CustomTitle   string `json:"custom_title"`
	TopicTitle    string `json:"topic_title"`
	Preview       string `json:"preview"`
	ParentID      string `json:"parent_id"`
	WorkspaceRoot string `json:"workspace_root"`
	UpdatedAt     string `json:"updated_at"`
	CreatedAt     string `json:"created_at"`
	Turns         int    `json:"turns"`
}

// reasonixACPMeta is the subset of the ACP sidecar this reader takes.
type reasonixACPMeta struct {
	SessionID string `json:"sessionId"`
	Cwd       string `json:"cwd"`
	Title     string `json:"title"`
	UpdatedAt string `json:"updatedAt"`
}

// reasonixHome resolves Reasonix's state root.
func reasonixHome(q agent.StoredSessionQuery) string {
	if dir := strings.TrimSpace(q.Env("REASONIX_HOME")); dir != "" {
		return pathutil.ExpandHome(dir, q.Home())
	}
	if runtime.GOOS == "windows" {
		if appData := strings.TrimSpace(q.Env("APPDATA")); appData != "" {
			return filepath.Join(appData, "reasonix")
		}
	}
	home := q.Home()
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".reasonix")
}

// reasonixWorkspaceSlug follows Reasonix path encoding. Windows ignores path case. Every slash
// or backslash becomes a hyphen. Every colon becomes a hyphen also.
//
// Reasonix appends an FNV-1a hash beyond 255 bytes. This reader does not reproduce that
// truncated form. It instead uses the global root and checks the recorded working directory.
// Such a long slug already exceeds a normal filesystem component limit.
func reasonixWorkspaceSlug(absPath string) (string, bool) {
	if runtime.GOOS == "windows" {
		absPath = strings.ToLower(absPath)
	}
	slug := strings.NewReplacer(string(os.PathSeparator), "-", "/", "-", `\`, "-", ":", "-").Replace(absPath)
	if len(slug) > reasonixSlugMaxLen {
		return "", false
	}
	return slug, true
}

// reasonixSessionRoots lists the directories that may hold sessions for
// `workingDir`, most specific first.
func reasonixSessionRoots(q agent.StoredSessionQuery, workingDir string) []string {
	home := reasonixHome(q)
	if home == "" {
		return nil
	}
	roots := make([]string, 0, 2)
	if slug, ok := reasonixWorkspaceSlug(workingDir); ok {
		roots = append(roots, filepath.Join(home, "projects", slug, "sessions"))
	}
	roots = append(roots, filepath.Join(home, "sessions"))
	return roots
}

// reasonixStoredSessions is Reasonix's Provider.ListStoredSessions.
func reasonixStoredSessions(ctx context.Context, q agent.StoredSessionQuery) ([]agent.StoredSession, error) {
	workingDir := strings.TrimSpace(q.WorkingDir)
	if workingDir == "" {
		return nil, nil
	}
	roots := reasonixSessionRoots(q, workingDir)
	if len(roots) == 0 {
		return nil, nil
	}

	limit := q.EffectiveLimit()
	// One record per session id, because the same session can appear under
	// both roots. The project root is walked first and wins.
	seen := make(map[string]struct{}, limit)
	sessions := make([]agent.StoredSession, 0, limit)
	var firstErr error
	for _, root := range roots {
		// The roots share one accepted-session budget. The project root takes precedence. Once it
		// supplies a complete answer, skip the global root. Otherwise, the reader would scan and sort
		// that entire root before discarding its first candidate.
		if len(sessions) >= limit {
			break
		}
		if ctx.Err() != nil {
			break
		}
		// Read accepted sessions after scanning the candidates. The global root can contain other
		// working directories. Only each sidecar identifies its directory, so a cut before reading can
		// discard matching sessions.
		entries, err := sessionstore.NewestEntries(root, 0, sessionstore.EntryItself(isReasonixSessionMeta))
		if err != nil {
			if !errors.Is(err, sessionstore.ErrAbsent) && firstErr == nil {
				firstErr = err
			}
			continue
		}
		collected := sessionstore.Collect(ctx, entries, limit-len(sessions), func(entry sessionstore.Entry) (agent.StoredSession, bool) {
			session, ok := readReasonixSession(entry, workingDir)
			if !ok {
				return agent.StoredSession{}, false
			}
			if _, duplicate := seen[session.Handle]; duplicate {
				return agent.StoredSession{}, false
			}
			seen[session.Handle] = struct{}{}
			return session, true
		})
		sessions = append(sessions, collected...)
	}
	if len(sessions) == 0 && firstErr != nil {
		return nil, firstErr
	}
	return agent.SortAndCapSessions(sessions, limit), nil
}

// isReasonixSessionMeta accepts the listing sidecars.
func isReasonixSessionMeta(entry os.DirEntry) bool {
	return !entry.IsDir() && strings.HasSuffix(entry.Name(), reasonixSessionMetaSuffix)
}

// readReasonixSession derives one session from its sidecars.
func readReasonixSession(entry sessionstore.Entry, workingDir string) (agent.StoredSession, bool) {
	var meta reasonixBranchMeta
	if err := sessionstore.ReadSidecarFile(entry.Path, sessionstore.MaxSidecarBytes, func(data []byte) error {
		return json.Unmarshal(data, &meta)
	}); err != nil {
		return agent.StoredSession{}, false
	}
	// A branched session is not one a user resumes from this list.
	if strings.TrimSpace(meta.ParentID) != "" {
		return agent.StoredSession{}, false
	}
	// Reasonix's own lister drops a session with no turns, and so does this:
	// an empty session has nothing to resume into.
	if meta.Turns <= 0 {
		return agent.StoredSession{}, false
	}

	id := strings.TrimSpace(meta.ID)
	if id == "" {
		id = strings.TrimSuffix(entry.Name, reasonixSessionMetaSuffix)
	}

	// The `.meta` carries `workspace_root` only sometimes; the ACP sidecar
	// always carries `cwd`, and every session LeapMux starts has one.
	cwd := strings.TrimSpace(meta.WorkspaceRoot)
	var acp reasonixACPMeta
	acpPath := filepath.Join(filepath.Dir(entry.Path), id+reasonixACPSuffix)
	// A failed decode can leave earlier fields populated. Discard the complete ACP value on error.
	// No rejected sidecar field may supply the title or activity time.
	if err := sessionstore.ReadSidecarFile(acpPath, sessionstore.MaxSidecarBytes, func(data []byte) error {
		return json.Unmarshal(data, &acp)
	}); err != nil {
		acp = reasonixACPMeta{}
	}
	if cwd == "" {
		cwd = strings.TrimSpace(acp.Cwd)
	}
	// A session that states no working directory at all cannot be placed, and
	// offering it under this directory would be a guess.
	if !sessionstore.SameDir(cwd, workingDir) {
		return agent.StoredSession{}, false
	}

	return agent.StoredSession{
		Handle:    id,
		Title:     sessionstore.TrimTitle(reasonixTitle(meta, acp)),
		UpdatedAt: reasonixSessionTime(meta, acp, entry),
	}, true
}

// reasonixTitle selects the first available title in this order:
// - The user title.
// - The derived topic.
// - The branch name.
// - The ACP title.
// - The stored first-prompt preview.
func reasonixTitle(meta reasonixBranchMeta, acp reasonixACPMeta) string {
	return sessionstore.FirstNonBlank(meta.CustomTitle, meta.TopicTitle, meta.Name, acp.Title, meta.Preview)
}

// reasonixSessionTime is a Reasonix session's last activity: the sidecars'
// own timestamps, and the sidecar file's modification time when neither parses.
func reasonixSessionTime(meta reasonixBranchMeta, acp reasonixACPMeta, entry sessionstore.Entry) time.Time {
	for _, candidate := range []string{meta.UpdatedAt, acp.UpdatedAt, meta.CreatedAt} {
		if ts := sessionstore.ParseRFC3339(candidate); !ts.IsZero() {
			return ts
		}
	}
	return entry.ModTime
}
