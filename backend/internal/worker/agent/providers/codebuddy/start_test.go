package codebuddy

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/optionids"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/leapmux/leapmux/internal/worker/terminal"
)

const codebuddyStartupHelperEnv = "LEAPMUX_CODEBUDDY_STARTUP_HELPER"

func TestCodebuddyStartupHelper(t *testing.T) {
	scenario := os.Getenv(codebuddyStartupHelperEnv)
	if scenario == "" {
		return
	}
	writeFrame := func(frame string) {
		if _, err := fmt.Fprintln(os.Stdout, frame); err != nil {
			os.Exit(25)
		}
	}
	readSettingsArgument := func() map[string]json.RawMessage {
		var settings map[string]json.RawMessage
		for index, argument := range os.Args {
			if argument == "--settings" {
				if settings != nil || index+1 == len(os.Args) {
					os.Exit(24)
				}
				if err := json.Unmarshal([]byte(os.Args[index+1]), &settings); err != nil || settings == nil {
					os.Exit(24)
				}
			}
		}
		return settings
	}
	if scenario == "explicit-style" || scenario == "resume-style" {
		settings := readSettingsArgument()
		var style string
		if json.Unmarshal(settings["outputStyle"], &style) != nil || len(settings) != 1 || style != "Concise" {
			fmt.Fprintln(os.Stderr, "Expected the native outputStyle startup override.")
			os.Exit(24)
		}
	}
	if strings.HasPrefix(scenario, "thinking-") {
		settings := readSettingsArgument()
		if scenario == "thinking-auto" {
			if settings != nil {
				os.Exit(24)
			}
		} else {
			wantFields := 1
			if scenario == "thinking-style" || scenario == "thinking-auto-style" {
				var style string
				if json.Unmarshal(settings["outputStyle"], &style) != nil || style != "Concise" {
					os.Exit(24)
				}
				wantFields++
			}
			if scenario == "thinking-auto-style" {
				wantFields--
				if _, present := settings["alwaysThinkingEnabled"]; present {
					os.Exit(24)
				}
			} else {
				var enabled *bool
				if json.Unmarshal(settings["alwaysThinkingEnabled"], &enabled) != nil || enabled == nil || *enabled != (scenario == "thinking-on") {
					os.Exit(24)
				}
			}
			if len(settings) != wantFields {
				os.Exit(24)
			}
		}
	}
	scanner := bufio.NewScanner(os.Stdin)
	for _, subtype := range []string{"initialize", "get_available_models"} {
		if !scanner.Scan() {
			os.Exit(24)
		}
		var request struct {
			Type      string `json:"type"`
			RequestID string `json:"request_id"`
			Request   struct {
				Subtype      string          `json:"subtype"`
				Capabilities map[string]bool `json:"capabilities"`
			} `json:"request"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil || request.Type != "control_request" ||
			request.RequestID == "" || request.Request.Subtype != subtype {
			fmt.Fprintln(os.Stderr, "Expected native startup request:", subtype)
			os.Exit(24)
		}
		if subtype == "initialize" && (len(request.Request.Capabilities) != 1 || !request.Request.Capabilities["askUserQuestion"]) {
			fmt.Fprintln(os.Stderr, "Expected only the native question capability.")
			os.Exit(24)
		}
		if scenario == subtype+"-exit" {
			fmt.Fprintln(os.Stderr, "Controlled native startup failure:", subtype)
			os.Exit(23)
		}
		answer := codebuddyControlAnswer(`{"availableModels":[{"modelId":"custom-local:test","name":"Test"}]}`)(request.RequestID)
		if subtype == "initialize" {
			switch scenario {
			case "initialize-refusal":
				answer = codebuddyControlFailure("Controlled initialization refusal.")(request.RequestID)
			case "initialize-malformed":
				answer = codebuddyControlAnswer(`"invalid payload"`)(request.RequestID)
			case "native-style":
				answer = codebuddyControlAnswer(`{"output_style":"Explanatory","available_output_styles":["Default","Concise","Explanatory"]}`)(request.RequestID)
			case "explicit-style", "resume-style", "thinking-style", "thinking-auto-style":
				answer = codebuddyControlAnswer(`{"output_style":"Concise","available_output_styles":["Default","Concise","Explanatory"]}`)(request.RequestID)
			default:
				answer = codebuddyControlAnswer(`{"commands":[],"output_style":"default","available_output_styles":[],"models":[],"account":null,"currentModelId":"custom-local:test"}`)(request.RequestID)
			}
		}
		if subtype == "get_available_models" && strings.HasPrefix(scenario, "resume") {
			if scenario != "resume-failure" {
				writeFrame(`{"type":"system","subtype":"init","session_id":"existing-session","model":"unknown","permissionMode":"plan","tools":[],"mcp_servers":[]}`)
				writeFrame(`{"type":"assistant","session_id":"existing-session","message":{"content":[{"type":"text","text":"Historical answer."}]}}`)
				writeFrame(`{"type":"result","session_id":"existing-session","result":"","is_error":false}`)
			} else {
				writeFrame(`{"type":"result","session_id":"existing-session","is_error":true,"errors":["Controlled resume refusal."]}`)
			}
		}
		writeFrame(answer)
	}
	// Keep the process alive until the owner closes its input or stops it.
	for scanner.Scan() {
	}
	os.Exit(0)
}

func TestCodebuddyStartupHandshake(t *testing.T) {
	for _, tc := range []struct {
		name      string
		scenario  string
		resume    string
		failure   string
		style     string
		wantStyle string
		thinking  string
	}{
		{name: "fresh session", scenario: "success"},
		{name: "resumed session", scenario: "resume", resume: "existing-session"},
		{name: "configured native style", scenario: "native-style", wantStyle: "Explanatory"},
		{name: "explicit style", scenario: "explicit-style", style: "Concise", wantStyle: "Concise"},
		{name: "resumed style", scenario: "resume-style", resume: "existing-session", style: "Concise", wantStyle: "Concise"},
		{name: "thinking on", scenario: "thinking-on", thinking: "true"},
		{name: "thinking off", scenario: "thinking-off", thinking: "false"},
		{name: "thinking auto", scenario: "thinking-auto", thinking: "auto"},
		{name: "thinking and style", scenario: "thinking-style", thinking: "false", style: "Concise", wantStyle: "Concise"},
		{name: "thinking auto keeps style", scenario: "thinking-auto-style", thinking: "auto", style: "Concise", wantStyle: "Concise"},
		{name: "resume refusal", scenario: "resume-failure", resume: "existing-session", failure: "Controlled resume refusal."},
		{name: "initialization refusal", scenario: "initialize-refusal", failure: "Controlled initialization refusal."},
		{name: "malformed initialization", scenario: "initialize-malformed", failure: "CodeBuddy sent an invalid control response."},
		{name: "initialization exit", scenario: "initialize-exit", failure: "Controlled native startup failure: initialize"},
		{name: "catalog exit", scenario: "get_available_models-exit", failure: "Controlled native startup failure: get_available_models"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The launch locator is global, so this controlled process test stays serial.
			executable, err := os.Executable()
			require.NoError(t, err)
			previous := codebuddyLocator
			codebuddyLocator = launch.Custom(func(context.Context, string, bool) (launch.Spec, launch.Resolution) {
				return launch.Spec{
					Program:    executable,
					PrefixArgs: []string{"-test.run=^TestCodebuddyStartupHelper$", "--"},
					Env:        []string{codebuddyStartupHelperEnv + "=" + tc.scenario},
				}, launch.Found
			})
			t.Cleanup(func() { codebuddyLocator = previous })
			sink := &agenttest.Sink{}
			home := t.TempDir()
			options := map[string]string{}
			if tc.style != "" {
				options["outputStyle"] = tc.style
			}
			if tc.thinking != "" {
				options["thinking"] = tc.thinking
			}
			started, err := Start(t.Context(), agent.Options{
				Options:         options,
				ResumeSessionID: tc.resume, AgentID: "native-startup-test", WorkingDir: t.TempDir(), HomeDir: home,
				Shell: terminal.ResolveDefaultShell(), ExtraEnv: []string{"HOME=" + home, "USERPROFILE=" + home, "ZDOTDIR=" + home},
			}, agent.NewProviderServices(sink))
			if started != nil {
				t.Cleanup(started.Stop)
			}
			if tc.failure != "" {
				require.Error(t, err)
				assert.Nil(t, started)
				assert.Contains(t, err.Error(), tc.failure)
				if strings.HasSuffix(tc.scenario, "-exit") {
					assert.Contains(t, err.Error(), "code 23")
				}
				assert.Empty(t, sink.SessionIDs(), "a failed startup must not publish its provisional native session")
				return
			}
			require.NoError(t, err)
			require.NotNil(t, started)
			require.Len(t, sink.SessionIDs(), 1)
			assert.Empty(t, sink.Messages(), "native history must not duplicate the restored Worker transcript")
			assert.NotEqual(t, "unknown", started.(*Agent).SettingsSnapshot().SurfacedOptions[agent.OptionIDModel])
			wantThinking := tc.thinking
			if wantThinking == "" {
				wantThinking = "auto"
			}
			assert.Equal(t, wantThinking, started.(*Agent).SettingsSnapshot().SurfacedOptions["thinking"])
			if tc.wantStyle != "" {
				group := optionids.GroupByID(started.OptionGroups(), "outputStyle")
				require.NotNil(t, group)
				assert.Equal(t, tc.wantStyle, group.GetCurrentValue())
				assert.Equal(t, tc.wantStyle, started.(*Agent).SettingsSnapshot().SurfacedOptions["outputStyle"])
				var choices []string
				for _, choice := range group.GetOptions() {
					choices = append(choices, choice.GetId())
				}
				assert.Equal(t, []string{"Default", "Concise", "Explanatory"}, choices)
			}
			if tc.resume != "" {
				assert.Equal(t, tc.resume, sink.SessionIDs()[0])
			} else {
				assert.NotEmpty(t, sink.SessionIDs()[0])
			}
		})
	}
}

func TestCodebuddyStartRejectsUnknownThinkingBeforeLaunch(t *testing.T) {
	previous := codebuddyLocator
	called := false
	missing := filepath.Join(t.TempDir(), "missing-agent")
	codebuddyLocator = launch.Custom(func(context.Context, string, bool) (launch.Spec, launch.Resolution) {
		called = true
		return launch.Spec{Program: missing}, launch.Found
	})
	t.Cleanup(func() { codebuddyLocator = previous })
	sink := &agenttest.Sink{}
	started, err := Start(t.Context(), agent.Options{Options: map[string]string{"thinking": "invalid"}}, agent.NewProviderServices(sink))
	if started != nil {
		t.Cleanup(started.Stop)
	}
	require.ErrorContains(t, err, "thinking")
	assert.Nil(t, started)
	assert.False(t, called)
	assert.Empty(t, sink.SessionIDs())
}
