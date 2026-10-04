//go:build unix

package gemini

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const geminiModelHelperEnv = "LEAPMUX_GEMINI_MODEL_HELPER"
const geminiModelRequestsEnv = "LEAPMUX_GEMINI_MODEL_REQUESTS"

func TestHelperProcessGeminiModelRoute(t *testing.T) {
	agenttest.ServeFakeJSONRPC(geminiModelHelperEnv, func(method string) (string, bool, bool) {
		file, err := os.OpenFile(os.Getenv(geminiModelRequestsEnv), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			os.Exit(2)
		}
		_, writeErr := file.WriteString(method + "\n")
		closeErr := file.Close()
		if writeErr != nil || closeErr != nil {
			os.Exit(2)
		}
		switch method {
		case "initialize":
			return `{"protocolVersion":1,"agentInfo":{"name":"gemini-cli","version":"0.62.0"},"agentCapabilities":{"loadSession":true}}`, false, true
		case "session/new":
			return `{"sessionId":"gemini-model-session","models":{"currentModelId":"gemini-2.5-pro","availableModels":[{"modelId":"gemini-2.5-pro","name":"Gemini 2.5 Pro"},{"modelId":"gemini-3.8-flash","name":"Gemini 3.8 Flash"}]},"modes":{"currentModeId":"default","availableModes":[{"id":"default","name":"Default"}]}}`, false, true
		case "session/set_model", "session/set_mode":
			return `{}`, false, true
		case "session/cancel":
			return "", false, false
		default:
			return `{"code":-32601,"message":"Method not found"}`, true, true
		}
	})
}

func TestStartChangesModelsThroughTheNativeModelRoute(t *testing.T) {
	home := t.TempDir()
	work := filepath.Join(home, "work")
	require.NoError(t, os.MkdirAll(work, 0o700))
	requestsPath := filepath.Join(home, "model-requests.txt")
	t.Setenv("GEMINI_CLI_HOME", home)
	agenttest.InstallFakeCLI(t, agenttest.FakeCLI{
		Binary: "gemini", HelperRun: "TestHelperProcessGeminiModelRoute", WantEnv: geminiModelHelperEnv,
		Env: []string{geminiModelRequestsEnv + "=" + requestsPath},
	})
	started, err := Start(t.Context(), agent.Options{
		AgentID: "gemini-model-route", HomeDir: home, WorkingDir: work,
		// A plain shell keeps the private PATH instead of loading a user's zsh startup configuration.
		Shell: "/bin/sh", StartupTimeout: 30 * time.Second,
		Options: optionmap.Map{agent.OptionIDModel: "gemini-2.5-pro", agent.OptionIDPermissionMode: "default"},
	}, agent.NewProviderServices(&agenttest.Sink{}))
	require.NoError(t, err)
	t.Cleanup(func() {
		started.Stop()
		err := started.Wait()
		require.True(t, expectedGeminiStopError(err), "unexpected controlled-peer cleanup failure: %v", err)
	})
	a, valid := started.(*Agent)
	require.True(t, valid)
	require.Equal(t, "gemini-model-session", a.CurrentSessionID(), "the controlled peer must supply the session before the behavioral assertions")
	initialMethods, err := os.ReadFile(requestsPath)
	require.NoError(t, err, "the controlled peer must record the actual startup requests")
	require.Contains(t, strings.Split(strings.TrimSpace(string(initialMethods)), "\n"), "session/new")
	result := a.UpdateSettings(optionmap.Map{agent.OptionIDModel: "gemini-3.8-flash"})
	assert.True(t, result.AppliedLive, "Gemini supports native model changes without restarting its process")
	assert.Equal(t, "gemini-3.8-flash", result.ConfirmedOptions()[agent.OptionIDModel])
	assert.Equal(t, "gemini-model-session", a.CurrentSessionID())
	methods, err := os.ReadFile(requestsPath)
	require.NoError(t, err)
	assert.Contains(t, strings.Split(strings.TrimSpace(string(methods)), "\n"), acp.MethodSessionSetModel)
	assert.NotContains(t, strings.Split(strings.TrimSpace(string(methods)), "\n"), acp.MethodSessionSetConfigOption)
}

// Each joined failure must be an expected stop outcome. A cancellation cannot hide an ownership failure.
func expectedGeminiStopError(err error) bool {
	if err == nil || err == context.Canceled {
		return true
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		causes := joined.Unwrap()
		if len(causes) == 0 {
			return false
		}
		for _, cause := range causes {
			if !expectedGeminiStopError(cause) {
				return false
			}
		}
		return true
	}
	if cause := errors.Unwrap(err); cause != nil {
		return expectedGeminiStopError(cause)
	}
	if exited, ok := err.(*exec.ExitError); ok && exited.ProcessState != nil {
		status, valid := exited.Sys().(syscall.WaitStatus)
		return valid && status.Signaled() && (status.Signal() == syscall.SIGTERM || status.Signal() == syscall.SIGKILL)
	}
	return false
}

type geminiEmptyJoinedError struct{}

func (geminiEmptyJoinedError) Error() string   { return "unexpected cleanup failure" }
func (geminiEmptyJoinedError) Unwrap() []error { return nil }

func TestGeminiStopCleanupRejectsUnexpectedJoinedFailures(t *testing.T) {
	t.Parallel()
	assert.True(t, expectedGeminiStopError(nil))
	assert.True(t, expectedGeminiStopError(context.Canceled))
	assert.True(t, expectedGeminiStopError(errors.Join(context.Canceled, context.Canceled)))
	assert.False(t, expectedGeminiStopError(errors.Join(context.Canceled, errors.New("ownership enumeration failed"))))
	assert.False(t, expectedGeminiStopError(errors.New("context canceled")))
	assert.False(t, expectedGeminiStopError(context.DeadlineExceeded))
	assert.False(t, expectedGeminiStopError(geminiEmptyJoinedError{}))
	for _, command := range []struct {
		source   string
		expected bool
	}{
		{"kill -TERM $$", true},
		{"kill -KILL $$", true},
		{"exit 2", false},
	} {
		err := exec.CommandContext(t.Context(), "/bin/sh", "-c", command.source).Run()
		require.Error(t, err)
		assert.Equal(t, command.expected, expectedGeminiStopError(err))
	}
}
