//go:build windows

package locallisten

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultHubAddressForUser(t *testing.T) {
	t.Parallel()

	addr, err := defaultHubAddressForUser("S-1-5-21-1001")
	require.NoError(t, err)
	assert.Equal(t, "npipe:leapmux-hub-S-1-5-21-1001", addr)
	assert.True(t, IsLocal(addr))

	addr, err = defaultHubAddressForUser("")
	require.Error(t, err)
	assert.ErrorContains(t, err, "empty SID")
	assert.Empty(t, addr)
}
