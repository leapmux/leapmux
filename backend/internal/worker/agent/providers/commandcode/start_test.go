package commandcode

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLaunchArgsSelectTheNativeRPCAndSafeDefault(t *testing.T) {
	root := t.TempDir()
	args, err := launchArgs(agent.Options{}, root)
	require.NoError(t, err)
	assert.Equal(t, []string{"--experimental", "--rpc", "--no-auto-update", "--skip-onboarding", "--mod", filepath.Join(root, "bridge.mjs"), "--permission-mode", "standard"}, args)
	assert.NotContains(t, args, "acp")
}

func TestLaunchArgsPreserveNativeModelEffortAndResume(t *testing.T) {
	args, err := launchArgs(agent.Options{ResumeSessionID: "native-session", Options: optionmap.Map{agent.OptionIDModel: "native/model", agent.OptionIDEffort: "high", agent.OptionIDPermissionMode: contracts.CommandCodePermissionModeBypass}}, t.TempDir())
	require.NoError(t, err)
	assert.Contains(t, args, "--resume")
	assert.Contains(t, args, "native-session")
	assert.Contains(t, args, "native/model")
	assert.Contains(t, args, "high")
	assert.Contains(t, args, "--yolo")
}

func TestLaunchArgsRejectAnUnknownModeAndUnsafeResume(t *testing.T) {
	_, err := launchArgs(agent.Options{Options: optionmap.Map{agent.OptionIDPermissionMode: "unknown"}}, t.TempDir())
	require.Error(t, err)
	_, err = launchArgs(agent.Options{ResumeSessionID: "../another/session"}, t.TempDir())
	require.Error(t, err)
}

func TestNativeStateRequiresProtocolAndSessionIdentity(t *testing.T) {
	a, _ := testAgent(t)
	a.Mu.Lock()
	a.sessionID = ""
	a.Mu.Unlock()
	for _, raw := range []string{`null`, `{}`, `{"protocolVersion":2,"session":{"id":"s","model":"m","permissionMode":"default"}}`, `{"protocolVersion":1,"session":{"id":"s","model":"m","permissionMode":"unknown"}}`} {
		assert.Error(t, a.applyState(json.RawMessage(raw)))
	}
	require.NoError(t, a.applyState(json.RawMessage(`{"protocolVersion":1,"session":{"id":"new-session","model":"native/model","effort":"high","permissionMode":"plan"}}`)))
	assert.Equal(t, "new-session", a.sessionID)
	assert.Equal(t, "native/model", a.model)
	assert.Equal(t, "high", a.effort)
	assert.Equal(t, "plan", a.permissionMode)
}
