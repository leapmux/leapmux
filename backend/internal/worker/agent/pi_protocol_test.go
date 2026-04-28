package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestPiProtocolEventConstants pins each event-type constant to its
// wire-format literal. The literals are part of Pi's stdin/stdout
// contract and any divergence between this list and Pi's emitter would
// silently route events to the unknown-event default arm.
func TestPiProtocolEventConstants(t *testing.T) {
	cases := map[string]string{
		"agent_start":           PiEventAgentStart,
		"agent_end":             PiEventAgentEnd,
		"turn_start":            PiEventTurnStart,
		"turn_end":              PiEventTurnEnd,
		"message_start":         PiEventMessageStart,
		"message_end":           PiEventMessageEnd,
		"tool_execution_start":  PiEventToolExecutionStart,
		"tool_execution_end":    PiEventToolExecutionEnd,
		"tool_execution_update": PiEventToolExecutionUpdate,
		"extension_ui_request":  PiEventExtensionUIRequest,
		"extension_error":       PiEventExtensionError,
		"compaction_start":      PiEventCompactionStart,
		"compaction_end":        PiEventCompactionEnd,
		"auto_retry_start":      PiEventAutoRetryStart,
		"auto_retry_end":        PiEventAutoRetryEnd,
		"queue_update":          PiEventQueueUpdate,
		"response":              PiEventResponse,
	}
	for want, got := range cases {
		assert.Equal(t, want, got, "Pi event constant must match the wire literal")
	}
}

// TestPiProtocolDialogMethodConstants pins the dialog-method constants
// used on extension_ui_request envelopes.
func TestPiProtocolDialogMethodConstants(t *testing.T) {
	assert.Equal(t, "select", PiDialogMethodSelect)
	assert.Equal(t, "confirm", PiDialogMethodConfirm)
	assert.Equal(t, "input", PiDialogMethodInput)
	assert.Equal(t, "editor", PiDialogMethodEditor)
}

// TestPiProtocolToolNameConstants pins the tool identifiers Pi uses on
// tool_execution_* envelopes. Renderer dispatch keys off these.
func TestPiProtocolToolNameConstants(t *testing.T) {
	assert.Equal(t, "bash", PiToolBash)
	assert.Equal(t, "read", PiToolRead)
	assert.Equal(t, "edit", PiToolEdit)
	assert.Equal(t, "write", PiToolWrite)
}
