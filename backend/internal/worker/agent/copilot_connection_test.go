//go:build unix

package agent

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/textproto"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/stretchr/testify/require"
)

func TestCopilotNativeConnection(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "args")
	installFakeACPCLI(t, fakeACPCLISpec{
		binary: "copilot", helperRun: "TestHelperCopilotNativeConnection",
		wantEnv: "LEAPMUX_TEST_COPILOT_NATIVE", argsFile: argsFile,
	})
	notifications := make(chan []byte, 1)
	connection, err := startCopilotConnection(t.Context(), Options{
		AgentID: "copilot-native", WorkingDir: t.TempDir(), Shell: testutil.TestShell(),
		APITimeout: time.Second,
	}, func(line *parsedLine) { notifications <- line.Raw })
	require.NoError(t, err)
	t.Cleanup(func() { connection.Stop(); _ = connection.Wait() })
	args, err := os.ReadFile(argsFile)
	require.NoError(t, err)
	require.Contains(t, string(args), "--server --stdio")
	require.NotContains(t, string(args), "--acp")
	require.NotContains(t, string(args), "--disable-builtin-mcps")
	result, err := connection.sendRequest("probe.echo", json.RawMessage(`{"text":"한글"}`), time.Second)
	require.NoError(t, err)
	require.JSONEq(t, `{"text":"한글"}`, string(result))
	for _, value := range []string{
		`{"kind":"completed","message":"Switched to interactive mode."}`,
		`{"code":429,"message":"This is tool output, not a protocol error."}`,
		`null`, `false`, `0`, `""`,
	} {
		result, err := connection.sendRequest("probe.echo", json.RawMessage(value), time.Second)
		require.NoError(t, err, value)
		require.JSONEq(t, value, string(result))
	}
	select {
	case raw := <-notifications:
		require.Equal(t, " {\"jsonrpc\":\"2.0\",\"method\":\"probe.notification\", \"params\":{\"counter\":9007199254740993}} ", string(raw))
	case <-time.After(time.Second):
		t.Fatal("The connection lost a native notification during startup")
	}
}

func TestCopilotNativeConnectionRejectsAnUnknownProtocol(t *testing.T) {
	installFakeACPCLI(t, fakeACPCLISpec{
		binary: "copilot", helperRun: "TestHelperCopilotNativeConnection",
		wantEnv: "LEAPMUX_TEST_COPILOT_NATIVE", env: []string{"LEAPMUX_TEST_COPILOT_PROTOCOL=99"},
	})
	_, err := startCopilotConnection(t.Context(), Options{
		AgentID: "copilot-native", WorkingDir: t.TempDir(), Shell: testutil.TestShell(),
		APITimeout: time.Second,
	}, func(*parsedLine) {})
	require.ErrorContains(t, err, "protocol 99")
}

func TestHelperCopilotNativeConnection(t *testing.T) {
	if os.Getenv("LEAPMUX_TEST_COPILOT_NATIVE") != "1" {
		return
	}
	reader := bufio.NewReader(os.Stdin)
	headers := textproto.NewReader(reader)
	send := func(raw []byte) {
		_, err := fmt.Fprintf(os.Stdout, "Content-Length: %d\r\n\r\n", len(raw))
		require.NoError(t, err)
		_, err = os.Stdout.Write(raw)
		require.NoError(t, err)
	}
	var sessionID, model, effort string
	var delayedSendID json.RawMessage
	sendCount := 0
	createCount := 0
	replacementFailure := os.Getenv("LEAPMUX_TEST_COPILOT_REPLACEMENT_FAILURE")
	mode, permissionMode := "interactive", "manual"
	// The fake runtime's autopilot objective. An empty objective means none, which
	// getState reports as a null state.
	goal := fakeCopilotObjective{}
	releasedInterests := 0
	sessionClosed := false
	var lastControlValue json.RawMessage
	emitIdle := func() {
		raw, err := json.Marshal(map[string]any{
			"jsonrpc": "2.0", "method": "session.event",
			"params": map[string]any{"sessionId": sessionID, "event": map[string]any{"type": "session.idle", "data": map[string]any{}}},
		})
		require.NoError(t, err)
		send(raw)
	}
	for {
		if next, err := reader.Peek(1); err == nil && next[0] == '{' {
			_, _ = fmt.Fprintln(os.Stderr, "The native probe rejects the ACP transport")
			os.Exit(2)
		}
		header, err := headers.ReadMIMEHeader()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		length, err := strconv.Atoi(header.Get("Content-Length"))
		require.NoError(t, err)
		payload := make([]byte, length)
		_, err = io.ReadFull(reader, payload)
		require.NoError(t, err)
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		require.NoError(t, json.Unmarshal(payload, &request))
		if path := os.Getenv("LEAPMUX_TEST_COPILOT_SESSION_REQUESTS"); path != "" {
			file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
			require.NoError(t, err)
			_, err = fmt.Fprintln(file, string(payload))
			require.NoError(t, err)
			require.NoError(t, file.Close())
		}
		result := request.Params
		if request.Method == "status.get" {
			send([]byte(" {\"jsonrpc\":\"2.0\",\"method\":\"probe.notification\", \"params\":{\"counter\":9007199254740993}} "))
			protocol := 3
			if value := os.Getenv("LEAPMUX_TEST_COPILOT_PROTOCOL"); value != "" {
				protocol, err = strconv.Atoi(value)
				require.NoError(t, err)
			}
			result = json.RawMessage(fmt.Sprintf(`{"version":"probe","protocolVersion":%d}`, protocol))
		} else if request.Method == "session.create" || request.Method == "session.resume" {
			if request.Method == "session.create" {
				createCount++
			}
			if createCount > 1 && (replacementFailure == "create" && request.Method == "session.create" || replacementFailure == "restore" && request.Method == "session.resume") {
				send([]byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"error":{"code":-32603,"message":"The session could not open"}}`, request.ID)))
				continue
			}
			// The real runtime refuses to resume a session its store never recorded,
			// which is every session that ran no model turn. See CP-012.
			if request.Method == "session.resume" && os.Getenv("LEAPMUX_TEST_COPILOT_UNRESUMABLE") != "" {
				send([]byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"error":{"code":-32603,"message":"Request session.resume failed with message: Failed to load session events: Session not found"}}`, request.ID)))
				continue
			}
			var params struct {
				SessionID       string `json:"sessionId"`
				Model           string `json:"model"`
				ReasoningEffort string `json:"reasoningEffort"`
			}
			require.NoError(t, json.Unmarshal(request.Params, &params))
			require.NotEmpty(t, params.SessionID)
			sessionID, model, effort = params.SessionID, params.Model, params.ReasoningEffort
			mode, permissionMode = "interactive", "manual"
			if replacement := os.Getenv("LEAPMUX_TEST_COPILOT_SESSION_RETURN_ID"); replacement != "" {
				params.SessionID = replacement
			}
			result, err = json.Marshal(map[string]any{"sessionId": params.SessionID})
			require.NoError(t, err)
		} else if request.Method == "sessions.close" {
			sessionClosed = true
			result = json.RawMessage(`{"closed":true}`)
		} else if request.Method == "probe.interests" {
			result, err = json.Marshal(map[string]any{"released": releasedInterests, "closed": sessionClosed})
			require.NoError(t, err)
		} else if request.Method == "probe.lastControlValue" {
			result, err = json.Marshal(map[string]any{"value": string(lastControlValue)})
			require.NoError(t, err)
		} else if request.Method == "models.list" {
			result = json.RawMessage(`{"models":[{"id":"probe-model","name":"Probe model","supportedReasoningEfforts":["low","medium","high"]}]}`)
		} else if strings.HasPrefix(request.Method, "session.") {
			var params struct {
				SessionID        string          `json:"sessionId"`
				RequestID        string          `json:"requestId"`
				ModelID          string          `json:"modelId"`
				ReasoningEffort  string          `json:"reasoningEffort"`
				Mode             string          `json:"mode"`
				RequireAvailable bool            `json:"requireAvailable"`
				WorkingDirectory string          `json:"workingDirectory"`
				Result           json.RawMessage `json:"result"`
			}
			require.NoError(t, json.Unmarshal(request.Params, &params))
			if request.Method != "session.suspend" {
				require.Equal(t, sessionID, params.SessionID)
			}
			var value any = map[string]any{}
			switch request.Method {
			case "session.model.list":
				value = map[string]any{"list": []map[string]any{{
					"id": "probe-model", "name": "Probe model",
					"capabilities": map[string]any{"supports": map[string]any{"reasoning_effort": []string{"low", "medium", "high"}}},
				}}}
			case "session.model.getCurrent":
				value = map[string]any{"modelId": model, "reasoningEffort": effort}
			case "session.model.switchTo":
				if params.RequireAvailable && params.ModelID == "missing-model" {
					value = map[string]any{"status": "cancelled", "deferred": false, "modelId": model}
					break
				}
				model = params.ModelID
				value = map[string]any{"status": "applied", "deferred": false}
			case "session.model.setReasoningEffort":
				effort = params.ReasoningEffort
				value = map[string]any{"reasoningEffort": effort}
			case "session.mode.get":
				value = mode
			case "session.mode.set":
				mode = params.Mode
				value = map[string]any{"status": "unchanged", "modelChanged": false}
			case "session.permissions.getMode":
				value = map[string]any{"mode": permissionMode}
			case "session.permissions.setMode":
				permissionMode = params.Mode
				value = map[string]any{"success": true, "mode": permissionMode}
			case "session.send":
				sendCount++
				if rejection := os.Getenv("LEAPMUX_TEST_COPILOT_REJECT_INPUT"); rejection != "" {
					if rejection == "after-new-turn" {
						emitIdle()
						send([]byte(fmt.Sprintf(`{"jsonrpc":"2.0","method":"session.event","params":{"sessionId":%q,"event":{"type":"assistant.turn_start","data":{"turnId":"autonomous-turn"}}}}`, sessionID)))
					}
					send([]byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"error":{"code":-32602,"message":"The input was rejected"}}`, request.ID)))
					continue
				}
				if os.Getenv("LEAPMUX_TEST_COPILOT_LATE_SEND_ERROR") == "1" && sendCount == 1 {
					delayedSendID = append(json.RawMessage(nil), request.ID...)
					emitIdle()
					continue
				}
				value = map[string]any{"messageId": "delivered-message"}
			case "session.eventLog.registerInterest":
				if createCount > 1 && (replacementFailure == "subscription" || replacementFailure == "restore") {
					replacementFailure = map[string]string{"subscription": "", "restore": "restore"}[replacementFailure]
					send([]byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"error":{"code":-32603,"message":"The control subscription failed"}}`, request.ID)))
					continue
				}
				value = map[string]any{"handle": "interest-" + string(request.ID)}
			case "session.permissions.handlePendingPermissionRequest", "session.ui.handlePendingUserInput", "session.ui.handlePendingExitPlanMode", "session.ui.handlePendingElicitation":
				lastControlValue = append(json.RawMessage(nil), params.Result...)
				switch os.Getenv("LEAPMUX_TEST_COPILOT_CONTROL_OUTCOME") {
				case "timeout":
					continue
				case "missing-receipt":
					send([]byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":{"unrelated":true}}`, request.ID)))
					continue
				case "protocol-error":
					send([]byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"error":{"code":-32602,"message":"The response is invalid"}}`, request.ID)))
					continue
				}
				accepted := os.Getenv("LEAPMUX_TEST_COPILOT_CONTROL_REJECT") != "1"
				value = map[string]any{"success": accepted}
				if accepted {
					kind := map[string]string{
						"session.permissions.handlePendingPermissionRequest": "permission.completed",
						"session.ui.handlePendingUserInput":                  "user_input.completed",
						"session.ui.handlePendingExitPlanMode":               "exit_plan_mode.completed",
						"session.ui.handlePendingElicitation":                "elicitation.completed",
					}[request.Method]
					event, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "session.event", "params": map[string]any{"sessionId": sessionID, "event": map[string]any{"type": kind, "data": map[string]any{"requestId": params.RequestID}}}})
					require.NoError(t, err)
					send(event)
				}
			case "session.abort", "session.suspend":
				emitIdle()
			case "session.permissions.locations.resolve":
				if os.Getenv("LEAPMUX_TEST_COPILOT_NO_LOCATION") == "1" {
					value = map[string]any{"locationKey": ""}
					break
				}
				value = map[string]any{"locationKey": "location-" + params.WorkingDirectory, "locationType": "project"}
			case "session.eventLog.releaseInterest":
				releasedInterests++
				value = map[string]any{"released": true}
			case "session.commands.invoke":
				value = invokeFakeCopilotCommand(t, request.Params, &goal)
			case "session.autopilotObjective.getState":
				if goal.objective == "" {
					value = map[string]any{"state": nil}
					break
				}
				value = map[string]any{"state": map[string]any{
					"id": 1, "objective": goal.objective, "status": goal.status, "turnCount": goal.turns,
				}}
			case "session.shutdown":
				value = map[string]any{}
			case "session.workspaces.deleteAutopilotObjective":
				if os.Getenv("LEAPMUX_TEST_COPILOT_GOAL_CLEAR_SURVIVES") != "1" {
					goal = fakeCopilotObjective{}
				}
				value = map[string]any{"deleted": true}
			}
			result, err = json.Marshal(value)
			require.NoError(t, err)
		}
		send([]byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":%s}`, request.ID, result)))
		if request.Method == "session.send" && len(delayedSendID) > 0 {
			send([]byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"error":{"code":-32602,"message":"The first input was rejected"}}`, delayedSendID)))
			delayedSendID = nil
		}
	}
	os.Exit(0)
}

// fakeCopilotObjective is the autopilot objective the fake runtime stores.
type fakeCopilotObjective struct {
	objective string
	status    string
	turns     int
}

// invokeFakeCopilotCommand models the autopilot command that CP-002 and CP-003
// verified against Copilot 1.0.83:
//
//   - `off` pauses the stored objective and returns a completion.
//   - `on` changes the mode only, and leaves a paused objective paused.
//   - `--max-ai-credits <N>` resumes a paused objective and returns the prompt effect.
//   - `-- <text>` sets that literal objective and returns the prompt effect.
func invokeFakeCopilotCommand(t *testing.T, params json.RawMessage, goal *fakeCopilotObjective) any {
	t.Helper()
	var command struct {
		Name  string `json:"name"`
		Input string `json:"input"`
	}
	require.NoError(t, json.Unmarshal(params, &command))
	require.Equal(t, "autopilot", command.Name)
	switch {
	case command.Input == "off":
		if goal.objective != "" {
			goal.status = "paused"
		}
		return map[string]any{"kind": "completed", "message": "Autopilot is off."}
	case command.Input == "on":
		return map[string]any{"kind": "completed", "message": "Autopilot is on."}
	case strings.HasPrefix(command.Input, "--max-ai-credits"):
		if goal.objective == "" {
			return map[string]any{"kind": "text", "text": "There is no objective to resume."}
		}
		goal.status = "active"
		goal.turns++
		return map[string]any{"kind": "agent-prompt", "prompt": "Continue the objective.", "displayPrompt": "Continue"}
	default:
		goal.objective = strings.TrimPrefix(command.Input, "-- ")
		goal.status = "active"
		goal.turns = 0
		return map[string]any{"kind": "agent-prompt", "prompt": "Pursue: " + goal.objective, "displayPrompt": goal.objective}
	}
}
