package providerkit

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProcessBindDescendantsRequiresThePreparedOwner(t *testing.T) {
	process := NewProcessFrom(ProcessConfig{})
	require.ErrorContains(t, process.BindDescendants(t.Context()), "no prepared owner")
}
