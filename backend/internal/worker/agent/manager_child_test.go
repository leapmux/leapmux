//go:build unix

// Depends on stubProvider (defined in manager_test.go, unix-only).

package agent_test

import (
	"testing"

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

func (s *steerableStub) SendChildInput(childKey, content string, attachments []*leapmuxv1.Attachment) error {
	s.sendInputCalls = append(s.sendInputCalls, sendInputCall{
		childKey:    childKey,
		content:     content,
		attachments: len(attachments),
	})
	return s.sendInputErr
}

func (s *steerableStub) SteerChildInput(childKey, content string, attachments []*leapmuxv1.Attachment) error {
	s.steerInputCalls = append(s.steerInputCalls, sendInputCall{
		childKey: childKey, content: content, attachments: len(attachments),
	})
	return s.sendInputErr
}

func (s *steerableStub) InterruptChild(childKey string) error {
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
	err := m.SendChildInput("nope", "child-1", "hello", nil)
	assert.ErrorIs(t, err, agent.ErrAgentNotFound)
}

func TestManager_SendChildInputUnsupportedProvider(t *testing.T) {
	t.Parallel()
	m := agent.NewManager(testRegistry, nil)
	m.PutAgentForTest("root", &stubProvider{})
	err := m.SendChildInput("root", "child-1", "hello", nil)
	assert.ErrorIs(t, err, agent.ErrChildOperationUnsupported)
}

func TestManager_SendChildInputDispatch(t *testing.T) {
	t.Parallel()
	m := agent.NewManager(testRegistry, nil)
	st := &steerableStub{}
	m.PutAgentForTest("root", st)

	atts := []*leapmuxv1.Attachment{{Filename: "a.txt"}}
	err := m.SendChildInput("root", "child-1", "hello", atts)
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

	err := m.SendChildInput("root", "child-1", "hello", nil)
	var busyErr *agent.AgentBusyError
	require.ErrorAs(t, err, &busyErr)
	assert.True(t, busyErr.ActiveTurnSteerable)
}

func TestManager_SteerChildInputDispatch(t *testing.T) {
	t.Parallel()
	m := agent.NewManager(testRegistry, nil)
	st := &steerableStub{}
	m.PutAgentForTest("root", st)

	require.NoError(t, m.SteerChildInput("root", "child-1", "guide", nil))
	require.Len(t, st.steerInputCalls, 1)
	assert.Equal(t, "child-1", st.steerInputCalls[0].childKey)
	assert.Equal(t, "guide", st.steerInputCalls[0].content)
}

func TestManager_InterruptChildDispatch(t *testing.T) {
	t.Parallel()
	m := agent.NewManager(testRegistry, nil)
	st := &steerableStub{interruptErr: nil}
	m.PutAgentForTest("root", st)

	require.NoError(t, m.InterruptChild("root", "child-2"))
	require.Len(t, st.interruptCalls, 1)
	assert.Equal(t, "child-2", st.interruptCalls[0])
}

func TestManager_InterruptChildUnsupportedProvider(t *testing.T) {
	t.Parallel()
	m := agent.NewManager(testRegistry, nil)
	m.PutAgentForTest("root", &stubProvider{})
	err := m.InterruptChild("root", "child-1")
	assert.ErrorIs(t, err, agent.ErrChildOperationUnsupported)
}
