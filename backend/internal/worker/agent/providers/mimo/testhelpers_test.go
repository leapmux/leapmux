package mimo

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/coder/quartz"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/require"
)

// testSessionID is the session every test agent drives.
const testSessionID = "ses_test"

// testTimeout is the deadline of a test agent's requests, and of every wait
// for something the agent does on another goroutine. Nothing asserts it, so it
// stays generous.
const testTimeout = 30 * time.Second

// nopStdin is the stdin of a test agent. `mimo serve` reads nothing from stdin,
// and Process.Stop closes it.
type nopStdin struct{}

func (nopStdin) Write(p []byte) (int, error) { return len(p), nil }
func (nopStdin) Close() error                { return nil }

// newTestAgent builds an agent with no process behind it, whose requests reach
// a fake server, and whose events a test feeds through HandleOutput.
//
// The agent already holds a session and the fake catalog, and runs the build
// mode on mock/alpha with the Ask policy, which is where Start leaves a new
// agent. A nil services records nothing.
//
// The agent runs on a mock clock, so no timer of it fires unless a test
// advances the clock. A test that drives a timer takes the clock with
// useMockClock.
func newTestAgent(t *testing.T, services agent.ProviderServices) (*Agent, *fakeServer) {
	t.Helper()
	if services == nil {
		services = agenttest.Nop()
	}
	server, url := startFakeServer(t)
	endpoint, err := providerkit.NewHTTPEndpoint(url, providerkit.BasicAuth(serverUser, server.password))
	require.NoError(t, err)
	t.Cleanup(endpoint.Close)

	workingDir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	a := newAgentState(agent.NewModelProgressResetSink(services),
		mimoRPC{endpoint: endpoint.WithHeader(directoryHeader, workingDir), timeout: testTimeout}, workingDir,
		testutil.NewQuartzMock(t))
	a.Process = providerkit.NewProcessFrom(providerkit.ProcessConfig{
		AgentID:      "test-agent",
		ProviderName: mimoBinaryName,
		Ctx:          ctx,
		Cancel:       cancel,
		Stdin:        nopStdin{},
		APITimeout:   testTimeout,
	})
	a.sessionID = testSessionID
	a.catalog = testCatalog(t)
	a.model = "mock/alpha"
	a.effort = agent.EffortAuto
	a.mode = contracts.MiMoModeBuild
	a.permissionPolicy = contracts.MiMoPermissionPolicyAsk
	return a, server
}

// useMockClock gives the agent a mock clock that the test drives, and returns
// it. Call it before anything of the agent runs, because the clock is read
// without a lock.
func useMockClock(t *testing.T, a *Agent) *quartz.Mock {
	t.Helper()
	clock := testutil.NewQuartzMock(t)
	a.clock = clock
	return clock
}

// newControlTestAgent is newTestAgent with a sink that records control
// requests and plan updates.
func newControlTestAgent(t *testing.T) (*Agent, *agenttest.ControlSink, *fakeServer) {
	t.Helper()
	sink := &agenttest.ControlSink{}
	a, server := newTestAgent(t, agent.NewProviderServices(sink))
	return a, sink, server
}

// newSinkTestAgent is newTestAgent with a recording sink.
func newSinkTestAgent(t *testing.T) (*Agent, *agenttest.Sink, *fakeServer) {
	t.Helper()
	sink := &agenttest.Sink{}
	a, server := newTestAgent(t, agent.NewProviderServices(sink))
	return a, sink, server
}

// testCatalog is the catalog that the fake server's answers build.
func testCatalog(t *testing.T) mimoCatalog {
	t.Helper()
	var providers mimoConfigProviders
	require.NoError(t, json.Unmarshal([]byte(fakeProviders), &providers))
	var agents []mimoAgentInfo
	require.NoError(t, json.Unmarshal([]byte(fakeAgents), &agents))
	return buildMiMoCatalog(providers, mimoConfig{Model: "mock/beta"}, agents)
}

// eventJSON renders one event of the stream.
func eventJSON(t *testing.T, eventType string, properties any) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"type": eventType, "properties": properties})
	require.NoError(t, err)
	return raw
}

// feed dispatches events to the agent in order, as the stream goroutine does.
func feed(a *Agent, events ...[]byte) {
	for _, event := range events {
		a.HandleOutput(event)
	}
}

// statusEvent is session.status for the test session.
func statusEvent(t *testing.T, statusType string) []byte {
	t.Helper()
	return eventJSON(t, contracts.MiMoEventSessionStatus, map[string]any{
		"sessionID": testSessionID, "status": map[string]any{"type": statusType},
	})
}

// messageEvent is message.updated for one message of the test session.
func messageEvent(t *testing.T, id, role, actorID string, completed bool) []byte {
	t.Helper()
	info := map[string]any{"id": id, "sessionID": testSessionID, "role": role}
	if actorID != "" {
		info["agentID"] = actorID
	}
	if completed {
		info["time"] = map[string]any{"created": 1, "completed": 2}
	}
	return eventJSON(t, eventMessageUpdated, map[string]any{"sessionID": testSessionID, "info": info})
}

// textPartEvent is message.part.updated for a text or reasoning part. A part
// that ended carries its end time, as MiMo's final update does.
func textPartEvent(t *testing.T, partType, id, messageID, text string, ended bool) []byte {
	t.Helper()
	part := map[string]any{"id": id, "messageID": messageID, "sessionID": testSessionID, "type": partType, "text": text}
	if ended {
		part["time"] = map[string]any{"start": 1, "end": 2}
	} else {
		part["time"] = map[string]any{"start": 1}
	}
	return eventJSON(t, contracts.MiMoEventMessagePartUpdated, map[string]any{"sessionID": testSessionID, "part": part})
}

// userTextPartEvent is message.part.updated for the text part of a user message.
// MiMo writes a user message whole, so the part states no time at all, as MiMo
// 0.1.14 sends it (probe/mimo-code/actor.sse.jsonl).
func userTextPartEvent(t *testing.T, id, messageID, text string) []byte {
	t.Helper()
	part := map[string]any{"id": id, "messageID": messageID, "sessionID": testSessionID, "type": partTypeText, "text": text}
	return eventJSON(t, contracts.MiMoEventMessagePartUpdated, map[string]any{"sessionID": testSessionID, "part": part})
}

// returnFormatSuffix is the start of the instruction that MiMo appends to the
// first message of a general subagent (actor/spawn.ts, RETURN_FORMAT_INSTRUCTION).
const returnFormatSuffix = "\n\n---\n\n## Return format (required)\n\nYour FINAL assistant message — what the spawning agent will receive — MUST start with this header block:\n"

// deltaEvent is message.part.delta for a text part.
func deltaEvent(t *testing.T, partID, messageID, delta string) []byte {
	t.Helper()
	return eventJSON(t, eventMessagePartDelta, map[string]any{
		"sessionID": testSessionID, "messageID": messageID, "partID": partID, "field": "text", "delta": delta,
	})
}

// toolState is the `state` of a tool part.
type toolState struct {
	Status   string         `json:"status"`
	Input    map[string]any `json:"input,omitempty"`
	Output   string         `json:"output,omitempty"`
	Error    string         `json:"error,omitempty"`
	Title    string         `json:"title,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// toolPartEvent is message.part.updated for a tool part.
func toolPartEvent(t *testing.T, partID, messageID, tool, callID string, state toolState) []byte {
	t.Helper()
	return eventJSON(t, contracts.MiMoEventMessagePartUpdated, map[string]any{
		"sessionID": testSessionID,
		"part": map[string]any{
			"id": partID, "messageID": messageID, "sessionID": testSessionID, "type": contracts.MiMoPartTypeTool,
			"tool": tool, "callID": callID, "state": state,
		},
	})
}

// rowTypes decodes the `type` of each persisted row, which an assembled
// message also carries, so a test can assert the order of the rows.
func rowTypes(t *testing.T, messages []agenttest.Message) []string {
	t.Helper()
	types := make([]string, 0, len(messages))
	for _, message := range messages {
		var row struct {
			Type string `json:"type"`
		}
		require.NoError(t, json.Unmarshal(message.Content, &row), "row %s", message.Content)
		types = append(types, row.Type)
	}
	return types
}

// sortedKeys returns the keys of m in order, so a test can compare the ids that
// the agent keeps.
func sortedKeys[V any](m map[string]V) []string {
	return slices.Sorted(maps.Keys(m))
}

// waitFor waits for condition, which another goroutine of the agent makes
// true.
func waitFor(t *testing.T, condition func() bool, message string) {
	t.Helper()
	require.Eventually(t, condition, testTimeout, 5*time.Millisecond, message)
}
