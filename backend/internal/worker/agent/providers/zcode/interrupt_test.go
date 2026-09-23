//go:build unix

package zcode

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInterrupt_ZCodeWireFormatMatchesProviderClassifier(t *testing.T) {
	t.Parallel()

	b, err := json.Marshal(map[string]any{
		"method": MethodSessionStop,
		"params": map[string]any{"sessionId": "sess-1"},
	})
	require.NoError(t, err)
	assert.True(t, zcodeProvider{}.IsInterrupt(string(b)),
		"zcodeProvider.IsInterrupt must recognise the frame zcodeAgent.Interrupt emits")
}
