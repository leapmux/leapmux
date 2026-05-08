package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"testing"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type cliCall struct {
	command  string
	args     []string
	soloMode bool
}

func testCLIRunners(calls *[]cliCall) cliRunners {
	record := func(command string, args []string, soloMode bool) {
		*calls = append(*calls, cliCall{
			command:  command,
			args:     append([]string(nil), args...),
			soloMode: soloMode,
		})
	}
	return cliRunners{
		runHub: func(args []string) error {
			record("hub", args, false)
			return nil
		},
		runWorker: func(args []string) error {
			record("worker", args, false)
			return nil
		},
		runSolo: func(args []string, soloMode bool) error {
			record("solo", args, soloMode)
			return nil
		},
		runAdmin: func(args []string) error {
			record("admin", args, false)
			return nil
		},
		version: func() string {
			return "test-version"
		},
	}
}

func TestRunCLIExplicitRouting(t *testing.T) {
	adminUsageText := formatAdminGroupUsage(adminTree, "admin")
	adminUserUsageText := formatAdminGroupUsage(findTestAdminGroup(t, "user"), "admin user")
	adminWorkerRegKeyUsageText := formatAdminGroupUsage(findTestAdminGroup(t, "worker", "reg-key"), "admin worker reg-key")

	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string
		wantStderr string
		wantCalls  []cliCall
	}{
		{
			name:       "bare command is rejected",
			args:       nil,
			wantCode:   1,
			wantStderr: `hint: use "leapmux solo"`,
		},
		{
			name:       "top-level solo flag is rejected",
			args:       []string{"-dev-frontend", "http://localhost:4328"},
			wantCode:   1,
			wantStderr: `hint: use "leapmux solo -dev-frontend"`,
		},
		{
			name:       "top-level help flag prints help",
			args:       []string{"--help"},
			wantCode:   0,
			wantStdout: usageText,
		},
		{
			name:       "top-level help command prints help",
			args:       []string{"help"},
			wantCode:   0,
			wantStdout: usageText,
		},
		{
			name:     "solo dispatches to solo mode",
			args:     []string{"solo", "-dev-frontend", "http://localhost:4328"},
			wantCode: 0,
			wantCalls: []cliCall{{
				command:  "solo",
				args:     []string{"-dev-frontend", "http://localhost:4328"},
				soloMode: true,
			}},
		},
		{
			name:     "solo version flag dispatches through solo",
			args:     []string{"solo", "--version"},
			wantCode: 0,
			wantCalls: []cliCall{{
				command:  "solo",
				args:     []string{"--version"},
				soloMode: true,
			}},
		},
		{
			name:     "dev dispatches to dev mode",
			args:     []string{"dev", "-dev-frontend", "http://localhost:4328"},
			wantCode: 0,
			wantCalls: []cliCall{{
				command:  "solo",
				args:     []string{"-dev-frontend", "http://localhost:4328"},
				soloMode: false,
			}},
		},
		{
			name:     "hub dispatches unchanged",
			args:     []string{"hub", "-listen", ":4327"},
			wantCode: 0,
			wantCalls: []cliCall{{
				command: "hub",
				args:    []string{"-listen", ":4327"},
			}},
		},
		{
			name:     "worker dispatches unchanged",
			args:     []string{"worker", "--hub", "https://hub.example.com"},
			wantCode: 0,
			wantCalls: []cliCall{{
				command: "worker",
				args:    []string{"--hub", "https://hub.example.com"},
			}},
		},
		{
			name:     "admin dispatches unchanged",
			args:     []string{"admin", "user", "list"},
			wantCode: 0,
			wantCalls: []cliCall{{
				command: "admin",
				args:    []string{"user", "list"},
			}},
		},
		{
			name:       "admin without group prints admin usage without dispatching",
			args:       []string{"admin"},
			wantCode:   1,
			wantStderr: "Usage: leapmux admin <group> <command> [flags]",
		},
		{
			name:       "admin help prints admin usage without dispatching",
			args:       []string{"admin", "--help"},
			wantCode:   0,
			wantStdout: adminUsageText,
		},
		{
			name:       "admin group without command prints group usage without dispatching",
			args:       []string{"admin", "user"},
			wantCode:   1,
			wantStderr: "Usage: leapmux admin user <command> [flags]",
		},
		{
			name:       "admin group help prints group usage without dispatching",
			args:       []string{"admin", "user", "--help"},
			wantCode:   0,
			wantStdout: adminUserUsageText,
		},
		{
			name:       "unknown admin group prints clean error without dispatching",
			args:       []string{"admin", "bogus"},
			wantCode:   1,
			wantStderr: "unknown admin group: bogus",
		},
		{
			name:       "unknown admin group command prints group usage without dispatching",
			args:       []string{"admin", "user", "bogus"},
			wantCode:   1,
			wantStderr: "unknown admin user command: bogus",
		},
		{
			name:       "admin nested group without command prints nested usage without dispatching",
			args:       []string{"admin", "worker", "reg-key"},
			wantCode:   1,
			wantStderr: "Usage: leapmux admin worker reg-key <command> [flags]",
		},
		{
			name:       "admin nested group help prints nested usage without dispatching",
			args:       []string{"admin", "worker", "reg-key", "help"},
			wantCode:   0,
			wantStdout: adminWorkerRegKeyUsageText,
		},
		{
			name:       "unknown admin nested group command prints nested usage without dispatching",
			args:       []string{"admin", "worker", "reg-key", "bogus"},
			wantCode:   1,
			wantStderr: "unknown admin worker reg-key command: bogus",
		},
		{
			name:       "unknown command is rejected",
			args:       []string{"bogus"},
			wantCode:   1,
			wantStderr: "unknown command: bogus",
		},
		{
			name:       "version command prints version",
			args:       []string{"version"},
			wantCode:   0,
			wantStdout: "test-version\n",
		},
		{
			name:       "version help prints usage",
			args:       []string{"version", "--help"},
			wantCode:   0,
			wantStdout: "Print version and exit.\n\nUsage: leapmux version\n",
		},
		{
			name:       "top-level long version flag prints version",
			args:       []string{"--version"},
			wantCode:   0,
			wantStdout: "test-version\n",
		},
		{
			name:       "top-level short version flag prints version",
			args:       []string{"-version"},
			wantCode:   0,
			wantStdout: "test-version\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			var calls []cliCall

			code := runCLI(tt.args, &stdout, &stderr, testCLIRunners(&calls))

			assert.Equal(t, tt.wantCode, code)
			assert.Equal(t, tt.wantStdout, stdout.String())
			assert.Contains(t, stderr.String(), tt.wantStderr)
			assert.Equal(t, tt.wantCalls, calls)
		})
	}
}

func TestRunCLISubcommandErrorReportsPlainly(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	runners := testCLIRunners(&[]cliCall{})
	runners.runHub = func([]string) error {
		return fmt.Errorf("simulated startup failure")
	}

	code := runCLI([]string{"hub"}, &stdout, &stderr, runners)

	assert.Equal(t, 1, code)
	assert.Equal(t, "", stdout.String())
	assert.Equal(t, "error: simulated startup failure\n", stderr.String())
}

func TestRunCLISubcommandHelpReturnsSuccess(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	runners := testCLIRunners(&[]cliCall{})
	runners.runHub = func([]string) error {
		_, _ = stdout.WriteString("hub help\n")
		return flag.ErrHelp
	}

	code := runCLI([]string{"hub", "--help"}, &stdout, &stderr, runners)

	assert.Equal(t, 0, code)
	assert.Equal(t, "", stderr.String())
	assert.Contains(t, stdout.String(), "hub help")
}

func TestRunCLIAdminLeafHelpIncludesDescription(t *testing.T) {
	var stderr bytes.Buffer
	runners := testCLIRunners(&[]cliCall{})
	runners.runAdmin = func(args []string) error {
		return runAdmin(args)
	}

	var code int
	stdout := testutil.CaptureStdout(t, func() {
		code = runCLI([]string{"admin", "user", "list", "--help"}, io.Discard, &stderr, runners)
	})

	assert.Equal(t, 0, code)
	assert.Equal(t, "", stderr.String())
	assert.Contains(t, stdout, "List users.")
	assert.Contains(t, stdout, "Usage: leapmux admin user list [flags]")
	assert.Contains(t, stdout, "-query string")
}

func TestRunSoloRejectsUnknownFlag(t *testing.T) {
	err := runSolo([]string{"--unknown-flag"}, true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown-flag")
}

func findTestAdminGroup(t *testing.T, path ...string) adminGroup {
	t.Helper()
	g := adminTree
	for _, name := range path {
		var next *adminGroup
		for i := range g.Subgroups {
			if g.Subgroups[i].Name == name {
				next = &g.Subgroups[i]
				break
			}
		}
		require.NotNil(t, next, "admin tree: missing group %v", path)
		g = *next
	}
	return g
}
