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

// This file locates Codewhale thread stores and reads their sessions.
//
// Each agent uses a separate runtime store. The runtime holds an exclusive store lock for its
// lifetime, so agents cannot share that store. The user's codewhale web and app-server own the
// default ~/.codewhale/tasks store. LeapMux must not prevent their access.
//
// Stores reside under $CODEWHALE_HOME/leapmux/<store>/. CODEWHALE_HOME defaults to ~/.codewhale.
// Launch and discovery can both resolve that location without a LeapMux data directory. The
// picker receives only the user's home and environment.
//
// LeapMux starts one thread per store. A new agent creates a store. Resume selects the store
// that holds the thread. ClearContext restarts the agent with a new store. The reader still
// accepts multiple valid thread records when a store contains them.
//
// The runtime owns every record file:
// - runtime/state.json contains schema_version and next_seq. Each event rewrites it.
// - runtime/threads/<thread_id>.json contains a ThreadRecord.
// - runtime/turns/<turn_id>.json contains a TurnRecord.
// Readers never write to the store.

// codewhaleStoresDirName is the directory under Codewhale's home that holds
// LeapMux's stores.
const codewhaleStoresDirName = "leapmux"

// codewhaleHomeDirName is Codewhale's home under the user's home.
const codewhaleHomeDirName = ".codewhale"

// codewhaleStorePrefix starts every store directory name, so a stray directory
// under the stores root is not read as a store.
const codewhaleStorePrefix = "store-"

// codewhaleHomePrefix identifies a store's private runtime home. The home sits beside the store.
// A nested home could exceed the platform's daemon socket path limit in a deep workspace.
const codewhaleHomePrefix = "home-"

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
	// home is this store's private CODEWHALE_HOME, so its runtime coordinates
	// with no other runtime of the configured home.
	home string
}

// tasksDir is what CODEWHALE_TASKS_DIR states.
func (s codewhaleStore) tasksDir() string { return s.dir }

// runtimeHome is the CODEWHALE_HOME of this store's runtime.
func (s codewhaleStore) runtimeHome() string {
	if s.home != "" {
		return s.home
	}
	return storeHome(s.dir)
}

// storeHome derives the private home of a store from its directory, for a store
// that a lookup found rather than made.
func storeHome(storeDir string) string {
	root := filepath.Dir(storeDir)
	suffix := strings.TrimPrefix(filepath.Base(storeDir), codewhaleStorePrefix)
	return filepath.Join(root, codewhaleHomePrefix+suffix)
}

// runtimeDir is the thread store root, which CODEWHALE_RUNTIME_DIR states.
func (s codewhaleStore) runtimeDir() string { return filepath.Join(s.dir, runtimeStoreDir) }

// threadPath is where the store keeps one thread's record.
func (s codewhaleStore) threadPath(threadID string) string {
	return filepath.Join(s.runtimeDir(), "threads", threadID+".json")
}

// codewhaleSharedHomeEntries identifies shared configuration that no runtime owns:
// - Provider configuration.
// - MCP server registrations.
// - The model catalog.
// Each private home copies the configured entries because the runtime rejects symlinked
// configuration. Each runtime shares the configuration but keeps separate state.
var codewhaleSharedHomeEntries = []string{"config.toml", "mcp.json", "catalog"}

// newCodewhaleStore creates an empty store and a private runtime home.
// Codewhale coordinates each home's processes through one daemon socket and one owner ledger. It
// refuses a second runtime while another runtime holds that state. A separate home lets another
// worker's agent start without sharing that coordination state.
func newCodewhaleStore(root string) (codewhaleStore, error) {
	if root == "" {
		return codewhaleStore{}, errors.New("no home directory to keep a Codewhale store in")
	}
	shortID := id.Short()
	dir := filepath.Join(root, codewhaleStorePrefix+shortID)
	// 0700: a store holds the whole conversation, and the runtime creates its own
	// files 0600 inside it.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return codewhaleStore{}, fmt.Errorf("create the Codewhale store: %w", err)
	}
	home := filepath.Join(root, codewhaleHomePrefix+shortID)
	if err := os.MkdirAll(home, 0o700); err != nil {
		return codewhaleStore{}, fmt.Errorf("create the Codewhale store home: %w", err)
	}
	sharedHome := filepath.Dir(root)
	for _, entry := range codewhaleSharedHomeEntries {
		if err := copyHomeEntry(filepath.Join(sharedHome, entry), filepath.Join(home, entry)); err != nil {
			return codewhaleStore{}, fmt.Errorf("share the Codewhale home entry %s: %w", entry, err)
		}
	}
	return codewhaleStore{dir: dir, home: home}, nil
}

// copyHomeEntry copies one configuration entry of a Codewhale home into a
// store's home. A missing entry shares nothing, and an existing destination
// states a retry that already shared it.
func copyHomeEntry(source, destination string) error {
	info, err := os.Lstat(source)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if _, err := os.Lstat(destination); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if !info.Mode().IsRegular() {
		// The catalog is a directory of provider files.
		entries, err := os.ReadDir(source)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(destination, 0o700); err != nil {
			return err
		}
		for _, entry := range entries {
			if !entry.Type().IsRegular() {
				continue
			}
			if err := copyHomeEntry(filepath.Join(source, entry.Name()), filepath.Join(destination, entry.Name())); err != nil {
				return err
			}
		}
		return nil
	}
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	return os.WriteFile(destination, data, 0o600)
}

// errThreadNotStored reports a resume handle that no LeapMux store holds.
var errThreadNotStored = errors.New("no LeapMux store holds this Codewhale thread")

// findCodewhaleStore returns the store that contains a thread. A random-suffix collision can put
// one thread ID in two stores. The newer record takes precedence because it identifies the most
// recently used thread.
func findCodewhaleStore(root, threadID string) (codewhaleStore, error) {
	if root == "" || threadID == "" || strings.ContainsAny(threadID, `/\`) {
		return codewhaleStore{}, errThreadNotStored
	}
	// Windows can list a plain file as one entry instead of returning an error. The explicit
	// directory check preserves the POSIX path contract.
	if info, err := os.Stat(root); err == nil && !info.IsDir() {
		return codewhaleStore{}, fmt.Errorf("read the Codewhale stores: %s is not a directory", root)
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
		store := codewhaleStore{dir: filepath.Join(root, entry.Name()), home: storeHome(filepath.Join(root, entry.Name()))}
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

// codewhaleStoredSessions lists matching threads, newest first.
// The runtime rewrites runtime/state.json on every event. That file supplies the candidate
// timestamp. Each thread record supplies its working directory and activity timestamp. The
// reader completes the boundary timestamp group before selecting results.
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
	sessions := sessionstore.CollectMany(ctx, entries, limit, func(entry sessionstore.Entry) []agent.StoredSession {
		return readStoreSessions(codewhaleStore{dir: entry.Path, home: storeHome(entry.Path)}, q.WorkingDir)
	})
	return agent.SortAndCapSessions(sessions, limit), nil
}

// readStoreSessions reads the store's threads for workingDir. An unused thread has no
// conversation to resume. The reader excludes it and archived threads.
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
