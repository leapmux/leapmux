package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLoad_HubFlagAcceptsAllTransportSchemes verifies the --hub CLI flag
// round-trips every URL shape supported by backend/internal/worker/hub.New.
// Load itself performs no scheme validation — dispatch happens later in the
// transport layer — so these tests enforce that the flag value is preserved
// verbatim for every expected scheme.
func TestLoad_HubFlagAcceptsAllTransportSchemes(t *testing.T) {
	cases := []struct {
		name string
		arg  string
	}{
		{"http", "http://localhost:4327"},
		{"https", "https://hub.example:443"},
		{"unix", "unix:/tmp/leapmux/hub.sock"},
		{"npipe short", "npipe:leapmux-hub"},
		{"npipe full NT", `npipe:\\.\pipe\leapmux-hub`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, _, err := Load([]string{"-hub", tc.arg})
			require.NoError(t, err)
			assert.Equal(t, tc.arg, cfg.HubURL)
		})
	}
}

func TestLoad_HubFlagDefaultRemainsHTTP(t *testing.T) {
	cfg, _, err := Load(nil)
	require.NoError(t, err)
	assert.Equal(t, "http://localhost:4327", cfg.HubURL,
		"default HubURL should be preserved for backwards compatibility")
}
