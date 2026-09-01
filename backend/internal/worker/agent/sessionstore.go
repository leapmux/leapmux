package agent

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

	"github.com/leapmux/leapmux/internal/util/pathutil"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
)

// This file holds the provider-NEUTRAL half of session-store discovery: the
// record shape, the query shape, and the readers every provider's own file
// shares (a read-only SQLite handle, a newest-first directory walk, a capped
// JSONL read). Which directory to walk, which table to query and where the
// title lives are provider decisions, so they live in that provider's own file
// behind Provider.ListStoredSessions.
//
// Every store here belongs to another program. Nothing in this file or its
// callers may write to a store's DATA. The read is not free of every side
// effect: a read-only open of a WAL database makes SQLite create the `-shm`
// sidecar, and the `-wal` file when it is absent, inside the store's own
// directory. Thus a reader must never take a session's time from that
// directory's modification time -- see namedFileInside.
//
// A store that is absent, unreadable, or shaped differently than the version
// this code was written against means "no sessions from this provider" --
// never a failed RPC.

// DefaultStoredSessionLimit caps how many sessions one provider's reader
// collects when the caller states no limit of its own. It limits the I/O of a
// scan, not just the response: a reader stats its candidates, sorts them, and
// only then opens the newest few.
const DefaultStoredSessionLimit = 50

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
func openSessionStoreDB(ctx context.Context, path string) (*sql.DB, error) {
	if path == "" {
		return nil, errSessionStoreAbsent
	}
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, errSessionStoreAbsent
		}
		return nil, fmt.Errorf("stat session store: %w", err)
	}
	db, err := sqlitedb.OpenReadOnly(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("open session store: %w", err)
	}
	return db, nil
}

// SortAndCapSessions orders newest first and truncates to `limit`.
//
// The handle breaks a timestamp tie, so a store whose timestamps have
// one-second resolution (Goose) still produces one stable order rather than
// whatever the scan happened to yield. Records with no handle are dropped:
// a row this code cannot resume is not a choice to offer.
//
// Exported because the worker's service layer merges these records with its own
// and must order the result by the SAME rule. A second copy of the rule there
// drifted from this one the moment either changed.
func SortAndCapSessions(sessions []StoredSession, limit int) []StoredSession {
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

// entrySelector filters one directory entry and, when it accepts the entry,
// supplies the storeEntry that times and orders it.
//
// Filtering and timing are one step because the two answers come from the same
// stat: which file carries a candidate's time is a per-store decision, and
// splitting it into a separate parameter invites a reader to take the time from
// whichever file the walk happened to visit.
type entrySelector func(dir string, entry os.DirEntry) (storeEntry, bool)

// entryItself times a candidate by the entry's OWN modification time, and keeps
// the entries that `keep` accepts. It is the right selector for a store whose
// sessions are FILES.
func entryItself(keep func(os.DirEntry) bool) entrySelector {
	return func(dir string, entry os.DirEntry) (storeEntry, bool) {
		if keep != nil && !keep(entry) {
			return storeEntry{}, false
		}
		info, err := entry.Info()
		if err != nil {
			return storeEntry{}, false
		}
		return storeEntry{
			Path:    filepath.Join(dir, entry.Name()),
			Name:    entry.Name(),
			ModTime: info.ModTime(),
		}, true
	}
}

// namedFileInside times a candidate DIRECTORY by one named file inside it, and
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
func namedFileInside(fileName string) entrySelector {
	return func(dir string, entry os.DirEntry) (storeEntry, bool) {
		if !entry.IsDir() {
			return storeEntry{}, false
		}
		sessionDir := filepath.Join(dir, entry.Name())
		// A stat, not an open: this runs for every session in the store and
		// only orders them. The files are read after the cut.
		info, err := os.Stat(filepath.Join(sessionDir, fileName))
		if err != nil {
			return storeEntry{}, false
		}
		return storeEntry{Path: sessionDir, Name: entry.Name(), ModTime: info.ModTime()}, true
	}
}

// newestEntries lists the entries of `dir` that `sel` accepts, newest first,
// truncated to `limit`.
//
// The stat-then-sort order is the point: these stores have no index, so the
// only cheap recency signal is the modification time, and reading the files to
// find out which are recent would read every file in the directory. An entry
// whose stat fails is dropped rather than sorted to the epoch, so a file
// deleted mid-walk cannot displace a real answer.
func newestEntries(dir string, limit int, sel entrySelector) ([]storeEntry, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, errSessionStoreAbsent
		}
		return nil, fmt.Errorf("read session store directory: %w", err)
	}
	found := make([]storeEntry, 0, len(entries))
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
	found = sortAndCapEntries(found, storedSessionScanCap)
	return sortAndCapEntries(found, limit), nil
}

// sortAndCapEntries orders candidates newest first and truncates to `limit`.
// A `limit` of zero or less keeps every entry.
//
// The name breaks a timestamp tie, so a store whose timestamps have one-second
// resolution still produces one stable order rather than whatever the scan
// happened to yield.
func sortAndCapEntries(found []storeEntry, limit int) []storeEntry {
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

// jsonlHead returns the complete lines within the first `maxBytes` of a file,
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
// jsonlTail never had the defect, because its `size > maxBytes` is strict.
func jsonlHead(path string, maxBytes int64) (lines [][]byte, atEOF bool, err error) {
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

// maxSidecarBytes caps a read of a small metadata file next to a session --
// a configuration file, a `.meta`, a `workspace.yaml`. Generous for every such
// file, and small enough that a file which is not what its name says cannot be
// read into memory whole.
const maxSidecarBytes = 256 * 1024

// readSidecarFile reads a small metadata file beside a session and hands its
// bytes to the caller's decoder. The decoder, not this function, decides the
// format: these sidecars are JSON in some stores and YAML in others.
//
// `maxBytes` caps the read so a file that is not what its name says cannot be
// pulled into memory whole. A caller must DISCARD everything the decoder wrote
// when this function reports an error: a decoder fills each field it reads
// before it reports a fault on a later one, so a rejected document can leave
// the target partly populated.
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

// sameDir reports whether two paths identify the same directory.
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
func sameDir(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return pathutil.SamePath(a, b)
}

// firstNonBlank returns the first candidate that holds more than whitespace,
// or the empty string when none does. Several readers state a title precedence
// as an ordered list of candidates.
func firstNonBlank(candidates ...string) string {
	for _, candidate := range candidates {
		if strings.TrimSpace(candidate) != "" {
			return candidate
		}
	}
	return ""
}

// homeDirFromEnv resolves a provider's state directory: `envVar` wins and may
// begin with `~`, and `name` is the default directory under the user's home.
//
// A provider whose resolution has more steps than these two -- a platform
// branch, or a configuration file that can move the store -- keeps its own
// resolver rather than passing a flag here.
func homeDirFromEnv(q StoredSessionQuery, envVar, name string) string {
	if dir := strings.TrimSpace(q.env(envVar)); dir != "" {
		return pathutil.ExpandHome(dir, q.home())
	}
	home := q.home()
	if home == "" {
		return ""
	}
	return filepath.Join(home, name)
}

// storeOverridePath resolves an environment variable that points at a store
// FILE: an absolute path wins, and a bare name is taken under `dir`. It is the
// rule OPENCODE_DB and KILO_DB share, and the CLIs' own.
//
// The second return reports that the variable was SET, which the path alone
// cannot: an override under an unresolvable data directory yields "", and a
// caller that read only the path would fall through to its default name under
// the same unresolvable directory and answer "" a second time by accident.
func storeOverridePath(q StoredSessionQuery, envVar, dir string) (string, bool) {
	override := strings.TrimSpace(q.env(envVar))
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

// collectStoredSessions reads the candidates in order and keeps the ones the
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
func collectStoredSessions(
	ctx context.Context,
	entries []storeEntry,
	limit int,
	read func(storeEntry) (StoredSession, bool),
) []StoredSession {
	sessions := make([]StoredSession, 0, min(limit, len(entries)))
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

// epochMillis converts one of these stores' millisecond timestamps to a time.
// Zero and negative values mean "the store said nothing", which the zero time
// carries and SortAndCapSessions orders last.
func epochMillis(ms int64) time.Time {
	if ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

// contentBlockText pulls the readable text out of a message's content, which is
// either a plain string or an array of typed blocks.
//
// The encoding is shared, not one provider's: Claude Code and Pi both write it,
// so it sits here rather than in either reader. Only a `text` block is taken. A
// user record also carries `tool_result` blocks, and a tool result is machine
// output that says nothing about what the session is for.
func contentBlockText(content json.RawMessage) string {
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

// queryStoredSessions runs one SQL-backed store's listing query.
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
func queryStoredSessions(
	ctx context.Context,
	dbPath, query string,
	q StoredSessionQuery,
	scan func(*sql.Rows) (StoredSession, bool),
) ([]StoredSession, error) {
	if strings.TrimSpace(q.WorkingDir) == "" {
		return nil, nil
	}
	db, err := openSessionStoreDB(ctx, dbPath)
	if err != nil {
		if errors.Is(err, errSessionStoreAbsent) {
			return nil, nil
		}
		return nil, err
	}
	defer func() { _ = db.Close() }()

	limit := q.limit()
	rows, err := db.QueryContext(ctx, query, filepath.Clean(q.WorkingDir), limit)
	if err != nil {
		return nil, fmt.Errorf("query session store: %w", err)
	}
	defer func() { _ = rows.Close() }()

	sessions := make([]StoredSession, 0, limit)
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
	return SortAndCapSessions(sessions, limit), nil
}

// scanEpochMillisSession reads the (id, title, epoch milliseconds) row shape
// that Codex and the OpenCode family both return.
func scanEpochMillisSession(rows *sql.Rows) (StoredSession, bool) {
	var (
		id      string
		title   string
		updated int64
	)
	if err := rows.Scan(&id, &title, &updated); err != nil {
		return StoredSession{}, false
	}
	return StoredSession{
		Handle:    strings.TrimSpace(id),
		Title:     trimTitle(title),
		UpdatedAt: epochMillis(updated),
	}, true
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
