//go:build unix

package agent

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCopilot_NoHooksNoRegistryWrites is the regression guard that Copilot is
// inert with respect to the subagent/background-task registry: it registers
// via registerACPProvider/acpStart WITHOUT setting the subagentFromToolCall /
// subagentFromToolCallUpdate hooks (verified by the live probe), so feeding it
// tool_call / tool_call_update payloads that WOULD trigger spawn detection for
// other providers (OpenCode's {description,prompt,subagent_type}, Goose's
// _meta.goose.toolCall {delegate,summon}, Reasonix's {description,prompt}) must
// not produce any registry write on the sink.
//
// This is asserted two complementary ways:
//
//  1. Structurally: after constructing a CopilotCLIAgent exactly the way the
//     production helper (newCopilotAgentForRPC) does, both subagent hooks are
//     nil. Copilot.go's StartCopilotCLI configure callback is the ONLY place
//     that could set them and it does not.
//  2. Behaviorally: feeding spawn-shaped tool_call / tool_call_update wire
//     payloads through the shared ACP dispatcher leaves the testSink's
//     background-task registry empty.
func TestCopilot_NoHooksNoRegistryWrites(t *testing.T) {
	t.Parallel()

	// --- Structural assertion: hooks stay nil on a CopilotCLIAgent. ---
	// Constructed the same way newCopilotAgentForRPC (the harness used by every
	// other Copilot test) builds one: a fresh CopilotCLIAgent with the
	// modeChannel set, mirroring what StartCopilotCLI's configure callback does.
	// StartCopilotCLI's configure only sets modeChannel + effortConfigID -- it
	// never assigns the subagent hooks (unlike goose/opencode/kilo/cursor/
	// reasonix, which assign at least one). Re-checking here means a future
	// edit that wires Copilot into the registry is caught at unit level.
	agent, _ := newCopilotAgentForRPC(t)
	base := &agent.acpBase
	assert.Nil(t, base.subagentFromToolCall,
		"Copilot must not register a subagentFromToolCall hook")
	assert.Nil(t, base.subagentFromToolCallUpdate,
		"Copilot must not register a subagentFromToolCallUpdate hook")

	// --- Behavioral assertion: spawn-shaped payloads write nothing to the
	// registry when driven through the shared ACP session-update dispatcher. ---
	// Wire a fresh testSink so a registry write (UpsertBackgroundTask /
	// EnsureChildAgent) is observable via sink.BackgroundTasks().
	sink := &testSink{}
	base.sink = sink

	// A payload that WOULD be detected as a spawn by the OpenCode detector
	// ({description,prompt,subagent_type}) and Reasonix detector
	// ({description,prompt}) if Copilot had wired those hooks.
	opencodeSpawnShape := json.RawMessage(`{"sessionUpdate":"tool_call","toolCallId":"tc-opencode","title":"build feature","kind":"other","status":"in_progress","rawInput":{"description":"build feature","prompt":"do the thing","subagent_type":"build"}}`)

	// A payload that WOULD be detected as a spawn by the Goose detector
	// (_meta.goose.toolCall {delegate,summon}).
	gooseSpawnShape := json.RawMessage(`{"sessionUpdate":"tool_call","toolCallId":"tc-goose","title":"delegate","status":"in_progress","_meta":{"goose":{"toolCall":{"toolName":"delegate","extensionName":"summon"}}}}`)

	// Drive each through the shared dispatcher (the entry point the reader
	// goroutine uses). Because Copilot's hooks are nil, the subagent branch is
	// never entered regardless of payload shape.
	require.NotPanics(t, func() {
		base.handleACPSessionUpdate(
			json.RawMessage(`{"update":`+string(opencodeSpawnShape)+`}`),
			nil, /* extra: no provider-specific session-update handler */
		)
	})
	require.NotPanics(t, func() {
		base.handleACPSessionUpdate(
			json.RawMessage(`{"update":`+string(gooseSpawnShape)+`}`),
			nil,
		)
	})

	// A terminal tool_call_update that WOULD re-key + close a registry row for
	// the OpenCode/Cursor detectors. Copilot has no tool_call_update hook, so it
	// must not create or close any row.
	terminalUpdate := json.RawMessage(`{"sessionUpdate":"tool_call_update","toolCallId":"tc-opencode","status":"completed","rawOutput":{"metadata":{"sessionId":"child-sess-1"}}}`)
	require.NotPanics(t, func() {
		base.handleACPSessionUpdate(
			json.RawMessage(`{"update":`+string(terminalUpdate)+`}`),
			nil,
		)
	})

	// The registry must be empty: no UpsertBackgroundTask, no EnsureChildAgent.
	assert.Empty(t, sink.BackgroundTasks(),
		"Copilot must not write background-task registry rows for spawn-shaped payloads")
	// No child transcript sink was ever created either.
	sink.childSinkMu.Lock()
	assert.Empty(t, sink.children,
		"Copilot must not spawn child transcripts")
	sink.childSinkMu.Unlock()
}
