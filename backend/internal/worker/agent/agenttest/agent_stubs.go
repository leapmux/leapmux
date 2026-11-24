package agenttest

import (
	"sync"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// IdleAgent is the smallest Agent: every method answers the neutral value and
// does nothing. A test embeds it and overrides the one method it is about. It
// builds on every platform, so a test with no platform behaviour runs in the
// Windows job too.
type IdleAgent struct{}

func (IdleAgent) AgentID() string { return "idle" }

func (IdleAgent) SendInput(string, []*leapmuxv1.Attachment) error { return nil }

func (IdleAgent) SendInputForSession(string, string, []*leapmuxv1.Attachment) error {
	return agent.ErrInputSessionChanged
}

func (IdleAgent) PublishTurnActive() agent.TurnState {
	return agent.TurnState{}
}

func (IdleAgent) SendRawInput(agent.RawInput) error { return nil }

func (IdleAgent) Stop() {}

func (IdleAgent) IsStopped() bool { return false }

func (IdleAgent) DiscardOutput() {}

func (IdleAgent) ClearContext() (string, error) { return "", agent.ErrContextClearUnsupported }

func (IdleAgent) Wait() error { return nil }

func (IdleAgent) Stderr() string { return "" }

func (IdleAgent) HandleOutput([]byte) {}

func (IdleAgent) OptionGroups() []*leapmuxv1.AvailableOptionGroup { return nil }

func (IdleAgent) SettingsSnapshot() agent.SettingsApplyResult { return agent.ConfirmedSettings(nil) }

func (IdleAgent) UpdateSettings(options optionmap.Map) agent.SettingsApplyResult {
	return agent.ConfirmedSettings(options)
}

func (IdleAgent) Interrupt(stop agent.StopContext) error { return nil }

// GroupsAgent is an IdleAgent that reports groups as its live option groups.
type GroupsAgent struct {
	IdleAgent
	groups []*leapmuxv1.AvailableOptionGroup
}

func (a GroupsAgent) OptionGroups() []*leapmuxv1.AvailableOptionGroup { return a.groups }

// RunningAgent is an IdleAgent whose Wait blocks until Stop is called.
// The Manager drops any agent whose Wait returns, so tests that need a live
// registered agent embed or construct RunningAgent instead of IdleAgent.
type RunningAgent struct {
	IdleAgent
	id       string
	stopped  chan struct{}
	stopOnce sync.Once
}

// NewRunningAgent returns a RunningAgent that blocks in Wait until Stop is called.
func NewRunningAgent(id string) *RunningAgent {
	return &RunningAgent{
		id:      id,
		stopped: make(chan struct{}),
	}
}

func (a *RunningAgent) AgentID() string {
	if a.id != "" {
		return a.id
	}
	return a.IdleAgent.AgentID()
}

func (a *RunningAgent) Stop() {
	a.stopOnce.Do(func() { close(a.stopped) })
}

func (a *RunningAgent) Wait() error {
	<-a.stopped
	return nil
}

func (a *RunningAgent) IsStopped() bool {
	select {
	case <-a.stopped:
		return true
	default:
		return false
	}
}
