package antigravity

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
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
				outsideTarget := filepath.Join(outside, "secret.txt")
				if existing {
					require.NoError(t, os.WriteFile(outsideTarget, []byte("original"), 0o600))
				}
				assert.False(t, pathWithinWorkspace(workspace, raw), "the unresolved path is not eligible for automatic approval")
				a, _ := testAgent(t)
				a.workspace = workspace
				permission := &permissionInteraction{}
				permission.Resource.Action = contracts.AntigravityPermissionActionWriteFile
				permission.Resource.Target = raw
				assert.False(t, a.autoAnswerLocked(contracts.AntigravityModeAcceptEdits, permission))
				writtenTarget := outsideTarget
				untouchedTarget := filepath.Join(workspace, "secret.txt")
				if runtime.GOOS == "windows" {
					// Win32 normalizes parent segments before following the symlink.
					// Permission checks still reject the unresolved path on every OS.
					writtenTarget, untouchedTarget = untouchedTarget, writtenTarget
				}
				require.NoError(t, os.WriteFile(raw, []byte("native write marker"), 0o600))
				content, err := os.ReadFile(writtenTarget)
				require.NoError(t, err)
				assert.Equal(t, "native write marker", string(content))
				if runtime.GOOS == "windows" && existing {
					content, err = os.ReadFile(untouchedTarget)
					require.NoError(t, err)
					assert.Equal(t, "original", string(content))
				} else {
					_, err = os.Stat(untouchedTarget)
					assert.ErrorIs(t, err, os.ErrNotExist)
				}
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
	a.root.trajectoryID = "trajectory"
	raw := []byte(`{"type":"CORTEX_STEP_TYPE_GENERIC","status":"CORTEX_STEP_STATUS_WAITING","metadata":{"toolCall":{"id":"call","name":"run_command"}},"requestedInteraction":{"permission":{"resource":{"action":"command","target":"echo"},"actionDescription":"Run echo"}}}`)
	step, err := decodeStep(raw)
	require.NoError(t, err)
	a.root.steps[0] = &stepState{status: contracts.AntigravityStepStatusWaiting, raw: raw}
	a.registerInteraction(a.root, 0, step)
	require.Equal(t, 1, sink.PublishedControlCount())
	request := sink.LastPublishedControl()
	assert.Equal(t, "cascade", request.AgentSessionID)
	assert.Contains(t, string(request.Payload), "run_command")
	step.Status = contracts.AntigravityStepStatusCanceled
	assert.NoError(t, a.applyStep(a.root, 0, step, a.root.steps[0].raw))
	assert.Equal(t, []string{request.RequestID}, sink.CanceledControls())
}

// Every interaction control has a request ID that AnswerControlRequest reads
// back, so each answer that its prompt offers can reach the step. A step that
// gives no such ID has no control.
func TestNewInteractionControlBuildsOnlyAnAnswerableRequest(t *testing.T) {
	step, err := decodeStep(waitingInteractionStep("permission"))
	require.NoError(t, err)
	control, err := newInteractionControl("cascade", "trajectory", 3, step)
	require.NoError(t, err)
	address, err := antigravityAddressOf(control.requestID)
	require.NoError(t, err)
	assert.Equal(t, antigravityAddress{cascadeID: "cascade", trajectoryID: "trajectory", stepIndex: 3}, address)
	assert.NotEmpty(t, control.instanceID)
	assert.NotNil(t, control.permission)

	unknown, err := decodeStep(waitingStep(`{"browserAction":{}}`, 0))
	require.NoError(t, err)
	for name, tc := range map[string]struct {
		cascadeID, trajectoryID string
		step                    antigravityStep
		reason                  string
	}{
		"no trajectory":                        {cascadeID: "cascade", step: step, reason: "trajectory"},
		"no cascade":                           {trajectoryID: "trajectory", step: step, reason: `"antigravity::trajectory:3"`},
		"a colon in the cascade":               {cascadeID: "cas:cade", trajectoryID: "trajectory", step: step, reason: `"antigravity:cas:cade:trajectory:3"`},
		"a colon in the trajectory":            {cascadeID: "cascade", trajectoryID: "traj:ectory", step: step, reason: `"antigravity:cascade:traj:ectory:3"`},
		"an interaction LeapMux cannot answer": {cascadeID: "cascade", trajectoryID: "trajectory", step: unknown, reason: "browserAction"},
	} {
		_, err := newInteractionControl(tc.cascadeID, tc.trajectoryID, 3, tc.step)
		assert.ErrorContains(t, err, tc.reason, name)
	}
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
	agenttest.AssertPublishedControlKeys(t, antigravityProvider{}, sink)
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

// "input not registered" refuses the answer of a step that still waits, and
// the refusal stays an error. An answer to a step whose control LeapMux no
// longer holds reaches nothing, so it is refused before it is sent: it
// returns ErrControlRequestNotPending, not nil, because nil would make the
// Worker record the answer as delivered and write a transcript row for it.
func TestAntigravityControlAnswerRefusesAStepThatTakesNoAnswer(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
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
	a.root.controls[2] = &antigravityControl{kind: "permission", instanceID: "instance", cascadeID: a.root.id, trajectoryID: a.root.trajectoryID, stepIndex: 2}
	answer := agent.RawInput{Data: []byte(`{"cascadeId":"cascade","interaction":{"trajectoryId":"trajectory","stepIndex":2,"permission":{"allow":true}}}`), ControlInstanceID: "instance"}
	err = a.SendRawInput(answer)
	require.Error(t, err)
	assert.NotErrorIs(t, err, agent.ErrControlRequestNotPending, "the step still waits, so its own refusal stands")
	delete(a.root.controls, 2)
	assert.ErrorIs(t, a.SendRawInput(answer), agent.ErrControlRequestNotPending)
	assert.Equal(t, int32(1), calls.Load(), "the answer to a step with no control is not sent")
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
	a.root.controls[2] = &antigravityControl{kind: "permission", instanceID: "instance", cascadeID: a.root.id, trajectoryID: a.root.trajectoryID, stepIndex: 2}
	err := a.SendRawInput(agent.RawInput{Data: []byte(`{"cascadeId":"cascade","interaction":{"trajectoryId":"trajectory","stepIndex":2,"permission":{"allow":true}}}`), ControlInstanceID: "instance"})
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
	a.root.controls[2] = &antigravityControl{kind: "permission", instanceID: "instance", cascadeID: a.root.id, trajectoryID: a.root.trajectoryID, stepIndex: 2}
	done := make(chan error, 1)
	go func() {
		done <- a.SendRawInput(agent.RawInput{Data: []byte(`{"cascadeId":"cascade","interaction":{"trajectoryId":"trajectory","stepIndex":2,"permission":{"allow":true}}}`), ControlInstanceID: "instance"})
	}()
	receiveTestValue(t, entered)
	a.withdrawInteraction(a.root, 2)
	unblock()
	// The language server took no answer, and the step's control is gone, so
	// the answer reached nothing. nil would make the Worker record it as
	// delivered.
	err := receiveTestValue(t, done)
	assert.ErrorIs(t, err, agent.ErrControlRequestNotPending)
	assert.ErrorContains(t, err, "input not registered")
}

// The language server addresses an answer to a step, not to one interaction
// of it, and a step can replace the interaction that it waits for. So an
// answer reaches the language server only while the request instance that it
// answers is the step's current control. An answer to the replaced
// permission, and an answer that states no instance, reach nothing, and the
// refusal lets the Worker release the answer. The answer to the current
// question reaches the step.
func TestAntigravityControlReplyRefusesAReplacedInstance(t *testing.T) {
	a, sink := publicationTestAgent(t, nil)
	rpc, server := newRecordedLanguageServer(t)
	a.Mu.Lock()
	a.rpc = rpc
	a.Mu.Unlock()
	feedUpdate(t, a, updateWithSteps(cascadeStatusRunning, []int{0}, []json.RawMessage{waitingInteractionStep("permission")}))
	require.Equal(t, 1, sink.PublishedControlCount())
	replaced := sink.LastPublishedControl()
	feedUpdate(t, a, updateWithSteps(cascadeStatusRunning, []int{0}, []json.RawMessage{waitingInteractionStep("question")}))
	require.Equal(t, 2, sink.PublishedControlCount())
	current := sink.LastPublishedControl()
	require.Equal(t, replaced.RequestID, current.RequestID, "the step keeps its request ID")

	allow, err := antigravityPermissionAnswer(replaced.RequestID, true, "")
	require.NoError(t, err)
	require.ErrorIs(t, a.SendRawInput(agent.RawInput{Data: allow, ControlInstanceID: replaced.InstanceID}), agent.ErrControlInstanceReplaced)
	assert.ErrorIs(t, a.SendRawInput(agent.RawInput{Data: allow}), agent.ErrControlInstanceUnstated)
	assert.Empty(t, server.answered(), "no answer reaches the question that replaced the permission")

	address, err := antigravityAddressOf(current.RequestID)
	require.NoError(t, err)
	dismiss, err := address.answer("askQuestion", map[string]any{"cancelled": true})
	require.NoError(t, err)
	require.NoError(t, a.SendRawInput(agent.RawInput{Data: dismiss, ControlInstanceID: current.InstanceID}))
	require.Len(t, server.answered(), 1)
	assert.JSONEq(t, `{"trajectoryId":"trajectory","stepIndex":0,"askQuestion":{"cancelled":true}}`, string(server.answered()[0]))
}

// The step replaces its control before it withdraws the replaced instance
// (registerInteraction). A manual answer to the replaced instance that
// arrives between the two reaches nothing either.
func TestAntigravityControlReplyRefusesAnInstanceThatItsCancelStillFollows(t *testing.T) {
	a, sink := publicationTestAgent(t, nil)
	rpc, server := newRecordedLanguageServer(t)
	a.Mu.Lock()
	a.rpc = rpc
	a.Mu.Unlock()
	feedUpdate(t, a, updateWithSteps(cascadeStatusRunning, []int{0}, []json.RawMessage{waitingInteractionStep("permission")}))
	require.Equal(t, 1, sink.PublishedControlCount())
	replaced := sink.LastPublishedControl()

	sink.cancelEntered = make(chan struct{}, 1)
	var unblock func()
	sink.cancelRelease, unblock = newTestGate(t)
	done := make(chan error, 1)
	go func() {
		done <- a.applyUpdate(a.root, updateWithSteps(cascadeStatusRunning, []int{0}, []json.RawMessage{waitingInteractionStep("question")}), false)
	}()
	receiveTestValue(t, sink.cancelEntered)
	allow, err := antigravityPermissionAnswer(replaced.RequestID, true, "")
	require.NoError(t, err)
	err = a.SendRawInput(agent.RawInput{Data: allow, ControlInstanceID: replaced.InstanceID})
	unblock()
	require.NoError(t, receiveTestValue(t, done))
	require.ErrorIs(t, err, agent.ErrControlInstanceReplaced)
	assert.Empty(t, server.answered(), "no answer reaches the question that replaced the permission")
	assert.Equal(t, []string{replaced.RequestID}, sink.CanceledControls())
	require.Equal(t, 2, sink.PublishedControlCount(), "the user sees the question")
	assert.NotNil(t, agenttest.ControlPromptOf(antigravityProvider{}, sink.LastPublishedControl().Payload).GetQuestion())
}

func TestAntigravityPlanReadUsesCheckedDescriptor(t *testing.T) {
	directory := t.TempDir()
	original := filepath.Join(directory, "original.md")
	replacement := filepath.Join(directory, "replacement.md")
	path := filepath.Join(directory, "plan.md")
	require.NoError(t, os.WriteFile(original, []byte("checked plan"), 0o600))
	require.NoError(t, os.WriteFile(replacement, []byte("replacement plan"), 0o600))
	require.NoError(t, os.Symlink(original, path))
	file, err := os.Open(path)
	require.NoError(t, err)
	defer func() { require.NoError(t, file.Close()) }()
	// Replacing the alias works on Windows, where an ordinary open handle
	// prevents replacing its target. Reopening the alias must read other bytes.
	require.NoError(t, os.Remove(path))
	require.NoError(t, os.Symlink(replacement, path))
	current, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "replacement plan", string(current))
	content, err := readPlanContent(file)
	require.NoError(t, err)
	assert.Equal(t, "checked plan", string(content))
}

// An answer reaches the language server only while its control is current.
// Every other answer reached nothing, so it returns ErrControlRequestNotPending
// rather than nil, which would make the Worker record it as delivered. None of
// these controls was replaced by another instance of the same request, so
// none is the silent refusal of a replaced instance.
func TestAntigravityManualReplyRequiresCurrentObserver(t *testing.T) {
	for _, scenario := range []string{"current", "closed", "old generation", "wrong trajectory", "absent control", "discarded", "replacement", "unknown cascade"} {
		t.Run(scenario, func(t *testing.T) {
			a, _ := testControlAgent(t)
			var calls atomic.Int32
			a.rpc = testAntigravityRPC(t, func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); _, _ = w.Write([]byte(`{}`)) })
			state := a.root
			state.trajectoryID = "trajectory"
			state.controls[0] = &antigravityControl{kind: "permission", instanceID: "instance", cascadeID: state.id, trajectoryID: state.trajectoryID, stepIndex: 0}
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
			cascadeID := state.id
			if scenario == "unknown cascade" {
				cascadeID = "another-cascade"
			}
			raw, _ := json.Marshal(map[string]any{"cascadeId": cascadeID, "interaction": map[string]any{"trajectoryId": trajectory, "stepIndex": 0, "permission": map[string]any{"allow": true}}})
			err := a.SendRawInput(agent.RawInput{Data: raw, ControlInstanceID: "instance"})
			if scenario == "current" {
				require.NoError(t, err)
				assert.Equal(t, int32(1), calls.Load())
				return
			}
			assert.ErrorIs(t, err, agent.ErrControlRequestNotPending)
			assert.NotErrorIs(t, err, agent.ErrControlInstanceReplaced)
			assert.Zero(t, calls.Load())
		})
	}
}
