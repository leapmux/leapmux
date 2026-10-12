package muse

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCatalogRetainsNativeEffortsAndZeroContext(t *testing.T) {
	t.Parallel()
	models, err := decodeCatalog([]byte(`{"models":[{"modelId":"native","displayLabel":"Native","isDefault":true,"contextLimit":0,"variants":["low","high"],"reasoningEffortVariants":[{"tier":"low"},{"tier":"high"}],"defaultReasoningEffort":"high"}]}`))
	require.NoError(t, err)
	require.Len(t, models, 1)
	assert.Equal(t, "native", models[0].Id)
	assert.Equal(t, int64(0), models[0].ContextWindow)
	assert.Equal(t, "low", models[0].SupportedEfforts[0].Id)
	assert.Equal(t, "high", models[0].DefaultEffort)
}

func TestCatalogRejectsMalformedAndDuplicateModels(t *testing.T) {
	t.Parallel()
	for _, source := range []string{`null`, `{}`, `{"models":[{"modelId":""}]}`, `{"models":[{"modelId":"a"},{"modelId":"a"}]}`, `{"models":[{"modelId":"a","contextLimit":-1}]}`} {
		_, err := decodeCatalog([]byte(source))
		require.Error(t, err, source)
	}
}
