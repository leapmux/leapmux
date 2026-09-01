package agent

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/leapmux/leapmux/internal/util/sqlitedb"
)

// This file holds the provider-NEUTRAL half of session-store discovery: the
// record shape, the query shape, and the readers every provider's own file
// shares (a read-only SQLite handle, a newest-first directory walk, a bounded
// JSONL read). Which directory to walk, which table to query and where the
// title lives are provider decisions, so they live in that provider's own file
// behind Provider.ListStoredSessions.
//
// Every store here belongs to another program. Nothing in this file or its
// callers may write to one, and a store that is absent, unreadable, or shaped
// differently than the version this code was written against means "no
// sessions from this provider" -- never a failed RPC.

// DefaultStoredSessionLimit caps how many sessions one provider's reader
// collects when the caller states no limit of its own. It bounds the I/O of a
// scan, not just the response: a reader stats its candidates, sorts them, and
// only then opens the newest few.
const DefaultStoredSessionLimit = 50

// storedSessionScanCap limits how many CANDIDATES a directory-walking reader
// stats before it sorts. A user with years of history has tens of thousands of
// session files, and the picker needs the newest few; without this cap the walk
// grows without limit while the answer does not change.
const storedSessionScanCap = 2000

// jsonlProbeBytes is how much of a JSONL session file a reader looks at from
// each end. Claude's own lister uses the same shape (first and last 64 KB),
// because a session's identity is in the first records and its latest title is
// in the last ones, while the middle is a transcript nothing here reads.
const jsonlProbeBytes = 64 * 1024

// errSessionStoreAbsent reports that this provider keeps no store on this
// machine. That is the NORMAL state for a CLI the user never ran, so a reader
// turns it into an empty result and a caller must not log it as a failure.
var errSessionStoreAbsent = errors.New("session store not present")

// StoredSession is one resumable session that a provider's own storage holds.
type StoredSession struct {
	// Handle is the resume handle, in the SAME form the provider reports at
	// runtime through UpdateSessionID.
	//
	// That equality is what lets the caller dedupe this record against
	// `agents.agent_session_id` with a plain string compare and no per-provider
	// key function. Pi is the case that fixes the rule: it identifies one
	// session by an ID and by a file path, and `pi --session` takes either, so
	// its reader must return the ID -- the form the running process reports --
	// and not the path it found the session at.
	Handle string
	// Title is a human-readable summary, or empty when the provider stores
	// none. The caller shows the handle in its place rather than inventing one.
	Title string
	// UpdatedAt is the last activity. The zero value means the store gave no
	// answer, and sorts last.
	UpdatedAt time.Time
}

// StoredSessionQuery states which sessions a reader must collect.
type StoredSessionQuery struct {
	// WorkingDir is the directory a session must have run in, compared
	// EXACTLY. Every store here records the session's cwd (some as a mangled
	// directory name, which is why the comparison belongs to each reader), and
	// a session picker offered inside a directory answers for that directory.
	WorkingDir string
	// HomeDir locates a store under the user's home. Empty falls back to the
	// process's own home directory.
	HomeDir string
	// Getenv reads the environment that locates a store (CODEX_HOME,
	// XDG_DATA_HOME, ...). Nil means os.Getenv.
	//
	// The worker's own environment is the right answer, not a stored copy:
	// FinalizeAgentEnv deliberately PRESERVES every home/config-dir variable
	// when it spawns an agent, so what this process sees is what the CLI sees.
	Getenv func(string) string
	// Limit caps the returned records. Zero means DefaultStoredSessionLimit.
	Limit int
}

// env reads one environment variable through the query's seam.
func (q StoredSessionQuery) env(key string) string {
	if q.Getenv != nil {
		return q.Getenv(key)
	}
	return os.Getenv(key)
}

// home is the directory to resolve a store path against.
func (q StoredSessionQuery) home() string {
	if q.HomeDir != "" {
		return q.HomeDir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}

// limit is the query's cap, defaulted.
func (q StoredSessionQuery) limit() int {
	if q.Limit > 0 {
		return q.Limit
	}
	return DefaultStoredSessionLimit
}

// xdgDataHome resolves the XDG data directory the way the `xdg-basedir` npm
// package does, which is what OpenCode, Kilo and Goose are built on: it reads
// XDG_DATA_HOME and falls back to `~/.local/share` on EVERY platform, macOS
// included. Resolving to `~/Library/Application Support` there would look more
// native and find nothing.
func (q StoredSessionQuery) xdgDataHome() string {
	if dir := strings.TrimSpace(q.env("XDG_DATA_HOME")); dir != "" {
		return dir
	}
	home := q.home()
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".local", "share")
}

// openSessionStoreDB opens another program's SQLite session store for reading.
//
// The os.Stat comes first so an absent store is reported as
// errSessionStoreAbsent rather than as a driver error: `mode=ro` refuses to
// create the file, but its message describes a failure and this is not one.
func openSessionStoreDB(path string) (*sql.DB, error) {
	if path == "" {
		return nil, errSessionStoreAbsent
	}
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, errSessionStoreAbsent
		}
		return nil, fmt.Errorf("stat session store: %w", err)
	}
	db, err := sqlitedb.OpenReadOnly(path)
	if err != nil {
		return nil, fmt.Errorf("open session store: %w", err)
	}
	return db, nil
}

// sortAndCapSessions orders newest first and truncates to `limit`.
//
// The handle breaks a timestamp tie, so a store whose timestamps have
// one-second resolution (Goose) still produces one stable order rather than
// whatever the scan happened to yield. Records with no handle are dropped:
// a row this code cannot resume is not a choice to offer.
func sortAndCapSessions(sessions []StoredSession, limit int) []StoredSession {
	// A fresh slice, not `sessions[:0]`. Filtering in place would overwrite the
	// caller's backing array, so a reader that kept a reference to what it
	// passed would find it rewritten. At these sizes the copy costs nothing,
	// and it makes that mistake impossible rather than merely absent today.
	kept := make([]StoredSession, 0, len(sessions))
	for _, s := range sessions {
		if strings.TrimSpace(s.Handle) == "" {
			continue
		}
		kept = append(kept, s)
	}
	sort.SliceStable(kept, func(i, j int) bool {
		if !kept[i].UpdatedAt.Equal(kept[j].UpdatedAt) {
			return kept[i].UpdatedAt.After(kept[j].UpdatedAt)
		}
		return kept[i].Handle < kept[j].Handle
	})
	if limit > 0 && len(kept) > limit {
		kept = kept[:limit]
	}
	return kept
}

// storeEntry is one candidate a directory-walking reader found, carried with
// the modification time that orders it.
type storeEntry struct {
	Path    string
	Name    string
	ModTime time.Time
}

// newestEntries lists the entries of `dir` that `keep` accepts, newest first,
// truncated to `limit`.
//
// The stat-then-sort order is the point: a JSONL store has no index, so the
// only cheap recency signal is the modification time, and reading the files to
// find out which are recent would read every file in the directory. An entry
// whose stat fails is dropped rather than sorted to the epoch, so a file
// deleted mid-walk cannot displace a real answer.
func newestEntries(dir string, limit int, keep func(os.DirEntry) bool) ([]storeEntry, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, errSessionStoreAbsent
		}
		return nil, fmt.Errorf("read session store directory: %w", err)
	}
	found := make([]storeEntry, 0, min(len(entries), storedSessionScanCap))
	for _, entry := range entries {
		if len(found) >= storedSessionScanCap {
			break
		}
		if keep != nil && !keep(entry) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		found = append(found, storeEntry{
			Path:    filepath.Join(dir, entry.Name()),
			Name:    entry.Name(),
			ModTime: info.ModTime(),
		})
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

// jsonlHead returns the complete lines within the first `maxBytes` of a file.
//
// A trailing PARTIAL line is dropped, because the caller unmarshals what it
// gets and half a JSON object is not a record. When the whole file fits, the
// last line is complete by definition and is kept.
func jsonlHead(path string, maxBytes int64) ([][]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	buf := make([]byte, maxBytes)
	n, err := io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, err
	}
	buf = buf[:n]
	atEOF := int64(n) < maxBytes
	return splitJSONLines(buf, !atEOF, false), nil
}

// jsonlTail returns the complete lines within the last `maxBytes` of a file.
//
// The leading partial line is dropped, for the same reason jsonlHead drops the
// trailing one. When the file is shorter than the window the whole file is
// read, and then the first line is complete and is kept.
func jsonlTail(path string, maxBytes int64) ([][]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := info.Size()
	start := int64(0)
	truncated := false
	if size > maxBytes {
		start = size - maxBytes
		truncated = true
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil, err
	}
	buf, err := io.ReadAll(io.LimitReader(f, maxBytes))
	if err != nil {
		return nil, err
	}
	return splitJSONLines(buf, false, truncated), nil
}

// splitJSONLines splits a byte window into non-empty lines, dropping the
// partial line at whichever end the window was cut.
func splitJSONLines(buf []byte, dropLast, dropFirst bool) [][]byte {
	lines := bytes.Split(buf, []byte("\n"))
	if dropLast && len(lines) > 0 {
		lines = lines[:len(lines)-1]
	}
	if dropFirst && len(lines) > 0 {
		lines = lines[1:]
	}
	out := make([][]byte, 0, len(lines))
	for _, line := range lines {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		out = append(out, line)
	}
	return out
}

// readSidecarFile reads a small metadata file beside a session and hands its
// bytes to the caller's decoder. The decoder, not this function, decides the
// format: these sidecars are JSON in some stores and YAML in others.
//
// `maxBytes` caps the read so a file that is not what its name says cannot be
// pulled into memory whole.
func readSidecarFile(path string, maxBytes int64, unmarshal func([]byte) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxBytes))
	if err != nil {
		return err
	}
	return unmarshal(data)
}

// parseRFC3339 reads an ISO timestamp, or returns the zero time -- which means
// "the store said nothing" and sorts last, unlike a parse failure that fell
// back to the epoch.
func parseRFC3339(value string) time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}
	}
	ts, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}
	}
	return ts.UTC()
}

// sameDir reports whether two paths name the same directory.
//
// filepath.Clean only, deliberately: no symlink resolution and no Stat. The
// value on the left comes from a foreign store and may name a directory that
// no longer exists, and a comparison that needs the filesystem would answer
// "not equal" for every session whose worktree was since removed -- while
// costing a syscall per candidate on the dialog path.
func sameDir(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

// trimTitle normalizes a title taken from a foreign store: one line, no edge
// whitespace, capped.
//
// Several stores put the first user PROMPT in the title field, and a prompt is
// arbitrary user text -- multi-line, and long enough to fill a menu row on its
// own. The cap is applied in RUNES so a multi-byte character is never cut in
// half into invalid UTF-8.
func trimTitle(title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return ""
	}
	if i := strings.IndexAny(title, "\r\n"); i >= 0 {
		title = strings.TrimSpace(title[:i])
	}
	runes := []rune(title)
	if len(runes) > maxStoredSessionTitleRunes {
		return strings.TrimSpace(string(runes[:maxStoredSessionTitleRunes])) + "…"
	}
	return title
}

// maxStoredSessionTitleRunes caps a title from a foreign store. One menu row
// holds far less than this; the cap exists so a prompt-shaped title cannot make
// the response large, not to do the layout's job.
const maxStoredSessionTitleRunes = 120
