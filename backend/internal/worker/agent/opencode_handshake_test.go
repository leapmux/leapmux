//go:build unix

package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionids"
	"github.com/leapmux/leapmux/internal/util/optionmap"
)

func installFakeOpenCodeACP(t *testing.T, scenario string) {
	t.Helper()

	dir := t.TempDir()
	launcher := filepath.Join(dir, "opencode")
	script := fmt.Sprintf("#!/bin/sh\nLEAPMUX_OPENCODE_TEST_SCENARIO=%q exec %q -test.run=TestHelperProcessOpenCodeACP --\n", scenario, os.Args[0])
	require.NoError(t, os.WriteFile(launcher, []byte(script), 0o755))

	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GO_WANT_HELPER_PROCESS_OPENCODE", "1")
}

func TestHelperProcessOpenCodeACP(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS_OPENCODE") != "1" {
		return
	}

	scenario := os.Getenv("LEAPMUX_OPENCODE_TEST_SCENARIO")

	scanner := bufio.NewScanner(os.Stdin)
	writer := bufio.NewWriter(os.Stdout)
	defer func() { _ = writer.Flush() }()

	writeResult := func(id json.RawMessage, body string) {
		_, _ = fmt.Fprintf(writer, `{"jsonrpc":"2.0","id":%s,"result":%s}`+"\n", string(id), body)
		_ = writer.Flush()
	}
	writeError := func(id json.RawMessage, message string) {
		_, _ = fmt.Fprintf(writer, `{"jsonrpc":"2.0","id":%s,"error":{"code":-32602,"message":%q}}`+"\n", string(id), message)
		_ = writer.Flush()
	}

	for scanner.Scan() {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			continue
		}

		switch req.Method {
		case acpMethodInitialize:
			writeResult(req.ID, `{"protocolVersion":1,"agentCapabilities":{"loadSession":true}}`)
		case acpMethodSessionNew:
			if scenario == "generic-option" {
				// A third axis (thought_level) the model/mode channels don't claim. It
				// must surface as a mutable option group alongside the primary-agent
				// group (which is the claimed `mode`).
				writeResult(req.ID, `{"sessionId":"opencode-new","modes":{"currentModeId":"build","availableModes":[{"id":"build","name":"Build"},{"id":"plan","name":"Plan"}]},"configOptions":[{"id":"mode","currentValue":"build","options":[{"value":"build","name":"Build"},{"value":"plan","name":"Plan"}]},{"id":"model","currentValue":"anthropic/claude-sonnet-4","options":[{"value":"anthropic/claude-sonnet-4","name":"Claude Sonnet 4"}]},{"id":"thoughtLevel","category":"thought_level","name":"Thought Level","currentValue":"high","options":[{"value":"low","name":"Low"},{"value":"high","name":"High"}]}]}`)
				continue
			}
			if scenario == "effort-on-model-switch" {
				// The default model has NO reasoning variants, so the handshake carries
				// no effort option -- the real OpenCode daemon only emits the effort
				// select once the current model has variants. Switching to gpt-5.5
				// (handled in session/set_config_option below) surfaces it.
				writeResult(req.ID, `{"sessionId":"opencode-new","modes":{"currentModeId":"build","availableModes":[{"id":"build","name":"Build"},{"id":"plan","name":"Plan"}]},"configOptions":[{"id":"mode","currentValue":"build","options":[{"value":"build","name":"Build"},{"value":"plan","name":"Plan"}]},{"id":"model","category":"model","currentValue":"anthropic/claude-sonnet-4","options":[{"value":"anthropic/claude-sonnet-4","name":"Claude Sonnet 4"},{"value":"openai/gpt-5.5","name":"GPT-5.5"}]}]}`)
				continue
			}
			// Real OpenCode reports models ONLY through the configOptions `model`
			// select -- never the SessionModelState `models` field. Primary agents
			// arrive via the `modes` channel.
			writeResult(req.ID, `{"sessionId":"opencode-new","modes":{"currentModeId":"build","availableModes":[{"id":"build","name":"Build"},{"id":"plan","name":"Plan"}]},"configOptions":[{"id":"mode","currentValue":"build","options":[{"value":"build","name":"Build"},{"value":"plan","name":"Plan"}]},{"id":"model","currentValue":"anthropic/claude-sonnet-4","options":[{"value":"anthropic/claude-sonnet-4","name":"Claude Sonnet 4"},{"value":"openai/gpt-5","name":"GPT-5"}]}]}`)
		case acpMethodSessionSetConfigOption:
			// LeapMux now writes the model through session/set_config_option (configId
			// "model"); the daemon echoes the refreshed configOptions, which is how a
			// model-dependent effort axis is surfaced.
			var p struct {
				ConfigID string `json:"configId"`
				Value    string `json:"value"`
			}
			_ = json.Unmarshal(req.Params, &p)
			if scenario == "reject-model" && p.ConfigID == "model" {
				writeError(req.ID, "unknown model")
				continue
			}
			if scenario == "effort-on-model-switch" && p.ConfigID == "model" && p.Value == "openai/gpt-5.5" {
				// gpt-5.5 has reasoning variants, so the refreshed configOptions now
				// include the effort select.
				writeResult(req.ID, `{"configOptions":[{"id":"mode","currentValue":"build","options":[{"value":"build","name":"Build"},{"value":"plan","name":"Plan"}]},{"id":"model","category":"model","currentValue":"openai/gpt-5.5","options":[{"value":"anthropic/claude-sonnet-4","name":"Claude Sonnet 4"},{"value":"openai/gpt-5.5","name":"GPT-5.5"}]},{"id":"effort","category":"thought_level","name":"Effort","currentValue":"medium","options":[{"value":"low","name":"Low"},{"value":"medium","name":"Medium"},{"value":"high","name":"High"}]}]}`)
				continue
			}
			// Default: echo the current model/mode config options (no effort axis).
			writeResult(req.ID, `{"configOptions":[{"id":"mode","currentValue":"build","options":[{"value":"build","name":"Build"},{"value":"plan","name":"Plan"}]},{"id":"model","category":"model","currentValue":"anthropic/claude-sonnet-4","options":[{"value":"anthropic/claude-sonnet-4","name":"Claude Sonnet 4"},{"value":"openai/gpt-5","name":"GPT-5"}]}]}`)
		case acpMethodSessionSetModel:
			// Legacy unstable channel: kept so a stray set_model still acks, but LeapMux
			// no longer sends it (it writes the model via set_config_option above).
			writeResult(req.ID, `{}`)
		case acpMethodSessionSetMode, acpMethodSessionPrompt:
			writeResult(req.ID, `{}`)
		}
	}
	os.Exit(0)
}

func TestStartOpenCode_NewSessionHandshakeReadsConfigOptionModels(t *testing.T) {
	installFakeOpenCodeACP(t, "")

	provider, err := StartOpenCode(context.Background(), Options{
		AgentID:       "opencode-new",
		WorkingDir:    t.TempDir(),
		Shell:         testutil.TestShell(),
		LoginShell:    false,
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE,
	}, &testSink{})
	require.NoError(t, err)

	agent := provider.(*OpenCodeAgent)
	t.Cleanup(func() {
		agent.Stop()
		_ = agent.Wait()
	})

	assert.Equal(t, "opencode-new", agent.sessionID)
	// Current model and the model list come from the configOptions `model` select.
	assert.Equal(t, "anthropic/claude-sonnet-4", agent.model)
	require.Len(t, agent.availableModels, 2)
	assert.Equal(t, "anthropic/claude-sonnet-4", agent.availableModels[0].GetId())
	assert.Equal(t, "Claude Sonnet 4", agent.availableModels[0].DisplayName)
	assert.True(t, agent.availableModels[0].IsDefault)
	assert.Equal(t, "openai/gpt-5", agent.availableModels[1].GetId())
	// The model catalog is projected into the model group, whose default badge
	// reflects the catalog's default model.
	groups := agent.OptionGroups()
	modelGroup := optionids.GroupByID(groups, OptionIDModel)
	require.NotNil(t, modelGroup)
	assert.Equal(t, "anthropic/claude-sonnet-4", modelGroup.GetDefaultValue())
	// Primary agents still come from the modes channel, unaffected by the fix.
	require.NotNil(t, optionids.GroupByID(groups, OptionIDPrimaryAgent))
}

// End-to-end: a handshake reporting an unmapped config option surfaces it as a
// mutable option group after the mapped primary-agent group, and its value rides
// in CurrentSettings extras next to the primaryAgent key. This exercises the
// primary-agent AvailableOptionGroups path; Copilot covers the permission-mode path.
func TestStartOpenCode_HandshakeSurfacesGenericConfigOption(t *testing.T) {
	installFakeOpenCodeACP(t, "generic-option")

	provider, err := StartOpenCode(context.Background(), Options{
		AgentID:       "opencode-generic",
		WorkingDir:    t.TempDir(),
		Shell:         testutil.TestShell(),
		LoginShell:    false,
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE,
	}, &testSink{})
	require.NoError(t, err)

	agent := provider.(*OpenCodeAgent)
	t.Cleanup(func() {
		agent.Stop()
		_ = agent.Wait()
	})

	groups := agent.OptionGroups()
	// The mapped primary-agent group and the mutable option group both surface
	// (alongside the model group projected from the configOptions `model` select).
	assert.NotNil(t, optionids.GroupByID(groups, OptionIDPrimaryAgent))
	generic := optionids.GroupByID(groups, "thoughtLevel")
	require.NotNil(t, generic)
	assert.Equal(t, "Thought Level", generic.GetLabel())
	require.Len(t, generic.GetOptions(), 2)

	// The current option value rides in CurrentOptions, next to primaryAgent.
	current := CurrentOptions(groups)
	assert.Equal(t, "high", current["thoughtLevel"])
	assert.Equal(t, OpenCodePrimaryAgentBuild, current[OptionIDPrimaryAgent])
}

// A rejected requested model must not abort startup: the agent starts on the
// server's current model with the primary agent still applied.
func TestStartOpenCode_RejectedModelIsNonFatal(t *testing.T) {
	installFakeOpenCodeACP(t, "reject-model")

	provider, err := StartOpenCode(context.Background(), Options{
		AgentID:       "opencode-reject",
		WorkingDir:    t.TempDir(),
		Options:       map[string]string{OptionIDModel: "made-up/model"},
		Shell:         testutil.TestShell(),
		LoginShell:    false,
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE,
	}, &testSink{})
	require.NoError(t, err, "a rejected model must not abort startup")

	agent := provider.(*OpenCodeAgent)
	t.Cleanup(func() {
		agent.Stop()
		_ = agent.Wait()
	})

	// The rejected model was dropped; the server's current model is kept.
	assert.Equal(t, "anthropic/claude-sonnet-4", agent.model)
	require.Len(t, agent.availableModels, 2)
	// The primary agent was still applied -- it is configured before the model, so
	// a model rejection does not undo it.
	assert.Equal(t, OpenCodePrimaryAgentBuild, agent.currentPrimaryAgent)
}

// Switching to a model that has reasoning variants must surface the daemon's effort
// option group. LeapMux writes the model via session/set_config_option (configId
// "model"), whose response carries the refreshed configOptions; folding them surfaces
// the effort axis the new model newly offers. Regression: the prior session/set_model
// write returned {} (no configOptions), so the effort group never appeared after a
// model change.
func TestStartOpenCode_ModelChangeSurfacesEffort(t *testing.T) {
	installFakeOpenCodeACP(t, "effort-on-model-switch")

	sink := &testSink{}
	provider, err := StartOpenCode(context.Background(), Options{
		AgentID:       "opencode-effort",
		WorkingDir:    t.TempDir(),
		Shell:         testutil.TestShell(),
		LoginShell:    false,
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE,
	}, sink)
	require.NoError(t, err)

	agent := provider.(*OpenCodeAgent)
	t.Cleanup(func() {
		agent.Stop()
		_ = agent.Wait()
	})

	// The default model has no variants, so no effort group at handshake.
	require.Nil(t, optionids.GroupByID(agent.OptionGroups(), OptionIDEffort),
		"the variant-less default model must not surface an effort group")
	broadcastsBefore := sink.StatusActiveCount()

	// Switch to the variant-bearing model.
	require.True(t, agent.UpdateSettings(optionmap.Map{OptionIDModel: "openai/gpt-5.5"}))

	// The refreshed configOptions surfaced the effort axis.
	groups := agent.OptionGroups()
	effort := optionids.GroupByID(groups, OptionIDEffort)
	require.NotNil(t, effort, "effort group must surface after switching to a variant-bearing model")
	require.Len(t, effort.GetOptions(), 3)
	assert.Equal(t, "medium", CurrentOptions(groups)[OptionIDEffort])

	// The option-group set changed, so a status refresh must be broadcast -- the frontend
	// rebuilds its option-group catalog only from statusChange events, so without this the
	// newly-surfaced effort group would never reach the settings panel.
	assert.Greater(t, sink.StatusActiveCount(), broadcastsBefore,
		"a status refresh must broadcast the new effort group to the frontend")
}
