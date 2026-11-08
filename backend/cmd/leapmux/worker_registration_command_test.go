package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/hub"
)

// The --hub value round-trips every URL shape that hubtransport accepts. The
// loader validates no scheme; the transport does, later, so the value must
// arrive verbatim.
func TestLoadWorkerRegisterCommandPreservesEveryHubScheme(t *testing.T) {
	for _, tc := range []struct {
		name string
		url  string
	}{
		{"http", "http://localhost:4327"},
		{"https", "https://hub.example:443"},
		{"unix", "unix:/tmp/leapmux/hub.sock"},
		{"npipe short", "npipe:leapmux-hub"},
		{"npipe full NT", `npipe:\\.\pipe\leapmux-hub`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, options, err := loadWorkerRegisterCommand([]string{"--hub", tc.url, "--registration-key", "key", "--data-dir", t.TempDir()})
			require.NoError(t, err)
			assert.Equal(t, tc.url, options.hubURL)
			assert.Equal(t, "key", options.registrationKey)
		})
	}
}

// The Hub URL and the key are flags only. A value from the config file or the
// environment could register with a Hub that the operator never typed, so
// neither source satisfies the command.
func TestLoadWorkerRegisterCommandReadsTheEndpointAndKeyFromFlagsOnly(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "worker.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte("hub: https://from-file.example\nregistration_key: from-file\n"), 0o600))
	t.Setenv("LEAPMUX_WORKER_HUB", "https://from-env.example")
	t.Setenv("LEAPMUX_WORKER_REGISTRATION_KEY", "from-env")

	_, _, err := loadWorkerRegisterCommand([]string{"--config", configPath, "--registration-key", "key"})
	require.ErrorContains(t, err, "--hub is required")

	_, _, err = loadWorkerRegisterCommand([]string{"--config", configPath, "--hub", "https://hub.example"})
	require.ErrorContains(t, err, "--registration-key is required")

	_, options, err := loadWorkerRegisterCommand([]string{"--config", configPath, "--hub", "https://hub.example", "--registration-key", "key"})
	require.NoError(t, err)
	assert.Equal(t, "https://hub.example", options.hubURL)
	assert.Equal(t, "key", options.registrationKey)
}

// Each credential command accepts only the data-directory selection and its
// own flags. A runtime flag would be accepted and ignored, because neither
// command starts the Worker.
func TestWorkerCredentialCommandsRefuseOtherFlags(t *testing.T) {
	for _, tc := range []struct {
		command string
		args    []string
		flag    string
	}{
		{"register", []string{"--hub", "https://hub.example", "--registration-key", "key", "--name", "box"}, "-name"},
		{"register", []string{"--hub", "https://hub.example", "--registration-key", "key", "--log-level", "debug"}, "-log-level"},
		{"deregister", []string{"--hub", "https://hub.example"}, "-hub"},
		{"deregister", []string{"--registration-key", "key"}, "-registration-key"},
		{"deregister", []string{"--agent-startup-timeout", "1m"}, "-agent-startup-timeout"},
	} {
		t.Run(tc.command+" "+tc.flag, func(t *testing.T) {
			args := append(tc.args, "--data-dir", t.TempDir())
			var err error
			if tc.command == "register" {
				_, _, err = loadWorkerRegisterCommand(args)
			} else {
				_, _, err = loadWorkerDeregisterCommand(args)
			}
			require.ErrorContains(t, err, "flag provided but not defined: "+tc.flag)
		})
	}
}

func TestWorkerCredentialCommandTimeouts(t *testing.T) {
	t.Run("register waits five minutes by default", func(t *testing.T) {
		_, options, err := loadWorkerRegisterCommand([]string{"--hub", "https://hub.example", "--registration-key", "key", "--data-dir", t.TempDir()})
		require.NoError(t, err)
		assert.Equal(t, hub.DefaultRegistrationDeadline, options.timeout)
		assert.Equal(t, 5*time.Minute, options.timeout)
	})
	t.Run("deregister waits thirty seconds by default", func(t *testing.T) {
		_, timeout, err := loadWorkerDeregisterCommand([]string{"--data-dir", t.TempDir()})
		require.NoError(t, err)
		assert.Equal(t, 30*time.Second, timeout)
	})
	t.Run("an explicit timeout replaces the default", func(t *testing.T) {
		_, timeout, err := loadWorkerDeregisterCommand([]string{"--data-dir", t.TempDir(), "--timeout", "90"})
		require.NoError(t, err)
		assert.Equal(t, 90*time.Second, timeout, "a bare number is seconds")
	})
	for _, value := range []string{"0", "-1s"} {
		t.Run("refuses "+value, func(t *testing.T) {
			_, _, err := loadWorkerDeregisterCommand([]string{"--data-dir", t.TempDir(), "--timeout", value})
			require.ErrorContains(t, err, "--timeout must be positive")
		})
	}
}

// Each credential command prints its own help: its own usage line, its own
// summary, and only the flags it accepts.
func TestWorkerCredentialCommandHelp(t *testing.T) {
	for _, tc := range []struct {
		command string
		summary string
		present []string
		absent  []string
	}{
		{
			command: "register",
			summary: workerRegisterSummary,
			present: []string{"  -hub string", "  -registration-key string", "  -timeout ", "  -data-dir string", "  -config string"},
			absent:  []string{"  -name ", "  -log-level ", "  -agent-startup-timeout ", "  -db-max-conns ", "127.0.0.1:4327", "first run"},
		},
		{
			command: "deregister",
			summary: workerDeregisterSummary,
			present: []string{"  -timeout ", "  -data-dir string", "  -config string"},
			absent:  []string{"  -hub ", "  -registration-key ", "  -name ", "  -log-level "},
		},
	} {
		t.Run(tc.command, func(t *testing.T) {
			result := invokeLifecycleCLI(t, t.TempDir(), "worker", tc.command, "--help")
			require.Equal(t, 0, result.exitCode, result.stderr)
			assert.True(t, strings.HasPrefix(result.stdout, tc.summary+".\n\nUsage: leapmux worker "+tc.command+" [flags]\n"), result.stdout)
			for _, line := range tc.present {
				assert.Contains(t, result.stdout, line)
			}
			for _, text := range tc.absent {
				assert.NotContains(t, result.stdout, text)
			}
		})
	}
}
