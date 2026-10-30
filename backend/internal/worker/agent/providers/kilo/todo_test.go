package kilo

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent/providers/kilo/kilotest"
)

// Kilo reports its todo list in the OpenCode family's shape.
func TestKiloNativeTodoResults(t *testing.T) {
	t.Parallel()
	kilotest.AssertReadsTheFamilyTodoResult(t, Registration().Plugin)
}
