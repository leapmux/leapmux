package zcode

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestZCodeToolSupplementConformance(t *testing.T) {
	t.Parallel()
	var fixture struct {
		Cases []struct {
			agenttest.StoredSupplementCase
			Record struct {
				Native    json.RawMessage   `json:"native"`
				Artifacts map[string]string `json:"artifacts"`
			} `json:"record"`
		} `json:"cases"`
	}
	data, err := os.ReadFile(testutil.RepoPath(t, "testdata", "zcode_tool_supplement_conformance.json"))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &fixture))
	require.NotEmpty(t, fixture.Cases)
	for _, tc := range fixture.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()
			var native contracts.ZCodeStoredTool
			require.NoError(t, json.Unmarshal(tc.Record.Native, &native))
			encoded, err := zcodeToolResultSupplement(tc.Original, zcodeToolRecord{native: native, artifacts: tc.Record.Artifacts})
			require.NoError(t, err)
			assert.JSONEq(t, string(tc.Supplement), string(encoded))
		})
	}
}
