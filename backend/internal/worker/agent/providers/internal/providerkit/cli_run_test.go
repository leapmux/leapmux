package providerkit

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAfterPreamble(t *testing.T) {
	t.Parallel()
	const delimiter = "LEAPMUX_DELIMITER"
	for _, test := range []struct {
		name      string
		input     string
		delimiter string
		output    string
		ready     bool
	}{
		{"returns the provider output", "motd\n" + delimiter + "\n[]\n", delimiter, "[]\n", true},
		{"accepts CRLF and empty output", "motd\r\n" + delimiter + "\r\n", delimiter, "", true},
		{"rejects an absent delimiter", "[]", delimiter, "", false},
		{"rejects an unterminated delimiter", "motd\n" + delimiter, delimiter, "", false},
		{"rejects an embedded delimiter", "motd" + delimiter + "\n[]\n", delimiter, "", false},
		{"accepts raw output without a preamble", "x", "", "x", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			output, ready := AfterPreamble([]byte(test.input), test.delimiter)
			assert.Equal(t, test.output, string(output))
			assert.Equal(t, test.ready, ready)
		})
	}
}

func TestRunCLIPreservesLaunchRequirementsWithAnExactEnvironment(t *testing.T) {
	t.Parallel()
	executable, err := os.Executable()
	require.NoError(t, err)
	home := t.TempDir()
	locator := launch.Custom(func(context.Context, string, bool) (launch.Spec, launch.Resolution) {
		return launch.Spec{Program: executable, PrefixArgs: []string{"-test.run=^TestRunCLIEnvironmentPeer$", "--"}, Env: []string{"LEAPMUX_REQUIRED_LAUNCH=required"}}, launch.Found
	})
	snapshot := agenttest.PlatformBootstrapEnv([]string{"HOME=" + home, "USERPROFILE=" + home, "ZDOTDIR=" + home, "PATH=" + os.Getenv("PATH"), "LEAPMUX_CLI_ENV_PEER=1", "PRIVATE_TOKEN=snapshot-value"})
	output, err := RunCLI(t.Context(), CLIRun{
		Locator: locator, Label: "controlled CLI", WorkingDir: home, SetEnv: []string{"PRIVATE_TOKEN=protected-value"},
		Env: func([]string) []string { return snapshot }, Timeout: 30 * time.Second, MaxOutput: 64 << 10,
	})
	require.NoError(t, err, "%s", output)
	var observed map[string]string
	require.NoError(t, json.Unmarshal(output, &observed))
	assert.Equal(t, "protected-value", observed["private"])
	assert.Equal(t, "required", observed["launch"])
}

func TestRunCLIEnvironmentPeer(t *testing.T) {
	if os.Getenv("LEAPMUX_CLI_ENV_PEER") != "1" {
		return
	}
	raw, err := json.Marshal(map[string]string{"private": os.Getenv("PRIVATE_TOKEN"), "launch": os.Getenv("LEAPMUX_REQUIRED_LAUNCH")})
	if err != nil {
		os.Exit(2)
	}
	fmt.Println(string(raw))
	os.Exit(0)
}
