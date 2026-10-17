// Package sessionstore holds readers that several providers share for another program's session
// store.
// The readers support these operations:
// - A read-only SQLite connection.
// - A newest-first directory scan.
// - A limited JSONL read.
// - A checked archive-file read.
// The session record and listing query are provider-neutral. Each provider selects its store layout
// and ancestor-symlink rule.
//
// Nothing in this package or its callers may write to the other program's stored data.
// A read-only WAL database open still creates SQLite sidecars in the store directory. SQLite
// creates the -shm file and an absent -wal file.
// Therefore, readers must not derive session activity from that directory's modification time. See
// NamedFileInside.
//
// An absent store normally means that the provider has no sessions. Unreadable stores or unfamiliar
// shapes do not justify a failed RPC.
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

// storedSessionScanCap sets the nominal candidate limit after directory ordering.
// A user with years of history can have tens of thousands of session files. The picker needs only
// the newest records.
// The limit preserves the complete boundary timestamp group, so a large tie can exceed it.
//
// The limit applies after sorting by modification time. os.ReadDir sorts by filename, which can
// hold a UUID or a timestamp.
// A cut during that scan would retain the first filenames and discard newer sessions.
// One real Claude directory holds 3905 transcripts. Six of its ten newest sessions sort after
// position 2000 by filename.
const storedSessionScanCap = 2000

// JSONLProbeBytes limits the JSONL bytes that a reader examines at each end of a session file.
// Claude's own lister reads the first and last 64 KB also.
// The first records identify the session. The last records hold its latest title. The middle
// contains transcript records that this reader does not need.
const JSONLProbeBytes = 64 * 1024

// ErrAbsent reports that this provider has no store on this machine.
// This state is normal when the user never ran the CLI. A reader returns no sessions, and callers
// must not log a failure.
var ErrAbsent = errors.New("session store not present")

// Moved reports whether the cached session store no longer identifies the file at path.
// The caller must then discard its handle and the data from that handle.
//
// A runtime can delete and recreate a store at the same path. sqlitedb.OpenReadOnly keeps one
// connection without a lifetime limit.
// That connection can retain the unlinked inode and return deleted data for the agent's lifetime.
// os.SameFile detects that replacement. A nil cached value identifies a store without a previous
// stat result.
//
// On Windows, SQLite opens files with FILE_SHARE_READ and FILE_SHARE_WRITE, but without
// FILE_SHARE_DELETE.
// An open handle therefore prevents the runtime from deleting and replacing the store.
// Windows os.FileInfo loads its file ID lazily by reopening the path. A stale value can therefore
// identify the replacement at its first comparison.
// The path comparison supplies the useful check on Windows.
// TestZCodeToolStoreReopensAStoreReplacedAtTheSamePath skips Windows for both reasons.
func Moved(cachedPath string, cached os.FileInfo, path string, current os.FileInfo) bool {
	return cachedPath != path || cached == nil || !os.SameFile(cached, current)
}

// OpenDB opens another program's SQLite session store for reading.
// The initial os.Stat identifies an absent store as ErrAbsent. The mode=ro option refuses file
// creation, but its driver error would incorrectly suggest a failure.
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
// Filtering and timing use the same stat result. Each store decides which file supplies a
// candidate's activity time.
// Separate parameters could let a reader use the time of whichever file the scan encountered.
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

// NamedFileInside selects candidate directories that contain fileName. It derives each timestamp
// from that file.
//
// A directory's modification time tracks file creation and removal, not session activity.
// A read-only SQLite open can create the -shm sidecar and change the directory time.
// TestOpenReadOnly_CreatesShmButLeavesTheDatabaseFile verifies this behavior.
// A Cursor scan that used the directory time would report when LeapMux inspected it, not when
// Cursor wrote it.
// A store migration can change all directory times also. Four directories in one real Copilot store
// have one migration timestamp, although their sessions span eight days.
//
// The returned Path identifies the directory because callers read more than its timing file.
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

// NewestEntries lists the entries that sel accepts, newest first. It preserves the complete
// timestamp group at the selected limit.
//
// These stores have no index. Modification time supplies the cheap recency signal before the reader
// opens the files.
// Reading content timestamps first would require every file read. A failed stat excludes that
// candidate instead of assigning the epoch.
// Thus, a file removed during the scan cannot displace a real result.
func NewestEntries(dir string, limit int, sel EntrySelector) ([]Entry, error) {
	// Windows can list a plain file as one entry instead of returning an error.
	// The directory check preserves the contract that the POSIX error path already enforces.
	if info, err := os.Stat(dir); err == nil && !info.IsDir() {
		return nil, fmt.Errorf("read session store directory: %s is not a directory", dir)
	}
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
	// Apply the smaller positive limit after one sort. os.ReadDir already lists the complete directory
	// by filename.
	// A cut during that scan would select the wrong entries. See storedSessionScanCap.
	if limit <= 0 || limit > storedSessionScanCap {
		limit = storedSessionScanCap
	}
	return SortAndCapEntries(found, limit), nil
}

// SortAndCapEntries orders candidates newest first and preserves the complete group at the limit.
// A nonpositive limit keeps every entry.
//
// The name breaks equal timestamps to give a stable order on stores with coarse timestamps.
// Names do not indicate recency inside a tie. Splitting that group could discard the session with
// the newest content timestamp.
func SortAndCapEntries(found []Entry, limit int) []Entry {
	sort.SliceStable(found, func(i, j int) bool {
		if !found[i].ModTime.Equal(found[j].ModTime) {
			return found[i].ModTime.After(found[j].ModTime)
		}
		return found[i].Name < found[j].Name
	})
	if limit > 0 && len(found) > limit {
		cut := limit
		for cut < len(found) && found[cut].ModTime.Equal(found[cut-1].ModTime) {
			cut++
		}
		found = found[:cut]
	}
	return found
}

// JSONLHead returns complete lines within the first maxBytes bytes. It reports whether the window
// reaches the end of the file.
//
// It discards a trailing partial line because the caller cannot decode part of a JSON record.
// When the complete file fits, the last line is complete and remains in the result.
//
// The read requests one extra byte to distinguish an exact fit from a longer file.
// io.ReadFull otherwise returns the same count and nil error in both cases.
// An exact-fit check once discarded the last complete line. A single-record file then lost its
// working directory and disappeared from the picker.
// JSONLTail uses a strict size comparison.
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

// JSONLTail returns complete lines within the last maxBytes bytes.
// It checks the byte before the window to distinguish a complete first line from a partial line.
// A shorter file needs no leading-line removal.
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
		if maxBytes > 0 {
			var previous [1]byte
			if _, err := f.ReadAt(previous[:], start-1); err != nil {
				return nil, err
			}
			truncated = previous[0] != '\n'
		}
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

// MaxSidecarBytes limits small metadata files beside a session. These include configuration files
// and session sidecars.
// The limit accommodates those files without reading an unexpectedly large file into memory.
const MaxSidecarBytes = 256 * 1024

// ReadSidecarFile reads a limited metadata file and passes the bytes to the caller's decoder.
// The caller selects the format because stores use both JSON and YAML.
//
// The limit prevents a mislabeled large file from consuming unrestricted memory.
// When this function returns an error, the caller must discard everything that the decoder wrote.
// A decoder can populate earlier fields before it rejects a later field.
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

// ParseRFC3339 reads an ISO timestamp. An invalid value returns the zero time, which means that the
// store supplied no timestamp.
// That value sorts last. It must not become an epoch timestamp.
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
// pathutil.SamePath cleans both paths and ignores case on Windows. This comparison deliberately
// performs no filesystem access.
// A foreign store can identify a directory that no longer exists. A filesystem comparison would
// reject every session from a removed worktree.
// It would also add a system call for each candidate in the picker.
//
// The empty check is required. SamePath cleans an empty path to a dot and considers two empty paths
// equal.
// A store with no recorded working directory must never match an arbitrary directory.
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

// HomeDirFromEnv resolves a provider's state directory. envVar takes precedence and can start with
// a tilde.
// The default is name under the user's home.
//
// A provider with more resolution rules keeps its own resolver. Examples include a platform branch
// or a configuration file that changes the store location.
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

// OverridePath resolves a store-file environment variable. An absolute path takes precedence. A
// relative filename resolves under dir.
// OPENCODE_DB and KILO_DB share this rule with their CLIs.
//
// The second result distinguishes a set override from an absent override.
// A relative override with no resolvable directory returns an empty path. The caller must not then
// attempt its default filename.
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

// Collect reads accepted candidates and returns the newest sessions in display order.
// A nonpositive limit returns no sessions. The read includes the complete boundary timestamp group.
func Collect(ctx context.Context, entries []Entry, limit int, read func(Entry) (agent.StoredSession, bool)) []agent.StoredSession {
	collector := agent.NewStoredSessionCollector(limit)
	VisitAccepted(ctx, entries, limit, read, collector.Add)
	return collector.Sessions()
}

// CollectMany reads candidates that can supply several sessions, such as stores with several threads.
// It uses the same accepted-session budget and timestamp groups as Collect.
func CollectMany(ctx context.Context, entries []Entry, limit int, read func(Entry) []agent.StoredSession) []agent.StoredSession {
	collector := agent.NewStoredSessionCollector(limit)
	visitEntries(ctx, entries, limit, func(entry Entry, accept func(agent.StoredSession)) {
		for _, session := range read(entry) {
			accept(session)
		}
	}, collector.Add)
	return collector.Sessions()
}

// VisitAccepted visits every accepted session before selection or deduplication.
// A caller can retain its first occurrence across groups without retaining every parsed record.
func VisitAccepted(ctx context.Context, entries []Entry, limit int, read func(Entry) (agent.StoredSession, bool), visit func(agent.StoredSession)) {
	visitEntries(ctx, entries, limit, func(entry Entry, accept func(agent.StoredSession)) {
		if session, ok := read(entry); ok {
			accept(session)
		}
	}, visit)
}

// visitEntries expects newest-first entries. Acceptance controls the read budget independently of the result count.
// A boundary tie can require every candidate read. Cancellation stops before the next candidate.
func visitEntries(ctx context.Context, entries []Entry, limit int, read func(Entry, func(agent.StoredSession)), visit func(agent.StoredSession)) {
	if limit <= 0 {
		return
	}
	accepted := 0
	var oldestAccepted time.Time
	for _, entry := range entries {
		if accepted >= limit && !entry.ModTime.Equal(oldestAccepted) {
			break
		}
		if ctx.Err() != nil {
			break
		}
		read(entry, func(session agent.StoredSession) {
			// Only the threshold matters. Saturation prevents a large batch from overflowing the count.
			if accepted < limit {
				accepted++
			}
			if accepted == 1 || entry.ModTime.Before(oldestAccepted) {
				oldestAccepted = entry.ModTime
			}
			visit(session)
		})
	}
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

// ContentBlock is one block of a message's content: its type, and its text when
// the block carries text.
type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// ContentBlocks decodes a plain string or an array of typed content blocks. A plain string produces
// one text block.
//
// Several providers share this encoding:
// - Claude Code.
// - Qoder.
// - Pi.
// - Oh My Pi.
// Each provider still selects the block that contains its prompt.
// An array item with an incompatible object or field type contributes no block. Other content
// shapes return nil.
func ContentBlocks(content json.RawMessage) []ContentBlock {
	content = bytes.TrimSpace(content)
	if len(content) == 0 {
		return nil
	}
	if content[0] == '"' {
		var text string
		if json.Unmarshal(content, &text) != nil {
			return nil
		}
		return []ContentBlock{{Type: "text", Text: text}}
	}
	if content[0] != '[' {
		return nil
	}
	var items []json.RawMessage
	if json.Unmarshal(content, &items) != nil {
		return nil
	}
	blocks := make([]ContentBlock, 0, len(items))
	for _, item := range items {
		item = bytes.TrimSpace(item)
		if len(item) == 0 || item[0] != '{' {
			continue
		}
		var block ContentBlock
		if json.Unmarshal(item, &block) != nil {
			continue
		}
		blocks = append(blocks, block)
	}
	return blocks
}

// ContentBlockText returns the first non-blank `text` block of a message's
// content, trimmed.
//
// Only a `text` block is taken. A user record also carries `tool_result`
// blocks, and a tool result is machine output that says nothing about what the
// session is for.
func ContentBlockText(content json.RawMessage) string {
	for _, block := range ContentBlocks(content) {
		if block.Type == "text" && strings.TrimSpace(block.Text) != "" {
			return strings.TrimSpace(block.Text)
		}
	}
	return ""
}

// Query runs a SQL-backed session listing query.
// Codex and Goose use it. The OpenCode store readers use it also.
// The shared procedure performs these steps:
// - Reject an empty working directory.
// - Open the store read-only.
// - Return no sessions for an absent store.
// - Bind the cleaned working directory and result limit.
// - Skip rows that the provider cannot scan.
// - Sort the accepted sessions.
// Each provider supplies its database path and SQL. It supplies the row decoder also.
//
// SQL compares the working directory to keep the scan inside SQLite.
// These tables have no directory index and can hold thousands of rows.
// filepath.Clean normalizes the parameter. The worker uses this form, and the stores contain their
// CLI's already-clean process.cwd() value.
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
		// One unreadable row must not discard the remaining rows.
		// A newer CLI can change a column type. The reader must tolerate that change without failing the
		// picker.
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

// TrimTitle normalizes a stored title to one line without edge whitespace. It limits the result
// length.
//
// Some stores use the first user prompt as the title. That arbitrary text can span several lines or
// exceed a menu row.
// The limit counts runes, so it cannot cut a multibyte character into invalid UTF-8.
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

// maxStoredSessionTitleRunes limits titles from foreign stores. A menu row holds much less text.
// The limit controls response size, not layout.
const maxStoredSessionTitleRunes = 120
