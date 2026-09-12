package db_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// The worker migration is frozen history: it cannot take a query parameter the
// way a query in `db/queries/` can, so every ordinal it spells is a copy of a
// proto ordinal that nothing else moves. This file is what moves it.
//
// It mirrors internal/hub/store/enum_column_numbering_test.go, which does the
// same for the hub's six enum columns.
const workerMigration = "migrations/00001_initial.sql"

// Each enum column's CHECK range, and the proto enum it must match.
//
// The upper limit is the last ordinal the column ACCEPTS, which is not always
// the last the enum declares -- two columns deliberately admit less than the
// whole enum, and each says why. Every lower limit is 1 except goal_status,
// where 0 is the real state "no goal".
func TestEnumColumnChecksMatchTheirProtoRanges(t *testing.T) {
	t.Parallel()

	sql := statementsOfMigration(t)
	for _, c := range []struct {
		column       string
		check        string
		lastAccepted int32
		lastDeclared int32
		// narrower says the column admits LESS than the enum declares, and why.
		narrower string
	}{
		{
			column:       "agent_todos.status",
			check:        "CHECK (status BETWEEN 1 AND 4)",
			lastAccepted: int32(leapmuxv1.TodoStatus_TODO_STATUS_DELETED),
			lastDeclared: lastDeclaredOrdinal(t, leapmuxv1.TodoStatus_name),
		},
		{
			column:       "agent_background_tasks.kind",
			check:        "CHECK (kind BETWEEN 1 AND 2)",
			lastAccepted: int32(leapmuxv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_SHELL),
			lastDeclared: lastDeclaredOrdinal(t, leapmuxv1.BackgroundTaskKind_name),
		},
		{
			column:       "agent_background_tasks.status",
			check:        "CHECK (status BETWEEN 1 AND 6)",
			lastAccepted: int32(leapmuxv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_INTERRUPTED),
			lastDeclared: lastDeclaredOrdinal(t, leapmuxv1.BackgroundTaskStatus_name),
		},
		{
			column:       "agents.goal_status",
			check:        "CHECK (goal_status BETWEEN 0 AND 4)",
			lastAccepted: int32(leapmuxv1.AgentGoalStatus_AGENT_GOAL_STATUS_DONE),
			lastDeclared: lastDeclaredOrdinal(t, leapmuxv1.AgentGoalStatus_name),
			narrower: "DORMANT is derived from the running-agent map at read time and stored nowhere; " +
				"the CHECK is what makes that unrepresentable rather than merely unwritten",
		},
		{
			column:       "control_response_answers.state",
			check:        "CHECK (state BETWEEN 2 AND 5)",
			lastAccepted: int32(leapmuxv1.ControlResponseState_CONTROL_RESPONSE_STATE_COMPLETED),
			lastDeclared: lastDeclaredOrdinal(t, leapmuxv1.ControlResponseState_name),
			narrower: "READY and CANCELED describe a request with NO answer row, which controlResponseState " +
				"derives from control_requests; a row carrying one would contradict its own existence",
		},
	} {
		t.Run(c.column, func(t *testing.T) {
			assert.Containsf(t, sql, c.check, "%s must carry %q", c.column, c.check)
			if c.narrower != "" {
				assert.Lessf(t, c.lastAccepted, c.lastDeclared,
					"%s no longer admits less than its enum declares; delete the `narrower` note (%s)", c.column, c.narrower)
				return
			}
			assert.Equalf(t, c.lastDeclared, c.lastAccepted,
				"%s: the enum gained a value past the one its CHECK admits -- widen the CHECK, or say here why the column excludes it", c.column)
		})
	}
}

// goal_status is the one column whose zero is a REAL state, so ClearAgentGoal
// spells the literal 0. proto3 fixes the first enumerator at 0, so no renumber
// can move it -- this states that rather than guarding against it.
func TestClearAgentGoalWritesTheUnspecifiedOrdinal(t *testing.T) {
	t.Parallel()

	require.Equal(t, leapmuxv1.AgentGoalStatus(0),
		leapmuxv1.AgentGoalStatus_AGENT_GOAL_STATUS_UNSPECIFIED)
	data, err := os.ReadFile(filepath.Join("queries", "agents.sql"))
	require.NoError(t, err)
	assert.Contains(t, string(data), "goal_status = 0",
		"ClearAgentGoal must return goal_status to AGENT_GOAL_STATUS_UNSPECIFIED")
}

// No worker query may go back to spelling an enum as a WORD. The four-value
// status lists these replaced were the drift this change removed: a renamed Go
// constant left them stale, and nothing reported it.
func TestNoWorkerQueryStoresEnumWords(t *testing.T) {
	t.Parallel()

	files, err := filepath.Glob(filepath.Join("queries", "*.sql"))
	require.NoError(t, err)
	require.NotEmpty(t, files)
	for _, f := range files {
		data, err := os.ReadFile(f)
		require.NoError(t, err)
		for i, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "--") {
				continue
			}
			for _, column := range []string{"status", "state", "kind", "goal_status"} {
				assert.NotContainsf(t, line, column+" IN ('",
					"%s:%d compares %s against words; a database enum is bound by its ordinal", f, i+1, column)
			}
		}
	}
}

// One migration's SQL with every `--` comment line removed, so an assertion
// cannot be satisfied by a comment that merely DISCUSSES the constraint.
func statementsOfMigration(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(workerMigration)
	require.NoError(t, err)
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

// lastDeclaredOrdinal is the highest ordinal a generated `_name` map holds,
// which is the whole enum rather than the values a test remembered to list.
func lastDeclaredOrdinal(t *testing.T, names map[int32]string) int32 {
	t.Helper()
	require.NotEmpty(t, names)
	var highest int32
	for ordinal := range names {
		if ordinal > highest {
			highest = ordinal
		}
	}
	return highest
}
