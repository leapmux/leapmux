package codebuddy

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/leapmux/leapmux/internal/worker/terminal"
)

const codebuddyStartupHelperEnv = "LEAPMUX_CODEBUDDY_STARTUP_HELPER"

func TestCodebuddyStartupHelper(t *testing.T) {
	if os.Getenv(codebuddyStartupHelperEnv) != "1" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		os.Exit(24)
	}
	var request struct {
		Type      string `json:"type"`
		RequestID string `json:"request_id"`
		Request   struct {
			Subtype string `json:"subtype"`
		} `json:"request"`
	}
	if json.Unmarshal(scanner.Bytes(), &request) != nil || request.Type != "control_request" ||
		request.RequestID == "" || request.Request.Subtype != "get_available_models" {
		os.Exit(24)
	}
	if _, err := fmt.Fprintln(os.Stderr, "Controlled native catalog startup failure."); err != nil {
		os.Exit(25)
	}
	os.Exit(23)
}

func TestCodebuddyStartRejectsAProcessThatExitsDuringTheNativeCatalogRequest(t *testing.T) {
	// The launch locator is global, so this controlled process test stays serial.
	executable, err := os.Executable()
	require.NoError(t, err)
	previous := codebuddyLocator
	codebuddyLocator = launch.Custom(func(context.Context, string, bool) (launch.Spec, launch.Resolution) {
		return launch.Spec{
			Program:    executable,
			PrefixArgs: []string{"-test.run=^TestCodebuddyStartupHelper$", "--"},
			Env:        []string{codebuddyStartupHelperEnv + "=1"},
		}, launch.Found
	})
	t.Cleanup(func() { codebuddyLocator = previous })
	sink := &agenttest.Sink{}
	home := t.TempDir()
	started, err := Start(t.Context(), agent.Options{
		AgentID: "native-startup-test", WorkingDir: t.TempDir(), HomeDir: home,
		Shell: terminal.ResolveDefaultShell(), ExtraEnv: []string{"HOME=" + home, "USERPROFILE=" + home, "ZDOTDIR=" + home},
	}, agent.NewProviderServices(sink))
	if started != nil {
		t.Cleanup(started.Stop)
	}
	require.Error(t, err)
	assert.Nil(t, started)
	assert.Contains(t, err.Error(), "code 23")
	assert.Contains(t, err.Error(), "Controlled native catalog startup failure.")
	assert.Empty(t, sink.SessionIDs(), "a failed startup must not publish its provisional native session")
}
