package control

import (
	"os"
	"path/filepath"
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

	entries, err := os.ReadDir(dir)
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
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, e := range entries {
		names = append(names, e.Name())
	}
	assert.ElementsMatch(t, []string{filepath.Base(other), filepath.Base(other) + ".tmp99"}, names)
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
