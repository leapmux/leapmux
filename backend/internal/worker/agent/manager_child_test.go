//go:build unix

// Depends on stubProvider (defined in manager_test.go, unix-only).

package agent_test

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// steerableStub implements the Agent provider surface, ChildSteerer, and
// ChildInterrupter.
type steerableStub struct {
	stubProvider
	sendInputErr        error
	interruptErr        error
	activeTurnSteerable bool
	sendInputCalls      []sendInputCall
	steerInputCalls     []sendInputCall
	interruptCalls      []string
}

type sendInputCall struct {
	childKey    string
	content     string
	attachments int
}

func (s *steerableStub) SendChildInput(childKey string, input agent.ChildInput) (agent.ChildInputResult, error) {
	s.sendInputCalls = append(s.sendInputCalls, sendInputCall{
		childKey:    childKey,
		content:     input.Content,
		attachments: len(input.Attachments),
	})
	return agent.ChildInputResult{StartsTurn: true}, s.sendInputErr
}

func (s *steerableStub) SteerChildInput(childKey string, request agent.SteerRequest) (agent.NativeInputReceipt, error) {
	s.steerInputCalls = append(s.steerInputCalls, sendInputCall{
		childKey: childKey, content: request.Content, attachments: len(request.Attachments),
	})
	return agent.NativeInputReceipt{}, s.sendInputErr
}

func (*steerableStub) ChildSteerPolicy() agent.SteerPolicy { return agent.SteerPolicy{} }

func (s *steerableStub) InterruptChild(childKey string, stop agent.StopContext) error {
	s.interruptCalls = append(s.interruptCalls, childKey)
	return s.interruptErr
}

func (s *steerableStub) ActiveChildTurnState(string) agent.TurnState {
	return agent.TurnState{Active: s.activeTurnSteerable, Steerable: s.activeTurnSteerable}
}

// Ensure stubProvider stays compatible (this catches an interface drift at
// compile time). steerableStub embeds stubProvider; adding ChildSteerer makes
// it satisfy the type-assert in Manager.SendChildInput.
var _ agent.ChildSteerer = (*steerableStub)(nil)
var _ agent.ChildInterrupter = (*steerableStub)(nil)
var _ agent.Agent = (*steerableStub)(nil)

func TestManager_SendChildInputNotRunning(t *testing.T) {
	t.Parallel()
	m := agent.NewManager(testRegistry, nil)
	err := agenttest.SendChildInputToOwner(m, "nope", "child-1", agent.ChildInput{Content: "hello"})
	assert.ErrorIs(t, err, agent.ErrAgentNotFound)
}

func TestManager_SendChildInputUnsupportedProvider(t *testing.T) {
	t.Parallel()
	m := agent.NewManager(testRegistry, nil)
	m.PutAgentForTest("root", &stubProvider{})
	err := agenttest.SendChildInputToOwner(m, "root", "child-1", agent.ChildInput{Content: "hello"})
	assert.ErrorIs(t, err, agent.ErrChildOperationUnsupported)
}

func TestManager_SendChildInputDispatch(t *testing.T) {
	t.Parallel()
	m := agent.NewManager(testRegistry, nil)
	st := &steerableStub{}
	m.PutAgentForTest("root", st)

	atts := []*leapmuxv1.Attachment{{Filename: "a.txt"}}
	err := agenttest.SendChildInputToOwner(m, "root", "child-1", agent.ChildInput{Content: "hello", Attachments: atts})
	require.NoError(t, err)
	require.Len(t, st.sendInputCalls, 1)
	assert.Equal(t, "child-1", st.sendInputCalls[0].childKey)
	assert.Equal(t, "hello", st.sendInputCalls[0].content)
	assert.Equal(t, 1, st.sendInputCalls[0].attachments)
}

func TestManager_SendChildInputPreservesABusyTurnKind(t *testing.T) {
	t.Parallel()
	m := agent.NewManager(testRegistry, nil)
	st := &steerableStub{
		sendInputErr:        agent.ErrAgentBusy,
		activeTurnSteerable: true,
	}
	m.PutAgentForTest("root", st)

	err := agenttest.SendChildInputToOwner(m, "root", "child-1", agent.ChildInput{Content: "hello"})
	var busyErr *agent.AgentBusyError
	require.ErrorAs(t, err, &busyErr)
	assert.True(t, busyErr.ActiveTurnSteerable)
}

func TestManager_SteerChildInputDispatch(t *testing.T) {
	t.Parallel()
	m := agent.NewManager(testRegistry, nil)
	st := &steerableStub{}
	m.PutAgentForTest("root", st)

	receipt, err := m.SteerChildInput("root", "child-1", agent.SteerRequest{InputID: "input-1", Content: "guide"})
	require.NoError(t, err)
	assert.Empty(t, receipt)
	require.Len(t, st.steerInputCalls, 1)
	assert.Equal(t, "child-1", st.steerInputCalls[0].childKey)
	assert.Equal(t, "guide", st.steerInputCalls[0].content)
}

func TestManager_SteerChildInputRejectsEmptyInputID(t *testing.T) {
	t.Parallel()
	m := agent.NewManager(testRegistry, nil)
	provider := &echoChildSteerer{}
	m.PutAgentForTest("owner", provider)

	receipt, err := m.SteerChildInput("owner", "child-key", agent.SteerRequest{Content: "guide"})
	require.Error(t, err)
	assert.Empty(t, receipt)
	assert.Empty(t, provider.childKey)
	assert.Empty(t, provider.steerInputCalls)
}

type echoChildSteerer struct {
	steerableStub
	childKey string
	request  agent.SteerRequest
}

func (s *echoChildSteerer) SteerChildInput(childKey string, request agent.SteerRequest) (agent.NativeInputReceipt, error) {
	s.childKey, s.request = childKey, request
	return agent.NativeInputReceipt{}, nil
}

func (*echoChildSteerer) ChildSteerPolicy() agent.SteerPolicy {
	return agent.SteerPolicy{RowAtEcho: true, SurvivesTurnEnd: true}
}

func TestManagerChildEchoKeepsTheDispatchIdentity(t *testing.T) {
	t.Parallel()
	m := agent.NewManager(testRegistry, nil)
	provider := &echoChildSteerer{}
	m.PutAgentForTest("owner", provider)
	assert.Equal(t, agent.SteerPolicy{RowAtEcho: true, SurvivesTurnEnd: true}, m.ChildSteerPolicy("owner"))
	assert.Empty(t, m.SteerPolicy("owner"))
	request := agent.SteerRequest{InputID: "input", AttemptID: "dispatch", Content: "Guide the child."}
	receipt, err := m.SteerChildInput("owner", "child-key", request)
	require.NoError(t, err)
	assert.Empty(t, receipt)
	assert.Equal(t, "child-key", provider.childKey)
	assert.Equal(t, request, provider.request)
	assert.Empty(t, provider.steerInputCalls)
}

func TestManager_InterruptChildDispatch(t *testing.T) {
	t.Parallel()
	m := agent.NewManager(testRegistry, nil)
	st := &steerableStub{interruptErr: nil}
	m.PutAgentForTest("root", st)

	require.NoError(t, m.InterruptChild("root", "child-2", agent.StopContext{}))
	require.Len(t, st.interruptCalls, 1)
	assert.Equal(t, "child-2", st.interruptCalls[0])
}

func TestManager_InterruptChildUnsupportedProvider(t *testing.T) {
	t.Parallel()
	m := agent.NewManager(testRegistry, nil)
	m.PutAgentForTest("root", &stubProvider{})
	err := m.InterruptChild("root", "child-1", agent.StopContext{})
	assert.ErrorIs(t, err, agent.ErrChildOperationUnsupported)
}
