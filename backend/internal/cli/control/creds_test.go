package control

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDeleteCredentials_SweepsWhatAnInterruptedWriteLeft is what makes
// `auth logout` true.
//
// A process killed between the write and the rename leaves a temporary file
// holding a complete access and refresh pair at mode 0600. Nothing else ever
// shows it: ListCredentialFiles reads only the ".json" suffix, so `auth
// list` cannot report one, and the logout said "it is gone from this
// machine" while the secret stayed on the disk for the rest of its window.
func TestDeleteCredentials_SweepsWhatAnInterruptedWriteLeft(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", dir)
	hubURL := "https://hub.example"
	require.NoError(t, SaveCredentials(hubURL, CredentialFile{
		HubURL:       hubURL,
		AccessToken:  "lmx_a_access_0",
		RefreshToken: "lmx_a_refresh_0",
		ExpiresAt:    time.Now().Add(time.Hour),
	}))

	path, err := CredentialsPath(hubURL)
	require.NoError(t, err)
	// What a kill between the write and the rename leaves behind.
	require.NoError(t, os.WriteFile(path+".tmp2145", []byte(`{"access_token":"lmx_a_access_0"}`), 0o600))

	require.NoError(t, DeleteCredentials(hubURL))

	entries, err := os.ReadDir(filepath.Dir(path))
	require.NoError(t, err)
	assert.Empty(t, entries, "no copy of the credential may survive a logout")

	// Idempotent: a second logout is not a failure.
	assert.NoError(t, DeleteCredentials(hubURL))
}

// TestDeleteCredentials_LeavesAnotherHubsFilesAlone: the sweep is keyed on
// ONE destination, so signing out of one hub cannot touch another.
func TestDeleteCredentials_LeavesAnotherHubsFilesAlone(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", dir)
	for _, hubURL := range []string{"https://one.example", "https://two.example"} {
		require.NoError(t, SaveCredentials(hubURL, CredentialFile{
			HubURL: hubURL, AccessToken: "lmx_a_access_0", ExpiresAt: time.Now().Add(time.Hour),
		}))
	}
	other, err := CredentialsPath("https://two.example")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(other+".tmp99", []byte("{}"), 0o600))

	require.NoError(t, DeleteCredentials("https://one.example"))

	var names []string
	entries, err := os.ReadDir(filepath.Dir(other))
	require.NoError(t, err)
	for _, e := range entries {
		names = append(names, e.Name())
	}
	assert.ElementsMatch(t, []string{filepath.Base(other), filepath.Base(other) + ".tmp99"}, names)
}

func TestCredentialsPath_LivesUnderCredentialsSubdirectory(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", dir)
	hubURL := "https://sub.example"
	require.NoError(t, SaveCredentials(hubURL, CredentialFile{
		HubURL: hubURL, AccessToken: "lmx_a_sub", ExpiresAt: time.Now().Add(time.Hour),
	}))
	// Unrelated JSON files in ConfigDir root must never appear in ListCredentialFiles.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "other.json"), []byte(`{"hub_url":"https://rogue.example"}`), 0o600))

	path, err := CredentialsPath(hubURL)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "credentials"), filepath.Dir(path))

	files, err := ListCredentialFiles()
	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, hubURL, files[0].HubURL)
}

// TestListCredentialFiles_IgnoresAnInterruptedWrite pins why the sweep has
// to exist: the listing cannot see a temporary file, so no operator can find
// one to remove by hand.
func TestListCredentialFiles_IgnoresAnInterruptedWrite(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", dir)
	hubURL := "https://hub.example"
	require.NoError(t, SaveCredentials(hubURL, CredentialFile{
		HubURL: hubURL, AccessToken: "lmx_a_access_0", ExpiresAt: time.Now().Add(time.Hour),
	}))
	path, err := CredentialsPath(hubURL)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path+".tmp2145",
		[]byte(`{"hub_url":"https://hub.example","access_token":"lmx_a_leftover"}`), 0o600))

	files, err := ListCredentialFiles()
	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, "lmx_a_access_0", files[0].AccessToken)
}

// TestSaveCredentials_WritesUTC pins the file's one-zone rule: the writers
// mint deadlines in the writer's local zone, and the stored file must carry
// UTC so a reader can compare it with the hub's own Z-ending timestamps
// without parsing zones first. The fixed +02:00 zone makes the assertion
// fail even on a UTC machine.
func TestSaveCredentials_WritesUTC(t *testing.T) {
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", t.TempDir())
	hubURL := "https://zone.example"
	plusTwo := time.FixedZone("plus-two", 2*60*60)
	creds := CredentialFile{
		HubURL:           hubURL,
		AccessToken:      "lmx_a_at_zone",
		RefreshToken:     "lmx_a_rt_zone",
		ExpiresAt:        time.Date(2026, 1, 2, 3, 4, 5, 0, plusTwo),
		RefreshExpiresAt: time.Date(2026, 4, 2, 3, 4, 5, 0, plusTwo),
	}
	require.NoError(t, SaveCredentials(hubURL, creds))

	path, err := CredentialsPath(hubURL)
	require.NoError(t, err)
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"expires_at": "2026-01-02T01:04:05Z"`,
		"the file carries UTC, not the writer's offset")
	assert.Contains(t, string(raw), `"refresh_expires_at": "2026-04-02T01:04:05Z"`)

	loaded, err := LoadCredentials(hubURL)
	require.NoError(t, err)
	assert.True(t, loaded.ExpiresAt.Equal(creds.ExpiresAt),
		"normalizing the zone keeps the instant")
	assert.True(t, loaded.RefreshExpiresAt.Equal(creds.RefreshExpiresAt))
}

func TestListCredentialFiles_MissingDirCorruptJSONSubdirAndEmptyHubURL(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", dir)

	// Missing credentials/ directory returns nil slice and no error.
	files, err := ListCredentialFiles()
	require.NoError(t, err)
	assert.Nil(t, files)

	// LoadCredentials on missing file returns ErrNotLoggedIn.
	_, err = LoadCredentials("https://missing.example")
	require.ErrorIs(t, err, ErrNotLoggedIn)

	// Save one valid credential and place invalid/non-credential entries beside it.
	hubURL := "https://valid.example"
	require.NoError(t, SaveCredentials(hubURL, CredentialFile{
		HubURL:      hubURL,
		AccessToken: "lmx_valid",
		ExpiresAt:   time.Now().Add(time.Hour),
	}))
	credsDir := filepath.Join(dir, credentialsDirName)
	require.NoError(t, os.Mkdir(filepath.Join(credsDir, "nested.json"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(credsDir, "notes.txt"), []byte("ignore"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(credsDir, "corrupt.json"), []byte("{not json"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(credsDir, "empty-hub.json"), []byte(`{"access_token":"tok"}`), 0o600))

	files, err = ListCredentialFiles()
	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, hubURL, files[0].HubURL)
	assert.Equal(t, "lmx_valid", files[0].AccessToken)

	// LoadCredentials on a corrupt credential file reports the path under credentials/.
	path, err := CredentialsPath(hubURL)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, []byte("{broken"), 0o600))
	_, err = LoadCredentials(hubURL)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse credentials")
	assert.Contains(t, err.Error(), path)
}

func TestIsLocalIPCLive_InvalidAddrAndCanceledContext(t *testing.T) {
	assert.False(t, isLocalIPCLive(t.Context(), "https://not-a-local-socket.example"))

	if runtime.GOOS == "windows" {
		return
	}
	dir, err := os.MkdirTemp("", "lmxlive-*")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	sockPath := filepath.Join(dir, "hub.sock")
	ln, err := net.Listen("unix", sockPath)
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

	assert.True(t, isLocalIPCLive(t.Context(), "unix:"+sockPath))

	canceledCtx, cancel := context.WithCancel(t.Context())
	cancel()
	assert.False(t, isLocalIPCLive(canceledCtx, "unix:"+sockPath),
		"an already-canceled context must fail the liveness probe")
}

func TestResolveDefaultHub_ReturnsSourceProvenance(t *testing.T) {
	t.Run("env takes precedence", func(t *testing.T) {
		t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", t.TempDir())
		t.Setenv("LEAPMUX_HUB", "https://env.example")
		hub, source, creds, err := resolveDefaultHub(true)
		require.NoError(t, err)
		assert.Equal(t, "https://env.example", hub)
		assert.Equal(t, HubSourceEnv, source)
		assert.Nil(t, creds)
	})

	t.Run("context hub reports HubSourceContext", func(t *testing.T) {
		t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", t.TempDir())
		t.Setenv("LEAPMUX_HUB", "")
		require.NoError(t, SaveContext(ContextConfig{Hub: "https://ctx.example"}))
		hub, source, creds, err := resolveDefaultHub(true)
		require.NoError(t, err)
		assert.Equal(t, "https://ctx.example", hub)
		assert.Equal(t, HubSourceContext, source)
		assert.Nil(t, creds)
	})

	t.Run("sole credential file reports HubSourceSingleCredential", func(t *testing.T) {
		t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", t.TempDir())
		t.Setenv("LEAPMUX_HUB", "")
		require.NoError(t, SaveCredentials("https://sole.example", CredentialFile{
			HubURL:      "https://sole.example",
			AccessToken: "lmx_sole",
			ExpiresAt:   time.Now().Add(time.Hour),
		}))
		hub, source, creds, err := resolveDefaultHub(true)
		require.NoError(t, err)
		assert.Equal(t, "https://sole.example", hub)
		assert.Equal(t, HubSourceSingleCredential, source)
		require.NotNil(t, creds)
		assert.Equal(t, "lmx_sole", creds.AccessToken)
	})

	t.Run("discovered local socket reports HubSourceDiscovered", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("Unix socket discovery applies only to non-Windows hosts")
		}
		root, err := os.MkdirTemp("", "lmxsrc-*")
		require.NoError(t, err)
		t.Cleanup(func() { _ = os.RemoveAll(root) })

		cfgDir := filepath.Join(root, "control")
		soloDir := filepath.Join(root, "solo", "hub")
		require.NoError(t, os.MkdirAll(cfgDir, 0o700))
		require.NoError(t, os.MkdirAll(soloDir, 0o700))
		t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", cfgDir)
		t.Setenv("LEAPMUX_HUB", "")

		sockPath := filepath.Join(soloDir, "hub.sock")
		ln, err := net.Listen("unix", sockPath)
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

		hub, source, err := DefaultHubWithSource()
		require.NoError(t, err)
		assert.Equal(t, "unix:"+sockPath, hub)
		assert.Equal(t, HubSourceDiscovered, source)

		// ConfiguredDefaultHub disables local discovery.
		_, _, _, cfgErr := resolveDefaultHub(false)
		require.Error(t, cfgErr)
		var noHub *NoHubError
		require.ErrorAs(t, cfgErr, &noHub)
	})

	t.Run("no hub returns HubSourceUnspecified", func(t *testing.T) {
		t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", t.TempDir())
		t.Setenv("LEAPMUX_HUB", "")
		_, source, _, err := resolveDefaultHub(false)
		require.Error(t, err)
		assert.Equal(t, HubSourceUnspecified, source)
	})
}
