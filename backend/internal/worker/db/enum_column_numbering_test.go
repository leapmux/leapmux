package db_test

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	workerdb "github.com/leapmux/leapmux/internal/worker/db"
)

// The initial schema cannot bind a query parameter. These tests compare each
// literal ordinal range with the proto enum that supplies its numbering.
// The hub store checks its schema with the same method.
const workerMigration = "migrations/00001_initial.sql"

type integerEnumColumn struct {
	label, update, read string
	minimum, maximum    int64
	// admitted lists the ordinals the column accepts when the set is NOT the
	// plain minimum-to-maximum range. Nil means every ordinal in that range.
	admitted []int64
}

// accepts reports whether the column takes one ordinal.
func (c integerEnumColumn) accepts(ordinal int64) bool {
	if c.admitted == nil {
		return c.minimum <= ordinal && ordinal <= c.maximum
	}
	return slices.Contains(c.admitted, ordinal)
}

func integerEnumColumns() []integerEnumColumn {
	return []integerEnumColumn{
		{"agents.agent_provider", "UPDATE agents SET agent_provider = ?", "SELECT typeof(agent_provider), agent_provider FROM agents", 1, int64(len(leapmuxv1.AgentProvider_name) - 1), nil},
		{"agents.goal_status", "UPDATE agents SET goal_status = ?", "SELECT typeof(goal_status), goal_status FROM agents", 0, 5, nil},
		{"messages.content_compression", "UPDATE messages SET content_compression = ?", "SELECT typeof(content_compression), content_compression FROM messages", 1, 2, nil},
		{"messages.supplemental_content_compression", "UPDATE messages SET supplemental_content_compression = ?", "SELECT typeof(supplemental_content_compression), supplemental_content_compression FROM messages", 1, 2, nil},
		{"messages.agent_provider", "UPDATE messages SET agent_provider = ?", "SELECT typeof(agent_provider), agent_provider FROM messages", 1, int64(len(leapmuxv1.AgentProvider_name) - 1), nil},
		{"agent_input_queue_items.kind", "UPDATE agent_input_queue_items SET kind = ?", "SELECT typeof(kind), kind FROM agent_input_queue_items", 1, 8, nil},
		{"agent_input_queue_items.state", "UPDATE agent_input_queue_items SET state = ?", "SELECT typeof(state), state FROM agent_input_queue_items", 1, 5, nil},
		{"control_deliveries.state", "UPDATE control_deliveries SET state = ?", "SELECT typeof(state), state FROM control_deliveries", 1, 4, nil},
		{"control_deliveries.kind", "UPDATE control_deliveries SET kind = ? WHERE id = 'enum-unattached'", "SELECT typeof(kind), kind FROM control_deliveries WHERE id = 'enum-unattached'", 1, 2, nil},
		{"control_response_payloads.agent_provider", "UPDATE control_response_payloads SET agent_provider = ?", "SELECT typeof(agent_provider), agent_provider FROM control_response_payloads", 1, int64(len(leapmuxv1.AgentProvider_name) - 1), nil},
		// The tie CHECK demands follow_up text exactly when the kind is non-zero,
		// so the update writes both from one bound parameter (?1 appears twice).
		// A range refusal then cannot hide behind a tie refusal.
		{"control_response_payloads.follow_up_kind", "UPDATE control_response_payloads SET follow_up_kind = ?1, follow_up = CASE WHEN ?1 = 0 THEN '' ELSE 'answer' END", "SELECT typeof(follow_up_kind), follow_up_kind FROM control_response_payloads", 0, 7, []int64{0, 6, 7}},
		{"agent_todos.status", "UPDATE agent_todos SET status = ?", "SELECT typeof(status), status FROM agent_todos", 1, 5, nil},
		{"agent_background_tasks.kind", "UPDATE agent_background_tasks SET kind = ?", "SELECT typeof(kind), kind FROM agent_background_tasks", 1, 3, nil},
		{"agent_background_tasks.status", "UPDATE agent_background_tasks SET status = ?", "SELECT typeof(status), status FROM agent_background_tasks", 1, 8, nil},
	}
}

func integerEnumStore(t *testing.T) *sql.DB {
	t.Helper()
	store, err := workerdb.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, store.Close()) })
	require.NoError(t, workerdb.Migrate(t.Context(), store))
	for _, statement := range []string{
		"INSERT INTO agents (id) VALUES ('enum-owner')",
		"INSERT INTO messages (id, agent_id, seq, source, content, content_compression) VALUES ('enum-message', 'enum-owner', 1, 1, X'7b7d', 1)",
		"INSERT INTO agent_input_queue_items (id, agent_id, order_index, kind, state) VALUES ('enum-input', 'enum-owner', 1, 1, 1)",
		"INSERT INTO control_deliveries (id, agent_id, kind, operation_id, instance_id, state) VALUES ('enum-response', 'enum-owner', 1, 'enum-request', 'enum-claim', 1)",
		"INSERT INTO control_response_payloads (delivery_id, agent_provider) VALUES ('enum-response', 1)",
		"INSERT INTO control_deliveries (id, agent_id, kind, operation_id, instance_id, state) VALUES ('enum-unattached', 'enum-owner', 1, 'unattached', '', 1)",
		"INSERT INTO control_deliveries (id, agent_id, kind, operation_id, instance_id, state) VALUES ('enum-review', 'enum-owner', 2, 'review', 'session', 1)",
		"INSERT INTO auto_review_approval_payloads (delivery_id, message_id) VALUES ('enum-review', 'enum-message')",
		"INSERT INTO agent_todos (agent_id, row_key, seq, content, status) VALUES ('enum-owner', 'enum-todo', 1, 'todo', 1)",
		"INSERT INTO agent_background_tasks (owner_agent_id, row_key, seq, kind, status) VALUES ('enum-owner', 'enum-task', 1, 1, 1)",
	} {
		_, err := store.ExecContext(t.Context(), statement)
		require.NoError(t, err)
	}
	return store
}

func TestEnumColumnsRejectFractionalOrdinals(t *testing.T) {
	t.Parallel()
	for _, column := range integerEnumColumns() {
		t.Run(column.label, func(t *testing.T) {
			store := integerEnumStore(t)
			for ordinal := column.minimum; ordinal <= column.maximum; ordinal++ {
				if !column.accepts(ordinal) {
					continue
				}
				_, err := store.ExecContext(t.Context(), column.update, ordinal)
				assert.NoError(t, err, "every declared stored ordinal must remain valid")
				if err != nil {
					continue
				}
				var storageType string
				var stored int64
				require.NoError(t, store.QueryRowContext(t.Context(), column.read).Scan(&storageType, &stored))
				assert.Equal(t, "integer", storageType)
				assert.Equal(t, ordinal, stored)
			}
			_, err := store.ExecContext(t.Context(), column.update, column.minimum)
			require.NoError(t, err)
			for _, invalid := range []any{float64(max(column.minimum, 1)) + 0.5, int64(1 << 40), nil} {
				_, err := store.ExecContext(t.Context(), column.update, invalid)
				assert.Error(t, err, "a non-ordinal must fail at the actual storage boundary")
				var storageType string
				var stored any
				require.NoError(t, store.QueryRowContext(t.Context(), column.read).Scan(&storageType, &stored))
				assert.Equal(t, "integer", storageType)
				assert.Equal(t, column.minimum, stored, "a refused value must leave the actual stored ordinal unchanged")
				_, err = store.ExecContext(t.Context(), column.update, column.minimum)
				require.NoError(t, err)
			}
			for ordinal := column.minimum - 1; ordinal <= column.maximum+1; ordinal++ {
				if column.accepts(ordinal) {
					continue
				}
				_, err := store.ExecContext(t.Context(), column.update, ordinal)
				assert.Errorf(t, err, "the ordinal %d lies outside the admitted set and must fail at the actual storage boundary", ordinal)
				var storageType string
				var stored any
				require.NoError(t, store.QueryRowContext(t.Context(), column.read).Scan(&storageType, &stored))
				assert.Equal(t, "integer", storageType)
				assert.Equal(t, column.minimum, stored, "a refused value must leave the actual stored ordinal unchanged")
				_, err = store.ExecContext(t.Context(), column.update, column.minimum)
				require.NoError(t, err)
			}
		})
	}
}

func TestEnumColumnIntegerChecksMatchTheirStorage(t *testing.T) {
	t.Parallel()
	statements := statementsOfMigration(t)
	labels := make([]string, 0, len(integerEnumColumns())+6)
	for _, column := range integerEnumColumns() {
		labels = append(labels, column.label)
	}
	labels = append(labels, "messages.source", "messages.mark_type", "messages.assembled_kind", "messages.completion", "agent_input_queue_state.pause_reason", "agent_input_queue_state.pause_owner")
	for _, label := range labels {
		t.Run(label, func(t *testing.T) {
			table, column, found := strings.Cut(label, ".")
			require.True(t, found)
			_, declaration, found := strings.Cut(statements, "CREATE TABLE "+table+" (")
			require.True(t, found)
			declaration, _, found = strings.Cut(declaration, "\n);")
			require.True(t, found)
			var columnDeclaration string
			for _, line := range strings.Split(declaration, "\n") {
				fields := strings.Fields(line)
				if len(fields) > 1 && fields[0] == column {
					columnDeclaration = strings.TrimSpace(line)
					continue
				}
				if columnDeclaration == "" {
					continue
				}
				if strings.HasPrefix(strings.TrimSpace(line), "CHECK (") {
					columnDeclaration += " " + strings.TrimSpace(line)
					continue
				}
				break
			}
			require.NotEmpty(t, columnDeclaration)
			assert.Contains(t, columnDeclaration, "typeof("+column+") = 'integer'", "each actual range enum must reject fractions in its own column declaration")
		})
	}
}

// idx_agent_input_queue_one_auto_retry_kind pins one pending retry per kind and
// agent. Its predicate spells three ordinals SQLite can only match
// syntactically -- a partial index cannot bind a parameter -- so this test
// keeps them tied to the proto enums a renumber would move.
func TestAutoRetryPartialIndexOrdinals(t *testing.T) {
	t.Parallel()

	queued := int64(leapmuxv1.AgentInputState_AGENT_INPUT_STATE_QUEUED)
	usageLimit := int64(leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_USAGE_LIMIT)
	serverError := int64(leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR)

	sql := statementsOfMigration(t)
	_, index, found := strings.Cut(sql, "CREATE UNIQUE INDEX idx_agent_input_queue_one_auto_retry_kind")
	require.True(t, found, "the schema must keep the one-auto-retry-per-kind index")
	index, _, found = strings.Cut(index, ";")
	require.True(t, found)
	assert.Contains(t, index, fmt.Sprintf("state = %d", queued),
		"the index's state literal is the AGENT_INPUT_STATE_QUEUED ordinal")
	assert.Contains(t, index, fmt.Sprintf("kind IN (%d, %d)", usageLimit, serverError),
		"the index's kind literals are the two AUTO_RETRY ordinals")

	connection, err := workerdb.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, connection.Close()) })
	require.NoError(t, workerdb.Migrate(t.Context(), connection))
	_, err = connection.ExecContext(t.Context(), `INSERT INTO agents (id) VALUES ('retry-owner')`)
	require.NoError(t, err)
	insert := func(id string, order int64, kind int64, state int64) error {
		_, err := connection.ExecContext(t.Context(),
			`INSERT INTO agent_input_queue_items (id, agent_id, order_index, kind, text, state) VALUES (?, 'retry-owner', ?, ?, 'Continue.', ?)`,
			id, order, kind, state)
		return err
	}
	require.NoError(t, insert("retry-usage", 1, usageLimit, queued))
	require.NoError(t, insert("retry-server", 2, serverError, queued),
		"the two retry kinds coexist for one agent")
	assert.ErrorContains(t, insert("retry-usage-2", 3, usageLimit, queued),
		"UNIQUE constraint failed", "a second queued retry of one kind is refused")
	require.NoError(t, insert("retry-failed", 3, usageLimit, int64(leapmuxv1.AgentInputState_AGENT_INPUT_STATE_FAILED)))
	_, err = connection.ExecContext(t.Context(), `UPDATE agent_input_queue_items SET state = ? WHERE id = 'retry-failed'`, queued)
	assert.ErrorContains(t, err, "UNIQUE constraint failed",
		"a queued retry conflicts with a FAILED row of the same kind, so cancellation must delete the row")
}

// The tie CHECK binds follow_up text to follow_up_kind: a queued kind without
// its text, and a text without its kind, each fails at the storage boundary
// instead of reaching finalization as a value nobody chose.
func TestFollowUpTieCheckTiesTextToKind(t *testing.T) {
	t.Parallel()
	store := integerEnumStore(t)
	_, err := store.ExecContext(t.Context(), "UPDATE control_response_payloads SET follow_up = '', follow_up_kind = 6")
	assert.ErrorContains(t, err, "CHECK constraint failed", "a follow-up kind with no text must fail")
	_, err = store.ExecContext(t.Context(), "UPDATE control_response_payloads SET follow_up = 'answer', follow_up_kind = 0")
	assert.ErrorContains(t, err, "CHECK constraint failed", "a follow-up text with no kind must fail")
	_, err = store.ExecContext(t.Context(), "UPDATE control_response_payloads SET follow_up = 'answer', follow_up_kind = 6")
	assert.NoError(t, err, "control feedback with its text must remain valid")
	_, err = store.ExecContext(t.Context(), "UPDATE control_response_payloads SET follow_up = '', follow_up_kind = 0")
	assert.NoError(t, err, "no follow-up input must remain valid")
}

func TestGoalUnknownStorageAcceptsFiveAndRejectsDerivedDormantSix(t *testing.T) {
	t.Parallel()
	sqlDB, err := workerdb.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, workerdb.Migrate(t.Context(), sqlDB))
	_, err = sqlDB.ExecContext(t.Context(), "INSERT INTO agents (id) VALUES (?)", "unknown-goal-owner")
	require.NoError(t, err)
	for _, status := range []any{int64(0), int64(1), int64(2), int64(3), int64(4), int64(5)} {
		_, writeErr := sqlDB.ExecContext(t.Context(), "UPDATE agents SET goal_status = ? WHERE id = ?", status, "unknown-goal-owner")
		assert.NoError(t, writeErr, "each stored goal ordinal through Unknown must remain valid")
		if writeErr == nil {
			var stored int64
			require.NoError(t, sqlDB.QueryRowContext(t.Context(), "SELECT goal_status FROM agents WHERE id = ?", "unknown-goal-owner").Scan(&stored))
			assert.Equal(t, status, stored)
		}
	}
	for _, status := range []any{int64(-1), int64(6), int64(7), int64(1 << 40), 1.5} {
		_, writeErr := sqlDB.ExecContext(t.Context(), "UPDATE agents SET goal_status = ? WHERE id = ?", status, "unknown-goal-owner")
		assert.Error(t, writeErr, "derived, fractional, negative, and undeclared statuses must not enter storage")
	}
}

// Each enum column's CHECK range, and the proto enum it must match.
//
// The upper limit is the last ordinal the column ACCEPTS, which is not always
// the last the enum declares -- two columns deliberately admit less than the
// whole enum, and each says why.
//
// A lower limit of 1 is the rule, because proto3 fixes UNSPECIFIED at 0 and an
// unset Go field holds 0, so a write that forgot the value fails instead of
// recording a state nobody chose. Six columns start at 0, and in each the zero
// IS a state the code chose: goal_status means "no goal", assembled_kind and
// completion both mean "this message is not an assembled one and reports no
// completion", pause_reason means "not paused", pause_owner means "nobody
// owns this pause", and follow_up_kind means "no follow-up input".
func TestEnumColumnChecksMatchTheirProtoRanges(t *testing.T) {
	t.Parallel()

	sql := statementsOfMigration(t)
	for _, c := range []struct {
		column       string
		check        string
		lastAccepted int32
		lastDeclared int32
		// admitted lists the ordinals the CHECK accepts when the set is not a
		// plain minimum-to-maximum range. Nil means every ordinal from zero to
		// lastAccepted.
		admitted []int32
		// columns is how many columns carry this exact CHECK text, where more
		// than one does. A Contains assertion cannot tell three columns that
		// share a CHECK from one column that kept it, so this counts them.
		columns int
		// narrower says the column admits LESS than the enum declares, and why.
		narrower string
	}{
		{
			column:       "messages.assembled_kind",
			check:        "CHECK (assembled_kind BETWEEN 1 AND 3)",
			lastAccepted: int32(leapmuxv1.AssembledMessageKind_ASSEMBLED_MESSAGE_KIND_PLAN),
			lastDeclared: lastDeclaredOrdinal(t, leapmuxv1.AssembledMessageKind_name),
		},
		{
			column:       "messages.completion",
			check:        "CHECK (completion BETWEEN 0 AND 4)",
			lastAccepted: int32(leapmuxv1.MessageCompletion_MESSAGE_COMPLETION_FINISHED),
			lastDeclared: lastDeclaredOrdinal(t, leapmuxv1.MessageCompletion_name),
		},
		{
			column:       "agent_input_queue_state.pause_reason",
			check:        "CHECK (pause_reason BETWEEN 1 AND 6)",
			lastAccepted: int32(leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_STORE_FAULT),
			lastDeclared: lastDeclaredOrdinal(t, leapmuxv1.AgentInputQueuePauseReason_name),
		},
		{
			column:       "agent_input_queue_items.kind",
			check:        "CHECK (kind BETWEEN 1 AND 8)",
			lastAccepted: int32(leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_AUTO_RETRY_SERVER_ERROR),
			lastDeclared: lastDeclaredOrdinal(t, leapmuxv1.AgentInputKind_name),
		},
		{
			column:       "control_response_payloads.follow_up_kind",
			check:        "CHECK (follow_up_kind IN (0, 6, 7))",
			lastAccepted: int32(leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_QUESTION_ANSWER),
			lastDeclared: lastDeclaredOrdinal(t, leapmuxv1.AgentInputKind_name),
			admitted:     []int32{0, 6, 7},
			narrower:     "an answer queues only control feedback or a question answer; 0 means no follow-up",
		},
		{
			column:       "control_response_payloads follow_up tie",
			check:        "CHECK ((follow_up_kind = 0) = (follow_up = ''))",
			lastAccepted: int32(leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_QUESTION_ANSWER),
			lastDeclared: lastDeclaredOrdinal(t, leapmuxv1.AgentInputKind_name),
			admitted:     []int32{0, 6, 7},
			narrower:     "an answer queues only control feedback or a question answer; 0 means no follow-up",
		},
		{
			column:       "messages.source",
			check:        "CHECK (source BETWEEN 1 AND 3)",
			lastAccepted: int32(leapmuxv1.MessageSource_MESSAGE_SOURCE_LEAPMUX),
			lastDeclared: lastDeclaredOrdinal(t, leapmuxv1.MessageSource_name),
		},
		{
			column:       "messages.mark_type",
			check:        "CHECK (mark_type BETWEEN 1 AND 2)",
			lastAccepted: int32(leapmuxv1.MarkType_MARK_TYPE_CONTROL_RESPONSE),
			lastDeclared: lastDeclaredOrdinal(t, leapmuxv1.MarkType_name),
		},
		{
			column:       "agent_input_queue_state.pause_owner",
			check:        "CHECK (pause_owner BETWEEN 1 AND 6)",
			lastAccepted: int32(leapmuxv1.AgentInputQueuePauseOwner_AGENT_INPUT_QUEUE_PAUSE_OWNER_RECOVERY),
			lastDeclared: lastDeclaredOrdinal(t, leapmuxv1.AgentInputQueuePauseOwner_name),
		},
		{
			column:       "agent_input_queue_items.state",
			check:        "CHECK (state BETWEEN 1 AND 5)",
			lastAccepted: int32(leapmuxv1.AgentInputState_AGENT_INPUT_STATE_AWAITING_ECHO),
			lastDeclared: lastDeclaredOrdinal(t, leapmuxv1.AgentInputState_name),
		},
		{
			column:       "agent_todos.status",
			check:        "CHECK (status BETWEEN 1 AND 5)",
			lastAccepted: int32(leapmuxv1.TodoStatus_TODO_STATUS_BLOCKED),
			lastDeclared: lastDeclaredOrdinal(t, leapmuxv1.TodoStatus_name),
		},
		{
			column:       "agent_background_tasks.kind",
			check:        "CHECK (kind BETWEEN 1 AND 3)",
			lastAccepted: int32(leapmuxv1.BackgroundTaskKind_BACKGROUND_TASK_KIND_WORKFLOW),
			lastDeclared: lastDeclaredOrdinal(t, leapmuxv1.BackgroundTaskKind_name),
		},
		{
			column:       "agent_background_tasks.status",
			check:        "CHECK (status BETWEEN 1 AND 8)",
			lastAccepted: int32(leapmuxv1.BackgroundTaskStatus_BACKGROUND_TASK_STATUS_ENDED_WITH_UNKNOWN_OUTCOME),
			lastDeclared: lastDeclaredOrdinal(t, leapmuxv1.BackgroundTaskStatus_name),
		},
		{
			column:       "agents.goal_status",
			check:        "CHECK (goal_status BETWEEN 0 AND 5)",
			lastAccepted: int32(leapmuxv1.AgentGoalStatus_AGENT_GOAL_STATUS_UNKNOWN),
			lastDeclared: lastDeclaredOrdinal(t, leapmuxv1.AgentGoalStatus_name),
			narrower: "DORMANT is derived from the running-agent map at read time and stored nowhere; " +
				"the CHECK is what makes that unrepresentable rather than merely unwritten",
		},
		{
			column: "control_deliveries.state", check: "CHECK (state BETWEEN 1 AND 4)",
			lastAccepted: int32(leapmuxv1.ControlDeliveryState_CONTROL_DELIVERY_STATE_COMPLETED),
			lastDeclared: lastDeclaredOrdinal(t, leapmuxv1.ControlDeliveryState_name),
		},
		{
			column: "control_deliveries.kind", check: "CHECK (kind BETWEEN 1 AND 2)",
			lastAccepted: int32(leapmuxv1.ControlDeliveryKind_CONTROL_DELIVERY_KIND_AUTO_REVIEW_APPROVAL),
			lastDeclared: lastDeclaredOrdinal(t, leapmuxv1.ControlDeliveryKind_name),
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
			// control_response_payloads each store the provider that produced
			// the row.
			column:       "agent_provider (agents, messages, control_response_payloads)",
			check:        "CHECK (agent_provider BETWEEN 1 AND 30)",
			columns:      3,
			lastAccepted: int32(leapmuxv1.AgentProvider_AGENT_PROVIDER_MUSE_CODE),
			lastDeclared: lastDeclaredOrdinal(t, leapmuxv1.AgentProvider_name),
		},
	} {
		t.Run(c.column, func(t *testing.T) {
			want := max(c.columns, 1)
			assert.Equalf(t, want, strings.Count(sql, c.check),
				"%d column(s) must carry %q; %s is one of them", want, c.check, c.column)
			if c.narrower != "" {
				admitted := c.admitted
				if admitted == nil {
					for ordinal := int32(0); ordinal <= c.lastAccepted; ordinal++ {
						admitted = append(admitted, ordinal)
					}
				}
				for _, ordinal := range admitted {
					assert.LessOrEqualf(t, ordinal, c.lastDeclared,
						"%s admits %d, which its enum does not declare", c.column, ordinal)
				}
				refusesSomeDeclaredOrdinal := false
				for ordinal := int32(0); ordinal <= c.lastDeclared; ordinal++ {
					if !slices.Contains(admitted, ordinal) {
						refusesSomeDeclaredOrdinal = true
						break
					}
				}
				assert.Truef(t, refusesSomeDeclaredOrdinal,
					"%s no longer admits less than its enum declares; delete the `narrower` note (%s)", c.column, c.narrower)
				return
			}
			assert.Equalf(t, c.lastDeclared, c.lastAccepted,
				"%s: the enum gained a value past the one its CHECK admits -- widen the CHECK, or say here why the column excludes it", c.column)
		})
	}
}

// AgentProvider is CONTIGUOUS, and the three CHECKs above state a plain range
// because of it. The project leaves no `reserved` and no hole, so a removed
// provider is deleted and every provider past it moves down one. That is a data
// change, not a rename -- these columns store the ordinal -- and this test is
// what makes a hole visible if one ever reappears, because the range assertion
// above reads the ceiling alone and would not notice a missing middle.
func TestAgentProviderOrdinalsAreContiguous(t *testing.T) {
	t.Parallel()

	for ordinal := int32(1); ordinal <= int32(leapmuxv1.AgentProvider_AGENT_PROVIDER_MUSE_CODE); ordinal++ {
		_, declared := leapmuxv1.AgentProvider_name[ordinal]
		assert.Truef(t, declared,
			"AgentProvider %d is a hole; the three agent_provider CHECKs state a plain range, so either close it by renumbering or narrow them", ordinal)
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
	// One past the last declared provider. The CHECK states a plain range now,
	// so its ceiling is the only thing standing between an unset enum and a row
	// nobody can read back.
	beyondLast := leapmuxv1.AgentProvider(int32(leapmuxv1.AgentProvider_AGENT_PROVIDER_MUSE_CODE) + 1)
	assert.ErrorContains(t, createAgent("beyond-last-ordinal", beyondLast),
		"CHECK constraint failed", "an ordinal past the last declared provider is refused")
}
