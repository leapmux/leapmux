package codewhale

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

// seedStore writes one store the way the runtime lays it out: the state file
// the picker times the store by, the thread record, and the latest turn.
func seedStore(t *testing.T, root, name string, thread threadRecord, turn *turnRecord, touched time.Time) codewhaleStore {
	t.Helper()
	store := codewhaleStore{dir: filepath.Join(root, name)}
	require.NoError(t, os.MkdirAll(filepath.Join(store.runtimeDir(), "threads"), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(store.runtimeDir(), "turns"), 0o700))
	state := filepath.Join(store.runtimeDir(), "state.json")
	require.NoError(t, os.WriteFile(state, []byte(`{"schema_version":1,"next_seq":9}`), 0o600))
	require.NoError(t, os.Chtimes(state, touched, touched))
	require.NoError(t, os.WriteFile(store.threadPath(thread.ID), mustJSON(t, thread), 0o600))
	require.NoError(t, os.Chtimes(store.threadPath(thread.ID), touched, touched))
	if turn != nil {
		require.NoError(t, os.WriteFile(filepath.Join(store.runtimeDir(), "turns", turn.ID+".json"), mustJSON(t, turn), 0o600))
	}
	return store
}

func TestCodewhaleHome(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	query := func(env map[string]string) agent.StoredSessionQuery {
		return agent.StoredSessionQuery{HomeDir: home, Getenv: agenttest.FixtureEnv(env)}
	}

	assert.Equal(t, filepath.Join(home, ".codewhale"), codewhaleHome(query(nil)))
	absolute := filepath.Join(home, "elsewhere")
	assert.Equal(t, absolute, codewhaleHome(query(map[string]string{envHome: absolute})))
	assert.Equal(t, filepath.Join(home, "tilde"), codewhaleHome(query(map[string]string{envHome: "~/tilde"})))
	// The runtime refuses a relative home, so the reader falls back as well.
	assert.Equal(t, filepath.Join(home, ".codewhale"), codewhaleHome(query(map[string]string{envHome: "relative/dir"})))
	assert.Equal(t, filepath.Join(home, ".codewhale", "leapmux"), codewhaleStoresRoot(query(nil)))
}

func TestNewCodewhaleStore(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "leapmux")

	first, err := newCodewhaleStore(root)
	require.NoError(t, err)
	second, err := newCodewhaleStore(root)
	require.NoError(t, err)
	assert.NotEqual(t, first.dir, second.dir, "each agent gets a store of its own")
	assert.Equal(t, root, filepath.Dir(first.dir))
	assert.Contains(t, filepath.Base(first.dir), codewhaleStorePrefix)
	info, err := os.Stat(first.dir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm())
	assert.Equal(t, first.dir, first.tasksDir())
	assert.Equal(t, filepath.Join(first.dir, "runtime"), first.runtimeDir())

	_, err = newCodewhaleStore("")
	assert.Error(t, err)
}

func TestFindCodewhaleStore(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	now := time.Now()
	older := seedStore(t, root, "store-older", threadRecord{ID: "thr_1"}, nil, now.Add(-time.Hour))
	newer := seedStore(t, root, "store-newer", threadRecord{ID: "thr_1"}, nil, now)
	seedStore(t, root, "store-other", threadRecord{ID: "thr_2"}, nil, now)
	// A directory without the prefix is not a store, whatever it holds.
	seedStore(t, root, "stray", threadRecord{ID: "thr_3"}, nil, now)

	found, err := findCodewhaleStore(root, "thr_1")
	require.NoError(t, err)
	assert.Equal(t, newer.dir, found.dir, "the newer record wins a collision")
	assert.NotEqual(t, older.dir, found.dir)

	for _, threadID := range []string{"thr_3", "thr_missing", "", "../thr_1", `a\b`} {
		_, err := findCodewhaleStore(root, threadID)
		assert.ErrorIs(t, err, errThreadNotStored, threadID)
	}
	_, err = findCodewhaleStore(filepath.Join(root, "absent"), "thr_1")
	assert.ErrorIs(t, err, errThreadNotStored)
	_, err = findCodewhaleStore("", "thr_1")
	assert.ErrorIs(t, err, errThreadNotStored)
}

func TestCodewhaleStoredSessions(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	root := filepath.Join(home, ".codewhale", codewhaleStoresDirName)
	workspace := filepath.Join(home, "project")
	now := time.Now().UTC().Truncate(time.Second)

	seedStore(t, root, "store-titled", threadRecord{ID: "thr_titled", Workspace: workspace, LatestTurnID: "turn_a", Title: "A titled thread", UpdatedAt: now.Format(time.RFC3339)}, nil, now)
	seedStore(t, root, "store-untitled", threadRecord{ID: "thr_untitled", Workspace: workspace, LatestTurnID: "turn_b", UpdatedAt: now.Add(-time.Minute).Format(time.RFC3339)}, &turnRecord{ID: "turn_b", InputSummary: "Count the files."}, now.Add(-time.Minute))
	// Each of these is not a session worth offering.
	seedStore(t, root, "store-silent", threadRecord{ID: "thr_silent", Workspace: workspace}, nil, now)
	seedStore(t, root, "store-archived", threadRecord{ID: "thr_archived", Workspace: workspace, LatestTurnID: "turn_c", Archived: true}, nil, now)
	seedStore(t, root, "store-elsewhere", threadRecord{ID: "thr_elsewhere", Workspace: filepath.Join(home, "other"), LatestTurnID: "turn_d"}, nil, now)
	misnamed := seedStore(t, root, "store-misnamed", threadRecord{ID: "thr_real", Workspace: workspace, LatestTurnID: "turn_e"}, nil, now)
	require.NoError(t, os.Rename(misnamed.threadPath("thr_real"), misnamed.threadPath("thr_other")))

	sessions, err := codewhaleStoredSessions(context.Background(), agent.StoredSessionQuery{WorkingDir: workspace, HomeDir: home, Getenv: agenttest.FixtureEnv(nil)})
	require.NoError(t, err)
	require.Len(t, sessions, 2)
	assert.Equal(t, "thr_titled", sessions[0].Handle)
	assert.Equal(t, "A titled thread", sessions[0].Title)
	assert.Equal(t, now, sessions[0].UpdatedAt.UTC())
	assert.Equal(t, "thr_untitled", sessions[1].Handle)
	assert.Equal(t, "Count the files.", sessions[1].Title, "an untitled thread takes its latest turn's prompt")

	limited, err := codewhaleStoredSessions(context.Background(), agent.StoredSessionQuery{WorkingDir: workspace, HomeDir: home, Getenv: agenttest.FixtureEnv(nil), Limit: 1})
	require.NoError(t, err)
	assert.Len(t, limited, 1)
}

func TestCodewhaleStoredSessionsFindNothingWithoutAStore(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	for _, query := range []agent.StoredSessionQuery{
		{WorkingDir: filepath.Join(home, "project"), HomeDir: home, Getenv: agenttest.FixtureEnv(nil)},
		{WorkingDir: "  ", HomeDir: home, Getenv: agenttest.FixtureEnv(nil)},
	} {
		sessions, err := codewhaleStoredSessions(context.Background(), query)
		require.NoError(t, err)
		assert.Empty(t, sessions)
	}
}

func TestCodewhaleStoredSessionsSkipAnUnreadableRecord(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	root := filepath.Join(home, ".codewhale", codewhaleStoresDirName)
	workspace := filepath.Join(home, "project")
	store := seedStore(t, root, "store-broken", threadRecord{ID: "thr_broken", Workspace: workspace, LatestTurnID: "turn_a"}, nil, time.Now())
	require.NoError(t, os.WriteFile(store.threadPath("thr_broken"), []byte("{not json"), 0o600))

	sessions, err := codewhaleStoredSessions(context.Background(), agent.StoredSessionQuery{WorkingDir: workspace, HomeDir: home, Getenv: agenttest.FixtureEnv(nil)})
	require.NoError(t, err)
	assert.Empty(t, sessions)
}

func TestLatestTurnSummaryRefusesAPath(t *testing.T) {
	t.Parallel()
	store := codewhaleStore{dir: t.TempDir()}
	assert.Empty(t, latestTurnSummary(store, ""))
	assert.Empty(t, latestTurnSummary(store, "../turn"))
	assert.Empty(t, latestTurnSummary(store, "turn_absent"))
}

func TestReadsSessionStore(t *testing.T) {
	t.Parallel()
	agenttest.RequireReadsSessionStore(t, Registration().Plugin, func(t *testing.T, home, dir string) string {
		root := filepath.Join(home, ".codewhale", codewhaleStoresDirName)
		seedStore(t, root, "store-conformance", threadRecord{ID: "thr_conformance", Workspace: dir, LatestTurnID: "turn_1", UpdatedAt: time.Now().UTC().Format(time.RFC3339)}, nil, time.Now())
		return "thr_conformance"
	})
}

// The thread record in a store and on the wire is one shape: the picker reads
// the file the runtime writes with the struct the REST reply decodes into.
func TestThreadRecordReadsTheStoredShape(t *testing.T) {
	t.Parallel()
	stored := []byte(`{"schema_version":2,"id":"thr_97fda004","created_at":"2026-09-23T18:10:25Z","updated_at":"2026-09-23T18:10:36Z","model":"mock-model","model_provider":"openai","model_provider_id":"openai","workspace":"/w","mode":"plan","permission_posture":"ask","allow_shell":true,"latest_turn_id":"turn_8c418bde","archived":false}`)
	var thread threadRecord
	require.NoError(t, json.Unmarshal(stored, &thread))
	assert.Equal(t, threadRecord{
		ID: "thr_97fda004", CreatedAt: "2026-09-23T18:10:25Z", UpdatedAt: "2026-09-23T18:10:36Z",
		Model: "mock-model", ModelProvider: "openai", ModelProviderID: "openai", Workspace: "/w",
		Mode: "plan", PermissionPosture: "ask", LatestTurnID: "turn_8c418bde",
	}, thread)
}

// A query with no home dir falls back to the process's home. When the process
// has none either, no store resolves, and the picker lists nothing rather than
// read a relative path. Not parallel: it clears the process's home for the
// whole test binary.
func TestCodewhaleHomeWithNoHome(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")
	query := agent.StoredSessionQuery{WorkingDir: "/w", Getenv: agenttest.FixtureEnv(nil)}
	require.Empty(t, query.Home(), "the process has no home")
	assert.Empty(t, codewhaleHome(query), "no home and no CODEWHALE_HOME resolve nothing")
	assert.Empty(t, codewhaleStoresRoot(query))
	sessions, err := codewhaleStoredSessions(context.Background(), query)
	require.NoError(t, err)
	assert.Empty(t, sessions)

	// An absolute CODEWHALE_HOME needs no home of the user's.
	dir := t.TempDir()
	query.Getenv = agenttest.FixtureEnv(map[string]string{envHome: dir})
	assert.Equal(t, dir, codewhaleHome(query))
	// A CODEWHALE_HOME under `~` cannot resolve with no home, and the runtime
	// refuses a relative one.
	query.Getenv = agenttest.FixtureEnv(map[string]string{envHome: "~/codewhale"})
	assert.Empty(t, codewhaleHome(query))
}

// A picker that the reader closed stops the walk: the walk reads the stores one
// at a time, and it reads none after the end.
func TestCodewhaleStoredSessionsStopWhenTheContextEnds(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	root := filepath.Join(home, ".codewhale", codewhaleStoresDirName)
	workspace := filepath.Join(home, "project")
	seedStore(t, root, "store-a", threadRecord{ID: "thr_a", Workspace: workspace, LatestTurnID: "turn_a"}, nil, time.Now())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	sessions, err := codewhaleStoredSessions(ctx, agent.StoredSessionQuery{WorkingDir: workspace, HomeDir: home, Getenv: agenttest.FixtureEnv(nil)})
	require.NoError(t, err)
	assert.Empty(t, sessions)
}

func TestFindCodewhaleStoreEdges(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	// A thread path that is a directory is no thread record.
	store := codewhaleStore{dir: filepath.Join(root, "store-dir")}
	require.NoError(t, os.MkdirAll(store.threadPath("thr_1"), 0o700))
	_, err := findCodewhaleStore(root, "thr_1")
	assert.ErrorIs(t, err, errThreadNotStored)

	// The walk cannot list a root that is a file, and that is not a missing thread.
	file := filepath.Join(root, "not-a-dir")
	require.NoError(t, os.WriteFile(file, nil, 0o600))
	_, err = findCodewhaleStore(file, "thr_1")
	require.Error(t, err)
	assert.NotErrorIs(t, err, errThreadNotStored)
	assert.ErrorContains(t, err, "read the Codewhale stores")
}
