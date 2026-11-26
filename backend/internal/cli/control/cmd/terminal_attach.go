package cmd

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"golang.org/x/term"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
)

func newTerminalAttachLeaf() *cobra.Command {
	return cli.NewLeaf(cli.Leaf{
		Use:              "attach [terminal]",
		Short:            "Attach interactively to a remote terminal over E2EE (detach with Ctrl-])",
		QuietUnsupported: true,
		Long: `Attach this terminal interactively to a LeapMux terminal over the encrypted
Worker channel. Keystrokes travel straight to the remote PTY, output streams
live to stdout, and local window resizes forward automatically.

Press Ctrl-] (ASCII 0x1d, or the key set by --detach) to detach cleanly
without closing the remote shell.`,
		Examples: []cli.Example{
			{Command: "leapmux terminal attach 4cbb3f34"},
			{Comment: "Attach with Ctrl-b as the detach key.", Command: "leapmux terminal attach 4cbb3f34 --detach ctrl-b"},
		},
		Flags: func(fs *pflag.FlagSet) {
			fs.String("detach", "ctrl-]", "key sequence that detaches without closing the remote shell (e.g. ctrl-], ctrl-b, ctrl-a)")
			addTargetWorkspaceFlag(fs)
		},
		Complete: completeTargets,
		Run:      runTerminalAttach,
	})
}

func newTerminalPSLeaf() *cobra.Command {
	return cli.NewLeaf(cli.Leaf{
		Use:   "ps [terminal...]",
		Short: "List foreground and background processes running inside one or more terminals",
		Examples: []cli.Example{
			{Command: "leapmux terminal ps"},
			{Command: "leapmux terminal ps 4cbb3f34"},
			{Comment: "Inspect every terminal in the workspace.", Command: "leapmux terminal ps --all"},
		},
		Flags: func(fs *pflag.FlagSet) {
			fs.Bool("all", false, "inspect every open terminal instead of a single target")
			fs.String("workspace", "", `inspect terminals in this workspace (implies --all when no targets are given)`)
		},
		Complete: completeTargets,
		Run:      runTerminalPS,
	})
}

// parseDetachByte parses a control-key name like "ctrl-]" or "ctrl-b" into its
// raw ASCII control byte.
func parseDetachByte(raw string) (byte, error) {
	s := strings.ToLower(strings.TrimSpace(raw))
	if !strings.HasPrefix(s, "ctrl-") || len(s) != 6 {
		return 0, cli.Usagef("--detach must be a ctrl-<key> sequence such as ctrl-], ctrl-a, or ctrl-b; got %q", raw)
	}
	ch := s[5]
	switch {
	case ch >= 'a' && ch <= 'z':
		return ch - 'a' + 1, nil
	case ch == ']':
		return 0x1d, nil
	case ch == '\\':
		return 0x1c, nil
	case ch == '^':
		return 0x1e, nil
	case ch == '_':
		return 0x1f, nil
	default:
		return 0, cli.Usagef("unsupported --detach key %q; use ctrl-a..ctrl-z or ctrl-]", raw)
	}
}

func runTerminalAttach(inv *cli.Invocation) (any, error) {
	detachByte, err := parseDetachByte(stringFlag(inv, "detach"))
	if err != nil {
		return nil, err
	}
	s, err := openStreamSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()

	bounded := s.bounded()
	got, err := bounded.resolveTab(resolve.KindTerminal, inv.Arg("terminal"), resolve.Need{WorkerID: true})
	if err != nil {
		bounded.Close()
		return nil, err
	}
	info, err := bounded.terminalInfo(got)
	bounded.Close()
	if err != nil {
		return nil, err
	}

	out := inv.Command.OutOrStdout()
	in := inv.Stdin
	if in == nil {
		in = os.Stdin
	}

	// Put local terminal into raw mode when stdin is a real TTY.
	isTTY := false
	ttyFD := -1
	if f, ok := in.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		isTTY = true
		ttyFD = int(f.Fd())
		oldState, rawErr := term.MakeRaw(ttyFD)
		if rawErr == nil {
			defer func() { _ = term.Restore(ttyFD, oldState) }()
		}
	}

	// Write initial retained screen bytes immediately.
	if len(info.GetScreen()) > 0 {
		if _, err := out.Write(info.GetScreen()); err != nil {
			return nil, err
		}
	}

	ctx, cancel := context.WithCancel(s.ctx)
	defer cancel()

	// Open ONE persistent E2EE channel for the entire attach session so
	// keystrokes and SIGWINCH resizes reuse a single Noise_NK handshake instead
	// of handshaking per keystroke.
	var runErr error
	chErr := withWorkerChannel(ctx, s.c, got.WorkerID, func(w workerCall) error {
		if isTTY {
			if cols, rows, sizeErr := term.GetSize(ttyFD); sizeErr == nil && cols > 0 && rows > 0 {
				syncCtx, syncCancel := rpcDeadline(ctx)
				_ = w.Call(syncCtx, leapmuxv1connect.WorkerTerminalServiceResizeTerminalProcedure, &leapmuxv1.ResizeTerminalRequest{
					TerminalId: got.ID,
					Cols:       uint32(cols),
					Rows:       uint32(rows),
				}, nil)
				syncCancel()
			}
		}

		// Forward SIGWINCH resizes to the remote PTY over the shared channel.
		var auxWG sync.WaitGroup
		winch, stopWinch := notifyResizeSignals()
		defer stopWinch()
		if winch != nil && isTTY {
			auxWG.Add(1)
			go func() {
				defer auxWG.Done()
				for {
					select {
					case <-ctx.Done():
						return
					case <-winch:
						if cols, rows, sizeErr := term.GetSize(ttyFD); sizeErr == nil && cols > 0 && rows > 0 {
							rCtx, rCancel := rpcDeadline(ctx)
							_ = w.Call(rCtx, leapmuxv1connect.WorkerTerminalServiceResizeTerminalProcedure, &leapmuxv1.ResizeTerminalRequest{
								TerminalId: got.ID,
								Cols:       uint32(cols),
								Rows:       uint32(rows),
							}, nil)
							rCancel()
						}
					}
				}
			}()
		}

		var feedWG sync.WaitGroup
		items := make(chan screenFeedItem, 32)
		startPos := terminalScreenPositionFromInfo(info)
		startScreenFeed(ctx, &feedWG, workerScreenFeedSource(s.c, got.WorkerID), got.ID, startPos, items)

		// Decouple blocking stdin reads from RPC dispatch so channel shutdown
		// never races with in-flight SendInput calls on w.
		type stdinChunk struct {
			data []byte
			err  error
		}
		stdinCh := make(chan stdinChunk, 16)
		go func() {
			buf := make([]byte, 1024)
			for {
				n, rerr := in.Read(buf)
				if n > 0 {
					select {
					case stdinCh <- stdinChunk{data: bytes.Clone(buf[:n])}:
					case <-ctx.Done():
						return
					}
				}
				if rerr != nil {
					select {
					case stdinCh <- stdinChunk{err: rerr}:
					case <-ctx.Done():
					}
					return
				}
			}
		}()

		defer func() {
			cancel()
			feedWG.Wait()
			auxWG.Wait()
		}()

		for {
			select {
			case <-ctx.Done():
				return nil
			case sc := <-stdinCh:
				if len(sc.data) > 0 {
					chunk := sc.data
					if idx := bytes.IndexByte(chunk, detachByte); idx >= 0 {
						if idx > 0 {
							sendCtx, sendCancel := rpcDeadline(ctx)
							_ = w.Call(sendCtx, leapmuxv1connect.WorkerTerminalServiceSendInputProcedure, &leapmuxv1.SendInputRequest{
								TerminalId: got.ID,
								Data:       chunk[:idx],
							}, nil)
							sendCancel()
						}
						return nil
					}
					sendCtx, sendCancel := rpcDeadline(ctx)
					err := w.Call(sendCtx, leapmuxv1connect.WorkerTerminalServiceSendInputProcedure, &leapmuxv1.SendInputRequest{
						TerminalId: got.ID,
						Data:       chunk,
					}, nil)
					sendCancel()
					if err != nil {
						return err
					}
				}
				if sc.err != nil {
					if sc.err == io.EOF {
						return nil
					}
					return sc.err
				}
			case item := <-items:
				if item.kind != screenFeedEvent {
					return item.err
				}
				if data := item.event.GetData(); data != nil && len(data.GetData()) > 0 {
					if _, err := out.Write(data.GetData()); err != nil {
						return err
					}
				}
				if item.event.GetClosed() != nil {
					return nil
				}
			}
		}
	})
	if chErr != nil {
		runErr = chErr
	}
	return nil, runErr
}

func terminalScreenPositionFromInfo(info *leapmuxv1.TerminalInfo) terminalScreenPosition {
	timeline := info.GetScreenTimeline()
	geometry := timeline.GetBase()
	if changes := timeline.GetChanges(); len(changes) != 0 {
		geometry = changes[len(changes)-1]
	}
	return terminalScreenPosition{
		offset:   info.GetScreenEndOffset(),
		epoch:    geometry.GetEpoch(),
		revision: geometry.GetRevision(),
	}
}

// terminalChunkSanitizer buffers incomplete trailing UTF-8 sequences and
// incomplete ANSI escape sequences across incoming PTY chunks so non-raw
// `terminal read --follow` never splits a rune or leaks a partial CSI/OSC tail.
type terminalChunkSanitizer struct {
	pending []byte
}

func (ts *terminalChunkSanitizer) write(chunk []byte) string {
	data := append(ts.pending, chunk...)
	ts.pending = nil
	if len(data) == 0 {
		return ""
	}
	cut := len(data)
	// Hold back an incomplete trailing 7-bit ESC sequence (CSI, OSC, DCS, APC,
	// PM, SOS, or 3-byte charset designation) or 8-bit C1 sequence.
	if escIdx := findTrailingIncompleteEscape(data); escIdx >= 0 {
		cut = escIdx
	}
	// Hold back an incomplete trailing UTF-8 rune (at most 3 trailing bytes).
	if cut > 0 {
		maxBack := min(3, cut)
		for back := 1; back <= maxBack; back++ {
			b := data[cut-back]
			if utf8.RuneStart(b) {
				if !utf8.FullRune(data[cut-back : cut]) {
					cut -= back
				}
				break
			}
		}
	}
	if cut < len(data) {
		ts.pending = append(ts.pending[:0], data[cut:]...)
	}
	if cut == 0 {
		return ""
	}
	return cli.SanitizeTerminalText(string(data[:cut]))
}

func (ts *terminalChunkSanitizer) flush() string {
	if len(ts.pending) == 0 {
		return ""
	}
	rem := ts.pending
	ts.pending = nil
	return cli.SanitizeTerminalText(string(rem))
}

const maxPendingEscapeBytes = 16 * 1024

func findTrailingIncompleteEscape(data []byte) int {
	start := 0
	if len(data) > maxPendingEscapeBytes {
		start = len(data) - maxPendingEscapeBytes
	}
	for i := len(data) - 1; i >= start; i-- {
		if data[i] == 0x1b {
			if isIncompleteEscape(data[i:]) {
				return i
			}
			return -1
		}
		if data[i] == 0xc2 && i+1 < len(data) {
			switch data[i+1] {
			case 0x9b: // 8-bit C1 CSI
				if isIncompleteCSIBody(data[i+2:]) {
					return i
				}
				return -1
			case 0x90, 0x98, 0x9d, 0x9e, 0x9f: // 8-bit C1 DCS/SOS/OSC/PM/APC
				if isIncompleteStringBody(data[i+2:]) {
					return i
				}
				return -1
			}
		}
	}
	return -1
}

func isIncompleteEscape(seq []byte) bool {
	if len(seq) == 1 {
		return true
	}
	switch seq[1] {
	case '[': // CSI ends with 0x40..0x7E.
		return isIncompleteCSIBody(seq[2:])
	case ']', 'P', '_', '^', 'X': // OSC, DCS, APC, PM, SOS: BEL, ESC \, or 8-bit ST.
		return isIncompleteStringBody(seq[2:])
	case '(', ')', '*', '+', '-', '.', '/': // 3-byte charset designation.
		return len(seq) < 3
	default:
		return false
	}
}

func isIncompleteCSIBody(body []byte) bool {
	for _, b := range body {
		if b >= 0x40 && b <= 0x7e {
			return false
		}
	}
	return true
}

func isIncompleteStringBody(body []byte) bool {
	for i := 0; i < len(body); i++ {
		if body[i] == 0x07 {
			return false
		}
		if body[i] == 0x1b && i+1 < len(body) && body[i+1] == '\\' {
			return false
		}
		if body[i] == 0xc2 && i+1 < len(body) && body[i+1] == 0x9c {
			return false
		}
	}
	return true
}

type terminalFollowChunk struct {
	ID   string `json:"id"`
	Text string `json:"text"`
	Raw  bool   `json:"raw,omitempty"`
}

func (c terminalFollowChunk) TextLine() string { return c.Text }

// followTerminalOutput streams live terminal output after printing the initial
// retained screen, supporting both --raw bytes, plain text chunks, and --json /
// --format ndjson structured chunk events.
func followTerminalOutput(inv *cli.Invocation, s *session, got tabTarget, info *leapmuxv1.TerminalInfo, opts screenReadOptions) error {
	if inv.Printer != nil && inv.Printer.Format == cli.FormatQuiet {
		return cli.Usagef("--quiet cannot be used with --follow").
			WithHint("use --json or --format ndjson for structured streaming, or omit --follow")
	}
	structured := inv.Printer != nil && (inv.Printer.Format == cli.FormatJSON || inv.Printer.Format == cli.FormatNDJSON)
	out := inv.Command.OutOrStdout()
	emitChunk := func(text string) error {
		if text == "" {
			return nil
		}
		if structured {
			return inv.Printer.Event(terminalFollowChunk{ID: got.ID, Text: text, Raw: opts.raw})
		}
		_, err := io.WriteString(out, text)
		return err
	}
	if opts.raw {
		if len(info.GetScreen()) > 0 {
			if structured {
				if err := emitChunk(string(info.GetScreen())); err != nil {
					return err
				}
			} else if _, err := out.Write(info.GetScreen()); err != nil {
				return err
			}
		}
	} else {
		rendered, err := renderScreen(info, opts)
		if err != nil {
			return err
		}
		if structured {
			var buf bytes.Buffer
			if err := rendered.RenderText(&buf); err != nil {
				return err
			}
			if err := emitChunk(buf.String()); err != nil {
				return err
			}
		} else if err := rendered.RenderText(out); err != nil {
			return err
		}
	}
	if info.GetStatus() == leapmuxv1.TerminalStatus_TERMINAL_STATUS_EXITED {
		return nil
	}
	ctx, cancel := context.WithCancel(s.ctx)
	defer cancel()
	var wg sync.WaitGroup
	defer wg.Wait()
	items := make(chan screenFeedItem, 32)
	startPos := terminalScreenPositionFromInfo(info)
	startScreenFeed(ctx, &wg, workerScreenFeedSource(s.c, got.WorkerID), got.ID, startPos, items)
	var sanitizer terminalChunkSanitizer
	for {
		select {
		case <-ctx.Done():
			if !opts.raw {
				_ = emitChunk(sanitizer.flush())
			}
			return nil
		case item := <-items:
			if item.kind != screenFeedEvent {
				if !opts.raw {
					_ = emitChunk(sanitizer.flush())
				}
				return item.err
			}
			if data := item.event.GetData(); data != nil && len(data.GetData()) > 0 {
				chunk := data.GetData()
				if opts.raw {
					if structured {
						if err := emitChunk(string(chunk)); err != nil {
							return err
						}
					} else if _, err := out.Write(chunk); err != nil {
						return err
					}
				} else {
					if err := emitChunk(sanitizer.write(chunk)); err != nil {
						return err
					}
				}
			}
			if item.event.GetClosed() != nil {
				if !opts.raw {
					_ = emitChunk(sanitizer.flush())
				}
				return nil
			}
		}
	}
}

type terminalProcessRow struct {
	TerminalID string `json:"terminal_id" table:"TERMINAL,short"`
	PID        int32  `json:"pid" table:"PID"`
	Name       string `json:"name" table:"PROCESS"`
}

type terminalPSResult struct {
	Processes []terminalProcessRow `json:"processes"`
	Truncated map[string]int32     `json:"truncated_extra,omitempty"`
}

func (r terminalPSResult) QuietLines() []string {
	out := make([]string, 0, len(r.Processes))
	for _, p := range r.Processes {
		out = append(out, fmt.Sprintf("%s\t%d\t%s", p.TerminalID, p.PID, p.Name))
	}
	return out
}

func runTerminalPS(inv *cli.Invocation) (any, error) {
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()

	args := inv.Variadic("terminal")
	all := boolFlag(inv, "all") || (len(args) == 0 && inv.Changed("workspace"))
	byWorker := map[string][]string{}
	if all {
		tabs, err := s.listWorkspaceTabs(leapmuxv1.TabType_TAB_TYPE_TERMINAL)
		if err != nil {
			return nil, err
		}
		for _, t := range tabs {
			byWorker[t.GetWorkerId()] = append(byWorker[t.GetWorkerId()], t.GetTab().GetTabId())
		}
	} else {
		if len(args) == 0 {
			args = []string{""}
		}
		for _, arg := range args {
			got, err := s.resolveTab(resolve.KindTerminal, arg, resolve.Need{WorkerID: true})
			if err != nil {
				return nil, err
			}
			byWorker[got.WorkerID] = append(byWorker[got.WorkerID], got.ID)
		}
	}

	var rows []terminalProcessRow
	truncated := map[string]int32{}
	workerIDs := make([]string, 0, len(byWorker))
	for wk := range byWorker {
		workerIDs = append(workerIDs, wk)
	}
	slices.Sort(workerIDs)
	for _, wk := range workerIDs {
		ids := byWorker[wk]
		var resp leapmuxv1.InspectTerminalProcessesResponse
		if err := callInnerRPC(s.ctx, s.c, wk, leapmuxv1connect.WorkerTerminalServiceInspectTerminalProcessesProcedure, &leapmuxv1.InspectTerminalProcessesRequest{
			TerminalIds: ids,
		}, &resp); err != nil {
			return nil, err
		}
		for _, tp := range resp.GetTerminals() {
			for _, proc := range tp.GetProcesses() {
				name := proc.GetName()
				if name == "" {
					name = "(unknown)"
				}
				rows = append(rows, terminalProcessRow{
					TerminalID: tp.GetTerminalId(),
					PID:        proc.GetPid(),
					Name:       name,
				})
			}
			if extra := tp.GetTotalCount() - int32(len(tp.GetProcesses())); extra > 0 {
				truncated[tp.GetTerminalId()] = extra
			}
		}
	}
	res := terminalPSResult{Processes: rows}
	if len(truncated) > 0 {
		res.Truncated = truncated
	}
	return res, nil
}
