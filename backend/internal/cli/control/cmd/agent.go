package cmd

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"golang.org/x/sync/errgroup"
	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
	"github.com/leapmux/leapmux/internal/util/agentlabels"
	"github.com/leapmux/leapmux/internal/worker/controlenv"
)

func newAgentCommand() *cobra.Command {
	return cli.SetAliases(cli.Group("agent", "Start, message, watch and configure coding agents",
		cli.NewLeaf(cli.Leaf{
			Use:     "list",
			Aliases: []string{"ls"},
			Short:   "List your agents with their states",
			Examples: []cli.Example{
				{Command: "leapmux agent list"},
				{Comment: "List the agents of the workspace that this tab lives in.", Command: "leapmux agent list --workspace self"},
			},
			Flags: func(fs *pflag.FlagSet) {
				fs.String("workspace", "", `list only the agents of this workspace (an id, a prefix, "title:<title>" or "self")`)
				fs.String("worker", "", `list only the agents hosted by this worker (an id, a prefix, "name:<name>" or "self")`)
				fs.String("state", "", "list only the agents in this activity state")
				cli.SetEnum(fs, "state", enumTokens(leapmuxv1.AgentActivityState(0))...)
				fs.String("provider", "", `list only the agents of this provider (such as claude, codex)`)
				fs.String("parent", "", `list only the direct child subagents of this parent agent (an id, prefix, title, or "self")`)
				fs.Bool("all", false, "include inactive or non-active agent process statuses in the table")
			},
			FlagComplete: map[string]cobra.CompletionFunc{
				"provider": completeProviderFlag,
			},
			Run: runAgentList,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:   "get [agent]",
			Short: "Show an agent's state and settings (default: this agent)",
			Examples: []cli.Example{
				{Command: "leapmux agent get"},
				{Comment: "Show every option that the agent offers.", Command: "leapmux agent get 87a19d5f --options"},
			},
			Flags: func(fs *pflag.FlagSet) {
				fs.Bool("options", false, "include every option group with its choices")
				addTargetWorkspaceFlag(fs)
			},
			Complete: completeTargets,
			Run:      runAgentGet,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:     "open",
			Mutates: true,
			Short:   "Start a new agent in a new tab",
			Next: []string{
				`leapmux agent send <id> "Run the tests" --wait`,
				`leapmux agent wait <id>`,
				`leapmux agent messages <id> --follow`,
			},
			Long: `Start a new agent in a new tab.

--resume continues an earlier session of the provider in the new tab. "agent
sessions" lists the sessions that it takes, for the same --provider, --worker
and --dir.

--request-id makes a retry safe. A second run with the same request id and
the same arguments opens no second agent: it prints the agent that the first
run opened, and it queues the --message no second time. A run with the same
request id and other arguments fails with conflict.`,
			Examples: []cli.Example{
				{Comment: "Start an agent beside this tab and keep its id.", Command: `A=$(leapmux agent open --provider claude --message "Run the tests" -q)`},
				{Comment: "Start an agent in a new git worktree.", Command: "leapmux agent open --worktree fix-flaky-spec --base main"},
				{Comment: "Continue an earlier session that \"agent sessions\" lists.", Command: "leapmux agent open --provider claude --resume 4f6c1e0a-9b7d-4c2e-8f3a-2d5b6c7e8f90"},
				{Comment: "Open an agent that a retry of the same command does not open twice.", Command: `leapmux agent open --provider codex --message "Review the diff" --request-id nightly-review-2026-11-19`},
			},
			Flags: func(fs *pflag.FlagSet) {
				fs.String("provider", "", `agent provider; when neither this flag nor the variable gives one, the only provider that the worker offers; "leapmux agent providers" lists them`)
				cli.SetEnvDefault(fs, "provider", controlenv.AgentProviderVariable)
				fs.String("model", "", "model")
				fs.String("effort", "", "reasoning effort")
				fs.String("permission-mode", "", "permission mode")
				fs.StringArray("option", nil, `provider option as key=value; repeat it for more than one; "agent get --options" lists them`)
				fs.String("title", "", "tab title (default: the worker picks one)")
				fs.String("name", "", "unique short agent slug (lowercase letters, digits, hyphens, underscores)")
				for _, name := range []string{"provider", "model", "effort", "permission-mode", "option", "title", "name"} {
					cli.SetFlagGroup(fs, name, "Agent")
				}
				fs.String("message", "", `queue a first message with the agent creation ("-" reads stdin)`)
				fs.String("file", "", `read the first message from this file ("-" reads stdin)`)
				fs.StringArray("attach", nil, "attach this file to the first message; repeat it for more than one")
				fs.String("resume", "", `resume this earlier session of the provider; "agent sessions" lists them`)
				addRequestIDFlag(fs)
				for _, name := range []string{"message", "file", "attach", "resume", "request-id"} {
					cli.SetFlagGroup(fs, name, "Session & Input")
				}
				fs.Bool("wait", false, "wait until the agent settles (and print its reply when a message was sent)")
				fs.Bool("all", false, "with --wait and a message, print every reply, not only the last one")
				fs.Duration("timeout", 0, "with --wait, stop waiting after this long, such as 90s or 30m; 0 waits without a limit")
				addOutputSchemaFlags(fs)
				for _, name := range []string{"wait", "all", "timeout", outputSchemaFlag, schemaRetriesFlag} {
					cli.SetFlagGroup(fs, name, "Wait")
				}
				addWorkingDirFlag(fs, "working directory of the agent")
				cli.SetFlagGroup(fs, "dir", "Git & Directory")
				addGitSetupFlags(fs)
				addPlacementFlags(fs)
			},
			// agent open has no target agent, so --model, --effort, and
			// --permission-mode intentionally register no option completer.
			FlagComplete: mergeFlagCompletions(
				map[string]cobra.CompletionFunc{"provider": completeProviderFlag},
				gitSetupFlagCompletions(),
				tabPositionFlagCompletions(),
			),
			Run: runAgentOpen,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:     "send <agent> [text]",
			Short:   "Send a message to an agent, and optionally wait for its reply",
			Mutates: true,
			Long: fmt.Sprintf(`Send a message to an agent.

The text is the second argument, "-" to read it from stdin, or --file. A
message that starts with "-" needs "--" before it, so that a misspelled flag
never reaches the agent as text.

The agent's input queue holds the message until the agent reads it.
"agent queue list" shows the queue.

A message that a process in an agent tab sends reaches the recipient as that
agent's message, not as the user's. The recipient reads it after a line that
states the sending agent and that the message is not from its operator, and
LeapMux shows the sender beside it. The Worker states the sender from the
tab's own credentials, so no flag sets or hides it. A message that an agent
sends to itself, such as a reminder that --at holds, reaches it as its own
message too. A message from a terminal tab, or from outside LeapMux, reaches
the agent as the user's.

--attach attaches a file, once for each file. A message with an attachment
needs no text. The CLI refuses a file that it cannot read, and a message
whose text and attachments hold more than %d MiB together. The Worker infers
the type of each file from its name and its content, and it refuses a file
that the agent's provider cannot read with invalid_argument. The error
states the file.

--at holds the message in the queue until a time:
  - An RFC 3339 time with its offset, such as 2026-11-20T09:00:00+09:00 or
    2026-11-20T00:00:00Z. The CLI assumes no time zone, so a time without an
    offset fails.
  - "+" and a duration from now, such as +10m or +1h30m.
A new schedule must lie in the future. The result states it in UTC.

--request-id makes a retry safe. A second run with the same request id, the
same agent and the same message queues nothing new, and with --wait it
prints the reply to the message of the first run. This holds also after
someone edited the message in the queue: the retry repeats the message that
the first run sent, and the queue keeps the edit. It holds also after
someone removed the message from the queue: for 7 days after the removal, a
retry queues nothing, and with --wait it fails with not_found and exits 6.
A run with the same request id and a message other than the first run's
fails with conflict, even when its message equals the edited one. A retry of
a scheduled message repeats its RFC 3339 timestamp, even after that time has
passed. A relative --at changes on each run and cannot use --request-id.

With --wait, a new message to an agent waiting for permission exits 5.
An explicit --request-id can still match an input the Worker recorded.
The command waits until the agent settles and prints the last reply as the
transcript shows it, or every reply with --all. It exits 6 if the agent
waits for permission or --timeout passes after the Worker acknowledges
the input. The output reports the last confirmed delivery state.
The timeout counts from the Worker's enqueue acknowledgment.`, contracts.MaxAgentInputItemBytes/(1024*1024)),
			Next: []string{
				`leapmux agent wait <agent>`,
				`leapmux agent messages <agent>`,
				`leapmux agent requests <agent>`,
			},
			Examples: []cli.Example{
				{Command: `leapmux agent send 87a19d5f "Run the tests"`},
				{Comment: "Send the contents of a file.", Command: "leapmux agent send 87a19d5f --file prompt.md"},
				{Comment: "Send a message that starts with a dash.", Command: `leapmux agent send 87a19d5f -- "-v is the verbose flag"`},
				{Comment: "Ask a question and print the answer.", Command: `leapmux agent send 87a19d5f "Which tests fail?" --wait --timeout 30m`},
				{Comment: "Attach a screenshot and a log.", Command: `leapmux agent send 87a19d5f "Why does the page look like this?" --attach screenshot.png --attach console.log`},
				{Comment: "Send the message in ten minutes.", Command: `leapmux agent send 87a19d5f "Check the deploy" --at +10m`},
				{Comment: "Send a message that a retry of the same command does not send twice.", Command: `leapmux agent send 87a19d5f "Run the release checklist" --request-id release-2026-11-19`},
			},
			Flags: func(fs *pflag.FlagSet) {
				fs.String("file", "", `read the message from this file ("-" reads stdin)`)
				fs.StringArray("attach", nil, "attach this file; repeat it for more than one")
				fs.String("at", "", "hold the message until this time: an RFC 3339 time with its offset, or + and a duration from now, such as +10m")
				fs.Bool("steer", false, "inject the message into the agent's active turn immediately (requires a steerable active turn)")
				fs.Bool("preempt", false, "interrupt the agent's active turn so this message dispatches next")
				addRequestIDFlag(fs)
				for _, name := range []string{"file", "attach", "at", "steer", "preempt", "request-id"} {
					cli.SetFlagGroup(fs, name, "Message")
				}
				fs.Bool("wait", false, "wait until the agent settles after the message, then print its last reply")
				fs.Bool("all", false, "with --wait, print every reply after the message, not only the last one")
				fs.Duration("timeout", 0, "with --wait, stop waiting after this long, such as 90s or 30m; 0 waits without a limit")
				addOutputSchemaFlags(fs)
				for _, name := range []string{"wait", "all", "timeout", outputSchemaFlag, schemaRetriesFlag} {
					cli.SetFlagGroup(fs, name, "Wait")
				}
				addTargetWorkspaceFlag(fs)
				cli.SetFlagGroup(fs, "workspace", "Scope")
			},
			Complete: completeTargets,
			Run:      runAgentSend,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:   "wait <agent>...",
			Short: "Wait until agents settle, or until they reach another state",
			// The calling agent cannot settle while it waits, so the wait
			// refuses it, and the help offers no "self".
			Args: map[string]string{"agent": resolve.KindAgent.HelpWithoutSelf()},
			Long: `Wait until each agent reaches a requested state, then print the state of each.

Repeat --until to accept several states. Each agent can reach a different
requested state. A state reported at any moment during the wait counts,
including the agent's initial state.
"settled" is "idle" or "waiting_for_user": the agent works no more, because it
finished or because it waits for an answer to a permission prompt. An agent
whose input queue holds a message that it will read is neither idle nor
settled yet, as an agent that "agent open --message" just started.

The command exits 0 when the condition holds. It exits 6 when the wait ended
without it: --timeout passed, or an agent closed before it reached a requested state.
It prints the state of each agent in both cases.`,
			Examples: []cli.Example{
				{Command: "leapmux agent wait 87a19d5f"},
				{Comment: "Wait for an agent to start working or ask for input.", Command: "leapmux agent wait 87a19d5f --until working --until waiting_for_user"},
				{Comment: "Wait until the first of two agents waits for an answer, for 10 minutes at most.", Command: "leapmux agent wait 87a19d5f 5c2e0b91 --until waiting_for_user --any --timeout 10m"},
			},
			Flags: func(fs *pflag.FlagSet) {
				fs.StringArray("until", []string{string(waitUntilSettled)}, "a state to wait for; repeat to accept alternatives")
				cli.SetEnum(fs, "until", waitUntilTokens()...)
				fs.Bool("any", false, "end the wait as soon as one agent reaches a requested state")
				fs.Duration("timeout", 0, "stop waiting after this long, such as 90s or 10m; 0 waits without a limit")
				addTargetWorkspaceFlag(fs)
			},
			Complete: completeOtherAgents,
			Run:      runAgentWait,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:     "requests [agent]...",
			Aliases: []string{"req"},
			Short:   "List the permission prompts, plans and questions that agents wait on (default: every agent)",
			Args:    map[string]string{"agent": resolve.KindAgent.Help() + " Without one, the command lists the requests of every agent."},
			Long: `List the pending control requests of agents: permission prompts, plans
to approve, questions, MCP elicitation forms and extension dialogs.

Each request prints with its handle. "agent approve", "agent deny" and
"agent answer" take the agent and the handle, or a unique prefix of the
handle. A request that the agent replaces gets a new handle, so an answer
never reaches a request that you did not read.

The text form prints each request in full, with the numbers that --option
takes. --json prints each prompt as data.`,
			Next: []string{
				`leapmux agent approve <agent> [request]`,
				`leapmux agent deny <agent> [request]`,
				`leapmux agent answer <agent> [request] --option 1`,
			},
			Examples: []cli.Example{
				{Command: "leapmux agent requests"},
				{Comment: "List the requests of one agent, as JSON.", Command: "leapmux agent requests 87a19d5f --json"},
			},
			Flags: func(fs *pflag.FlagSet) {
				fs.String("workspace", "", `scope listing or target lookup to this workspace (an id, a prefix, "title:<title>" or "self")`)
			},
			Complete: completeTargets,
			Run:      runAgentRequests,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:     "approve <agent> [request]",
			Short:   "Approve a permission prompt or a plan, or confirm a dialog",
			Mutates: true,
			Args:    map[string]string{"agent": resolve.KindAgent.HelpWithoutSelf(), "request": requestArgHelp},
			Long: `Approve a pending request:

  - A permission prompt: its one-time approval. Choose another option, such
    as an approval for the session, with "agent answer --option".
  - A plan: the approval. --permission-mode and --clear-context set how the
    agent implements it.
  - An MCP elicitation: the accept, with the form's default values. Fill a
    form with "agent answer --field".
  - A confirm dialog: the confirmation.

When [request] is omitted and the agent has exactly one pending request, it
approves that request automatically. A single globally unique request handle
also resolves its owning agent automatically.

An agent cannot answer its own requests.`,
			Examples: []cli.Example{
				{Command: "leapmux agent approve 87a19d5f 3kQ9xZ1b"},
				{Comment: "Approve the agent's sole pending request.", Command: "leapmux agent approve 87a19d5f"},
				{Comment: "Approve a plan, and implement it in a fresh context.", Command: "leapmux agent approve 87a19d5f 3kQ9 --permission-mode acceptEdits --clear-context"},
			},
			Flags: func(fs *pflag.FlagSet) {
				addPlanApprovalFlags(fs)
				addTargetWorkspaceFlag(fs)
			},
			FlagComplete: map[string]cobra.CompletionFunc{
				"permission-mode": completeAgentOptionFlag("permission-mode"),
			},
			Complete: completeOtherAgents,
			Run:      runAgentApprove,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:     "deny <agent> [request]",
			Short:   "Refuse a permission prompt, a plan, a question or a dialog",
			Mutates: true,
			Args:    map[string]string{"agent": resolve.KindAgent.HelpWithoutSelf(), "request": requestArgHelp},
			Long: `Refuse a pending request:

  - A permission prompt: its one-time refusal, with --message as the reason.
  - A plan: the refusal, with --message as what to change.
  - A question: the refusal to answer, with --message as the reason.
  - An MCP elicitation: the decline.
  - A dialog: the refusal of a confirm dialog, or the cancel of another.

When [request] is omitted and the agent has exactly one pending request, it
refuses that request automatically. A single globally unique request handle
also resolves its owning agent automatically.

An agent cannot answer its own requests.`,
			Examples: []cli.Example{
				{Command: `leapmux agent deny 87a19d5f 3kQ9xZ1b --message "Run the unit tests only"`},
			},
			Flags: func(fs *pflag.FlagSet) {
				fs.String("message", "", "the reason of the refusal, which the agent reads")
				addTargetWorkspaceFlag(fs)
			},
			Complete: completeOtherAgents,
			Run:      runAgentDeny,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:     "answer <agent> [request] [text]...",
			Short:   "Answer a question, fill a form, or choose any offered option",
			Mutates: true,
			Args: map[string]string{
				"agent":   resolve.KindAgent.HelpWithoutSelf(),
				"request": requestArgHelp,
				"text":    `A typed answer, or "-" to read it from standard input. With several questions, write "<question>:<text>".`,
			},
			Long: `Answer a pending request with any answer that it offers. "agent requests"
numbers the options of each request. When [request] is omitted and the agent
has exactly one pending request, it answers that request automatically.

  - A question: --option chooses an option, and --text (or a text argument)
    gives a typed answer. Use --text when omitting [request] so the typed
    text is never parsed as a request handle. A multiple-choice question
    takes both: the answer then holds the chosen options and the text. With
    several questions, write each value as "<question>:<value>", such as
    --option 2:1 or --text "1:Use the cache". A question that no --option
    answers keeps the options that the agent selected for it, which "agent
    requests" shows as its default. With no value at all, the command sends
    those defaults, which must answer every question that needs an answer.
  - A permission prompt: --option chooses any option, such as an approval
    for the session. --message gives the reason of a refusing option.
  - A plan: --option chooses one of its choices. --message gives what to
    change, and --permission-mode and --clear-context apply to an approval.
  - An MCP elicitation: --field fills the form, once for each field, or
    once for each value of a list field. A field that "agent requests"
    lists with a condition takes a value only when the condition holds,
    and a field that it does not list keeps its default. --option chooses
    an accept choice.
  - An input or editor dialog: --text or a text argument.

An agent cannot answer its own requests.`,
			Examples: []cli.Example{
				{Comment: "Choose the second option of a question.", Command: "leapmux agent answer 87a19d5f 3kQ9 --option 2"},
				{Comment: "Choose an option and add typed text without specifying the sole request handle.", Command: `leapmux agent answer 87a19d5f --option 1 --text "Also check metrics"`},
				{Comment: "Answer an agent's sole pending question or input dialog with typed text.", Command: `leapmux agent answer 87a19d5f --text "v2.4.0"`},
				{Comment: "Choose two options of a multiple-choice question, and type one more answer.", Command: `leapmux agent answer 87a19d5f 3kQ9 --option 1 --option 3 "Metrics"`},
				{Comment: "Answer two questions.", Command: `leapmux agent answer 87a19d5f 3kQ9 --option 1:2 "2:Use the staging database"`},
				{Comment: "Approve a command for the rest of the session.", Command: "leapmux agent answer 87a19d5f 3kQ9 --option 2"},
				{Comment: "Fill an MCP form.", Command: "leapmux agent answer 87a19d5f 3kQ9 --field repository=leapmux --field private=true"},
			},
			Flags: func(fs *pflag.FlagSet) {
				fs.StringArray("option", nil, `an option by its number, or "<question>:<number>" with several questions; repeat it for more than one`)
				fs.StringArray("text", nil, `a typed answer (or "-" to read stdin); repeat for more than one`)
				fs.StringArray("field", nil, "a form field as <name>=<value>; repeat it for more than one")
				fs.String("message", "", "the reason of a refusing option or choice, which the agent reads")
				addPlanApprovalFlags(fs)
				addTargetWorkspaceFlag(fs)
			},
			FlagComplete: map[string]cobra.CompletionFunc{
				"permission-mode": completeAgentOptionFlag("permission-mode"),
			},
			Complete: completeOtherAgents,
			Run:      runAgentAnswer,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:     "interrupt <agent>...",
			Short:   "Stop the current turn of one or more agents",
			Mutates: true,
			Examples: []cli.Example{
				{Command: "leapmux agent interrupt 87a19d5f"},
			},
			Flags: func(fs *pflag.FlagSet) {
				fs.String("reason", "", "a reason that the agent's transcript records")
				addTargetWorkspaceFlag(fs)
			},
			Complete: completeTargets,
			Run:      runAgentInterrupt,
		}),
		newAgentClearCommand(),
		newAgentCompactCommand(),
		cli.NewLeaf(cli.Leaf{
			Use:     "set <agent>",
			Short:   "Change an agent's model, effort, permission mode or another option",
			Mutates: true,
			Examples: []cli.Example{
				{Command: "leapmux agent set 87a19d5f --model opus --effort high"},
				{Comment: "Set an option that only one provider has.", Command: "leapmux agent set 87a19d5f --option sandbox=workspace-write"},
			},
			Flags: func(fs *pflag.FlagSet) {
				fs.String("model", "", "model id")
				fs.String("effort", "", "effort id")
				fs.String("permission-mode", "", "permission mode")
				fs.StringArray("option", nil, `provider option as key=value; repeat it for more than one; "agent get --options" lists them`)
				addTargetWorkspaceFlag(fs)
			},
			FlagComplete: agentOptionFlagCompletions(),
			Complete:     completeTargets,
			Run:          runAgentSet,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:     "messages [agent]",
			Aliases: []string{"msg"},
			Short:   "Print an agent's messages, or follow new ones (default: this agent)",
			Long: `Print an agent's messages, or follow new ones.

A message that another agent sent prints its sender after its source, and
--json states the sender as sender.`,
			Examples: []cli.Example{
				{Command: "leapmux agent messages 87a19d5f"},
				{Comment: "Print new messages as they arrive, one JSON envelope per line.", Command: "leapmux agent messages 87a19d5f --follow --json"},
			},
			Flags: func(fs *pflag.FlagSet) {
				// A single page anchor mirrors MessagePageAnchor: latest (default),
				// oldest, before, or after. `before`/`after` page relative to
				// --cursor-seq; `latest`/`oldest` ignore it. One selector instead of
				// three mutually-exclusive flags -- you can't pick two pages at once.
				fs.String("anchor", "latest", "page to print")
				cli.SetEnum(fs, "anchor", "latest", "oldest", "before", "after")
				fs.Int64("cursor-seq", 0, "the seq that --anchor before or after starts from; the page excludes it")
				fs.Int("limit", contracts.MessagePageLimit, "messages per page")
				fs.Bool("follow", false, "print new messages as they arrive, until interrupted")
				for _, name := range []string{"anchor", "cursor-seq", "limit", "follow"} {
					cli.SetFlagGroup(fs, name, "Pagination")
				}
				fs.Bool("raw", false, "print raw provider JSON payloads in text mode")
				fs.Bool("full", false, "do not truncate long message lines in text mode")
				fs.Bool("markdown", false, "render messages as Markdown sections")
				fs.Bool("summary", false, "append current goal, todos, and active background tasks after the messages")
				for _, name := range []string{"raw", "full", "markdown", "summary"} {
					cli.SetFlagGroup(fs, name, "Display")
				}
				addTargetWorkspaceFlag(fs)
				cli.SetFlagGroup(fs, "workspace", "Scope")
			},
			Complete: completeTargets,
			Run:      runAgentMessages,
		}),
		newAgentQueueCommand(),
		newAgentGoalCommand(),
		newAgentTodosCommand(),
		newAgentTasksCommand(),
		newAgentUsageCommand(),
		newAgentTreeCommand(),
		newAgentOpenTabCommand(),
		newAgentStartupCommand(),
		newAgentRetryTurnCommand(),
		newAgentApproveReviewCommand(),
		newAgentMessageCommand(),
		newAgentSpanCommand(),
		newAgentMarksCommand(),
		newAgentExportCommand(),
		newAgentRecoveriesCommand(),
		newAgentRecoverCommand(),
		newAgentSearchCommand(),
		newAgentForkCommand(),
		newAgentRequestStatusCommand(),
		cli.NewLeaf(cli.Leaf{
			Use:   "providers",
			Short: "List the agent providers that a worker offers",
			Examples: []cli.Example{
				{Command: "leapmux agent providers"},
				{Command: "leapmux agent providers --worker name:build-box"},
			},
			Flags: addWorkerFlag,
			Run:   runAgentProviders,
		}),
		newAgentSessionsCommand(),
		newTabCloseLeafFor(resolve.KindAgent, "agent"),
		newTabRenameLeafFor(resolve.KindAgent, "agent"),
		newTabRevealLeafFor(resolve.KindAgent, "agent"),
	), "ag")
}

// inputText reads the text of a message: the positional argument argName,
// "-" for stdin, or --file. Exactly one source applies, and the text must
// not be empty.
func inputText(inv *cli.Invocation, argName string) (string, error) {
	hasArg := inv.HasArg(argName)
	if inv.Changed("file") {
		if hasArg {
			return "", cli.Usagef("give the text as an argument or with --file, not both")
		}
		data, err := readFileOrStdin(inv.Stdin, stringFlag(inv, "file"), "--file")
		if err != nil {
			return "", err
		}
		return nonEmptyText(string(data))
	}
	if !hasArg {
		return "", cli.Usagef("missing <%s>", argName).
			WithHint(`give it as an argument, as "-" to read stdin, or with --file`)
	}
	text := inv.Arg(argName)
	if text == "-" {
		data, err := readStdinGuarded(inv.Stdin)
		if err != nil {
			return "", err
		}
		text = string(data)
	}
	return nonEmptyText(text)
}

func nonEmptyText(text string) (string, error) {
	if strings.TrimSpace(text) == "" {
		return "", cli.Usagef("the text is empty")
	}
	return text, nil
}

// workerAgents are agents of one Worker, which one channel to that Worker
// serves.
type workerAgents struct {
	workerID string
	agentIDs []string
}

// groupAgentsByWorker groups the agents by Worker, in the order of the first
// agent of each, and lists the agents in the order of the arguments. An
// agent that two arguments give counts once, at its first place.
func groupAgentsByWorker(targets []tabTarget) (groups []workerAgents, agentIDs []string) {
	seen := map[string]bool{}
	for _, t := range targets {
		if seen[t.ID] {
			continue
		}
		seen[t.ID] = true
		agentIDs = append(agentIDs, t.ID)
		i := slices.IndexFunc(groups, func(g workerAgents) bool { return g.workerID == t.WorkerID })
		if i < 0 {
			groups = append(groups, workerAgents{workerID: t.WorkerID})
			i = len(groups) - 1
		}
		groups[i].agentIDs = append(groups[i].agentIDs, t.ID)
	}
	return groups, agentIDs
}

// agentResult is the result of a command that acted on an agent and has
// nothing else to report.
type agentResult struct {
	ID string `json:"id"`
	// InputID is the id of the input that the agent queued.
	InputID string `json:"input_id,omitempty"`
	// RequestID is the explicit or auto-generated idempotency key for safe retry.
	RequestID string `json:"request_id,omitempty"`
	// ScheduledAt is the UTC time before which the agent does not read the
	// input, for a message that --at holds.
	ScheduledAt string `json:"scheduled_at,omitempty"`
}

// QuietLines prints the newly queued input ID when present (for agent send,
// clear, compact), otherwise the agent ID.
func (r agentResult) QuietLines() []string {
	if r.InputID != "" {
		return []string{r.InputID}
	}
	if r.ID == "" {
		return nil
	}
	return []string{r.ID}
}

// runAgentSend forwards a user message to the agent. The Worker queues it,
// and the agent reads it when its current turn ends. With --wait it waits
// for the agent's reply; see sendAndWait.
func runAgentSend(inv *cli.Invocation) (any, error) {
	wait := boolFlag(inv, "wait")
	steer := boolFlag(inv, "steer")
	preempt := boolFlag(inv, "preempt")
	if steer && preempt {
		return nil, cli.Usagef("--steer and --preempt exclude each other")
	}
	if (steer || preempt) && inv.Changed("at") {
		return nil, cli.Usagef("--steer and --preempt cannot be combined with --at")
	}
	if !wait {
		for _, name := range []string{"all", "timeout", outputSchemaFlag, schemaRetriesFlag} {
			if inv.Changed(name) {
				return nil, cli.Usagef("--%s needs --wait", name)
			}
		}
	}
	timeout, err := waitTimeoutFlag(inv)
	if err != nil {
		return nil, err
	}
	spec, err := readSendSpec(inv, time.Now())
	if err != nil {
		return nil, err
	}
	promptUsedStdin := (inv.HasArg("text") && inv.Arg("text") == "-") || (inv.Changed("file") && stringFlag(inv, "file") == "-")
	schemaSpec, err := readOutputSchemaSpec(inv, wait, true, promptUsedStdin)
	if err != nil {
		return nil, err
	}
	// A wait runs until the agent settles, so only each call has a deadline.
	open := openSession
	if wait {
		open = openStreamSession
	}
	s, err := open(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	reqSession := s.bounded()
	defer reqSession.Close()
	got, err := reqSession.resolveTab(resolve.KindAgent, inv.Arg("agent"), resolve.Need{WorkerID: true})
	if err != nil {
		return nil, err
	}
	if err := maybePreflightWorker(reqSession.ctx, s.c, got.WorkerID); err != nil {
		return nil, err
	}
	inputID := requestScopedID(spec.requestID, requestScopeAgentSend, got.ID)
	receipt := retryReceipt{
		RequestID:    spec.requestID,
		Explicit:     spec.explicitRequestID,
		QueryCommand: buildAgentRequestStatusCmd(got.ID, spec.requestID),
		RetryCommand: buildAgentSendRetryCmd(inv, got.ID, spec.message.text, wait, spec.requestID),
	}
	delivery := sendDeliveryNormal
	if steer {
		delivery = sendDeliverySteer
	} else if preempt {
		delivery = sendDeliveryPreempt
	}
	if !wait {
		stage := retryStageDuringSend
		if err := withWorkerChannel(reqSession.ctx, s.c, got.WorkerID, func(w workerCall) error {
			var ack leapmuxv1.EnqueueAgentInputResponse
			if err := w.Call(reqSession.ctx, leapmuxv1connect.WorkerAgentInputServiceEnqueueAgentInputProcedure, spec.message.request(got.ID, inputID), &ack); err != nil {
				return err
			}
			if delivery != sendDeliveryNormal && ack.GetAcceptedSeq() <= 0 {
				stage = retryStageDuringWait
				return applySendDeliveryModeOn(reqSession.ctx, w, got.ID, inputID, delivery)
			}
			return nil
		}); err != nil {
			return nil, wrapRetryReceipt(err, stage, receipt)
		}
		return agentResult{ID: got.ID, InputID: inputID, RequestID: spec.requestID, ScheduledAt: spec.message.scheduledAt}, nil
	}
	result, outcome, stage, err := sendAndWait(s, sendAndWaitRequest{
		target:    got,
		message:   spec.message,
		requestID: spec.requestID,
		inputID:   inputID,
		delivery:  delivery,
		all:       boolFlag(inv, "all"),
		timeout:   timeout,
		schema:    schemaSpec,
	})
	if err != nil {
		return nil, wrapRetryReceipt(err, stage, receipt)
	}
	result.RequestID = spec.requestID
	if outcome == replyWaitSettled {
		return result, nil
	}
	return nil, agentReplyEnded(inv.Printer, result, outcome, timeout)
}

type agentBatchResult struct {
	Agents []agentResult `json:"agents"`
}

func (r agentBatchResult) QuietLines() []string {
	out := make([]string, 0, len(r.Agents))
	for _, a := range r.Agents {
		out = append(out, a.QuietLines()...)
	}
	return out
}

func applySendDeliveryModeOn(ctx context.Context, w workerCall, agentID, inputID string, mode sendDeliveryMode) error {
	switch mode {
	case sendDeliverySteer:
		return w.Call(ctx, leapmuxv1connect.WorkerAgentInputServiceSteerQueuedAgentInputProcedure,
			&leapmuxv1.SteerQueuedAgentInputRequest{AgentId: agentID, InputId: inputID}, nil)
	case sendDeliveryPreempt:
		return w.Call(ctx, leapmuxv1connect.WorkerAgentInputServicePreemptQueuedAgentInputProcedure,
			&leapmuxv1.PreemptQueuedAgentInputRequest{AgentId: agentID, InputId: inputID}, nil)
	default:
		return nil
	}
}

func runAgentInterrupt(inv *cli.Invocation) (any, error) {
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	targets, err := s.resolveVariadicAgentTargets(inv.Variadic("agent"), resolve.Need{WorkerID: true})
	if err != nil {
		return nil, err
	}
	reason := stringFlag(inv, "reason")
	groups, orderedIDs := groupAgentsByWorker(targets)
	for _, grp := range groups {
		if err := maybePreflightWorker(s.ctx, s.c, grp.workerID); err != nil {
			return nil, err
		}
	}
	eg, gctx := errgroup.WithContext(s.ctx)
	eg.SetLimit(detailFanoutLimit)
	for _, grp := range groups {
		grp := grp
		eg.Go(func() error {
			return withWorkerChannel(gctx, s.c, grp.workerID, func(w workerCall) error {
				for _, id := range grp.agentIDs {
					if err := w.Call(gctx, leapmuxv1connect.WorkerAgentServiceInterruptAgentProcedure, &leapmuxv1.InterruptAgentRequest{AgentId: id, Reason: reason}, nil); err != nil {
						return err
					}
				}
				return nil
			})
		})
	}
	if err := eg.Wait(); err != nil {
		return nil, err
	}
	if len(orderedIDs) == 1 {
		return agentResult{ID: orderedIDs[0]}, nil
	}
	rows := make([]agentResult, 0, len(orderedIDs))
	for _, id := range orderedIDs {
		rows = append(rows, agentResult{ID: id})
	}
	return agentBatchResult{Agents: rows}, nil
}

// runAgentGet prints the Worker's record of one agent. It reuses
// ListAgents with a single tab id rather than a GetAgent RPC, since the
// Worker already filters by id.
func runAgentGet(inv *cli.Invocation) (any, error) {
	arg := strings.TrimSpace(inv.Arg("agent"))
	if (arg == "" || arg == resolve.SelfTarget) && (resolve.Listing{}).SelfTabID(resolve.KindAgent) == "" {
		return nil, cli.Usagef(`"self" selects your own agent, and this shell does not run inside a LeapMux agent tab`).
			WithHint("give the agent's id instead")
	}
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
	info, err := s.agentInfo(got)
	if err != nil {
		return nil, err
	}
	withOptions := boolFlag(inv, "options")
	result := agentRowOf(info, withOptions)
	result.Settings = additionalAgentSettings(info.GetOptionGroups())
	return result, nil
}

// agentInfo reads the Worker's record of the agent t.
func (s *session) agentInfo(t tabTarget) (*leapmuxv1.AgentInfo, error) {
	var resp leapmuxv1.ListAgentsResponse
	if err := callInnerRPC(s.ctx, s.c, t.WorkerID, leapmuxv1connect.WorkerAgentServiceListAgentsProcedure, &leapmuxv1.ListAgentsRequest{TabIds: []string{t.ID}}, &resp); err != nil {
		return nil, err
	}
	for _, a := range resp.GetAgents() {
		if a.GetId() == t.ID {
			return a, nil
		}
	}
	return nil, cli.Newf(cli.CodeNotFound, "the worker has no agent %s", t.ID)
}

// agentList is the result of `agent list`.
type agentList struct {
	Agents     []agentRow `json:"agents"`
	hideStatus bool
}

func (l agentList) DefaultHiddenColumns() []string {
	if l.hideStatus {
		return []string{"status"}
	}
	return nil
}

func (l agentList) QuietLines() []string {
	out := make([]string, 0, len(l.Agents))
	for _, a := range l.Agents {
		out = append(out, a.ID)
	}
	return out
}

// runAgentList lists the agent tabs, each joined with the Worker's record:
// its title, its provider, its state and its model.
func runAgentList(inv *cli.Invocation) (any, error) {
	stateFilter := strings.TrimSpace(stringFlag(inv, "state"))
	if stateFilter != "" && !slices.Contains(enumTokens(leapmuxv1.AgentActivityState(0)), stateFilter) {
		return nil, cli.Usagef("--state must be one of %s; got %q", strings.Join(enumTokens(leapmuxv1.AgentActivityState(0)), ", "), stateFilter)
	}
	providerFilter, err := parseProviderFlag(stringFlag(inv, "provider"))
	if err != nil {
		return nil, err
	}
	if inv.Changed("parent") && strings.TrimSpace(stringFlag(inv, "parent")) == "" {
		return nil, cli.Usagef("--parent is empty")
	}
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	tabs, err := s.listWorkspaceTabs(leapmuxv1.TabType_TAB_TYPE_AGENT)
	if err != nil {
		return nil, err
	}
	if inv.Changed("worker") {
		w, err := s.resolveOne(resolve.KindWorker, stringFlag(inv, "worker"))
		if err != nil {
			return nil, err
		}
		filtered := make([]*leapmuxv1.WorkspaceTab, 0, len(tabs))
		for _, t := range tabs {
			if t.GetWorkerId() == w.ID {
				filtered = append(filtered, t)
			}
		}
		tabs = filtered
	}
	details, workerNames := fetchTabDetailsAndWorkers(s.ctx, s.c, tabs, true)
	parentID := ""
	if inv.Changed("parent") {
		parentInput := stringFlag(inv, "parent")
		base := s.listing()
		listing := resolve.Listing{
			Tabs: func(context.Context) ([]*leapmuxv1.WorkspaceTab, error) {
				return tabs, nil
			},
			TabTitles: func(_ context.Context, _ []*leapmuxv1.WorkspaceTab) map[string]string {
				titles := make(map[string]string, len(details))
				for id, d := range details {
					titles[id] = d.Title
				}
				return titles
			},
			ParentAgent: func(ctx context.Context, workerID, selfAgentID string) (string, error) {
				if d := details[selfAgentID]; d.Agent != nil {
					return d.Agent.GetParentAgentId(), nil
				}
				return base.ParentAgent(ctx, workerID, selfAgentID)
			},
		}
		target, err := resolve.Canonical(s.ctx, listing, resolve.KindAgent, parentInput)
		if err != nil && (inv.Changed("workspace") || inv.Changed("worker")) && cli.FromError(err).Code == cli.CodeNotFound {
			p, fallbackErr := s.resolveTab(resolve.KindAgent, parentInput, resolve.Need{})
			if fallbackErr != nil {
				return nil, fallbackErr
			}
			target = resolve.Target{ID: p.ID}
			err = nil
		}
		if err != nil {
			return nil, err
		}
		parentID = target.ID
	}
	rows := make([]agentRow, 0, len(tabs))
	for _, t := range tabs {
		var row agentRow
		if d := details[t.GetTab().GetTabId()]; d.Agent != nil {
			if providerFilter != leapmuxv1.AgentProvider_AGENT_PROVIDER_UNSPECIFIED && d.Agent.GetAgentProvider() != providerFilter {
				continue
			}
			row = agentRowOf(d.Agent, false)
		} else {
			if providerFilter != leapmuxv1.AgentProvider_AGENT_PROVIDER_UNSPECIFIED {
				continue
			}
			// The Worker did not answer: the hub's row is all there is.
			row = agentRow{ID: t.GetTab().GetTabId(), WorkerID: t.GetWorkerId()}
		}
		row.WorkspaceID = t.GetWorkspaceId()
		if name := workerNames[t.GetWorkerId()]; name != "" {
			row.Worker = name
		} else {
			row.Worker = cli.ShortID(t.GetWorkerId())
		}
		if stateFilter != "" && !strings.EqualFold(row.State, stateFilter) {
			continue
		}
		if parentID != "" && row.ParentAgentID != parentID {
			continue
		}
		rows = append(rows, row)
	}
	showStatus := boolFlag(inv, "all")
	if !showStatus {
		for _, r := range rows {
			if r.Status != "" && r.Status != "active" {
				showStatus = true
				break
			}
		}
	}
	return agentList{Agents: rows, hideStatus: !showStatus}, nil
}

// providerRow is one agent provider that a Worker offers.
type providerRow struct {
	Name string `json:"name" table:"NAME"`
	// Aliases lists every spelling that --provider accepts for the
	// provider, the canonical one first.
	Aliases []string `json:"aliases" table:"ALIASES"`
}

// providerList is the result of `agent providers`.
type providerList struct {
	Providers []providerRow `json:"providers"`
}

func (l providerList) QuietLines() []string {
	out := make([]string, 0, len(l.Providers))
	for _, p := range l.Providers {
		out = append(out, p.Name)
	}
	return out
}

// runAgentProviders lists the agent providers a worker supports.
func runAgentProviders(inv *cli.Invocation) (any, error) {
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	workerID, err := s.workerScope()
	if err != nil {
		return nil, err
	}
	if err := maybePreflightWorker(s.ctx, s.c, workerID); err != nil {
		return nil, err
	}
	var resp leapmuxv1.ListAvailableProvidersResponse
	if err := callInnerRPC(s.ctx, s.c, workerID, leapmuxv1connect.WorkerAgentServiceListAvailableProvidersProcedure, &leapmuxv1.ListAvailableProvidersRequest{}, &resp); err != nil {
		return nil, err
	}
	rows := make([]providerRow, 0, len(resp.GetProviders()))
	for _, p := range resp.GetProviders() {
		rows = append(rows, providerRow{Name: agentProviderName(p), Aliases: agentlabels.AliasesFor(p)})
	}
	return providerList{Providers: rows}, nil
}

// providerListerFn is the inner-RPC seam resolveProvider issues its one call
// through, so it can be unit-tested without standing up a real E2EE channel or
// local-IPC socket.
//
// It matches workerCall.Call rather than a free function: the worker and the
// transport are already bound by the caller, so this names only the method and
// its messages -- and there is no per-call workerID for a caller to pass
// inconsistently with the channel it is reusing.
type providerListerFn func(ctx context.Context, method string, in proto.Message, out proto.Message) error

// defaultProviderInput returns the trimmed provider selection from --provider,
// falling back when the flag is unset to $LEAPMUX_CONTROL_AGENT_PROVIDER and
// then the saved control context provider.
func defaultProviderInput(inv *cli.Invocation) string {
	if inv.Changed("provider") {
		return strings.TrimSpace(stringFlag(inv, "provider"))
	}
	if env := strings.TrimSpace(os.Getenv(controlenv.AgentProviderVariable)); env != "" {
		return env
	}
	if ctxCfg, err := control.LoadContext(); err == nil {
		return strings.TrimSpace(ctxCfg.Provider)
	}
	return ""
}

// parseProviderFlag trims raw and parses it into an AgentProvider enum.
// Empty or whitespace-only input returns AGENT_PROVIDER_UNSPECIFIED and nil.
func parseProviderFlag(raw string) (leapmuxv1.AgentProvider, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return leapmuxv1.AgentProvider_AGENT_PROVIDER_UNSPECIFIED, nil
	}
	p, ok := agentlabels.ParseProvider(trimmed)
	if !ok {
		return leapmuxv1.AgentProvider_AGENT_PROVIDER_UNSPECIFIED, cli.Usagef("unknown --provider %q", trimmed).
			WithHint("accepted values: %s", strings.Join(allProviderAliases(), ", "))
	}
	return p, nil
}

// resolveProvider turns the user-supplied --provider value (possibly
// pre-filled from $LEAPMUX_CONTROL_AGENT_PROVIDER) into an
// AgentProvider enum, querying the worker's installed providers when
// the caller passed no value at all.
//
// Three rejection paths, each with a code a script can branch on:
//
//   - raw trimmed != "" and not a known alias → usage (lists every alias the
//     parser accepts; the typo is local, no need for an RPC).
//   - raw trimmed == "" and the worker reports zero installed providers →
//     conflict, with a fix-it hint.
//   - raw trimmed == "" and the worker reports more than one → usage, with the
//     display names of what's installed.
//
// The exactly-one case is the silent success path: a worker with a
// single provider needs no `--provider` flag at all, matching the
// frontend's first-launch UX where the picker auto-selects when only
// one option is available.
func resolveProvider(ctx context.Context, raw string, list providerListerFn) (leapmuxv1.AgentProvider, error) {
	p, err := parseProviderFlag(raw)
	if err != nil {
		return 0, err
	}
	if p != leapmuxv1.AgentProvider_AGENT_PROVIDER_UNSPECIFIED {
		return p, nil
	}
	var resp leapmuxv1.ListAvailableProvidersResponse
	if err := list(ctx, leapmuxv1connect.WorkerAgentServiceListAvailableProvidersProcedure, &leapmuxv1.ListAvailableProvidersRequest{}, &resp); err != nil {
		return 0, err
	}
	switch providers := resp.GetProviders(); len(providers) {
	case 0:
		return 0, cli.Newf(cli.CodeConflict, "the worker has no agent provider installed").
			WithHint("install a provider on the worker, or pass --provider")
	case 1:
		return providers[0], nil
	default:
		names := make([]string, 0, len(providers))
		for _, p := range providers {
			names = append(names, agentlabels.DisplayName(p))
		}
		return 0, cli.Usagef("the worker offers more than one provider (%s)", strings.Join(names, ", ")).
			WithHint("pass --provider to choose one")
	}
}

// allProviderAliases returns every alias ParseProvider accepts,
// grouped by provider and ordered deterministically. Used in the
// "unknown --provider" error so the user sees the full set of valid
// inputs without needing to run `agent providers` against a worker.
func allProviderAliases() []string {
	var out []string
	for _, p := range agentlabels.AllProviders() {
		out = append(out, agentlabels.AliasesFor(p)...)
	}
	return out
}
