package cmd

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
)

func TestAuthUseChoosesOnlyAStoredHub(t *testing.T) {
	clearRemoteEnv(t)
	t.Setenv("LEAPMUX_HUB", "")
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", t.TempDir())
	const first = "https://first.example.test"
	const second = "https://second.example.test"
	save := func(hub, token string) {
		require.NoError(t, control.SaveCredentials(hub, control.CredentialFile{
			HubURL: hub, AccessToken: token, UserID: "user-1", ExpiresAt: time.Now().Add(time.Hour),
		}))
	}

	save(first, "first-synthetic-token")
	assert.Equal(t, first, runControl(t, "auth", "status").Data(t)["hub_url"])
	save(second, "second-synthetic-token")
	assert.Equal(t, second, runControl(t, "auth", "use", second).Data(t)["hub_url"])
	assert.Equal(t, second, runControl(t, "auth", "status").Data(t)["hub_url"])

	runControl(t, "auth", "use", "https://unknown.example.test").requireFailure(t, cli.CodeNotLoggedIn)
	assert.Equal(t, second, runControl(t, "auth", "status").Data(t)["hub_url"])
	assert.Equal(t, first, runControl(t, "auth", "status", "--hub", first).Data(t)["hub_url"])
	assert.Equal(t, second, runControl(t, "auth", "status").Data(t)["hub_url"])
}

func TestAuthUseOverridesContextHubAndLogoutClearsDefault(t *testing.T) {
	isolateCLIEnv(t)
	const hubA = "https://hub-a.example.test"
	const hubB = "https://hub-b.example.test"
	for _, hub := range []string{hubA, hubB} {
		require.NoError(t, control.SaveCredentials(hub, control.CredentialFile{
			HubURL: hub, AccessToken: "tok", UserID: "u-1", ExpiresAt: time.Now().Add(time.Hour),
		}))
	}

	// Set hub A and a default worker via context use.
	runControl(t, "context", "use", "--hub", hubA, "--worker", "build-box").ok(t)
	assert.Equal(t, hubA, runControl(t, "auth", "status").Data(t)["hub_url"])

	// auth use B must override context.json's hub while keeping other context defaults.
	runControl(t, "auth", "use", hubB).ok(t)
	assert.Equal(t, hubB, runControl(t, "auth", "status").Data(t)["hub_url"])
	cfg, err := control.LoadContext()
	require.NoError(t, err)
	assert.Equal(t, hubB, cfg.Hub)
	assert.Equal(t, "build-box", cfg.Worker)

	// Logging out of a non-default hub must leave context.json's default hub intact.
	runControl(t, "auth", "logout", "--hub", hubA).ok(t)
	cfg, err = control.LoadContext()
	require.NoError(t, err)
	assert.Equal(t, hubB, cfg.Hub)
	assert.Equal(t, "build-box", cfg.Worker)
	require.NoError(t, control.SaveCredentials(hubA, control.CredentialFile{
		HubURL: hubA, AccessToken: "tok", UserID: "u-1", ExpiresAt: time.Now().Add(time.Hour),
	}))

	// Logging out of the active default hub clears context.json's hub while
	// keeping other saved context fields, so the sole remaining hub becomes default.
	runControl(t, "auth", "logout", "--hub", hubB).ok(t)
	cfg, err = control.LoadContext()
	require.NoError(t, err)
	assert.Empty(t, cfg.Hub)
	assert.Equal(t, "build-box", cfg.Worker)
	assert.Equal(t, hubA, runControl(t, "auth", "status").Data(t)["hub_url"])
}
