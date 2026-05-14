package cmd

import (
	"context"
	"io"
	"os"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli/remote"
	"github.com/leapmux/leapmux/internal/cli/remote/resolve"
)

// `terminal send`, `terminal get`, and `terminal shells` mirror the
// agent-side verbs: they bind the universal entity-ID flag set pinned
// to TabTypeTerminal (where applicable), resolve the (worker, tab)
// pair via the same RPCs, then dispatch worker inner-RPCs over the
// E2EE / local-IPC transport. The generic `tab open / close / list /
// rename` surface handles every operation that isn't terminal-specific.

// RunTerminalSend writes input bytes to a terminal's PTY. `--data`
// passes ASCII directly; `--stdin` slurps stdin so callers can pipe
// binary payloads (escape sequences, control characters, paste
// buffers) without shell-quoting. Empty payloads are rejected so
// scripts don't silently no-op.
func RunTerminalSend(rawCtx any, args []string) error {
	cmd := asCtx(rawCtx)
	var hub, dataStr string
	var stdinFlag bool
	var in resolve.Inputs
	fs := flagSet(cmd, &hub)
	resolve.BindEntityFlags(fs, &in, resolve.FlagOptions{
		HideOrg:      true,
		HideUser:     true,
		FixedTabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL,
	})
	fs.StringVar(&dataStr, "data", "", "bytes to write to the PTY (or use --stdin for binary input)")
	fs.BoolVar(&stdinFlag, "stdin", false, "read input bytes from stdin")
	if err := parseFlags(fs, args, cmd.Description()); err != nil {
		return err
	}
	var payload []byte
	if stdinFlag {
		buf, err := io.ReadAll(os.Stdin)
		if err != nil {
			return remote.EmitErrorWith("stdin_read_failed", err)
		}
		payload = buf
	} else {
		payload = []byte(dataStr)
	}
	if len(payload) == 0 {
		return remote.EmitError("invalid_request", "--data or --stdin (with non-empty input) is required")
	}
	return resolveAndEmit(hub, resolve.Need{TabID: true, WorkerID: true}, in, func(ctx context.Context, c *remote.Client, got resolve.Resolved) error {
		if err := maybePreflightWorker(ctx, c, got.WorkerID); err != nil {
			return err
		}
		req := &leapmuxv1.SendInputRequest{
			OrgId:       got.OrgID,
			WorkspaceId: got.WorkspaceID,
			TerminalId:  got.TabID,
			Data:        payload,
		}
		if err := callInnerRPC(ctx, c, got.WorkerID, "SendInput", req, nil); err != nil {
			return err
		}
		return remote.EmitData(map[string]any{"tab_id": got.TabID, "bytes_sent": len(payload)})
	})
}

// RunTerminalGet returns the worker-side terminal record (geometry,
// shell, working dir, title, git context). Mirrors `agent get`:
// reuses ListTerminals with a single tab id rather than introducing a
// GetTerminal RPC, since the worker already filters by id and we only
// ever want one row.
//
// `--screen` prints just the PTY's retained-window bytes directly to
// stdout (no JSON envelope) so a follow-up `cat` / `less -R` renders
// the ANSI escape sequences naturally. Strict JSON would escape the
// control bytes (ESC -> ), which is parser-correct but
// unreadable for a human inspecting a terminal. The metadata-only
// JSON envelope is still the default since most callers want the
// title / geometry / git context.
func RunTerminalGet(rawCtx any, args []string) error {
	cmd := asCtx(rawCtx)
	var hub string
	var screenOnly bool
	var in resolve.Inputs
	fs := flagSet(cmd, &hub)
	resolve.BindEntityFlags(fs, &in, resolve.FlagOptions{
		HideOrg:      true,
		HideUser:     true,
		FixedTabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL,
	})
	fs.BoolVar(&screenOnly, "screen", false, "print the PTY's retained-window bytes directly to stdout (no JSON envelope); ANSI escapes render naturally in a terminal")
	if err := parseFlags(fs, args, cmd.Description()); err != nil {
		return err
	}
	return resolveAndEmit(hub, resolve.Need{TabID: true, WorkerID: true}, in, func(ctx context.Context, c *remote.Client, got resolve.Resolved) error {
		if err := maybePreflightWorker(ctx, c, got.WorkerID); err != nil {
			return err
		}
		var resp leapmuxv1.ListTerminalsResponse
		if err := callInnerRPC(ctx, c, got.WorkerID, "ListTerminals", &leapmuxv1.ListTerminalsRequest{TabIds: []string{got.TabID}}, &resp); err != nil {
			return err
		}
		for _, t := range resp.GetTerminals() {
			if t.GetTerminalId() == got.TabID {
				if screenOnly {
					_, _ = remote.Out.Write(t.GetScreen())
					return nil
				}
				return remote.EmitData(terminalInfoToMap(t))
			}
		}
		return remote.EmitError("not_found", "terminal not found or not accessible: "+got.TabID)
	})
}

// terminalInfoToMap projects a TerminalInfo into a JSON-friendly map.
// The screen field is the PTY's retained-window byte buffer (raw
// terminal output, ANSI escape sequences and all); the proto-generated
// Go type carries it as []byte, which encoding/json marshals as
// base64 -- correct for transport but useless for a human reading the
// envelope. We emit it as a Go string so `jq -r .data.screen` (or a
// plain cat of the response) shows the actual terminal content, with
// ANSI sequences intact so a follow-up `printf %s` reproduces what
// the user would see on the live terminal.
//
// status is rendered through terminalStatusName so the envelope shows
// "ready" / "starting" / etc. instead of an opaque integer ordinal.
func terminalInfoToMap(t *leapmuxv1.TerminalInfo) map[string]any {
	return map[string]any{
		"terminal_id":       t.GetTerminalId(),
		"cols":              t.GetCols(),
		"rows":              t.GetRows(),
		"screen":            string(t.GetScreen()),
		"screen_end_offset": t.GetScreenEndOffset(),
		"exited":            t.GetExited(),
		"working_dir":       t.GetWorkingDir(),
		"shell_start_dir":   t.GetShellStartDir(),
		"git_branch":        t.GetGitBranch(),
		"git_origin_url":    t.GetGitOriginUrl(),
		"git_toplevel":      t.GetGitToplevel(),
		"title":             t.GetTitle(),
		"status":            terminalStatusName(t.GetStatus()),
		"startup_error":     t.GetStartupError(),
		"startup_message":   t.GetStartupMessage(),
	}
}

// RunTerminalShells lists the shells installed on a worker. Resolver
// pulls --worker-id from any universal input (--tab-id, --workspace-id,
// ...); the response carries the worker's $SHELL default alongside the
// full shells list so callers can render a picker.
func RunTerminalShells(rawCtx any, args []string) error {
	cmd := asCtx(rawCtx)
	var hub string
	var in resolve.Inputs
	fs := flagSet(cmd, &hub)
	resolve.BindEntityFlags(fs, &in, resolve.FlagOptions{
		HideOrg:  true,
		HideUser: true,
	})
	if err := parseFlags(fs, args, cmd.Description()); err != nil {
		return err
	}
	return resolveAndEmit(hub, resolve.Need{WorkerID: true}, in, func(ctx context.Context, c *remote.Client, got resolve.Resolved) error {
		if err := maybePreflightWorker(ctx, c, got.WorkerID); err != nil {
			return err
		}
		req := &leapmuxv1.ListAvailableShellsRequest{
			OrgId:       got.OrgID,
			WorkspaceId: got.WorkspaceID,
			WorkerId:    got.WorkerID,
		}
		var resp leapmuxv1.ListAvailableShellsResponse
		if err := callInnerRPC(ctx, c, got.WorkerID, "ListAvailableShells", req, &resp); err != nil {
			return err
		}
		return remote.EmitData(map[string]any{
			"shells":        resp.GetShells(),
			"default_shell": resp.GetDefaultShell(),
		})
	})
}

// renameTerminalTab wires `tab rename` for terminal-typed tabs.
// Mirrors the agent rename path: a single worker inner-RPC
// (UpdateTerminalTitle) updates the in-memory manager and the DB row,
// which the worker also broadcasts on the workspace's private-event
// channel so live clients see the new title.
func renameTerminalTab(ctx context.Context, c *remote.Client, got resolve.Resolved, title string) error {
	req := &leapmuxv1.UpdateTerminalTitleRequest{
		OrgId:       got.OrgID,
		WorkspaceId: got.WorkspaceID,
		TerminalId:  got.TabID,
		Title:       title,
	}
	if err := callInnerRPC(ctx, c, got.WorkerID, "UpdateTerminalTitle", req, nil); err != nil {
		return err
	}
	return remote.EmitData(map[string]string{"tab_id": got.TabID, "tab_type": "terminal", "title": title})
}
