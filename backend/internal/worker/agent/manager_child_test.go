//go:build unix

// Depends on stubProvider (defined in manager_test.go, unix-only).

package agent

import (
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// steerableStub implements both the Agent provider surface (via stubProvider)
// and ChildSteerer, so Manager.SendChildInput/InterruptChild can reach it.
type steerableStub struct {
	stubProvider
	sendInputErr   error
	interruptErr   error
	sendInputCalls []sendInputCall
	interruptCalls []string
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

func (s *steerableStub) InterruptChild(childKey string) error {
	s.interruptCalls = append(s.interruptCalls, childKey)
	return s.interruptErr
}

// Ensure stubProvider stays compatible (this catches an interface drift at
// compile time). steerableStub embeds stubProvider; adding ChildSteerer makes
// it satisfy the type-assert in Manager.SendChildInput.
var _ ChildSteerer = (*steerableStub)(nil)
var _ Agent = (*steerableStub)(nil)

func TestManager_SendChildInputNotRunning(t *testing.T) {
	t.Parallel()
	m := NewManager(nil)
	err := m.SendChildInput("nope", "child-1", "hello", nil)
	assert.ErrorIs(t, err, ErrAgentNotFound)
}

func TestManager_SendChildInputUnsupportedProvider(t *testing.T) {
	t.Parallel()
	m := NewManager(nil)
	m.mu.Lock()
	m.agents["root"] = &stubProvider{}
	m.mu.Unlock()
	err := m.SendChildInput("root", "child-1", "hello", nil)
	assert.ErrorIs(t, err, ErrChildSteeringUnsupported)
}

func TestManager_SendChildInputDispatch(t *testing.T) {
	t.Parallel()
	m := NewManager(nil)
	st := &steerableStub{}
	m.mu.Lock()
	m.agents["root"] = st
	m.mu.Unlock()

	atts := []*leapmuxv1.Attachment{{Filename: "a.txt"}}
	err := m.SendChildInput("root", "child-1", "hello", atts)
	require.NoError(t, err)
	require.Len(t, st.sendInputCalls, 1)
	assert.Equal(t, "child-1", st.sendInputCalls[0].childKey)
	assert.Equal(t, "hello", st.sendInputCalls[0].content)
	assert.Equal(t, 1, st.sendInputCalls[0].attachments)
}

func TestManager_InterruptChildDispatch(t *testing.T) {
	t.Parallel()
	m := NewManager(nil)
	st := &steerableStub{interruptErr: nil}
	m.mu.Lock()
	m.agents["root"] = st
	m.mu.Unlock()

	require.NoError(t, m.InterruptChild("root", "child-2"))
	require.Len(t, st.interruptCalls, 1)
	assert.Equal(t, "child-2", st.interruptCalls[0])
}

func TestManager_InterruptChildUnsupportedProvider(t *testing.T) {
	t.Parallel()
	m := NewManager(nil)
	m.mu.Lock()
	m.agents["root"] = &stubProvider{}
	m.mu.Unlock()
	err := m.InterruptChild("root", "child-1")
	assert.ErrorIs(t, err, ErrChildSteeringUnsupported)
}
