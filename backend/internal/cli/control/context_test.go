package control_test

import (
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/cli/control"
)

func TestContextConfig_LoadSaveClear(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", dir)

	cfg, err := control.LoadContext()
	require.NoError(t, err)
	assert.True(t, cfg.IsEmpty())

	want := control.ContextConfig{
		Hub:       "https://hub.example.com",
		Worker:    "build-box",
		Workspace: "Release 1.4",
		Provider:  "claude",
	}
	require.NoError(t, control.SaveContext(want))

	got, err := control.LoadContext()
	require.NoError(t, err)
	assert.Equal(t, want, got)

	// context.json must never be treated as a credential file.
	credFiles, err := control.ListCredentialFiles()
	require.NoError(t, err)
	assert.Empty(t, credFiles)

	require.NoError(t, control.ClearContext())
	got, err = control.LoadContext()
	require.NoError(t, err)
	assert.True(t, got.IsEmpty())
}

func TestDefaultHub_UsesContextBeforeCredentialsAndDiscovery(t *testing.T) {
	root := t.TempDir()
	cfgDir := filepath.Join(root, "control")
	require.NoError(t, os.MkdirAll(cfgDir, 0o700))
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", cfgDir)
	t.Setenv("LEAPMUX_HUB", "")

	require.NoError(t, control.SaveContext(control.ContextConfig{Hub: "https://ctx.example.com"}))
	hub, err := control.DefaultHub()
	require.NoError(t, err)
	assert.Equal(t, "https://ctx.example.com", hub)

	// $LEAPMUX_HUB overrides context.json.
	t.Setenv("LEAPMUX_HUB", "https://env.example.com")
	hub, err = control.DefaultHub()
	require.NoError(t, err)
	assert.Equal(t, "https://env.example.com", hub)
}

func TestDefaultHub_ReturnsErrorOnCorruptContextFile(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", cfgDir)
	t.Setenv("LEAPMUX_HUB", "")

	// A valid stored credential exists, but context.json is corrupt. DefaultHub
	// must report the corrupt context file instead of silently targeting the
	// other Hub.
	require.NoError(t, control.SaveCredentials("https://other.example.com", control.CredentialFile{
		HubURL: "https://other.example.com", AccessToken: "tok", ExpiresAt: time.Now().Add(time.Hour),
	}))
	ctxPath, err := control.ContextPath()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(ctxPath, []byte("{corrupt json"), 0o600))

	_, err = control.DefaultHub()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse context")
}

func TestSetDefaultHub_UnifiesWithContext(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", cfgDir)
	t.Setenv("LEAPMUX_HUB", "")

	require.NoError(t, control.SaveContext(control.ContextConfig{
		Hub:    "https://a.example.com",
		Worker: "build-box",
	}))

	require.NoError(t, control.SetDefaultHub("https://b.example.com"))

	hub, err := control.DefaultHub()
	require.NoError(t, err)
	assert.Equal(t, "https://b.example.com", hub)

	cfg, err := control.LoadContext()
	require.NoError(t, err)
	assert.Equal(t, "https://b.example.com", cfg.Hub)
	assert.Equal(t, "build-box", cfg.Worker, "updating the default hub must keep other saved context fields")
}

func TestDiscoverLocalHub_FindsLiveSoloSocketAndIgnoresStale(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix domain socket discovery applies only to non-Windows hosts")
	}
	// Use a short temp dir so Unix socket path stays under 104 bytes on macOS.
	root, err := os.MkdirTemp("", "lmxdisc-*")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(root) })

	cfgDir := filepath.Join(root, "control")
	soloHubDir := filepath.Join(root, "solo", "hub")
	devHubDir := filepath.Join(root, "dev", "hub")
	require.NoError(t, os.MkdirAll(cfgDir, 0o700))
	require.NoError(t, os.MkdirAll(soloHubDir, 0o700))
	require.NoError(t, os.MkdirAll(devHubDir, 0o700))
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", cfgDir)
	t.Setenv("LEAPMUX_HUB", "")

	// Stale solo socket file (no listener) -> ignored.
	staleSock := filepath.Join(soloHubDir, "hub.sock")
	require.NoError(t, os.WriteFile(staleSock, nil, 0o600))
	assert.Equal(t, "", control.DiscoverLocalHub())

	// Live dev socket at dev/hub/hub.sock -> discovered when solo is stale.
	devSock := filepath.Join(devHubDir, "hub.sock")
	ln, err := net.Listen("unix", devSock)
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	assert.Equal(t, "unix:"+devSock, control.DiscoverLocalHub())

	// DefaultHub resolves to the discovered local IPC socket when no stored creds exist.
	hub, err := control.DefaultHub()
	require.NoError(t, err)
	assert.Equal(t, "unix:"+devSock, hub)

	// When 2+ stored credentials exist with no saved default Hub in context.json,
	// DefaultHub must NOT silently fall through to a local discovered Hub.
	require.NoError(t, control.SaveCredentials("https://one.example.com", control.CredentialFile{
		HubURL: "https://one.example.com", AccessToken: "tok1", ExpiresAt: time.Now().Add(time.Hour),
	}))
	require.NoError(t, control.SaveCredentials("https://two.example.com", control.CredentialFile{
		HubURL: "https://two.example.com", AccessToken: "tok2", ExpiresAt: time.Now().Add(time.Hour),
	}))
	_, err = control.DefaultHub()
	require.Error(t, err)
	var noHub *control.NoHubError
	require.ErrorAs(t, err, &noHub)
	assert.Equal(t, 2, noHub.Stored)
}

func TestDiscoverLocalHub_PrefersHighestPriorityUnderConcurrentProbes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix domain socket discovery applies only to non-Windows hosts")
	}
	root, err := os.MkdirTemp("", "lmxprio-*")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(root) })

	cfgDir := filepath.Join(root, "control")
	soloDir := filepath.Join(root, "solo", "hub")
	devDir := filepath.Join(root, "dev", "hub")
	hubDir := filepath.Join(root, "hub")
	for _, d := range []string{cfgDir, soloDir, devDir, hubDir} {
		require.NoError(t, os.MkdirAll(d, 0o700))
	}
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", cfgDir)
	t.Setenv("LEAPMUX_HUB", "")

	listen := func(path string) net.Listener {
		ln, listenErr := net.Listen("unix", path)
		require.NoError(t, listenErr)
		t.Cleanup(func() { _ = ln.Close() })
		go func() {
			for {
				c, acceptErr := ln.Accept()
				if acceptErr != nil {
					return
				}
				_ = c.Close()
			}
		}()
		return ln
	}

	devSock := filepath.Join(devDir, "hub.sock")
	hubSock := filepath.Join(hubDir, "hub.sock")
	listen(devSock)
	listen(hubSock)

	// Stale solo socket + live dev + live standalone -> dev wins.
	soloSock := filepath.Join(soloDir, "hub.sock")
	require.NoError(t, os.WriteFile(soloSock, nil, 0o600))
	assert.Equal(t, "unix:"+devSock, control.DiscoverLocalHub())

	// Replace stale solo with live solo -> solo wins over dev and standalone.
	require.NoError(t, os.Remove(soloSock))
	listen(soloSock)
	assert.Equal(t, "unix:"+soloSock, control.DiscoverLocalHub())
}

func TestContextConfig_TrimsWhitespaceDeletesWhenEmptyAndSweepsTempFiles(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", dir)

	// SaveContext trims surrounding whitespace on every field.
	require.NoError(t, control.SaveContext(control.ContextConfig{
		Hub:       "  https://hub.example.com  ",
		Worker:    "  w-1\n",
		Workspace: "\tws-1 ",
		Provider:  " claude ",
	}))
	got, err := control.LoadContext()
	require.NoError(t, err)
	assert.Equal(t, control.ContextConfig{
		Hub:       "https://hub.example.com",
		Worker:    "w-1",
		Workspace: "ws-1",
		Provider:  "claude",
	}, got)

	// LoadContext also trims whitespace from hand-edited JSON.
	ctxPath, err := control.ContextPath()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(ctxPath, []byte(`{"hub":"  https://edited.example  ","worker":" box "}`), 0o600))
	got, err = control.LoadContext()
	require.NoError(t, err)
	assert.Equal(t, control.ContextConfig{Hub: "https://edited.example", Worker: "box"}, got)

	// Saving all-whitespace fields removes context.json and sweeps interrupted temp files.
	tmpCopy := ctxPath + ".tmp1234"
	require.NoError(t, os.WriteFile(tmpCopy, []byte(`{"hub":"partial"}`), 0o600))
	require.NoError(t, control.SaveContext(control.ContextConfig{Hub: "   ", Worker: "\t"}))
	_, statErr := os.Stat(ctxPath)
	assert.True(t, os.IsNotExist(statErr), "saving an empty context must remove context.json")
	_, tmpErr := os.Stat(tmpCopy)
	assert.True(t, os.IsNotExist(tmpErr), "clearing context must sweep leftover temp files")

	// ClearContext is idempotent when already absent.
	assert.NoError(t, control.ClearContext())
}

func TestConfiguredDefaultHub_SkipsLocalDiscoveryAndHonorsPrecedence(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix domain socket discovery applies only to non-Windows hosts")
	}
	root, err := os.MkdirTemp("", "lmxcfg-*")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(root) })

	cfgDir := filepath.Join(root, "control")
	soloDir := filepath.Join(root, "solo", "hub")
	require.NoError(t, os.MkdirAll(cfgDir, 0o700))
	require.NoError(t, os.MkdirAll(soloDir, 0o700))
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", cfgDir)
	t.Setenv("LEAPMUX_HUB", "")

	soloSock := filepath.Join(soloDir, "hub.sock")
	ln, err := net.Listen("unix", soloSock)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}
			_ = c.Close()
		}
	}()

	// DefaultHub discovers the live solo socket, while ConfiguredDefaultHub must skip it.
	discovered, err := control.DefaultHub()
	require.NoError(t, err)
	assert.Equal(t, "unix:"+soloSock, discovered)

	_, err = control.ConfiguredDefaultHub()
	var noHub *control.NoHubError
	require.ErrorAs(t, err, &noHub)
	assert.Zero(t, noHub.Stored)

	// Sole stored credential satisfies ConfiguredDefaultHub.
	require.NoError(t, control.SaveCredentials("https://sole.example", control.CredentialFile{
		HubURL: "https://sole.example", AccessToken: "tok", ExpiresAt: time.Now().Add(time.Hour),
	}))
	hub, err := control.ConfiguredDefaultHub()
	require.NoError(t, err)
	assert.Equal(t, "https://sole.example", hub)

	// context.json beats the sole stored credential.
	require.NoError(t, control.SaveContext(control.ContextConfig{Hub: "https://ctx.example"}))
	hub, err = control.ConfiguredDefaultHub()
	require.NoError(t, err)
	assert.Equal(t, "https://ctx.example", hub)

	// LEAPMUX_HUB beats context.json.
	t.Setenv("LEAPMUX_HUB", "  https://env.example  ")
	hub, err = control.ConfiguredDefaultHub()
	require.NoError(t, err)
	assert.Equal(t, "https://env.example", hub)
}

func TestClearDefaultHubIf_MatchAndMismatch(t *testing.T) {
	t.Run("empty and whitespace are no-ops", func(t *testing.T) {
		cfgDir := t.TempDir()
		t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", cfgDir)
		require.NoError(t, control.SaveContext(control.ContextConfig{Hub: "https://keep.example", Worker: "w-1"}))

		require.NoError(t, control.ClearDefaultHubIf(""))
		require.NoError(t, control.ClearDefaultHubIf("   \t\n"))

		cfg, err := control.LoadContext()
		require.NoError(t, err)
		assert.Equal(t, "https://keep.example", cfg.Hub)
		assert.Equal(t, "w-1", cfg.Worker)
	})

	t.Run("mismatch preserves context", func(t *testing.T) {
		cfgDir := t.TempDir()
		t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", cfgDir)
		require.NoError(t, control.SaveContext(control.ContextConfig{Hub: "https://a.example", Worker: "w-1"}))

		require.NoError(t, control.ClearDefaultHubIf("https://b.example"))

		cfg, err := control.LoadContext()
		require.NoError(t, err)
		assert.Equal(t, "https://a.example", cfg.Hub)
		assert.Equal(t, "w-1", cfg.Worker)
	})

	t.Run("match clears hub while preserving other context fields", func(t *testing.T) {
		cfgDir := t.TempDir()
		t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", cfgDir)
		require.NoError(t, control.SaveContext(control.ContextConfig{
			Hub:       "https://a.example",
			Worker:    "w-1",
			Workspace: "ws-1",
			Provider:  "claude",
		}))

		require.NoError(t, control.ClearDefaultHubIf("  https://a.example  "))

		cfg, err := control.LoadContext()
		require.NoError(t, err)
		assert.Empty(t, cfg.Hub)
		assert.Equal(t, "w-1", cfg.Worker)
		assert.Equal(t, "ws-1", cfg.Workspace)
		assert.Equal(t, "claude", cfg.Provider)
	})

	t.Run("match deletes context.json when hub was the only field", func(t *testing.T) {
		cfgDir := t.TempDir()
		t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", cfgDir)
		require.NoError(t, control.SaveContext(control.ContextConfig{Hub: "https://only.example"}))

		require.NoError(t, control.ClearDefaultHubIf("https://only.example"))

		ctxPath, err := control.ContextPath()
		require.NoError(t, err)
		_, statErr := os.Stat(ctxPath)
		assert.True(t, os.IsNotExist(statErr), "clearing the sole context field must delete context.json")
	})

	t.Run("corrupt context.json returns parse error", func(t *testing.T) {
		cfgDir := t.TempDir()
		t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", cfgDir)
		ctxPath, err := control.ContextPath()
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(ctxPath, []byte("{bad json"), 0o600))

		err = control.ClearDefaultHubIf("https://a.example")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "parse context")
	})
}

func TestSetDefaultHub_EmptyClearsHub(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", cfgDir)
	require.NoError(t, control.SaveContext(control.ContextConfig{Hub: "https://a.example", Worker: "w-1"}))

	require.NoError(t, control.SetDefaultHub("  "))

	cfg, err := control.LoadContext()
	require.NoError(t, err)
	assert.Empty(t, cfg.Hub)
	assert.Equal(t, "w-1", cfg.Worker)

	// When no other context fields remain, SetDefaultHub("") removes context.json.
	require.NoError(t, control.SaveContext(control.ContextConfig{Hub: "https://only.example"}))
	require.NoError(t, control.SetDefaultHub(""))
	ctxPath, err := control.ContextPath()
	require.NoError(t, err)
	_, statErr := os.Stat(ctxPath)
	assert.True(t, os.IsNotExist(statErr))
}

func TestDiscoverLocalHub_AllDeadOrMissingAndStandaloneFallback(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix domain socket discovery applies only to non-Windows hosts")
	}
	root, err := os.MkdirTemp("", "lmxdead-*")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(root) })

	cfgDir := filepath.Join(root, "control")
	soloDir := filepath.Join(root, "solo", "hub")
	devDir := filepath.Join(root, "dev", "hub")
	hubDir := filepath.Join(root, "hub")
	for _, d := range []string{cfgDir, soloDir, devDir, hubDir} {
		require.NoError(t, os.MkdirAll(d, 0o700))
	}
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", cfgDir)

	// No socket files at all -> empty string.
	assert.Empty(t, control.DiscoverLocalHub())

	// All three candidates exist on disk as stale files -> concurrent probes all fail -> empty string.
	soloSock := filepath.Join(soloDir, "hub.sock")
	devSock := filepath.Join(devDir, "hub.sock")
	hubSock := filepath.Join(hubDir, "hub.sock")
	for _, sock := range []string{soloSock, devSock, hubSock} {
		require.NoError(t, os.WriteFile(sock, nil, 0o600))
	}
	assert.Empty(t, control.DiscoverLocalHub())

	// Live standalone hub wins when solo and dev are stale.
	require.NoError(t, os.Remove(hubSock))
	ln, err := net.Listen("unix", hubSock)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}
			_ = c.Close()
		}
	}()
	assert.Equal(t, "unix:"+hubSock, control.DiscoverLocalHub())

	// Single live candidate exercises the len(candidates) == 1 fast path.
	require.NoError(t, os.Remove(soloSock))
	require.NoError(t, os.Remove(devSock))
	assert.Equal(t, "unix:"+hubSock, control.DiscoverLocalHub())
}

func TestCanonicalHubURL(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty", in: "", want: ""},
		{name: "whitespace only", in: "  \t\n ", want: ""},
		{name: "lowercase scheme and host and strip root slash", in: "  HTTPS://HUB.EXAMPLE.COM/  ", want: "https://hub.example.com"},
		{name: "strip multiple trailing slashes on base path", in: "https://HUB.EXAMPLE.COM:8443/App///", want: "https://hub.example.com:8443/App"},
		{name: "percent-encoded path with trailing slashes preserves raw path", in: "https://HUB.EXAMPLE.COM/team%2Fdev///", want: "https://hub.example.com/team%2Fdev"},
		{name: "ipv6 literal host lowercased", in: "HTTP://[2001:DB8::1]:8080/", want: "http://[2001:db8::1]:8080"},
		{name: "unix socket preserves case and path", in: "  unix:/tmp/MyHub/Hub.sock  ", want: "unix:/tmp/MyHub/Hub.sock"},
		{name: "npipe preserves case and slashes", in: "  npipe://./pipe/LeapMux-Hub  ", want: "npipe://./pipe/LeapMux-Hub"},
		{name: "hostless string falls back to trimming trailing slashes", in: "hub.example///", want: "hub.example"},
		{name: "unparseable url falls back to trimming trailing slashes", in: "://bad///", want: "://bad"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, control.CanonicalHubURL(tc.in))
		})
	}
}

func TestSameHubURL(t *testing.T) {
	cases := []struct {
		name string
		a, b string
		want bool
	}{
		{name: "both empty", a: "", b: "", want: false},
		{name: "one whitespace", a: "   ", b: "https://hub.example", want: false},
		{name: "host case and trailing slash", a: "https://HUB.EXAMPLE/", b: "https://hub.example", want: true},
		{name: "scheme case and surrounding whitespace", a: "  HTTPS://HUB.EXAMPLE///  ", b: "https://hub.example", want: true},
		{name: "base path trailing slash", a: "https://hub.example/base/", b: "https://hub.example/base", want: true},
		{name: "different schemes stay distinct", a: "http://hub.example", b: "https://hub.example", want: false},
		{name: "explicit http port 80 stays distinct", a: "http://hub.example:80", b: "http://hub.example", want: false},
		{name: "explicit https port 443 stays distinct", a: "https://hub.example:443", b: "https://hub.example", want: false},
		{name: "different ports stay distinct", a: "https://hub.example:8443", b: "https://hub.example:9443", want: false},
		{name: "different paths stay distinct", a: "https://hub.example/a", b: "https://hub.example/b", want: false},
		{name: "path case stays distinct", a: "https://hub.example/App", b: "https://hub.example/app", want: false},
		{name: "identical unix socket", a: "  unix:/tmp/hub.sock ", b: "unix:/tmp/hub.sock", want: true},
		{name: "unix socket case stays distinct", a: "unix:/tmp/Hub.sock", b: "unix:/tmp/hub.sock", want: false},
		{name: "unix socket slash vs underscore stays distinct", a: "unix:/tmp/a/b.sock", b: "unix:/tmp/a_b.sock", want: false},
		{name: "unix socket vs remote stays distinct", a: "unix:/tmp/hub.sock", b: "https://hub.example", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, control.SameHubURL(tc.a, tc.b))
			assert.Equal(t, tc.want, control.SameHubURL(tc.b, tc.a), "SameHubURL must be symmetric")
		})
	}
}

func TestSetDefaultHub_CanonicalizesStoredURL(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", cfgDir)

	require.NoError(t, control.SetDefaultHub("  HTTPS://HUB.EXAMPLE.COM/  "))
	cfg, err := control.LoadContext()
	require.NoError(t, err)
	assert.Equal(t, "https://hub.example.com", cfg.Hub)

	require.NoError(t, control.SetDefaultHub("  unix:/tmp/MyHub/hub.sock  "))
	cfg, err = control.LoadContext()
	require.NoError(t, err)
	assert.Equal(t, "unix:/tmp/MyHub/hub.sock", cfg.Hub)
}

func TestClearDefaultHubIf_MatchesCanonicalDifferencesInContext(t *testing.T) {
	t.Run("non-canonical input clears canonical context", func(t *testing.T) {
		cfgDir := t.TempDir()
		t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", cfgDir)
		require.NoError(t, control.SaveContext(control.ContextConfig{
			Hub:    "https://hub.example",
			Worker: "w-1",
		}))

		require.NoError(t, control.ClearDefaultHubIf("HTTPS://HUB.EXAMPLE/"))

		cfg, err := control.LoadContext()
		require.NoError(t, err)
		assert.Empty(t, cfg.Hub)
		assert.Equal(t, "w-1", cfg.Worker)
	})

	t.Run("canonical input clears non-canonical context", func(t *testing.T) {
		cfgDir := t.TempDir()
		t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", cfgDir)
		require.NoError(t, control.SaveContext(control.ContextConfig{
			Hub:    "https://HUB.EXAMPLE/",
			Worker: "w-1",
		}))

		require.NoError(t, control.ClearDefaultHubIf("https://hub.example"))

		cfg, err := control.LoadContext()
		require.NoError(t, err)
		assert.Empty(t, cfg.Hub)
		assert.Equal(t, "w-1", cfg.Worker)
	})

	t.Run("different scheme preserves context", func(t *testing.T) {
		cfgDir := t.TempDir()
		t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", cfgDir)
		require.NoError(t, control.SaveContext(control.ContextConfig{Hub: "https://hub.example"}))

		require.NoError(t, control.ClearDefaultHubIf("http://hub.example"))

		cfg, err := control.LoadContext()
		require.NoError(t, err)
		assert.Equal(t, "https://hub.example", cfg.Hub)
	})
}
