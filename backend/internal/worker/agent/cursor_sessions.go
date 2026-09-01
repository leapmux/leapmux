package agent

import (
	"context"
	"crypto/md5" //nolint:gosec // Not a security primitive: this reproduces Cursor's own directory-naming hash.
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
	entries, err := cursorSessionCandidates(dir, limit)
	if err != nil {
		if errors.Is(err, errSessionStoreAbsent) {
			return nil, nil
		}
		return nil, err
	}

	sessions := make([]StoredSession, 0, len(entries))
	for _, entry := range entries {
		session, ok := readCursorSession(ctx, entry)
		if !ok {
			continue
		}
		sessions = append(sessions, session)
	}
	return sortAndCapSessions(sessions, limit), nil
}

// cursorSessionCandidates lists the session directories under `dir`, newest
// first, timed by each session's `store.db`.
//
// It does NOT use `newestEntries`, and the difference is the whole point: that
// helper times an entry by the entry's own mtime, which for Cursor is the
// session DIRECTORY. Reading a session opens its database, and a read-only open
// of a WAL database still creates the `-shm` sidecar inside that directory --
// which updates the directory's mtime to the moment LeapMux listed it.
// `TestOpenReadOnly_CreatesShmButLeavesTheDatabaseFile` pins that behaviour.
//
// So the directory time reports LeapMux's own footprint, not Cursor's writing:
// every session collapses to one timestamp, the list loses its order, and every
// row reads "0s ago". `store.db` is the file Cursor writes and the read-only
// open leaves alone, so it is the only honest signal here. Its `-wal` is not
// usable either -- the same open creates that file when it is absent.
func cursorSessionCandidates(dir string, limit int) ([]storeEntry, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, errSessionStoreAbsent
		}
		return nil, fmt.Errorf("read cursor chats directory: %w", err)
	}
	found := make([]storeEntry, 0, min(len(entries), storedSessionScanCap))
	for _, entry := range entries {
		if len(found) >= storedSessionScanCap {
			break
		}
		if !entry.IsDir() {
			continue
		}
		sessionDir := filepath.Join(dir, entry.Name())
		// A stat, not an open: this runs for every session in the directory and
		// only orders them. The databases are opened after the cut.
		info, err := os.Stat(filepath.Join(sessionDir, cursorStoreFileName))
		if err != nil {
			continue
		}
		found = append(found, storeEntry{Path: sessionDir, Name: entry.Name(), ModTime: info.ModTime()})
	}
	sort.SliceStable(found, func(i, j int) bool {
		if !found[i].ModTime.Equal(found[j].ModTime) {
			return found[i].ModTime.After(found[j].ModTime)
		}
		return found[i].Name < found[j].Name
	})
	if limit > 0 && len(found) > limit {
		found = found[:limit]
	}
	return found, nil
}

// readCursorSession opens one session's database and reads its metadata row.
func readCursorSession(ctx context.Context, entry storeEntry) (StoredSession, bool) {
	storePath := filepath.Join(entry.Path, cursorStoreFileName)
	db, err := openSessionStoreDB(storePath)
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
// modification time, which `cursorSessionCandidates` already took. That comment
// states why neither the session directory nor the `-wal` can supply it.
//
// The cost of using `store.db` alone is real and accepted: SQLite moves the
// main file when it checkpoints, so a session written to seconds ago and not
// yet checkpointed reads slightly old. A time that lags is a smaller defect
// than a time LeapMux overwrites with the moment it looked.
func cursorSessionTime(entry storeEntry, meta cursorSessionMeta) time.Time {
	if !entry.ModTime.IsZero() {
		return entry.ModTime
	}
	return epochMillis(meta.CreatedAt)
}
