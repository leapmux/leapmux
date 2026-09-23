//go:build unix

package copilot

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

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
	require.NoError(t, a.Interrupt())
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
	for decoder.More() {
		var request struct {
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		require.NoError(t, decoder.Decode(&request))
		if request.Method == "session.create" {
			created = request.Params
		}
	}
	require.NotNil(t, created)
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
