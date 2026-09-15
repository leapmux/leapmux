package db_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	workerdb "github.com/leapmux/leapmux/internal/worker/db"
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
		// columns is how many columns carry this exact CHECK text, where more
		// than one does. A Contains assertion cannot tell three columns that
		// share a CHECK from one column that kept it, so this counts them.
		columns int
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
		{
			column:       "messages.content_compression",
			check:        "CHECK (content_compression BETWEEN 1 AND 2)",
			lastAccepted: int32(leapmuxv1.ContentCompression_CONTENT_COMPRESSION_ZSTD),
			lastDeclared: lastDeclaredOrdinal(t, leapmuxv1.ContentCompression_name),
		},
		{
			column:       "messages.supplemental_content_compression",
			check:        "CHECK (supplemental_content_compression BETWEEN 1 AND 2)",
			lastAccepted: int32(leapmuxv1.ContentCompression_CONTENT_COMPRESSION_ZSTD),
			lastDeclared: lastDeclaredOrdinal(t, leapmuxv1.ContentCompression_name),
		},
		{
			// One CHECK, three columns: agents, messages and
			// control_response_answers each store the provider that produced
			// the row.
			column:       "agent_provider (agents, messages, control_response_answers)",
			check:        "CHECK (agent_provider BETWEEN 1 AND 11 AND agent_provider <> 3)",
			columns:      3,
			lastAccepted: int32(leapmuxv1.AgentProvider_AGENT_PROVIDER_ZCODE),
			lastDeclared: lastDeclaredOrdinal(t, leapmuxv1.AgentProvider_name),
		},
	} {
		t.Run(c.column, func(t *testing.T) {
			want := max(c.columns, 1)
			assert.Equalf(t, want, strings.Count(sql, c.check),
				"%d column(s) must carry %q; %s is one of them", want, c.check, c.column)
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

// The three agent_provider CHECKs exclude ordinal 3, which agent.proto RESERVES
// for a removed provider. The range assertion above reads the ceiling alone, so
// a new enumerator that claimed 3 would be a provider no column admits and
// nothing else would report it.
func TestAgentProviderReservesTheRemovedOrdinal(t *testing.T) {
	t.Parallel()

	name, taken := leapmuxv1.AgentProvider_name[3]
	assert.Falsef(t, taken,
		"AgentProvider 3 is reserved, and %q claimed it; widen the three agent_provider CHECKs or renumber", name)
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

// The two compression columns REFUSE an UNSPECIFIED 0, and the enum-range test
// above reads the migration text alone -- a CHECK the engine never enforced
// (a typo in the column name, a clause the table body never reached) would read
// the same. This runs the writes.
//
// The refusal matters because msgcodec.Decompress rejects an UNSPECIFIED
// compression, and the two columns then fail differently. A reader of `content`
// propagates that error, so the row is permanently unreadable. A reader of the
// supplement only LOGS it, so the supplement disappears and nobody sees why.
// Both failures surface far from the write, which is what the CHECK corrects.
func TestMessageCompressionColumnsRefuseUnspecified(t *testing.T) {
	t.Parallel()

	connection, err := workerdb.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, connection.Close()) })
	require.NoError(t, workerdb.Migrate(t.Context(), connection))
	_, err = connection.ExecContext(t.Context(), `INSERT INTO agents (id) VALUES ('agent-1')`)
	require.NoError(t, err)

	insert := func(id string, compression, supplementalCompression leapmuxv1.ContentCompression) error {
		_, err := connection.ExecContext(t.Context(), `
			INSERT INTO messages
			(id, agent_id, seq, source, content, content_compression, supplemental_content_compression, agent_provider)
			VALUES (?, 'agent-1', ?, ?, '{}', ?, ?, ?)`,
			id, len(id), leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
			compression, supplementalCompression,
			leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
		return err
	}
	none := leapmuxv1.ContentCompression_CONTENT_COMPRESSION_NONE
	zstd := leapmuxv1.ContentCompression_CONTENT_COMPRESSION_ZSTD
	unspecified := leapmuxv1.ContentCompression_CONTENT_COMPRESSION_UNSPECIFIED

	require.NoError(t, insert("a", none, none), "NONE is the first accepted ordinal")
	require.NoError(t, insert("bb", zstd, zstd), "ZSTD is the last accepted ordinal")
	assert.ErrorContains(t, insert("ccc", unspecified, none),
		"CHECK constraint failed", "content_compression must refuse UNSPECIFIED")
	assert.ErrorContains(t, insert("dddd", none, unspecified),
		"CHECK constraint failed", "supplemental_content_compression must refuse UNSPECIFIED")
}

// agents.agent_provider refuses an UNSPECIFIED 0, the same as its two siblings.
// Every agent runs ONE provider's process, so a 0 is a field the writer forgot.
// Both writers of the table are covered: CreateAgent for a root agent, and
// CreateChildAgent for a subagent transcript, which copies the parent's value.
func TestAgentProviderColumnRefusesUnspecified(t *testing.T) {
	t.Parallel()

	connection, err := workerdb.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, connection.Close()) })
	require.NoError(t, workerdb.Migrate(t.Context(), connection))

	createAgent := func(id string, provider leapmuxv1.AgentProvider) error {
		_, err := connection.ExecContext(t.Context(),
			`INSERT INTO agents (id, agent_provider) VALUES (?, ?)`, id, provider)
		return err
	}
	createChild := func(id, parent string, provider leapmuxv1.AgentProvider) error {
		_, err := connection.ExecContext(t.Context(),
			`INSERT INTO agents (id, parent_agent_id, agent_provider) VALUES (?, ?, ?)`, id, parent, provider)
		return err
	}
	claude := leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE
	unspecified := leapmuxv1.AgentProvider_AGENT_PROVIDER_UNSPECIFIED

	require.NoError(t, createAgent("root", claude))
	require.NoError(t, createChild("child", "root", claude))
	assert.ErrorContains(t, createAgent("no-provider", unspecified),
		"CHECK constraint failed", "a root agent must state its provider")
	assert.ErrorContains(t, createChild("child-no-provider", "root", unspecified),
		"CHECK constraint failed", "a child agent must carry the parent's provider")
	assert.ErrorContains(t, createAgent("removed-ordinal", leapmuxv1.AgentProvider(3)),
		"CHECK constraint failed", "ordinal 3 is reserved for a removed provider")
}
