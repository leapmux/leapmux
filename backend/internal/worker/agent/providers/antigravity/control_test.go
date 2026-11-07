package antigravity

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
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
	t.Cleanup(a.streamCancel)
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

func TestAntigravityAcceptEditsResolvesSymlinksBeforeParentSegments(t *testing.T) {
	for _, relativeLink := range []bool{false, true} {
		for _, existing := range []bool{false, true} {
			t.Run(fmt.Sprintf("relative=%t/existing=%t", relativeLink, existing), func(t *testing.T) {
				workspace, outside := t.TempDir(), t.TempDir()
				nested := filepath.Join(outside, "nested")
				require.NoError(t, os.Mkdir(nested, 0o700))
				target := nested
				if relativeLink {
					var err error
					target, err = filepath.Rel(workspace, nested)
					require.NoError(t, err)
				}
				link := filepath.Join(workspace, "link")
				require.NoError(t, os.Symlink(target, link))
				raw := link + string(filepath.Separator) + ".." + string(filepath.Separator) + "secret.txt"
				actual := filepath.Join(outside, "secret.txt")
				if existing {
					require.NoError(t, os.WriteFile(actual, []byte("original"), 0o600))
				}
				assert.False(t, pathWithinWorkspace(workspace, raw), "the raw path reaches the outside directory")
				a, _ := testAgent(t)
				a.workspace = workspace
				permission := &permissionInteraction{}
				permission.Resource.Action = contracts.AntigravityPermissionActionWriteFile
				permission.Resource.Target = raw
				assert.False(t, a.autoAnswerLocked(contracts.AntigravityModeAcceptEdits, permission))
				require.NoError(t, os.WriteFile(raw, []byte("outside marker"), 0o600))
				content, err := os.ReadFile(actual)
				require.NoError(t, err)
				assert.Equal(t, "outside marker", string(content))
				_, err = os.Stat(filepath.Join(workspace, "secret.txt"))
				assert.ErrorIs(t, err, os.ErrNotExist)
			})
		}
	}
}

func TestAntigravityWorkspacePathBoundaries(t *testing.T) {
	workspace := t.TempDir()
	inside := filepath.Join(workspace, "nested")
	require.NoError(t, os.Mkdir(inside, 0o700))
	link := filepath.Join(workspace, "inside-link")
	require.NoError(t, os.Symlink(inside, link))
	dangling := filepath.Join(workspace, "dangling")
	require.NoError(t, os.Symlink(filepath.Join(t.TempDir(), "absent"), dangling))
	loop := filepath.Join(workspace, "loop")
	require.NoError(t, os.Symlink(loop, loop))
	file := filepath.Join(workspace, "file.txt")
	require.NoError(t, os.WriteFile(file, nil, 0o600))
	root, err := filepath.EvalSymlinks(workspace)
	require.NoError(t, err)
	for parent := filepath.Dir(root); parent != root; parent = filepath.Dir(root) {
		root = parent
	}
	separator := string(filepath.Separator)
	for _, tc := range []struct {
		name, workspace, target string
		want                    bool
	}{
		{name: "absent nested suffix", workspace: workspace, target: filepath.Join(workspace, "absent", "nested", "new.txt"), want: true},
		{name: "inside symlink parent", workspace: workspace, target: link + separator + ".." + separator + "new.txt", want: true},
		{name: "root workspace", workspace: root, target: filepath.Join(workspace, "new.txt"), want: true},
		{name: "workspace itself", workspace: workspace, target: workspace},
		{name: "sibling prefix", workspace: workspace, target: filepath.Join(workspace+"-sibling", "new.txt")},
		{name: "missing parent segment", workspace: workspace, target: workspace + separator + "absent" + separator + ".." + separator + "new.txt"},
		{name: "dangling symlink", workspace: workspace, target: dangling + separator + "new.txt"},
		{name: "dangling symlink trailing separator", workspace: workspace, target: dangling + separator},
		{name: "symlink loop", workspace: workspace, target: loop + separator + "new.txt"},
		{name: "non-directory ancestor", workspace: workspace, target: file + separator + "new.txt"},
		{name: "unresolved workspace", workspace: workspace + separator + "absent" + separator + "..", target: filepath.Join(workspace, "new.txt")},
		{name: "empty workspace", target: file},
		{name: "empty target", workspace: workspace},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, pathWithinWorkspace(tc.workspace, tc.target))
		})
	}
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
	assert.NoError(t, a.applyStep(a.root, 0, step, a.root.steps[0].raw))
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
	assert.NoError(t, a.endTurn(a.root, agent.MessageCompletionComplete))
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
	assert.NoError(t, a.endTurn(a.root, agent.MessageCompletionComplete))
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
	a.root.controls[2] = &antigravityControl{kind: "permission", cascadeID: a.root.id, trajectoryID: a.root.trajectoryID, stepIndex: 2}
	answer := []byte(`{"cascadeId":"cascade","interaction":{"trajectoryId":"trajectory","stepIndex":2,"permission":{"allow":true}}}`)
	assert.Error(t, a.executeControlReply(answer))
	delete(a.root.controls, 2)
	assert.NoError(t, a.executeControlReply(answer))
}

func TestAntigravityControlReplyPreservesUncertainDelivery(t *testing.T) {
	var accepted atomic.Int32
	a, _ := testControlAgent(t)
	a.rpc = testAntigravityRPC(t, func(w http.ResponseWriter, r *http.Request) {
		_, err := io.ReadAll(r.Body)
		assert.NoError(t, err)
		accepted.Add(1)
		w.Header().Set("Content-Length", "5")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	})
	a.root.trajectoryID = "trajectory"
	a.root.controls[2] = &antigravityControl{kind: "permission", cascadeID: a.root.id, trajectoryID: a.root.trajectoryID, stepIndex: 2}
	err := a.executeControlReply([]byte(`{"cascadeId":"cascade","interaction":{"trajectoryId":"trajectory","stepIndex":2,"permission":{"allow":true}}}`))
	assert.Equal(t, int32(1), accepted.Load())
	assert.ErrorIs(t, err, agent.ErrDeliveryUncertain)
	assert.ErrorIs(t, err, io.ErrUnexpectedEOF)
}

func TestAntigravityControlReplyRechecksRetirement(t *testing.T) {
	entered := make(chan struct{})
	release, unblock := newTestGate(t)
	a, _ := testControlAgent(t)
	a.rpc = testAntigravityRPC(t, func(w http.ResponseWriter, _ *http.Request) {
		close(entered)
		<-release
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("input not registered"))
	})
	a.root.trajectoryID = "trajectory"
	a.root.controls[2] = &antigravityControl{kind: "permission", cascadeID: a.root.id, trajectoryID: a.root.trajectoryID, stepIndex: 2}
	done := make(chan error, 1)
	go func() {
		done <- a.executeControlReply([]byte(`{"cascadeId":"cascade","interaction":{"trajectoryId":"trajectory","stepIndex":2,"permission":{"allow":true}}}`))
	}()
	receiveTestValue(t, entered)
	a.withdrawInteraction(a.root, 2)
	unblock()
	assert.NoError(t, receiveTestValue(t, done))
}

func TestAntigravityPlanReadUsesCheckedDescriptor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plan.md")
	require.NoError(t, os.WriteFile(path, []byte("checked plan"), 0o600))
	file, err := os.Open(path)
	require.NoError(t, err)
	defer func() { require.NoError(t, file.Close()) }()
	replacement := path + ".replacement"
	require.NoError(t, os.WriteFile(replacement, []byte("replacement plan"), 0o600))
	require.NoError(t, os.Rename(replacement, path))
	content, err := readPlanContent(file)
	require.NoError(t, err)
	assert.Equal(t, "checked plan", string(content))
}

func TestAntigravityManualReplyRequiresCurrentObserver(t *testing.T) {
	for _, scenario := range []string{"current", "closed", "old generation", "wrong trajectory", "absent control", "discarded", "replacement"} {
		t.Run(scenario, func(t *testing.T) {
			a, _ := testControlAgent(t)
			var calls atomic.Int32
			a.rpc = testAntigravityRPC(t, func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); _, _ = w.Write([]byte(`{}`)) })
			state := a.root
			state.trajectoryID = "trajectory"
			state.controls[0] = &antigravityControl{kind: "permission", cascadeID: state.id, trajectoryID: state.trajectoryID, stepIndex: 0}
			trajectory := "trajectory"
			switch scenario {
			case "current":
			case "closed":
				state.closed = true
			case "old generation":
				a.generation++
			case "wrong trajectory":
				trajectory = "obsolete-trajectory"
			case "absent control":
				delete(state.controls, 0)
			case "discarded":
				a.DiscardOutput()
			case "replacement":
				a.installRoot(state.id, false)
			}
			raw, _ := json.Marshal(map[string]any{"cascadeId": state.id, "interaction": map[string]any{"trajectoryId": trajectory, "stepIndex": 0, "permission": map[string]any{"allow": true}}})
			require.NoError(t, a.executeControlReply(raw))
			expected := int32(0)
			if scenario == "current" {
				expected = 1
			}
			assert.Equal(t, expected, calls.Load())
		})
	}
}
