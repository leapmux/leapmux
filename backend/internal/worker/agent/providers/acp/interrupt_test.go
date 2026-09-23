//go:build unix

package acp

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInterrupt_ACPWireFormatMatchesProviderClassifier(t *testing.T) {
	t.Parallel()

	b, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  "session/cancel",
		"params":  map[string]any{"sessionId": "session-1"},
	})
	require.NoError(t, err)
	assert.True(t, Provider{}.IsInterrupt(string(b)),
		"Provider.IsInterrupt must recognise the frame Base.Interrupt emits")
}
