package gemini

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGeminiRegistrationReadsItsNativeSessionStore(t *testing.T) {
	t.Parallel()
	agenttest.RequireReadsSessionStore(t, Registration().Plugin, func(t *testing.T, home, workingDir string) string {
		t.Helper()
		require.NoError(t, os.MkdirAll(workingDir, 0o700))
		root := filepath.Join(home, ".gemini")
		project := filepath.Join(root, "tmp", "native-project")
		chats := filepath.Join(project, "chats")
		require.NoError(t, os.MkdirAll(chats, 0o700))
		registry, err := json.Marshal(map[string]any{"projects": map[string]string{geminiRegistryPath(workingDir): "native-project"}})
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(root, "projects.json"), registry, 0o600))
		require.NoError(t, os.WriteFile(filepath.Join(project, ".project_root"), []byte(geminiRegistryPath(workingDir)), 0o600))
		_, hash := geminiProjectIdentity(workingDir)
		writeGeminiSession(t, geminiFixtureSessionPath(chats, "native-session"), "native-session", hash, "main", "native session context")
		return "native-session"
	})
}

func TestGeminiPluginStatesTheChildCapabilitiesOfItsAgent(t *testing.T) {
	t.Parallel()
	agenttest.AssertChildCapabilities(t, Registration().Plugin, (*Agent)(nil))
}

func TestGeminiResumeHandleIsAToken(t *testing.T) {
	t.Parallel()
	agenttest.AssertTokenResumeRule(t, Registration().Plugin)
}

func TestGeminiControlRequestsKeepNumericAndStringIdentitiesSeparate(t *testing.T) {
	sink := &agenttest.ControlSink{}
	a := &Agent{}
	a.SetSinkForTest(agent.NewProviderServices(sink))
	agenttest.AssertControlIdentitiesStaySeparate(t, sink, a.HandleOutput, "session/request_permission")
}

func TestGeminiPluginUsesNativePlanModeValues(t *testing.T) {
	t.Parallel()
	plugin := Registration().Plugin
	assert.Equal(t, contracts.GeminiModePlan, plugin.PlanModePermissionMode(agent.PlanModeControlEnter))
	assert.Equal(t, contracts.GeminiModeDefault, plugin.PlanModePermissionMode(agent.PlanModeControlExit))
	assert.Empty(t, plugin.PlanModePermissionMode(0))
	for _, kind := range []agent.AttachmentKind{agent.AttachmentKindText, agent.AttachmentKindImage, agent.AttachmentKindPDF, agent.AttachmentKindBinary} {
		assert.NoError(t, plugin.ValidateAttachment(agent.ClassifiedAttachment{Kind: kind, Filename: "native-input"}))
	}
}
