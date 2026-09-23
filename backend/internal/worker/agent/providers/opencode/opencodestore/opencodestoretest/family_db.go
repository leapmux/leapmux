package opencodestoretest

import (
	"testing"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/require"
)

// FamilyDDL is the `session` table as OpenCode, Kilo and ZCode all ship
// it. Only the columns the shared reader touches are declared: a fixture that
// restated the whole table would drift against three CLIs without testing
// anything more.
const FamilyDDL = `
CREATE TABLE session (
  id text PRIMARY KEY,
  project_id text,
  parent_id text,
  directory text NOT NULL,
  title text NOT NULL DEFAULT '',
  time_created integer NOT NULL,
  time_updated integer,
  time_archived integer
);`

// SeedFamilyDB writes the shared fixture: one directory's sessions plus
// the rows every filter has to drop.
func SeedFamilyDB(t *testing.T, path, dir string) {
	t.Helper()
	agenttest.RequireHostAbsDir(t, dir)
	db := agenttest.NewFixtureDB(t, path, FamilyDDL)
	insert := func(id, parent, directory, title string, created, updated int64, archived any) {
		t.Helper()
		var parentVal any
		if parent != "" {
			parentVal = parent
		}
		_, err := db.Exec(
			`INSERT INTO session (id, parent_id, directory, title, time_created, time_updated, time_archived)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			id, parentVal, directory, title, created, updated, archived)
		require.NoError(t, err)
	}

	insert("ses_new", "", dir, "Newest session", 1_000, 3_000, nil)
	insert("ses_old", "", dir, "Older session", 1_000, 2_000, nil)
	insert("ses_subagent", "ses_new", dir, "Spawn subagent", 1_000, 9_000, nil)
	insert("ses_archived", "", dir, "Put away", 1_000, 9_000, 8_000)
	insert("ses_elsewhere", "", testutil.NativeAbsPath("/somewhere/else"), "Another directory", 1_000, 9_000, nil)
	insert("ses_no_updated", "", dir, "Never updated", 500, 0, nil)
}
