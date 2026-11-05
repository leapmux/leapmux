package antigravity

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testControlAgent(t *testing.T) (*Agent, *agenttest.ControlSink) {
	t.Helper()
	sink := &agenttest.ControlSink{}
	a := newAgentState(agent.NewProviderServices(sink), antigravityRPC{}, t.TempDir(), nil)
	a.catalog = buildAntigravityCatalog(antigravityCatalogReply{ClientModelConfigs: []antigravityModelConfig{{ModelID: "test", ModelOrAlias: struct {
		Model string `json:"model"`
	}{Model: "MODEL_TEST"}}}})
	a.Mu.Lock()
	a.installRoot("cascade", false)
	a.Mu.Unlock()
	return a, sink
}

func TestAntigravityAutoAnswerModes(t *testing.T) {
	workspace := t.TempDir()
	a, _ := testAgent(t)
	a.workspace = workspace
	inside := filepath.Join(workspace, "new.txt")
	outside := filepath.Join(workspace+"-sibling", "file.txt")
	for _, tc := range []struct {
		mode, action, target string
		want                 bool
	}{
		{mode: contracts.AntigravityModeDefault, action: "write_file", target: inside},
		{mode: contracts.AntigravityModePlan, action: "write_file", target: inside},
		{mode: contracts.AntigravityModeAcceptEdits, action: "write_file", target: inside, want: true},
		{mode: contracts.AntigravityModeAcceptEdits, action: "command", target: inside},
		{mode: contracts.AntigravityModeAcceptEdits, action: "write_file", target: outside},
		{mode: contracts.AntigravityModeAcceptEdits, action: "write_file", target: workspace},
		{mode: contracts.AntigravityModeBypass, action: "mcp", target: "server/tool", want: true},
	} {
		permission := &permissionInteraction{}
		permission.Resource.Action = tc.action
		permission.Resource.Target = tc.target
		assert.Equal(t, tc.want, a.autoAnswerLocked(tc.mode, permission), "%s %s %s", tc.mode, tc.action, tc.target)
	}
}

func TestAntigravityAcceptEditsRefusesSymlinkEscape(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	require.NoError(t, os.Symlink(outside, filepath.Join(workspace, "link")))
	assert.False(t, pathWithinWorkspace(workspace, filepath.Join(workspace, "link", "new.txt")))
	assert.True(t, pathWithinWorkspace(workspace, filepath.Join(workspace, "new.txt")))
}

func TestAntigravityPermissionPublishesAndWithdraws(t *testing.T) {
	a, sink := testControlAgent(t)
	step := antigravityStep{Status: contracts.AntigravityStepStatusWaiting}
	step.Type = contracts.AntigravityStepTypeGeneric
	step.RequestedInteraction = json.RawMessage(`{"permission":{"resource":{"action":"command","target":"echo"},"actionDescription":"Run echo"}}`)
	a.root.steps[0] = &stepState{status: contracts.AntigravityStepStatusWaiting, raw: []byte(`{"type":"CORTEX_STEP_TYPE_GENERIC","status":"CORTEX_STEP_STATUS_WAITING","metadata":{"toolCall":{"id":"call","name":"run_command"}},"requestedInteraction":{"permission":{"resource":{"action":"command","target":"echo"}}}}`)}
	a.registerInteraction(a.root, 0, step)
	require.Equal(t, 1, sink.PublishedControlCount())
	request := sink.LastPublishedControl()
	assert.Equal(t, "cascade", request.AgentSessionID)
	assert.Contains(t, string(request.Payload), "run_command")
	step.Status = contracts.AntigravityStepStatusCanceled
	a.applyStep(a.root, 0, step, a.root.steps[0].raw)
	assert.Equal(t, []string{request.RequestID}, sink.CanceledControls())
}

func TestAntigravityPlanReviewPublishesSavedPlan(t *testing.T) {
	a, sink := testControlAgent(t)
	a.mode = contracts.AntigravityModePlan
	a.homeDir = t.TempDir()
	planPath := filepath.Join(a.homeDir, ".gemini", "antigravity-cli", "brain", "cascade", "implementation_plan.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(planPath), 0o700))
	require.NoError(t, os.WriteFile(planPath, []byte("# Implementation plan\n\nStep one.\n"), 0o600))
	a.root.planPath = planPath
	a.beginTurn(a.root)
	a.root.planPath = planPath
	a.endTurn(a.root, agent.MessageCompletionComplete)
	require.Equal(t, 1, sink.PlanUpdateCount())
	content, err := msgcodec.Decompress(sink.LastPlanUpdate().Content, sink.LastPlanUpdate().Compression)
	require.NoError(t, err)
	assert.Contains(t, string(content), "Step one")
	require.Equal(t, 1, sink.PublishedControlCount())
	assert.Contains(t, string(sink.LastPublishedControl().Payload), contracts.AntigravityControlToolPlanReview)
}

func TestAntigravityPlanReviewRefusesOutsidePath(t *testing.T) {
	a, sink := testControlAgent(t)
	a.mode = contracts.AntigravityModePlan
	a.homeDir = t.TempDir()
	outside := filepath.Join(a.homeDir, "outside.md")
	require.NoError(t, os.WriteFile(outside, []byte("secret"), 0o600))
	a.beginTurn(a.root)
	a.root.planPath = outside
	a.endTurn(a.root, agent.MessageCompletionComplete)
	assert.Zero(t, sink.PlanUpdateCount())
	assert.Zero(t, sink.PublishedControlCount())
}

func TestAntigravityControlAnswerIgnoresOnlyStaleInputNotRegistered(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("input not registered"))
	}))
	defer server.Close()
	endpoint, err := providerkit.NewHTTPEndpoint(server.URL, nil)
	require.NoError(t, err)
	defer endpoint.Close()
	a, _ := testControlAgent(t)
	a.rpc = antigravityRPC{endpoint: endpoint, timeout: time.Second}
	a.root.trajectoryID = "trajectory"
	a.root.controls[2] = &antigravityControl{kind: "permission"}
	answer := []byte(`{"cascadeId":"cascade","interaction":{"trajectoryId":"trajectory","stepIndex":2,"permission":{"allow":true}}}`)
	assert.Error(t, a.executeControlReply(answer))
	delete(a.root.controls, 2)
	assert.NoError(t, a.executeControlReply(answer))
}
