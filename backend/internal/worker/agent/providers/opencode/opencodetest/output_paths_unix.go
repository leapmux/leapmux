//go:build unix

package opencodetest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func AssertNativeOutputPathDoesNotSaveText(t *testing.T, binary string, start agent.StartFunc) {
	t.Helper()
	home, data := t.TempDir(), t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	path := filepath.Join(data, binary, "tool-output", "tool_0fa3ad9a5001sgZjoc4ufeqePo")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	complete := fmt.Sprintf("first\ncomputed-middle%d\nlast%d", 70+7, 40+2)
	require.NoError(t, os.WriteFile(path, []byte(complete), 0o600))
	agenttest.InstallFakeCLI(t, agenttest.FakeCLI{
		Binary: binary, HelperRun: "TestHelperProcessCompleteToolOutputFile", WantEnv: outputFileHelperEnv,
		Env: []string{outputFilePathEnv + "=" + path},
	})
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	sink := &outputFileTurnSink{Sink: &agenttest.Sink{}, finished: make(chan struct{})}
	provider, err := start(ctx, agent.Options{AgentID: "full-output-startup", HomeDir: home, WorkingDir: t.TempDir(), Shell: testutil.TestShell()}, agent.NewProviderServices(sink))
	require.NoError(t, err)
	defer func() {
		provider.Stop()
		// Stop cancels the process context. EOF can finish the fake CLI before its stop signal arrives.
		if waitErr := provider.Wait(); waitErr != nil && !errors.Is(waitErr, context.Canceled) {
			var exit *exec.ExitError
			require.ErrorAs(t, waitErr, &exit)
			status, ok := exit.Sys().(syscall.WaitStatus)
			require.True(t, ok)
			assert.True(t, status.Signaled(), "the stopped fake CLI must exit through its stop signal")
		}
	}()
	require.NoError(t, provider.SendInput("Run the captured native operation.", nil))
	select {
	case <-sink.finished:
	case <-ctx.Done():
		t.Fatal("the fake native turn did not end")
	}
	var results []agenttest.Message
	for _, row := range sink.Messages() {
		if row.SpanID == "native-full-output" && row.Closing {
			results = append(results, row)
		}
	}
	require.Len(t, results, 1)
	var original struct {
		RawOutput struct {
			Output string `json:"output"`
		} `json:"rawOutput"`
	}
	require.NoError(t, json.Unmarshal(results[0].Content, &original))
	assert.NotContains(t, original.RawOutput.Output, "computed-middle77")
	assert.NotContains(t, string(results[0].SupplementalContent), "computed-middle77", "external output text must not enter Worker storage")
	assert.Contains(t, string(results[0].Content), path)
}
