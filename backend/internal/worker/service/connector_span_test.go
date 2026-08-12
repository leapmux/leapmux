package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// resolveConnectorSpanID stays in this package: it decides which span a
// persisted row visually connects to, which is a persist-path concern rather
// than span-engine state. Its tests followed it here when the engine moved to
// the spantrack package.

func TestSpanTracker_ToolUseConnectorInSubagent(t *testing.T) {
	t.Parallel()

	// When a tool_use message is emitted inside a subagent, the tool_use's
	// own span hasn't been opened yet (it opens after persist). The parent
	// subagent span IS active. The span line for the subagent should render
	// as "connector" (├), not "active" (│).
	tracker := &SpanTracker{}
	tracker.OpenSpan("subagent", "")

	// Simulate persistAndBroadcast for a tool_use inside the subagent:
	//   span.SpanID       = "tool-1"  (not yet open)
	//   span.ParentSpanID = "subagent" (already open)
	//   span.Closing      = false
	connectorSpanID := resolveConnectorSpanID("tool-1", "", "subagent", false)
	_, lines, _ := tracker.Snapshot("subagent", connectorSpanID, false)

	var parsed []*SpanLine
	require.NoError(t, json.Unmarshal([]byte(lines), &parsed))
	require.Len(t, parsed, 1)
	assert.Equal(t, SpanLineConnector, parsed[0].Type,
		"tool_use inside subagent should show connector to parent span")
}

func TestSpanTracker_ToolResultConnectorInSubagent(t *testing.T) {
	t.Parallel()

	// A tool_result (closing) message should still connect to the tool's
	// own span, not the parent — the span is open at this point.
	tracker := &SpanTracker{}
	tracker.OpenSpan("subagent", "")
	tracker.OpenSpan("tool-1", "subagent")

	connectorSpanID := resolveConnectorSpanID("tool-1", "", "subagent", true)
	_, lines, _ := tracker.Snapshot("subagent", connectorSpanID, true)

	var parsed []*SpanLine
	require.NoError(t, json.Unmarshal([]byte(lines), &parsed))
	require.Len(t, parsed, 2)
	assert.Equal(t, SpanLineActive, parsed[0].Type)
	assert.Equal(t, SpanLineConnectorEnd, parsed[1].Type,
		"tool_result should show connector_end on its own span")
}

func TestSpanTracker_TopLevelToolUseNoConnector(t *testing.T) {
	t.Parallel()

	// A top-level tool_use (no parent span) should have no connector.
	tracker := &SpanTracker{}

	connectorSpanID := resolveConnectorSpanID("tool-1", "", "", false)
	_, lines, _ := tracker.Snapshot("", connectorSpanID, false)
	assert.Equal(t, "[]", lines)
}

func TestSpanTracker_ExplicitClosingConnectorUsesOverride(t *testing.T) {
	t.Parallel()

	tracker := &SpanTracker{}
	tracker.OpenSpan("subagent", "")

	connectorSpanID := resolveConnectorSpanID("wait-1", "subagent", "", true)
	_, lines, _ := tracker.Snapshot("", connectorSpanID, true)

	var parsed []*SpanLine
	require.NoError(t, json.Unmarshal([]byte(lines), &parsed))
	require.Len(t, parsed, 1)
	assert.Equal(t, SpanLineConnectorEnd, parsed[0].Type,
		"explicit connector override should let a spanless message close its parent span")
}
