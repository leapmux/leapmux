package agent

import "fmt"

// agentRegistration identifies one provider instance without comparing Agent values.
// Manager.mu guards done and exiting. The provider remains fixed.
type agentRegistration struct {
	provider Agent
	done     chan struct{}
	exiting  bool
}

func (r *agentRegistration) providerOrNil() Agent {
	if r == nil {
		return nil
	}
	return r.provider
}

// StopTarget retains the provider instance that received one stop request.
// A replacement registration never changes this target.
type StopTarget struct {
	manager *Manager
	agentID string
	entry   *agentRegistration
}

func (m *Manager) CaptureStopTarget(agentID string) (StopTarget, error) {
	m.mu.RLock()
	entry := m.agents[agentID]
	available := entry != nil && !entry.exiting
	m.mu.RUnlock()
	if !available {
		return StopTarget{}, fmt.Errorf("%w: %s", ErrAgentNotFound, agentID)
	}
	return StopTarget{manager: m, agentID: agentID, entry: entry}, nil
}

func (t StopTarget) IsCurrent() bool {
	if t.manager == nil || t.entry == nil {
		return false
	}
	t.manager.mu.RLock()
	defer t.manager.mu.RUnlock()
	return t.manager.agents[t.agentID] == t.entry && !t.entry.exiting
}

func (t StopTarget) Interrupt(stop StopContext) error {
	if t.entry == nil {
		return ErrAgentNotFound
	}
	return t.entry.provider.Interrupt(stop)
}

func (t StopTarget) InterruptChild(childKey string, stop StopContext) error {
	if t.entry == nil {
		return ErrAgentNotFound
	}
	interrupter, ok := t.entry.provider.(ChildInterrupter)
	if !ok {
		return ErrChildOperationUnsupported
	}
	return interrupter.InterruptChild(childKey, stop)
}

func (t StopTarget) SendRawInput(data []byte, stop StopContext) error {
	if t.entry == nil {
		return ErrAgentNotFound
	}
	return t.entry.provider.SendRawInput(data, stop)
}

func (t StopTarget) EscalationReady() bool {
	if t.entry == nil {
		return false
	}
	provider, ok := t.entry.provider.(interface{ InterruptEscalationReady() bool })
	return ok && provider.InterruptEscalationReady()
}

// StopContext identifies one stop attempt through its ignored-stop report.
// The zero value carries no user stop and reports nothing.
type StopContext struct {
	reportIgnored func()
}

func NewStopContext(reportIgnored func()) StopContext {
	return StopContext{reportIgnored: reportIgnored}
}

// ReportIgnored restores only the activity that this stop attempt suppressed.
// A provider calls it only after native output proves that this stop was ignored.
func (s StopContext) ReportIgnored() {
	if s.reportIgnored != nil {
		s.reportIgnored()
	}
}
