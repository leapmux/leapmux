package cmd

import (
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/cli"
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

func TestGuideCanonicalAliasesResolveOnCommandTree(t *testing.T) {
	full := runControl(t, "guide").Data(t)
	guideText, ok := full["guide"].(string)
	require.True(t, ok)

	aliasSpanRe := regexp.MustCompile(`(?s)Stable\s+aliases\s+\(([^)]+)\)`)
	span := aliasSpanRe.FindStringSubmatch(guideText)
	require.Len(t, span, 2, "agent-guide.md must document Stable aliases (...)")

	tokenRe := regexp.MustCompile("`([^`]+)`")
	matches := tokenRe.FindAllStringSubmatch(span[1], -1)
	require.NotEmpty(t, matches)

	registered := map[string]bool{}
	var walk func(*cobra.Command)
	walk = func(c *cobra.Command) {
		for _, alias := range c.Aliases {
			registered[alias] = true
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(NewCommand())

	checked := 0
	for _, m := range matches {
		alias := strings.TrimSpace(m[1])
		if alias == "lmx" {
			continue
		}
		checked++
		assert.Truef(t, registered[alias], "canonical alias %q documented in agent-guide.md does not match any command or subcommand on NewCommand()", alias)
	}
	assert.GreaterOrEqual(t, checked, 20, "expected to verify every documented command alias")
}
