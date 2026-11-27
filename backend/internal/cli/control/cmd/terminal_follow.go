package cmd

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"sync"
	"unicode/utf8"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
)

type terminalFollowChunk struct {
	ID         string `json:"id"`
	Text       string `json:"text,omitempty"`
	DataBase64 string `json:"data_base64,omitempty"`
	Raw        bool   `json:"raw,omitempty"`
}

func (c terminalFollowChunk) TextLine() string { return c.Text }

func terminalStartupFailedError(id, detail string) error {
	if detail != "" {
		return cli.Newf(cli.CodeInternal, "terminal %s failed to start: %s", id, detail)
	}
	return cli.Newf(cli.CodeInternal, "terminal %s failed to start", id)
}

// terminalFeedItemStatus inspects a non-event screenFeedItem for attach and
// follow sessions. A missing terminal ends the session cleanly without
// ExitWaitEnded; forbidden, overflow, and stream failures return CLI errors.
func terminalFeedItemStatus(terminalID string, item screenFeedItem) (done bool, err error) {
	if item.rejected != nil {
		id := item.rejected.GetEntityId()
		if id == "" {
			id = terminalID
		}
		disp, err := classifyWatchRejection("terminal", id, item.rejected.GetReason())
		switch disp {
		case watchRejectionRetry:
			return false, nil
		case watchRejectionNotFound:
			return true, nil
		default:
			return true, err
		}
	}
	if item.overflow {
		return true, cli.Newf(cli.CodeInternal, "terminal %s produced too much screen output before replay finished", terminalID)
	}
	if item.err != nil {
		return true, item.err
	}
	if item.event == nil {
		return true, nil
	}
	return false, nil
}

// followTerminalOutput streams live terminal output after printing the initial
// retained screen, supporting --raw bytes, plain text chunks, and structured
// chunk events (--json, --format ndjson, --format tsv).
func followTerminalOutput(inv *cli.Invocation, s *session, got tabTarget, info *leapmuxv1.TerminalInfo, opts screenReadOptions) error {
	return followTerminalFromSource(inv, s.ctx, workerScreenFeedSource(s.c, got.WorkerID), got.ID, info, opts)
}

func followTerminalFromSource(inv *cli.Invocation, parentCtx context.Context, source screenFeedSource, terminalID string, info *leapmuxv1.TerminalInfo, opts screenReadOptions) (err error) {
	if inv.Printer != nil && inv.Printer.Format == cli.FormatQuiet {
		return cli.Usagef("--quiet cannot be used with --follow").
			WithHint("use --json or --format ndjson for structured streaming, or omit --follow")
	}
	if info.GetStatus() == leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTUP_FAILED {
		return terminalStartupFailedError(terminalID, info.GetStartupError())
	}

	structured := inv.Printer != nil && inv.Printer.Format != cli.FormatText
	out := inv.Command.OutOrStdout()
	if inv.Printer != nil && inv.Printer.Out != nil {
		out = inv.Printer.Out
	}

	emitText := func(text string) error {
		if text == "" {
			return nil
		}
		if structured {
			return inv.Printer.Event(terminalFollowChunk{ID: terminalID, Text: text})
		}
		_, writeErr := io.WriteString(out, text)
		return writeErr
	}
	emitRaw := func(chunk []byte) error {
		if len(chunk) == 0 {
			return nil
		}
		if structured {
			ev := terminalFollowChunk{
				ID:         terminalID,
				DataBase64: base64.StdEncoding.EncodeToString(chunk),
				Raw:        true,
			}
			if utf8.Valid(chunk) {
				ev.Text = string(chunk)
			}
			return inv.Printer.Event(ev)
		}
		_, writeErr := out.Write(chunk)
		return writeErr
	}

	sawInitialScreen := len(info.GetScreen()) > 0
	initialEndOffset := info.GetScreenEndOffset()
	if opts.raw {
		if err := emitRaw(info.GetScreen()); err != nil {
			return err
		}
	} else {
		rendered, err := renderScreen(info, opts)
		if err != nil {
			return err
		}
		var buf bytes.Buffer
		if err := rendered.RenderText(&buf); err != nil {
			return err
		}
		if err := emitText(buf.String()); err != nil {
			return err
		}
	}

	if info.GetStatus() == leapmuxv1.TerminalStatus_TERMINAL_STATUS_EXITED {
		return nil
	}

	ctx, cancel := context.WithCancel(parentCtx)
	var wg sync.WaitGroup
	// Register wg.Wait before cancel so LIFO runs cancel first and unblocks
	// the feed goroutine before waiting.
	defer wg.Wait()
	defer cancel()

	items := make(chan screenFeedItem, 32)
	startTerminalOutputFeed(ctx, &wg, source, terminalID, info.GetScreenEndOffset(), items)

	sanitizer := cli.NewTerminalSanitizer()
	defer func() {
		if !opts.raw {
			if ferr := emitText(sanitizer.Flush()); err == nil && ferr != nil {
				err = ferr
			}
		}
	}()

	initialSnapshotPending := sawInitialScreen
	for {
		select {
		case <-ctx.Done():
			return nil
		case item := <-items:
			if done, feedErr := terminalFeedItemStatus(terminalID, item); done {
				return feedErr
			}
			if data := item.event.GetData(); data != nil {
				chunk := data.GetData()
				if data.GetIsSnapshot() {
					skipDuplicate := initialSnapshotPending && data.GetEndOffset() == initialEndOffset
					initialSnapshotPending = false
					if !skipDuplicate {
						if opts.raw {
							if err := emitRaw(chunk); err != nil {
								return err
							}
						} else {
							if err := emitText(sanitizer.Flush()); err != nil {
								return err
							}
							sanitizer.Reset()
							if err := emitText(sanitizer.Write(chunk)); err != nil {
								return err
							}
						}
					}
				} else {
					if data.GetReplayComplete() || len(chunk) > 0 {
						initialSnapshotPending = false
					}
					if len(chunk) > 0 {
						if opts.raw {
							if err := emitRaw(chunk); err != nil {
								return err
							}
						} else {
							if err := emitText(sanitizer.Write(chunk)); err != nil {
								return err
							}
						}
					}
				}
			}
			if sc := item.event.GetStatusChange(); sc != nil {
				switch sc.GetStatus() {
				case leapmuxv1.TerminalStatus_TERMINAL_STATUS_EXITED:
					return nil
				case leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTUP_FAILED:
					return terminalStartupFailedError(terminalID, sc.GetStartupError())
				default:
				}
			}
			if item.event.GetClosed() != nil {
				return nil
			}
		}
	}
}
