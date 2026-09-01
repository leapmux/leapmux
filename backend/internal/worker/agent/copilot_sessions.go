package agent

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	// The fork at the maintained import path, which koanf's YAML parser already
	// makes a dependency of this module. `gopkg.in/yaml.v3` exposes the same
	// Unmarshal and the same tag rules, so the choice is which of two copies of
	// one project this repo's own code depends on -- and one is enough.
	"go.yaml.in/yaml/v3"
)

// GitHub Copilot keeps one directory per session at
// `<copilot home>/session-state/<session id>/`, and states that session's
// identity in a `workspace.yaml` sidecar beside its event log.
//
// The sidecar is the source, not the `session-store.db` SQLite index in the
// same home. That index is incomplete -- it held 64 rows against 115 session
// directories on the machine this was written against -- while `workspace.yaml`
// is the record the CLI itself reads back when it resumes a session.

// copilotHomeDirName is the directory Copilot keeps under the user's home.
const copilotHomeDirName = ".copilot"

// copilotSessionStateDirName holds one directory per session.
const copilotSessionStateDirName = "session-state"

// copilotWorkspaceFileName is the per-session sidecar.
const copilotWorkspaceFileName = "workspace.yaml"

// copilotWorkspace is the subset of `workspace.yaml` this reader takes.
type copilotWorkspace struct {
	ID        string `yaml:"id"`
	Cwd       string `yaml:"cwd"`
	Name      string `yaml:"name"`
	CreatedAt string `yaml:"created_at"`
	UpdatedAt string `yaml:"updated_at"`
}

// copilotHome resolves Copilot's state directory.
//
// COPILOT_HOME wins. XDG_STATE_HOME is deliberately NOT consulted: Copilot
// treats it as a MIGRATION source, moving `session-state` out of it into
// `~/.copilot` on startup, so the XDG path points at where the store was rather
// than where it is.
func copilotHome(q StoredSessionQuery) string {
	return homeDirFromEnv(q, "COPILOT_HOME", copilotHomeDirName)
}

// copilotStoredSessions is GitHub Copilot's Provider.ListStoredSessions.
//
// Every session directory has to be read, because the working directory is
// inside the sidecar rather than in the directory name -- so unlike Claude and
// Pi there is no path to compute and stat. Two limits restrict the walk: the
// newest-first stat sort puts the plausible candidates first, and the read
// stops once `limit` matching sessions are found.
func copilotStoredSessions(ctx context.Context, q StoredSessionQuery) ([]StoredSession, error) {
	workingDir := strings.TrimSpace(q.WorkingDir)
	if workingDir == "" {
		return nil, nil
	}
	home := copilotHome(q)
	if home == "" {
		return nil, nil
	}
	root := filepath.Join(home, copilotSessionStateDirName)

	// No limit on the walk: `workspace.yaml` states which working directory a
	// session belongs to, so cutting the list before reading would drop older
	// sessions of THIS directory in favour of newer ones of another.
	//
	// Timed by `workspace.yaml`, not by the session directory. A directory's
	// modification time changes only when a file appears in it or leaves it, so
	// it tracks a session's CREATION and never its use -- and the migration out
	// of the XDG location stamped whole groups of directories with the single
	// time of the move. One real store holds four directories that share one
	// timestamp while the sessions inside them span eight days.
	entries, err := newestEntries(root, 0, namedFileInside(copilotWorkspaceFileName))
	if err != nil {
		if errors.Is(err, errSessionStoreAbsent) {
			return nil, nil
		}
		return nil, err
	}

	limit := q.limit()
	sessions := collectStoredSessions(ctx, entries, limit, func(entry storeEntry) (StoredSession, bool) {
		return readCopilotSession(entry, workingDir)
	})
	return SortAndCapSessions(sessions, limit), nil
}

// readCopilotSession derives one session from its `workspace.yaml`.
func readCopilotSession(entry storeEntry, workingDir string) (StoredSession, bool) {
	var ws copilotWorkspace
	path := filepath.Join(entry.Path, copilotWorkspaceFileName)
	if err := readSidecarFile(path, maxSidecarBytes, func(data []byte) error {
		return yaml.Unmarshal(data, &ws)
	}); err != nil {
		return StoredSession{}, false
	}
	if !sameDir(ws.Cwd, workingDir) {
		return StoredSession{}, false
	}

	// The directory name is the session id, and the sidecar repeats it. The
	// sidecar wins where they disagree, because it is what the CLI reads.
	handle := strings.TrimSpace(ws.ID)
	if handle == "" {
		handle = entry.Name
	}

	updated := parseRFC3339(ws.UpdatedAt)
	if updated.IsZero() {
		updated = parseRFC3339(ws.CreatedAt)
	}
	if updated.IsZero() {
		updated = entry.ModTime
	}

	return StoredSession{
		Handle: handle,
		// `name` is the CLI's own label: the first prompt until the model
		// renames the session, and whatever the user typed once `user_named`
		// is set. Either way it is the string Copilot itself shows.
		Title:     trimTitle(ws.Name),
		UpdatedAt: updated,
	}, true
}
