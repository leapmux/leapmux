package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMessageSupplementSeparatesProviderDataAndMetadata(t *testing.T) {
	t.Parallel()
	original := []byte(` {"duration_ms":"provider value","wide":9007199254740993} `)
	providerData := []byte(`{"metadata":{"duration_ms":"native"},"wide":9007199254740993}`)
	metadata := []byte(`{"duration_ms":0,"total_cost_usd":0,"num_tool_uses":2}`)
	encoded, err := EncodeMessageSupplement(MessageContent{Supplemental: providerData, Metadata: metadata})
	require.NoError(t, err)
	decoded, err := DecodeMessageSupplement(original, encoded)
	require.NoError(t, err)
	assert.Equal(t, original, decoded.Original)
	assert.Equal(t, providerData, decoded.Supplemental)
	assert.Equal(t, metadata, decoded.Metadata)
	resolved := ResolveMessageContent(noopProvider{}, decoded)
	assert.Contains(t, string(resolved), `"wide":9007199254740993`)
	assert.Contains(t, string(resolved), `"duration_ms":0`)
	assert.NotContains(t, string(resolved), `"metadata"`)
	assert.Equal(t, original, decoded.Original)
}

func TestMessageMetadataDoesNotReadProviderSupplementFields(t *testing.T) {
	t.Parallel()
	original := []byte(` {"duration_ms":"original"} `)
	content := MessageContent{Original: original, Supplemental: []byte(`{"duration_ms":999,"total_cost_usd":999}`)}
	assert.Equal(t, original, ResolveMessageContent(noopProvider{}, content))
}

func TestMessageMetadataRejectsInvalidFields(t *testing.T) {
	t.Parallel()
	original := []byte(` {"duration_ms":"original"} `)
	for _, metadata := range []string{`null`, `[]`, `invalid`, `{"duration_ms":-1,"num_tool_uses":false,"total_cost_usd":"wrong","context_usage":[]}`, `{"unrelated":true}`} {
		assert.Equal(t, original, ResolveMessageContent(noopProvider{}, MessageContent{Original: original, Metadata: []byte(metadata)}))
	}
}

func TestMessageSupplementCodecErrorsKeepTheOriginal(t *testing.T) {
	t.Parallel()
	original := []byte(`original`)
	decoded, err := DecodeMessageSupplement(original, []byte(`{invalid`))
	require.Error(t, err)
	assert.Equal(t, original, decoded.Original)
	_, err = EncodeMessageSupplement(MessageContent{Supplemental: []byte(`{invalid`)})
	require.Error(t, err)
	encoded, err := EncodeMessageSupplement(MessageContent{})
	require.NoError(t, err)
	assert.Empty(t, encoded)
}
