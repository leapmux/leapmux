package store_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// Every migration that hard-codes a SectionType number, and what it uses it
// for. A migration is frozen history: it cannot take a query parameter the way
// the queries in `db/queries/` now do, so these numbers are the one place where
// a renumber of `SectionType` would go unnoticed.
var sectionTypeMigrations = []string{
	filepath.Join("sqlite", "db", "migrations", "00001_initial.sql"),
	filepath.Join("postgres", "db", "migrations", "00001_initial.sql"),
	filepath.Join("mysql", "db", "migrations", "00001_initial.sql"),
}

// A user may hold any number of custom sections and exactly one of every other
// type. All three migrations encode that as `section_type <> 1` -- a partial
// unique index on sqlite and postgres, a generated column on mysql -- and each
// also defaults the column to 1.
//
// Renumber `SECTION_TYPE_WORKSPACES_CUSTOM` and the constraint silently swaps
// which type a user may hold many of: the new 1 becomes unconstrained, and
// custom sections become unique per user, so the second one a user creates
// fails on an index nobody edited.
//
// The queries no longer need this pin. `RenameWorkspaceSection`,
// `DeleteWorkspaceSection`, `HasDefaultSectionsForUser` and
// `IsWorkspaceInArchivedSection` all bind the type as a parameter from the enum
// itself, so a renumber propagates through them. Only the schema is left.
func TestSectionTypeCustomIsOneInEveryMigration(t *testing.T) {
	t.Parallel()

	require.Equal(t, leapmuxv1.SectionType(1), leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_CUSTOM,
		"the migrations below spell this number; renumbering it needs a migration, not just a proto edit")

	for _, rel := range sectionTypeMigrations {
		sql := statementsOf(t, rel)
		assert.Contains(t, sql, "section_type <> 1",
			"%s encodes SECTION_TYPE_WORKSPACES_CUSTOM as the literal 1", rel)
		// The STATEMENT that carries the rule, not merely the number. A whole
		// file search passes on the comment beside the constraint, so deleting
		// the constraint and keeping the comment would leave this green.
		hasPartialIndex := strings.Contains(sql, "ON workspace_sections(user_id, section_type) WHERE section_type <> 1")
		hasGeneratedColumn := strings.Contains(sql, "AS (CASE WHEN section_type <> 1 THEN section_type END)")
		assert.True(t, hasPartialIndex || hasGeneratedColumn,
			"%s must carry the one-per-type rule as a partial unique index or a generated column", rel)
	}
}

// One migration's SQL with every `--` comment line removed.
//
// The assertions below search for statement text, and a comment that merely
// DISCUSSES the constraint satisfies a whole-file substring search just as well
// as the constraint itself -- which would let a dropped index pass.
func statementsOf(t *testing.T, rel string) string {
	t.Helper()
	data, err := os.ReadFile(rel)
	require.NoError(t, err, "read %s", rel)
	var b strings.Builder
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}

// The default a bare INSERT falls back to is the same number, so a row written
// without an explicit type is a CUSTOM section.
func TestSectionTypeColumnDefaultsToCustom(t *testing.T) {
	t.Parallel()

	for _, rel := range sectionTypeMigrations {
		sql := statementsOf(t, rel)
		assert.True(t,
			strings.Contains(sql, "section_type INTEGER NOT NULL DEFAULT 1") ||
				strings.Contains(sql, "section_type INT NOT NULL DEFAULT 1"),
			"%s must default section_type to SECTION_TYPE_WORKSPACES_CUSTOM (%d)",
			rel, leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_CUSTOM)
	}
}
