package agent

import (
	"strings"
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

func TestMessageMetadataSkipsTheEnvelopeParseWithNoMetadata(t *testing.T) {
	// The stdout reader resolves EVERY persisted message, and almost none carries
	// worker metadata. A row with none must reach its answer without a parse of the
	// provider envelope, which is the large half. Allocation count is what proves
	// it: a parse into a map allocates, and returning the original slice does not.
	original := []byte(`{"type":"assistant","message":{"role":"assistant","content":[` +
		`{"type":"text","text":"` + strings.Repeat("filler ", 2000) + `"}]}}`)
	content := MessageContent{Original: original}
	var provider Provider = noopProvider{}
	allocs := testing.AllocsPerRun(50, func() {
		if got := ResolveMessageContent(provider, content); len(got) != len(original) {
			t.Fatalf("resolved length %d, want %d", len(got), len(original))
		}
	})
	assert.Zero(t, allocs, "resolving a message with no metadata must not parse the envelope")
}

func TestMessageMetadataMergesWhenMetadataIsPresent(t *testing.T) {
	t.Parallel()
	// The early return above must not swallow a real merge: a row that DOES carry
	// metadata still folds every validated field onto the provider envelope.
	content := MessageContent{
		Original: []byte(`{"type":"result","duration_ms":0}`),
		Metadata: []byte(`{"duration_ms":42,"num_tool_uses":3,"total_cost_usd":0.5,"context_usage":{"used":7}}`),
	}
	resolved := string(ResolveMessageContent(noopProvider{}, content))
	assert.Contains(t, resolved, `"duration_ms":42`)
	assert.Contains(t, resolved, `"num_tool_uses":3`)
	assert.Contains(t, resolved, `"total_cost_usd":0.5`)
	assert.Contains(t, resolved, `"context_usage":{"used":7}`)
}

func TestMessageMetadataKeepsAnUnparsableEnvelope(t *testing.T) {
	t.Parallel()
	// Metadata present, envelope not an object: the merge has nowhere to write, so
	// the original bytes travel on unchanged.
	for _, original := range []string{`not json`, `[1,2,3]`, `null`, ``} {
		content := MessageContent{Original: []byte(original), Metadata: []byte(`{"duration_ms":42}`)}
		assert.Equal(t, []byte(original), ResolveMessageContent(noopProvider{}, content))
	}
}
