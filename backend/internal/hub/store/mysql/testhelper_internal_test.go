package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/leapmux/leapmux/internal/hub/store/sqlutil"
	"github.com/leapmux/leapmux/internal/hub/store/storetest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type cleanupSession struct {
	mu         sync.Mutex
	checks     bool
	cancel     context.CancelFunc
	deleteErr  error
	statements []string
}
type cleanupConnector struct{ session *cleanupSession }

func (c cleanupConnector) Connect(context.Context) (driver.Conn, error) {
	return &cleanupConnection{session: c.session}, nil
}
func (c cleanupConnector) Driver() driver.Driver { return cleanupDriver(c) }

type cleanupDriver struct{ session *cleanupSession }

func (d cleanupDriver) Open(string) (driver.Conn, error) {
	return &cleanupConnection{session: d.session}, nil
}

type cleanupConnection struct{ session *cleanupSession }

func (*cleanupConnection) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepare")
}
func (*cleanupConnection) Begin() (driver.Tx, error) {
	return nil, errors.New("unexpected transaction")
}
func (*cleanupConnection) Close() error { return nil }
func (c *cleanupConnection) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	c.session.mu.Lock()
	defer c.session.mu.Unlock()
	c.session.statements = append(c.session.statements, query)
	switch query {
	case "SET FOREIGN_KEY_CHECKS = 0":
		c.session.checks = false
	case "SET FOREIGN_KEY_CHECKS = 1":
		c.session.checks = true
	default:
		if strings.HasPrefix(query, "DELETE FROM ") {
			if c.session.deleteErr != nil {
				return nil, c.session.deleteErr
			}
			if c.session.cancel != nil {
				c.session.cancel()
				c.session.cancel = nil
			}
		}
	}
	return driver.RowsAffected(1), nil
}
func (c *cleanupConnection) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	c.session.mu.Lock()
	defer c.session.mu.Unlock()
	var checks int64
	if c.session.checks {
		checks = 1
	}
	return &cleanupRows{value: checks}, nil
}

type cleanupRows struct {
	value int64
	read  bool
}

func (*cleanupRows) Columns() []string { return []string{"foreign_key_checks"} }
func (*cleanupRows) Close() error      { return nil }
func (r *cleanupRows) Next(values []driver.Value) error {
	if r.read {
		return io.EOF
	}
	r.read = true
	values[0] = r.value
	return nil
}

func TestTruncateAll_PreservesSessionForeignKeyEnforcement(t *testing.T) {
	t.Parallel()
	failure := errors.New("delete unavailable")
	for _, mode := range []string{"success", "cancel after first delete", "delete error"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			session := &cleanupSession{checks: true}
			if mode == "cancel after first delete" {
				session.cancel = cancel
			}
			if mode == "delete error" {
				session.deleteErr = failure
			}
			db := sql.OpenDB(cleanupConnector{session: session})
			db.SetMaxOpenConns(1)
			db.SetMaxIdleConns(1)
			t.Cleanup(func() { _ = db.Close() })
			helper := &mysqlTestHelper{db: db}
			err := helper.TruncateAll(ctx)
			switch mode {
			case "success":
				require.NoError(t, err)
			case "cancel after first delete":
				require.ErrorIs(t, err, context.Canceled)
			case "delete error":
				require.ErrorIs(t, err, failure)
			}
			var checks int
			require.NoError(t, db.QueryRowContext(t.Context(), "SELECT @@FOREIGN_KEY_CHECKS").Scan(&checks))
			assert.Equal(t, 1, checks, "the same pooled session must still enforce foreign keys")
			if mode == "success" {
				want := make([]string, 0, len(sqlutil.SQLTruncateTableOrder)+2)
				for _, table := range sqlutil.SQLTruncateTableOrder {
					want = append(want, sqlutil.TruncateStatement(table))
				}
				want = append(want, "INSERT INTO revocation_event_sequence (id, last_seq) VALUES (1, 0)")
				want = append(want, "INSERT INTO account_lifecycle_lock (singleton_id) VALUES (1)")
				assert.Equal(t, want, session.statements)
			}
		})
	}
}

func TestTruncateAll_OrderCoversSchemaAndDeletesChildrenBeforeParents(t *testing.T) {
	t.Parallel()
	files, err := storetest.MigrationFiles(migrations)
	require.NoError(t, err)
	order := make(map[string]int, len(sqlutil.SQLTruncateTableOrder))
	for index, table := range sqlutil.SQLTruncateTableOrder {
		_, duplicate := order[table]
		require.False(t, duplicate)
		order[table] = index
	}
	create := regexp.MustCompile(`^CREATE\s+TABLE\s+([A-Z_][A-Z_0-9]*)\s*\(`)
	references := regexp.MustCompile(`\bREFERENCES\s+([A-Z_][A-Z_0-9]*)\s*\(`)
	referenceKeyword := regexp.MustCompile(`\bREFERENCES\b`)
	seen := map[string]bool{}
	edges := 0
	for _, file := range files {
		require.NoError(t, storetest.WalkCreateTableStatements(file.SQL, func(statement string) {
			name := create.FindStringSubmatch(statement)
			require.Len(t, name, 2, "unsupported CREATE TABLE syntax: %s", statement)
			child := strings.ToLower(name[1])
			_, exists := order[child]
			require.True(t, exists, "table %s is absent from cleanup order", child)
			require.False(t, seen[child], "duplicate table %s", child)
			seen[child] = true
			parents := references.FindAllStringSubmatch(statement, -1)
			require.Len(t, parents, len(referenceKeyword.FindAllStringIndex(statement, -1)), "unparsed REFERENCES in %s", child)
			for _, parent := range parents {
				table := strings.ToLower(parent[1])
				_, exists := order[table]
				require.True(t, exists, "unknown referenced table %s", table)
				assert.Less(t, order[child], order[table], "delete child %s before parent %s", child, table)
				edges++
			}
		}))
	}
	assert.Len(t, seen, len(order))
	assert.Positive(t, edges)
}
