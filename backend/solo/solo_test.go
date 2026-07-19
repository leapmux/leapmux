package solo

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	hubconfig "github.com/leapmux/leapmux/internal/hub/config"
)

func TestListenIsNonLoopback(t *testing.T) {
	t.Parallel()
	cases := []struct {
		listen string
		want   bool
	}{
		// Empty / missing host → all interfaces → warn.
		{"", true},
		{":4327", true},
		// Wildcard binds → warn.
		{"0.0.0.0:4327", true},
		{"[::]:4327", true},
		// Loopback → no warn.
		{"127.0.0.1:4327", false},
		{"127.0.0.5:4327", false}, // entire 127.0.0.0/8 is loopback
		{"[::1]:4327", false},
		{"localhost:4327", false},
		// Non-loopback IPs → warn.
		{"192.168.1.10:4327", true},
		{"100.64.1.2:4327", true}, // Tailscale CGNAT range
		{"10.0.0.5:4327", true},
		// Unparseable / hostname-only → warn (conservative).
		{"garbage", true},
		{"hostonly:4327", true},
	}
	for _, tc := range cases {
		got := listenIsNonLoopback(tc.listen)
		if got != tc.want {
			t.Errorf("listenIsNonLoopback(%q) = %v, want %v", tc.listen, got, tc.want)
		}
	}
}

func TestInstanceStopReturnsHubError(t *testing.T) {
	wantErr := errors.New("lease release failed")
	hubDone := make(chan struct{})
	close(hubDone)
	inst := &Instance{
		cancel:  func() {},
		hubErr:  wantErr,
		hubDone: hubDone,
	}

	require.ErrorIs(t, inst.Stop(), wantErr)
}

// TestDefaultExtraFlagsCarryWorkerScopedKnobs pins that solo's extra flags are the
// worker-scoped settings the embedded worker needs. max-incomplete-chunked is the
// load-bearing case: it is NOT a hub setting (the Hub's chunk-count cap is
// unreachable -- channelmgr admits one in-flight sequence per channel+direction),
// so solo is the only place it can be tuned for the embedded worker. If it ever
// drops out of this list again, `leapmux solo -max-incomplete-chunked=N` starts
// failing with "flag provided but not defined" while the docs still advertise it.
func TestDefaultExtraFlagsCarryWorkerScopedKnobs(t *testing.T) {
	byName := map[string]hubconfig.ExtraFlagDef{}
	for _, ef := range defaultExtraFlags() {
		byName[ef.Name] = ef
	}
	for _, name := range []string{"encryption-mode", "use-login-shell", "max-incomplete-chunked"} {
		require.Contains(t, byName, name, "solo must expose the worker-scoped %q flag", name)
	}

	chunked := byName["max-incomplete-chunked"]
	assert.Equal(t, "max_incomplete_chunked", chunked.KoanfKey,
		"the koanf key is what bringUpLocalWorker reads out of Extras")
	assert.Equal(t, "0", chunked.StrDefault,
		"0 must be the default so the worker applies channelwire.DefaultMaxIncompleteChunked")
	assert.Equal(t, "Timeout and limit options", chunked.Category,
		"it is a limit, not a server option -- the help output groups it accordingly")
}

// TestParseIntReadsTheStringTypedExtras covers the Extras -> RunConfig hop. Extras
// is string-typed (koanf reads every extra with k.String), so a malformed or absent
// value must degrade to the default rather than silently becoming 0-and-meaningful.
func TestParseIntReadsTheStringTypedExtras(t *testing.T) {
	tests := []struct {
		name       string
		in         string
		defaultVal int
		want       int
	}{
		{"a set value", "8", 0, 8},
		{"surrounding whitespace", "  8  ", 0, 8},
		{"the unset default", "0", 0, 0},
		{"absent key", "", 0, 0},
		{"absent key with a non-zero default", "", 4, 4},
		{"garbage falls back", "lots", 4, 4},
		{"a float is not an int", "8.5", 4, 4},
		{"negative parses (the worker clamps <=0 to its default)", "-1", 0, -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, parseInt(tt.in, tt.defaultVal))
		})
	}
}
