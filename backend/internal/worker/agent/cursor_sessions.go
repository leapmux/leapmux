package agent

import (
	"context"
	"crypto/md5" //nolint:gosec // Not a security primitive: this reproduces Cursor's own directory-naming hash.
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"time"
)

// Cursor keeps ONE SQLite database per session, at
// `~/.cursor/chats/<md5 of cwd>/<agent id>/store.db`.
//
// That layout is unusual but convenient here: the directory holding a working
// directory's sessions is addressed by a hash of the path, so this reader
// computes one hash and reads one directory rather than scanning a store.
//
// Each `store.db` holds two tables. `blobs` is the transcript, which nothing
// here reads. `meta` holds one row under key `'0'` whose value is the session's
// metadata -- as JSON, HEX-ENCODED into a TEXT column.

// cursorChatsDirName holds one directory per working directory.
const cursorChatsDirName = "chats"

// cursorStoreFileName is the per-session database.
const cursorStoreFileName = "store.db"

// cursorMetaKey is the `meta` row holding a session's metadata.
const cursorMetaKey = "0"

// cursorSessionMeta is the subset of that row this reader takes.
type cursorSessionMeta struct {
	AgentID string `json:"agentId"`
	Name    string `json:"name"`
	// CreatedAt is epoch milliseconds. There is no updated-at counterpart, so
	// the reader orders by the store file's modification time and keeps this
	// only as the fallback.
	CreatedAt int64 `json:"createdAt"`
	// SubagentInfo is present ONLY on a subagent's session. Cursor stores a
	// subagent as a sibling directory of the session that spawned it, so this
	// field is the only thing that tells the two apart.
	SubagentInfo json.RawMessage `json:"subagentInfo"`
}

// cursorHome resolves Cursor's state directory. Cursor offers no environment
// override for it.
func cursorHome(q StoredSessionQuery) string {
	home := q.home()
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".cursor")
}

// cursorChatsDirFor reproduces Cursor's directory name for a working
// directory: the lowercase hex MD5 of the path.
//
// MD5 is Cursor's choice, and this code has to reproduce it to find the
// directory. It is a naming function here, never a security one.
func cursorChatsDirFor(workingDir string) string {
	sum := md5.Sum([]byte(workingDir)) //nolint:gosec // See above: reproduces Cursor's directory name.
	return hex.EncodeToString(sum[:])
}

// cursorStoredSessions is Cursor's Provider.ListStoredSessions.
func cursorStoredSessions(ctx context.Context, q StoredSessionQuery) ([]StoredSession, error) {
	workingDir := strings.TrimSpace(q.WorkingDir)
	if workingDir == "" {
		return nil, nil
	}
	home := cursorHome(q)
	if home == "" {
		return nil, nil
	}
	// filepath.Clean before hashing: the hash is of the exact bytes Cursor
	// passed, and Cursor passes its own `process.cwd()`, which is clean.
	dir := filepath.Join(home, cursorChatsDirName, cursorChatsDirFor(filepath.Clean(workingDir)))

	limit := q.limit()
	// Capped at the limit: unlike Copilot, the directory ALREADY answers "is
	// this session for this working directory", so the newest few are the
	// answer and reading further only opens databases that cannot change it.
	//
	// `namedFileInside` is what times a candidate by its `store.db` rather than
	// by the session DIRECTORY. Its own comment holds the reason: reading a
	// session opens its database, and a read-only open creates the `-shm`
	// sidecar inside that directory, so the directory's time reports the moment
	// LeapMux looked. `store.db` is the file Cursor writes and the read-only
	// open leaves alone.
	entries, err := newestEntries(dir, limit, namedFileInside(cursorStoreFileName))
	if err != nil {
		if errors.Is(err, errSessionStoreAbsent) {
			return nil, nil
		}
		return nil, err
	}

	// This reader opens one database per candidate, which is why the shared
	// collector checks the context between candidates and not only inside a
	// query.
	sessions := collectStoredSessions(ctx, entries, limit, func(entry storeEntry) (StoredSession, bool) {
		return readCursorSession(ctx, entry)
	})
	return SortAndCapSessions(sessions, limit), nil
}

// readCursorSession opens one session's database and reads its metadata row.
func readCursorSession(ctx context.Context, entry storeEntry) (StoredSession, bool) {
	storePath := filepath.Join(entry.Path, cursorStoreFileName)
	db, err := openSessionStoreDB(ctx, storePath)
	if err != nil {
		return StoredSession{}, false
	}
	defer func() { _ = db.Close() }()

	var raw string
	if err := db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, cursorMetaKey).Scan(&raw); err != nil {
		return StoredSession{}, false
	}
	meta, ok := decodeCursorMeta(raw)
	if !ok {
		return StoredSession{}, false
	}
	// A subagent is not a session a user resumes.
	if len(meta.SubagentInfo) > 0 && string(meta.SubagentInfo) != "null" {
		return StoredSession{}, false
	}

	handle := strings.TrimSpace(meta.AgentID)
	if handle == "" {
		// The directory name is the agent id, so a metadata row that lost it
		// is still resumable.
		handle = entry.Name
	}

	return StoredSession{
		Handle: handle,
		// Cursor leaves a session called "New Agent" until something renames
		// it, and that string is a truthful answer -- the CLI shows it too.
		Title:     trimTitle(meta.Name),
		UpdatedAt: cursorSessionTime(entry, meta),
	}, true
}

// decodeCursorMeta reads the metadata row.
//
// The stored value is the JSON hex-encoded into a TEXT column, so it is decoded
// twice. A value that is not hex is tried as JSON directly, so a future version
// that drops the encoding still reads here rather than reporting no sessions.
func decodeCursorMeta(raw string) (cursorSessionMeta, bool) {
	var meta cursorSessionMeta
	if decoded, err := hex.DecodeString(strings.TrimSpace(raw)); err == nil {
		if json.Unmarshal(decoded, &meta) == nil {
			return meta, true
		}
	}
	if json.Unmarshal([]byte(raw), &meta) == nil {
		return meta, true
	}
	return cursorSessionMeta{}, false
}

// cursorSessionTime is a Cursor session's last activity.
//
// Cursor's metadata records only a creation time, so the answer is `store.db`'s
// modification time, which `namedFileInside(cursorStoreFileName)` already took.
// That helper's comment states why neither the session directory nor the `-wal`
// can supply it.
//
// The cost of using `store.db` alone is real and accepted: SQLite moves the
// main file when it checkpoints, so a session written to seconds ago and not
// yet checkpointed reads slightly old. A time that lags is a smaller defect
// than a time LeapMux overwrites with the moment it looked.
//
// The zero-time branch is DEFENSIVE, not a path a supported filesystem takes:
// the walk keeps a candidate only when its `store.db` stat succeeded, and a
// successful stat cannot report a modification time of January 1 of year 1. It
// stays because the metadata's creation time is the only other answer the store
// holds, and a future selector that leaves the time unset must not sort every
// session to the end.
func cursorSessionTime(entry storeEntry, meta cursorSessionMeta) time.Time {
	if !entry.ModTime.IsZero() {
		return entry.ModTime
	}
	return epochMillis(meta.CreatedAt)
}
