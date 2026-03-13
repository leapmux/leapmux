package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"sync"
)

// ErrAgentNotFound is returned when an agent process does not exist.
var ErrAgentNotFound = errors.New("agent not found")

// Manager tracks active agents and routes messages.
type Manager struct {
	mu     sync.RWMutex
	agents map[string]*Agent // agentID -> Agent
	onExit ExitHandler
}

// NewManager creates a new agent Manager.
// The optional onExit handler is called when any agent process exits.
func NewManager(onExit ExitHandler) *Manager {
	return &Manager{
		agents: make(map[string]*Agent),
		onExit: onExit,
	}
}

// startFunc is the function signature for starting an agent process.
type startFunc func(ctx context.Context, opts Options, outputFn OutputHandler) (*Agent, error)

// StartAgent spawns a new Claude Code agent for the given agent ID.
// The outputFn callback receives each NDJSON line of output.
// Returns the confirmed permission mode from the startup handshake.
func (m *Manager) StartAgent(ctx context.Context, opts Options, outputFn OutputHandler) (string, error) {
	return m.startAgentWith(ctx, opts, outputFn, Start)
}

func (m *Manager) startAgentWith(ctx context.Context, opts Options, outputFn OutputHandler, start startFunc) (string, error) {
	m.mu.Lock()
	if _, exists := m.agents[opts.AgentID]; exists {
		m.mu.Unlock()
		return "", fmt.Errorf("agent already running for agent %s", opts.AgentID)
	}
	m.mu.Unlock()

	agent, err := start(ctx, opts, outputFn)
	if err != nil {
		return "", err
	}

	confirmedMode := agent.ConfirmedPermissionMode()

	m.mu.Lock()
	m.agents[opts.AgentID] = agent
	m.mu.Unlock()

	// Wait for the agent to exit in the background, then clean up.
	go func() {
		err := agent.Wait()
		m.mu.Lock()
		delete(m.agents, opts.AgentID)
		m.mu.Unlock()

		exitCode := 0
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				exitCode = exitErr.ExitCode()
			} else {
				exitCode = -1
			}
		}

		if agent.IsStopped() {
			slog.Info("agent stopped",
				"agent_id", opts.AgentID,
			)
		} else if err != nil {
			stderr := agent.Stderr()
			slog.Warn("agent exited with error",
				"agent_id", opts.AgentID,
				"error", err,
				"stderr", stderr,
			)
		} else {
			slog.Info("agent exited",
				"agent_id", opts.AgentID,
			)
		}

		if m.onExit != nil {
			m.onExit(opts.AgentID, exitCode, err)
		}
	}()

	return confirmedMode, nil
}

// SendInput routes a user message to the specified agent.
func (m *Manager) SendInput(agentID, content string) error {
	m.mu.RLock()
	agent, ok := m.agents[agentID]
	m.mu.RUnlock()

	if !ok {
		return fmt.Errorf("%w: %s", ErrAgentNotFound, agentID)
	}

	return agent.SendInput(content)
}

// SendRawInput writes raw bytes directly to the specified agent's stdin
// without wrapping in a UserInputMessage.
func (m *Manager) SendRawInput(agentID string, data []byte) error {
	m.mu.RLock()
	agent, ok := m.agents[agentID]
	m.mu.RUnlock()

	if !ok {
		return fmt.Errorf("%w: %s", ErrAgentNotFound, agentID)
	}

	return agent.SendRawInput(data)
}

// StopAgent stops the agent with the given agent ID.
// Returns true if the agent was found (and will eventually trigger onExit),
// false if the agent had already exited.
func (m *Manager) StopAgent(agentID string) bool {
	m.mu.RLock()
	agent, ok := m.agents[agentID]
	m.mu.RUnlock()

	if ok {
		agent.Stop()
	}
	return ok
}

// StopAndWaitAgent stops the agent and waits for it to fully exit and be
// removed from the manager's map. This is necessary before restarting an
// agent to avoid the "agent already running" error from StartAgent.
// Returns true if the agent was found and stopped, false if it was not running.
func (m *Manager) StopAndWaitAgent(agentID string) bool {
	m.mu.RLock()
	agent, ok := m.agents[agentID]
	m.mu.RUnlock()

	if !ok {
		return false
	}

	agent.Stop()
	_ = agent.Wait()

	// Remove the map entry eagerly so that StartAgent can proceed
	// immediately. The background goroutine's delete will be a no-op.
	m.mu.Lock()
	delete(m.agents, agentID)
	m.mu.Unlock()

	return true
}

// SupportsModelEffort returns whether the agent supports --model/--effort CLI args.
// Returns true as default (agent not found = safe fallback for Anthropic API).
func (m *Manager) SupportsModelEffort(agentID string) bool {
	m.mu.RLock()
	a, ok := m.agents[agentID]
	m.mu.RUnlock()

	if !ok {
		return true
	}

	return a.SupportsModelEffort()
}

// HasAgent returns true if an agent is running with the given agent ID.
func (m *Manager) HasAgent(agentID string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.agents[agentID]
	return ok
}

// ListAgentIDs returns the IDs of all currently tracked agents.
func (m *Manager) ListAgentIDs() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ids := make([]string, 0, len(m.agents))
	for id := range m.agents {
		ids = append(ids, id)
	}
	return ids
}

// StopAll stops all running agents.
func (m *Manager) StopAll() {
	m.mu.Lock()
	agents := make([]*Agent, 0, len(m.agents))
	for _, a := range m.agents {
		agents = append(agents, a)
	}
	m.mu.Unlock()

	for _, a := range agents {
		a.Stop()
	}
}
