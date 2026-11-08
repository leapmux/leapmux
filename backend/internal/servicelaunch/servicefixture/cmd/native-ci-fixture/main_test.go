package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func invocationEnvironment(t *testing.T) map[string]string {
	t.Helper()
	return map[string]string{"GITHUB_ACTIONS": "true", "RUNNER_ENVIRONMENT": "github-hosted", "GITHUB_RUN_ID": "123", "GITHUB_RUN_ATTEMPT": "2", "GITHUB_REPOSITORY": "leapmux/leapmux", "RUNNER_TEMP": t.TempDir()}
}

func TestValidateInvocation(t *testing.T) {
	env := invocationEnvironment(t)
	read := func(key string) string { return env[key] }
	output := filepath.Join(env["RUNNER_TEMP"], "account.yaml")
	reference, err := validateInvocation(read, []string{"prepare", output})
	require.NoError(t, err)
	require.Equal(t, "LeapMux/CI/leapmux/leapmux/123/2", reference)
	reference, err = validateInvocation(read, []string{"cleanup"})
	require.NoError(t, err)
	require.Equal(t, "LeapMux/CI/leapmux/leapmux/123/2", reference)
	for _, args := range [][]string{nil, {"prepare"}, {"prepare", "relative"}, {"unknown"}, {"cleanup", "extra"}, {"prepare", filepath.Join(t.TempDir(), "outside.yaml")}} {
		_, err := validateInvocation(read, args)
		require.Error(t, err)
	}
	for _, entry := range []struct{ key, value string }{
		{"GITHUB_ACTIONS", "false"}, {"RUNNER_ENVIRONMENT", "self-hosted"}, {"GITHUB_RUN_ID", ""}, {"GITHUB_RUN_ID", "0"}, {"GITHUB_RUN_ID", "../123"},
		{"GITHUB_RUN_ATTEMPT", "-1"}, {"GITHUB_REPOSITORY", "invalid"}, {"RUNNER_TEMP", "relative"},
	} {
		original := env[entry.key]
		env[entry.key] = entry.value
		_, err := validateInvocation(read, []string{"prepare", output})
		require.Error(t, err, entry.key)
		env[entry.key] = original
	}
}

func TestValidateInvocationRejectsSymlinkEscape(t *testing.T) {
	env := invocationEnvironment(t)
	link := filepath.Join(env["RUNNER_TEMP"], "redirect")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Skipf("symlink creation is unavailable: %v", err)
	}
	_, err := validateInvocation(func(key string) string { return env[key] }, []string{"prepare", filepath.Join(link, "account.yaml")})
	require.ErrorContains(t, err, "inside the runner temporary directory")
}

func TestRunRefusesWorkstationBeforeNativeOperations(t *testing.T) {
	for _, args := range [][]string{{"cleanup"}, {"prepare", filepath.Join(t.TempDir(), "account.yaml")}} {
		require.ErrorContains(t, run(func(string) string { return "" }, args), "GitHub-hosted")
	}
}
