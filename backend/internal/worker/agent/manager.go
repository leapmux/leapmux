package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"sync"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// ErrAgentNotFound is returned when an agent process does not exist.
var ErrAgentNotFound = errors.New("agent not found")

// Manager tracks active agents and routes messages.
type Manager struct {
	mu     sync.RWMutex
	agents map[string]Provider // agentID -> Provider
	onExit ExitHandler
}

// NewManager creates a new agent Manager.
// The optional onExit handler is called when any agent process exits.
func NewManager(onExit ExitHandler) *Manager {
	return &Manager{
		agents: make(map[string]Provider),
		onExit: onExit,
	}
}

// startFunc is the function signature for starting an agent process.
type startFunc func(ctx context.Context, opts Options, sink OutputSink) (Provider, error)

// StartAgent spawns an agent for the given agent ID, dispatching based on
// opts.AgentProvider.
// The sink receives parsed output events.
// Returns the confirmed permission mode from the startup handshake.
func (m *Manager) StartAgent(ctx context.Context, opts Options, sink OutputSink) (string, error) {
	var start startFunc
	switch opts.AgentProvider {
	case leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX:
		start = startCodex
	case leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE:
		start = startOpenCode
	default:
		start = startClaudeCode
	}
	return m.startAgentWith(ctx, opts, sink, start)
}

// startClaudeCode wraps StartClaudeCode() to satisfy the startFunc signature.
func startClaudeCode(ctx context.Context, opts Options, sink OutputSink) (Provider, error) {
	return StartClaudeCode(ctx, opts, sink)
}

// startCodex wraps StartCodex() to satisfy the startFunc signature.
func startCodex(ctx context.Context, opts Options, sink OutputSink) (Provider, error) {
	return StartCodex(ctx, opts, sink)
}

// startOpenCode wraps StartOpenCode() to satisfy the startFunc signature.
func startOpenCode(ctx context.Context, opts Options, sink OutputSink) (Provider, error) {
	return StartOpenCode(ctx, opts, sink)
}

func (m *Manager) startAgentWith(ctx context.Context, opts Options, sink OutputSink, start startFunc) (string, error) {
	m.mu.Lock()
	if _, exists := m.agents[opts.AgentID]; exists {
		m.mu.Unlock()
		return "", fmt.Errorf("agent already running for agent %s", opts.AgentID)
	}
	m.mu.Unlock()

	provider, err := start(ctx, opts, sink)
	if err != nil {
		return "", err
	}

	confirmedMode := provider.ConfirmedPermissionMode()

	m.mu.Lock()
	m.agents[opts.AgentID] = provider
	m.mu.Unlock()

	// Wait for the agent to exit in the background, then clean up.
	go func() {
		err := provider.Wait()
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

		if provider.IsStopped() {
			slog.Info("agent stopped",
				"agent_id", opts.AgentID,
			)
		} else if err != nil {
			stderr := provider.Stderr()
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
	p, ok := m.agents[agentID]
	m.mu.RUnlock()

	if !ok {
		return fmt.Errorf("%w: %s", ErrAgentNotFound, agentID)
	}

	return p.SendInput(content)
}

// SendRawInput writes raw bytes directly to the specified agent's stdin
// without wrapping in a UserInputMessage.
func (m *Manager) SendRawInput(agentID string, data []byte) error {
	m.mu.RLock()
	p, ok := m.agents[agentID]
	m.mu.RUnlock()

	if !ok {
		return fmt.Errorf("%w: %s", ErrAgentNotFound, agentID)
	}

	return p.SendRawInput(data)
}

// StopAgent stops the agent with the given agent ID.
// Returns true if the agent was found (and will eventually trigger onExit),
// false if the agent had already exited.
func (m *Manager) StopAgent(agentID string) bool {
	m.mu.RLock()
	p, ok := m.agents[agentID]
	m.mu.RUnlock()

	if ok {
		p.Stop()
	}
	return ok
}

// StopAndWaitAgent stops the agent and waits for it to fully exit and be
// removed from the manager's map. This is necessary before restarting an
// agent to avoid the "agent already running" error from StartAgent.
// Returns true if the agent was found and stopped, false if it was not running.
func (m *Manager) StopAndWaitAgent(agentID string) bool {
	m.mu.RLock()
	p, ok := m.agents[agentID]
	m.mu.RUnlock()

	if !ok {
		return false
	}

	p.Stop()
	_ = p.Wait()

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
	p, ok := m.agents[agentID]
	m.mu.RUnlock()

	if !ok {
		return true
	}

	return p.SupportsModelEffort()
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
	providers := make([]Provider, 0, len(m.agents))
	for _, p := range m.agents {
		providers = append(providers, p)
	}
	m.mu.Unlock()

	for _, p := range providers {
		p.Stop()
	}
}
