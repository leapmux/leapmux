package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/leapmux/leapmux/internal/util/pathutil"
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
func reasonixHome(q StoredSessionQuery) string {
	if dir := strings.TrimSpace(q.env("REASONIX_HOME")); dir != "" {
		return pathutil.ExpandHome(dir, q.home())
	}
	if runtime.GOOS == "windows" {
		if appData := strings.TrimSpace(q.env("APPDATA")); appData != "" {
			return filepath.Join(appData, "reasonix")
		}
	}
	home := q.home()
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".reasonix")
}

// reasonixWorkspaceSlug reproduces Reasonix's `WorkspaceSlug`: fold case on
// Windows, then replace every `/`, `\` and `:` with a hyphen.
//
// The truncated form is NOT reproduced. Reasonix appends an FNV-1a hash past
// 255 bytes, and rather than reimplement that, an over-long slug simply yields
// no project directory -- the global root still lists those sessions, and the
// cwd check places them. A working directory whose slug passes 255 bytes is far
// past what a filesystem accepts for a single component anyway.
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
func reasonixSessionRoots(q StoredSessionQuery, workingDir string) []string {
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
func reasonixStoredSessions(ctx context.Context, q StoredSessionQuery) ([]StoredSession, error) {
	workingDir := strings.TrimSpace(q.WorkingDir)
	if workingDir == "" {
		return nil, nil
	}
	roots := reasonixSessionRoots(q, workingDir)
	if len(roots) == 0 {
		return nil, nil
	}

	limit := q.limit()
	// One record per session id, because the same session can appear under
	// both roots. The project root is walked first and wins.
	seen := make(map[string]struct{}, limit)
	sessions := make([]StoredSession, 0, limit)
	var firstErr error
	for _, root := range roots {
		// The budget is shared across the roots, and it is checked HERE as well
		// as inside. The project root is walked first, so once it supplies a
		// full answer the global root -- which holds every session on the
		// machine and is walked uncapped -- would otherwise be read and sorted
		// in full and then discarded on its first candidate.
		if len(sessions) >= limit {
			break
		}
		if ctx.Err() != nil {
			break
		}
		// Unlimited walk, then a capped read: only the sidecar says which
		// working directory a session in the GLOBAL root belongs to, so a cut
		// before reading would drop this directory's sessions in favour of
		// another's.
		entries, err := newestEntries(root, 0, entryItself(isReasonixSessionMeta))
		if err != nil {
			if !errors.Is(err, errSessionStoreAbsent) && firstErr == nil {
				firstErr = err
			}
			continue
		}
		for _, entry := range entries {
			if len(sessions) >= limit {
				break
			}
			session, ok := readReasonixSession(entry, workingDir)
			if !ok {
				continue
			}
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
	return SortAndCapSessions(sessions, limit), nil
}

// isReasonixSessionMeta accepts the listing sidecars.
func isReasonixSessionMeta(entry os.DirEntry) bool {
	return !entry.IsDir() && strings.HasSuffix(entry.Name(), reasonixSessionMetaSuffix)
}

// readReasonixSession derives one session from its sidecars.
func readReasonixSession(entry storeEntry, workingDir string) (StoredSession, bool) {
	var meta reasonixBranchMeta
	if err := readSidecarFile(entry.Path, maxSidecarBytes, func(data []byte) error {
		return json.Unmarshal(data, &meta)
	}); err != nil {
		return StoredSession{}, false
	}
	// A branched session is not one a user resumes from this list.
	if strings.TrimSpace(meta.ParentID) != "" {
		return StoredSession{}, false
	}
	// Reasonix's own lister drops a session with no turns, and so does this:
	// an empty session has nothing to resume into.
	if meta.Turns <= 0 {
		return StoredSession{}, false
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
	// A decode that failed leaves `acp` PARTIALLY populated, because the decoder
	// writes each field it reads before it reports a type error on a later one.
	// The whole struct is discarded, so nothing from a document this reader
	// rejected can reach the title or the time further down.
	if err := readSidecarFile(acpPath, maxSidecarBytes, func(data []byte) error {
		return json.Unmarshal(data, &acp)
	}); err != nil {
		acp = reasonixACPMeta{}
	}
	if cwd == "" {
		cwd = strings.TrimSpace(acp.Cwd)
	}
	// A session that states no working directory at all cannot be placed, and
	// offering it under this directory would be a guess.
	if !sameDir(cwd, workingDir) {
		return StoredSession{}, false
	}

	return StoredSession{
		Handle:    id,
		Title:     trimTitle(reasonixTitle(meta, acp)),
		UpdatedAt: reasonixSessionTime(meta, acp, entry),
	}, true
}

// reasonixTitle states the title precedence: the title the user set, then the
// topic the model derived, then the branch name, then the ACP sidecar's own
// title, then the stored preview of the first prompt.
func reasonixTitle(meta reasonixBranchMeta, acp reasonixACPMeta) string {
	return firstNonBlank(meta.CustomTitle, meta.TopicTitle, meta.Name, acp.Title, meta.Preview)
}

// reasonixSessionTime is a Reasonix session's last activity: the sidecars'
// own timestamps, and the sidecar file's modification time when neither parses.
func reasonixSessionTime(meta reasonixBranchMeta, acp reasonixACPMeta, entry storeEntry) time.Time {
	for _, candidate := range []string{meta.UpdatedAt, acp.UpdatedAt, meta.CreatedAt} {
		if ts := parseRFC3339(candidate); !ts.IsZero() {
			return ts
		}
	}
	return entry.ModTime
}
