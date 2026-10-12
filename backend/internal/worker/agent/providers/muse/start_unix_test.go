//go:build !windows

package muse

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func installMuseTestHost(t *testing.T) {
	t.Helper()
	executable, err := os.Executable()
	require.NoError(t, err)
	dir := t.TempDir()
	path := filepath.Join(dir, "muse")
	script := "#!/bin/sh\nexec '" + strings.ReplaceAll(executable, "'", "'\\''") + "' -test.run '^TestMuseNativeHostHelper$' -- \"$@\"\n"
	require.NoError(t, os.WriteFile(path, []byte(script), 0o700))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestMuseInitializationInstallsTransportBeforeCallbacks(t *testing.T) {
	executable, err := os.Executable()
	require.NoError(t, err)
	cmd := exec.CommandContext(testutil.DeadlineContext(t), executable, "-test.run=^TestMuseInitializationProbe$")
	cmd.Env = append(os.Environ(), "LEAPMUX_MUSE_INITIALIZATION_PROBE=1")
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, string(output))
	assert.Contains(t, string(output), "PASS")
}

func TestMuseInitializationProbe(t *testing.T) {
	if os.Getenv("LEAPMUX_MUSE_INITIALIZATION_PROBE") != "1" {
		return
	}
	installMuseTestHost(t)
	t.Setenv("LEAPMUX_MUSE_NATIVE_HOST", "early-control")
	sink := &agenttest.Sink{}
	started, err := Start(testutil.DeadlineContext(t), agent.Options{AgentID: "native-probe", WorkingDir: t.TempDir(), Shell: "/bin/sh"}, agent.NewProviderServices(sink))
	require.NoError(t, err)
	started.Stop()
	require.NoError(t, started.Wait())
}

func TestMuseProcessCloseRetainsPartialOutput(t *testing.T) {
	installMuseTestHost(t)
	t.Setenv("LEAPMUX_MUSE_NATIVE_HOST", "close-turn")
	sink := &agenttest.Sink{}
	started, err := Start(testutil.DeadlineContext(t), agent.Options{AgentID: "native-probe", WorkingDir: t.TempDir(), Shell: "/bin/sh"}, agent.NewProviderServices(sink))
	require.NoError(t, err)
	require.NoError(t, started.SendInput("run", nil))
	require.NoError(t, started.Wait())
	var partials []string
	for _, message := range sink.Messages() {
		if _, text, ended, ok := agenttest.DecodeAssembledMessage(message.Content); ok {
			partials = append(partials, text)
			assert.Equal(t, agent.MessageCompletionError, ended)
			assert.Equal(t, "native-session", message.AgentSessionID)
		}
	}
	assert.Equal(t, []string{"native partial"}, partials)
}

func TestStoredSessionPickerRejectsInvalidRequiredDates(t *testing.T) {
	installMuseTestHost(t)
	t.Setenv("LEAPMUX_MUSE_NATIVE_HOST", "session-list")
	for _, field := range []string{"createdAt", "updatedAt", "lastActivityAt"} {
		t.Run(field, func(t *testing.T) {
			t.Setenv("LEAPMUX_MUSE_NATIVE_LIST_INVALID_DATE", field)
			_, err := storedSessions(testutil.DeadlineContext(t), agent.StoredSessionQuery{WorkingDir: t.TempDir(), Shell: "/bin/sh"}, nil)
			require.Error(t, err)
		})
	}
}

func TestMuseStartPreservesNativeRefusalAndOwnedCleanupFailures(t *testing.T) {
	for _, tc := range []struct {
		label  string
		mode   string
		resume string
		cause  string
	}{
		{label: "session start", mode: "refuse-session-open", cause: "The native session cannot open"},
		{label: "session resume", mode: "refuse-session-open", resume: "stored-session", cause: "The native session cannot open"},
		{label: "model catalog", mode: "refuse-model-catalog", cause: "The native model catalog is unavailable"},
	} {
		t.Run(tc.label, func(t *testing.T) {
			installMuseTestHost(t)
			t.Setenv("LEAPMUX_MUSE_NATIVE_HOST", tc.mode)
			t.Setenv("LEAPMUX_MUSE_NATIVE_CLOSE_CODE", "7")
			closed := filepath.Join(t.TempDir(), "owned-host-close.txt")
			t.Setenv("LEAPMUX_MUSE_NATIVE_CLOSE_RECORD", closed)
			sink := &agenttest.Sink{}
			started, err := Start(testutil.DeadlineContext(t), agent.Options{AgentID: "native-refusal", WorkingDir: t.TempDir(), Shell: "/bin/sh", ResumeSessionID: tc.resume}, agent.NewProviderServices(sink))
			require.Nil(t, started)
			require.ErrorContains(t, err, tc.cause)
			var refusal *providerkit.JSONRPCResponseError
			if assert.ErrorAs(t, err, &refusal) {
				assert.Equal(t, -32000, refusal.Code)
				assert.Equal(t, tc.cause, refusal.Message)
			}
			var nativeExit *exec.ExitError
			if assert.ErrorAs(t, err, &nativeExit) {
				assert.Equal(t, 7, nativeExit.ExitCode())
			}
			if tc.resume != "" {
				assert.ErrorContains(t, err, `could not resume session "stored-session"`)
			}
			raw, readErr := os.ReadFile(closed)
			require.NoError(t, readErr)
			assert.Equal(t, "7", string(raw))
		})
	}
}

func TestMuseNativeHostHelper(t *testing.T) {
	mode := os.Getenv("LEAPMUX_MUSE_NATIVE_HOST")
	if mode == "" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	var initializeID json.RawMessage
	write := func(value any) {
		raw, err := json.Marshal(value)
		if err != nil {
			os.Exit(2)
		}
		fmt.Println(string(raw))
	}
	reply := func(id json.RawMessage, result any) {
		write(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	}
	for scanner.Scan() {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params map[string]any  `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			os.Exit(3)
		}
		switch request.Method {
		case methodInitialize:
			if mode == "early-control" {
				initializeID = request.ID
				write(map[string]any{"jsonrpc": "2.0", "id": "early", "method": "userInput/request", "params": map[string]any{"sessionId": "not-open", "userInputId": "early"}})
				continue
			}
			reply(request.ID, map[string]any{"serverInfo": map[string]any{"name": "muse", "version": "test"}, "schema": map[string]any{"version": 1, "fingerprint": "sha256:" + strings.Repeat("0", 64)}})
		case "":
			if string(request.ID) == `"early"` && initializeID != nil {
				reply(initializeID, map[string]any{"serverInfo": map[string]any{"name": "muse", "version": "test"}, "schema": map[string]any{"version": 1, "fingerprint": "sha256:" + strings.Repeat("0", 64)}})
			}
		case methodInitialized:
		case methodSessionStart, methodSessionResume:
			if mode == "refuse-session-open" {
				write(map[string]any{"jsonrpc": "2.0", "id": request.ID, "error": map[string]any{"code": -32000, "message": "The native session cannot open"}})
				continue
			}
			reply(request.ID, map[string]any{"session": map[string]any{"sessionId": "native-session", "activeTurnId": nil, "modelId": nil}, "viewCursor": "zero"})
		case methodModelList:
			if mode == "refuse-model-catalog" {
				write(map[string]any{"jsonrpc": "2.0", "id": request.ID, "error": map[string]any{"code": -32000, "message": "The native model catalog is unavailable"}})
				continue
			}
			reply(request.ID, map[string]any{"models": []any{}})
		case methodSessionList:
			session := map[string]any{"sessionId": "native-session", "kind": "root", "modelId": nil, "activeTurnId": nil, "createdAt": "2026-10-08T00:00:00Z", "updatedAt": "2026-10-08T00:00:01Z", "lastActivityAt": "2026-10-08T00:00:01Z"}
			if field := os.Getenv("LEAPMUX_MUSE_NATIVE_LIST_INVALID_DATE"); field != "" {
				session[field] = "invalid-native-date"
			}
			reply(request.ID, map[string]any{"sessions": []any{session}, "nextCursor": nil})
		case methodTurnStart:
			reply(request.ID, map[string]any{"commandId": request.Params["commandId"], "status": "accepted", "turnId": "native-turn"})
			write(map[string]any{"jsonrpc": "2.0", "method": "item/started", "params": map[string]any{"sessionId": "native-session", "viewCursor": "one", "item": map[string]any{"itemId": "native-text", "kind": "agentMessage", "turnId": "native-turn", "revision": 1, "status": "inProgress"}}})
			write(map[string]any{"jsonrpc": "2.0", "method": "item/delta", "params": map[string]any{"sessionId": "native-session", "viewCursor": "two", "itemId": "native-text", "delta": "native partial"}})
			os.Exit(0)
		default:
			os.Exit(4)
		}
	}
	closeCode := 0
	if value := os.Getenv("LEAPMUX_MUSE_NATIVE_CLOSE_CODE"); value != "" {
		var err error
		closeCode, err = strconv.Atoi(value)
		if err != nil {
			os.Exit(5)
		}
	}
	if path := os.Getenv("LEAPMUX_MUSE_NATIVE_CLOSE_RECORD"); path != "" {
		if os.WriteFile(path, []byte(strconv.Itoa(closeCode)), 0o600) != nil {
			os.Exit(6)
		}
	}
	os.Exit(closeCode)
}
