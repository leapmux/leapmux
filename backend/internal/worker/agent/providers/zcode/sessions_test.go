package zcode

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/leapmux/leapmux/internal/util/testutil"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/opencode/opencodestore/opencodestoretest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// zcodeStorePathsFor uses a native preparation receipt for the supplied database.
func zcodeStorePathsFor(t *testing.T, home string, nativePath ...string) zcodeToolStoreLocation {
	t.Helper()
	path := filepath.Join(home, ".zcode", "cli", "db", "db.sqlite")
	if len(nativePath) > 0 {
		path = nativePath[0]
	}
	return zcodeTestStorePaths(t, agent.StoredSessionQuery{HomeDir: home, Getenv: agenttest.FixtureEnv(nil)}, path)
}

func zcodeTestStorePaths(t *testing.T, q agent.StoredSessionQuery, nativePath string) zcodeToolStoreLocation {
	t.Helper()
	location, err := zcodeToolStorePaths(t.Context(), q, newZCodeTestStorageQuery(nativePath, "", ""))
	require.NoError(t, err)
	return location
}

func zcodeTestDBPath(t *testing.T, q agent.StoredSessionQuery, nativePath string) string {
	t.Helper()
	receipt := filepath.Join(t.TempDir(), "native-query-receipt.json")
	path, err := zcodeSessionDBPath(t.Context(), q, newZCodeTestStorageQuery(nativePath, "", receipt))
	require.NoError(t, err)
	data, err := os.ReadFile(receipt)
	require.NoError(t, err, "the native command must receive its reuse acknowledgement")
	var observed struct {
		Home   string `json:"home"`
		Config string `json:"config"`
	}
	require.NoError(t, json.Unmarshal(data, &observed))
	assert.Equal(t, q.Home(), observed.Home)
	config, readErr := os.ReadFile(filepath.Join(q.Home(), ".zcode", "cli", "config.json"))
	if readErr == nil {
		assert.Equal(t, string(config), observed.Config, "native validation receives the unchanged complete config")
	}
	return path
}

// writeZCodeSessionDBPath states storage.sessionDbPath in the CLI configuration.
func writeZCodeSessionDBPath(t *testing.T, home, databasePath string) {
	t.Helper()
	agenttest.WriteFixtureFile(t, filepath.Join(home, ".zcode", "cli", "config.json"),
		`{"storage":{"sessionDbPath":`+agenttest.JSONString(databasePath)+`}}`)
}

func TestZCodeToolStorePaths(t *testing.T) {
	t.Parallel()

	t.Run("the stock layout keeps the database and output files as siblings", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(home, ".zcode", "cli", "artifacts"), 0o700))
		location := zcodeStorePathsFor(t, home)
		assert.Equal(t, filepath.Join(home, ".zcode", "cli", "db", "db.sqlite"), location.databasePath)
		assert.Equal(t, filepath.Join(home, ".zcode", "cli", "artifacts"), location.outputFileRoot,
			"a live installation holds cli/db/db.sqlite beside cli/artifacts")
	})

	t.Run("an absent output directory still reports the stock path", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()
		assert.Equal(t, filepath.Join(home, ".zcode", "cli", "artifacts"), zcodeStorePathsFor(t, home).outputFileRoot,
			"an absent image directory cannot select another database-relative root")
	})

	t.Run("sessionDbPath moves only the database", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()
		moved := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(moved, "cli", "artifacts"), 0o700))
		writeZCodeSessionDBPath(t, home, filepath.Join(moved, "cli", "db", "db.sqlite"))
		location := zcodeStorePathsFor(t, home, filepath.Join(moved, "cli", "db", "db.sqlite"))
		assert.Equal(t, filepath.Join(moved, "cli", "db", "db.sqlite"), location.databasePath)
		assert.Equal(t, filepath.Join(home, ".zcode", "cli", "artifacts"), location.outputFileRoot,
			"the independent storage directory keeps its image output files")
	})

	t.Run("an existing output directory stays on its configured storage root", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()
		moved := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(home, ".zcode", "cli", "artifacts"), 0o700))
		require.NoError(t, os.MkdirAll(filepath.Join(moved, "cli", "artifacts"), 0o700))
		writeZCodeSessionDBPath(t, home, filepath.Join(moved, "cli", "db", "db.sqlite"))
		assert.Equal(t, filepath.Join(home, ".zcode", "cli", "artifacts"), zcodeStorePathsFor(t, home).outputFileRoot)
	})

	t.Run("a regular file at the output path cannot become a storage directory", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()
		moved := t.TempDir()
		agenttest.WriteFixtureFile(t, filepath.Join(home, ".zcode", "cli", "artifacts"), "not a directory")
		require.NoError(t, os.MkdirAll(filepath.Join(moved, "cli", "artifacts"), 0o700))
		writeZCodeSessionDBPath(t, home, filepath.Join(moved, "cli", "db", "db.sqlite"))
		assert.Equal(t, filepath.Join(home, ".zcode", "cli", "artifacts"), zcodeStorePathsFor(t, home).outputFileRoot)
	})

	t.Run("a database with no grandparent directory keeps the storage-root path", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()
		writeZCodeSessionDBPath(t, home, "db.sqlite")
		assert.Equal(t, filepath.Join(home, ".zcode", "cli", "artifacts"), zcodeStorePathsFor(t, home).outputFileRoot)
	})
}

func TestZCodeOutputFileRootRemainsIndependentFromDatabase(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	for _, database := range []string{filepath.Join(home, "native.db"), filepath.Join(home, "moved", "cli", "db", "db.sqlite")} {
		assert.Equal(t, filepath.Join(home, ".zcode", "cli", "artifacts"), zcodeStorePathsFor(t, home, database).outputFileRoot)
	}
}

func TestZCodeSessionDBPath(t *testing.T) {
	t.Parallel()

	t.Run("defaults to the stock installation", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()
		q := agent.StoredSessionQuery{HomeDir: home, Getenv: agenttest.FixtureEnv(nil)}
		assert.Equal(t, filepath.Join(home, ".zcode", "cli", "db", "db.sqlite"), zcodeTestDBPath(t, q, filepath.Join(home, ".zcode", "cli", "db", "db.sqlite")))
	})

	t.Run("follows storage.sessionDbPath from the CLI configuration", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()
		agenttest.WriteFixtureFile(t, filepath.Join(home, ".zcode", "cli", "config.json"),
			`{"storage":{"dir":"~/.zcode","sessionDbPath":"~/elsewhere/db.sqlite"}}`)

		q := agent.StoredSessionQuery{HomeDir: home, Getenv: agenttest.FixtureEnv(nil)}
		assert.Equal(t, filepath.Join(home, "elsewhere", "db.sqlite"), zcodeTestDBPath(t, q, filepath.Join(home, "elsewhere", "db.sqlite")),
			"the explicit database path wins, and its leading ~ expands")
	})

	t.Run("keeps the native database when only storage.dir changes", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()
		moved := testutil.NativeAbsPath("/moved/zcode")
		agenttest.WriteFixtureFile(t, filepath.Join(home, ".zcode", "cli", "config.json"),
			`{"storage":{"dir":`+agenttest.JSONString(moved)+`}}`)

		q := agent.StoredSessionQuery{HomeDir: home, Getenv: agenttest.FixtureEnv(nil)}
		assert.Equal(t, filepath.Join(home, ".zcode", "cli", "db", "db.sqlite"), zcodeTestDBPath(t, q, filepath.Join(home, ".zcode", "cli", "db", "db.sqlite")))
	})

	t.Run("ZCODE_STORAGE_DIR wins over the configuration", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()
		agenttest.WriteFixtureFile(t, filepath.Join(home, ".zcode", "cli", "config.json"),
			`{"storage":{"dir":`+agenttest.JSONString(testutil.NativeAbsPath("/moved/zcode"))+`}}`)

		fromEnv := testutil.NativeAbsPath("/env/zcode")
		q := agent.StoredSessionQuery{HomeDir: home, Getenv: agenttest.FixtureEnv(map[string]string{"ZCODE_STORAGE_DIR": fromEnv})}
		assert.Equal(t, filepath.Join(home, ".zcode", "cli", "db", "db.sqlite"), zcodeTestDBPath(t, q, filepath.Join(home, ".zcode", "cli", "db", "db.sqlite")))
	})

	t.Run("survives a malformed configuration", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()
		agenttest.WriteFixtureFile(t, filepath.Join(home, ".zcode", "cli", "config.json"), "{ not json")

		q := agent.StoredSessionQuery{HomeDir: home, Getenv: agenttest.FixtureEnv(nil)}
		assert.Equal(t, filepath.Join(home, ".zcode", "cli", "db", "db.sqlite"), zcodeTestDBPath(t, q, filepath.Join(home, ".zcode", "cli", "db", "db.sqlite")),
			"an unreadable configuration falls back to the stock layout rather than reporting no sessions")
	})
}

func TestZCodeStoredSessions_EndToEnd(t *testing.T) {
	t.Parallel()
	dir := testutil.NativeAbsPath("/Users/dev/project")
	home := t.TempDir()
	opencodestoretest.SeedFamilyDB(t, filepath.Join(home, ".zcode", "cli", "db", "db.sqlite"), dir)

	got, err := zcodeProvider{storageQuery: newZCodeTestStorageQuery(filepath.Join(home, ".zcode", "cli", "db", "db.sqlite"), "", "")}.ListStoredSessions(context.Background(), agent.StoredSessionQuery{
		WorkingDir: dir,
		HomeDir:    home,
		Getenv:     agenttest.FixtureEnv(nil),
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"ses_new", "ses_old", "ses_no_updated"}, agenttest.Handles(got),
		"ZCode's session table is OpenCode's, so parent_id already excludes its subagent_child rows")
}

func TestZCodeReadsItsSessionStore(t *testing.T) {
	t.Parallel()
	agenttest.RequireReadsSessionStore(t, zcodeProvider{storageQuery: zcodeTestHomeStorageQuery{}}, func(t *testing.T, home, dir string) string {
		opencodestoretest.SeedFamilyDB(t, filepath.Join(home, ".zcode", "cli", "db", "db.sqlite"), dir)
		return "ses_new"
	})
}

func TestZCodeSessionDatabaseAndOutputFileSettingsRemainIndependent(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name          string
		storageDir    bool
		envDir        bool
		database      bool
		outputFileDir bool
	}{
		{name: "native defaults"},
		{name: "user storage directory changes only the output file root", storageDir: true},
		{name: "environment storage directory changes only the output file root", storageDir: true, envDir: true},
		{name: "explicit database keeps the absent default output file root", database: true},
		{name: "explicit database keeps the existing default output file root", database: true, outputFileDir: true},
		{name: "independent explicit database and output file roots", storageDir: true, database: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			storage := filepath.Join(home, "native-full-output-storage")
			database := filepath.Join(home, "native-database", "db.sqlite")
			settings := map[string]string{}
			if tc.storageDir {
				settings["dir"] = storage
			}
			if tc.database {
				settings["sessionDbPath"] = database
			}
			config, err := json.Marshal(map[string]any{"storage": settings})
			require.NoError(t, err)
			agenttest.WriteFixtureFile(t, filepath.Join(home, ".zcode", "cli", "config.json"), string(config))
			env := map[string]string{}
			if tc.envDir {
				storage = filepath.Join(home, "environment-full-output-storage")
				env["ZCODE_STORAGE_DIR"] = storage
			}
			if !tc.storageDir && !tc.envDir {
				storage = filepath.Join(home, ".zcode")
			}
			if !tc.database {
				database = filepath.Join(home, ".zcode", "cli", "db", "db.sqlite")
			}
			outputFiles := filepath.Join(storage, "cli", "artifacts")
			if tc.outputFileDir {
				require.NoError(t, os.MkdirAll(outputFiles, 0o700))
			}
			q := agent.StoredSessionQuery{HomeDir: home, Getenv: agenttest.FixtureEnv(env)}
			location := zcodeTestStorePaths(t, q, database)
			assert.Equal(t, database, location.databasePath, "native storage.dir does not relocate storage.sessionDbPath")
			assert.Equal(t, outputFiles, location.outputFileRoot, "native output files do not follow a moved database")
		})
	}
}

func TestZCodeSessionDatabaseEnvironmentOverridesUserConfiguration(t *testing.T) {
	t.Parallel()
	for _, key := range []string{"ZCODE_SESSION_DB_PATH", "ZCODE_SESSION_DB"} {
		t.Run(key, func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			writeZCodeSessionDBPath(t, home, filepath.Join(home, "user-database", "db.sqlite"))
			database := filepath.Join(home, "environment-database", "db.sqlite")
			q := agent.StoredSessionQuery{HomeDir: home, Getenv: agenttest.FixtureEnv(map[string]string{key: database})}
			assert.Equal(t, database, zcodeTestDBPath(t, q, database), "the native environment overrides the user database setting")
		})
	}
}

func TestZCodeStoredSessionsReadsTheIndependentNativeDatabase(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	directory := filepath.Join(t.TempDir(), "workspace")
	storage := filepath.Join(home, "separate-full-output")
	opencodestoretest.SeedFamilyDB(t, filepath.Join(home, ".zcode", "cli", "db", "db.sqlite"), directory)
	got, err := (zcodeProvider{storageQuery: newZCodeTestStorageQuery(filepath.Join(home, ".zcode", "cli", "db", "db.sqlite"), "", "")}).ListStoredSessions(t.Context(), agent.StoredSessionQuery{
		HomeDir: home, WorkingDir: directory,
		Getenv: agenttest.FixtureEnv(map[string]string{"ZCODE_STORAGE_DIR": storage}),
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"ses_new", "ses_old", "ses_no_updated"}, agenttest.Handles(got))
}

func TestZCodeStoredSessionsRejectsUnknownConflictingDatabaseAliasOrder(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	got, err := (zcodeProvider{storageQuery: newZCodeTestStorageQuery(filepath.Join(home, ".zcode", "cli", "db", "db.sqlite"), "", "")}).ListStoredSessions(t.Context(), agent.StoredSessionQuery{
		HomeDir: home, WorkingDir: t.TempDir(),
		Getenv: agenttest.FixtureEnv(map[string]string{
			"ZCODE_SESSION_DB_PATH": filepath.Join(home, "first.db"),
			"ZCODE_SESSION_DB":      filepath.Join(home, "second.db"),
		}),
	})
	require.Error(t, err, "a lookup-only environment cannot state which native alias wins")
	assert.Empty(t, got)
}

func TestZCodeStoragePathsUseTheNativeProjectMerge(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	ancestor := t.TempDir()
	repository := filepath.Join(ancestor, "repository")
	directory := filepath.Join(repository, "nested")
	require.NoError(t, os.MkdirAll(filepath.Join(repository, ".git"), 0o700))
	require.NoError(t, os.MkdirAll(directory, 0o700))
	userDB := filepath.Join(home, "user.db")
	projectDB := filepath.Join(repository, "ignored-project.db")
	storage := filepath.Join(directory, "project-storage")
	writeZCodeSessionDBPath(t, home, userDB)
	agenttest.WriteFixtureFile(t, filepath.Join(ancestor, "zcode.json"),
		`{"storage":{"sessionDbPath":`+agenttest.JSONString(filepath.Join(ancestor, "foreign.db"))+`}}`)
	agenttest.WriteFixtureFile(t, filepath.Join(repository, "zcode.json"),
		`{"storage":{"sessionDbPath":`+agenttest.JSONString(filepath.Join(repository, "earlier.db"))+`}}`)
	agenttest.WriteFixtureFile(t, filepath.Join(repository, ".zcode", "config.json"),
		`{"storage":{"sessionDbPath":`+agenttest.JSONString(projectDB)+`}}`)
	agenttest.WriteFixtureFile(t, filepath.Join(directory, "zcode.json"),
		`{"storage":{"dir":`+agenttest.JSONString(storage)+`}}`)
	q := agent.StoredSessionQuery{HomeDir: home, WorkingDir: directory, Getenv: agenttest.FixtureEnv(nil)}
	location := zcodeTestStorePaths(t, q, userDB)
	assert.Equal(t, userDB, location.databasePath, "the app-server opens its shared database before it reads workspace config")
	assert.Equal(t, filepath.Join(storage, "cli", "artifacts"), location.outputFileRoot, "the child config overrides only its declared storage field")
}

func TestZCodeStorageConfigKeepsNativeJSONParsingRules(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		config func(string) string
		valid  bool
	}{
		{name: "unknown fields stay opaque", valid: true, config: func(path string) string {
			return `{"unknown":{"counter":9007199254740993,"enabled":false,"empty":""},"storage":{"sessionDbPath":` + agenttest.JSONString(path) + `}}`
		}},
		{name: "comments are invalid JSON", config: func(path string) string {
			return `{"storage":{"sessionDbPath":` + agenttest.JSONString(path) + `}/* native JSON.parse rejects comments */}`
		}},
		{name: "a byte order mark is invalid JSON", config: func(path string) string {
			return "\xef\xbb\xbf" + `{"storage":{"sessionDbPath":` + agenttest.JSONString(path) + `}}`
		}},
		{name: "a trailing comma is invalid JSON", config: func(path string) string {
			return `{"storage":{"sessionDbPath":` + agenttest.JSONString(path) + `},}`
		}},
		{name: "an invalid recognized section rejects the complete patch", config: func(path string) string {
			return `{"permission":{"mode":"native-invalid-mode"},"storage":{"sessionDbPath":` + agenttest.JSONString(path) + `}}`
		}},
		{name: "an empty storage field rejects the complete patch", config: func(path string) string {
			return `{"storage":{"dir":"","sessionDbPath":` + agenttest.JSONString(path) + `}}`
		}},
		{name: "last duplicate JSON keys replace earlier values", valid: true, config: func(path string) string {
			return `{"storage":{"sessionDbPath":false},"storage":{"sessionDbPath":` + agenttest.JSONString(path) + `}}`
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			database := filepath.Join(home, "native-custom.db")
			path := filepath.Join(home, ".zcode", "cli", "config.json")
			config := tc.config(database)
			agenttest.WriteFixtureFile(t, path, config)
			want := filepath.Join(home, ".zcode", "cli", "db", "db.sqlite")
			if tc.valid {
				want = database
			}
			q := agent.StoredSessionQuery{HomeDir: home, Getenv: agenttest.FixtureEnv(nil)}
			assert.Equal(t, want, zcodeTestDBPath(t, q, want))
			unchanged, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Equal(t, config, string(unchanged), "the reader does not rewrite opaque config values")
		})
	}
}

func TestZCodeStorageConfigFollowsTheNativeConfigSymlink(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	path := filepath.Join(home, ".zcode", "cli", "config.json")
	target := filepath.Join(home, "native-linked-config.json")
	database := filepath.Join(home, "native-linked.db")
	agenttest.WriteFixtureFile(t, target, `{"storage":{"sessionDbPath":`+agenttest.JSONString(database)+`}}`)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("The host cannot create a symbolic link: %v", err)
	}
	assert.Equal(t, database, zcodeTestDBPath(t, agent.StoredSessionQuery{HomeDir: home, Getenv: agenttest.FixtureEnv(nil)}, database))
}

func TestZCodeStorageConfigSkipsANativeReadFailure(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	path := filepath.Join(home, ".zcode", "cli", "config.json")
	require.NoError(t, os.MkdirAll(path, 0o700))
	assert.Equal(t, filepath.Join(home, ".zcode", "cli", "db", "db.sqlite"),
		zcodeTestDBPath(t, agent.StoredSessionQuery{HomeDir: home, Getenv: agenttest.FixtureEnv(nil)}, filepath.Join(home, ".zcode", "cli", "db", "db.sqlite")))
}

func TestZCodeProjectStorageStopsAtANativeWorktreeFile(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	ancestor := t.TempDir()
	repository := filepath.Join(ancestor, "repository")
	directory := filepath.Join(repository, "nested")
	storage := filepath.Join(repository, "native-repository-storage")
	agenttest.WriteFixtureFile(t, filepath.Join(repository, ".git"), "gitdir: native-worktree-control")
	require.NoError(t, os.MkdirAll(directory, 0o700))
	agenttest.WriteFixtureFile(t, filepath.Join(ancestor, "zcode.json"),
		`{"storage":{"dir":`+agenttest.JSONString(filepath.Join(ancestor, "foreign-storage"))+`}}`)
	agenttest.WriteFixtureFile(t, filepath.Join(repository, "zcode.json"),
		`{"storage":{"dir":`+agenttest.JSONString(filepath.Join(repository, "earlier-storage"))+`}}`)
	agenttest.WriteFixtureFile(t, filepath.Join(repository, ".zcode", "config.json"),
		`{"storage":{"dir":`+agenttest.JSONString(storage)+`}}`)
	location := zcodeTestStorePaths(t, agent.StoredSessionQuery{HomeDir: home, WorkingDir: directory, Getenv: agenttest.FixtureEnv(nil)}, filepath.Join(home, ".zcode", "cli", "db", "db.sqlite"))
	assert.Equal(t, filepath.Join(storage, "cli", "artifacts"), location.outputFileRoot)
	assert.Equal(t, filepath.Join(home, ".zcode", "cli", "db", "db.sqlite"), location.databasePath)
}

func TestZCodeStoragePathsResolveRelativeValuesAgainstTheNativeWorkingDirectory(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	directory := t.TempDir()
	agenttest.WriteFixtureFile(t, filepath.Join(home, ".zcode", "cli", "config.json"),
		`{"storage":{"dir":"native-storage","sessionDbPath":"native-state/db.sqlite"}}`)
	location := zcodeTestStorePaths(t, agent.StoredSessionQuery{HomeDir: home, WorkingDir: directory, Getenv: agenttest.FixtureEnv(nil)}, filepath.Join(directory, "native-state", "db.sqlite"))
	assert.Equal(t, filepath.Join(directory, "native-state", "db.sqlite"), location.databasePath)
	assert.Equal(t, filepath.Join(directory, "native-storage", "cli", "artifacts"), location.outputFileRoot)
}

func TestZCodeSessionDatabaseKeepsNativeEnvironmentEntryOrder(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		entries []string
		last    string
	}{
		{name: "native alias follows the primary key", entries: []string{"ZCODE_SESSION_DB_PATH=first.db", "ZCODE_SESSION_DB=second.db"}, last: "second.db"},
		{name: "primary key follows the native alias", entries: []string{"ZCODE_SESSION_DB=first.db", "ZCODE_SESSION_DB_PATH=second.db"}, last: "second.db"},
		{name: "a final duplicate retains its position", entries: []string{"ZCODE_SESSION_DB_PATH=first.db", "ZCODE_SESSION_DB=second.db", "ZCODE_SESSION_DB_PATH=third.db"}, last: "third.db"},
		{name: "malformed entries do not replace the native path", entries: []string{"ZCODE_SESSION_DB=second.db", "ZCODE_SESSION_DB_PATH", "=foreign.db"}, last: "second.db"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			directory := t.TempDir()
			q := agent.StoredSessionQuery{HomeDir: home, WorkingDir: directory, EnvEntries: tc.entries}
			assert.Equal(t, filepath.Join(directory, tc.last), zcodeTestDBPath(t, q, filepath.Join(directory, tc.last)))
		})
	}
}
