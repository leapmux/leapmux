package phasetrace

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLogLazyBuildsFieldsOnlyWhenEnabled(t *testing.T) {
	for _, enabled := range []string{"0", "1"} {
		t.Run(enabled, func(t *testing.T) {
			t.Setenv("LEAPMUX_UNIT_PHASE_TRACE", enabled)
			tracer := New("LEAPMUX_UNIT_PHASE_TRACE", "unit", "unit trace")
			assert.Equal(t, enabled == "1", tracer.Enabled())
			calls := 0
			tracer.LogLazy(func() []any {
				calls++
				return []any{"field", "value"}
			})
			if enabled == "1" {
				assert.Equal(t, 1, calls)
			} else {
				assert.Zero(t, calls)
			}
		})
	}
}
