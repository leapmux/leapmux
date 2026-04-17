package config

import (
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/locallisten"
)

// TestLocalListen_DefaultPerPlatform verifies the default-path computation.
// The per-platform helpers live in default_listen_{unix,windows}.go — this
// test is platform-agnostic but its expectation branches on runtime.GOOS.
func TestLocalListen_DefaultPerPlatform(t *testing.T) {
	cfg := &Config{DataDir: "/data/leapmux/hub"}
	got, err := cfg.LocalListenURL()
	require.NoError(t, err)
	switch runtime.GOOS {
	case "windows":
		assert.True(t, strings.HasPrefix(got, "npipe:leapmux-hub"),
			"expected Windows default to start with npipe:leapmux-hub, got %q", got)
	default:
		assert.Equal(t, "unix:/data/leapmux/hub/hub.sock", got)
	}
}

func TestLocalListen_ExplicitUnix(t *testing.T) {
	cfg := &Config{LocalListen: "unix:/srv/custom.sock", DataDir: "/ignored"}
	got, err := cfg.LocalListenURL()
	require.NoError(t, err)
	assert.Equal(t, "unix:/srv/custom.sock", got)
}

func TestLocalListen_ExplicitNpipe(t *testing.T) {
	cfg := &Config{LocalListen: "npipe:custom-hub", DataDir: "/ignored"}
	got, err := cfg.LocalListenURL()
	require.NoError(t, err)
	assert.Equal(t, "npipe:custom-hub", got)
}

func TestLoad_LocalListenFlagRoundTrips(t *testing.T) {
	cases := []struct {
		name string
		arg  string
		want string
	}{
		{"unix", "unix:/srv/leapmux/hub.sock", "unix:/srv/leapmux/hub.sock"},
		{"npipe short", "npipe:custom-hub", "npipe:custom-hub"},
		{"npipe full NT", `npipe:\\.\pipe\custom-hub`, `npipe:\\.\pipe\custom-hub`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, _, err := Load([]string{"-local-listen", tc.arg})
			require.NoError(t, err)
			assert.Equal(t, tc.want, cfg.LocalListen)
			got, err := cfg.LocalListenURL()
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestLoad_LocalListenEmptyFallsBackToDefault(t *testing.T) {
	cfg, _, err := Load(nil)
	require.NoError(t, err)
	assert.Equal(t, "", cfg.LocalListen, "flag not set should not back-fill the field")
	// LocalListenURL() resolves to the per-platform default.
	got, err := cfg.LocalListenURL()
	require.NoError(t, err)
	assert.NotEmpty(t, got)
}

func TestLoad_LocalListenEnvVarRoundTrips(t *testing.T) {
	t.Setenv(locallisten.EnvLocalListen, "unix:/from/env.sock")
	cfg, _, err := Load(nil)
	require.NoError(t, err)
	assert.Equal(t, "unix:/from/env.sock", cfg.LocalListen)
}

// TestLoad_LocalListenMalformedRejected confirms that a bad --local-listen
// value is surfaced at Load time rather than deferred to the listener.
// Covers: unknown scheme, missing target after the scheme colon, plain
// strings with no scheme at all.
func TestLoad_LocalListenMalformedRejected(t *testing.T) {
	cases := []struct {
		name string
		arg  string
	}{
		{"unknown scheme", "gopher://example:70/bogus"},
		{"tcp scheme", "tcp://127.0.0.1:4327"},
		{"missing target unix", "unix:"},
		{"missing target npipe", "npipe:"},
		{"bare string no scheme", "just-a-name"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := Load([]string{"-local-listen", tc.arg})
			require.Error(t, err, "Load should reject %q", tc.arg)
			assert.Contains(t, err.Error(), "invalid local_listen",
				"error should surface the field name so users can find the flag")
		})
	}
}
