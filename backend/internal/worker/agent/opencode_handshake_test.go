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
				// must surface as a read-only generic group alongside the primary-agent
				// group (which is the claimed `mode`).
				writeResult(req.ID, `{"sessionId":"opencode-new","modes":{"currentModeId":"build","availableModes":[{"id":"build","name":"Build"},{"id":"plan","name":"Plan"}]},"configOptions":[{"id":"mode","currentValue":"build","options":[{"value":"build","name":"Build"},{"value":"plan","name":"Plan"}]},{"id":"model","currentValue":"anthropic/claude-sonnet-4","options":[{"value":"anthropic/claude-sonnet-4","name":"Claude Sonnet 4"}]},{"id":"thoughtLevel","category":"thought_level","name":"Thought Level","currentValue":"high","options":[{"value":"low","name":"Low"},{"value":"high","name":"High"}]}]}`)
				continue
			}
			// Real OpenCode reports models ONLY through the configOptions `model`
			// select -- never the SessionModelState `models` field. Primary agents
			// arrive via the `modes` channel.
			writeResult(req.ID, `{"sessionId":"opencode-new","modes":{"currentModeId":"build","availableModes":[{"id":"build","name":"Build"},{"id":"plan","name":"Plan"}]},"configOptions":[{"id":"mode","currentValue":"build","options":[{"value":"build","name":"Build"},{"value":"plan","name":"Plan"}]},{"id":"model","currentValue":"anthropic/claude-sonnet-4","options":[{"value":"anthropic/claude-sonnet-4","name":"Claude Sonnet 4"},{"value":"openai/gpt-5","name":"GPT-5"}]}]}`)
		case acpMethodSessionSetModel:
			if scenario == "reject-model" {
				writeError(req.ID, "unknown model")
				continue
			}
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
	require.Len(t, agent.AvailableModels(), 2)
	assert.Equal(t, "anthropic/claude-sonnet-4", agent.AvailableModels()[0].GetId())
	assert.Equal(t, "Claude Sonnet 4", agent.AvailableModels()[0].GetDisplayName())
	assert.True(t, agent.AvailableModels()[0].GetIsDefault())
	assert.Equal(t, "openai/gpt-5", agent.AvailableModels()[1].GetId())
	// Primary agents still come from the modes channel, unaffected by the fix.
	require.Len(t, agent.AvailableOptionGroups(), 1)
	assert.Equal(t, OptionGroupKeyPrimaryAgent, agent.AvailableOptionGroups()[0].GetKey())
}

// End-to-end: a handshake reporting an unmapped config option surfaces it as a
// read-only generic group after the mapped primary-agent group, and its value rides
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

	groups := agent.AvailableOptionGroups()
	require.Len(t, groups, 2, "the mapped primary-agent group plus one generic group")
	assert.Equal(t, OptionGroupKeyPrimaryAgent, groups[0].GetKey())
	assert.Equal(t, "thoughtLevel", groups[1].GetKey())
	assert.Equal(t, "Thought Level", groups[1].GetLabel())
	require.Len(t, groups[1].GetOptions(), 2)

	// The current generic value rides in CurrentSettings extras, next to primaryAgent.
	extras := agent.CurrentSettings().GetExtraSettings()
	assert.Equal(t, "high", extras["thoughtLevel"])
	assert.Equal(t, OpenCodePrimaryAgentBuild, extras[OptionGroupKeyPrimaryAgent])
}

// A rejected requested model must not abort startup: the agent starts on the
// server's current model with the primary agent still applied.
func TestStartOpenCode_RejectedModelIsNonFatal(t *testing.T) {
	installFakeOpenCodeACP(t, "reject-model")

	provider, err := StartOpenCode(context.Background(), Options{
		AgentID:       "opencode-reject",
		WorkingDir:    t.TempDir(),
		Model:         "made-up/model",
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
	require.Len(t, agent.AvailableModels(), 2)
	// The primary agent was still applied -- it is configured before the model, so
	// a model rejection does not undo it.
	assert.Equal(t, OpenCodePrimaryAgentBuild, agent.currentPrimaryAgent)
}
