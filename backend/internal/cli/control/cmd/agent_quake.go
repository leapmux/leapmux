package cmd

import (
	"context"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
)

// RunAgentQuakeOpen, RunAgentQuakeClose and RunAgentQuakeToggle show, hide and
// flip the quake panel of one agent tab on every frontend the caller has open.
//
// They are the one place the Control CLI reaches into the UI, and they do it
// without introducing shared UI state: SetQuakePanel stores nothing and the
// worker relays the request as a transient event. A frontend that is not
// running simply never hears it, which is the correct outcome for a keystroke.
func RunAgentQuakeOpen(rawCtx any, args []string) error {
	return runAgentQuake(rawCtx, args, leapmuxv1.QuakePanelAction_QUAKE_PANEL_ACTION_OPEN)
}

func RunAgentQuakeClose(rawCtx any, args []string) error {
	return runAgentQuake(rawCtx, args, leapmuxv1.QuakePanelAction_QUAKE_PANEL_ACTION_CLOSE)
}

func RunAgentQuakeToggle(rawCtx any, args []string) error {
	return runAgentQuake(rawCtx, args, leapmuxv1.QuakePanelAction_QUAKE_PANEL_ACTION_TOGGLE)
}

// runAgentQuake is the shared body. The action is the only difference between
// the three verbs, so they share everything else rather than restating a flag
// set and a resolve three times.
//
// It does NOT use `withResolvedAgent`, and the reason is the most common way
// this command is run. That scaffold pins the tab type to AGENT, which makes the
// `--tab-id` env default fire only when LEAPMUX_CONTROL_TAB_TYPE is "agent".
// Inside the quake terminal itself the ambient tab is the TERMINAL, so the
// scaffold would refuse "close the panel I am typing in" -- the most natural use
// there is. Leaving the type unpinned lets the resolver report what the ambient
// id actually names, and a companion terminal is then mapped to its owner below.
func runAgentQuake(rawCtx any, args []string, action leapmuxv1.QuakePanelAction) error {
	cmd := asCtx(rawCtx)
	var hub string
	var in resolve.Inputs
	fs := flagSet(cmd, &hub)
	resolve.BindEntityFlags(fs, &in, resolve.FlagOptions{})
	if err := parseFlags(fs, args, cmd.Description()); err != nil {
		return err
	}
	if !hasAnyEntityInput(in) {
		return control.EmitError("invalid_request", "missing required ID(s): pass --tab-id (or set LEAPMUX_CONTROL_TAB_ID)")
	}
	return resolveAndEmit(hub, resolve.Need{TabID: true, WorkerID: true}, in, func(ctx context.Context, c *control.Client, got resolve.Resolved) error {
		if err := maybePreflightWorker(ctx, c, got.WorkerID); err != nil {
			return err
		}
		agentID, err := quakeOwnerAgentID(ctx, c, got)
		if err != nil {
			return err
		}
		req := &leapmuxv1.SetQuakePanelRequest{AgentId: agentID, Action: action}
		if err := callInnerRPC(ctx, c, got.WorkerID, "SetQuakePanel", req, nil); err != nil {
			return err
		}
		return control.EmitData(map[string]any{
			"agent_id": agentID,
			"action":   quakeActionName(action),
		})
	})
}

// quakeOwnerAgentID maps the resolved tab to the agent whose panel is meant.
//
// An AGENT tab names itself. A TERMINAL tab is accepted only when it is a
// companion -- the shell behind a quake panel -- and it resolves to its owner,
// which is how the command works when run from inside the panel. The worker row
// already carries that link, so nothing new is injected into the terminal's
// environment to express it: a second copy in an env var could disagree with the
// row after a close and reopen.
//
// Any other tab kind is refused by name rather than silently, because "nothing
// happened" and "you aimed at a file tab" need different fixes from the user.
func quakeOwnerAgentID(ctx context.Context, c *control.Client, got resolve.Resolved) (string, error) {
	if got.TabType == leapmuxv1.TabType_TAB_TYPE_AGENT {
		return got.TabID, nil
	}
	if got.TabType != leapmuxv1.TabType_TAB_TYPE_TERMINAL {
		return "", control.EmitError("invalid_request", "a quake panel belongs to an agent tab; pass --tab-id of an agent, or run this from inside the panel's terminal")
	}
	var resp leapmuxv1.ListTerminalsResponse
	req := &leapmuxv1.ListTerminalsRequest{TabIds: []string{got.TabID}}
	if err := callInnerRPC(ctx, c, got.WorkerID, "ListTerminals", req, &resp); err != nil {
		return "", err
	}
	for _, t := range resp.GetTerminals() {
		if t.GetTerminalId() == got.TabID && t.GetOwnerAgentId() != "" {
			return t.GetOwnerAgentId(), nil
		}
	}
	return "", control.EmitError("invalid_request", "this terminal is a tab of its own, not an agent's quake panel; pass --tab-id of an agent")
}

// quakeActionName is the envelope's spelling of the action. It restates the
// verb the user typed, so a caller reading the JSON never has to map an enum
// number back to a word.
func quakeActionName(action leapmuxv1.QuakePanelAction) string {
	switch action {
	case leapmuxv1.QuakePanelAction_QUAKE_PANEL_ACTION_OPEN:
		return "open"
	case leapmuxv1.QuakePanelAction_QUAKE_PANEL_ACTION_CLOSE:
		return "close"
	case leapmuxv1.QuakePanelAction_QUAKE_PANEL_ACTION_TOGGLE:
		return "toggle"
	default:
		return ""
	}
}
