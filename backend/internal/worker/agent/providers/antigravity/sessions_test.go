package antigravity

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const antigravitySessionDDL = `CREATE TABLE conversation_summaries (
	conversation_id text PRIMARY KEY,
	title text NOT NULL DEFAULT '',
	preview text NOT NULL DEFAULT '',
	step_count integer NOT NULL DEFAULT 0,
	last_modified_time datetime NOT NULL,
	workspace_uris text NOT NULL,
	status text NOT NULL DEFAULT '',
	source text NOT NULL DEFAULT '',
	project_id text NOT NULL DEFAULT '',
	agent_name text NOT NULL DEFAULT '',
	parent_conversation_id text NOT NULL DEFAULT '',
	nesting_depth integer NOT NULL DEFAULT 0,
	battle_id text NOT NULL DEFAULT '',
	winning_conversation_id text NOT NULL DEFAULT '',
	not_fully_idle numeric NOT NULL DEFAULT false,
	killed numeric NOT NULL DEFAULT false,
	last_user_input_time datetime NOT NULL,
	last_user_input_step_index integer NOT NULL DEFAULT -1,
	app_data_dir text NOT NULL DEFAULT '',
	raw_summary blob,
	group_id text NOT NULL DEFAULT ''
);`

func seedAntigravitySessions(t *testing.T, home, workingDir string) string {
	t.Helper()
	path := filepath.Join(home, ".gemini", "antigravity-cli", "conversation_summaries.db")
	uri := fileURI(workingDir)
	agenttest.NewFixtureDB(t, path, antigravitySessionDDL+`
INSERT INTO conversation_summaries (conversation_id,title,preview,last_modified_time,workspace_uris,parent_conversation_id,last_user_input_time) VALUES
('root','Root session','','2026-01-02 03:04:05','["`+uri+`"]','','2026-01-02 03:04:05'),
('child','Child session','','2026-01-03 03:04:05','["`+uri+`"]','root','2026-01-03 03:04:05'),
('other','Other folder','','2026-01-04 03:04:05','["file:///other"]','','2026-01-04 03:04:05');`)
	return "root"
}

func TestAntigravitySessions(t *testing.T) {
	home := t.TempDir()
	workingDir := filepath.Join(home, "folder with space")
	want := seedAntigravitySessions(t, home, workingDir)
	sessions, err := antigravityStoredSessions(context.Background(), agent.StoredSessionQuery{HomeDir: home, WorkingDir: workingDir})
	require.NoError(t, err)
	require.Len(t, sessions, 1)
	assert.Equal(t, want, sessions[0].Handle)
	assert.Equal(t, "Root session", sessions[0].Title)
	assert.Equal(t, time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), sessions[0].UpdatedAt)
}

func TestAntigravitySessionsAbsentStore(t *testing.T) {
	sessions, err := antigravityStoredSessions(context.Background(), agent.StoredSessionQuery{HomeDir: t.TempDir(), WorkingDir: t.TempDir()})
	require.NoError(t, err)
	assert.Empty(t, sessions)
}

func TestAntigravitySessionsSymlinkedWorkdir(t *testing.T) {
	home := t.TempDir()
	realDir := filepath.Join(home, "real")
	require.NoError(t, os.Mkdir(realDir, 0o700))
	resolvedDir, err := filepath.EvalSymlinks(realDir)
	require.NoError(t, err)
	seedAntigravitySessions(t, home, resolvedDir)
	link := filepath.Join(home, "link")
	require.NoError(t, os.Symlink(realDir, link))
	sessions, err := antigravityStoredSessions(context.Background(), agent.StoredSessionQuery{HomeDir: home, WorkingDir: link})
	require.NoError(t, err)
	require.Len(t, sessions, 1)
}

func TestAntigravityReadsSessionStore(t *testing.T) {
	agenttest.RequireReadsSessionStore(t, Registration().Plugin, func(t *testing.T, home, workingDir string) string {
		return seedAntigravitySessions(t, home, workingDir)
	})
}

func TestAntigravitySessionFixtureURI(t *testing.T) {
	assert.Equal(t, "file:///tmp/a%20b", fileURI("/tmp/a b"))
}
