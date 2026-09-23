package kilo

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent/providers/opencode/opencodetest"
)

// Kilo reports its todo list in the OpenCode family's shape.
func TestKiloNativeTodoResults(t *testing.T) {
	t.Parallel()
	opencodetest.AssertReadsTheFamilyTodoResult(t, Registration().Plugin)
}
