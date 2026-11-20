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
