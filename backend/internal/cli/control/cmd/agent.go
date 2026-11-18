package cmd

import (
	"context"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
	"github.com/leapmux/leapmux/internal/util/agentlabels"
	"github.com/leapmux/leapmux/internal/util/id"
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
			Examples: []cli.Example{
				{Comment: "Start an agent beside this tab and keep its id.", Command: `A=$(leapmux control agent open --provider claude --message "Run the tests" -q)`},
				{Comment: "Start an agent in a new git worktree.", Command: "leapmux control agent open --worktree fix-flaky-spec --base main"},
			},
			Flags: func(fs *pflag.FlagSet) {
				fs.String("provider", "", `agent provider; when neither this flag nor the variable gives one, the only provider that the worker offers; "leapmux control agent providers" lists them`)
				cli.SetEnvDefault(fs, "provider", "LEAPMUX_CONTROL_AGENT_PROVIDER")
				fs.String("model", "", "model")
				fs.String("effort", "", "reasoning effort")
				fs.String("permission-mode", "", "permission mode")
				fs.String("title", "", "tab title (default: the worker picks one)")
				fs.String("message", "", "a first message that the agent receives once it starts")
				fs.String("resume", "", "resume this earlier session of the provider")
				addWorkingDirFlag(fs, "working directory of the agent")
				addGitSetupFlags(fs)
				addPlacementFlags(fs)
			},
			Run: runAgentOpen,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:     "send <agent> [text]",
			Short:   "Send a message to an agent",
			Mutates: true,
			Long: `Send a message to an agent.

The text is the second argument, "-" to read it from stdin, or --file. A
message that starts with "-" needs "--" before it, so that a misspelled flag
never reaches the agent as text.`,
			Examples: []cli.Example{
				{Command: `leapmux control agent send 87a19d5f "Run the tests"`},
				{Comment: "Send the contents of a file.", Command: "leapmux control agent send 87a19d5f --file prompt.md"},
				{Comment: "Send a message that starts with a dash.", Command: `leapmux control agent send 87a19d5f -- "-v is the verbose flag"`},
			},
			Flags: func(fs *pflag.FlagSet) {
				fs.String("file", "", `read the message from this file ("-" reads stdin)`)
			},
			Complete: completeTargets,
			Run:      runAgentSend,
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

// agentResult is the result of a command that acted on an agent and has
// nothing else to report.
type agentResult struct {
	ID string `json:"id"`
	// InputID is the id of the message that the agent queued.
	InputID string `json:"input_id,omitempty"`
}

// runAgentSend forwards a user message to the agent. The Worker queues it,
// and the agent reads it when its current turn ends.
func runAgentSend(inv *cli.Invocation) (any, error) {
	text, err := inputText(inv, "text")
	if err != nil {
		return nil, err
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
	inputID := id.Generate()
	if err := callInnerRPC(s.ctx, s.c, got.WorkerID, leapmuxv1connect.WorkerAgentInputServiceEnqueueAgentInputProcedure, &leapmuxv1.EnqueueAgentInputRequest{
		AgentId: got.ID, InputId: inputID, Text: text,
		Kind: leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_USER_MESSAGE,
	}, nil); err != nil {
		return nil, err
	}
	return agentResult{ID: got.ID, InputID: inputID}, nil
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
