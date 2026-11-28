package cmd

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/worker/controlenv"
)

func TestContextUse_InheritsHubFlagAndValidatesStoredCredentials(t *testing.T) {
	t.Run("context use does not shadow persistent --hub and keeps flag completions", func(t *testing.T) {
		root := NewCommand()
		useCmd, _, err := root.Find([]string{"context", "use"})
		require.NoError(t, err)
		assert.Nil(t, useCmd.LocalNonPersistentFlags().Lookup("hub"), "context use must inherit persistent --hub instead of shadowing it")
		assert.NotNil(t, useCmd.Flags().Lookup("hub"))
		for _, flag := range []string{"hub", "worker", "workspace", "provider"} {
			_, ok := useCmd.GetFlagCompletionFunc(flag)
			assert.True(t, ok, "context use must have completion for --%s", flag)
		}
	})

	t.Run("rejects unknown remote hub with login hint and accepts stored hub or local IPC socket", func(t *testing.T) {
		isolateCLIEnv(t)

		f := runControl(t, "context", "use", "--hub", "https://unknown.example.test").requireFailure(t, cli.CodeNotLoggedIn)
		require.NotEmpty(t, f.Hints)
		assert.Contains(t, f.Hints[0], "leapmux auth login --hub https://unknown.example.test")

		const hubURL = "https://saved.example.test"
		require.NoError(t, control.SaveCredentials(hubURL, control.CredentialFile{
			HubURL:      hubURL,
			AccessToken: "tok-1",
			UserID:      "u-1",
			Username:    "alice",
			ExpiresAt:   time.Now().Add(time.Hour),
		}))

		used := runControl(t, "context", "use", "--hub", hubURL+"/").Data(t)
		assert.Equal(t, hubURL, used["hub"])

		cfg, err := control.LoadContext()
		require.NoError(t, err)
		assert.Equal(t, hubURL, cfg.Hub)

		// Local IPC socket URLs do not require stored credentials.
		localSock := "unix:/tmp/leapmux-solo.sock"
		usedLocal := runControl(t, "context", "use", "--hub", localSock).Data(t)
		assert.Equal(t, localSock, usedLocal["hub"])
	})
}

func TestContextShow_ReportsAmbientTabAndEnvHubOverrides(t *testing.T) {
	t.Run("omits ambient fields when unset", func(t *testing.T) {
		isolateCLIEnv(t)

		data := runControl(t, "context", "show").Data(t)
		assert.NotContains(t, data, "active_tab_socket")
		assert.NotContains(t, data, "env_hub")

		text := runControlText(t, "context", "show")
		require.Equal(t, cli.ExitOK, text.Code, text.Stderr)
		assert.Equal(t, "no saved context defaults\n", text.Stdout)
	})

	t.Run("reports active_tab_socket and env_hub in JSON and notes in text mode", func(t *testing.T) {
		isolateCLIEnv(t)
		require.NoError(t, control.SaveContext(control.ContextConfig{
			Hub:      "https://saved.example.test",
			Worker:   "build-box",
			Provider: "claude",
		}))
		t.Setenv(controlenv.SocketVariable, "unix:/tmp/tab.sock")
		t.Setenv("LEAPMUX_HUB", "https://env.example.test")

		data := runControl(t, "context", "show").Data(t)
		assert.Equal(t, true, data["active_tab_socket"])
		assert.Equal(t, "https://env.example.test", data["env_hub"])
		assert.Equal(t, "https://saved.example.test", data["hub"])

		text := runControlText(t, "context", "show")
		require.Equal(t, cli.ExitOK, text.Code, text.Stderr)
		assert.Contains(t, text.Stdout, "LEAPMUX_CONTROL_SOCK")
		assert.Contains(t, text.Stdout, "LEAPMUX_HUB (https://env.example.test)")
	})
}
