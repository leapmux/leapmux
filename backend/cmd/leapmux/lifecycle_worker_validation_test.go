package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/lifecycle"
)

func workerValidationArguments(t *testing.T, source, setting, value string) ([]string, string) {
	t.Helper()
	for _, key := range []string{"LEAPMUX_WORKER_REGISTRATION_KEY", "LEAPMUX_WORKER_MAX_MESSAGE_SIZE"} {
		t.Setenv(key, "")
		require.NoError(t, os.Unsetenv(key))
	}
	cfg := writeRegisteredStateAgainst(t, "http://127.0.0.1:1")
	require.NoError(t, os.Mkdir(cfg.DBPath(), 0o700))
	configuration := filepath.Join(t.TempDir(), "worker.yaml")
	contents := "{}\n"
	if source == "configuration" || source == "managed" {
		contents = fmt.Sprintf("%s: %s\n", strings.ReplaceAll(setting, "-", "_"), value)
	}
	require.NoError(t, os.WriteFile(configuration, []byte(contents), 0o600))
	args := []string{"--config", configuration, "--data-dir", cfg.DataDir}
	switch source {
	case "flags":
		args = append(args, "--"+setting, value)
	case "environment":
		t.Setenv("LEAPMUX_WORKER_"+strings.ToUpper(strings.ReplaceAll(setting, "-", "_")), value)
	case "managed":
		spec, err := desiredSpec("worker", lifecycle.StartupBoot, cfg.DataDir, []string{"--config", configuration})
		require.NoError(t, err)
		path := saveManagedTestSpec(t, spec)
		args = []string{"--data-dir", spec.DataDirectory, "--service-spec", path}
	}
	return args, mustCanonicalDir(t, cfg.DataDir)
}

func TestWorkerRuntimeRejectsInvalidMessageSizeBeforePublication(t *testing.T) {
	for _, value := range []int{1, contracts.MaxConfigurableMessageSize + 1} {
		for _, source := range []string{"flags", "environment", "configuration", "managed"} {
			t.Run(source+"/"+strconv.Itoa(value), func(t *testing.T) {
				args, root := workerValidationArguments(t, source, "max-message-size", strconv.Itoa(value))
				err := runModeRunWithInvocation("worker", runInvocation{Context: testutil.DeadlineContext(t)})(cmdCtx{}, args)
				assert.ErrorContains(t, err, "max_message_size")
				assert.NoFileExists(t, lifecycle.PathsOf(root).RuntimeRecord, "invalid runtime configuration must precede publication")
				assert.NoFileExists(t, lifecycle.PathsOf(root).ControlToken)
			})
		}
	}
}

func TestWorkerRuntimeRefusesConfiguredRegistrationKeys(t *testing.T) {
	for _, source := range []string{"flags", "environment", "configuration", "managed"} {
		t.Run(source, func(t *testing.T) {
			args, root := workerValidationArguments(t, source, "registration-key", "registration-only-credential")
			err := runModeRunWithInvocation("worker", runInvocation{Context: testutil.DeadlineContext(t)})(cmdCtx{}, args)
			assert.ErrorContains(t, err, "leapmux worker register")
			assert.NoFileExists(t, lifecycle.PathsOf(root).RuntimeRecord)
			assert.NoFileExists(t, lifecycle.PathsOf(root).ControlToken)
		})
	}
}
