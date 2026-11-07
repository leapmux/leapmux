//go:build unix

package config

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func makeRegistrationRecordPublic(t *testing.T, path string) {
	t.Helper()
	require.NoError(t, os.Chmod(path, 0o644))
}

func TestPrivateRegistrationReadersRejectFIFOs(t *testing.T) {
	for _, reader := range registrationRecordReaders(t) {
		t.Run(reader.name, func(t *testing.T) {
			cfg := &Config{DataDir: t.TempDir()}
			require.NoError(t, unix.Mkfifo(reader.path(cfg), 0o600))
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestHelperPrivateRegistrationFIFO$")
			command.Env = append(os.Environ(), "LEAPMUX_TEST_REGISTRATION_FIFO="+reader.name, "LEAPMUX_TEST_REGISTRATION_FIFO_DIR="+cfg.DataDir)
			output, err := command.CombinedOutput()
			require.NoError(t, ctx.Err(), "the credential reader blocked on a FIFO without a writer")
			require.NoError(t, err, "%s", output)
			require.Contains(t, string(output), "not a regular private file")
		})
	}
}

func TestHelperPrivateRegistrationFIFO(t *testing.T) {
	kind := os.Getenv("LEAPMUX_TEST_REGISTRATION_FIFO")
	if kind == "" {
		return
	}
	cfg := &Config{DataDir: os.Getenv("LEAPMUX_TEST_REGISTRATION_FIFO_DIR")}
	var err error
	switch kind {
	case "state":
		_, err = cfg.LoadState()
	case "pending":
		_, err = cfg.LoadPendingRegistration()
	default:
		os.Exit(2)
	}
	if err == nil || !strings.Contains(err.Error(), "not a regular private file") {
		os.Exit(3)
	}
	if _, writeErr := fmt.Fprintln(os.Stdout, err); writeErr != nil {
		os.Exit(4)
	}
	os.Exit(0)
}
