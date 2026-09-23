// Package sessionstore holds the readers that several providers share to list
// the sessions in another program's session store: a read-only SQLite handle, a
// newest-first directory walk, and a capped JSONL read. The record and the query
// are provider-neutral, so they live in package agent. Which directory to walk,
// which table to query and where the title lives are provider decisions, so each
// provider keeps them in its own package behind Provider.ListStoredSessions.
//
// Every store here belongs to another program. Nothing in this package or its
// callers may write to a store's DATA. The read is not free of every side
// effect: a read-only open of a WAL database makes SQLite create the `-shm`
// sidecar, and the `-wal` file when it is absent, inside the store's own
// directory. Thus a reader must never take a session's time from that
// directory's modification time -- see NamedFileInside.
//
// A store that is absent, unreadable, or shaped differently than the version
// this code was written against means "no sessions from this provider" --
// never a failed RPC.
package sessionstore

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/util/pathutil"
)

// storedSessionScanCap limits how many CANDIDATES a directory-walking reader
// carries past the sort. A user with years of history has tens of thousands of
// session files, and the picker needs the newest few.
//
// The cap applies AFTER the modification times order the entries, never during
// the walk. os.ReadDir returns a directory sorted by FILE NAME, and every store
// here names a session with a UUID or a timestamp, so a cut taken during the
// walk keeps the lexicographically first entries and discards the newest ones.
// One real Claude directory holds 3905 transcripts, and six of its ten newest
// sessions sort past position 2000 by name.
const storedSessionScanCap = 2000

// JSONLProbeBytes is how much of a JSONL session file a reader looks at from
// each end. Claude's own lister uses the same shape (first and last 64 KB),
// because a session's identity is in the first records and its latest title is
// in the last ones, while the middle is a transcript nothing here reads.
const JSONLProbeBytes = 64 * 1024

// ErrAbsent reports that this provider keeps no store on this
// machine. That is the NORMAL state for a CLI the user never ran, so a reader
// turns it into an empty result and a caller must not log it as a failure.
var ErrAbsent = errors.New("session store not present")

// Moved reports whether a cached session store is no longer the file
// at `path`, so its handle and everything read through it must be dropped.
//
// A path comparison alone is NOT enough, and that is the whole reason this exists.
// A runtime that deletes and recreates its store at the SAME path leaves the cached
// handle open on the unlinked inode -- sqlitedb.OpenReadOnly holds one connection
// with no lifetime -- and every later read answers from the deleted file for the
// life of the agent. os.SameFile is what catches that; `cached == nil` catches a
// store that was never stated.
//
// On Windows the file-identity half does no work, and it never needs to. SQLite opens
// every file with FILE_SHARE_READ|FILE_SHARE_WRITE and no FILE_SHARE_DELETE, so an
// open handle refuses the owning runtime the delete that a replacement needs: the file
// under a cached handle cannot be replaced there. os.SameFile is also unable to see
// one. A Windows os.FileInfo carries a file id that loads lazily, by a re-open of the
// PATH, so a stale FileInfo resolves to whatever that path holds at the first
// comparison. The path comparison is what does the work on that platform, and
// TestZCodeToolStoreReopensAStoreReplacedAtTheSamePath skips there for both reasons.
func Moved(cachedPath string, cached os.FileInfo, path string, current os.FileInfo) bool {
	return cachedPath != path || cached == nil || !os.SameFile(cached, current)
}

// OpenDB opens another program's SQLite session store for reading.
//
// The os.Stat comes first so an absent store is reported as
// ErrAbsent rather than as a driver error: `mode=ro` refuses to
// create the file, but its message describes a failure and this is not one.
func OpenDB(ctx context.Context, path string) (*sql.DB, error) {
	if path == "" {
		return nil, ErrAbsent
	}
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrAbsent
		}
		return nil, fmt.Errorf("stat session store: %w", err)
	}
	db, err := sqlitedb.OpenReadOnly(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("open session store: %w", err)
	}
	return db, nil
}

// Entry is one candidate a directory-walking reader found, carried with
// the modification time that orders it.
type Entry struct {
	Path    string
	Name    string
	ModTime time.Time
}

// EntrySelector filters one directory entry and, when it accepts the entry,
// supplies the Entry that times and orders it.
//
// Filtering and timing are one step because the two answers come from the same
// stat: which file carries a candidate's time is a per-store decision, and
// splitting it into a separate parameter invites a reader to take the time from
// whichever file the walk happened to visit.
type EntrySelector func(dir string, entry os.DirEntry) (Entry, bool)

// EntryItself times a candidate by the entry's OWN modification time, and keeps
// the entries that `keep` accepts. It is the right selector for a store whose
// sessions are FILES.
func EntryItself(keep func(os.DirEntry) bool) EntrySelector {
	return func(dir string, entry os.DirEntry) (Entry, bool) {
		if keep != nil && !keep(entry) {
			return Entry{}, false
		}
		info, err := entry.Info()
		if err != nil {
			return Entry{}, false
		}
		return Entry{
			Path:    filepath.Join(dir, entry.Name()),
			Name:    entry.Name(),
			ModTime: info.ModTime(),
		}, true
	}
}

// NamedFileInside times a candidate DIRECTORY by one named file inside it, and
// keeps only the directories that hold that file. It is the right selector for
// a store whose sessions are directories.
//
// A directory's own modification time is never the session's activity. It
// changes only when a file appears in the directory or leaves it, so it tracks
// the session's CREATION and not its use. Worse, any program that adds a file
// rewrites it: a read-only SQLite open creates the `-shm` sidecar, which
// TestOpenReadOnly_CreatesShmButLeavesTheDatabaseFile pins, so a Cursor walk
// timed that way reports the moment LeapMux looked rather than the moment
// Cursor wrote. A store migration has the same effect on a whole store at once:
// four directories of one real Copilot store carry the single timestamp of the
// move that created them, while the sessions inside span eight days.
//
// The Path of the returned entry is the DIRECTORY, because the caller reads
// more than the timing file out of it.
func NamedFileInside(fileName string) EntrySelector {
	return func(dir string, entry os.DirEntry) (Entry, bool) {
		if !entry.IsDir() {
			return Entry{}, false
		}
		sessionDir := filepath.Join(dir, entry.Name())
		// A stat, not an open: this runs for every session in the store and
		// only orders them. The files are read after the cut.
		info, err := os.Stat(filepath.Join(sessionDir, fileName))
		if err != nil {
			return Entry{}, false
		}
		return Entry{Path: sessionDir, Name: entry.Name(), ModTime: info.ModTime()}, true
	}
}

// NewestEntries lists the entries of `dir` that `sel` accepts, newest first,
// truncated to `limit`.
//
// The stat-then-sort order is the point: these stores have no index, so the
// only cheap recency signal is the modification time, and reading the files to
// find out which are recent would read every file in the directory. An entry
// whose stat fails is dropped rather than sorted to the epoch, so a file
// deleted mid-walk cannot displace a real answer.
func NewestEntries(dir string, limit int, sel EntrySelector) ([]Entry, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrAbsent
		}
		return nil, fmt.Errorf("read session store directory: %w", err)
	}
	found := make([]Entry, 0, len(entries))
	for _, entry := range entries {
		selected, ok := sel(dir, entry)
		if !ok {
			continue
		}
		found = append(found, selected)
	}
	// Both caps apply after the sort, never during the walk. os.ReadDir already
	// read and sorted the whole directory by name before this loop started, so
	// a cut taken here would only choose the wrong entries -- see
	// storedSessionScanCap.
	found = SortAndCapEntries(found, storedSessionScanCap)
	return SortAndCapEntries(found, limit), nil
}

// SortAndCapEntries orders candidates newest first and truncates to `limit`.
// A `limit` of zero or less keeps every entry.
//
// The name breaks a timestamp tie, so a store whose timestamps have one-second
// resolution still produces one stable order rather than whatever the scan
// happened to yield.
func SortAndCapEntries(found []Entry, limit int) []Entry {
	sort.SliceStable(found, func(i, j int) bool {
		if !found[i].ModTime.Equal(found[j].ModTime) {
			return found[i].ModTime.After(found[j].ModTime)
		}
		return found[i].Name < found[j].Name
	})
	if limit > 0 && len(found) > limit {
		found = found[:limit]
	}
	return found
}

// JSONLHead returns the complete lines within the first `maxBytes` of a file,
// and reports whether that window reached the END of the file.
//
// A trailing PARTIAL line is dropped, because the caller unmarshals what it
// gets and half a JSON object is not a record. When the whole file fits, the
// last line is complete by definition and is kept.
//
// The read asks for one byte MORE than the window, which is the only way to
// tell a file of exactly `maxBytes` from a longer one: io.ReadFull reports the
// same count and a nil error for both. A comparison against the window size
// alone dropped the last complete line of a file that ends exactly on the
// boundary, and lost the whole file when that line was its only record -- which
// takes the session's `cwd` with it and removes the session from the picker.
// JSONLTail never had the defect, because its `size > maxBytes` is strict.
func JSONLHead(path string, maxBytes int64) (lines [][]byte, atEOF bool, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = f.Close() }()

	buf := make([]byte, maxBytes+1)
	n, err := io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, false, err
	}
	truncated := int64(n) > maxBytes
	if truncated {
		n = int(maxBytes)
	}
	return splitJSONLines(buf[:n], truncated, false), !truncated, nil
}

// JSONLTail returns the complete lines within the last `maxBytes` of a file.
//
// The leading partial line is dropped, for the same reason JSONLHead drops the
// trailing one. When the file is shorter than the window the whole file is
// read, and then the first line is complete and is kept.
func JSONLTail(path string, maxBytes int64) ([][]byte, error) {
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

// MaxSidecarBytes caps a read of a small metadata file next to a session --
// a configuration file, a `.meta`, a `workspace.yaml`. Generous for every such
// file, and small enough that a file which is not what its name says cannot be
// read into memory whole.
const MaxSidecarBytes = 256 * 1024

// ReadSidecarFile reads a small metadata file beside a session and hands its
// bytes to the caller's decoder. The decoder, not this function, decides the
// format: these sidecars are JSON in some stores and YAML in others.
//
// `maxBytes` caps the read so a file that is not what its name says cannot be
// pulled into memory whole. A caller must DISCARD everything the decoder wrote
// when this function reports an error: a decoder fills each field it reads
// before it reports a fault on a later one, so a rejected document can leave
// the target partly populated.
func ReadSidecarFile(path string, maxBytes int64, unmarshal func([]byte) error) error {
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

// ParseRFC3339 reads an ISO timestamp, or returns the zero time -- which means
// "the store said nothing" and sorts last, unlike a parse failure that fell
// back to the epoch.
func ParseRFC3339(value string) time.Time {
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

// SameDir reports whether two paths identify the same directory.
//
// It delegates to pathutil.SamePath, which cleans both sides and folds case on
// Windows. That comparison touches no filesystem, deliberately: the value on
// the left comes from a foreign store and may identify a directory that no
// longer exists, and a comparison that needs the filesystem would answer "not
// equal" for every session whose worktree was since removed -- while costing a
// syscall per candidate on the dialog path.
//
// The empty check is load-bearing and stays here. SamePath cleans "" to "." and
// answers true for two empty paths, while a store that recorded NO working
// directory must never be offered under an arbitrary one.
func SameDir(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return pathutil.SamePath(a, b)
}

// FirstNonBlank returns the first candidate that holds more than whitespace,
// or the empty string when none does. Several readers state a title precedence
// as an ordered list of candidates.
func FirstNonBlank(candidates ...string) string {
	for _, candidate := range candidates {
		if strings.TrimSpace(candidate) != "" {
			return candidate
		}
	}
	return ""
}

// HomeDirFromEnv resolves a provider's state directory: `envVar` wins and may
// begin with `~`, and `name` is the default directory under the user's home.
//
// A provider whose resolution has more steps than these two -- a platform
// branch, or a configuration file that can move the store -- keeps its own
// resolver rather than passing a flag here.
func HomeDirFromEnv(q agent.StoredSessionQuery, envVar, name string) string {
	if dir := strings.TrimSpace(q.Env(envVar)); dir != "" {
		return pathutil.ExpandHome(dir, q.Home())
	}
	home := q.Home()
	if home == "" {
		return ""
	}
	return filepath.Join(home, name)
}

// OverridePath resolves an environment variable that points at a store
// FILE: an absolute path wins, and a bare name is taken under `dir`. It is the
// rule OPENCODE_DB and KILO_DB share, and the CLIs' own.
//
// The second return reports that the variable was SET, which the path alone
// cannot: an override under an unresolvable data directory yields "", and a
// caller that read only the path would fall through to its default name under
// the same unresolvable directory and answer "" a second time by accident.
func OverridePath(q agent.StoredSessionQuery, envVar, dir string) (string, bool) {
	override := strings.TrimSpace(q.Env(envVar))
	if override == "" {
		return "", false
	}
	if filepath.IsAbs(override) {
		return override, true
	}
	if dir == "" {
		return "", true
	}
	return filepath.Join(dir, override), true
}

// Collect reads the candidates in order and keeps the ones the
// reader accepts, stopping at `limit` accepted sessions or at a cancelled
// context.
//
// The limit stop is what makes an uncapped walk affordable: Claude and Copilot
// hand this every candidate in the store, because only the file's own contents
// say which working directory it belongs to. For a reader whose walk was
// already capped it is a no-op that costs one comparison and keeps one shape.
//
// The cancellation check is per CANDIDATE, not per call: each read opens a file
// or a database, so a dismissed dialog has to be observed between candidates
// and not only inside one read.
//
// Reasonix does NOT route through this. Its walk spans two roots with one
// shared budget and a `seen` map across them, which no per-root call can carry
// -- and its loop states that difference rather than hiding it behind a
// parameter four other readers would pass nil for.
func Collect(
	ctx context.Context,
	entries []Entry,
	limit int,
	read func(Entry) (agent.StoredSession, bool),
) []agent.StoredSession {
	sessions := make([]agent.StoredSession, 0, min(limit, len(entries)))
	for _, entry := range entries {
		if len(sessions) >= limit {
			break
		}
		if ctx.Err() != nil {
			break
		}
		session, ok := read(entry)
		if !ok {
			continue
		}
		sessions = append(sessions, session)
	}
	return sessions
}

// EpochMillis converts one of these stores' millisecond timestamps to a time.
// Zero and negative values mean "the store said nothing", which the zero time
// carries and SortAndCapSessions orders last.
func EpochMillis(ms int64) time.Time {
	if ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

// ContentBlockText pulls the readable text out of a message's content, which is
// either a plain string or an array of typed blocks.
//
// The encoding is shared, not one provider's: Claude Code and Pi both write it,
// so it sits here rather than in either reader. Only a `text` block is taken. A
// user record also carries `tool_result` blocks, and a tool result is machine
// output that says nothing about what the session is for.
func ContentBlockText(content json.RawMessage) string {
	if len(content) == 0 {
		return ""
	}
	var text string
	if json.Unmarshal(content, &text) == nil {
		return strings.TrimSpace(text)
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(content, &blocks) != nil {
		return ""
	}
	for _, block := range blocks {
		if block.Type == "text" && strings.TrimSpace(block.Text) != "" {
			return strings.TrimSpace(block.Text)
		}
	}
	return ""
}

// Query runs one SQL-backed store's listing query.
//
// Five providers keep their sessions in a SQLite database, and the steps around
// the query are the same for every one of them: refuse an empty working
// directory, open the store read-only, turn an ABSENT store into the empty
// result rather than a failure, bind the cleaned working directory and the
// limit, skip a row this reader cannot scan, and order the survivors. Only the
// path, the SQL and the row shape differ, so only those are parameters.
//
// The working directory is compared in SQL rather than in Go so the scan stays
// in SQLite: these tables carry no index on their directory column and hold
// thousands of rows. The bound value is filepath.Clean-ed because that is the
// form the worker holds, and these stores hold the CLI's own `process.cwd()`,
// which is already clean.
func Query(
	ctx context.Context,
	dbPath, query string,
	q agent.StoredSessionQuery,
	scan func(*sql.Rows) (agent.StoredSession, bool),
) ([]agent.StoredSession, error) {
	if strings.TrimSpace(q.WorkingDir) == "" {
		return nil, nil
	}
	db, err := OpenDB(ctx, dbPath)
	if err != nil {
		if errors.Is(err, ErrAbsent) {
			return nil, nil
		}
		return nil, err
	}
	defer func() { _ = db.Close() }()

	limit := q.EffectiveLimit()
	rows, err := db.QueryContext(ctx, query, filepath.Clean(q.WorkingDir), limit)
	if err != nil {
		return nil, fmt.Errorf("query session store: %w", err)
	}
	defer func() { _ = rows.Close() }()

	sessions := make([]agent.StoredSession, 0, limit)
	for rows.Next() {
		// One unreadable row must not lose the rest: a column whose type
		// changed in a newer CLI is exactly the drift this reader has to
		// survive without failing the dialog.
		session, ok := scan(rows)
		if !ok {
			continue
		}
		sessions = append(sessions, session)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan session store: %w", err)
	}
	return agent.SortAndCapSessions(sessions, limit), nil
}

// ScanEpochMillisSession reads the (id, title, epoch milliseconds) row shape
// that Codex and the OpenCode family both return.
func ScanEpochMillisSession(rows *sql.Rows) (agent.StoredSession, bool) {
	var (
		id      string
		title   string
		updated int64
	)
	if err := rows.Scan(&id, &title, &updated); err != nil {
		return agent.StoredSession{}, false
	}
	return agent.StoredSession{
		Handle:    strings.TrimSpace(id),
		Title:     TrimTitle(title),
		UpdatedAt: EpochMillis(updated),
	}, true
}

// TrimTitle normalizes a title taken from a foreign store: one line, no edge
// whitespace, capped.
//
// Several stores put the first user PROMPT in the title field, and a prompt is
// arbitrary user text -- multi-line, and long enough to fill a menu row on its
// own. The cap is applied in RUNES so a multi-byte character is never cut in
// half into invalid UTF-8.
func TrimTitle(title string) string {
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
