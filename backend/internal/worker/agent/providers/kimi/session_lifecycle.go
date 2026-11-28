package kimi

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

// Kimi Code's session lifecycle.
//
// The server holds any number of sessions; the agent drives one. A new session
// is POST /sessions, and it has NO model until the profile binds one: the server
// never applies the configured default to a session it creates, and the first
// prompt of an unbound session fails with `model.not_configured`. So every new
// session gets its whole profile before it is used.
//
// A native WebSocket subscription resumes a stored session and restores its state.

// kimiSessionReply is the part of a session object the worker reads.
type kimiSessionReply struct {
	ID string `json:"id"`
}

// kimiSessionAdmission retains state received before a new session becomes current.
// dispatchMu protects the admission and its frames.
type kimiSessionAdmission struct {
	sessionID string
	frames    []kimiFrame
	bytes     int
	err       error
}

func (p *kimiSessionAdmission) append(frame kimiFrame) {
	if p.err != nil {
		return
	}
	// Include per-frame bookkeeping so empty frames cannot grow the slice without a limit.
	size := frame.wireSize + 256
	if size > kimiStreamReadLimit-p.bytes {
		p.err = fmt.Errorf("the Kimi session's initial state exceeds the memory limit")
		p.frames = nil
		return
	}
	p.bytes += size
	p.frames = append(p.frames, frame)
}

func (a *Agent) takeSessionAdmissionLocked(sessionID string) ([]kimiFrame, error) {
	pending := a.pendingSession
	if pending == nil || pending.sessionID != sessionID {
		return nil, fmt.Errorf("the Kimi session has no matching subscription admission")
	}
	a.pendingSession = nil
	return pending.frames, pending.err
}

func (a *Agent) adoptSessionLocked(sessionID string, frames []kimiFrame) {
	a.Mu.Lock()
	previousSessionID := a.sessionID
	a.sessionID = sessionID
	a.Mu.Unlock()
	if previousSessionID != sessionID {
		a.retireKimiSessionReplies(previousSessionID)
		a.cancelMediaSession(previousSessionID)
	}
	a.sink.UpdateSessionID(sessionID)
	for _, frame := range frames {
		a.dispatchFrame(frame)
	}
}

// ownsNativeSession reports whether the event belongs to the current session.
func (a *Agent) ownsNativeSession(sessionID string) bool {
	a.Mu.Lock()
	defer a.Mu.Unlock()
	return sessionID != "" && sessionID == a.sessionID
}

// createSession creates a session in the working directory, binds its profile
// and subscribes to it. It returns the session id.
func (a *Agent) createSession(ctx context.Context, settings kimiSettings) (string, error) {
	var created kimiSessionReply
	body := map[string]any{"metadata": map[string]any{"cwd": a.workingDir}}
	if err := a.api.post(ctx, kimiRouteSessions, body, &created); err != nil {
		return "", fmt.Errorf("create a Kimi Code session: %w", err)
	}
	if err := kimiCheckID("session", created.ID); err != nil {
		return "", err
	}
	if err := a.postProfile(ctx, created.ID, a.profileConfig(settings, nil)); err != nil {
		return "", fmt.Errorf("configure the Kimi Code session: %w", err)
	}
	a.dispatchMu.Lock()
	if a.pendingSession != nil {
		a.finishDispatch()
		return "", fmt.Errorf("another Kimi session subscription still waits for adoption")
	}
	a.pendingSession = &kimiSessionAdmission{sessionID: created.ID}
	a.finishDispatch()
	if err := a.stream.subscribe(ctx, created.ID); err != nil {
		a.dispatchMu.Lock()
		if a.pendingSession != nil && a.pendingSession.sessionID == created.ID {
			a.pendingSession = nil
		}
		a.finishDispatch()
		return "", fmt.Errorf("subscribe to the Kimi Code session: %w", err)
	}
	return created.ID, nil
}

// resumeSession loads a stored session and subscribes to it. The session keeps
// its own model and history; only the axes the launch states differently are
// written to its profile.
func (a *Agent) resumeSession(ctx context.Context, sessionID string, wanted kimiSettings) (kimiStatus, error) {
	if err := kimiCheckID("session", sessionID); err != nil {
		return kimiStatus{}, err
	}
	status, err := a.readStatus(ctx, sessionID)
	if err != nil {
		return kimiStatus{}, err
	}
	stored := kimiSettings{
		model: status.Model, effort: status.ThinkingLevel, permission: status.Permission,
		planMode: status.PlanMode, swarmMode: status.SwarmMode,
	}
	config := a.profileConfig(wanted, &stored)
	// A launch that states no model keeps the model of the stored session, so only a stated
	// model that differs moves it. The stored level then belongs to the old model.
	a.restateAutoThinking(config, wanted, false, wanted.model != "" && wanted.model != stored.model)
	if len(config) > 0 {
		if err := a.postProfile(ctx, sessionID, config); err != nil {
			return kimiStatus{}, fmt.Errorf("configure the resumed Kimi Code session: %w", err)
		}
		if status, err = a.confirmProfileStatus(ctx, sessionID, config); err != nil {
			return kimiStatus{}, err
		}
	}
	return status, nil
}

// ClearContext requires a private runtime replacement. Native session creation
// leaves detached work running after its subscription and routes disappear.
// The Worker stops this runtime and starts fresh while preserving saved history.
func (a *Agent) ClearContext() (string, error) {
	return "", agent.ErrContextClearUnsupported
}

// CompactContext asks the server to compact the session's context. Progress
// arrives through native agent state and conversation turns.
func (a *Agent) CompactContext() error {
	a.Mu.Lock()
	sessionID := a.sessionID
	a.Mu.Unlock()
	if err := kimiCheckID("session", sessionID); err != nil {
		return err
	}
	ctx, cancel := a.requestContext()
	defer cancel()
	return a.api.post(ctx, kimiSessionPath(sessionID, kimiActionCompact), nil, nil)
}

// resyncSession retries a subscription that supplied no native agent state.
// Accepted subscriptions already supply flat state, transcript restoration and
// pending controls. Their queued frames own reconciliation after the acknowledgement.
func (a *Agent) resyncSession(ctx context.Context, sessionID string) {
	a.Mu.Lock()
	current := a.sessionID
	a.Mu.Unlock()
	if sessionID != current || kimiCheckID("session", sessionID) != nil {
		return
	}
	if _, exists := a.stream.sessionStatus(sessionID); exists {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, a.APITimeout())
	defer cancel()
	if err := a.stream.subscribe(ctx, sessionID); err != nil {
		slog.Warn("Kimi session resubscription failed", "agent_id", a.AgentID(), "session_id", sessionID, "error", err)
	}
}
