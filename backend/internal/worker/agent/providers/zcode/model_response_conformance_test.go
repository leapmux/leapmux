package zcode

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestZCodeModelResponseConformance replays the shared main-reply corpus.
//
// The browser replays the same file against `zcodeIsMainReply` in the provider
// plugin. A divergence between the two copies persists a side query as an
// assistant bubble on one side and hides it on the other.
func TestZCodeModelResponseConformance(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(testutil.RepoPath(t, "testdata", "zcode_model_response_conformance.json"))
	require.NoError(t, err)
	var fixture struct {
		Cases []struct {
			Name        string          `json:"name"`
			Payload     json.RawMessage `json:"payload"`
			IsMainReply bool            `json:"isMainReply"`
		} `json:"cases"`
	}
	require.NoError(t, json.Unmarshal(data, &fixture))
	require.NotEmpty(t, fixture.Cases)
	for _, tc := range fixture.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()
			var payload struct {
				QuerySource json.RawMessage `json:"querySource"`
				StopReason  string          `json:"stopReason"`
			}
			require.NoError(t, json.Unmarshal(tc.Payload, &payload))
			assert.Equal(t, tc.IsMainReply, zcodeIsMainReply(payload.QuerySource, payload.StopReason))
		})
	}
}
