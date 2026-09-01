package agent

import (
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"

	"github.com/stretchr/testify/assert"
)

// TestPiProtocolEventConstants pins each event-type constant to its
// wire-format literal. The literals are part of Pi's stdin/stdout
// contract and any divergence between this list and Pi's emitter would
// silently route events to the unknown-event default arm.
func TestPiProtocolEventConstants(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"agent_start":           contracts.PiEventAgentStart,
		"agent_end":             contracts.PiEventAgentEnd,
		"turn_start":            contracts.PiEventTurnStart,
		"turn_end":              contracts.PiEventTurnEnd,
		"message_start":         contracts.PiEventMessageStart,
		"message_end":           contracts.PiEventMessageEnd,
		"tool_execution_start":  contracts.PiEventToolExecutionStart,
		"tool_execution_end":    contracts.PiEventToolExecutionEnd,
		"tool_execution_update": contracts.PiEventToolExecutionUpdate,
		"extension_ui_request":  contracts.PiEventExtensionUIRequest,
		"extension_error":       contracts.PiEventExtensionError,
		"compaction_start":      contracts.PiEventCompactionStart,
		"compaction_end":        contracts.PiEventCompactionEnd,
		"auto_retry_start":      contracts.PiEventAutoRetryStart,
		"auto_retry_end":        contracts.PiEventAutoRetryEnd,
		"queue_update":          contracts.PiEventQueueUpdate,
		"response":              contracts.PiEventResponse,
	}
	for want, got := range cases {
		assert.Equal(t, want, got, "Pi event constant must match the wire literal")
	}
}

// TestPiProtocolDialogMethodConstants pins the dialog-method constants
// used on extension_ui_request envelopes.
func TestPiProtocolDialogMethodConstants(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "select", contracts.PiDialogMethodSelect)
	assert.Equal(t, "confirm", contracts.PiDialogMethodConfirm)
	assert.Equal(t, "input", contracts.PiDialogMethodInput)
	assert.Equal(t, "editor", contracts.PiDialogMethodEditor)
}

// TestPiProtocolToolNameConstants pins the tool identifiers Pi uses on
// tool_execution_* envelopes. Renderer dispatch keys off these.
func TestPiProtocolToolNameConstants(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "bash", contracts.PiToolBash)
	assert.Equal(t, "read", contracts.PiToolRead)
	assert.Equal(t, "edit", contracts.PiToolEdit)
	assert.Equal(t, "write", contracts.PiToolWrite)
}
