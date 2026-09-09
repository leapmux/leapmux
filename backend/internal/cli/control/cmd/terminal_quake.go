package cmd

import (
	"context"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
)

// RunTerminalQuakeOpen, RunTerminalQuakeClose and RunTerminalQuakeToggle show,
// hide and flip the quake panel of one WORKING DIRECTORY on every frontend the
// caller has open.
//
// They are the one place the Control CLI reaches into the UI, and they do it
// without introducing shared UI state: SetQuakePanel stores nothing and the
// worker relays the request as a transient event. A frontend that does not run
// simply never hears it, which is the correct outcome for a keystroke.
func RunTerminalQuakeOpen(rawCtx any, args []string) error {
	return runTerminalQuake(rawCtx, args, leapmuxv1.QuakePanelAction_QUAKE_PANEL_ACTION_OPEN)
}

func RunTerminalQuakeClose(rawCtx any, args []string) error {
	return runTerminalQuake(rawCtx, args, leapmuxv1.QuakePanelAction_QUAKE_PANEL_ACTION_CLOSE)
}

func RunTerminalQuakeToggle(rawCtx any, args []string) error {
	return runTerminalQuake(rawCtx, args, leapmuxv1.QuakePanelAction_QUAKE_PANEL_ACTION_TOGGLE)
}

// quakeWorkingDirUsage states the flag AND its default in one string, because
// the default is what makes the command usable with no flags at all.
const quakeWorkingDirUsage = "working directory whose quake panel to act on (defaults to $LEAPMUX_CONTROL_WORKING_DIR)"

// runTerminalQuake is the shared body. The action is the only difference
// between the three verbs, so they share everything else rather than restating
// a flag set and a resolve three times.
//
// A quake terminal is addressed by (worker, working directory), so this needs
// no tab at all -- which is exactly why it lives beside the terminal verbs
// rather than the agent ones. `--working-dir` defaults to
// $LEAPMUX_CONTROL_WORKING_DIR, which every spawn exports, so the bare command
// works from inside the panel it toggles, from an agent's own shell, and from a
// terminal tab alike -- and in every one of those the ambient directory is the
// panel the user means.
//
// The worker still has to be resolved, because the directory alone does not
// name a machine: `--worker-id` (or $LEAPMUX_CONTROL_WORKER_ID), or a
// `--tab-id` the resolver can derive one from.
func runTerminalQuake(rawCtx any, args []string, action leapmuxv1.QuakePanelAction) error {
	cmd := asCtx(rawCtx)
	var hub, workingDir string
	var in resolve.Inputs
	fs := flagSet(cmd, &hub)
	resolve.BindEntityFlags(fs, &in, resolve.FlagOptions{})
	fs.StringVar(&workingDir, "working-dir", workingDirEnv(), quakeWorkingDirUsage)
	if err := parseFlags(fs, args, cmd.Description()); err != nil {
		return err
	}
	if workingDir == "" {
		return emitMissingDirFlagErr("--working-dir")
	}
	return resolveAndEmit(hub, resolve.Need{WorkerID: true}, in, func(ctx context.Context, c *control.Client, got resolve.Resolved) error {
		if err := maybePreflightWorker(ctx, c, got.WorkerID); err != nil {
			return err
		}
		req := &leapmuxv1.SetQuakePanelRequest{WorkingDir: workingDir, Action: action}
		if err := callInnerRPC(ctx, c, got.WorkerID, "SetQuakePanel", req, nil); err != nil {
			return err
		}
		return control.EmitData(map[string]any{
			"working_dir": workingDir,
			"action":      quakeActionName(action),
		})
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
