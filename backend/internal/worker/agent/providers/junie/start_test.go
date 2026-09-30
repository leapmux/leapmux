package junie

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
)

func TestJunieBaseArgsUsesTheProjectMCPLocation(t *testing.T) {
	t.Parallel()
	args := junieBaseArgs(agent.Options{
		WorkingDir: "/workspace/project",
		Options: optionmap.Map{
			agent.OptionIDModel:  "custom:mock-model",
			agent.OptionIDEffort: "high",
		},
	})
	assert.Contains(t, args, "--mcp-default-locations=false")
	assert.Contains(t, args, "--mcp-location")
	for index, arg := range args {
		if arg == "--mcp-location" {
			require.Less(t, index+1, len(args))
			assert.Equal(t, "/workspace/project/.junie/mcp", args[index+1])
		}
	}
	assert.Contains(t, args, "custom:mock-model")
	assert.Contains(t, args, "high")
}

func TestJunieBaseArgsSkipsTheProjectMCPLocationWithoutAWorkingDir(t *testing.T) {
	t.Parallel()
	args := junieBaseArgs(agent.Options{})
	assert.NotContains(t, args, "--mcp-location")
}

func TestJunieBaseArgsKeepsProxyIdentityOnRestart(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		wireID string
	}{
		{name: "first proxy", wireID: "v1:24:proxy:leapmux-e2e-openai:gpt-5.3-codex"},
		{name: "second proxy", wireID: "v1:11:proxy:other:gpt-5.3-codex"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			args := junieBaseArgs(agent.Options{Options: optionmap.Map{
				agent.OptionIDModel: tc.wireID, agent.OptionIDEffort: "low",
			}})
			assert.NotContains(t, args, "--provider")
			assert.Contains(t, args, "low")
			for flag, want := range map[string]string{"--model": tc.wireID, "--effort": "low"} {
				index := -1
				for i, arg := range args {
					if arg == flag {
						index = i
						break
					}
				}
				require.GreaterOrEqual(t, index, 0, "%s must be present", flag)
				require.Less(t, index+1, len(args))
				assert.Equal(t, want, args[index+1], flag)
			}
		})
	}
}

func TestJunieStartupModelSelectionKeepsNativeProviderIdentity(t *testing.T) {
	t.Parallel()
	const proxyA = "v1:24:proxy:leapmux-e2e-openai:gpt-5.3-codex"
	const proxyB = "v1:11:proxy:other:gpt-5.3-codex"
	for _, tc := range []struct {
		name      string
		current   string
		requested string
		want      string
	}{
		{name: "bare launch model", current: proxyA, requested: "gpt-5.3-codex", want: proxyA},
		{name: "another proxy", current: proxyA, requested: proxyB, want: proxyB},
		{name: "another model", current: proxyA, requested: "gpt-5.4", want: "gpt-5.4"},
		{name: "custom profile", current: "v1:6:custom:custom:mock-model", requested: "custom:mock-model", want: "custom:mock-model"},
		{name: "bare native model", current: "gpt-5.3-codex", requested: "gpt-5.3-codex", want: "gpt-5.3-codex"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			handshake := &acp.SessionResult{CurrentModelID: tc.current}
			assert.Equal(t, tc.want, junieStartupModelSelection(handshake, tc.requested))
		})
	}

	// Some agents report the selected model only through configOptions.
	handshake := &acp.SessionResult{ConfigOptions: []acp.ConfigOption{{ID: acp.ConfigOptionIDModel, CurrentValue: proxyA}}}
	assert.Equal(t, proxyA, junieStartupModelSelection(handshake, "gpt-5.3-codex"))
}
