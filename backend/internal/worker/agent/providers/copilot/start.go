package copilot

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/leapmux/leapmux/internal/util/optionmap"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

var _ agent.StartFunc = Start

// Start starts the Copilot CLI on its NATIVE protocol and opens a session.
//
// The CLI also speaks the Agent Client Protocol, and LeapMux used that adapter
// before. The native protocol carries what the adapter drops, and each of those is a
// feature the user reaches:
//
//   - A question and a plan decision keep their native request ID and tool-call ID,
//     so an answer reaches the exact request (CP-004).
//   - A permission decision carries its scope, so "approve for this project" means
//     what the runtime means by it (CP-007).
//   - A Model Context Protocol elicitation reaches the browser at all (CP-001).
//   - The autopilot objective supports Set, Pause, Resume and Clear (CP-002, CP-003,
//     CP-008).
//   - Every subagent states its own identity and its parent in the event stream, so
//     a child transcript needs no read of the CLI's session-store files.
func Start(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (agent.Agent, error) {
	return startNativeCopilot(ctx, opts, sink)
}

func startNativeCopilot(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (agent.Agent, error) {
	a := &Agent{
		sink: agent.NewModelProgressResetSink(sink), opts: opts,
		options: make(optionmap.Map),
	}
	connection, err := startCopilotConnection(ctx, opts)
	if err != nil {
		return nil, err
	}
	// Adopt the connection BEFORE the reader starts. The reader reaches this agent
	// through the embedded pointer, so a frame that arrived first would read a nil one.
	a.copilotConnection = connection
	connection.startReading(a.handleNativeOutput)
	if err := connection.verifyNativeProtocol(opts); err != nil {
		return nil, err
	}
	cleanup := func(err error) (agent.Agent, error) {
		a.outputMu.Lock()
		a.closing = true
		a.outputMu.Unlock()
		a.releaseNativeControlEvents()
		connection.Stop()
		_ = connection.Wait()
		a.outputMu.Lock()
		a.clearNativeChildren()
		a.clearNativeControls()
		a.outputMu.Unlock()
		return nil, err
	}
	id := opts.ResumeSessionID
	if id == "" {
		generated, err := uuid.NewRandom()
		if err != nil {
			return cleanup(fmt.Errorf("create Copilot session ID: %w", err))
		}
		id = generated.String()
	}
	a.stateMu.Lock()
	a.sessionID = id
	a.stateMu.Unlock()
	if _, err := connection.openSession(opts, id, opts.ResumeSessionID != "", opts.EffectiveStartupTimeout()); err != nil {
		return cleanup(connection.FormatStartupError("native session initialization", err))
	}
	a.sink.UpdateSessionID(id)
	// Startup subscribes and then READS the runtime's own settings, because a new
	// process states what it starts with. A session that opens AGAIN takes the same
	// two steps through prepareNativeSession, which RESTORES the stored values
	// instead: the session it replaces already settled them.
	if err := a.registerNativeControlEvents(); err != nil {
		return cleanup(err)
	}
	modelData, err := connection.requestSession(id, "model.list", nil, opts.EffectiveStartupTimeout())
	if err != nil {
		return cleanup(fmt.Errorf("read Copilot models: %w", err))
	}
	models, err := parseCopilotModels(modelData)
	if err != nil {
		return cleanup(err)
	}
	a.stateMu.Lock()
	a.models = append([]*agent.ModelInfo{agent.AccountDefaultModelEntry("Use the model that Copilot selects for this account.")}, models...)
	a.stateMu.Unlock()
	if err := a.refreshNativeSettings(); err != nil {
		return cleanup(err)
	}
	startupSettings := optionmap.Map{}
	for _, key := range []string{copilotOptionSessionMode, agent.OptionIDPermissionMode} {
		if value := opts.Get(key); value != "" {
			startupSettings[key] = value
		}
	}
	if len(startupSettings) > 0 {
		applied := a.UpdateSettings(startupSettings)
		for key := range startupSettings {
			if applied.Settlements[key].State != agent.OptionSettlementConfirmed {
				return cleanup(fmt.Errorf("the Copilot runtime did not confirm the requested %s setting", key))
			}
		}
	}
	// A resumed session can already hold an objective. Read it before the first
	// broadcast so the goal card opens on the stored objective rather than empty.
	a.refreshNativeGoal(opts.ResumeSessionID != "")
	a.sink.BroadcastStatusActive(id)
	return a, nil
}
