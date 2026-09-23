package cursor

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildACPModels dedups by the final (post-normalize) id, so a normalizer that
// collapses two distinct wire ids to one does not surface a duplicate model.
func TestBuildACPModels_DedupsByNormalizedID(t *testing.T) {
	t.Parallel()

	models := acp.BuildModelsForTest([]acp.ModelInfo{
		{ModelID: cursorCLIModelAuto, Name: "Auto"},
		{ModelID: cursorCLIModelAutoWire, Name: "Auto (wire form)"}, // "default[]" -> "auto"
		{ModelID: "gpt-5", Name: "GPT-5"},
	}, cursorCLIModelAuto, normalizeCursorModelID)

	require.Len(t, models, 2)
	assert.Equal(t, cursorCLIModelAuto, models[0].GetId())
	assert.Equal(t, "Auto", models[0].DisplayName) // first occurrence wins
	assert.True(t, models[0].IsDefault)
	assert.Equal(t, "gpt-5", models[1].GetId())
}
