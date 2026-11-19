package cmd

import (
	"github.com/spf13/cobra"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
	"github.com/leapmux/leapmux/internal/util/id"
)

// `agent clear` and `agent compact` queue the two context commands. Each is an
// input of its own kind in the agent's input queue, as the browser queues
// "/clear" and "/compact" when a user types them, so each waits for the
// inputs before it and for the end of the current turn.

// contextCommand is one context command: the kind of input that it queues,
// and the text that the input carries. The text is the command that a user
// types in the browser, so the transcript shows the same row for both, and a
// provider without native compaction receives "/compact" as a message.
type contextCommand struct {
	kind leapmuxv1.AgentInputKind
	text string
}

var (
	clearContext   = contextCommand{kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_CLEAR_CONTEXT, text: "/clear"}
	compactContext = contextCommand{kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_COMPACT_CONTEXT, text: "/compact"}
)

func newAgentClearCommand() *cobra.Command {
	return cli.NewLeaf(cli.Leaf{
		Use:     "clear <agent>",
		Short:   "Clear an agent's context, and continue in a fresh session",
		Mutates: true,
		Long: `Clear an agent's context. The command queues a clear. When the agent reads
it, the Worker stops the agent's process and starts the provider again in a
fresh session, in the same tab. The transcript keeps the earlier messages.

The clear waits in the agent's input queue behind the inputs before it, and
it waits for the current turn to end, so it never cuts off a reply. "agent
queue list" shows it, and "agent queue remove" takes it back while it waits.
A clear also restarts an agent whose start failed.

A subagent refuses a clear.`,
		Examples: []cli.Example{
			{Command: "leapmux control agent clear 87a19d5f"},
			{Comment: "Clear the context, then give the agent its next task.", Command: `leapmux control agent clear 87a19d5f && leapmux control agent send 87a19d5f "Review the diff of the last commit"`},
		},
		Complete: completeTargets,
		Run:      func(inv *cli.Invocation) (any, error) { return runContextCommand(inv, clearContext) },
	})
}

func newAgentCompactCommand() *cobra.Command {
	return cli.NewLeaf(cli.Leaf{
		Use:     "compact <agent>",
		Short:   "Ask an agent to summarize its context, to free room in its context window",
		Mutates: true,
		Long: `Ask an agent to compact its context: the provider replaces the conversation
so far with a summary, which frees room in the context window. The command
queues the request. A provider without a compaction command of its own
receives "/compact" as a message.

The request waits in the agent's input queue behind the inputs before it,
and it waits for the current turn to end. "agent queue list" shows it, and
"agent queue remove" takes it back while it waits.

A subagent refuses a compaction.`,
		Examples: []cli.Example{
			{Command: "leapmux control agent compact 87a19d5f"},
		},
		Complete: completeTargets,
		Run:      func(inv *cli.Invocation) (any, error) { return runContextCommand(inv, compactContext) },
	})
}

// runContextCommand queues command for the agent that the arguments give.
func runContextCommand(inv *cli.Invocation, command contextCommand) (any, error) {
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	got, err := s.resolveTab(resolve.KindAgent, inv.Arg("agent"), resolve.Need{WorkerID: true})
	if err != nil {
		return nil, err
	}
	if err := maybePreflightWorker(s.ctx, s.c, got.WorkerID); err != nil {
		return nil, err
	}
	inputID := id.Generate()
	if _, err := enqueueAgentInput(s.ctx, s.c, got, &leapmuxv1.EnqueueAgentInputRequest{
		AgentId: got.ID, InputId: inputID, Kind: command.kind, Text: command.text,
	}); err != nil {
		return nil, err
	}
	return agentResult{ID: got.ID, InputID: inputID}, nil
}
