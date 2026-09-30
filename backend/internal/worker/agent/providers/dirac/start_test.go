package dirac

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/leapmux/leapmux/internal/worker/terminal"
)

const diracStartupHelperEnv = "LEAPMUX_DIRAC_STARTUP_HELPER"
const diracStartupLogEnv = "LEAPMUX_DIRAC_STARTUP_LOG"

// TestMain runs the fake ACP peer before the Go test runner parses CLI flags.
// The provider's real `--acp --subagents` flags then reach this test binary.
func TestMain(m *testing.M) {
	if os.Getenv(diracStartupHelperEnv) == "1" {
		runDiracStartupPeer()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func runDiracStartupPeer() {
	log, err := os.OpenFile(os.Getenv(diracStartupLogEnv), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		os.Exit(2)
	}
	scanner := bufio.NewScanner(os.Stdin)
	writer := bufio.NewWriter(os.Stdout)
	for scanner.Scan() {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil || len(request.ID) == 0 {
			continue
		}
		if _, err := log.Write(append(append([]byte(nil), scanner.Bytes()...), '\n')); err != nil {
			os.Exit(2)
		}
		result := `{}`
		switch request.Method {
		case "initialize":
			result = `{"protocolVersion":1,"agentInfo":{"name":"dirac-test","version":"1"},"agentCapabilities":{}}`
		case "session/new":
			result = `{"sessionId":"dirac-test-session","modes":{"currentModeId":"act","availableModes":[{"id":"plan","name":"Plan"},{"id":"act","name":"Act"}]}}`
		}
		if _, err := fmt.Fprintf(writer, `{"jsonrpc":"2.0","id":%s,"result":%s}`+"\n", request.ID, result); err != nil {
			os.Exit(2)
		}
		if writer.Flush() != nil {
			os.Exit(2)
		}
	}
	if scanner.Err() != nil {
		os.Exit(2)
	}
	if log.Close() != nil {
		os.Exit(2)
	}
}

func TestStartAppliesTheRequestedNativePlanMode(t *testing.T) {
	home := t.TempDir()
	work := filepath.Join(home, "work")
	require.NoError(t, os.MkdirAll(work, 0o700))
	logPath := filepath.Join(home, "requests.jsonl")
	executable, err := os.Executable()
	require.NoError(t, err)

	previous := diracLocator
	diracLocator = launch.Custom(func(context.Context, string, bool) (launch.Spec, launch.Resolution) {
		return launch.Spec{Program: executable, Env: []string{
			diracStartupHelperEnv + "=1",
			diracStartupLogEnv + "=" + logPath,
		}}, launch.Found
	})
	t.Cleanup(func() { diracLocator = previous })

	sink := &agenttest.Sink{}
	started, err := Start(t.Context(), agent.Options{
		AgentID: "dirac-startup-test", WorkingDir: work, HomeDir: home,
		Shell: terminal.ResolveDefaultShell(), StartupTimeout: 10 * time.Second,
		Options: optionmap.Map{agent.OptionIDPermissionMode: "plan"},
	}, agent.NewProviderServices(sink))
	require.NoError(t, err)
	t.Cleanup(started.Stop)

	data, err := os.ReadFile(logPath)
	require.NoError(t, err)
	var nativeMode string
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		var request struct {
			Method string `json:"method"`
			Params struct {
				ModeID string `json:"modeId"`
			} `json:"params"`
		}
		if json.Unmarshal(line, &request) == nil && request.Method == "session/set_mode" {
			nativeMode = request.Params.ModeID
		}
	}
	assert.Equal(t, "plan", nativeMode, "the native session must enter Plan before its first prompt")
}
