package claude

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/require"
)

func TestClaudeInputWriteKeepsStateAndStopAvailable(t *testing.T) {
	provider := &Agent{sink: agent.NewProviderServices(&agenttest.Sink{})}
	unlocked := false
	provider.SetStdinForTest(inspectingInputWriter{inspect: func() {
		unlocked = provider.Mu.TryLock()
		if unlocked {
			provider.Mu.Unlock()
		}
	}})
	require.NoError(t, agenttest.SendInput(provider, "input", nil))
	require.True(t, unlocked, "a blocked stdin write must not prevent Stop from acquiring the process mutex")
}

func TestClaudeInputDoesNotReopenATurnThatEndsDuringTheWrite(t *testing.T) {
	provider := &Agent{sink: agent.NewProviderServices(&agenttest.Sink{})}
	provider.SetStdinForTest(inspectingInputWriter{inspect: func() {
		if provider.Mu.TryLock() {
			provider.Mu.Unlock()
			provider.HandleOutput([]byte(`{"type":"result","subtype":"success"}`))
		}
	}})
	require.NoError(t, agenttest.SendInput(provider, "input", nil))
	require.False(t, provider.PublishTurnActive().Active)
	provider.Mu.Lock()
	awaited := provider.awaitedInputRevision
	provider.Mu.Unlock()
	require.Zero(t, awaited)
}

type inspectingInputWriter struct {
	inspect func()
}

func (writer inspectingInputWriter) Write(data []byte) (int, error) {
	writer.inspect()
	return len(data), nil
}

func (inspectingInputWriter) Close() error { return nil }
