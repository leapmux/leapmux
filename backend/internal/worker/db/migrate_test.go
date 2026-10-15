package db_test

import (
	"os"
	"testing"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorkerMigrationRemovesEveryApplicationTable(t *testing.T) {
	t.Parallel()
	connection := integerEnumStore(t)
	provider, err := goose.NewProvider(goose.DialectSQLite3, connection, os.DirFS("migrations"))
	require.NoError(t, err)
	_, err = provider.Down(t.Context())
	require.NoError(t, err)
	rows, err := connection.QueryContext(t.Context(), `SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' AND name <> 'goose_db_version' ORDER BY name`)
	require.NoError(t, err)
	var tables []string
	for rows.Next() {
		var name string
		require.NoError(t, rows.Scan(&name))
		tables = append(tables, name)
	}
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())
	assert.Empty(t, tables)
	_, err = provider.Up(t.Context())
	require.NoError(t, err, "the initial schema must create every table after Down")
}
