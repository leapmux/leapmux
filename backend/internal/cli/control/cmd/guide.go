package cmd

import (
	_ "embed"
	"io"

	"github.com/spf13/cobra"

	"github.com/leapmux/leapmux/internal/cli"
)

// agentGuide is the guide that `leapmux control guide` prints. It ships in
// the binary, so it always describes the commands of this version.
//
//go:embed guide/agent-guide.md
var agentGuide string

func newGuideCommand() *cobra.Command {
	return cli.NewLeaf(cli.Leaf{
		Use:   "guide",
		Short: "Print the guide for an agent that drives LeapMux",
		Examples: []cli.Example{
			{Command: "leapmux control guide"},
		},
		Run: func(*cli.Invocation) (any, error) {
			return guideText(agentGuide), nil
		},
	})
}

// guideText is the guide in a result. Its text form is the Markdown as it
// is; JSON carries it as one string.
type guideText string

func (g guideText) RenderText(w io.Writer) error {
	_, err := io.WriteString(w, string(g))
	return err
}

// MarshalJSON encodes the guide as {"guide": "<markdown>"}, so the result is
// an object like every other result.
func (g guideText) MarshalJSON() ([]byte, error) {
	return jsonObject("guide", string(g))
}
