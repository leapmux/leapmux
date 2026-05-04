package sqlutil

import (
	"testing"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/stretchr/testify/assert"
)

func TestAllTestEntitiesAreInSQLTruncateOrder(t *testing.T) {
	entities := []store.TestEntity{
		store.EntityOrgs,
		store.EntityUsers,
		store.EntitySessions,
		store.EntityWorkers,
		store.EntityWorkerRegistrationKeys,
		store.EntityWorkspaces,
	}

	tableSet := make(map[string]bool, len(SQLTruncateTableOrder))
	for _, tbl := range SQLTruncateTableOrder {
		tableSet[tbl] = true
	}

	for _, e := range entities {
		assert.Truef(t, tableSet[string(e)], "TestEntity %q is not in SQLTruncateTableOrder", e)
	}
}
