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
	"github.com/leapmux/leapmux/internal/worker/agent/internal/agentdir"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/agentdir/agentdirtest"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/leapmux/leapmux/internal/worker/terminal"
)

const codebuddyStartupHelperEnv = "LEAPMUX_CODEBUDDY_STARTUP_HELPER"

// codebuddyArgumentPassingEnv reports the PowerShell argument passing that the
// profile of the legacy-style scenario selects.
const codebuddyArgumentPassingEnv = "LEAPMUX_CODEBUDDY_ARGUMENT_PASSING"

// useStartupHelper makes Start launch TestCodebuddyStartupHelper with
// scenario in place of the CodeBuddy CLI. The launch locator is global, so a
// test that calls it stays serial.
func useStartupHelper(t *testing.T, scenario string) {
	t.Helper()
	executable, err := os.Executable()
	require.NoError(t, err)
	previous := codebuddyLocator
	codebuddyLocator = launch.Custom(func(context.Context, string, bool) (launch.Spec, launch.Resolution) {
		return launch.Spec{
			Program:    executable,
			PrefixArgs: []string{"-test.run=^TestCodebuddyStartupHelper$", "--"},
			Env:        []string{codebuddyStartupHelperEnv + "=" + scenario},
		}, launch.Found
	})
	t.Cleanup(func() { codebuddyLocator = previous })
}

func TestCodebuddyStartupHelper(t *testing.T) {
	scenario := os.Getenv(codebuddyStartupHelperEnv)
	if scenario == "" {
		return
	}
	swarmArguments := 0
	for _, argument := range os.Args {
		if argument == "--swarm" {
			swarmArguments++
		}
	}
	wantSwarmArguments := 0
	if strings.Contains(scenario, "swarm") {
		wantSwarmArguments = 1
	}
	if swarmArguments != wantSwarmArguments {
		fmt.Fprintf(os.Stderr, "Expected %d native Swarm arguments, got %d.\n", wantSwarmArguments, swarmArguments)
		os.Exit(24)
	}
	writeFrame := func(frame string) {
		if _, err := fmt.Fprintln(os.Stdout, frame); err != nil {
			os.Exit(25)
		}
	}
	// The settings arrive as the path of a file, never as inline JSON, which a
	// shell can rewrite on the way (see writeStartupSettings).
	readSettingsArgument := func() map[string]json.RawMessage {
		var settings map[string]json.RawMessage
		for index, argument := range os.Args {
			if argument == "--settings" {
				if settings != nil || index+1 == len(os.Args) {
					fmt.Fprintln(os.Stderr, "Expected one --settings argument with a value.")
					os.Exit(24)
				}
				path := os.Args[index+1]
				if !filepath.IsAbs(path) {
					fmt.Fprintf(os.Stderr, "Expected the absolute path of a settings file in --settings, got %q.\n", path)
					os.Exit(24)
				}
				data, err := os.ReadFile(path)
				if err == nil {
					err = json.Unmarshal(data, &settings)
				}
				if err != nil || settings == nil {
					fmt.Fprintf(os.Stderr, "Expected a JSON object in the settings file %s: %v\n", path, err)
					os.Exit(24)
				}
			}
		}
		return settings
	}
	if strings.HasPrefix(scenario, "legacy-") && os.Getenv(codebuddyArgumentPassingEnv) != "Legacy" {
		fmt.Fprintln(os.Stderr, "Expected the PowerShell profile to select Legacy argument passing.")
		os.Exit(24)
	}
	if scenario == "explicit-style" || scenario == "resume-style" || scenario == "legacy-style" ||
		scenario == "swarm-style" || scenario == "resume-swarm-style" || scenario == "legacy-swarm-style" {
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
		if scenario == subtype+"-exit" || scenario == "swarm-"+subtype+"-exit" {
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
			case "explicit-style", "resume-style", "legacy-style", "thinking-style", "thinking-auto-style",
				"swarm-style", "resume-swarm-style", "legacy-swarm-style":
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

// newAgentDirs returns the agent directories of the spec that CodeBuddy's
// registration states, as the worker builds them, and their base.
func newAgentDirs(t *testing.T) (*agentdir.Dirs, string) {
	t.Helper()
	spec := Registration().AgentDir
	require.NotNil(t, spec, "the registration states the agent directory, so the worker sweeps it")
	base := agentdirtest.ShortBase(t)
	return agentdirtest.NewDirs(t, []agentdir.Spec{*spec}, base), base
}

// agentDirsUnder returns each CodeBuddy agent directory under base.
func agentDirsUnder(t *testing.T, base string) []string {
	t.Helper()
	dirs, err := filepath.Glob(filepath.Join(base, "*", agentDirSpec().Prefix+"-*"))
	require.NoError(t, err)
	return dirs
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
		swarm     bool
		// settings is the JSON of the startup settings file, or "" for a start
		// that needs none.
		settings string
	}{
		{name: "fresh session", scenario: "success"},
		{name: "resumed session", scenario: "resume", resume: "existing-session"},
		{name: "configured native style", scenario: "native-style", wantStyle: "Explanatory"},
		{name: "explicit style", scenario: "explicit-style", style: "Concise", wantStyle: "Concise", settings: `{"outputStyle":"Concise"}`},
		{name: "resumed style", scenario: "resume-style", resume: "existing-session", style: "Concise", wantStyle: "Concise", settings: `{"outputStyle":"Concise"}`},
		{name: "swarm with style", scenario: "swarm-style", swarm: true, style: "Concise", wantStyle: "Concise", settings: `{"outputStyle":"Concise"}`},
		{name: "resumed swarm with style", scenario: "resume-swarm-style", swarm: true, resume: "existing-session", style: "Concise", wantStyle: "Concise", settings: `{"outputStyle":"Concise"}`},
		{name: "thinking on", scenario: "thinking-on", thinking: "true", settings: `{"alwaysThinkingEnabled":true}`},
		{name: "thinking off", scenario: "thinking-off", thinking: "false", settings: `{"alwaysThinkingEnabled":false}`},
		{name: "thinking auto", scenario: "thinking-auto", thinking: "auto"},
		{name: "thinking and style", scenario: "thinking-style", thinking: "false", style: "Concise", wantStyle: "Concise", settings: `{"outputStyle":"Concise","alwaysThinkingEnabled":false}`},
		{name: "thinking auto keeps style", scenario: "thinking-auto-style", thinking: "auto", style: "Concise", wantStyle: "Concise", settings: `{"outputStyle":"Concise"}`},
		{name: "resume refusal", scenario: "resume-failure", resume: "existing-session", failure: "Controlled resume refusal."},
		{name: "resume refusal with settings", scenario: "resume-failure", resume: "existing-session", style: "Concise", failure: "Controlled resume refusal."},
		{name: "initialization refusal", scenario: "initialize-refusal", failure: "Controlled initialization refusal."},
		{name: "malformed initialization", scenario: "initialize-malformed", failure: "CodeBuddy sent an invalid control response."},
		{name: "initialization exit", scenario: "initialize-exit", failure: "Controlled native startup failure: initialize"},
		{name: "initialization exit with settings", scenario: "initialize-exit", style: "Concise", failure: "Controlled native startup failure: initialize"},
		{name: "swarm initialization exit with settings", scenario: "swarm-initialize-exit", swarm: true, style: "Concise", failure: "Controlled native startup failure: initialize"},
		{name: "catalog exit", scenario: "get_available_models-exit", failure: "Controlled native startup failure: get_available_models"},
		{name: "catalog exit with settings", scenario: "get_available_models-exit", thinking: "true", failure: "Controlled native startup failure: get_available_models"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			useStartupHelper(t, tc.scenario)
			dirs, base := newAgentDirs(t)
			sink := &agenttest.Sink{}
			home := t.TempDir()
			options := map[string]string{}
			if tc.style != "" {
				options["outputStyle"] = tc.style
			}
			if tc.thinking != "" {
				options["thinking"] = tc.thinking
			}
			if tc.swarm {
				options[OptionSwarmMode] = "true"
			}
			started, err := Start(t.Context(), agent.Options{
				Options:         options,
				ResumeSessionID: tc.resume, AgentID: "native-startup-test", WorkingDir: t.TempDir(), HomeDir: home,
				Shell: terminal.ResolveDefaultShell(), ExtraEnv: []string{"HOME=" + home, "USERPROFILE=" + home, "ZDOTDIR=" + home},
				AgentDirs: dirs,
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
				assert.Empty(t, agentDirsUnder(t, base), "a failed startup removes its agent directory")
				return
			}
			require.NoError(t, err)
			require.NotNil(t, started)
			assert.Equal(t, tc.swarm, started.(*Agent).swarmMode)
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

			directory := started.(*Agent).directory
			if tc.settings == "" {
				assert.Nil(t, directory, "a start without settings needs no agent directory")
				assert.Empty(t, agentDirsUnder(t, base))
				return
			}
			require.NotNil(t, directory)
			assert.Equal(t, []string{directory.Path()}, agentDirsUnder(t, base))
			data, err := os.ReadFile(filepath.Join(directory.Path(), startupSettingsFileName))
			require.NoError(t, err)
			assert.JSONEq(t, tc.settings, string(data))
			// The file stays until the process exits, and Wait then removes it.
			started.Stop()
			_ = started.Wait()
			assert.NoDirExists(t, directory.Path())
			assert.Empty(t, agentDirsUnder(t, base))
		})
	}
}

// The worker always prepares the agent directories. A start without them
// still runs when it needs no settings file, and refuses before CodeBuddy
// starts when it needs one.
func TestCodebuddyStartNeedsAgentDirectoriesOnlyForStartupSettings(t *testing.T) {
	t.Run("no settings", func(t *testing.T) {
		useStartupHelper(t, "success")
		home := t.TempDir()
		started, err := Start(t.Context(), agent.Options{
			AgentID: "codebuddy-no-dirs", WorkingDir: t.TempDir(), HomeDir: home,
			Shell: terminal.ResolveDefaultShell(), ExtraEnv: []string{"HOME=" + home, "USERPROFILE=" + home, "ZDOTDIR=" + home},
		}, agent.NewProviderServices(&agenttest.Sink{}))
		if started != nil {
			t.Cleanup(started.Stop)
		}
		require.NoError(t, err)
		assert.Nil(t, started.(*Agent).directory)
	})
	t.Run("settings", func(t *testing.T) {
		// The helper would fail with its own message if CodeBuddy started.
		useStartupHelper(t, "initialize-exit")
		sink := &agenttest.Sink{}
		started, err := Start(t.Context(), agent.Options{
			Options: map[string]string{OptionOutputStyle: "Concise"},
			AgentID: "codebuddy-no-dirs", WorkingDir: t.TempDir(), Shell: terminal.ResolveDefaultShell(),
		}, agent.NewProviderServices(sink))
		if started != nil {
			t.Cleanup(started.Stop)
		}
		require.ErrorContains(t, err, "prepare the CodeBuddy agent directory")
		assert.NotContains(t, err.Error(), "Controlled native startup failure")
		assert.Nil(t, started)
		assert.Empty(t, sink.SessionIDs())
	})
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

func TestCodebuddyStartRemovesSettingsAfterProcessStartFailure(t *testing.T) {
	useStartupHelper(t, "success")
	dirs, base := newAgentDirs(t)
	started, err := Start(t.Context(), agent.Options{
		Options: map[string]string{OptionOutputStyle: "Concise"},
		AgentID: "failed-exec", WorkingDir: t.TempDir(),
		Shell: filepath.Join(t.TempDir(), "missing-shell"), AgentDirs: dirs,
	}, agent.NewProviderServices(&agenttest.Sink{}))
	require.Error(t, err)
	assert.Nil(t, started)
	assert.Empty(t, agentDirsUnder(t, base), "failed process creation must roll back the settings directory")
}

func TestCodebuddyWaitRemovesSettingsAfterNaturalExit(t *testing.T) {
	useStartupHelper(t, "explicit-style")
	dirs, base := newAgentDirs(t)
	home := t.TempDir()
	started, err := Start(t.Context(), agent.Options{
		Options: map[string]string{OptionOutputStyle: "Concise"},
		AgentID: "natural-exit", WorkingDir: t.TempDir(), HomeDir: home,
		Shell: terminal.ResolveDefaultShell(), AgentDirs: dirs,
		ExtraEnv: []string{"HOME=" + home, "USERPROFILE=" + home, "ZDOTDIR=" + home},
	}, agent.NewProviderServices(&agenttest.Sink{}))
	require.NoError(t, err)
	t.Cleanup(func() {
		started.Stop()
		_ = started.Wait()
	})
	a := started.(*Agent)
	assert.DirExists(t, a.directory.Path())
	require.NoError(t, a.StdinForTest().Close())
	require.NoError(t, a.Wait())
	assert.Empty(t, agentDirsUnder(t, base))
	assert.NoDirExists(t, a.directory.Path())
	require.NoError(t, a.Wait(), "a repeated wait must keep cleanup idempotent")
}

func TestCodebuddyStartRejectsUnknownSwarmBeforeLaunch(t *testing.T) {
	previous := codebuddyLocator
	called := false
	codebuddyLocator = launch.Custom(func(context.Context, string, bool) (launch.Spec, launch.Resolution) {
		called = true
		return launch.Spec{Program: filepath.Join(t.TempDir(), "missing-agent")}, launch.Found
	})
	t.Cleanup(func() { codebuddyLocator = previous })
	started, err := Start(t.Context(), agent.Options{Options: map[string]string{"swarmMode": "invalid"}}, agent.NewProviderServices(&agenttest.Sink{}))
	if started != nil {
		t.Cleanup(started.Stop)
	}
	require.Error(t, err)
	assert.Contains(t, err.Error(), "swarmMode")
	assert.False(t, called)
	assert.Nil(t, started)
}
