package cmd

import (
	"context"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli/control"
)

// RunAgentQuakeOpen, RunAgentQuakeClose and RunAgentQuakeToggle show, hide and
// flip the quake panel of one agent tab on every frontend the caller has open.
//
// They are the one place the Control CLI reaches into the UI, and they do it
// without introducing shared UI state: SetQuakePanel stores nothing and the
// worker relays the request as a transient event. A frontend that does not run
// simply never hears it, which is the correct outcome for a keystroke.
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
// It uses the ordinary agent scaffold, and that works from INSIDE the quake
// panel as well, which is the most common way this command runs. A companion
// terminal advertises its OWNER AGENT as the ambient tab rather than itself
// (see TerminalSpawning in `internal/worker/controlipc`), because a companion
// has no CRDT tab for the hub's LocateTab to resolve. So the `--tab-id` env
// default inside the panel already gives the agent whose panel it is, and no
// terminal-to-owner mapping is needed here.
func runAgentQuake(rawCtx any, args []string, action leapmuxv1.QuakePanelAction) error {
	return withResolvedAgent(rawCtx, args, agentScaffoldOpts{
		body: func(ctx context.Context, c *control.Client, workerID, agentID string) error {
			req := &leapmuxv1.SetQuakePanelRequest{AgentId: agentID, Action: action}
			if err := callInnerRPC(ctx, c, workerID, "SetQuakePanel", req, nil); err != nil {
				return err
			}
			return control.EmitData(map[string]any{
				"agent_id": agentID,
				"action":   quakeActionName(action),
			})
		},
	})
}

// quakeActionName is the envelope's spelling of the action. It restates the
// verb the user typed, so a caller that reads the JSON never has to map an enum
// number back to a word. Same rule as messageSourceName and terminalStatusName
// -- see the note on the enum helpers in `enums.go`.
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
