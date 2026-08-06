package solo

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	hubconfig "github.com/leapmux/leapmux/internal/hub/config"
)

// soloLoadOptions builds the options Start would pass, with the config file
// pointed at a path that does not exist so a real one on the developer's machine
// cannot reach the assertions.
func soloLoadOptions(t *testing.T, devMode bool) hubconfig.LoadOptions {
	t.Helper()
	return hubconfig.LoadOptions{
		CLIFlags:          defaultCLIFlags(devMode),
		ExtraFlags:        defaultExtraFlags(),
		DefaultConfigDir:  t.TempDir(),
		DefaultConfigFile: filepath.Join(t.TempDir(), "absent.yaml"),
		SoloMode:          !devMode,
	}
}

// TestSoloExposesThePerUserCapsOnTheCommandLine pins the two knobs a solo user can
// actually reach. Solo is the mode where every socket belongs to one identity --
// an active tab holds two leases, and the desktop app, any CLI `remote` session
// and every worker's remoteipc watcher draw on the same allowance -- so a user who
// hits the cap is told to close a tab, advice they cannot act on if --help offers
// no way to raise it.
func TestSoloExposesThePerUserCapsOnTheCommandLine(t *testing.T) {
	t.Parallel()

	assert.Subset(t, defaultCLIFlags(false),
		[]string{"max-connections-per-user", "max-workers-per-user"})

	cfg, _, err := hubconfig.LoadWithOptions(
		[]string{"--max-connections-per-user=64", "--max-workers-per-user=8"},
		soloLoadOptions(t, false))
	require.NoError(t, err, "solo must accept the per-user caps as CLI flags")
	assert.Equal(t, int64(64), cfg.MaxConnectionsPerUser)
	assert.Equal(t, int64(8), cfg.MaxWorkersPerUser)
}

// TestSoloKeepsTheQueueBudgetsOutOfHelpButReachableInConfig covers the half of the
// contract that is easy to break by accident: CLIFlags decides only what earns a
// line in --help, because LoadWithOptions records every flag's default and koanf
// key BEFORE it consults that list. A budget absent from the CLI must still be
// settable, or auto-sizing becomes the only option a solo user has.
func TestSoloKeepsTheQueueBudgetsOutOfHelpButReachableInConfig(t *testing.T) {
	t.Parallel()

	assert.NotContains(t, defaultCLIFlags(false), "relay-queue-memory-budget",
		"the budgets auto-size off this machine; they do not belong in solo's --help")

	_, _, err := hubconfig.LoadWithOptions(
		[]string{"--relay-queue-memory-budget=268435456"}, soloLoadOptions(t, false))
	require.Error(t, err, "a flag outside the allowlist must not parse")

	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path,
		[]byte("relay_queue_memory_budget: 268435456\n"), 0o600))

	opts := soloLoadOptions(t, false)
	opts.DefaultConfigFile = path
	cfg, _, err := hubconfig.LoadWithOptions(nil, opts)
	require.NoError(t, err)
	assert.Equal(t, int64(268435456), cfg.RelayQueueMemoryBudget,
		"the config file must still reach a key the CLI does not expose")
}

// TestDevModeAddsPublicURL guards the one flag that is dev-only: dev serves a
// frontend from a different origin, solo does not.
func TestDevModeAddsPublicURL(t *testing.T) {
	t.Parallel()

	assert.Contains(t, defaultCLIFlags(true), "public-url")
	assert.NotContains(t, defaultCLIFlags(false), "public-url")
	assert.Subset(t, defaultCLIFlags(true),
		[]string{"max-connections-per-user", "max-workers-per-user"},
		"dev must not lose the caps when it gains public-url")
}

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

	_, hasMaxMessageSizeExtra := byName["max-message-size"]
	assert.False(t, hasMaxMessageSizeExtra,
		"max-message-size is a first-class hub flag forwarded into worker.RunConfig, not an Extra")
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
