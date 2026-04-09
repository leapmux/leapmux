package sqlutil

import (
	"testing"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/stretchr/testify/assert"
)

func TestAllTestEntitiesAreValid(t *testing.T) {
	entities := []store.TestEntity{
		store.EntityOrgs,
		store.EntityUsers,
		store.EntitySessions,
		store.EntityWorkers,
		store.EntityWorkerRegistrations,
		store.EntityWorkspaces,
	}
	for _, e := range entities {
		assert.NoErrorf(t, ValidateEntity(e), "TestEntity %q is not in SQLTruncateTableOrder", e)
	}
}
