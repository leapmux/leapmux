package config

import (
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/locallisten"
)

// tcpOf and localOf split a bind set the way the bind path does, failing the
// test rather than returning two more values to forget to check.
func tcpOf(t *testing.T, entries []string) []string {
	t.Helper()
	tcp, _ := SplitListen(entries)
	return tcp
}

func localOf(t *testing.T, entries []string) []string {
	t.Helper()
	_, local := SplitListen(entries)
	return local
}

// TestListenEntries_BindSetRules pins the rule: the list is the bind set, and
// exactly one entry is added beside it -- the platform's local IPC URL when
// the list names no local entry.
func TestListenEntries_BindSetRules(t *testing.T) {
	for _, tc := range []struct {
		name    string
		listen  []string
		wantTCP []string
		// wantLocalEmpty says the local side is compared against the
		// per-platform default rather than a fixed string.
		wantLocal []string
	}{
		{
			name:    "an empty list takes both platform defaults",
			wantTCP: []string{":4327"},
		},
		{
			// The `leapmux control` guarantee: a TCP-only list still binds the
			// local IPC socket, because that socket is the only credential-free
			// path and nothing has ever turned it off.
			name:    "one TCP address keeps the platform local socket",
			listen:  []string{":8080"},
			wantTCP: []string{":8080"},
		},
		{
			name:      "a local entry alone binds that socket and no TCP address",
			listen:    []string{"unix:/tmp/x.sock"},
			wantTCP:   nil,
			wantLocal: []string{"unix:/tmp/x.sock"},
		},
		{
			name:      "one TCP and one local bind both as given and add no default",
			listen:    []string{":8080", "unix:/tmp/x.sock"},
			wantTCP:   []string{":8080"},
			wantLocal: []string{"unix:/tmp/x.sock"},
		},
		{
			name:    "two TCP addresses replace the default TCP and add the default local",
			listen:  []string{":8080", ":9090"},
			wantTCP: []string{":8080", ":9090"},
		},
		{
			name:      "two local entries bind neither default",
			listen:    []string{"unix:/x", "unix:/y"},
			wantTCP:   nil,
			wantLocal: []string{"unix:/x", "unix:/y"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{DataDir: t.TempDir(), Listen: tc.listen}
			entries, err := cfg.ListenEntries()
			require.NoError(t, err)
			assert.Equal(t, tc.wantTCP, emptyToNil(tcpOf(t, entries)))
			wantLocal := tc.wantLocal
			if wantLocal == nil {
				wantLocal = defaultLocalForTest(t, cfg)
			}
			assert.Equal(t, wantLocal, localOf(t, entries))
		})
	}
}

// TestListenEntries_AlwaysBindsAtLeastOneLocalIPCAddress is the invariant
// `leapmux control` depends on: SoloGate admits a local IPC caller alone, so a
// hub with no local socket would silently take the credential-free access of
// the host away. Every input must leave the LOCAL side of the split non-empty.
// A future change that drops the socket must fail this test.
func TestListenEntries_AlwaysBindsAtLeastOneLocalIPCAddress(t *testing.T) {
	cases := []struct {
		name   string
		listen []string
	}{
		{"empty list", nil},
		{"one TCP address", []string{":8080"}},
		{"several TCP addresses", []string{":8080", ":9090", "127.0.0.1:7000"}},
		{"one TCP and one local", []string{":8080", "unix:/tmp/x.sock"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{DataDir: t.TempDir(), Listen: tc.listen}
			entries, err := cfg.ListenEntries()
			require.NoError(t, err)
			assert.NotEmpty(t, localOf(t, entries),
				"ListenEntries must always hold at least one local IPC address")
		})
	}

	t.Run("LEAPMUX_HUB_LISTEN with two TCP addresses comma-delimited", func(t *testing.T) {
		unsetAmbientEnvPrefix(t, "LEAPMUX_HUB_")
		t.Setenv("LEAPMUX_HUB_LISTEN", ":8080,:9090")
		cfg, _, err := Load(nil)
		require.NoError(t, err)
		entries, err := cfg.ListenEntries()
		require.NoError(t, err)
		assert.NotEmpty(t, localOf(t, entries),
			"ListenEntries must always hold at least one local IPC address")
	})
}

// TestListenEntries_LocalEntriesReplaceThePlatformLocalSocket: naming a local
// entry replaces the default rather than adding to it, and the entry survives
// verbatim (a comma inside a socket path is not a separator here).
func TestListenEntries_LocalEntriesReplaceThePlatformLocalSocket(t *testing.T) {
	for _, tc := range []struct{ name, arg, want string }{
		{"unix", "unix:/srv/leapmux/hub.sock", "unix:/srv/leapmux/hub.sock"},
		{"npipe short", "npipe:custom-hub", "npipe:custom-hub"},
		{"npipe full NT", `npipe:\\.\pipe\custom-hub`, `npipe:\\.\pipe\custom-hub`},
		{"a comma in the path survives the flag", "unix:/tmp/a,b.sock", "unix:/tmp/a,b.sock"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, _, err := Load([]string{"-listen", tc.arg})
			require.NoError(t, err)
			local, err := cfg.LocalListenURLs()
			require.NoError(t, err)
			assert.Equal(t, []string{tc.want}, local)
			assert.Empty(t, tcpOf(t, mustEntries(t, cfg)),
				"a local-only list binds no TCP address")
		})
	}
}

// TestListenEntries_DefaultPerPlatform: the default local IPC URL path is the
// per-platform one under the hub's data directory. The per-platform helpers
// live in default_listen_{unix,windows}.go -- this test is platform-agnostic
// but its expectation branches on runtime.GOOS.
func TestListenEntries_DefaultPerPlatform(t *testing.T) {
	cfg := &Config{DataDir: "/data/leapmux/hub"}
	local, err := cfg.LocalListenURLs()
	require.NoError(t, err)
	require.Len(t, local, 1)
	switch runtime.GOOS {
	case "windows":
		assert.True(t, strings.HasPrefix(local[0], "npipe:leapmux-hub"),
			"expected Windows default to start with npipe:leapmux-hub, got %q", local[0])
	default:
		assert.Equal(t, "unix:/data/leapmux/hub/hub.sock", local[0])
	}
}

// TestPrimaryTCPListen pins what a browser-facing URL names: the first TCP
// address of the bind set, the launcher's default for an empty list, and ""
// for a local-only set (there is no TCP address to name).
func TestPrimaryTCPListen(t *testing.T) {
	for _, tc := range []struct {
		name       string
		listen     []string
		defaultTCP string
		want       string
	}{
		{name: "an empty list takes the launcher's TCP default", defaultTCP: "127.0.0.1:4327", want: "127.0.0.1:4327"},
		{name: "an empty list and no launcher default falls back to :4327", want: ":4327"},
		{name: "the first TCP entry is the primary", listen: []string{":8080", ":9090"}, want: ":8080"},
		{name: "a local-only list has no primary TCP address", listen: []string{"unix:/x"}, want: ""},
		{name: "a local entry beside a TCP one keeps the TCP primary", listen: []string{"unix:/x", ":8080"}, want: ":8080"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{DataDir: t.TempDir(), Listen: tc.listen, defaultTCP: tc.defaultTCP}
			assert.Equal(t, tc.want, cfg.PrimaryTCPListen())
		})
	}
}

// TestLoad_ListenEnvVarIsCommaDelimited: LEAPMUX_HUB_LISTEN with commas yields
// the same list the repeated flag yields.
func TestLoad_ListenEnvVarIsCommaDelimited(t *testing.T) {
	unsetAmbientEnvPrefix(t, "LEAPMUX_HUB_")
	t.Setenv("LEAPMUX_HUB_LISTEN", ":8080, unix:/tmp/x.sock")

	fromEnv, _, err := Load(nil)
	require.NoError(t, err)
	fromFlag, _, err := Load([]string{"-listen", ":8080", "-listen", "unix:/tmp/x.sock"})
	require.NoError(t, err)

	assert.Equal(t, []string{":8080", "unix:/tmp/x.sock"}, fromEnv.Listen)
	assert.Equal(t, fromFlag.Listen, fromEnv.Listen,
		"the comma-delimited env form and the repeated flag must yield the same list")
}

// TestLoad_ListenCommaIsAFlagCharacterAndAnEnvSeparator: one value with a
// comma survives a repeated flag verbatim (the flag has no separator), while
// the env var splits the same text on commas -- and the piece after the comma
// is not an address, so the env form refuses it at startup with the entry's
// index. A socket path that holds a comma therefore goes in a repeated flag
// or the config file, never in LEAPMUX_HUB_LISTEN.
func TestLoad_ListenCommaIsAFlagCharacterAndAnEnvSeparator(t *testing.T) {
	unsetAmbientEnvPrefix(t, "LEAPMUX_HUB_")

	fromFlag, _, err := Load([]string{"-listen", "unix:/tmp/a,b.sock"})
	require.NoError(t, err)
	assert.Equal(t, []string{"unix:/tmp/a,b.sock"}, fromFlag.Listen,
		"the flag takes the value verbatim")

	t.Setenv("LEAPMUX_HUB_LISTEN", "unix:/tmp/a,b.sock")
	_, _, err = Load(nil)
	require.Error(t, err, "the env form splits on commas")
	assert.Contains(t, err.Error(), "invalid listen[1]",
		"the split piece after the comma reaches the validator as its own entry")
}

// TestLoad_ListenEntryThatIsNeitherKindFailsWithItsIndex: a malformed entry
// surfaces at Load time with its position in the list, not later inside Serve.
func TestLoad_ListenEntryThatIsNeitherKindFailsWithItsIndex(t *testing.T) {
	for _, tc := range []struct{ name, arg string }{
		{"unknown scheme", "gopher://example:70/bogus"},
		{"missing target after unix", "unix:"},
		{"missing target after npipe", "npipe:"},
		{"bare string with no scheme or port", "just-a-name"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := Load([]string{"-listen", ":8080", "-listen", tc.arg})
			require.Error(t, err, "Load should reject %q", tc.arg)
			assert.Contains(t, err.Error(), "invalid listen[1]",
				"the error must name the entry's index")
		})
	}
}

// TestSplitListen_ALocalLookingPrefixCannotClaimTCPAddresses: classification
// is the `unix:` / `npipe:` PREFIX test alone, so a TCP address whose host
// merely starts with those words stays on the TCP side and can never reach a
// local listener (whose requests carry the credential-free local-IPC mark).
// The reverse direction is stated too: every local entry carries the scheme
// prefix, and every TCP entry does not.
func TestSplitListen_ALocalLookingPrefixCannotClaimTCPAddresses(t *testing.T) {
	entries := []string{
		"unix.example.com:4327",
		"npipe.example.com:4327",
		"127.0.0.1:4327",
		"unix:/tmp/x.sock",
		"npipe:custom-hub",
	}
	tcp, local := SplitListen(entries)

	assert.Equal(t, []string{"unix.example.com:4327", "npipe.example.com:4327", "127.0.0.1:4327"}, tcp)
	assert.Equal(t, []string{"unix:/tmp/x.sock", "npipe:custom-hub"}, local)
	for _, e := range tcp {
		assert.False(t, locallisten.IsLocal(e), "%q must not classify as local IPC", e)
	}
	for _, e := range local {
		assert.True(t, locallisten.IsLocal(e), "%q must classify as local IPC", e)
	}
}

func mustEntries(t *testing.T, cfg *Config) []string {
	t.Helper()
	entries, err := cfg.ListenEntries()
	require.NoError(t, err)
	return entries
}

// defaultLocalForTest resolves the platform default local IPC URL the same
// way ListenEntries does, for a table row that asserts the default rather than
// a written entry.
func defaultLocalForTest(t *testing.T, cfg *Config) []string {
	t.Helper()
	local, err := defaultLocalListen(cfg.DataDir)
	require.NoError(t, err)
	return []string{local}
}

// emptyToNil mirrors the shape the rule tables want: "binds none" is written
// as nil, because the distinction from an empty slice carries nothing here.
func emptyToNil(v []string) []string {
	if len(v) == 0 {
		return nil
	}
	return v
}
