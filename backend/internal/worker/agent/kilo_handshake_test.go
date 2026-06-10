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

func installFakeKiloACP(t *testing.T, scenario string) {
	t.Helper()

	dir := t.TempDir()
	launcher := filepath.Join(dir, "kilo")
	script := fmt.Sprintf("#!/bin/sh\nLEAPMUX_KILO_TEST_SCENARIO=%q exec %q -test.run=TestHelperProcessKiloACP --\n", scenario, os.Args[0])
	require.NoError(t, os.WriteFile(launcher, []byte(script), 0o755))

	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GO_WANT_HELPER_PROCESS_KILO", "1")
}

func TestHelperProcessKiloACP(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS_KILO") != "1" {
		return
	}

	scenario := os.Getenv("LEAPMUX_KILO_TEST_SCENARIO")

	scanner := bufio.NewScanner(os.Stdin)
	writer := bufio.NewWriter(os.Stdout)
	defer func() { _ = writer.Flush() }()

	writeResult := func(id json.RawMessage, body string) {
		_, _ = fmt.Fprintf(writer, `{"jsonrpc":"2.0","id":%s,"result":%s}`+"\n", string(id), body)
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
				// A third axis (thought_level) the model/mode channels don't claim; it
				// must surface as a read-only generic group alongside the primary agent.
				writeResult(req.ID, `{"sessionId":"kilo-new","modes":{"currentModeId":"code","availableModes":[{"id":"code","name":"Code"},{"id":"plan","name":"Plan"}]},"configOptions":[{"id":"mode","currentValue":"code","options":[{"value":"code","name":"Code"},{"value":"plan","name":"Plan"}]},{"id":"model","currentValue":"anthropic/claude-sonnet-4","options":[{"value":"anthropic/claude-sonnet-4","name":"Claude Sonnet 4"}]},{"id":"thoughtLevel","category":"thought_level","name":"Thought Level","currentValue":"high","options":[{"value":"low","name":"Low"},{"value":"high","name":"High"}]}]}`)
				continue
			}
			// Kilo, like OpenCode, reports models only through the configOptions
			// `model` select; primary agents arrive via the `modes` channel.
			writeResult(req.ID, `{"sessionId":"kilo-new","modes":{"currentModeId":"code","availableModes":[{"id":"code","name":"Code"},{"id":"plan","name":"Plan"}]},"configOptions":[{"id":"mode","currentValue":"code","options":[{"value":"code","name":"Code"},{"value":"plan","name":"Plan"}]},{"id":"model","currentValue":"anthropic/claude-sonnet-4","options":[{"value":"anthropic/claude-sonnet-4","name":"Claude Sonnet 4"},{"value":"openai/gpt-5","name":"GPT-5"}]}]}`)
		case acpMethodSessionSetModel, acpMethodSessionSetMode, acpMethodSessionPrompt:
			writeResult(req.ID, `{}`)
		}
	}
	os.Exit(0)
}

func TestStartKilo_NewSessionHandshakeReadsConfigOptionModels(t *testing.T) {
	installFakeKiloACP(t, "")

	provider, err := StartKilo(context.Background(), Options{
		AgentID:       "kilo-new",
		WorkingDir:    t.TempDir(),
		Shell:         testutil.TestShell(),
		LoginShell:    false,
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_KILO,
	}, &testSink{})
	require.NoError(t, err)

	agent := provider.(*KiloAgent)
	t.Cleanup(func() {
		agent.Stop()
		_ = agent.Wait()
	})

	assert.Equal(t, "kilo-new", agent.sessionID)
	assert.Equal(t, "anthropic/claude-sonnet-4", agent.model)
	require.Len(t, agent.AvailableModels(), 2)
	assert.Equal(t, "anthropic/claude-sonnet-4", agent.AvailableModels()[0].GetId())
	assert.True(t, agent.AvailableModels()[0].GetIsDefault())
	assert.Equal(t, "openai/gpt-5", agent.AvailableModels()[1].GetId())
	require.Len(t, agent.AvailableOptionGroups(), 1)
	assert.Equal(t, OptionGroupKeyPrimaryAgent, agent.AvailableOptionGroups()[0].GetKey())
}

// End-to-end: a Kilo handshake reporting an unmapped config option surfaces it as a
// read-only generic group after the mapped primary-agent group. Kilo shares the
// primary-agent seam with OpenCode; this is the parity guard.
func TestStartKilo_HandshakeSurfacesGenericConfigOption(t *testing.T) {
	installFakeKiloACP(t, "generic-option")

	provider, err := StartKilo(context.Background(), Options{
		AgentID:       "kilo-generic",
		WorkingDir:    t.TempDir(),
		Shell:         testutil.TestShell(),
		LoginShell:    false,
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_KILO,
	}, &testSink{})
	require.NoError(t, err)

	agent := provider.(*KiloAgent)
	t.Cleanup(func() {
		agent.Stop()
		_ = agent.Wait()
	})

	groups := agent.AvailableOptionGroups()
	require.Len(t, groups, 2, "the mapped primary-agent group plus one generic group")
	assert.Equal(t, OptionGroupKeyPrimaryAgent, groups[0].GetKey())
	assert.Equal(t, "thoughtLevel", groups[1].GetKey())
	assert.Equal(t, "high", agent.CurrentSettings().GetExtraSettings()["thoughtLevel"])
}
