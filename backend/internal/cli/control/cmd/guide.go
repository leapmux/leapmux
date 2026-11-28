package cmd

import (
	_ "embed"
	"encoding/json"
	"io"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/leapmux/leapmux/internal/cli"
)

// agentGuideSource holds the full guide and its topic IDs. It ships in the
// binary with the commands it describes.
//
//go:embed guide/agent-guide.md
var agentGuideSource string

var guideTopicHeading = regexp.MustCompile(`(?m)^## (.+) <!-- topic: ([a-z][a-z0-9-]*) -->$`)
var agentGuide, agentGuideTopics = indexAgentGuide(agentGuideSource)

type guideTopic struct {
	ID       string `json:"id" table:"TOPIC"`
	Title    string `json:"title" table:"TITLE"`
	markdown string
}

type guideIndex struct {
	Topics []guideTopic `json:"topics"`
}

func (g guideIndex) QuietLines() []string {
	out := make([]string, 0, len(g.Topics))
	for _, t := range g.Topics {
		out = append(out, t.ID)
	}
	return out
}

// indexAgentGuide takes topic IDs from heading annotations. Both the whole
// guide and each selected section omit the annotations.
func indexAgentGuide(source string) (string, []guideTopic) {
	headings := guideTopicHeading.FindAllStringSubmatchIndex(source, -1)
	if len(headings) == 0 {
		return source, nil
	}
	var whole strings.Builder
	whole.Grow(len(source))
	whole.WriteString(source[:headings[0][0]])
	topics := make([]guideTopic, 0, len(headings))
	for i, heading := range headings {
		end := len(source)
		if i+1 < len(headings) {
			end = headings[i+1][0]
		}
		title := source[heading[2]:heading[3]]
		section := "## " + title + source[heading[1]:end]
		topics = append(topics, guideTopic{ID: source[heading[4]:heading[5]], Title: title, markdown: section})
		whole.WriteString(section)
	}
	return whole.String(), topics
}

func newGuideCommand() *cobra.Command {
	cmd := cli.NewLeaf(cli.Leaf{
		Use:   "guide [topic]",
		Short: "Print the guide for an agent that drives LeapMux",
		Args:  map[string]string{"topic": "a topic ID from --topics"},
		Flags: func(fs *pflag.FlagSet) {
			fs.Bool("topics", false, "list the available guide topics")
		},
		Examples: []cli.Example{
			{Command: "leapmux guide"},
			{Command: "leapmux guide --topics --json"},
			{Command: "leapmux guide targets"},
		},
		Complete: func(cmd *cobra.Command, _ cli.ArgSpec, prefix string) ([]string, cobra.ShellCompDirective) {
			var matches []string
			if list, _ := cmd.Flags().GetBool("topics"); !list {
				for _, topic := range agentGuideTopics {
					if strings.HasPrefix(topic.ID, prefix) {
						matches = append(matches, topic.ID+"\t"+topic.Title)
					}
				}
			}
			return matches, cobra.ShellCompDirectiveNoFileComp
		},
		Run: func(inv *cli.Invocation) (any, error) {
			if boolFlag(inv, "topics") {
				if inv.HasArg("topic") {
					return nil, cli.Usagef("omit the topic when using --topics")
				}
				return guideIndex{Topics: agentGuideTopics}, nil
			}
			if !inv.HasArg("topic") {
				return guideText(agentGuide), nil
			}
			id := inv.Arg("topic")
			if strings.TrimSpace(id) == "" {
				return nil, cli.Usagef("the guide topic is empty")
			}
			for _, topic := range agentGuideTopics {
				if topic.ID == id {
					return guideText(topic.markdown), nil
				}
			}
			return nil, cli.Newf(cli.CodeNotFound, "no guide topic %q", id).
				WithHint("run leapmux guide --topics to list topics")
		},
	})
	cli.DisallowInheritedFlags(cmd, "hub")
	return cmd
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
	return json.Marshal(map[string]string{"guide": string(g)})
}
