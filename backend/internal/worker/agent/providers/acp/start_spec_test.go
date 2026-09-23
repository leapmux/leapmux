package acp

import (
	"reflect"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStartSpecReadsTheRegistration keeps the launch and option metadata in
// one Registration. A second field can differ from the registry at runtime.
func TestStartSpecReadsTheRegistration(t *testing.T) {
	t.Parallel()

	typ := reflect.TypeFor[StartSpec[struct{}]]()
	field, ok := typ.FieldByName("Registration")
	require.True(t, ok, "ACP startup must receive the provider Registration")
	assert.Equal(t, reflect.TypeFor[agent.Registration](), field.Type)
	for _, duplicate := range []string{"Provider", "Locator", "OptionGroups"} {
		_, ok := typ.FieldByName(duplicate)
		assert.Falsef(t, ok, "StartSpec must read %s from Registration", duplicate)
	}
}
