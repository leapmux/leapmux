package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
	"github.com/leapmux/leapmux/internal/util/agentlabels"
)

func newAgentCommand() *cobra.Command {
	return cli.Group("agent", "Start, message, watch and configure coding agents",
		cli.NewLeaf(cli.Leaf{
			Use:   "list",
			Short: "List your agents with their states",
			Examples: []cli.Example{
				{Command: "leapmux control agent list"},
				{Comment: "List the agents of the workspace that this tab lives in.", Command: "leapmux control agent list --workspace self"},
			},
			Flags: func(fs *pflag.FlagSet) {
				fs.String("workspace", "", `list only the agents of this workspace (an id, a prefix, "title:<title>" or "self")`)
			},
			Run: runAgentList,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:   "get [agent]",
			Short: "Show an agent's state and settings (default: this agent)",
			Examples: []cli.Example{
				{Command: "leapmux control agent get"},
				{Comment: "Show every option that the agent offers.", Command: "leapmux control agent get 87a19d5f --options"},
			},
			Flags: func(fs *pflag.FlagSet) {
				fs.Bool("options", false, "print every option group with its choices; --json always includes them")
			},
			Complete: completeTargets,
			Run:      runAgentGet,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:     "open",
			Mutates: true,
			Short:   "Start a new agent in a new tab",
			Long: `Start a new agent in a new tab.

--resume continues an earlier session of the provider in the new tab. "agent
sessions" lists the sessions that it takes, for the same --provider, --worker
and --dir.

--request-id makes a retry safe. A second run with the same request id and
the same arguments opens no second agent: it prints the agent that the first
run opened, and it queues the --message no second time. A run with the same
request id and other arguments fails with conflict.`,
			Examples: []cli.Example{
				{Comment: "Start an agent beside this tab and keep its id.", Command: `A=$(leapmux control agent open --provider claude --message "Run the tests" -q)`},
				{Comment: "Start an agent in a new git worktree.", Command: "leapmux control agent open --worktree fix-flaky-spec --base main"},
				{Comment: "Continue an earlier session that \"agent sessions\" lists.", Command: "leapmux control agent open --provider claude --resume 4f6c1e0a-9b7d-4c2e-8f3a-2d5b6c7e8f90"},
				{Comment: "Open an agent that a retry of the same command does not open twice.", Command: `leapmux control agent open --provider codex --message "Review the diff" --request-id nightly-review-2026-11-19`},
			},
			Flags: func(fs *pflag.FlagSet) {
				fs.String("provider", "", `agent provider; when neither this flag nor the variable gives one, the only provider that the worker offers; "leapmux control agent providers" lists them`)
				cli.SetEnvDefault(fs, "provider", "LEAPMUX_CONTROL_AGENT_PROVIDER")
				fs.String("model", "", "model")
				fs.String("effort", "", "reasoning effort")
				fs.String("permission-mode", "", "permission mode")
				fs.String("title", "", "tab title (default: the worker picks one)")
				fs.String("message", "", "a first message that the agent receives once it starts")
				fs.String("resume", "", `resume this earlier session of the provider; "agent sessions" lists them`)
				addRequestIDFlag(fs)
				addWorkingDirFlag(fs, "working directory of the agent")
				addGitSetupFlags(fs)
				addPlacementFlags(fs)
			},
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
The time must lie in the future. The result states it in UTC.

--request-id makes a retry safe. A second run with the same request id, the
same agent and the same message queues nothing new, and with --wait it
prints the reply to the message of the first run. A run with the same
request id and another message fails with conflict. A retry needs --at as an
RFC 3339 time, because a duration from now differs on each run.

With --wait, the command first refuses to send to an agent that waits for an
answer to a permission prompt, and exits 5. Otherwise it sends the message,
waits until the agent settles after it, and prints the agent's last reply as
the transcript shows it, or every reply with --all. It exits 6 when the agent
stops at a prompt after the message, or when --timeout passes. The output
then says whether the message is still queued.`, contracts.MaxAgentInputItemBytes/(1024*1024)),
			Examples: []cli.Example{
				{Command: `leapmux control agent send 87a19d5f "Run the tests"`},
				{Comment: "Send the contents of a file.", Command: "leapmux control agent send 87a19d5f --file prompt.md"},
				{Comment: "Send a message that starts with a dash.", Command: `leapmux control agent send 87a19d5f -- "-v is the verbose flag"`},
				{Comment: "Ask a question and print the answer.", Command: `leapmux control agent send 87a19d5f "Which tests fail?" --wait --timeout 30m`},
				{Comment: "Attach a screenshot and a log.", Command: `leapmux control agent send 87a19d5f "Why does the page look like this?" --attach screenshot.png --attach console.log`},
				{Comment: "Send the message in ten minutes.", Command: `leapmux control agent send 87a19d5f "Check the deploy" --at +10m`},
				{Comment: "Send a message that a retry of the same command does not send twice.", Command: `leapmux control agent send 87a19d5f "Run the release checklist" --request-id release-2026-11-19`},
			},
			Flags: func(fs *pflag.FlagSet) {
				fs.String("file", "", `read the message from this file ("-" reads stdin)`)
				fs.StringArray("attach", nil, "attach this file; repeat it for more than one")
				fs.String("at", "", "hold the message until this time: an RFC 3339 time with its offset, or + and a duration from now, such as +10m")
				addRequestIDFlag(fs)
				fs.Bool("wait", false, "wait until the agent settles after the message, then print its last reply")
				fs.Bool("all", false, "with --wait, print every reply after the message, not only the last one")
				fs.Duration("timeout", 0, "with --wait, stop waiting after this long, such as 90s or 30m; 0 waits without a limit")
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
			Long: `Wait until each agent reaches a state, then print the state of each.

An agent reaches the state when the Worker reports it at any moment after the
wait began, so an agent that is in the state already ends the wait at once.
"settled" is "idle" or "waiting_for_user": the agent works no more, because it
finished or because it waits for an answer to a permission prompt. An agent
whose input queue holds a message that it will read is neither idle nor
settled yet, as an agent that "agent open --message" just started.

The command exits 0 when the condition holds. It exits 6 when the wait ended
without it: --timeout passed, or an agent closed before it reached the state.
It prints the state of each agent in both cases.`,
			Examples: []cli.Example{
				{Command: "leapmux control agent wait 87a19d5f"},
				{Comment: "Wait until the first of two agents waits for an answer, for 10 minutes at most.", Command: "leapmux control agent wait 87a19d5f 5c2e0b91 --until waiting_for_user --any --timeout 10m"},
			},
			Flags: func(fs *pflag.FlagSet) {
				fs.String("until", string(waitUntilSettled), "the state to wait for")
				cli.SetEnum(fs, "until", waitUntilTokens()...)
				fs.Bool("any", false, "end the wait when one agent reaches the state, not every agent")
				fs.Duration("timeout", 0, "stop waiting after this long, such as 90s or 10m; 0 waits without a limit")
			},
			Complete: completeTargets,
			Run:      runAgentWait,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:   "requests [agent]...",
			Short: "List the permission prompts, plans and questions that agents wait on (default: every agent)",
			Args:  map[string]string{"agent": resolve.KindAgent.Help() + " Without one, the command lists the requests of every agent."},
			Long: `List the pending control requests of agents: permission prompts, plans
to approve, questions, MCP elicitation forms and extension dialogs.

Each request prints with its handle. "agent approve", "agent deny" and
"agent answer" take the agent and the handle, or a unique prefix of the
handle. A request that the agent replaces gets a new handle, so an answer
never reaches a request that you did not read.

The text form prints each request in full, with the numbers that --option
takes. --json prints each prompt as data.`,
			Examples: []cli.Example{
				{Command: "leapmux control agent requests"},
				{Comment: "List the requests of one agent, as JSON.", Command: "leapmux control agent requests 87a19d5f --json"},
			},
			Flags: func(fs *pflag.FlagSet) {
				fs.String("workspace", "", `without an agent, list only the agents of this workspace (an id, a prefix, "title:<title>" or "self")`)
			},
			Complete: completeTargets,
			Run:      runAgentRequests,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:     "approve <agent> <request>",
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

An agent cannot answer its own requests.`,
			Examples: []cli.Example{
				{Command: "leapmux control agent approve 87a19d5f 3kQ9xZ1b"},
				{Comment: "Approve a plan, and implement it in a fresh context.", Command: "leapmux control agent approve 87a19d5f 3kQ9 --permission-mode acceptEdits --clear-context"},
			},
			Flags:    addPlanApprovalFlags,
			Complete: completeTargets,
			Run:      runAgentApprove,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:     "deny <agent> <request>",
			Short:   "Refuse a permission prompt, a plan, a question or a dialog",
			Mutates: true,
			Args:    map[string]string{"agent": resolve.KindAgent.HelpWithoutSelf(), "request": requestArgHelp},
			Long: `Refuse a pending request:

  - A permission prompt: its one-time refusal, with --message as the reason.
  - A plan: the refusal, with --message as what to change.
  - A question: the refusal to answer, with --message as the reason.
  - An MCP elicitation: the decline.
  - A dialog: the refusal of a confirm dialog, or the cancel of another.

An agent cannot answer its own requests.`,
			Examples: []cli.Example{
				{Command: `leapmux control agent deny 87a19d5f 3kQ9xZ1b --message "Run the unit tests only"`},
			},
			Flags: func(fs *pflag.FlagSet) {
				fs.String("message", "", "the reason of the refusal, which the agent reads")
			},
			Complete: completeTargets,
			Run:      runAgentDeny,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:     "answer <agent> <request> [text]...",
			Short:   "Answer a question, fill a form, or choose any offered option",
			Mutates: true,
			Args: map[string]string{
				"agent":   resolve.KindAgent.HelpWithoutSelf(),
				"request": requestArgHelp,
				"text":    `A typed answer, or "-" to read it from standard input. With several questions, write "<question>:<text>".`,
			},
			Long: `Answer a pending request with any answer that it offers. "agent requests"
numbers the options of each request.

  - A question: --option chooses an option, and a text argument is a
    typed answer. A multiple-choice question takes both: the answer then
    holds the chosen options and the text. With several questions, write
    each value as "<question>:<value>", such as --option 2:1 or
    "1:Use the cache". A question that no --option answers keeps the
    options that the agent selected for it, which "agent requests" shows
    as its default. With no value at all, the command sends those
    defaults, which must answer every question that needs an answer.
  - A permission prompt: --option chooses any option, such as an approval
    for the session. --message gives the reason of a refusing option.
  - A plan: --option chooses one of its choices. --message gives what to
    change, and --permission-mode and --clear-context apply to an approval.
  - An MCP elicitation: --field fills the form, once for each field, or
    once for each value of a list field. A field that "agent requests"
    lists with a condition takes a value only when the condition holds,
    and a field that it does not list keeps its default. --option chooses
    an accept choice.
  - An input or editor dialog: the text.

An agent cannot answer its own requests.`,
			Examples: []cli.Example{
				{Comment: "Choose the second option of a question.", Command: "leapmux control agent answer 87a19d5f 3kQ9 --option 2"},
				{Comment: "Choose two options of a multiple-choice question, and type one more answer.", Command: `leapmux control agent answer 87a19d5f 3kQ9 --option 1 --option 3 "Metrics"`},
				{Comment: "Answer two questions.", Command: `leapmux control agent answer 87a19d5f 3kQ9 --option 1:2 "2:Use the staging database"`},
				{Comment: "Approve a command for the rest of the session.", Command: "leapmux control agent answer 87a19d5f 3kQ9 --option 2"},
				{Comment: "Fill an MCP form.", Command: "leapmux control agent answer 87a19d5f 3kQ9 --field repository=leapmux --field private=true"},
			},
			Flags: func(fs *pflag.FlagSet) {
				fs.StringArray("option", nil, `an option by its number, or "<question>:<number>" with several questions; repeat it for more than one`)
				fs.StringArray("field", nil, "a form field as <name>=<value>; repeat it for more than one")
				fs.String("message", "", "the reason of a refusing option or choice, which the agent reads")
				addPlanApprovalFlags(fs)
			},
			Complete: completeTargets,
			Run:      runAgentAnswer,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:     "interrupt <agent>",
			Short:   "Stop an agent's current turn",
			Mutates: true,
			Examples: []cli.Example{
				{Command: "leapmux control agent interrupt 87a19d5f"},
			},
			Flags: func(fs *pflag.FlagSet) {
				fs.String("reason", "", "a reason that the agent's transcript records")
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
				{Command: "leapmux control agent set 87a19d5f --model opus --effort high"},
				{Comment: "Set an option that only one provider has.", Command: "leapmux control agent set 87a19d5f --option sandbox=workspace-write"},
			},
			Flags: func(fs *pflag.FlagSet) {
				fs.String("model", "", "model id")
				fs.String("effort", "", "effort id")
				fs.String("permission-mode", "", "permission mode")
				fs.StringArray("option", nil, `provider option as key=value; repeat it for more than one; "agent get --options" lists them`)
			},
			Complete: completeTargets,
			Run:      runAgentSet,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:   "messages [agent]",
			Short: "Print an agent's messages, or follow new ones (default: this agent)",
			Long: `Print an agent's messages, or follow new ones.

A message that another agent sent prints its sender after its source, and
--json states the sender as sender.`,
			Examples: []cli.Example{
				{Command: "leapmux control agent messages 87a19d5f"},
				{Comment: "Print new messages as they arrive, one JSON envelope per line.", Command: "leapmux control agent messages 87a19d5f --follow --json"},
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
			},
			Complete: completeTargets,
			Run:      runAgentMessages,
		}),
		newAgentQueueCommand(),
		cli.NewLeaf(cli.Leaf{
			Use:   "providers",
			Short: "List the agent providers that a worker offers",
			Examples: []cli.Example{
				{Command: "leapmux control agent providers"},
				{Command: "leapmux control agent providers --worker name:build-box"},
			},
			Flags: addWorkerFlag,
			Run:   runAgentProviders,
		}),
		newAgentSessionsCommand(),
	)
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
		path := stringFlag(inv, "file")
		var data []byte
		var err error
		if path == "-" {
			data, err = io.ReadAll(inv.Stdin)
		} else {
			data, err = os.ReadFile(path)
		}
		if err != nil {
			return "", cli.Wrap(cli.CodeUsage, err, "read --file")
		}
		return nonEmptyText(string(data))
	}
	if !hasArg {
		return "", cli.Usagef("missing <%s>", argName).
			WithHint(`give it as an argument, as "-" to read stdin, or with --file`)
	}
	text := inv.Arg(argName)
	if text == "-" {
		data, err := io.ReadAll(inv.Stdin)
		if err != nil {
			return "", cli.Wrap(cli.CodeInternal, err, "read stdin")
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
	// ScheduledAt is the UTC time before which the agent does not read the
	// input, for a message that --at holds.
	ScheduledAt string `json:"scheduled_at,omitempty"`
}

// runAgentSend forwards a user message to the agent. The Worker queues it,
// and the agent reads it when its current turn ends. With --wait it waits
// for the agent's reply; see sendAndWait.
func runAgentSend(inv *cli.Invocation) (any, error) {
	wait := boolFlag(inv, "wait")
	if !wait {
		for _, name := range []string{"all", "timeout"} {
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
	bounded := s.bounded()
	defer bounded.Close()
	got, err := bounded.resolveTab(resolve.KindAgent, inv.Arg("agent"), resolve.Need{WorkerID: true})
	if err != nil {
		return nil, err
	}
	if err := maybePreflightWorker(bounded.ctx, s.c, got.WorkerID); err != nil {
		return nil, err
	}
	inputID := requestScopedID(spec.requestID, requestScopeAgentSend, got.ID)
	if !wait {
		if _, err := enqueueUserMessage(s.ctx, s.c, got, inputID, spec.message); err != nil {
			return nil, withRequestIDHint(err, spec.requestID)
		}
		return agentResult{ID: got.ID, InputID: inputID, ScheduledAt: spec.message.scheduledAt}, nil
	}
	result, outcome, err := sendAndWait(s, got, spec.message, inputID, boolFlag(inv, "all"), timeout)
	if err != nil {
		return nil, withRequestIDHint(err, spec.requestID)
	}
	if outcome == replyWaitSettled {
		return result, nil
	}
	return nil, agentReplyEnded(inv.Printer, result, outcome, timeout)
}

func runAgentInterrupt(inv *cli.Invocation) (any, error) {
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	got, err := s.resolveTab(resolve.KindAgent, inv.Arg("agent"), resolve.Need{WorkerID: true})
	if err != nil {
		return nil, err
	}
	if err := callInnerRPC(s.ctx, s.c, got.WorkerID, leapmuxv1connect.WorkerAgentServiceInterruptAgentProcedure, &leapmuxv1.InterruptAgentRequest{AgentId: got.ID, Reason: stringFlag(inv, "reason")}, nil); err != nil {
		return nil, err
	}
	return agentResult{ID: got.ID}, nil
}

// runAgentGet prints the Worker's record of one agent. It reuses
// ListAgents with a single tab id rather than a GetAgent RPC, since the
// Worker already filters by id.
func runAgentGet(inv *cli.Invocation) (any, error) {
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	got, err := s.resolveTab(resolve.KindAgent, inv.Arg("agent"), resolve.Need{WorkerID: true})
	if err != nil {
		return nil, err
	}
	info, err := s.agentInfo(got)
	if err != nil {
		return nil, err
	}
	withOptions := boolFlag(inv, "options") || inv.Printer.Format == cli.FormatJSON
	return agentRowOf(info, withOptions), nil
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
	Agents []agentRow `json:"agents"`
}

// runAgentList lists the agent tabs, each joined with the Worker's record:
// its title, its provider, its state and its model.
func runAgentList(inv *cli.Invocation) (any, error) {
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	req := &leapmuxv1.ListTabsRequest{}
	if inv.Changed("workspace") {
		ws, err := s.resolveOne(resolve.KindWorkspace, stringFlag(inv, "workspace"))
		if err != nil {
			return nil, err
		}
		req.WorkspaceIds = []string{ws.ID}
	}
	var resp leapmuxv1.ListTabsResponse
	if err := hubCallUnary(s.ctx, s.c, leapmuxv1connect.WorkspaceServiceListTabsProcedure, req, &resp); err != nil {
		return nil, err
	}
	tabs := filterTabsByType(resp.GetTabs(), leapmuxv1.TabType_TAB_TYPE_AGENT)
	details := fetchTabDetails(s.ctx, s.c, tabs)
	rows := make([]agentRow, 0, len(tabs))
	for _, t := range tabs {
		if d := details[t.GetTab().GetTabId()]; d.Agent != nil {
			rows = append(rows, agentRowOf(d.Agent, false))
			continue
		}
		// The Worker did not answer: the hub's row is all there is.
		rows = append(rows, agentRow{ID: t.GetTab().GetTabId(), WorkerID: t.GetWorkerId()})
	}
	return agentList{Agents: rows}, nil
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

// resolveProvider turns the user-supplied --provider value (possibly
// pre-filled from $LEAPMUX_CONTROL_AGENT_PROVIDER) into an
// AgentProvider enum, querying the worker's installed providers when
// the caller passed no value at all.
//
// Three rejection paths, each with a code a script can branch on:
//
//   - raw != "" and not a known alias → usage (lists every alias the
//     parser accepts; the typo is local, no need for an RPC).
//   - raw == "" and the worker reports zero installed providers →
//     conflict, with a fix-it hint.
//   - raw == "" and the worker reports more than one → usage, with the
//     display names of what's installed.
//
// The exactly-one case is the silent success path: a worker with a
// single provider needs no `--provider` flag at all, matching the
// frontend's first-launch UX where the picker auto-selects when only
// one option is available.
func resolveProvider(ctx context.Context, raw string, list providerListerFn) (leapmuxv1.AgentProvider, error) {
	if raw != "" {
		if p, ok := agentlabels.ParseProvider(raw); ok {
			return p, nil
		}
		return 0, cli.Usagef("unknown --provider %q", raw).
			WithHint("accepted values: %s", strings.Join(allProviderAliases(), ", "))
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
