package cmd

import (
	"testing"

	"github.com/leapmux/leapmux/internal/cli"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGuideTopicsMatchTheEmbeddedSections(t *testing.T) {
	full := runControl(t, "guide").Data(t)
	whole, ok := full["guide"].(string)
	require.True(t, ok)

	index := runControl(t, "guide", "--topics").Data(t)
	topics, ok := index["topics"].([]any)
	require.True(t, ok)
	require.Len(t, topics, 5)
	seen := make(map[string]bool)
	for _, value := range topics {
		metadata, ok := value.(map[string]any)
		require.True(t, ok)
		id, ok := metadata["id"].(string)
		require.True(t, ok)
		require.NotEmpty(t, id)
		assert.False(t, seen[id], "guide topic %q appears twice", id)
		seen[id] = true
		title, ok := metadata["title"].(string)
		require.True(t, ok)
		selection := runControl(t, "guide", id).Data(t)
		markdown, ok := selection["guide"].(string)
		require.True(t, ok)
		assert.Contains(t, markdown, "## "+title+"\n")
		assert.Contains(t, whole, markdown)
		assert.NotEqual(t, whole, markdown)
	}
	assert.True(t, seen["targets"])
	assert.True(t, seen["tasks"])
	assert.NotContains(t, whole, "<!-- topic:")
}

func TestGuideTopicIndexPrintsTitles(t *testing.T) {
	index := runControlText(t, "guide", "--topics")
	require.Equal(t, cli.ExitOK, index.Code, index.Stderr)
	assert.Contains(t, index.Stdout, "TOPIC")
	assert.Contains(t, index.Stdout, "How a command finds its object")
	assert.NotContains(t, index.Stdout, "ambiguous_target")
}

func TestGuideRejectsUnknownOrContradictoryTopicSelection(t *testing.T) {
	notFound := runControl(t, "guide", "unknown-topic").requireFailure(t, cli.CodeNotFound)
	assert.Contains(t, notFound.Message, "unknown-topic")
	assert.Contains(t, notFound.Hints, "run leapmux guide --topics to list topics")
	runControl(t, "guide", "targets", "--topics").requireFailure(t, cli.CodeUsage)
	runControl(t, "guide", "").requireFailure(t, cli.CodeUsage)
}
