package codewhale

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/sessionstore"
	"github.com/leapmux/leapmux/util/pathutil"
)

// Where LeapMux keeps Codewhale's thread stores, and how the session picker
// reads them.
//
// Each agent runs its own runtime on its OWN task store. The runtime takes an
// exclusive lock on its store for the life of the process, so two agents cannot
// share one, and the default store (`~/.codewhale/tasks`) belongs to the user's
// own `codewhale web` and `app-server`, which a LeapMux agent must not lock out.
//
// The stores live at `$CODEWHALE_HOME/leapmux/<store>/`, and `$CODEWHALE_HOME`
// defaults to `~/.codewhale`. Codewhale's own home is the one place both the
// launch and the session picker can find without a LeapMux data directory:
// the picker receives only the user's home and environment.
//
// ONE store holds ONE thread. A fresh agent makes a fresh store, a resume finds
// the store that holds its thread, and a clear restarts the agent on a fresh
// store (see Agent.ClearContext). So a resume never takes another tab's thread
// along with its own, and the picker lists a store by its one thread.
//
// The runtime keeps the store as one JSON file for each record:
//
//	<store>/runtime/state.json                 {schema_version, next_seq}, written on every event
//	<store>/runtime/threads/<thread_id>.json   ThreadRecord
//	<store>/runtime/turns/<turn_id>.json       TurnRecord
//
// A reader never writes into a store: the runtime owns every file in it.

// codewhaleStoresDirName is the directory under Codewhale's home that holds
// LeapMux's stores.
const codewhaleStoresDirName = "leapmux"

// codewhaleHomeDirName is Codewhale's home under the user's home.
const codewhaleHomeDirName = ".codewhale"

// codewhaleStorePrefix starts every store directory name, so a stray directory
// under the stores root is not read as a store.
const codewhaleStorePrefix = "store-"

// storeRecordMaxBytes limits one record the reader opens. A thread record
// carries the compaction summary, which is the largest field it has.
const storeRecordMaxBytes = 8 << 20

// codewhaleHome resolves Codewhale's home the way the runtime does: an absolute
// CODEWHALE_HOME (after `~` expands) wins, and `~/.codewhale` is the default.
// A relative value is one the runtime refuses, so the reader ignores it too.
func codewhaleHome(q agent.StoredSessionQuery) string {
	if dir := strings.TrimSpace(q.Env(envHome)); dir != "" {
		dir = pathutil.ExpandHome(dir, q.Home())
		if filepath.IsAbs(dir) {
			return dir
		}
	}
	home := q.Home()
	if home == "" {
		return ""
	}
	return filepath.Join(home, codewhaleHomeDirName)
}

// codewhaleStoresRoot is the directory that holds LeapMux's stores, or "" when
// no home resolves.
func codewhaleStoresRoot(q agent.StoredSessionQuery) string {
	home := codewhaleHome(q)
	if home == "" {
		return ""
	}
	return filepath.Join(home, codewhaleStoresDirName)
}

// codewhaleStore is one task store.
type codewhaleStore struct {
	dir string
}

// tasksDir is what CODEWHALE_TASKS_DIR states.
func (s codewhaleStore) tasksDir() string { return s.dir }

// runtimeDir is the thread store root, which CODEWHALE_RUNTIME_DIR states.
func (s codewhaleStore) runtimeDir() string { return filepath.Join(s.dir, runtimeStoreDir) }

// threadPath is where the store keeps one thread's record.
func (s codewhaleStore) threadPath(threadID string) string {
	return filepath.Join(s.runtimeDir(), "threads", threadID+".json")
}

// newCodewhaleStore makes a fresh, empty store under root.
func newCodewhaleStore(root string) (codewhaleStore, error) {
	if root == "" {
		return codewhaleStore{}, errors.New("no home directory to keep a Codewhale store in")
	}
	dir := filepath.Join(root, codewhaleStorePrefix+id.Short())
	// 0700: a store holds the whole conversation, and the runtime creates its own
	// files 0600 inside it.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return codewhaleStore{}, fmt.Errorf("create the Codewhale store: %w", err)
	}
	return codewhaleStore{dir: dir}, nil
}

// errThreadNotStored reports a resume handle that no LeapMux store holds.
var errThreadNotStored = errors.New("no LeapMux store holds this Codewhale thread")

// findCodewhaleStore returns the store that holds a thread. Two stores can hold
// the same thread id only by a collision of its random suffix; the newer record
// wins, because it is the thread the reader last used.
func findCodewhaleStore(root, threadID string) (codewhaleStore, error) {
	if root == "" || threadID == "" || strings.ContainsAny(threadID, `/\`) {
		return codewhaleStore{}, errThreadNotStored
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return codewhaleStore{}, errThreadNotStored
		}
		return codewhaleStore{}, fmt.Errorf("read the Codewhale stores: %w", err)
	}
	var found codewhaleStore
	var newest int64
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), codewhaleStorePrefix) {
			continue
		}
		store := codewhaleStore{dir: filepath.Join(root, entry.Name())}
		info, err := os.Stat(store.threadPath(threadID))
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if found.dir == "" || info.ModTime().UnixNano() > newest {
			found = store
			newest = info.ModTime().UnixNano()
		}
	}
	if found.dir == "" {
		return codewhaleStore{}, errThreadNotStored
	}
	return found, nil
}

// codewhaleStoredSessions lists the threads of LeapMux's stores that ran in
// the query's working directory, newest first.
//
// A store is timed by its `runtime/state.json`, which the runtime rewrites on
// every event, and that order is what the walk caps by. The working directory
// is inside the thread record, so every candidate is read until the limit is
// met.
func codewhaleStoredSessions(ctx context.Context, q agent.StoredSessionQuery) ([]agent.StoredSession, error) {
	if strings.TrimSpace(q.WorkingDir) == "" {
		return nil, nil
	}
	root := codewhaleStoresRoot(q)
	if root == "" {
		return nil, nil
	}
	entries, err := sessionstore.NewestEntries(root, 0, func(dir string, entry os.DirEntry) (sessionstore.Entry, bool) {
		if !strings.HasPrefix(entry.Name(), codewhaleStorePrefix) {
			return sessionstore.Entry{}, false
		}
		return sessionstore.NamedFileInside(filepath.Join(runtimeStoreDir, "state.json"))(dir, entry)
	})
	if err != nil {
		if errors.Is(err, sessionstore.ErrAbsent) {
			return nil, nil
		}
		return nil, err
	}
	limit := q.EffectiveLimit()
	var sessions []agent.StoredSession
	for _, entry := range entries {
		if len(sessions) >= limit || ctx.Err() != nil {
			break
		}
		sessions = append(sessions, readStoreSessions(codewhaleStore{dir: entry.Path}, q.WorkingDir)...)
	}
	return agent.SortAndCapSessions(sessions, limit), nil
}

// readStoreSessions reads the threads of one store that ran in workingDir. A
// thread that ran no turn is an agent that opened and never spoke, so it is not
// a session worth offering; neither is an archived one.
func readStoreSessions(store codewhaleStore, workingDir string) []agent.StoredSession {
	threadsDir := filepath.Join(store.runtimeDir(), "threads")
	entries, err := os.ReadDir(threadsDir)
	if err != nil {
		return nil
	}
	var sessions []agent.StoredSession
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		var thread threadRecord
		if err := sessionstore.ReadSidecarFile(filepath.Join(threadsDir, name), storeRecordMaxBytes, func(data []byte) error {
			return json.Unmarshal(data, &thread)
		}); err != nil {
			continue
		}
		if thread.ID == "" || thread.ID+".json" != name || thread.Archived || thread.LatestTurnID == "" {
			continue
		}
		if !sessionstore.SameDir(thread.Workspace, workingDir) {
			continue
		}
		sessions = append(sessions, agent.StoredSession{
			Handle:    thread.ID,
			Title:     sessionstore.TrimTitle(sessionstore.FirstNonBlank(thread.Title, latestTurnSummary(store, thread.LatestTurnID))),
			UpdatedAt: sessionstore.ParseRFC3339(thread.UpdatedAt),
		})
	}
	sort.SliceStable(sessions, func(i, j int) bool { return sessions[i].UpdatedAt.After(sessions[j].UpdatedAt) })
	return sessions
}

// latestTurnSummary reads what the thread's latest turn asked, which is what
// the runtime's own thread summary titles an untitled thread with.
func latestTurnSummary(store codewhaleStore, turnID string) string {
	if turnID == "" || strings.ContainsAny(turnID, `/\`) {
		return ""
	}
	var turn turnRecord
	if err := sessionstore.ReadSidecarFile(filepath.Join(store.runtimeDir(), "turns", turnID+".json"), storeRecordMaxBytes, func(data []byte) error {
		return json.Unmarshal(data, &turn)
	}); err != nil {
		return ""
	}
	return turn.InputSummary
}
