package amp

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

func TestRegistrationWiresThePlugin(t *testing.T) {
	t.Parallel()
	reg := Registration()
	_, ok := reg.Plugin.(ampProvider)
	assert.True(t, ok, "the registration hands out this package's plugin")
	assert.Equal(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_AMP, reg.Provider)
	assert.NotNil(t, reg.Start)
	assert.True(t, reg.Locator.Valid())
	_, err := agent.NewRegistry(reg)
	assert.NoError(t, err, "the registration passes the registry's checks")
}

func TestRegistrationStatesTheSettings(t *testing.T) {
	t.Parallel()
	reg := Registration()
	assert.Empty(t, reg.DefaultModels, "Amp picks the model from the mode")
	assert.Equal(t, []*leapmuxv1.AvailableOptionGroup{agentModeGroup, permissionModeGroup}, reg.OptionGroups)
	assert.Equal(t, contracts.AmpPermissionModeAsk, reg.PermissionDefaults.NewSession[agent.OptionIDPermissionMode])
	assert.Equal(t, contracts.AmpPermissionModeAsk, reg.PermissionDefaults.Fallback)
	assert.True(t, reg.FixedPermissionModes)
	assert.False(t, reg.ManagesEffort)
	assert.Empty(t, reg.EnvModelKey, "Amp has no model axis")
	assert.Empty(t, reg.EnvEffortKey, "Amp has no effort axis")
	assert.NotNil(t, reg.Helpers[helperPermission])
}

func TestResumeHandleKeepsTheTokenRule(t *testing.T) {
	t.Parallel()
	agenttest.AssertTokenResumeRule(t, ampProvider{})
	resolved, err := ampProvider{}.ResolveResumeHandle("T-019a2b3c-4d5e-7f60-8a9b-0c1d2e3f4a5b", "")
	require.NoError(t, err)
	assert.Equal(t, "T-019a2b3c-4d5e-7f60-8a9b-0c1d2e3f4a5b", resolved, "an Amp thread id passes unchanged")
}

func TestControlResponseSuites(t *testing.T) {
	t.Parallel()
	agenttest.AssertPreservesTheResponseWithoutARequest(t, ampProvider{})
	agenttest.AssertWithholdsTheResponseForAMalformedRequest(t, ampProvider{})
}

// The neutral approve and reject envelope passes through unchanged: the agent
// reads it in SendRawInput.
func TestControlResponseForwardsTheNeutralEnvelope(t *testing.T) {
	t.Parallel()
	request, err := json.Marshal(contracts.AmpPermissionRequest{
		Type: contracts.AmpPermissionRequestTypeRequest, ToolName: "shell_command", Input: json.RawMessage(`{"command":"ls"}`),
	})
	require.NoError(t, err)
	response := controlAnswer(t, "amp-permission-0a0b0c0d-1", agent.ControlBehaviorDeny, "no")
	resolution := ampProvider{}.ResolveControlResponse(agent.ControlResponseContext{
		RequestID:       "amp-permission-0a0b0c0d-1",
		RequestPayload:  request,
		ResponseContent: response,
	})
	assert.Equal(t, response, resolution.Content)
	assert.False(t, resolution.Withhold)
	assert.Equal(t, "amp-permission-0a0b0c0d-1", ampProvider{}.ControlResponseRequestID(response))
}

func TestRegistryNormalizesAttachmentsByAmpsPolicy(t *testing.T) {
	t.Parallel()
	registry := agenttest.MustNewRegistry(Registration())
	_, err := registry.NormalizeAttachments(leapmuxv1.AgentProvider_AGENT_PROVIDER_AMP, []*leapmuxv1.Attachment{
		{Filename: "notes.txt", Data: []byte("hi")},
	})
	require.NoError(t, err)
	_, err = registry.NormalizeAttachments(leapmuxv1.AgentProvider_AGENT_PROVIDER_AMP, []*leapmuxv1.Attachment{
		{Filename: "spec.pdf", Data: []byte("%PDF")},
	})
	assert.ErrorContains(t, err, "Amp does not support PDF attachments")
}

// The worker's executable reaches the permission helper through the generic
// registry dispatch, with nothing Amp-specific outside this package.
func TestRegistryRunsThePermissionHelper(t *testing.T) {
	t.Parallel()
	h := newHarness(t, withOptions(map[string]string{agent.OptionIDPermissionMode: contracts.AmpPermissionModeAllowAll}))
	h.startToolTurn()
	specPath := filepath.Join(t.TempDir(), helperSpecFileName)
	_, err := agent.WriteHelperSpec(specPath, agent.HelperSpec{
		Provider: leapmuxv1.AgentProvider_AGENT_PROVIDER_AMP.String(),
		Helper:   helperPermission,
		Config:   h.helperConfig(),
	})
	require.NoError(t, err)

	registry := agenttest.MustNewRegistry(Registration())
	var stderr bytes.Buffer
	code := registry.RunHelper(context.Background(), specPath, agent.HelperInvocation{
		Stdin:  strings.NewReader(shellInput),
		Stdout: &bytes.Buffer{},
		Stderr: &stderr,
		Getenv: envOf(map[string]string{envToolName: "shell_command"}),
	})
	assert.Equal(t, helperExitAllow, code)
	assert.Empty(t, stderr.String())
}

// The plugin states the child capabilities that the agent type implements. A
// subagent tab reads them before its root runs.
func TestPluginStatesTheChildCapabilitiesOfTheAgent(t *testing.T) {
	t.Parallel()
	agenttest.AssertChildCapabilities(t, Registration().Plugin, (*Agent)(nil))
}
