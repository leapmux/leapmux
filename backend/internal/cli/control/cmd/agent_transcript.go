package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
)

func newAgentMessageCommand() *cobra.Command {
	return cli.NewLeaf(cli.Leaf{
		Use:              "message <agent> <seq>",
		Short:            "Fetch a single transcript message by sequence number",
		QuietUnsupported: true,
		Args:             map[string]string{"seq": "Positive message sequence number."},
		Examples: []cli.Example{
			{Command: "leapmux agent message 87a19d5f 42"},
			{Command: "leapmux agent message self 42 --json"},
		},
		Flags: func(fs *pflag.FlagSet) {
			fs.Bool("raw", false, "print raw provider JSON payload in text mode")
			addTargetWorkspaceFlag(fs)
		},
		Complete: completeTargets,
		Run:      runAgentMessage,
	})
}

func runAgentMessage(inv *cli.Invocation) (any, error) {
	seq, err := strconv.ParseInt(strings.TrimSpace(inv.Arg("seq")), 10, 64)
	if err != nil || seq <= 0 {
		return nil, cli.Usagef("<seq> must be a positive integer")
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
	var resp leapmuxv1.GetAgentMessageResponse
	if err := callInnerRPC(s.ctx, s.c, got.WorkerID, leapmuxv1connect.WorkerAgentServiceGetAgentMessageProcedure,
		&leapmuxv1.GetAgentMessageRequest{AgentId: got.ID, Seq: seq, IncludeReplyText: true}, &resp); err != nil {
		return nil, err
	}
	if resp.GetMessage() == nil {
		return nil, cli.Newf(cli.CodeNotFound, "agent %s has no message at seq %d", got.ID, seq)
	}
	return formattedAgentMessage{
		agentMessage: renderAgentMessage(resp.GetMessage()),
		opts: messageFormatOptions{
			raw:  boolFlag(inv, "raw"),
			full: true,
		},
	}, nil
}

func newAgentSpanCommand() *cobra.Command {
	return cli.NewLeaf(cli.Leaf{
		Use:              "span <agent> <span-id>",
		Short:            "Fetch all transcript messages belonging to a tool or subagent span",
		QuietUnsupported: true,
		Args:             map[string]string{"span-id": "Tool or subagent span id (for example, toolu_01...)."},
		Examples: []cli.Example{
			{Command: "leapmux agent span 87a19d5f toolu_01AbCd"},
		},
		Flags: func(fs *pflag.FlagSet) {
			fs.String("session-id", "", "optional provider session id to disambiguate spans across sessions")
			fs.Bool("raw", false, "print raw provider JSON payloads in text mode")
			addTargetWorkspaceFlag(fs)
		},
		Complete: completeTargets,
		Run:      runAgentSpan,
	})
}

func runAgentSpan(inv *cli.Invocation) (any, error) {
	spanID := strings.TrimSpace(inv.Arg("span-id"))
	if spanID == "" {
		return nil, cli.Usagef("<span-id> is empty")
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
	var resp leapmuxv1.GetAgentSpanMessagesResponse
	if err := callInnerRPC(s.ctx, s.c, got.WorkerID, leapmuxv1connect.WorkerAgentServiceGetAgentSpanMessagesProcedure,
		&leapmuxv1.GetAgentSpanMessagesRequest{
			AgentId:          got.ID,
			SpanId:           spanID,
			AgentSessionId:   strings.TrimSpace(stringFlag(inv, "session-id")),
			IncludeReplyText: true,
		}, &resp); err != nil {
		return nil, err
	}
	rows := make([]agentMessage, 0, len(resp.GetMessages()))
	for _, m := range resp.GetMessages() {
		rows = append(rows, renderAgentMessage(m))
	}
	return agentMessageList{
		Messages: rows,
		opts: messageFormatOptions{
			raw:  boolFlag(inv, "raw"),
			full: true,
		},
	}, nil
}

func newAgentMarksCommand() *cobra.Command {
	return cli.NewLeaf(cli.Leaf{
		Use:   "marks [agent]",
		Short: "List transcript jump marks (user prompts, plans, errors) and sequence limits (default: this agent)",
		Examples: []cli.Example{
			{Command: "leapmux agent marks"},
			{Command: "leapmux agent marks 87a19d5f"},
		},
		Flags:    addTargetWorkspaceFlag,
		Complete: completeTargets,
		Run:      runAgentMarks,
	})
}

type agentMarkRow struct {
	Seq  int64  `json:"seq" table:"SEQ"`
	Type string `json:"type" table:"TYPE"`
}

type agentMarksResult struct {
	AgentID string         `json:"agent_id"`
	MinSeq  *int64         `json:"min_seq,omitempty"`
	MaxSeq  *int64         `json:"max_seq,omitempty"`
	Marks   []agentMarkRow `json:"marks"`
}

func (r agentMarksResult) QuietLines() []string {
	out := make([]string, 0, len(r.Marks))
	for _, m := range r.Marks {
		out = append(out, strconv.FormatInt(m.Seq, 10))
	}
	return out
}

func runAgentMarks(inv *cli.Invocation) (any, error) {
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	got, err := s.resolveTab(resolve.KindAgent, inv.Arg("agent"), resolve.Need{WorkerID: true})
	if err != nil {
		return nil, err
	}
	var resp leapmuxv1.ListMessageMarksResponse
	if err := callInnerRPC(s.ctx, s.c, got.WorkerID, leapmuxv1connect.WorkerAgentServiceListMessageMarksProcedure,
		&leapmuxv1.ListMessageMarksRequest{AgentId: got.ID}, &resp); err != nil {
		return nil, err
	}
	rows := make([]agentMarkRow, 0, len(resp.GetMarks()))
	for _, m := range resp.GetMarks() {
		rows = append(rows, agentMarkRow{Seq: m.GetSeq(), Type: enumToken(m.GetType())})
	}
	return agentMarksResult{
		AgentID: got.ID,
		MinSeq:  resp.MinSeq,
		MaxSeq:  resp.MaxSeq,
		Marks:   rows,
	}, nil
}

func newAgentExportCommand() *cobra.Command {
	return cli.NewLeaf(cli.Leaf{
		Use:              "export [agent]",
		Short:            "Export an agent's complete transcript as Markdown, text, or JSON (default: this agent)",
		QuietUnsupported: true,
		Examples: []cli.Example{
			{Command: "leapmux agent export 87a19d5f --output transcript.md"},
			{Command: "leapmux agent export self --style text"},
		},
		Flags: func(fs *pflag.FlagSet) {
			fs.String("style", "markdown", "export style")
			cli.SetEnum(fs, "style", "markdown", "text", "json")
			fs.StringP("output", "o", "", "write exported transcript to this file instead of stdout")
			addTargetWorkspaceFlag(fs)
		},
		Complete: completeTargets,
		Run:      runAgentExport,
	})
}

type agentExportResult struct {
	AgentID  string `json:"agent_id"`
	Messages int    `json:"messages"`
	Output   string `json:"output,omitempty"`
}

type exportStreamWriter struct {
	bw      *bufio.Writer
	style   string
	count   int
	started bool
}

func (w *exportStreamWriter) ensureHeader() error {
	if w.started {
		return nil
	}
	w.started = true
	switch w.style {
	case "json":
		_, err := w.bw.WriteString("{\n  \"messages\": [")
		return err
	case "envelope-json":
		_, err := w.bw.WriteString(`{"data":{"messages":[`)
		return err
	default:
		return nil
	}
}

func (w *exportStreamWriter) writeRow(msg agentMessage) error {
	if err := w.ensureHeader(); err != nil {
		return err
	}
	switch w.style {
	case "json":
		raw, err := json.MarshalIndent(msg, "    ", "  ")
		if err != nil {
			return err
		}
		sep := "\n    "
		if w.count > 0 {
			sep = ",\n    "
		}
		if _, err := w.bw.WriteString(sep); err != nil {
			return err
		}
		if _, err := w.bw.Write(raw); err != nil {
			return err
		}
	case "envelope-json":
		raw, err := json.Marshal(msg)
		if err != nil {
			return err
		}
		if w.count > 0 {
			if err := w.bw.WriteByte(','); err != nil {
				return err
			}
		}
		if _, err := w.bw.Write(raw); err != nil {
			return err
		}
	default:
		if w.style == "markdown" && w.count > 0 {
			if err := w.bw.WriteByte('\n'); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintln(w.bw, cli.SanitizeTerminalText(msg.formatText(messageFormatOptions{full: true, markdown: w.style == "markdown"}))); err != nil {
			return err
		}
	}
	w.count++
	return nil
}

func (w *exportStreamWriter) finish() error {
	if err := w.ensureHeader(); err != nil {
		return err
	}
	switch w.style {
	case "json":
		suffix := "]\n}\n"
		if w.count > 0 {
			suffix = "\n  ]\n}\n"
		}
		if _, err := w.bw.WriteString(suffix); err != nil {
			return err
		}
	case "envelope-json":
		if _, err := w.bw.WriteString("]}}\n"); err != nil {
			return err
		}
	}
	return w.bw.Flush()
}

func runAgentExport(inv *cli.Invocation) (any, error) {
	style := strings.ToLower(strings.TrimSpace(stringFlag(inv, "style")))
	switch style {
	case "markdown", "text", "json":
	default:
		return nil, cli.Usagef("--style must be one of markdown, text, json; got %q", style)
	}
	outPath := strings.TrimSpace(stringFlag(inv, "output"))
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	got, err := s.resolveTab(resolve.KindAgent, inv.Arg("agent"), resolve.Need{WorkerID: true})
	if err != nil {
		return nil, err
	}

	var (
		tmpFile       *os.File
		tmpPath       string
		cleanupTmp    bool
		streamWriter  *exportStreamWriter
		projectedRows []agentMessage
		emitRow       func(agentMessage) error
	)
	if outPath != "" {
		tmpFile, err = os.CreateTemp(filepath.Dir(outPath), ".leapmux-export-*")
		if err != nil {
			return nil, cli.Wrap(cli.CodeInternal, err, "write --output")
		}
		tmpPath = tmpFile.Name()
		cleanupTmp = true
		defer func() {
			if cleanupTmp {
				_ = tmpFile.Close()
				_ = os.Remove(tmpPath)
			}
		}()
		streamWriter = &exportStreamWriter{bw: bufio.NewWriter(tmpFile), style: style}
		emitRow = func(msg agentMessage) error {
			if err := streamWriter.writeRow(msg); err != nil {
				return cli.Wrap(cli.CodeInternal, err, "write --output")
			}
			return nil
		}
	} else if inv.Printer.Format == cli.FormatNDJSON {
		emitRow = func(msg agentMessage) error {
			return inv.Printer.Event(msg)
		}
	} else if inv.Printer.Format == cli.FormatJSON && len(inv.Printer.Fields) > 0 {
		projectedRows = []agentMessage{}
		emitRow = func(msg agentMessage) error {
			projectedRows = append(projectedRows, msg)
			return nil
		}
	} else {
		streamStyle := style
		if inv.Printer.Format == cli.FormatJSON {
			streamStyle = "envelope-json"
		}
		streamWriter = &exportStreamWriter{bw: bufio.NewWriter(inv.Printer.Out), style: streamStyle}
		emitRow = streamWriter.writeRow
	}

	if err := withWorkerChannel(s.ctx, s.c, got.WorkerID, func(w workerCall) error {
		var cursor int64
		for {
			req := &leapmuxv1.ListAgentMessagesRequest{
				AgentId:          got.ID,
				Limit:            contracts.MessagePageLimit,
				IncludeReplyText: true,
			}
			if cursor == 0 {
				req.Anchor = leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_OLDEST
			} else {
				req.Anchor = leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_AFTER
				req.CursorSeq = cursor
			}
			var resp leapmuxv1.ListAgentMessagesResponse
			if err := w.Call(s.ctx, leapmuxv1connect.WorkerAgentServiceListAgentMessagesProcedure, req, &resp); err != nil {
				return err
			}
			before := cursor
			for _, m := range resp.GetMessages() {
				if m.GetSeq() <= cursor {
					continue
				}
				cursor = m.GetSeq()
				if err := emitRow(renderAgentMessage(m)); err != nil {
					return err
				}
			}
			if !resp.GetHasMore() || cursor <= before {
				break
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}

	if outPath != "" {
		if err := streamWriter.finish(); err != nil {
			return nil, cli.Wrap(cli.CodeInternal, err, "write --output")
		}
		if err := tmpFile.Close(); err != nil {
			return nil, cli.Wrap(cli.CodeInternal, err, "write --output")
		}
		if err := os.Rename(tmpPath, outPath); err != nil {
			return nil, cli.Wrap(cli.CodeInternal, err, "write --output")
		}
		cleanupTmp = false
		return agentExportResult{AgentID: got.ID, Messages: streamWriter.count, Output: outPath}, nil
	}
	if projectedRows != nil {
		return agentMessageList{Messages: projectedRows}, nil
	}
	if streamWriter != nil {
		if err := streamWriter.finish(); err != nil {
			return nil, err
		}
	}
	return nil, nil
}
