package cmd

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
	"github.com/leapmux/leapmux/internal/util/agentlabels"
	"github.com/leapmux/leapmux/internal/util/timefmt"
)

// `agent queue` reads and changes an agent's input queue through
// WorkerAgentInputService, with the rules that the browser's queue follows:
//
//   - An input that the agent already reads is locked: it neither moves nor
//     leaves the queue.
//   - An input moves only inside its reorder segment, which the Worker
//     states for each input of a snapshot. The CLI compares the segments and
//     never calculates them, because the Worker's clock decides them.
//   - A removal or a move does not ask for the edit lease of the input, as
//     the browser's own buttons do not.
//
// Every response of these RPCs carries the whole queue, so each command
// reports from the queue that the Worker answers with.

func newAgentQueueCommand() *cobra.Command {
	return cli.SetAliases(cli.Group("queue", "Inspect, edit, steer, preempt, retry, reorder, pause, and clear an agent's input queue",
		cli.NewLeaf(cli.Leaf{
			Use:     "list [agent]",
			Aliases: []string{"ls"},
			Short:   "List the inputs that wait in an agent's queue (default: this agent)",
			Long: `List the inputs that wait in an agent's input queue, first to last: the
messages that "agent send" sent, the clears and the compactions.

A message that a process in another agent's tab sent is an agent_message.
FROM states the agent that sent it, and --json states it as sender.

Each input has a state:
  - queued: the input waits for its turn.
  - dispatching: the agent reads the input now.
  - awaiting_echo: the agent took the input into its running turn, and
    LeapMux waits for the agent to confirm it.
  - failed: the input did not reach the agent. The error states why.
  - delivery_uncertain: the input may not have reached the agent.

The queue holds while it is paused, while its first input failed or may not
have reached the agent, and while LeapMux has its first input open in an
editor. The result states a pause and its reason, and the command states
each other cause on stderr.

-q prints the id of each input.`,
			Examples: []cli.Example{
				{Command: "leapmux agent queue list 87a19d5f"},
				{Comment: "Print the queue as data, with the reorder segment of each input.", Command: "leapmux agent queue list 87a19d5f --json"},
			},
			Flags:    addTargetWorkspaceFlag,
			Complete: completeTargets,
			Run:      runAgentQueueList,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:         "remove <agent> <input>...",
			Aliases:     []string{"rm"},
			Short:       "Remove one or more inputs from an agent's queue before the agent reads them",
			Mutates:     true,
			Destructive: true,
			Args:        map[string]string{"input": queuedInputArgHelp},
			Long: `Remove one or more inputs from an agent's input queue. The agent never reads them.

An input that the agent already reads stays: the command fails with
conflict. A removal also ends an edit of the input that LeapMux has open.

A retry of the "agent send --request-id" run that queued the input queues
nothing for 7 days after the removal.`,
			Examples: []cli.Example{
				{Command: "leapmux agent queue remove 87a19d5f 3kQ9xZ1b"},
				{Comment: "Remove two queued inputs in one call.", Command: "leapmux agent queue remove 87a19d5f 3kQ9xZ1b 9Lm2Pq7c"},
			},
			Flags:    addTargetWorkspaceFlag,
			Complete: completeQueueArgs,
			Run:      runAgentQueueRemove,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:     "move <agent> <input>",
			Aliases: []string{"mv"},
			Short:   "Move an input to another place in an agent's queue",
			Mutates: true,
			Args:    map[string]string{"input": queuedInputArgHelp},
			Long: `Move an input to another place in an agent's input queue. --before, --first
or --last gives the place.

An input moves only among the inputs of its reorder segment, as in LeapMux:
  - The inputs that the agent reads as soon as it can form one segment, and
    the inputs that --at holds until one time form another. An input never
    moves past an input of another time.
  - An input that the agent already reads is locked. It stays in its place,
    and the inputs on its two sides form two segments.
--first and --last mean the first and the last place of the segment. "agent
queue list --json" states the segment of each input as reorder_segment.`,
			Examples: []cli.Example{
				{Comment: "Move an input before another one.", Command: "leapmux agent queue move 87a19d5f 3kQ9xZ1b --before 9Lm2Pq7c"},
				{Comment: "Make an input the next one that the agent reads.", Command: "leapmux agent queue move 87a19d5f 3kQ9xZ1b --first"},
			},
			Flags: func(fs *pflag.FlagSet) {
				fs.String("before", "", "move the input before this input, as an id or a unique prefix")
				fs.String("after", "", "move the input after this input, as an id or a unique prefix")
				fs.Bool("first", false, "move the input to the first place of its segment")
				fs.Bool("last", false, "move the input to the last place of its segment")
				addTargetWorkspaceFlag(fs)
			},
			FlagComplete: map[string]cobra.CompletionFunc{
				"before": completeQueueInputPositionFlag,
				"after":  completeQueueInputPositionFlag,
			},
			Complete: completeQueueArgs,
			Run:      runAgentQueueMove,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:              "get <agent> <input>",
			Short:            "Read the full text and metadata of one queued input",
			QuietUnsupported: true,
			Args:             map[string]string{"input": queuedInputArgHelp},
			Examples: []cli.Example{
				{Command: "leapmux agent queue get 87a19d5f 3kQ9xZ1b"},
			},
			Flags:    addTargetWorkspaceFlag,
			Complete: completeQueueArgs,
			Run:      runAgentQueueGet,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:     "edit <agent> <input> [text]",
			Short:   "Replace the text or schedule of a queued input",
			Mutates: true,
			Args:    map[string]string{"input": queuedInputArgHelp, "text": `Replacement message text, or "-" for stdin.`},
			Examples: []cli.Example{
				{Command: `leapmux agent queue edit 87a19d5f 3kQ9xZ1b "Run only unit tests"`},
				{Command: "leapmux agent queue edit 87a19d5f 3kQ9xZ1b --at +15m"},
			},
			Flags: func(fs *pflag.FlagSet) {
				fs.String("file", "", `read replacement text from this file ("-" reads stdin)`)
				fs.String("at", "", "set a new schedule (RFC 3339 with offset, or +duration)")
				fs.Bool("clear-schedule", false, "remove the input's scheduled send-not-before time")
				fs.Bool("takeover", false, "take over an active edit lease held by another client")
				addTargetWorkspaceFlag(fs)
			},
			Complete: completeQueueArgs,
			Run:      runAgentQueueEdit,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:     "steer <agent> [input]",
			Short:   "Inject a queued input into the agent's active turn immediately",
			Mutates: true,
			Args:    map[string]string{"input": queuedInputArgHelp + " When omitted, steers the head steerable input."},
			Examples: []cli.Example{
				{Command: "leapmux agent queue steer 87a19d5f"},
				{Command: "leapmux agent queue steer 87a19d5f 3kQ9xZ1b"},
			},
			Flags:    addTargetWorkspaceFlag,
			Complete: completeQueueArgs,
			Run:      runAgentQueueSteer,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:     "preempt <agent> [input]",
			Short:   "Cancel the active turn so a queued input dispatches next",
			Mutates: true,
			Args:    map[string]string{"input": queuedInputArgHelp + " When omitted, preempts with the head preemptable input."},
			Examples: []cli.Example{
				{Command: "leapmux agent queue preempt 87a19d5f"},
				{Command: "leapmux agent queue preempt 87a19d5f 3kQ9xZ1b"},
			},
			Flags:    addTargetWorkspaceFlag,
			Complete: completeQueueArgs,
			Run:      runAgentQueuePreempt,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:     "advance <agent>",
			Short:   "Run one queue advance step (steer, preempt, or resume as appropriate)",
			Mutates: true,
			Examples: []cli.Example{
				{Command: "leapmux agent queue advance 87a19d5f"},
			},
			Flags:    addTargetWorkspaceFlag,
			Complete: completeTargets,
			Run:      runAgentQueueAdvance,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:     "retry <agent> [input]",
			Short:   "Retry a failed or delivery-uncertain queued input",
			Mutates: true,
			Args:    map[string]string{"input": queuedInputArgHelp + " When omitted, retries the failed or uncertain head input."},
			Examples: []cli.Example{
				{Command: "leapmux agent queue retry 87a19d5f"},
				{Command: "leapmux agent queue retry 87a19d5f 3kQ9xZ1b --confirm-uncertain"},
			},
			Flags: func(fs *pflag.FlagSet) {
				fs.Bool("confirm-uncertain", false, "confirm retrying an input whose earlier delivery state was uncertain")
				addTargetWorkspaceFlag(fs)
			},
			Complete: completeQueueArgs,
			Run:      runAgentQueueRetry,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:         "clear <agent>",
			Short:       "Remove all unlocked inputs waiting in an agent's queue",
			Mutates:     true,
			Destructive: true,
			Examples: []cli.Example{
				{Command: "leapmux agent queue clear 87a19d5f"},
			},
			Flags:    addTargetWorkspaceFlag,
			Complete: completeTargets,
			Run:      runAgentQueueClear,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:     "pause <agent>",
			Short:   "Pause an agent's queue, so that the agent reads no queued input",
			Mutates: true,
			Long: `Pause an agent's input queue. The agent finishes its current turn, and then
reads no queued input until the queue resumes. New inputs still join the
queue.

LeapMux pauses a queue by itself too: when you interrupt the agent, when the
agent stops, and when an input does not reach the agent. "agent queue list"
states why a queue is paused.`,
			Examples: []cli.Example{
				{Command: "leapmux agent queue pause 87a19d5f"},
			},
			Flags:    addTargetWorkspaceFlag,
			Complete: completeTargets,
			Run:      func(inv *cli.Invocation) (any, error) { return runAgentQueueSetPaused(inv, true) },
		}),
		cli.NewLeaf(cli.Leaf{
			Use:     "resume <agent>",
			Short:   "Resume an agent's queue, whatever paused it",
			Mutates: true,
			Long: `Resume an agent's input queue, whatever paused it, so that the agent reads
the next input when it can.

A failed input keeps its failure. While the first input failed, or while
LeapMux has it open in an editor, the queue still holds, and the command
says so on stderr. Remove the failed input with "agent queue remove", or
retry it with "agent queue retry".`,
			Examples: []cli.Example{
				{Command: "leapmux agent queue resume 87a19d5f"},
			},
			Flags:    addTargetWorkspaceFlag,
			Complete: completeTargets,
			Run:      func(inv *cli.Invocation) (any, error) { return runAgentQueueSetPaused(inv, false) },
		}),
	), "q")
}

// queuedInputArgHelp describes the <input> argument.
const queuedInputArgHelp = `A queued input's id, as "agent queue list" prints it, or a unique prefix of it.`

// queueAttachmentRow is one attachment of a queued input.
type queueAttachmentRow struct {
	Filename string `json:"filename"`
	// MimeType is the media type that the Worker stores for the file.
	MimeType string `json:"mime_type,omitempty"`
	Size     int64  `json:"size"`
}

// queueItemRow is one input in an agent's queue.
type queueItemRow struct {
	ID string `json:"id" table:"ID,short"`
	// Kind is a token of AgentInputKind, such as "user_message" or
	// "clear_context". "agent_message" is a message that another agent sent.
	Kind string `json:"kind" table:"KIND"`
	// Sender is the agent tab that sent an agent message, and absent for
	// every other input.
	Sender *messageSender `json:"sender,omitempty" table:"FROM"`
	// State is a token of AgentInputState, such as "queued" or "failed".
	State string `json:"state" table:"STATE,state"`
	// ScheduledAt is the UTC time before which the agent does not read the
	// input, or empty for an input that the agent reads as soon as it can.
	ScheduledAt string `json:"scheduled_at,omitempty" table:"SCHEDULED,dim"`
	// Text is the beginning of the input's text.
	Text        string               `json:"text" table:"TEXT"`
	Attachments []queueAttachmentRow `json:"attachments,omitempty"`
	// Error states why a failed input did not reach the agent.
	Error string `json:"error,omitempty"`
	// Edited reports that LeapMux has the input open in an editor.
	Edited bool `json:"edited,omitempty"`
	// ReorderSegment groups the inputs that can trade places: two inputs
	// with the same value that is not 0. A locked input has 0. The values
	// mean nothing outside one listing.
	ReorderSegment uint32 `json:"reorder_segment"`
	Order          int64  `json:"order,omitempty"`
	Version        uint64 `json:"version,omitempty"`
	CanSteer       bool   `json:"can_steer,omitempty"`
	CanPreempt     bool   `json:"can_preempt,omitempty"`
	EditRefusal    string `json:"edit_refusal,omitempty"`
	CreatedAt      string `json:"created_at,omitempty"`
	UpdatedAt      string `json:"updated_at,omitempty"`
}

func queueItemRowOf(item *leapmuxv1.QueuedAgentInput) queueItemRow {
	row := queueItemRow{
		ID:             item.GetId(),
		Kind:           enumToken(item.GetKind()),
		Sender:         messageSenderOf(item.GetSender()),
		State:          enumToken(item.GetState()),
		ScheduledAt:    item.GetScheduledAt(),
		Text:           item.GetText(),
		Error:          item.GetError(),
		Edited:         item.GetEditOwnerClientId() != "",
		ReorderSegment: item.GetReorderSegment(),
		Order:          item.GetOrder(),
		Version:        item.GetVersion(),
		CanSteer:       item.GetCanSteer(),
		CanPreempt:     item.GetCanPreempt(),
		EditRefusal:    item.GetEditRefusal(),
		CreatedAt:      item.GetCreatedAt(),
		UpdatedAt:      item.GetUpdatedAt(),
	}
	for _, a := range item.GetAttachments() {
		row.Attachments = append(row.Attachments, queueAttachmentRow{Filename: a.GetFilename(), MimeType: a.GetMimeType(), Size: a.GetSize()})
	}
	return row
}

type queueRecoveryInfo struct {
	OutputGeneration int64  `json:"output_generation"`
	SessionID        string `json:"agent_session_id,omitempty"`
	Provider         string `json:"provider,omitempty"`
	ProcessInstance  string `json:"process_instance_id,omitempty"`
}

// agentQueueView is the result of `agent queue list`.
type agentQueueView struct {
	AgentID string `json:"agent_id"`
	// Revision monotonically tracks snapshot updates.
	Revision uint64 `json:"revision,omitempty"`
	// Paused reports that the agent reads no queued input until the queue
	// resumes.
	Paused bool `json:"paused"`
	// PauseReason is why the queue is paused: a token of
	// AgentInputQueuePauseReason, such as "manual" or "interrupted".
	PauseReason string `json:"pause_reason,omitempty"`
	// ActiveTurn reports that a turn of the agent runs now. The first
	// queued input waits for its end.
	ActiveTurn          bool               `json:"active_turn"`
	ActiveTurnSteerable bool               `json:"active_turn_steerable,omitempty"`
	ProcessRecovery     *queueRecoveryInfo `json:"process_recovery,omitempty"`
	Items               []queueItemRow     `json:"items"`
}

// QuietLines prints the queued input IDs, one per line.
func (v agentQueueView) QuietLines() []string {
	out := make([]string, 0, len(v.Items))
	for _, item := range v.Items {
		out = append(out, item.ID)
	}
	return out
}

// agentQueueState is the result of `agent queue pause` and `agent queue
// resume`.
type agentQueueState struct {
	ID          string `json:"id"`
	Paused      bool   `json:"paused"`
	PauseReason string `json:"pause_reason,omitempty"`
}

// queueMoveResult is the result of `agent queue move`.
type queueMoveResult struct {
	ID      string `json:"id"`
	InputID string `json:"input_id"`
	// Position is the place of the input in the queue after the move,
	// counting from 1.
	Position int `json:"position"`
}

func (r queueMoveResult) QuietLines() []string {
	if r.InputID == "" {
		return nil
	}
	return []string{r.InputID}
}

// agentQueue is the input queue of one agent, which a queue command reads or
// changes over one channel to the agent's Worker.
type agentQueue struct {
	ctx   context.Context
	agent tabTarget
	w     workerCall
}

// runAgentQueue resolves the agent that the arguments give and runs body with
// its queue. Every call of the command shares one channel to the Worker, so
// a command that reads the queue before it changes it opens one channel.
func runAgentQueue(inv *cli.Invocation, body func(q agentQueue) (any, error)) (any, error) {
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	got, err := s.resolveTab(resolve.KindAgent, inv.Arg("agent"), resolve.Need{WorkerID: true})
	if err != nil {
		return nil, err
	}
	var result any
	err = withWorkerChannel(s.ctx, s.c, got.WorkerID, func(w workerCall) error {
		var err error
		result, err = body(agentQueue{ctx: s.ctx, agent: got, w: w})
		return err
	})
	return result, err
}

// call sends one RPC for the queue.
func (q agentQueue) call(method string, in, out proto.Message) error {
	return q.w.Call(q.ctx, method, in, out)
}

// snapshot reads the queue.
func (q agentQueue) snapshot() (*leapmuxv1.AgentInputQueueSnapshot, error) {
	var resp leapmuxv1.ListAgentInputQueueResponse
	if err := q.call(leapmuxv1connect.WorkerAgentInputServiceListAgentInputQueueProcedure, &leapmuxv1.ListAgentInputQueueRequest{AgentId: q.agent.ID}, &resp); err != nil {
		return nil, err
	}
	return resp.GetSnapshot(), nil
}

// findInput returns the input of the queue whose id is handle, or the one
// input whose id starts with it.
func (q agentQueue) findInput(items []*leapmuxv1.QueuedAgentInput, handle string) (*leapmuxv1.QueuedAgentInput, error) {
	handle, err := normalizeIDHandle(handle, "input id")
	if err != nil {
		return nil, err
	}
	item, matches, found := lookupByIDPrefix(items, handle, (*leapmuxv1.QueuedAgentInput).GetId)
	if found {
		return item, nil
	}
	if len(matches) == 0 {
		return nil, cli.Newf(cli.CodeNotFound, "the queue of agent %s holds no input %s", q.agent.ID, handle).
			WithHint("list the queue with \"leapmux agent queue list %s\"", q.agent.ID)
	}
	e := cli.Newf(cli.CodeAmbiguousTarget, "%q matches %d inputs in the queue of agent %s", handle, len(matches), q.agent.ID).
		WithHint("give more characters of the id")
	for _, item := range matches {
		e.Candidates = append(e.Candidates, cli.Candidate{ID: item.GetId(), Kind: enumToken(item.GetKind()), Title: item.GetText()})
	}
	return nil, e
}

// requireUnlocked refuses an input that the agent already reads: it neither
// moves nor leaves the queue until the agent confirms it.
func (q agentQueue) requireUnlocked(item *leapmuxv1.QueuedAgentInput) error {
	if !inputLocked(item) {
		return nil
	}
	return cli.Newf(cli.CodeConflict, "the agent already reads input %s (%s), so it stays in its place", item.GetId(), enumToken(item.GetState()))
}

// inputLocked reports whether the agent already reads item: the Worker locks
// an input while it dispatches it and while it waits for the agent to
// confirm a steered input. The reorder segment 0 states the same lock.
func inputLocked(item *leapmuxv1.QueuedAgentInput) bool {
	switch item.GetState() {
	case leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DISPATCHING, leapmuxv1.AgentInputState_AGENT_INPUT_STATE_AWAITING_ECHO:
		return true
	default:
		return false
	}
}

// queueHold says why the queue holds its first input although no pause holds
// it, as the Worker's dispatch rule decides: the first input failed or may
// not have reached the agent, or LeapMux has it open in an editor. It
// returns "" when no such cause holds the queue. The first input is the
// first one that does not wait for the agent to confirm it.
func queueHold(agentID string, queue *leapmuxv1.AgentInputQueueSnapshot) string {
	items := queue.GetItems()
	i := slices.IndexFunc(items, func(item *leapmuxv1.QueuedAgentInput) bool {
		return item.GetState() != leapmuxv1.AgentInputState_AGENT_INPUT_STATE_AWAITING_ECHO
	})
	if i < 0 {
		return ""
	}
	head := items[i]
	remedy := fmt.Sprintf(`remove it with "leapmux agent queue remove %s %s", or retry it with "leapmux agent queue retry %s %s"`, agentID, head.GetId(), agentID, head.GetId())
	switch {
	case head.GetState() == leapmuxv1.AgentInputState_AGENT_INPUT_STATE_FAILED:
		return fmt.Sprintf("the queue of agent %s holds: its first input %s failed (%s); %s", agentID, head.GetId(), head.GetError(), remedy)
	case head.GetState() == leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DELIVERY_UNCERTAIN:
		return fmt.Sprintf("the queue of agent %s holds: its first input %s may not have reached the agent; %s", agentID, head.GetId(), remedy)
	case head.GetEditOwnerClientId() != "":
		return fmt.Sprintf("the queue of agent %s holds: LeapMux has its first input %s open in an editor", agentID, head.GetId())
	default:
		return ""
	}
}

// pauseReasonOf is the token of the queue's pause reason, or "" while the
// queue runs.
func pauseReasonOf(queue *leapmuxv1.AgentInputQueueSnapshot) string {
	return enumToken(queue.GetPauseReason())
}

// runAgentQueueList prints the queue of one agent.
func runAgentQueueList(inv *cli.Invocation) (any, error) {
	return runAgentQueue(inv, func(q agentQueue) (any, error) {
		queue, err := q.snapshot()
		if err != nil {
			return nil, err
		}
		rows := make([]queueItemRow, 0, len(queue.GetItems()))
		for _, item := range queue.GetItems() {
			rows = append(rows, queueItemRowOf(item))
		}
		if hold := queueHold(q.agent.ID, queue); hold != "" && inv.Printer.Format == cli.FormatText {
			_ = inv.Printer.Notice("%s", hold)
		}
		reason := pauseReasonOf(queue)
		var recInfo *queueRecoveryInfo
		if pr := queue.GetProcessRecovery(); pr != nil {
			recInfo = &queueRecoveryInfo{
				OutputGeneration: pr.GetOutputGeneration(),
				SessionID:        pr.GetAgentSessionId(),
				Provider:         agentlabels.CLIAlias(pr.GetAgentProvider()),
				ProcessInstance:  pr.GetProcessInstanceId(),
			}
		}
		return agentQueueView{
			AgentID:             q.agent.ID,
			Revision:            queue.GetRevision(),
			Paused:              reason != "",
			PauseReason:         reason,
			ActiveTurn:          queue.GetActiveTurn(),
			ActiveTurnSteerable: queue.GetActiveTurnSteerable(),
			ProcessRecovery:     recInfo,
			Items:               rows,
		}, nil
	})
}

type queueRemoveBatchResult struct {
	ID       string   `json:"id"`
	InputIDs []string `json:"input_ids"`
}

func (r queueRemoveBatchResult) QuietLines() []string { return r.InputIDs }

// deleteInputsBackToFront deletes targets in reverse queue order (tail to head).
// The Worker's Manager.Delete calls mutateAndDrain, which invokes scheduleDrain
// after every deletion. Deleting front-to-back would promote later queued inputs
// to the head and let the Worker dispatch them before the CLI removes them.
// Deleting strictly back-to-front guarantees every later target is removed before
// any earlier target is deleted.
func (q agentQueue) deleteInputsBackToFront(targets []*leapmuxv1.QueuedAgentInput) error {
	removed := 0
	for i := len(targets) - 1; i >= 0; i-- {
		item := targets[i]
		if err := q.call(leapmuxv1connect.WorkerAgentInputServiceDeleteQueuedAgentInputProcedure,
			&leapmuxv1.DeleteQueuedAgentInputRequest{AgentId: q.agent.ID, InputId: item.GetId()}, nil); err != nil {
			if removed == 0 {
				return err
			}
			e := cli.FromError(err)
			return cli.Wrap(e.Code, e, fmt.Sprintf("removed %d of %d inputs before failing on %s", removed, len(targets), item.GetId())).
				WithHint(`inspect remaining items with "leapmux agent queue list %s"`, q.agent.ID)
		}
		removed++
	}
	return nil
}

// runAgentQueueRemove removes one or more inputs from the queue.
func runAgentQueueRemove(inv *cli.Invocation) (any, error) {
	return runAgentQueue(inv, func(q agentQueue) (any, error) {
		queue, err := q.snapshot()
		if err != nil {
			return nil, err
		}
		items := queue.GetItems()
		queuePos := make(map[string]int, len(items))
		for i, item := range items {
			queuePos[item.GetId()] = i
		}
		handles := inv.Variadic("input")
		resolved := make([]*leapmuxv1.QueuedAgentInput, 0, len(handles))
		seen := map[string]bool{}
		for _, handle := range handles {
			item, err := q.findInput(items, handle)
			if err != nil {
				return nil, err
			}
			if err := q.requireUnlocked(item); err != nil {
				return nil, err
			}
			if seen[item.GetId()] {
				continue
			}
			seen[item.GetId()] = true
			resolved = append(resolved, item)
		}
		byQueueOrder := slices.Clone(resolved)
		slices.SortFunc(byQueueOrder, func(a, b *leapmuxv1.QueuedAgentInput) int {
			return cmp.Compare(queuePos[a.GetId()], queuePos[b.GetId()])
		})
		if err := q.deleteInputsBackToFront(byQueueOrder); err != nil {
			return nil, err
		}
		removed := make([]string, len(resolved))
		for i, item := range resolved {
			removed[i] = item.GetId()
		}
		if len(removed) == 1 {
			return agentResult{ID: q.agent.ID, InputID: removed[0]}, nil
		}
		return queueRemoveBatchResult{ID: q.agent.ID, InputIDs: removed}, nil
	})
}

// runAgentQueueMove moves one input inside its reorder segment.
func runAgentQueueMove(inv *cli.Invocation) (any, error) {
	var places []string
	for _, name := range []string{"before", "after"} {
		if inv.Changed(name) {
			places = append(places, "--"+name)
		}
	}
	for _, name := range []string{"first", "last"} {
		if boolFlag(inv, name) {
			places = append(places, "--"+name)
		}
	}
	switch {
	case len(places) == 0:
		return nil, cli.Usagef("give the new place with --before, --after, --first or --last")
	case len(places) > 1:
		return nil, cli.Usagef("%s exclude each other", strings.Join(places, " and "))
	}
	return runAgentQueue(inv, func(q agentQueue) (any, error) {
		queue, err := q.snapshot()
		if err != nil {
			return nil, err
		}
		items := queue.GetItems()
		item, err := q.findInput(items, inv.Arg("input"))
		if err != nil {
			return nil, err
		}
		if err := q.requireUnlocked(item); err != nil {
			return nil, err
		}
		beforeID, err := q.moveDestination(inv, items, item)
		if err != nil {
			return nil, err
		}
		var resp leapmuxv1.MoveQueuedAgentInputResponse
		if err := q.call(leapmuxv1connect.WorkerAgentInputServiceMoveQueuedAgentInputProcedure,
			&leapmuxv1.MoveQueuedAgentInputRequest{AgentId: q.agent.ID, InputId: item.GetId(), BeforeInputId: beforeID}, &resp); err != nil {
			return nil, err
		}
		position := slices.IndexFunc(resp.GetSnapshot().GetItems(), func(moved *leapmuxv1.QueuedAgentInput) bool { return moved.GetId() == item.GetId() }) + 1
		return queueMoveResult{ID: q.agent.ID, InputID: item.GetId(), Position: position}, nil
	})
}

const reorderSegmentHint = "an input moves only among the inputs of its segment: an input that the agent reads as soon as it can never trades places with one that --at holds, and no input moves past one that the agent already reads"

// resolveMoveTarget resolves the anchor input for --before or --after and
// verifies that it is unlocked and in the same reorder segment as item.
func (q agentQueue) resolveMoveTarget(items []*leapmuxv1.QueuedAgentInput, item *leapmuxv1.QueuedAgentInput, handle, relation string) (*leapmuxv1.QueuedAgentInput, error) {
	target, err := q.findInput(items, handle)
	if err != nil {
		return nil, err
	}
	if err := q.requireUnlocked(target); err != nil {
		return nil, err
	}
	if target.GetReorderSegment() != item.GetReorderSegment() {
		return nil, cli.Newf(cli.CodeConflict, "input %s cannot move %s input %s, which is in another reorder segment", item.GetId(), relation, target.GetId()).
			WithHint("%s", reorderSegmentHint)
	}
	return target, nil
}

// moveDestination returns the before_input_id of a move of item: the input
// that --before gives, the item immediately after --after (or "" at the end of
// the segment), the first input of the segment for --first, or "" for --last.
func (q agentQueue) moveDestination(inv *cli.Invocation, items []*leapmuxv1.QueuedAgentInput, item *leapmuxv1.QueuedAgentInput) (string, error) {
	switch {
	case boolFlag(inv, "last"):
		return "", nil
	case boolFlag(inv, "first"):
		i := slices.IndexFunc(items, func(other *leapmuxv1.QueuedAgentInput) bool {
			return other.GetReorderSegment() == item.GetReorderSegment()
		})
		return items[i].GetId(), nil
	case inv.Changed("after"):
		after, err := q.resolveMoveTarget(items, item, stringFlag(inv, "after"), "after")
		if err != nil {
			return "", err
		}
		idx := slices.IndexFunc(items, func(other *leapmuxv1.QueuedAgentInput) bool { return other.GetId() == after.GetId() })
		for j := idx + 1; j < len(items); j++ {
			if items[j].GetId() == item.GetId() {
				continue
			}
			if items[j].GetReorderSegment() == item.GetReorderSegment() {
				return items[j].GetId(), nil
			}
			break
		}
		return "", nil
	}
	before, err := q.resolveMoveTarget(items, item, stringFlag(inv, "before"), "before")
	if err != nil {
		return "", err
	}
	return before.GetId(), nil
}

// runAgentQueueGet reads the full text and metadata of one queued input
// without acquiring an edit lease or pausing queue dispatch.
func runAgentQueueGet(inv *cli.Invocation) (any, error) {
	return runAgentQueue(inv, func(q agentQueue) (any, error) {
		queue, err := q.snapshot()
		if err != nil {
			return nil, err
		}
		item, err := q.findInput(queue.GetItems(), inv.Arg("input"))
		if err != nil {
			return nil, err
		}
		row := queueItemRowOf(item)
		var getResp leapmuxv1.GetQueuedAgentInputResponse
		if err := q.call(leapmuxv1connect.WorkerAgentInputServiceGetQueuedAgentInputProcedure,
			&leapmuxv1.GetQueuedAgentInputRequest{AgentId: q.agent.ID, InputId: item.GetId()}, &getResp); err != nil {
			return nil, err
		}
		if getResp.GetItem() != nil {
			row = queueItemRowOf(getResp.GetItem())
		}
		row.Text = getResp.GetText()
		return row, nil
	})
}

// runAgentQueueEdit edits a queued input's text and/or schedule.
func runAgentQueueEdit(inv *cli.Invocation) (any, error) {
	hasText := inv.HasArg("text") || inv.Changed("file")
	hasAt := inv.Changed("at")
	clearSched := boolFlag(inv, "clear-schedule")
	if hasAt && clearSched {
		return nil, cli.Usagef("--at and --clear-schedule exclude each other")
	}
	if !hasText && !hasAt && !clearSched {
		return nil, cli.Usagef("nothing to update: pass replacement text, --file, --at, or --clear-schedule")
	}
	var newText string
	var err error
	if hasText {
		newText, err = inputText(inv, "text")
		if err != nil {
			return nil, err
		}
	}
	var scheduledAt *string
	if clearSched {
		empty := ""
		scheduledAt = &empty
	} else if hasAt {
		now := time.Now()
		sched, parseErr := parseSendAt(stringFlag(inv, "at"), now)
		if parseErr != nil {
			return nil, parseErr
		}
		if !sched.Relative {
			if err := requireFutureSchedule(sched.At, now, "pass a future timestamp or relative offset such as +5m"); err != nil {
				return nil, err
			}
		}
		formatted := timefmt.Format(sched.At)
		scheduledAt = &formatted
	}
	takeover := boolFlag(inv, "takeover")
	return runAgentQueue(inv, func(q agentQueue) (any, error) {
		queue, err := q.snapshot()
		if err != nil {
			return nil, err
		}
		item, err := q.findInput(queue.GetItems(), inv.Arg("input"))
		if err != nil {
			return nil, err
		}
		clientID := "cli-edit-" + uuid.NewString()
		var beginResp leapmuxv1.BeginQueuedAgentInputEditResponse
		if err := q.call(leapmuxv1connect.WorkerAgentInputServiceBeginQueuedAgentInputEditProcedure,
			&leapmuxv1.BeginQueuedAgentInputEditRequest{AgentId: q.agent.ID, InputId: item.GetId(), ClientId: clientID, Takeover: takeover}, &beginResp); err != nil {
			return nil, err
		}
		textToSend := newText
		if !hasText {
			textToSend = beginResp.GetText()
		}
		version := item.GetVersion()
		for _, snapItem := range beginResp.GetSnapshot().GetItems() {
			if snapItem.GetId() == item.GetId() {
				version = snapItem.GetVersion()
				break
			}
		}
		var updResp leapmuxv1.UpdateQueuedAgentInputResponse
		if err := q.call(leapmuxv1connect.WorkerAgentInputServiceUpdateQueuedAgentInputProcedure,
			&leapmuxv1.UpdateQueuedAgentInputRequest{
				AgentId:         q.agent.ID,
				InputId:         item.GetId(),
				ClientId:        clientID,
				ExpectedVersion: version,
				Text:            textToSend,
				Attachments:     beginResp.GetAttachments(),
				ScheduledAt:     scheduledAt,
			}, &updResp); err != nil {
			cancelCtx, cancel := context.WithTimeout(context.WithoutCancel(q.ctx), 5*time.Second)
			_ = q.w.Call(cancelCtx, leapmuxv1connect.WorkerAgentInputServiceCancelQueuedAgentInputEditProcedure,
				&leapmuxv1.CancelQueuedAgentInputEditRequest{AgentId: q.agent.ID, InputId: item.GetId(), ClientId: clientID}, nil)
			cancel()
			return nil, err
		}
		return agentResult{ID: q.agent.ID, InputID: item.GetId()}, nil
	})
}

// selectInput resolves an explicit <input> argument, or returns the first
// queued input matching match when <input> is omitted.
func (q agentQueue) selectInput(
	inv *cli.Invocation,
	items []*leapmuxv1.QueuedAgentInput,
	match func(*leapmuxv1.QueuedAgentInput) bool,
	notFoundCode cli.Code,
	notFoundMsg string,
) (*leapmuxv1.QueuedAgentInput, error) {
	if inv.HasArg("input") {
		return q.findInput(items, inv.Arg("input"))
	}
	if i := slices.IndexFunc(items, match); i >= 0 {
		return items[i], nil
	}
	return nil, cli.Newf(notFoundCode, "%s", notFoundMsg)
}

func runAgentQueueSteer(inv *cli.Invocation) (any, error) {
	return runAgentQueue(inv, func(q agentQueue) (any, error) {
		queue, err := q.snapshot()
		if err != nil {
			return nil, err
		}
		target, err := q.selectInput(inv, queue.GetItems(), (*leapmuxv1.QueuedAgentInput).GetCanSteer,
			cli.CodeConflict, fmt.Sprintf("agent %s has no steerable queued input", q.agent.ID))
		if err != nil {
			return nil, err
		}
		if err := q.call(leapmuxv1connect.WorkerAgentInputServiceSteerQueuedAgentInputProcedure,
			&leapmuxv1.SteerQueuedAgentInputRequest{AgentId: q.agent.ID, InputId: target.GetId()}, nil); err != nil {
			return nil, err
		}
		return agentResult{ID: q.agent.ID, InputID: target.GetId()}, nil
	})
}

func runAgentQueuePreempt(inv *cli.Invocation) (any, error) {
	return runAgentQueue(inv, func(q agentQueue) (any, error) {
		queue, err := q.snapshot()
		if err != nil {
			return nil, err
		}
		target, err := q.selectInput(inv, queue.GetItems(), (*leapmuxv1.QueuedAgentInput).GetCanPreempt,
			cli.CodeConflict, fmt.Sprintf("agent %s has no preemptable queued input", q.agent.ID))
		if err != nil {
			return nil, err
		}
		if err := q.call(leapmuxv1connect.WorkerAgentInputServicePreemptQueuedAgentInputProcedure,
			&leapmuxv1.PreemptQueuedAgentInputRequest{AgentId: q.agent.ID, InputId: target.GetId()}, nil); err != nil {
			return nil, err
		}
		return agentResult{ID: q.agent.ID, InputID: target.GetId()}, nil
	})
}

func runAgentQueueAdvance(inv *cli.Invocation) (any, error) {
	return runAgentQueue(inv, func(q agentQueue) (any, error) {
		var resp leapmuxv1.AdvanceAgentInputQueueResponse
		if err := q.call(leapmuxv1connect.WorkerAgentInputServiceAdvanceAgentInputQueueProcedure,
			&leapmuxv1.AdvanceAgentInputQueueRequest{AgentId: q.agent.ID}, &resp); err != nil {
			return nil, err
		}
		queue := resp.GetSnapshot()
		reason := pauseReasonOf(queue)
		return agentQueueState{ID: q.agent.ID, Paused: reason != "", PauseReason: reason}, nil
	})
}

func runAgentQueueRetry(inv *cli.Invocation) (any, error) {
	confirmUncertain := boolFlag(inv, "confirm-uncertain")
	return runAgentQueue(inv, func(q agentQueue) (any, error) {
		queue, err := q.snapshot()
		if err != nil {
			return nil, err
		}
		target, err := q.selectInput(inv, queue.GetItems(), func(item *leapmuxv1.QueuedAgentInput) bool {
			return item.GetState() == leapmuxv1.AgentInputState_AGENT_INPUT_STATE_FAILED ||
				item.GetState() == leapmuxv1.AgentInputState_AGENT_INPUT_STATE_DELIVERY_UNCERTAIN
		}, cli.CodeNotFound, fmt.Sprintf("agent %s has no failed or delivery-uncertain input to retry", q.agent.ID))
		if err != nil {
			return nil, err
		}
		if err := q.call(leapmuxv1connect.WorkerAgentInputServiceRetryQueuedAgentInputProcedure,
			&leapmuxv1.RetryQueuedAgentInputRequest{
				AgentId:                  q.agent.ID,
				InputId:                  target.GetId(),
				ConfirmDeliveryUncertain: confirmUncertain,
			}, nil); err != nil {
			return nil, err
		}
		return agentResult{ID: q.agent.ID, InputID: target.GetId()}, nil
	})
}

func runAgentQueueClear(inv *cli.Invocation) (any, error) {
	return runAgentQueue(inv, func(q agentQueue) (any, error) {
		queue, err := q.snapshot()
		if err != nil {
			return nil, err
		}
		unlocked := make([]*leapmuxv1.QueuedAgentInput, 0, len(queue.GetItems()))
		for _, item := range queue.GetItems() {
			if !inputLocked(item) {
				unlocked = append(unlocked, item)
			}
		}
		if err := q.deleteInputsBackToFront(unlocked); err != nil {
			return nil, err
		}
		removed := make([]string, len(unlocked))
		for i, item := range unlocked {
			removed[i] = item.GetId()
		}
		return queueRemoveBatchResult{ID: q.agent.ID, InputIDs: removed}, nil
	})
}

// runAgentQueueSetPaused pauses or resumes the queue.
func runAgentQueueSetPaused(inv *cli.Invocation, paused bool) (any, error) {
	return runAgentQueue(inv, func(q agentQueue) (any, error) {
		var resp leapmuxv1.SetAgentInputQueuePausedResponse
		if err := q.call(leapmuxv1connect.WorkerAgentInputServiceSetAgentInputQueuePausedProcedure,
			&leapmuxv1.SetAgentInputQueuePausedRequest{AgentId: q.agent.ID, Paused: paused}, &resp); err != nil {
			return nil, err
		}
		queue := resp.GetSnapshot()
		if !paused {
			if hold := queueHold(q.agent.ID, queue); hold != "" {
				_ = inv.Printer.Notice("%s", hold)
			}
		}
		reason := pauseReasonOf(queue)
		return agentQueueState{ID: q.agent.ID, Paused: reason != "", PauseReason: reason}, nil
	})
}
