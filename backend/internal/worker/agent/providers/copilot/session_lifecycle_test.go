//go:build unix

package copilot

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/require"
)

func TestNativeCopilotLateSendRejectionKeepsTheNewTurnActive(t *testing.T) {
	agenttest.InstallFakeCLI(t, agenttest.FakeCLI{
		Binary: "copilot", HelperRun: "TestHelperCopilotNativeConnection",
		WantEnv: "LEAPMUX_TEST_COPILOT_NATIVE", Env: []string{"LEAPMUX_TEST_COPILOT_LATE_SEND_ERROR=1"},
	})
	sink := &agenttest.Sink{}
	provider, err := startNativeCopilot(t.Context(), agent.Options{
		AgentID: "native-send-order", WorkingDir: t.TempDir(), Shell: testutil.TestShell(),
		APITimeout: 3 * time.Second,
	}, agent.NewProviderServices(sink))
	require.NoError(t, err)
	t.Cleanup(func() { provider.Stop(); _ = provider.Wait() })
	first := make(chan error, 1)
	go func() { first <- provider.SendInput("first", nil) }()
	require.Eventually(t, func() bool {
		for _, message := range sink.Messages() {
			if bytes.Contains(message.Content, []byte(`"type":"session.idle"`)) {
				return true
			}
		}
		return false
	}, 2*time.Second, time.Millisecond)
	require.NoError(t, provider.SendInput("second", nil))
	select {
	case err := <-first:
		require.ErrorContains(t, err, "first input was rejected")
	case <-time.After(time.Second):
		t.Fatal("Copilot did not return the delayed rejection")
	}
	require.True(t, provider.PublishTurnActive().Active)
}

func TestNativeCopilotInputRejectionRespectsNewNativeActivity(t *testing.T) {
	for _, mode := range []string{"before-activity", "after-new-turn"} {
		t.Run(mode, func(t *testing.T) {
			agenttest.InstallFakeCLI(t, agenttest.FakeCLI{
				Binary: "copilot", HelperRun: "TestHelperCopilotNativeConnection",
				WantEnv: "LEAPMUX_TEST_COPILOT_NATIVE",
				Env:     []string{"LEAPMUX_TEST_COPILOT_REJECT_INPUT=" + mode},
			})
			provider, err := startNativeCopilot(t.Context(), agent.Options{
				AgentID: "native-input-rejection", WorkingDir: t.TempDir(), Shell: testutil.TestShell(),
				APITimeout: time.Second,
			}, agent.NewProviderServices(&agenttest.Sink{}))
			require.NoError(t, err)
			t.Cleanup(func() { provider.Stop(); _ = provider.Wait() })
			require.ErrorContains(t, provider.SendInput("Rejected input", nil), "input was rejected")
			require.Equal(t, mode == "after-new-turn", provider.PublishTurnActive().Active)
		})
	}
}

func TestNativeCopilotSessionLifecycle(t *testing.T) {
	requestsPath := filepath.Join(t.TempDir(), "requests.jsonl")
	agenttest.InstallFakeCLI(t, agenttest.FakeCLI{
		Binary: "copilot", HelperRun: "TestHelperCopilotNativeConnection",
		WantEnv: "LEAPMUX_TEST_COPILOT_NATIVE",
		Env:     []string{"LEAPMUX_TEST_COPILOT_SESSION_REQUESTS=" + requestsPath},
	})
	sink := &agenttest.Sink{}
	provider, err := startNativeCopilot(t.Context(), agent.Options{
		AgentID: "native-session", WorkingDir: t.TempDir(), Shell: testutil.TestShell(),
		APITimeout: time.Second,
		Options: optionmap.Map{
			agent.OptionIDModel: "probe-model", agent.OptionIDEffort: "high",
			copilotOptionSessionMode: "plan", agent.OptionIDPermissionMode: "assisted",
		},
	}, agent.NewProviderServices(sink))
	require.NoError(t, err)
	t.Cleanup(func() { provider.Stop(); _ = provider.Wait() })
	a := provider.(*Agent)
	require.NotEmpty(t, sink.LastSessionID())
	require.Equal(t, "plan", a.SettingsSnapshot().SurfacedOptions[copilotOptionSessionMode])
	require.Equal(t, "assisted", a.SettingsSnapshot().SurfacedOptions[agent.OptionIDPermissionMode])
	require.Equal(t, "high", a.SettingsSnapshot().SurfacedOptions[agent.OptionIDEffort])
	require.Equal(t, " {\"jsonrpc\":\"2.0\",\"method\":\"probe.notification\", \"params\":{\"counter\":9007199254740993}} ", string(sink.Messages()[0].Content))
	content := "Keep the input unchanged.\n한글"
	image := []byte{0x89, 0x50, 0x4e, 0x47}
	require.NoError(t, a.SendInput(content, []*leapmuxv1.Attachment{{Filename: "image.png", MimeType: "image/png", Data: image}}))
	require.True(t, a.PublishTurnActive().Active)
	require.ErrorIs(t, a.SendInput("second input", nil), agent.ErrAgentBusy)
	require.NoError(t, a.Interrupt(agent.StopContext{}))
	require.False(t, a.PublishTurnActive().Active)
	before := len(sink.Messages())
	a.HandleOutput([]byte(`{"method":"session.event","params":{"sessionId":"foreign-session","event":{"type":"assistant.turn_start","data":{}}}}`))
	require.Len(t, sink.Messages(), before)
	require.False(t, a.PublishTurnActive().Active)
	oldID := a.currentNativeSessionID()
	newID, err := a.ClearContext()
	require.NoError(t, err)
	require.NotEqual(t, oldID, newID)
	require.Equal(t, "plan", a.SettingsSnapshot().SurfacedOptions[copilotOptionSessionMode])
	require.Equal(t, "assisted", a.SettingsSnapshot().SurfacedOptions[agent.OptionIDPermissionMode])
	file, err := os.Open(requestsPath)
	require.NoError(t, err)
	defer func() { require.NoError(t, file.Close()) }()
	decoder := json.NewDecoder(file)
	var sent []agenttest.RecordedRequest
	var sessionModelReads, globalModelReads int
	for decoder.More() {
		var request agenttest.RecordedRequest
		require.NoError(t, decoder.Decode(&request))
		if request.Method == "session.send" {
			sent = append(sent, request)
		}
		if request.Method == "session.model.list" {
			sessionModelReads++
		}
		if request.Method == "models.list" {
			globalModelReads++
		}
	}
	require.Equal(t, 1, sessionModelReads)
	require.Zero(t, globalModelReads)
	require.Len(t, sent, 1)
	require.Equal(t, content, sent[0].Params["prompt"])
	blob := sent[0].Params["attachments"].([]any)[0].(map[string]any)
	require.Equal(t, "image/png", blob["mimeType"])
	require.Equal(t, base64.StdEncoding.EncodeToString(image), blob["data"])
}

func TestNativeCopilotSteerSendsImmediateMode(t *testing.T) {
	requestsPath := filepath.Join(t.TempDir(), "requests.jsonl")
	agenttest.InstallFakeCLI(t, agenttest.FakeCLI{
		Binary: "copilot", HelperRun: "TestHelperCopilotNativeConnection",
		WantEnv: "LEAPMUX_TEST_COPILOT_NATIVE",
		Env:     []string{"LEAPMUX_TEST_COPILOT_SESSION_REQUESTS=" + requestsPath},
	})
	sink := &agenttest.Sink{}
	provider, err := startNativeCopilot(t.Context(), agent.Options{
		AgentID: "native-steer", WorkingDir: t.TempDir(), Shell: testutil.TestShell(),
		APITimeout: time.Second,
	}, agent.NewProviderServices(sink))
	require.NoError(t, err)
	t.Cleanup(func() { provider.Stop(); _ = provider.Wait() })
	a := provider.(*Agent)
	require.NotEmpty(t, sink.LastSessionID())

	// A steer owns no turn: with nothing running it must refuse rather than
	// start one, the way every other provider's steer refuses.
	require.ErrorIs(t, a.SteerInput("too early", nil), agent.ErrNoActiveTurn)

	require.NoError(t, a.SendInput("first", nil))
	state := a.PublishTurnActive()
	require.True(t, state.Active)
	require.True(t, state.Steerable, "an active Copilot turn must be published steerable")
	require.NoError(t, a.SteerInput("steered", nil))

	file, err := os.Open(requestsPath)
	require.NoError(t, err)
	defer func() { require.NoError(t, file.Close()) }()
	decoder := json.NewDecoder(file)
	var sends []agenttest.RecordedRequest
	for decoder.More() {
		var request agenttest.RecordedRequest
		require.NoError(t, decoder.Decode(&request))
		if request.Method == "session.send" {
			sends = append(sends, request)
		}
	}
	require.Len(t, sends, 2)
	require.NotContains(t, sends[0].Params, "mode", "a plain send must leave the delivery mode to Copilot's default")
	require.Equal(t, "first", sends[0].Params["prompt"])
	require.Equal(t, "immediate", sends[1].Params["mode"])
	require.Equal(t, "steered", sends[1].Params["prompt"])
}

// readRecordedCopilotRequests returns the requests that the fake runtime logged, in order.
func readRecordedCopilotRequests(t *testing.T, path string) []agenttest.RecordedRequest {
	t.Helper()
	file, err := os.Open(path)
	require.NoError(t, err)
	defer func() { require.NoError(t, file.Close()) }()
	decoder := json.NewDecoder(file)
	var requests []agenttest.RecordedRequest
	for decoder.More() {
		var request agenttest.RecordedRequest
		require.NoError(t, decoder.Decode(&request))
		requests = append(requests, request)
	}
	return requests
}

// Copilot 1.0.87 refuses Assisted mode unless the session opens with the
// AUTO_APPROVAL feature flag on. LeapMux offers Assisted and starts a new session in
// it, so every session must ask for the flag. Without it the startup read the
// refusal as an unconfirmed setting and the agent never started.
func TestNativeCopilotAsksForAssistedApprovalWhenItOpensASession(t *testing.T) {
	for _, resume := range []bool{false, true} {
		name := "create"
		if resume {
			name = "resume"
		}
		t.Run(name, func(t *testing.T) {
			requestsPath := filepath.Join(t.TempDir(), "requests.jsonl")
			agenttest.InstallFakeCLI(t, agenttest.FakeCLI{
				Binary: "copilot", HelperRun: "TestHelperCopilotNativeConnection",
				WantEnv: "LEAPMUX_TEST_COPILOT_NATIVE",
				Env:     []string{"LEAPMUX_TEST_COPILOT_REQUIRE_APPROVAL_FLAG=1", "LEAPMUX_TEST_COPILOT_SESSION_REQUESTS=" + requestsPath},
			})
			opts := agent.Options{
				AgentID: "native-approval-flag", WorkingDir: t.TempDir(), Shell: testutil.TestShell(),
				APITimeout: time.Second,
				Options:    optionmap.Map{agent.OptionIDPermissionMode: "assisted"},
			}
			if resume {
				opts.ResumeSessionID = "stored-session"
			}
			sink := &agenttest.Sink{}
			provider, err := startNativeCopilot(t.Context(), opts, agent.NewProviderServices(sink))
			require.NoError(t, err)
			t.Cleanup(func() { provider.Stop(); _ = provider.Wait() })
			require.Equal(t, "assisted", provider.(*Agent).SettingsSnapshot().SurfacedOptions[agent.OptionIDPermissionMode])

			opening := "session.create"
			if resume {
				opening = "session.resume"
			}
			var seen int
			for _, request := range readRecordedCopilotRequests(t, requestsPath) {
				if request.Method != opening {
					continue
				}
				seen++
				require.Equal(t, map[string]any{"AUTO_APPROVAL": true}, request.Params["featureFlags"])
			}
			require.Equal(t, 1, seen)
		})
	}
}

// A context clear opens a replacement session in the same process. The runtime
// resets its permission mode to Manual for that session and gates Assisted again, so
// the replacement must ask for the flag too, and the restored mode must hold.
func TestNativeCopilotKeepsAssistedApprovalAfterAContextClear(t *testing.T) {
	requestsPath := filepath.Join(t.TempDir(), "requests.jsonl")
	agenttest.InstallFakeCLI(t, agenttest.FakeCLI{
		Binary: "copilot", HelperRun: "TestHelperCopilotNativeConnection",
		WantEnv: "LEAPMUX_TEST_COPILOT_NATIVE",
		Env:     []string{"LEAPMUX_TEST_COPILOT_REQUIRE_APPROVAL_FLAG=1", "LEAPMUX_TEST_COPILOT_SESSION_REQUESTS=" + requestsPath},
	})
	provider, err := startNativeCopilot(t.Context(), agent.Options{
		AgentID: "native-approval-clear", WorkingDir: t.TempDir(), Shell: testutil.TestShell(),
		APITimeout: time.Second,
		Options:    optionmap.Map{agent.OptionIDPermissionMode: "assisted"},
	}, agent.NewProviderServices(&agenttest.Sink{}))
	require.NoError(t, err)
	t.Cleanup(func() { provider.Stop(); _ = provider.Wait() })
	a := provider.(*Agent)
	_, err = a.ClearContext()
	require.NoError(t, err)
	require.Equal(t, "assisted", a.SettingsSnapshot().SurfacedOptions[agent.OptionIDPermissionMode])
	var created int
	for _, request := range readRecordedCopilotRequests(t, requestsPath) {
		if request.Method == "session.create" {
			created++
			require.Equal(t, map[string]any{"AUTO_APPROVAL": true}, request.Params["featureFlags"])
		}
	}
	require.Equal(t, 2, created)
}

func TestCopilotNativeSessionConfigAsksForAssistedApproval(t *testing.T) {
	for _, resume := range []bool{false, true} {
		raw, err := json.Marshal(newCopilotSessionConfig(agent.Options{WorkingDir: "/project"}, "session", resume))
		require.NoError(t, err)
		var fields map[string]any
		require.NoError(t, json.Unmarshal(raw, &fields))
		require.Equal(t, map[string]any{"AUTO_APPROVAL": true}, fields["featureFlags"], "resume=%v", resume)
	}
}

func TestCopilotNativeSessionConfig(t *testing.T) {
	for _, resume := range []bool{false, true} {
		// Both sentinels mean "let the runtime choose", and neither word is one the
		// runtime's own catalogue holds. Each must be omitted rather than sent.
		config := newCopilotSessionConfig(agent.Options{WorkingDir: "/project", Options: map[string]string{
			agent.OptionIDModel: agent.DefaultModelSentinel, agent.OptionIDEffort: agent.EffortAuto,
		}}, "session", resume)
		raw, err := json.Marshal(config)
		require.NoError(t, err)
		var fields map[string]any
		require.NoError(t, json.Unmarshal(raw, &fields))
		require.NotContains(t, fields, "model")
		require.NotContains(t, fields, "reasoningEffort")
		if resume {
			require.Equal(t, true, fields["continuePendingWork"])
			require.NotContains(t, fields, "disableResume", "Copilot needs the resume event to recover pending work")
			require.NotContains(t, fields, "suppressResumeEvent")
		} else {
			require.NotContains(t, fields, "continuePendingWork")
			require.NotContains(t, fields, "disableResume")
			require.NotContains(t, fields, "suppressResumeEvent")
		}
	}
}

func TestNativeCopilotClearContextFailureRestoresThePreviousSession(t *testing.T) {
	for _, failure := range []string{"create", "subscription", "restore"} {
		t.Run(failure, func(t *testing.T) {
			agenttest.InstallFakeCLI(t, agenttest.FakeCLI{
				Binary: "copilot", HelperRun: "TestHelperCopilotNativeConnection",
				WantEnv: "LEAPMUX_TEST_COPILOT_NATIVE",
				Env:     []string{"LEAPMUX_TEST_COPILOT_REPLACEMENT_FAILURE=" + failure},
			})
			sink := &agenttest.Sink{}
			provider, err := startNativeCopilot(t.Context(), agent.Options{
				AgentID: "native-replacement", WorkingDir: t.TempDir(), Shell: testutil.TestShell(),
				APITimeout: time.Second,
				Options:    optionmap.Map{copilotOptionSessionMode: "plan", agent.OptionIDPermissionMode: "assisted"},
			}, agent.NewProviderServices(sink))
			require.NoError(t, err)
			t.Cleanup(func() { provider.Stop(); _ = provider.Wait() })
			a := provider.(*Agent)
			oldID := sink.LastSessionID()
			newID, err := a.ClearContext()
			require.Error(t, err)
			require.Empty(t, newID)
			require.Equal(t, oldID, sink.LastSessionID(), "a failed replacement must retain the persisted session identity")
			require.Equal(t, oldID, a.currentNativeSessionID())
			if failure == "restore" {
				require.ErrorContains(t, err, "session could not open")
				require.True(t, a.IsStopped(), "a failed restoration must not accept input")
				return
			}
			require.NoError(t, a.SendInput("Use the restored session.", nil))
			require.Equal(t, "plan", a.SettingsSnapshot().SurfacedOptions[copilotOptionSessionMode])
			require.Equal(t, "assisted", a.SettingsSnapshot().SurfacedOptions[agent.OptionIDPermissionMode])
		})
	}
}

func TestCopilotNativeSessionRejectsAnotherIdentity(t *testing.T) {
	agenttest.InstallFakeCLI(t, agenttest.FakeCLI{
		Binary: "copilot", HelperRun: "TestHelperCopilotNativeConnection",
		WantEnv: "LEAPMUX_TEST_COPILOT_NATIVE",
		Env:     []string{"LEAPMUX_TEST_COPILOT_SESSION_RETURN_ID=another-session"},
	})
	opts := agent.Options{AgentID: "native-identity", WorkingDir: t.TempDir(), Shell: testutil.TestShell()}
	connection, err := startCopilotConnectionForTest(t, opts, func(*providerkit.ParsedLine) {})
	require.NoError(t, err)
	t.Cleanup(func() { connection.Stop(); _ = connection.Wait() })
	for _, resume := range []bool{false, true} {
		_, err := connection.openSession(opts, "expected-session", resume, time.Second)
		require.ErrorContains(t, err, "different session ID")
	}
}

func TestCopilotNativeSessionRejectsAnEmptyIdentity(t *testing.T) {
	connection := &copilotConnection{}
	_, err := connection.openSession(agent.Options{}, "", false, time.Second)
	require.ErrorContains(t, err, "session ID is empty")
}

func TestCopilotNativeSessionReportsReasoningSummaryOptionFailure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		resume bool
	}{{name: "create"}, {name: "resume", resume: true}} {
		t.Run(tc.name, func(t *testing.T) {
			agenttest.InstallFakeCLI(t, agenttest.FakeCLI{
				Binary: "copilot", HelperRun: "TestHelperCopilotNativeConnection",
				WantEnv: "LEAPMUX_TEST_COPILOT_NATIVE",
				Env:     []string{"LEAPMUX_TEST_COPILOT_REJECT_REASONING_SUMMARIES=1"},
			})
			opts := agent.Options{AgentID: "native-summary", WorkingDir: t.TempDir(), Shell: testutil.TestShell()}
			connection, err := startCopilotConnectionForTest(t, opts, func(*providerkit.ParsedLine) {})
			require.NoError(t, err)
			t.Cleanup(func() { connection.Stop(); _ = connection.Wait() })
			_, err = connection.openSession(opts, "expected-session", tc.resume, time.Second)
			require.ErrorContains(t, err, "enable Copilot reasoning summaries")
			var nativeError *providerkit.JSONRPCResponseError
			require.ErrorAs(t, err, &nativeError)
			require.ErrorContains(t, nativeError, "Reasoning summaries refused")
		})
	}
}

func TestCopilotNativeSessionRequestsReasoningSummaryOnCreateAndResume(t *testing.T) {
	requestsPath := filepath.Join(t.TempDir(), "requests.jsonl")
	agenttest.InstallFakeCLI(t, agenttest.FakeCLI{
		Binary: "copilot", HelperRun: "TestHelperCopilotNativeConnection",
		WantEnv: "LEAPMUX_TEST_COPILOT_NATIVE",
		Env:     []string{"LEAPMUX_TEST_COPILOT_SESSION_REQUESTS=" + requestsPath},
	})
	opts := agent.Options{AgentID: "native-summary", WorkingDir: t.TempDir(), Shell: testutil.TestShell()}
	connection, err := startCopilotConnectionForTest(t, opts, func(*providerkit.ParsedLine) {})
	require.NoError(t, err)
	t.Cleanup(func() { connection.Stop(); _ = connection.Wait() })
	for _, resume := range []bool{false, true} {
		_, err := connection.openSession(opts, "native-summary-session", resume, time.Second)
		require.NoError(t, err)
	}

	file, err := os.Open(requestsPath)
	require.NoError(t, err)
	defer func() { require.NoError(t, file.Close()) }()
	decoder := json.NewDecoder(file)
	openCalls := 0
	for decoder.More() {
		var request struct {
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		require.NoError(t, decoder.Decode(&request))
		if request.Method != "session.create" && request.Method != "session.resume" {
			continue
		}
		openCalls++
		require.Equal(t, "detailed", request.Params["reasoningSummary"], request.Method)
	}
	require.Equal(t, 2, openCalls)
}

func TestCopilotUsesNativeSessionProtocol(t *testing.T) {
	requestsPath := filepath.Join(t.TempDir(), "requests.jsonl")
	argsPath := filepath.Join(t.TempDir(), "args")
	agenttest.InstallFakeCLI(t, agenttest.FakeCLI{
		Binary: "copilot", HelperRun: "TestHelperCopilotNativeConnection",
		WantEnv: "LEAPMUX_TEST_COPILOT_NATIVE", ArgsFile: argsPath,
		Env: []string{"LEAPMUX_TEST_COPILOT_SESSION_REQUESTS=" + requestsPath},
	})
	workingDir := t.TempDir()
	sink := &agenttest.Sink{}
	provider, err := Start(t.Context(), agent.Options{
		AgentID: "native-session", AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT,
		WorkingDir: workingDir, Shell: testutil.TestShell(),
		APITimeout: time.Second,
		Options:    map[string]string{agent.OptionIDModel: "probe-model", agent.OptionIDEffort: "high"},
	}, agent.NewProviderServices(sink))
	require.NoError(t, err)
	t.Cleanup(func() { provider.Stop(); _ = provider.Wait() })
	args, err := os.ReadFile(argsPath)
	require.NoError(t, err)
	require.Contains(t, string(args), "--server --stdio")
	require.NotContains(t, string(args), "--acp")
	file, err := os.Open(requestsPath)
	require.NoError(t, err)
	defer func() { require.NoError(t, file.Close()) }()
	decoder := json.NewDecoder(file)
	var created map[string]any
	var summaryOptions map[string]any
	createdIndex, summaryIndex := -1, -1
	requestIndex := 0
	for decoder.More() {
		var request struct {
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		require.NoError(t, decoder.Decode(&request))
		if request.Method == "session.create" {
			created = request.Params
			createdIndex = requestIndex
		}
		if request.Method == "session.options.update" && request.Params["enableReasoningSummaries"] == true {
			summaryOptions = request.Params
			summaryIndex = requestIndex
		}
		requestIndex++
	}
	require.NotNil(t, created)
	require.NotNil(t, summaryOptions, "the runtime must surface reasoning summary events")
	require.Greater(t, summaryIndex, createdIndex, "the summary option needs an open session")
	require.Equal(t, created["sessionId"], summaryOptions["sessionId"])
	require.Equal(t, workingDir, created["workingDirectory"])
	require.Equal(t, "probe-model", created["model"])
	require.Equal(t, "high", created["reasoningEffort"])
	for _, field := range []string{
		"enableConfigDiscovery", "enableSkills", "enableSessionStore", "requestExtensions",
		"requestPermission", "requestElicitation",
		"streaming", "includeSubAgentStreamingEvents",
	} {
		require.Equal(t, true, created[field], field)
	}
	for _, field := range []string{"requestUserInput", "requestExitPlanMode"} {
		require.Equal(t, false, created[field], field)
	}
	for _, field := range []string{"availableTools", "excludedTools", "mcpServers"} {
		require.NotContains(t, created, field, "session creation must retain the configured tool set")
	}
}

// permissionFallbackLog is the part of the warning that the startup writes once for a
// default permission mode that the runtime refused.
const permissionFallbackLog = "refused the default permission mode"

// installCopilotThatRefusesAMode installs the fake runtime with a policy that refuses
// `mode`, from the opened session that `fromOpen` counts (the first one is 1). The
// runtime refuses even when the session sends the AUTO_APPROVAL flag. An empty `mode`
// refuses no mode. It returns the path of the log that records each request.
func installCopilotThatRefusesAMode(t *testing.T, mode string, fromOpen int, env ...string) string {
	t.Helper()
	requestsPath := filepath.Join(t.TempDir(), "requests.jsonl")
	agenttest.InstallFakeCLI(t, agenttest.FakeCLI{
		Binary: "copilot", HelperRun: "TestHelperCopilotNativeConnection",
		WantEnv: "LEAPMUX_TEST_COPILOT_NATIVE",
		Env: append([]string{
			"LEAPMUX_TEST_COPILOT_SESSION_REQUESTS=" + requestsPath,
			"LEAPMUX_TEST_COPILOT_REFUSE_MODE=" + mode,
			"LEAPMUX_TEST_COPILOT_REFUSE_FROM_OPEN=" + strconv.Itoa(fromOpen),
		}, env...),
	})
	return requestsPath
}

// copilotLaunchWithPermissionMode returns launch options that ask for `mode`.
// `defaulted` states that LeapMux chose the mode as the safe default for a new session.
// Otherwise the user chose it, and the option map carries no such record.
func copilotLaunchWithPermissionMode(t *testing.T, mode string, defaulted bool) agent.Options {
	t.Helper()
	opts := agent.Options{
		AgentID: "native-permission-fallback", WorkingDir: t.TempDir(), Shell: testutil.TestShell(),
		APITimeout: 10 * time.Second,
		Options:    optionmap.Map{agent.OptionIDPermissionMode: mode},
	}
	if defaulted {
		opts.NewSessionDefaultOptionIDs = map[string]bool{agent.OptionIDPermissionMode: true}
	}
	return opts
}

// recordedPermissionModes lists, in order, the modes that LeapMux sent to
// session.permissions.setMode.
func recordedPermissionModes(t *testing.T, requestsPath string) []string {
	t.Helper()
	var modes []string
	for _, request := range readRecordedCopilotRequests(t, requestsPath) {
		if request.Method != "session.permissions.setMode" {
			continue
		}
		mode, ok := request.Params["mode"].(string)
		require.True(t, ok, "the permission mode request carries a mode word")
		modes = append(modes, mode)
	}
	return modes
}

// A policy can block Assisted although the session sends AUTO_APPROVAL. The runtime's own
// `defaultPermissionMode` setting says that Assisted is "ignored when it is off or policy
// blocks auto-approval". LeapMux chose Assisted for the user as the default of a new
// session, so a runtime that refuses it must not stop the agent. The agent keeps running
// in Manual, the safe mode, and its settings state Manual.
func TestNativeCopilotFallsBackToManualWhenTheRuntimeRefusesTheDefaultPermissionMode(t *testing.T) {
	for _, tc := range []struct {
		name    string
		resume  bool
		refusal string
	}{
		{name: "create", refusal: "result"},
		{name: "resume", resume: true, refusal: "result"},
		{name: "create with an error reply", refusal: "error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs := testutil.CaptureDefaultLogger(t)
			requestsPath := installCopilotThatRefusesAMode(t, contracts.CopilotPermissionModeAssisted, 1,
				"LEAPMUX_TEST_COPILOT_REFUSAL="+tc.refusal)
			opts := copilotLaunchWithPermissionMode(t, contracts.CopilotPermissionModeAssisted, true)
			if tc.resume {
				opts.ResumeSessionID = "stored-session"
			}
			sink := &agenttest.Sink{}
			provider, err := startNativeCopilot(t.Context(), opts, agent.NewProviderServices(sink))
			require.NoError(t, err, "a refused default must not fail the startup")
			t.Cleanup(func() { provider.Stop(); _ = provider.Wait() })

			snapshot := provider.(*Agent).SettingsSnapshot()
			require.Equal(t, contracts.CopilotPermissionModeManual, snapshot.SurfacedOptions[agent.OptionIDPermissionMode])
			require.Equal(t, contracts.CopilotPermissionModeManual, snapshot.ConfirmedOptions()[agent.OptionIDPermissionMode],
				"the Manager persists the confirmed options, so the row must state Manual")
			require.Equal(t, contracts.CopilotPermissionModeManual, sink.LastSettingsRefresh().PermissionMode)
			require.Equal(t, []string{contracts.CopilotPermissionModeAssisted, contracts.CopilotPermissionModeManual},
				recordedPermissionModes(t, requestsPath), "the agent asks for the default once, then for the safe mode")
			require.Equal(t, 1, strings.Count(logs.String(), permissionFallbackLog), "the fallback writes one warning")
			require.Contains(t, logs.String(), "level=WARN")
			require.NoError(t, provider.SendInput("The agent still takes input.", nil))
		})
	}
}

// A context clear opens a replacement session in the same process. A runtime that
// refused Assisted for the replacement only is the same refusal as at startup, and it
// must not fail the clear. The rollback to the previous session would meet the same
// refusal and stop the agent for good.
func TestNativeCopilotClearContextFallsBackToManualWhenTheReplacementRefusesTheDefaultPermissionMode(t *testing.T) {
	logs := testutil.CaptureDefaultLogger(t)
	requestsPath := installCopilotThatRefusesAMode(t, contracts.CopilotPermissionModeAssisted, 2)
	sink := &agenttest.Sink{}
	provider, err := startNativeCopilot(t.Context(),
		copilotLaunchWithPermissionMode(t, contracts.CopilotPermissionModeAssisted, true), agent.NewProviderServices(sink))
	require.NoError(t, err)
	t.Cleanup(func() { provider.Stop(); _ = provider.Wait() })
	a := provider.(*Agent)
	require.Equal(t, contracts.CopilotPermissionModeAssisted, a.SettingsSnapshot().SurfacedOptions[agent.OptionIDPermissionMode],
		"the first session accepts the default")

	_, err = a.ClearContext()
	require.NoError(t, err, "a refused default must not fail the context clear")

	require.False(t, a.IsStopped())
	require.Equal(t, contracts.CopilotPermissionModeManual, a.SettingsSnapshot().SurfacedOptions[agent.OptionIDPermissionMode])
	require.Equal(t, contracts.CopilotPermissionModeManual, sink.LastSettingsRefresh().PermissionMode)
	require.Equal(t, []string{
		contracts.CopilotPermissionModeAssisted,
		contracts.CopilotPermissionModeAssisted, contracts.CopilotPermissionModeManual,
	}, recordedPermissionModes(t, requestsPath))
	require.Equal(t, 1, strings.Count(logs.String(), permissionFallbackLog))
	require.NoError(t, a.SendInput("The replacement session takes input.", nil))
}

// A goal clear closes the session and opens it again under the same identity. It
// restores the settings of the session in the same way as a context clear does.
func TestNativeCopilotGoalClearFallsBackToManualWhenTheReopenedSessionRefusesTheDefaultPermissionMode(t *testing.T) {
	logs := testutil.CaptureDefaultLogger(t)
	requestsPath := installCopilotThatRefusesAMode(t, contracts.CopilotPermissionModeAssisted, 2)
	sink := &agenttest.Sink{}
	provider, err := startNativeCopilot(t.Context(),
		copilotLaunchWithPermissionMode(t, contracts.CopilotPermissionModeAssisted, true), agent.NewProviderServices(sink))
	require.NoError(t, err)
	t.Cleanup(func() { provider.Stop(); _ = provider.Wait() })
	a := provider.(*Agent)
	_, err = a.PerformGoalAction(agent.GoalActionSet, "Remove this objective")
	require.NoError(t, err)

	_, err = a.PerformGoalAction(agent.GoalActionClear, "")
	require.NoError(t, err, "a refused default must not fail the goal clear")

	require.False(t, a.IsStopped())
	require.Positive(t, sink.GoalClears())
	require.Equal(t, contracts.CopilotPermissionModeManual, a.SettingsSnapshot().SurfacedOptions[agent.OptionIDPermissionMode])
	require.Equal(t, []string{
		contracts.CopilotPermissionModeAssisted,
		contracts.CopilotPermissionModeAssisted, contracts.CopilotPermissionModeManual,
	}, recordedPermissionModes(t, requestsPath))
	require.Equal(t, 1, strings.Count(logs.String(), permissionFallbackLog))
	require.NoError(t, a.SendInput("The reopened session takes input.", nil))
}

// A mode that the user chose is a different case. Manual would ignore that choice, so the
// startup fails with the existing error, and the agent sends no request for Manual.
func TestNativeCopilotStillFailsStartupWhenTheRuntimeRefusesAPermissionModeThatTheUserChose(t *testing.T) {
	for _, tc := range []struct {
		name      string
		resume    bool
		defaulted map[string]bool
	}{
		{name: "create"},
		{name: "resume", resume: true},
		{name: "a default of another option", defaulted: map[string]bool{agent.OptionIDModel: true}},
		{name: "a default that the record marks false", defaulted: map[string]bool{agent.OptionIDPermissionMode: false}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs := testutil.CaptureDefaultLogger(t)
			requestsPath := installCopilotThatRefusesAMode(t, contracts.CopilotPermissionModeAssisted, 1)
			opts := copilotLaunchWithPermissionMode(t, contracts.CopilotPermissionModeAssisted, false)
			opts.NewSessionDefaultOptionIDs = tc.defaulted
			if tc.resume {
				opts.ResumeSessionID = "stored-session"
			}
			_, err := startNativeCopilot(t.Context(), opts, agent.NewProviderServices(&agenttest.Sink{}))
			require.ErrorContains(t, err, "the Copilot runtime did not confirm the requested permissionMode setting")
			require.Equal(t, []string{contracts.CopilotPermissionModeAssisted}, recordedPermissionModes(t, requestsPath),
				"a refused choice of the user never reaches the safe mode")
			require.NotContains(t, logs.String(), permissionFallbackLog)
		})
	}
}

// The runtime setting `defaultPermissionMode` can start a session in a mode that is wider
// than Manual. The agent must not keep the mode that the runtime reports after the
// refusal, because that mode is a choice that nobody made for this agent. It asks for
// Manual in a request of its own.
func TestNativeCopilotFallbackAsksForManualWhenTheRefusingSessionStartedInAnotherMode(t *testing.T) {
	requestsPath := installCopilotThatRefusesAMode(t, contracts.CopilotPermissionModeAssisted, 1,
		"LEAPMUX_TEST_COPILOT_INITIAL_PERMISSION_MODE="+contracts.CopilotPermissionModeAllowAll)
	provider, err := startNativeCopilot(t.Context(),
		copilotLaunchWithPermissionMode(t, contracts.CopilotPermissionModeAssisted, true), agent.NewProviderServices(&agenttest.Sink{}))
	require.NoError(t, err)
	t.Cleanup(func() { provider.Stop(); _ = provider.Wait() })
	require.Equal(t, contracts.CopilotPermissionModeManual,
		provider.(*Agent).SettingsSnapshot().SurfacedOptions[agent.OptionIDPermissionMode])
	require.Equal(t, []string{contracts.CopilotPermissionModeAssisted, contracts.CopilotPermissionModeManual},
		recordedPermissionModes(t, requestsPath))
}

// The fallback needs the runtime to confirm Manual. A runtime that refuses that mode too
// leaves the agent in a mode that nobody chose, so the startup fails with the existing error.
func TestNativeCopilotFailsStartupWhenTheRuntimeRefusesTheSafeModeToo(t *testing.T) {
	requestsPath := installCopilotThatRefusesAMode(t,
		contracts.CopilotPermissionModeAssisted+","+contracts.CopilotPermissionModeManual, 1,
		"LEAPMUX_TEST_COPILOT_INITIAL_PERMISSION_MODE="+contracts.CopilotPermissionModeAllowAll)
	_, err := startNativeCopilot(t.Context(),
		copilotLaunchWithPermissionMode(t, contracts.CopilotPermissionModeAssisted, true), agent.NewProviderServices(&agenttest.Sink{}))
	require.ErrorContains(t, err, "the Copilot runtime did not confirm the requested permissionMode setting")
	require.Equal(t, []string{contracts.CopilotPermissionModeAssisted, contracts.CopilotPermissionModeManual},
		recordedPermissionModes(t, requestsPath))
}

// A failed read of the permission mode shows no refusal. The agent cannot tell that the
// runtime refused the default, so it must not warn and must not change the mode.
func TestNativeCopilotKeepsTheDefaultRequestWhenThePermissionModeReadFails(t *testing.T) {
	logs := testutil.CaptureDefaultLogger(t)
	requestsPath := installCopilotThatRefusesAMode(t, "", 1, "LEAPMUX_TEST_COPILOT_FAIL_PERMISSION_READS_AFTER=1")
	_, err := startNativeCopilot(t.Context(),
		copilotLaunchWithPermissionMode(t, contracts.CopilotPermissionModeAssisted, true), agent.NewProviderServices(&agenttest.Sink{}))
	require.ErrorContains(t, err, "the Copilot runtime did not confirm the requested permissionMode setting")
	require.Equal(t, []string{contracts.CopilotPermissionModeAssisted}, recordedPermissionModes(t, requestsPath))
	require.NotContains(t, logs.String(), permissionFallbackLog)
}

// A runtime that accepts the default needs no fallback. The agent sends the default once
// and writes no warning, at startup and at a context clear.
func TestNativeCopilotDoesNotFallBackWhenTheRuntimeAcceptsTheDefaultPermissionMode(t *testing.T) {
	for _, resume := range []bool{false, true} {
		name := "create"
		if resume {
			name = "resume"
		}
		t.Run(name, func(t *testing.T) {
			logs := testutil.CaptureDefaultLogger(t)
			requestsPath := installCopilotThatRefusesAMode(t, "", 1, "LEAPMUX_TEST_COPILOT_REQUIRE_APPROVAL_FLAG=1")
			opts := copilotLaunchWithPermissionMode(t, contracts.CopilotPermissionModeAssisted, true)
			if resume {
				opts.ResumeSessionID = "stored-session"
			}
			provider, err := startNativeCopilot(t.Context(), opts, agent.NewProviderServices(&agenttest.Sink{}))
			require.NoError(t, err)
			t.Cleanup(func() { provider.Stop(); _ = provider.Wait() })
			a := provider.(*Agent)
			require.Equal(t, contracts.CopilotPermissionModeAssisted, a.SettingsSnapshot().SurfacedOptions[agent.OptionIDPermissionMode])
			require.Equal(t, []string{contracts.CopilotPermissionModeAssisted}, recordedPermissionModes(t, requestsPath))

			_, err = a.ClearContext()
			require.NoError(t, err)
			require.Equal(t, contracts.CopilotPermissionModeAssisted, a.SettingsSnapshot().SurfacedOptions[agent.OptionIDPermissionMode])
			require.Equal(t, []string{contracts.CopilotPermissionModeAssisted, contracts.CopilotPermissionModeAssisted},
				recordedPermissionModes(t, requestsPath))
			require.NotContains(t, logs.String(), permissionFallbackLog)
		})
	}
}

// The safe mode that replaced a refused default is the confirmed state of the session.
// A later context clear restores Manual, and the agent neither asks for Assisted again
// nor writes a second warning.
func TestNativeCopilotClearContextRestoresTheSafeModeThatReplacedARefusedDefault(t *testing.T) {
	logs := testutil.CaptureDefaultLogger(t)
	requestsPath := installCopilotThatRefusesAMode(t, contracts.CopilotPermissionModeAssisted, 1)
	provider, err := startNativeCopilot(t.Context(),
		copilotLaunchWithPermissionMode(t, contracts.CopilotPermissionModeAssisted, true), agent.NewProviderServices(&agenttest.Sink{}))
	require.NoError(t, err)
	t.Cleanup(func() { provider.Stop(); _ = provider.Wait() })
	a := provider.(*Agent)

	_, err = a.ClearContext()
	require.NoError(t, err)

	require.Equal(t, contracts.CopilotPermissionModeManual, a.SettingsSnapshot().SurfacedOptions[agent.OptionIDPermissionMode])
	require.Equal(t, []string{
		contracts.CopilotPermissionModeAssisted, contracts.CopilotPermissionModeManual,
		contracts.CopilotPermissionModeManual,
	}, recordedPermissionModes(t, requestsPath))
	require.Equal(t, 1, strings.Count(logs.String(), permissionFallbackLog))
}

// A replacement session that refuses a mode that the user chose fails the clear. The
// user chose the mode at launch, or changed to it afterwards. The agent then sends no
// request for Manual: it would replace the user's choice.
func TestNativeCopilotClearContextStillFailsWhenTheReplacementRefusesAPermissionModeThatTheUserChose(t *testing.T) {
	for _, tc := range []struct {
		name string
		// launch is the mode at launch, and defaulted states that LeapMux chose it.
		launch    string
		defaulted bool
		// later is the mode that the user selects after the launch. It is empty when the
		// user selects none.
		later string
	}{
		{name: "chosen at launch", launch: contracts.CopilotPermissionModeAssisted},
		{name: "changed after launch", launch: contracts.CopilotPermissionModeAssisted, defaulted: true, later: contracts.CopilotPermissionModeAllowAll},
	} {
		t.Run(tc.name, func(t *testing.T) {
			refused := tc.launch
			if tc.later != "" {
				refused = tc.later
			}
			logs := testutil.CaptureDefaultLogger(t)
			requestsPath := installCopilotThatRefusesAMode(t, refused, 2)
			provider, err := startNativeCopilot(t.Context(),
				copilotLaunchWithPermissionMode(t, tc.launch, tc.defaulted), agent.NewProviderServices(&agenttest.Sink{}))
			require.NoError(t, err)
			t.Cleanup(func() { provider.Stop(); _ = provider.Wait() })
			a := provider.(*Agent)
			if tc.later != "" {
				changed := a.UpdateSettings(optionmap.Map{agent.OptionIDPermissionMode: tc.later})
				require.Equal(t, agent.OptionSettlementConfirmed, changed.Settlements[agent.OptionIDPermissionMode].State)
			}

			newID, err := a.ClearContext()
			require.ErrorContains(t, err, "the Copilot runtime did not restore the permissionMode setting")
			require.Empty(t, newID)
			require.NotContains(t, recordedPermissionModes(t, requestsPath), contracts.CopilotPermissionModeManual)
			require.NotContains(t, logs.String(), permissionFallbackLog)
		})
	}
}

func TestUnchangedDefaultOptionIDs(t *testing.T) {
	launch := agent.Options{
		Options: optionmap.Map{
			agent.OptionIDPermissionMode: contracts.CopilotPermissionModeAssisted,
			agent.OptionIDModel:          "probe-model",
		},
		NewSessionDefaultOptionIDs: map[string]bool{agent.OptionIDPermissionMode: true},
	}
	for _, tc := range []struct {
		name    string
		launch  agent.Options
		current optionmap.Map
		want    map[string]bool
	}{
		{
			name: "keeps a default whose value did not change", launch: launch,
			current: optionmap.Map{agent.OptionIDPermissionMode: contracts.CopilotPermissionModeAssisted, agent.OptionIDModel: "other-model"},
			want:    map[string]bool{agent.OptionIDPermissionMode: true},
		},
		{
			name: "drops a default whose value changed", launch: launch,
			current: optionmap.Map{agent.OptionIDPermissionMode: contracts.CopilotPermissionModeManual},
			want:    map[string]bool{},
		},
		{
			name: "drops a default that the record marks false",
			launch: agent.Options{
				Options:                    launch.Options,
				NewSessionDefaultOptionIDs: map[string]bool{agent.OptionIDPermissionMode: false},
			},
			current: launch.Options,
			want:    map[string]bool{},
		},
		{
			name:    "returns an empty set when LeapMux chose no value",
			launch:  agent.Options{Options: launch.Options},
			current: launch.Options,
			want:    map[string]bool{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, unchangedDefaultOptionIDs(tc.launch, tc.current))
		})
	}
}
