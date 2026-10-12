package letta

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/quartz"
	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

// The runtime_start handshake must settle before Start returns its agent.
//
// The App Server DROPS an `input` whose runtime scope is empty: no
// `input_accepted`, no `loop_error`, nothing at all. The worker then logs only
// silence and the turn never starts. Start used to send `runtime_start` and
// return without waiting, so the worker dispatched queued user input while the
// scope was still empty -- 98ms after start in the E2E, 365ms before the
// response arrived.

// lettaLiveRuntimeStartResponse is a verbatim runtime_start_response from a
// live `letta server --listen` run (ids shortened).
const lettaLiveRuntimeStartResponse = `{"type":"runtime_start_response","request_id":"rs-1","success":true,"runtime":{"agent_id":"agent-local-ae1e0bbf-7eab-4dce-9182-64e3106c755d","conversation_id":"local-conv-1"},"agent":{"id":"agent-local-ae1e0bbf-7eab-4dce-9182-64e3106c755d"},"conversation":{"id":"local-conv-1"},"created":true}`

// fakeAppServer is one `letta server --listen` that answers on its WebSocket
// only when the test tells it to. It records every command the driver writes.
type fakeAppServer struct {
	server   *httptest.Server
	commands chan map[string]any
	replies  chan []byte
}

// newFakeAppServer starts the server and dials the driver's socket to it.
func newFakeAppServer(t *testing.T) (*fakeAppServer, *Agent) {
	t.Helper()
	return newFakeAppServerWithSink(t, &agenttest.Sink{})
}

// newFakeAppServerWithSink is newFakeAppServer for a test that reads the rows
// that the agent stores.
func newFakeAppServerWithSink(t *testing.T, sink *agenttest.Sink) (*fakeAppServer, *Agent) {
	t.Helper()
	fake := &fakeAppServer{
		commands: make(chan map[string]any, 8),
		replies:  make(chan []byte, 8),
	}
	upgrader := func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()
		go func() {
			for reply := range fake.replies {
				if err := conn.Write(r.Context(), websocket.MessageText, reply); err != nil {
					return
				}
			}
		}()
		for {
			_, data, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			var command map[string]any
			if json.Unmarshal(data, &command) != nil {
				continue
			}
			fake.commands <- command
		}
	}
	fake.server = httptest.NewServer(http.HandlerFunc(upgrader))
	t.Cleanup(fake.server.Close)

	ctx, cancel := context.WithCancel(t.Context())
	conn, err := dial(ctx, "ws"+strings.TrimPrefix(fake.server.URL, "http"))
	require.NoError(t, err)
	t.Cleanup(func() {
		conn.close()
		cancel()
	})

	a := &Agent{
		Process: providerkit.NewProcessFrom(providerkit.ProcessConfig{
			AgentID:      "worker-agent-1",
			ProviderName: "letta",
			Ctx:          ctx,
			Cancel:       cancel,
			Stdin:        nopWriteCloser{},
		}),
		sink:  agent.NewProviderServices(sink),
		ws:    conn,
		clock: quartz.NewReal(),
	}
	// The readLoop is what dispatches the response onto adoptRuntime. Start
	// starts it before openConversation; the test must too.
	go a.readLoop(ctx, conn)
	return fake, a
}

// newTestOptions returns the options openConversation reads.
func newTestOptions() agent.Options {
	return agent.Options{
		AgentID:        "worker-agent-1",
		StartupTimeout: 30 * time.Second,
		Options:        optionmap.Map{agent.OptionIDModel: "openai-compatible/letta-e2e"},
	}
}

// nopWriteCloser is the stdin of an agent that speaks only over its socket.
type nopWriteCloser struct{}

func (nopWriteCloser) Write(p []byte) (int, error) { return len(p), nil }
func (nopWriteCloser) Close() error                { return nil }

// nextCommand returns the command the driver wrote next.
func (f *fakeAppServer) nextCommand(t *testing.T) map[string]any {
	t.Helper()
	select {
	case command := <-f.commands:
		return command
	case <-time.After(30 * time.Second):
		t.Fatal("the driver wrote no command")
		return nil
	}
}

func (f *fakeAppServer) answerModelCatalog(t *testing.T) {
	t.Helper()
	command := f.nextCommand(t)
	require.Equal(t, "list_models", command["type"])
	f.replies <- []byte(fmt.Sprintf(`{"type":"list_models_response","request_id":%q,"success":true,"entries":[]}`, command["request_id"]))
}

// TestOpenConversationWaitsForTheRuntimeIdentity pins the handshake the input
// path depends on. openConversation must not return before the response states
// the runtime identity, because Start returning is what lets the worker deliver
// a user message, and an input with an empty scope is dropped without an error.
func TestOpenConversationWaitsForTheRuntimeIdentity(t *testing.T) {
	t.Parallel()
	fake, a := newFakeAppServer(t)

	returned := make(chan error, 1)
	go func() { returned <- a.openConversation(newTestOptions()) }()

	command := fake.nextCommand(t)
	assert.Equal(t, "runtime_start", command["type"], "the handshake opens with runtime_start")
	assert.Contains(t, command, "create_agent", "a fresh agent is created through create_agent")

	// The command is out and the response has not been sent. openConversation
	// cannot return yet: only the response settles the handshake.
	select {
	case err := <-returned:
		t.Fatalf("openConversation returned before the runtime_start response: %v", err)
	default:
	}

	fake.replies <- []byte(lettaLiveRuntimeStartResponse)
	fake.answerModelCatalog(t)
	select {
	case err := <-returned:
		require.NoError(t, err, "the response settles the handshake")
	case <-time.After(30 * time.Second):
		t.Fatal("openConversation did not return after the runtime_start response")
	}

	a.Mu.Lock()
	agentID, conversationID := a.agentID, a.conversationID
	a.Mu.Unlock()
	assert.Equal(t, "agent-local-ae1e0bbf-7eab-4dce-9182-64e3106c755d", agentID, "the server's agent id is adopted")
	assert.Equal(t, "local-conv-1", conversationID, "the server's conversation id is adopted")

	// An input sent now addresses the runtime, so it goes out with the scope.
	scope := a.runtime()
	assert.Equal(t, "agent-local-ae1e0bbf-7eab-4dce-9182-64e3106c755d", scope.AgentID)
	assert.Equal(t, "local-conv-1", scope.ConversationID)
}

func TestOpenConversationLoadsModelsBeforeItReturns(t *testing.T) {
	t.Parallel()
	fake, a := newFakeAppServer(t)
	returned := make(chan error, 1)
	go func() { returned <- a.openConversation(newTestOptions()) }()

	assert.Equal(t, "runtime_start", fake.nextCommand(t)["type"])
	fake.replies <- []byte(lettaLiveRuntimeStartResponse)
	command := fake.nextCommand(t)
	assert.Equal(t, "list_models", command["type"])
	select {
	case err := <-returned:
		t.Fatalf("openConversation returned before the model catalog: %v", err)
	default:
	}
	fake.replies <- []byte(fmt.Sprintf(`{"type":"list_models_response","request_id":%q,"success":true,"entries":[{"id":"letta-e2e","handle":"openai-compatible/letta-e2e","label":"Letta E2E"},{"id":"alternate","handle":"openai-compatible/alternate","label":"Alternate"}],"available_handles":["openai-compatible/letta-e2e","openai-compatible/alternate"]}`, command["request_id"]))
	select {
	case err := <-returned:
		require.NoError(t, err)
	case <-time.After(30 * time.Second):
		t.Fatal("openConversation did not return after the model catalog")
	}
	var models []string
	for _, group := range a.OptionGroups() {
		if group.GetId() != agent.OptionIDModel {
			continue
		}
		for _, option := range group.GetOptions() {
			models = append(models, option.GetId())
		}
	}
	assert.Equal(t, []string{"openai-compatible/letta-e2e", "openai-compatible/alternate"}, models)
}

func TestUpdateSettingsWaitsForNativeModelReply(t *testing.T) {
	t.Parallel()
	fake, a := newFakeAppServer(t)
	a.Mu.Lock()
	a.agentID = "agent-local-1"
	a.conversationID = "local-conv-1"
	a.settings.model = "openai-compatible/letta-e2e"
	a.Mu.Unlock()
	returned := make(chan agent.SettingsApplyResult, 1)
	go func() {
		returned <- a.UpdateSettings(optionmap.Map{agent.OptionIDModel: "openai-compatible/alternate"})
	}()

	command := fake.nextCommand(t)
	assert.Equal(t, "update_model", command["type"])
	payload, ok := command["payload"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "openai-compatible/alternate", payload["model_handle"])
	assert.NotContains(t, payload, "model")
	select {
	case <-returned:
		t.Fatal("UpdateSettings confirmed the model before the native reply")
	default:
	}
	fake.replies <- []byte(fmt.Sprintf(`{"type":"update_model_response","request_id":%q,"success":true,"model_handle":"openai-compatible/alternate"}`, command["request_id"]))
	select {
	case result := <-returned:
		assert.Equal(t, "openai-compatible/alternate", result.ConfirmedOptions()[agent.OptionIDModel])
	case <-time.After(30 * time.Second):
		t.Fatal("UpdateSettings did not return after the native model reply")
	}
}

func TestUpdateSettingsKeepsTheCurrentModelAfterNativeRefusal(t *testing.T) {
	t.Parallel()
	fake, a := newFakeAppServer(t)
	a.Mu.Lock()
	a.agentID = "agent-local-1"
	a.conversationID = "local-conv-1"
	a.settings.model = "openai-compatible/one"
	a.Mu.Unlock()
	returned := make(chan agent.SettingsApplyResult, 1)
	go func() {
		returned <- a.UpdateSettings(optionmap.Map{agent.OptionIDModel: "openai-compatible/invalid"})
	}()
	command := fake.nextCommand(t)
	fake.replies <- []byte(fmt.Sprintf(`{"type":"update_model_response","request_id":%q,"success":false,"error":"model not found"}`, command["request_id"]))
	select {
	case result := <-returned:
		assert.Equal(t, "openai-compatible/one", result.ConfirmedOptions()[agent.OptionIDModel])
	case <-time.After(30 * time.Second):
		t.Fatal("UpdateSettings did not return after the refused native model")
	}
	assert.Equal(t, "openai-compatible/one", a.SettingsSnapshot().ConfirmedOptions()[agent.OptionIDModel])
}

func TestUpdateSettingsEffortOnlyIncludesTheCurrentModel(t *testing.T) {
	t.Parallel()
	fake, a := newFakeAppServer(t)
	a.Mu.Lock()
	a.agentID = "agent-local-1"
	a.conversationID = "local-conv-1"
	a.settings.model = "openai-compatible/one"
	a.Mu.Unlock()
	returned := make(chan agent.SettingsApplyResult, 1)
	go func() {
		returned <- a.UpdateSettings(optionmap.Map{agent.OptionIDEffort: "high"})
	}()
	command := fake.nextCommand(t)
	payload, ok := command["payload"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "openai-compatible/one", payload["model_handle"])
	assert.Equal(t, "high", payload["reasoning_effort"])
	fake.replies <- []byte(fmt.Sprintf(`{"type":"update_model_response","request_id":%q,"success":true,"model_handle":"openai-compatible/one"}`, command["request_id"]))
	select {
	case result := <-returned:
		assert.Equal(t, "high", result.ConfirmedOptions()[agent.OptionIDEffort])
	case <-time.After(30 * time.Second):
		t.Fatal("UpdateSettings did not return after the effort reply")
	}
}

func TestUpdateSettingsEmptyModelKeepsTheCurrentModel(t *testing.T) {
	t.Parallel()
	_, a := newFakeAppServer(t)
	a.Mu.Lock()
	a.agentID = "agent-local-1"
	a.conversationID = "local-conv-1"
	a.settings.model = "openai-compatible/one"
	a.Mu.Unlock()

	result := a.UpdateSettings(optionmap.Map{agent.OptionIDModel: ""})
	assert.Equal(t, "openai-compatible/one", result.ConfirmedOptions()[agent.OptionIDModel])
}

func TestUpdateSettingsEmptyEffortRestoresTheProviderDefault(t *testing.T) {
	t.Parallel()
	fake, a := newFakeAppServer(t)
	a.Mu.Lock()
	a.agentID = "agent-local-1"
	a.conversationID = "local-conv-1"
	a.settings.model = "openai-compatible/one"
	a.settings.reasoningLevel = "high"
	a.Mu.Unlock()
	returned := make(chan agent.SettingsApplyResult, 1)
	go func() {
		returned <- a.UpdateSettings(optionmap.Map{agent.OptionIDEffort: ""})
	}()
	command := fake.nextCommand(t)
	payload, ok := command["payload"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "openai-compatible/one", payload["model_handle"])
	value, offered := payload["reasoning_effort"]
	assert.True(t, offered)
	assert.Nil(t, value)
	fake.replies <- []byte(fmt.Sprintf(`{"type":"update_model_response","request_id":%q,"success":true,"model_handle":"openai-compatible/one"}`, command["request_id"]))
	select {
	case result := <-returned:
		settlement := result.Settlements[agent.OptionIDEffort]
		assert.Equal(t, agent.OptionSettlementConfirmed, settlement.State)
		require.NotNil(t, settlement.Value)
		assert.Empty(t, *settlement.Value)
	case <-time.After(30 * time.Second):
		t.Fatal("UpdateSettings did not return after the effort reset")
	}
}

func TestUpdateSettingsAppliesModelWhenTheModeIsUnchanged(t *testing.T) {
	t.Parallel()
	fake, a := newFakeAppServer(t)
	a.Mu.Lock()
	a.agentID = "agent-local-1"
	a.conversationID = "local-conv-1"
	a.settings.model = "openai-compatible/one"
	a.settings.permissionMode = "unrestricted"
	a.Mu.Unlock()
	returned := make(chan agent.SettingsApplyResult, 1)
	go func() {
		returned <- a.UpdateSettings(optionmap.Map{
			agent.OptionIDModel:          "openai-compatible/two",
			agent.OptionIDPermissionMode: "unrestricted",
		})
	}()
	var command map[string]any
	select {
	case command = <-fake.commands:
	case <-returned:
		t.Fatal("an unchanged mode prevented the live model write")
	case <-time.After(30 * time.Second):
		t.Fatal("the driver wrote no model command")
	}
	assert.Equal(t, "update_model", command["type"])
	fake.replies <- []byte(fmt.Sprintf(`{"type":"update_model_response","request_id":%q,"success":true,"model_handle":"openai-compatible/two"}`, command["request_id"]))
	select {
	case result := <-returned:
		assert.Equal(t, "openai-compatible/two", result.ConfirmedOptions()[agent.OptionIDModel])
		assert.Equal(t, "unrestricted", result.ConfirmedOptions()[agent.OptionIDPermissionMode])
	case <-time.After(30 * time.Second):
		t.Fatal("UpdateSettings did not return after the native model reply")
	}
}

// TestOpenConversationFailsWhenRuntimeStartRefuses pins that a refused
// handshake fails Start rather than returning an agent that cannot take input.
func TestOpenConversationFailsWhenRuntimeStartRefuses(t *testing.T) {
	t.Parallel()
	fake, a := newFakeAppServer(t)

	returned := make(chan error, 1)
	go func() { returned <- a.openConversation(newTestOptions()) }()

	fake.nextCommand(t)
	fake.replies <- []byte(`{"type":"runtime_start_response","request_id":"rs-1","success":false,"error":"401: no such agent"}`)

	select {
	case err := <-returned:
		require.Error(t, err, "a refused runtime_start fails the handshake")
		assert.Contains(t, err.Error(), "401", "the failure carries the server's reason")
	case <-time.After(30 * time.Second):
		t.Fatal("openConversation did not return after the refused runtime_start")
	}
}

// TestOpenConversationFailsWhenRuntimeStartNamesNoRuntime pins that a response
// without a runtime fails the handshake: there is nothing to address input to.
func TestOpenConversationFailsWhenRuntimeStartNamesNoRuntime(t *testing.T) {
	t.Parallel()
	fake, a := newFakeAppServer(t)

	returned := make(chan error, 1)
	go func() { returned <- a.openConversation(newTestOptions()) }()

	fake.nextCommand(t)
	fake.replies <- []byte(`{"type":"runtime_start_response","request_id":"rs-1","success":true}`)

	select {
	case err := <-returned:
		require.Error(t, err, "a response with no runtime fails the handshake")
	case <-time.After(30 * time.Second):
		t.Fatal("openConversation did not return after the empty runtime_start response")
	}
}

// TestSendInputRefusesAnAddresslessInput pins the backstop under the handshake
// wait. An input with an empty runtime scope is dropped by the server with no
// reply, so the driver must refuse it rather than send it.
func TestSendInputRefusesAnAddresslessInput(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := &Agent{sink: agent.NewProviderServices(sink)}

	err := a.SendInput("This input has no runtime to address.", nil)
	require.Error(t, err, "an input with no runtime identity is refused")
	assert.Contains(t, err.Error(), "runtime", "the refusal states the missing identity")
	assert.Empty(t, sink.TurnActives(), "a refused input arms no turn")
}

// TestSendRawInputSendsTheAnswerOverTheSocket pins the control-answer path.
//
// The base implementation writes to the process's STDIN, which `letta server`
// does not read for protocol_v2: the approval then never arrives and the tool
// call hangs for the rest of the turn. The answer must travel as the PAYLOAD of
// an `input` command, addressed to the runtime.
func TestSendRawInputSendsTheAnswerOverTheSocket(t *testing.T) {
	t.Parallel()
	fake, a := newFakeAppServer(t)
	a.Mu.Lock()
	a.agentID = "agent-local-1"
	a.conversationID = "local-conv-1"
	a.Mu.Unlock()

	answer := []byte(`{"kind":"approval_response","request_id":"perm-call-1","decision":{"behavior":"allow"}}`)
	require.NoError(t, a.SendRawInput(answer, agent.StopContext{}))

	command := fake.nextCommand(t)
	assert.Equal(t, "input", command["type"], "the answer travels as an input command")
	scope, _ := command["runtime"].(map[string]any)
	require.NotNil(t, scope, "the input is addressed to a runtime")
	assert.Equal(t, "agent-local-1", scope["agent_id"])
	assert.Equal(t, "local-conv-1", scope["conversation_id"])
	payload, _ := command["payload"].(map[string]any)
	require.NotNil(t, payload, "the answer is the input's payload")
	assert.Equal(t, "approval_response", payload["kind"])
	assert.Equal(t, "perm-call-1", payload["request_id"])
}

// TestSendRawInputRefusesANonAnswer pins that the raw path carries only an
// approval answer. A user message takes SendInput, which arms the turn.
func TestSendRawInputRefusesANonAnswer(t *testing.T) {
	t.Parallel()
	fake, a := newFakeAppServer(t)
	a.Mu.Lock()
	a.agentID = "agent-local-1"
	a.conversationID = "local-conv-1"
	a.Mu.Unlock()

	require.Error(t, a.SendRawInput([]byte(`{"kind":"create_message"}`), agent.StopContext{}), "a non-answer is refused")
	require.Error(t, a.SendRawInput([]byte(`not json`), agent.StopContext{}), "an unparseable answer is refused")
	select {
	case command := <-fake.commands:
		t.Fatalf("a refused answer still wrote a command: %v", command)
	default:
	}
}
