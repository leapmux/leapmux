package cmd

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/sync/errgroup"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/hub/crdt"
)

// The busy-tab guard, shared by `tab close` and the `tile` verbs that tombstone
// tabs.
//
// The browser raises a dialog for this; the CLI never prompts (a worker-spawned
// caller has no terminal), so it refuses and names the flag that overrides it --
// the same shape the `--worktree` gate already uses.
//
// These operations do not use this guard.
//
//   - `agent interrupt` aborts a turn without closing anything. Interrupting IS
//     the intent, and a guard would ask the user to confirm what they asked for.

// allowBusyFlagHelp is the one spelling of the flag's help text, so `tab close`
// and the `tile` verbs cannot describe the same override differently.
const allowBusyFlagHelp = "close even if work is in progress or the Worker cannot verify the tab's activity"

// tabBusyReason is what one tab is running, already rendered.
type tabBusyReason struct {
	tabID string
	// detail is the human half: "agent turn is in progress, 2 active background
	// tasks", or "2 processes running (node pid 51234, esbuild pid 51240)".
	detail string
}

// errTabBusyRefused builds the refusal envelope.
//
// `tab_busy_refused` is a distinct code for the same reason
// `self_target_refused` is: a script can pattern-match it and decide whether to
// re-run with the override, instead of grepping a message. It is deliberately
// NOT --allow-self, which already means something else here -- "close even if
// the target is the calling tab" -- and widening that flag would let one
// hazard's override silently answer for another's.
func errTabBusyRefused(reasons []tabBusyReason) error {
	var b strings.Builder
	b.WriteString("refusing to close ")
	if len(reasons) == 1 {
		b.WriteString("busy tab ")
		b.WriteString(reasons[0].tabID)
		b.WriteString(": ")
		b.WriteString(reasons[0].detail)
	} else {
		fmt.Fprintf(&b, "%d busy tabs: ", len(reasons))
		parts := make([]string, 0, len(reasons))
		for _, r := range reasons {
			parts = append(parts, r.tabID+" ("+r.detail+")")
		}
		b.WriteString(strings.Join(parts, ", "))
	}
	return cli.Newf(cli.CodeTabBusyRefused, "%s", b.String()).
		WithHint("pass --allow-busy to close anyway")
}

// agentBusyDetail renders a working agent's reason, or "" when it is idle.
//
// Reads AgentInfo.activity_state, which the Worker derives from the provider's own turn
// bookkeeping, the background-task registry, the pending control requests and
// the process state. The CLI does not re-derive any of that; there is one
// definition and it lives on the Worker.
func agentBusyDetail(info *leapmuxv1.AgentInfo) string {
	if info == nil {
		return ""
	}
	// A SUBAGENT tab closes in the UI only. CloseTab on a child keeps the row
	// and the transcript and stops no process, so the run continues and the tab
	// can be revived -- there is nothing for a close to interrupt, whatever the
	// child's own registry row says. The browser's probe carves the same case
	// out; without this the CLI refused a scripted close that destroys nothing,
	// and a `tile close` refused the whole tile over one subagent transcript.
	if info.GetParentAgentId() != "" {
		return ""
	}
	n := info.GetActiveBackgroundTasks()
	switch info.GetActivityState() {
	case leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WAITING_FOR_USER:
		// The indicator does not spin here, but a close still kills the turn and
		// every background task under it. This is the state that used to close
		// unwarned, because the browser and the CLI both read one boolean that
		// said "not working".
		return withTaskCount("agent turn is waiting for a permission answer", n)
	case leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_WORKING:
		// WORKING covers a turn OR a running background task, and the state
		// alone does not say which. Claim the turn only when nothing else
		// explains it: a turn that ended while a subagent kept running would
		// otherwise be reported as in progress after it finished.
		if n == 0 {
			return "agent turn is in progress"
		}
		return withTaskCount("agent is working", n)
	default:
		return ""
	}
}

// withTaskCount appends the active-task count to a reason, so a refusal names
// what it protects rather than only that it refuses.
func withTaskCount(detail string, n int32) string {
	if n == 0 {
		return detail
	}
	return fmt.Sprintf("%s, %s active", detail, pluralize(int(n), "background task"))
}

// terminalBusyDetail renders a terminal's running processes, or "" when nothing
// runs below its login shell.
func terminalBusyDetail(t *leapmuxv1.TerminalProcesses) string {
	procs := t.GetProcesses()
	if len(procs) == 0 {
		return ""
	}
	named := make([]string, 0, len(procs))
	for _, p := range procs {
		name := p.GetName()
		if name == "" {
			// macOS resolves a long name through a second syscall that fails for
			// another user's process, so the Worker reports the pid alone.
			name = "unnamed"
		}
		named = append(named, fmt.Sprintf("%s pid %d", name, p.GetPid()))
	}
	detail := fmt.Sprintf("%s running (%s)", pluralize(len(procs), "process", "processes"), strings.Join(named, ", "))
	if extra := int(t.GetTotalCount()) - len(procs); extra > 0 {
		detail += fmt.Sprintf(" and %d more", extra)
	}
	return detail
}

// inspectTabsBusy asks the Worker which tabs are running work. The error
// states which activity it could not verify.
//
// Two reads, because the two tab families answer from different places: an agent
// carries activity on the row `ListAgents` already returns, and a terminal's
// process tree is asked for once, at the moment of the close. Both are batched
// and issued concurrently when a worker holds both families.
func inspectTabsBusy(call workerCaller, tabs []tabRef) ([]tabBusyReason, error) {
	var agentIDs, terminalIDs []string
	for _, t := range tabs {
		switch t.tabType {
		case leapmuxv1.TabType_TAB_TYPE_AGENT:
			agentIDs = append(agentIDs, t.tabID)
		case leapmuxv1.TabType_TAB_TYPE_TERMINAL:
			terminalIDs = append(terminalIDs, t.tabID)
		default:
			// A FILE or IMAGE tab is a viewer: closing it stops nothing.
		}
	}

	var (
		agentDetails map[string]string
		agentErr     error
		termDetails  map[string]string
		termErr      error
	)
	inspectAgents := func() {
		resp := &leapmuxv1.ListAgentsResponse{}
		if err := call(leapmuxv1connect.WorkerAgentServiceListAgentsProcedure, &leapmuxv1.ListAgentsRequest{TabIds: agentIDs}, resp); err != nil {
			agentErr = err
			return
		}
		agentDetails = make(map[string]string)
		known := map[string]bool{}
		for _, a := range resp.GetAgents() {
			known[a.GetId()] = a.GetParentAgentId() != "" || a.GetActivityState() != leapmuxv1.AgentActivityState_AGENT_ACTIVITY_STATE_UNSPECIFIED
			if d := agentBusyDetail(a); d != "" {
				agentDetails[a.GetId()] = d
			}
		}
		for _, verdict := range resp.GetVerdicts() {
			if verdict.GetStatus() == leapmuxv1.TabHydrationStatus_TAB_HYDRATION_STATUS_ABSENT {
				known[verdict.GetTabId()] = true
			}
		}
		var unknown []string
		for _, id := range agentIDs {
			if !known[id] {
				unknown = append(unknown, id)
			}
		}
		if len(unknown) > 0 {
			agentErr = cli.Newf(cli.CodeUnavailable, "the Worker did not report activity for %s", strings.Join(unknown, ", "))
		}
	}
	inspectTerminals := func() {
		resp := &leapmuxv1.InspectTerminalProcessesResponse{}
		if err := call(leapmuxv1connect.WorkerTerminalServiceInspectTerminalProcessesProcedure, &leapmuxv1.InspectTerminalProcessesRequest{TerminalIds: terminalIDs}, resp); err != nil {
			termErr = err
			return
		}
		termDetails = make(map[string]string)
		for _, t := range resp.GetTerminals() {
			if d := terminalBusyDetail(t); d != "" {
				termDetails[t.GetTerminalId()] = d
			}
		}
	}

	switch {
	case len(agentIDs) > 0 && len(terminalIDs) > 0:
		var g errgroup.Group
		g.Go(func() error {
			inspectAgents()
			return nil
		})
		g.Go(func() error {
			inspectTerminals()
			return nil
		})
		_ = g.Wait()
	case len(agentIDs) > 0:
		inspectAgents()
	case len(terminalIDs) > 0:
		inspectTerminals()
	}

	details := make(map[string]string, len(agentDetails)+len(termDetails))
	for id, d := range agentDetails {
		details[id] = d
	}
	for id, d := range termDetails {
		details[id] = d
	}
	var probeErrors []error
	if agentErr != nil {
		probeErrors = append(probeErrors, agentErr)
	}
	if termErr != nil {
		probeErrors = append(probeErrors, termErr)
	}

	// Report in the caller's tab order, so a refusal reads in the order the
	// close would have run.
	var out []tabBusyReason
	for _, t := range tabs {
		if d, ok := details[t.tabID]; ok {
			out = append(out, tabBusyReason{tabID: t.tabID, detail: d})
		}
	}
	return out, errors.Join(probeErrors...)
}

// inspectTabsBusyOnChannel runs the busy probe for tabs over an already-open
// Worker channel w. ListAgents and InspectTerminalProcesses run concurrently
// when tabs include both agents and terminals.
func inspectTabsBusyOnChannel(ctx context.Context, w workerCall, tabs []crdt.TabRef) (busy []tabBusyReason, probeErr error) {
	if len(tabs) == 0 {
		return nil, nil
	}
	refs := make([]tabRef, 0, len(tabs))
	for _, t := range tabs {
		refs = append(refs, tabRef{tabType: t.TabType, tabID: t.TabID})
	}
	return inspectTabsBusy(func(method string, in, out proto.Message) error {
		return w.Call(ctx, method, in, out)
	}, refs)
}

func busyCloseRefusal(busy []tabBusyReason, probeErr error) error {
	if len(busy) > 0 {
		return errTabBusyRefused(busy)
	}
	if probeErr != nil {
		return cli.FromError(probeErr).WithHint("pass --allow-busy to close even if activity cannot be verified")
	}
	return nil
}

func tabNeedsBusyProbe(tabType leapmuxv1.TabType) bool {
	return tabType == leapmuxv1.TabType_TAB_TYPE_AGENT || tabType == leapmuxv1.TabType_TAB_TYPE_TERMINAL
}

// tabRef is one tab to ask the busy probe about.
type tabRef struct {
	tabType leapmuxv1.TabType
	tabID   string
}

// groupTabsByWorker groups tabs by their hosting Worker in first-seen order.
// Tabs whose CRDT record carries no worker_id are returned in workerless so
// callers can record probe errors without aborting inspection of the rest.
func groupTabsByWorker(tabs []crdt.TabRef, state *leapmuxv1.UserMaterialized) (byWorker map[string][]crdt.TabRef, order []string, workerless []crdt.TabRef) {
	byWorker = make(map[string][]crdt.TabRef)
	stateTabs := state.GetTabs()
	for _, t := range tabs {
		workerID := stateTabs[t.TabID].GetWorkerId().GetValue()
		if workerID == "" {
			workerless = append(workerless, t)
			continue
		}
		if _, seen := byWorker[workerID]; !seen {
			order = append(order, workerID)
		}
		byWorker[workerID] = append(byWorker[workerID], t)
	}
	return byWorker, order, workerless
}
