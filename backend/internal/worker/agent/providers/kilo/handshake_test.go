//go:build unix

package kilo

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
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
)

// installFakeKiloACP puts a fake `kilo` on PATH. `envFile`, when given, makes the
// launcher dump the environment it was started with, so a test can assert what
// LeapMux actually hands the daemon.
func installFakeKiloACP(t *testing.T, scenario string, envFile ...string) {
	t.Helper()

	dir := t.TempDir()
	launcher := filepath.Join(dir, "kilo")
	dump := ""
	if len(envFile) > 0 && envFile[0] != "" {
		dump = fmt.Sprintf("env > %q\n", envFile[0])
	}
	script := fmt.Sprintf("#!/bin/sh\n%sLEAPMUX_KILO_TEST_SCENARIO=%q exec %q -test.run=TestHelperProcessKiloACP --\n", dump, scenario, os.Args[0])
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
		case acp.MethodInitialize:
			writeResult(req.ID, `{"protocolVersion":1,"agentCapabilities":{"loadSession":true}}`)
		case acp.MethodSessionNew:
			if scenario == "generic-option" {
				// A third axis (thought_level) the model/mode channels don't claim; it
				// must surface as a mutable option group alongside the primary agent.
				writeResult(req.ID, `{"sessionId":"kilo-new","modes":{"currentModeId":"code","availableModes":[{"id":"code","name":"Code"},{"id":"plan","name":"Plan"}]},"configOptions":[{"id":"mode","currentValue":"code","options":[{"value":"code","name":"Code"},{"value":"plan","name":"Plan"}]},{"id":"model","currentValue":"anthropic/claude-sonnet-4","options":[{"value":"anthropic/claude-sonnet-4","name":"Claude Sonnet 4"}]},{"id":"thoughtLevel","category":"thought_level","name":"Thought Level","currentValue":"high","options":[{"value":"low","name":"Low"},{"value":"high","name":"High"}]}]}`)
				continue
			}
			// Kilo, like OpenCode, reports models only through the configOptions
			// `model` select; primary agents arrive via the `modes` channel.
			writeResult(req.ID, `{"sessionId":"kilo-new","modes":{"currentModeId":"code","availableModes":[{"id":"code","name":"Code"},{"id":"plan","name":"Plan"}]},"configOptions":[{"id":"mode","currentValue":"code","options":[{"value":"code","name":"Code"},{"value":"plan","name":"Plan"}]},{"id":"model","currentValue":"anthropic/claude-sonnet-4","options":[{"value":"anthropic/claude-sonnet-4","name":"Claude Sonnet 4"},{"value":"openai/gpt-5","name":"GPT-5"}]}]}`)
		case acp.MethodSessionSetConfigOption, acp.MethodSessionSetModel, acp.MethodSessionSetMode, acp.MethodSessionPrompt:
			writeResult(req.ID, `{}`)
		}
	}
	os.Exit(0)
}

func TestStartKilo_NewSessionHandshakeReadsConfigOptionModels(t *testing.T) {
	installFakeKiloACP(t, "")

	provider, err := Start(context.Background(), agent.Options{
		AgentID:       "kilo-new",
		WorkingDir:    t.TempDir(),
		Shell:         testutil.TestShell(),
		LoginShell:    false,
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_KILO,
	}, agent.NewProviderServices(&agenttest.Sink{}))
	require.NoError(t, err)

	a := provider.(*Agent)
	t.Cleanup(func() {
		a.Stop()
		_ = a.Wait()
	})

	assert.Equal(t, "kilo-new", a.SessionIDForTest())
	assert.Equal(t, "anthropic/claude-sonnet-4", a.ModelForTest())
	require.Len(t, a.AvailableModelsForTest(), 2)
	assert.Equal(t, "anthropic/claude-sonnet-4", a.AvailableModelsForTest()[0].GetId())
	assert.True(t, a.AvailableModelsForTest()[0].IsDefault)
	assert.Equal(t, "openai/gpt-5", a.AvailableModelsForTest()[1].GetId())
	groups := a.OptionGroups()
	modelGroup := optionids.GroupByID(groups, agent.OptionIDModel)
	require.NotNil(t, modelGroup)
	assert.Equal(t, "anthropic/claude-sonnet-4", modelGroup.GetDefaultValue())
	require.NotNil(t, optionids.GroupByID(groups, agent.OptionIDPrimaryAgent))
}

// End-to-end: a Kilo handshake reporting an unmapped config option surfaces it as a
// mutable option group after the mapped primary-agent group. Kilo shares the
// primary-agent seam with OpenCode; this is the parity guard.
func TestStartKilo_HandshakeSurfacesGenericConfigOption(t *testing.T) {
	installFakeKiloACP(t, "generic-option")

	provider, err := Start(context.Background(), agent.Options{
		AgentID:       "kilo-generic",
		WorkingDir:    t.TempDir(),
		Shell:         testutil.TestShell(),
		LoginShell:    false,
		AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_KILO,
	}, agent.NewProviderServices(&agenttest.Sink{}))
	require.NoError(t, err)

	a := provider.(*Agent)
	t.Cleanup(func() {
		a.Stop()
		_ = a.Wait()
	})

	groups := a.OptionGroups()
	assert.NotNil(t, optionids.GroupByID(groups, agent.OptionIDPrimaryAgent))
	assert.NotNil(t, optionids.GroupByID(groups, "thoughtLevel"))
	assert.Equal(t, "high", agent.CurrentOptions(groups)["thoughtLevel"])
}
