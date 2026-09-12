package agent

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPiMessageContentConformance(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("../../../../testdata/pi_message_content_conformance.json")
	require.NoError(t, err)
	var fixture struct {
		Cases []struct {
			Name         string          `json:"name"`
			Original     json.RawMessage `json:"original"`
			Supplemental json.RawMessage `json:"supplemental"`
			Expected     json.RawMessage `json:"expected"`
		} `json:"cases"`
	}
	require.NoError(t, json.Unmarshal(data, &fixture))
	require.NotEmpty(t, fixture.Cases)
	for _, tc := range fixture.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()
			original := append([]byte(nil), tc.Original...)
			supplemental := append([]byte(nil), tc.Supplemental...)
			resolved := piProvider{}.ResolveProviderData(MessageContent{Original: tc.Original, Supplemental: tc.Supplemental})
			assert.JSONEq(t, string(tc.Expected), string(resolved))
			again := piProvider{}.ResolveProviderData(MessageContent{Original: resolved, Supplemental: tc.Supplemental})
			require.NotEmpty(t, resolved)
			require.NotEmpty(t, again)
			assert.Same(t, &resolved[0], &again[0], "resolved content must not be allocated again")
			assert.Equal(t, original, []byte(tc.Original))
			assert.Equal(t, supplemental, []byte(tc.Supplemental))
		})
	}
}
