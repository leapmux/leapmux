package cmd

import (
	"fmt"
	"strings"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/hub/crdt"
	"google.golang.org/protobuf/proto"
)

// The busy-tab guard, shared by `tab close` and the `tile` verbs that tombstone
// tabs.
//
// The browser raises a dialog for this; the CLI never prompts (a worker-spawned
// caller has no terminal), so it refuses and names the flag that overrides it --
// the same shape the `--worktree` gate already uses.

// allowBusyFlagHelp is the one spelling of the flag's help text, so `tab close`
// and the `tile` verbs cannot describe the same override differently.
const allowBusyFlagHelp = "close even if a tab has work in progress (an agent turn, or processes running in the terminal)"

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
// re-run with the override, instead of grepping an `invalid_request` blob. It is
// deliberately NOT `--force`, which already means something else here -- "close
// even if the target is the calling tab" -- and widening that flag would let one
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
	b.WriteString("; pass --allow-busy to close anyway")
	return control.EmitError("tab_busy_refused", b.String())
}

// agentBusyDetail renders a working agent's reason, or "" when it is idle.
//
// Reads AgentInfo.busy, which the Worker derives from the provider's own turn
// bookkeeping, the background-task registry, the pending control requests and
// the process state. The CLI does not re-derive any of that; there is one
// definition and it lives on the Worker.
func agentBusyDetail(info *leapmuxv1.AgentInfo) string {
	if info == nil || !info.GetBusy() {
		return ""
	}
	detail := "agent turn is in progress"
	if n := info.GetActiveBackgroundTasks(); n > 0 {
		detail += fmt.Sprintf(", %s active", pluralize(int(n), "background task"))
	}
	return detail
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

// inspectTabsBusy asks the worker which of these tabs are running work.
//
// Two reads, because the two tab families answer from different places: an agent
// carries `busy` on the row `ListAgents` already returns, and a terminal's
// process tree is asked for once, at the moment of the close. Both are batched,
// so a tile of eight tabs costs at most two round trips.
//
// FAILS OPEN, like the browser's probe: a worker that cannot answer lets the
// close proceed unwarned rather than refusing one nobody can confirm. The caller
// keeps its own unreachable-worker handling for the git preflight.
func inspectTabsBusy(call workerCaller, tabs []tabRef) []tabBusyReason {
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

	details := make(map[string]string)
	if len(agentIDs) > 0 {
		resp := &leapmuxv1.ListAgentsResponse{}
		if err := call("ListAgents", &leapmuxv1.ListAgentsRequest{TabIds: agentIDs}, resp); err == nil {
			for _, a := range resp.GetAgents() {
				if d := agentBusyDetail(a); d != "" {
					details[a.GetId()] = d
				}
			}
		}
	}
	if len(terminalIDs) > 0 {
		resp := &leapmuxv1.InspectTerminalProcessesResponse{}
		if err := call("InspectTerminalProcesses", &leapmuxv1.InspectTerminalProcessesRequest{TerminalIds: terminalIDs}, resp); err == nil {
			for _, t := range resp.GetTerminals() {
				if d := terminalBusyDetail(t); d != "" {
					details[t.GetTerminalId()] = d
				}
			}
		}
	}

	// Report in the caller's tab order, so a refusal reads in the order the
	// close would have run.
	var out []tabBusyReason
	for _, t := range tabs {
		if d, ok := details[t.tabID]; ok {
			out = append(out, tabBusyReason{tabID: t.tabID, detail: d})
		}
	}
	return out
}

// tabRef is one tab to ask about.
type tabRef struct {
	tabType leapmuxv1.TabType
	tabID   string
}

// pluralize formats a count with its noun. Mirrors the frontend helper of the
// same name so the two surfaces word the same fact identically.
func pluralize(count int, singular string, plural ...string) string {
	if count == 1 {
		return fmt.Sprintf("1 %s", singular)
	}
	word := singular + "s"
	if len(plural) > 0 {
		word = plural[0]
	}
	return fmt.Sprintf("%d %s", count, word)
}

// guardTabsBusy refuses a bulk close that would interrupt running work.
//
// `tile close --with-tabs=close`, `tile close --recursive` and
// `tile remove-grid --with-tabs=close` tombstone tabs in the CRDT WITHOUT
// calling the worker, so nothing else on their path would ever notice. They are
// the CLI's analogue of the browser's tile/grid/window close, which asks once,
// up front, about every busy tab -- and this asks the same question in the same
// place, before any op is submitted.
//
// One request per WORKER, in first-seen tab order so the refusal reads in the
// order the close would have run. A worker that cannot answer contributes
// nothing: the guard fails open rather than blocking a close nobody can confirm.
func guardTabsBusy(cc *crdtCall, tabs []crdt.TabRef, allowBusy bool) error {
	if allowBusy || len(tabs) == 0 {
		return nil
	}
	var order []string
	byWorker := map[string][]tabRef{}
	for _, t := range tabs {
		workerID := cc.bs.State.GetTabs()[t.TabID].GetWorkerId().GetValue()
		if workerID == "" {
			continue
		}
		if _, seen := byWorker[workerID]; !seen {
			order = append(order, workerID)
		}
		byWorker[workerID] = append(byWorker[workerID], tabRef{tabType: t.TabType, tabID: t.TabID})
	}

	var busy []tabBusyReason
	for _, workerID := range order {
		refs := byWorker[workerID]
		_ = withBestEffortWorkerChannel(cc.ctx, cc.c, workerID, func(w workerCall) error {
			busy = append(busy, inspectTabsBusy(func(method string, in, out proto.Message) error {
				return w.Call(cc.ctx, method, in, out)
			}, refs)...)
			return nil
		})
	}
	if len(busy) == 0 {
		return nil
	}
	return errTabBusyRefused(busy)
}
