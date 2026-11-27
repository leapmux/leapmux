package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/quartz"
	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
	"github.com/leapmux/leapmux/internal/cli/control/streamevents"
	"github.com/leapmux/leapmux/internal/util/agentlabels"
	"github.com/leapmux/leapmux/internal/util/backoffutil"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
)

// runAgentMessages prints messages for an agent. Without --follow it
// prints a single page; with --follow it streams new messages until
// ctrl-C using the worker's `WatchEvents` RPC, so a message prints within
// milliseconds of its generation.
func runAgentMessages(inv *cli.Invocation) (any, error) {
	limit, err := inv.Command.Flags().GetInt("limit")
	if err != nil {
		return nil, err
	}
	cursorSeq, err := inv.Command.Flags().GetInt64("cursor-seq")
	if err != nil {
		return nil, err
	}
	follow := boolFlag(inv, "follow")
	opts := messageFormatOptions{
		raw:      boolFlag(inv, "raw"),
		full:     boolFlag(inv, "full"),
		markdown: boolFlag(inv, "markdown"),
	}
	summaryMode := boolFlag(inv, "summary")
	if follow && summaryMode {
		return nil, cli.Usagef("--follow cannot be combined with --summary")
	}
	anchor := stringFlag(inv, "anchor")
	if inv.Changed("anchor") && strings.TrimSpace(anchor) == "" {
		return nil, cli.Usagef("invalid --anchor %q: want latest, oldest, before, or after", anchor)
	}
	req, err := listMessagesPageRequest("", anchor, cursorSeq, limit)
	if err != nil {
		return nil, cli.Wrap(cli.CodeUsage, err, "")
	}
	if err := followSelectorError(follow, req.GetAnchor()); err != nil {
		return nil, cli.Wrap(cli.CodeUsage, err, "")
	}
	agentArg := inv.Arg("agent")
	if (strings.TrimSpace(agentArg) == "" || strings.TrimSpace(agentArg) == resolve.SelfTarget) &&
		(resolve.Listing{}).SelfTabID(resolve.KindAgent) == "" {
		return nil, cli.Usagef(`"self" selects your own agent, and this shell does not run inside a LeapMux agent tab`).
			WithHint("give the agent's id instead")
	}

	s, err := openStreamSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	timed := s.bounded()
	defer timed.Close()
	got, err := timed.resolveTab(resolve.KindAgent, agentArg, resolve.Need{WorkerID: true})
	if err != nil {
		return nil, err
	}
	if err := maybePreflightWorker(timed.ctx, s.c, got.WorkerID); err != nil {
		return nil, err
	}
	req.AgentId = got.ID

	// Page 1: history pulled via ListAgentMessages so the user sees
	// prior context before the live tail starts. WatchEvents can replay the
	// LATEST page too, but ListAgentMessages is shaped for paginated
	// history, capped by contracts.MessagePageLimit. Keep it for the single-page
	// show-history-first behaviour.
	var resp leapmuxv1.ListAgentMessagesResponse
	if err := callInnerRPC(timed.ctx, s.c, got.WorkerID, leapmuxv1connect.WorkerAgentServiceListAgentMessagesProcedure, req, &resp); err != nil {
		return nil, err
	}
	if !follow {
		rows := make([]agentMessage, 0, len(resp.GetMessages()))
		for _, m := range resp.GetMessages() {
			rows = append(rows, renderAgentMessage(m))
		}
		format := cli.FormatText
		if inv.Printer != nil {
			format = inv.Printer.Format
		}
		// Omit summary metadata unless --summary was requested, and never attach
		// sibling list fields (Todos/BackgroundTasks) under row-oriented formats
		// (NDJSON/TSV) so Messages stays the sole projected primary list.
		includeSummary := summaryMode && format != cli.FormatNDJSON && format != cli.FormatTSV
		var (
			goal  *agentGoalView
			todos []agentTodoRow
			tasks []agentTaskRow
		)
		if includeSummary {
			if snap := resp.GetGoal(); snap != nil && snap.GetGoal() != nil {
				view := agentGoalViewOf(got.ID, snap)
				goal = &view
			}
			todos = agentTodoRowsOf(resp.GetTodos())
			tasks = agentTaskRowsOf(resp.GetBackgroundTasks(), false)
		}
		return agentMessageList{
			Messages:        rows,
			HasMore:         resp.GetHasMore(),
			LatestSeq:       resp.LatestSeq,
			Goal:            goal,
			Todos:           todos,
			BackgroundTasks: tasks,
			opts:            opts,
			showSummary:     summaryMode,
		}, nil
	}

	// Follow mode: print page 1, then stream live via WatchEvents. The
	// lineEmitter serializes every write under one mutex so the
	// subscription callback and the reconnect-drain loop can't interleave.
	em := &lineEmitter{printer: inv.Printer, opts: opts}
	for _, m := range resp.GetMessages() {
		if err := em.emitMessage(m); err != nil {
			return nil, err
		}
	}
	cursor := resumeCursorFor(req, resp.GetMessages(), resp.LatestSeq)
	return nil, tailAgentMessages(s.ctx, s.c, got.WorkerID, got.ID, cursor, em)
}

// resumeCursorFor picks the seq the live WatchEvents stream resumes after, given
// the page-1 request, the messages it returned, and the response's authoritative
// latest_seq. The last message shown wins (resume right after it). When page 1 is
// empty, an explicit AFTER resume -- or an INDETERMINATE latest_seq (unset, the worker
// couldn't read the tail; see ListAgentMessagesResponse.latest_seq) -- falls back to
// the request's own --cursor-seq rather than trusting a tail we don't have.
//
// Otherwise (an empty page with a present, positive latest_seq) resume after latest_seq-1,
// NOT latest_seq: the page rows and latest_seq are two non-atomic reads, so an empty
// LATEST page with latest_seq > 0 is the race where the agent's first/only message
// committed BETWEEN them -- resuming after latest_seq would skip that very message.
// Resuming after latest_seq-1 delivers seq >= latest_seq (catching it) while a
// populated agent still avoids the OLDEST full-history drain: latest_seq-1 stays a
// positive resume cursor (drainFetch treats afterSeq <= 0 as "from the oldest
// message"), and only a first-ever message at seq 1 drops to the oldest drain, which
// is then a single message. A present latest_seq == 0 (truly empty agent) stays 0
// ("tail from now"). Pure, so the empty-page fallback is table-testable without a
// live RPC.
func resumeCursorFor(req *leapmuxv1.ListAgentMessagesRequest, messages []*leapmuxv1.AgentChatMessage, latestSeq *int64) int64 {
	if n := len(messages); n > 0 {
		return messages[n-1].GetSeq()
	}
	if latestSeq == nil || req.GetAnchor() == leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_AFTER {
		return req.GetCursorSeq()
	}
	if *latestSeq > 0 {
		return *latestSeq - 1
	}
	return *latestSeq
}

// followSelectorError rejects --follow combined with a backward/historical page
// anchor (oldest or before): paging into history and tailing the live stream
// forward are contradictory, and the old code silently resumed the tail from
// seq 0, replaying the whole history. The forward anchors (latest, after) are
// compatible with --follow, so they are allowed.
func followSelectorError(follow bool, anchor leapmuxv1.MessagePageAnchor) error {
	if !follow {
		return nil
	}
	switch anchor {
	case leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_OLDEST,
		leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_BEFORE:
		return errors.New("--follow cannot be combined with --anchor oldest or before; use latest (the default) or after")
	default:
		return nil
	}
}

// clampInt32 saturates an int to the int32 range so the conversion can't wrap.
// A value above MaxInt32 becomes MaxInt32 and one below MinInt32 becomes
// MinInt32. Both values land outside the worker's valid page-limit range.
func clampInt32(v int) int32 {
	if v > math.MaxInt32 {
		return math.MaxInt32
	}
	if v < math.MinInt32 {
		return math.MinInt32
	}
	return int32(v)
}

// listMessagesPageRequest builds the ListAgentMessages request from the --anchor
// selector (latest|oldest|before|after) and --cursor-seq, mapping one-to-one
// onto MessagePageAnchor. `before`/`after` require a positive --cursor-seq;
// `latest`/`oldest` reject a stray one so a typo fails loudly instead of being
// silently ignored on the wire.
func listMessagesPageRequest(agentID, anchor string, cursorSeq int64, limit int) (*leapmuxv1.ListAgentMessagesRequest, error) {
	// Seqs are positive (assigned MAX(seq)+1 starting at 1).
	if cursorSeq < 0 {
		return nil, errors.New("--cursor-seq must be non-negative")
	}
	// The worker would silently clamp a non-positive --limit to
	// contracts.MessagePageLimit. Reject it loudly so a typo fails the same way
	// --cursor-seq and --anchor do instead of quietly returning a full page.
	if limit <= 0 {
		return nil, errors.New("--limit must be greater than 0")
	}

	req := &leapmuxv1.ListAgentMessagesRequest{
		AgentId: agentID,
		// Clamp to the int32 wire range so a huge --limit can't wrap to a small
		// positive that slips past the worker's cap. For example, int32(2^32+1) is 1.
		// The worker applies the page-limit clamp. The CLI only guards the conversion.
		Limit:            clampInt32(limit),
		IncludeReplyText: true,
	}
	switch strings.ToLower(strings.TrimSpace(anchor)) {
	case "", "latest":
		req.Anchor = leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_LATEST
	case "oldest":
		req.Anchor = leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_OLDEST
	case "before":
		req.Anchor = leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_BEFORE
	case "after":
		req.Anchor = leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_AFTER
	default:
		return nil, fmt.Errorf("invalid --anchor %q: want latest, oldest, before, or after", anchor)
	}

	switch req.Anchor {
	case leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_BEFORE,
		leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_AFTER:
		if cursorSeq == 0 {
			return nil, errors.New("--anchor before/after requires --cursor-seq greater than 0")
		}
		req.CursorSeq = cursorSeq
	default: // LATEST / OLDEST ignore the cursor on the wire.
		if cursorSeq != 0 {
			return nil, errors.New("--cursor-seq is only valid with --anchor before or after")
		}
	}
	return req, nil
}

// messageFormatOptions configures how an agentMessage renders in text mode.
type messageFormatOptions struct {
	raw      bool
	full     bool
	markdown bool
}

// agentMessage is one message of an agent's transcript.
type agentMessage struct {
	ID        string `json:"id"`
	Seq       int64  `json:"seq"`
	CreatedAt string `json:"created_at,omitempty"`
	Source    string `json:"source,omitempty"`
	Provider  string `json:"provider,omitempty"`
	// Sender is the agent tab that sent the message, on the user row of a
	// message from another agent.
	Sender *messageSender `json:"sender,omitempty"`
	// PreviousSeq marks a live reseq broadcast (notification-thread
	// consolidation), which re-emits an already-seen id at a new higher
	// seq. It is a MOVE from that older seq, so a --follow consumer can
	// reconcile by id (update the row at previous_seq) instead of seeing an
	// unexplained duplicate. Only the live broadcast sets it.
	PreviousSeq  int64  `json:"previous_seq,omitempty"`
	Depth        int32  `json:"depth,omitempty"`
	ParentSpanID string `json:"parent_span_id,omitempty"`
	SpanID       string `json:"span_id,omitempty"`
	SpanType     string `json:"span_type,omitempty"`
	SpanColor    int32  `json:"span_color,omitempty"`
	// ReplyText is the Worker's extracted human-readable reply text when present.
	ReplyText string `json:"reply_text,omitempty"`
	// Content is the provider's own payload: parsed JSON, or a string when
	// the payload is not JSON.
	Content any `json:"content,omitempty"`
	// ContentError and ContentRaw report a payload that did not
	// decompress, with its raw bytes, so a caller can still recover it.
	ContentError string `json:"content_error,omitempty"`
	ContentRaw   []byte `json:"content_raw,omitempty"`
	SpanLines    any    `json:"span_lines,omitempty"`
}

// RenderText prints the message in its default text form.
func (m agentMessage) RenderText(w io.Writer) error {
	_, err := fmt.Fprintln(w, cli.SanitizeTerminalText(m.TextLine()))
	return err
}

// QuietLines prints the sequence number of the message.
func (m agentMessage) QuietLines() []string {
	return []string{strconv.FormatInt(m.Seq, 10)}
}

// TextLine prints the message in its default compact text form.
func (m agentMessage) TextLine() string {
	return m.formatText(messageFormatOptions{})
}

// formatText prints the message as "#<seq> <source> [HH:MM:SS] [from <sender>]: <text>"
// (or as a Markdown section when opts.markdown is set). Unless opts.raw is set, it
// prefers Worker-extracted ReplyText before falling back to plain text or compact JSON.
func (m agentMessage) formatText(opts messageFormatOptions) string {
	header := fmt.Sprintf("#%d %s", m.Seq, m.Source)
	if ts := formatMessageTime(m.CreatedAt); ts != "" {
		header += " (" + ts + ")"
	}
	if m.Sender != nil {
		header += " from " + m.Sender.TextValue()
	}
	if m.ContentError != "" {
		errText := "[content decode error: " + oneLine(m.ContentError) + "]"
		if opts.markdown {
			return fmt.Sprintf("### %s\n\n%s", header, errText)
		}
		return fmt.Sprintf("%s: %s", header, truncateMessageText(errText, opts.full))
	}
	if opts.markdown {
		if opts.raw {
			return fmt.Sprintf("### %s\n\n```json\n%s\n```", header, contentSummaryOpts(m.Content, true))
		}
		fullBody := strings.TrimSpace(m.ReplyText)
		if fullBody == "" {
			if s, ok := m.Content.(string); ok {
				fullBody = strings.TrimSpace(s)
			} else {
				fullBody = contentSummaryOpts(m.Content, true)
			}
		}
		return fmt.Sprintf("### %s\n\n%s", header, fullBody)
	}
	var body string
	if opts.raw {
		body = contentSummaryOpts(m.Content, opts.full)
	} else if strings.TrimSpace(m.ReplyText) != "" {
		body = formatMessageText(m.ReplyText, opts.full)
	} else {
		body = contentSummaryOpts(m.Content, opts.full)
	}
	return fmt.Sprintf("%s: %s", header, body)
}

// formattedAgentMessage pairs an agentMessage with its text rendering options
// while keeping JSON, NDJSON, TSV, and quiet representations identical to agentMessage.
type formattedAgentMessage struct {
	agentMessage
	opts messageFormatOptions
}

func (m formattedAgentMessage) TextLine() string { return m.formatText(m.opts) }

func (m formattedAgentMessage) RenderText(w io.Writer) error {
	_, err := fmt.Fprintln(w, cli.SanitizeTerminalText(m.TextLine()))
	return err
}

func formatMessageTime(createdAt string) string {
	if createdAt == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return ""
	}
	return t.Format("15:04:05")
}

// maxSummaryRunes caps the content of a message in its text line.
const maxSummaryRunes = 200

func formatMessageText(text string, full bool) string {
	return truncateMessageText(oneLine(text), full)
}

func truncateMessageText(text string, full bool) string {
	if full {
		return text
	}
	if runes := []rune(text); len(runes) > maxSummaryRunes {
		return string(runes[:maxSummaryRunes-1]) + "…"
	}
	return text
}

// contentSummaryOpts compacts a message payload to one line of JSON (or plain text).
func contentSummaryOpts(content any, full bool) string {
	if content == nil {
		return ""
	}
	if s, ok := content.(string); ok {
		return formatMessageText(s, full)
	}
	raw, err := json.Marshal(content)
	if err != nil {
		return ""
	}
	// json.Marshal already emits compact single-line JSON; do not collapse
	// consecutive spaces inside JSON string literals via oneLine.
	return truncateMessageText(string(raw), full)
}

// agentMessageList is the result of `agent messages` without --follow.
type agentMessageList struct {
	Messages        []agentMessage `json:"messages"`
	HasMore         bool           `json:"has_more,omitempty"`
	LatestSeq       *int64         `json:"latest_seq,omitempty"`
	Goal            *agentGoalView `json:"goal,omitempty"`
	Todos           []agentTodoRow `json:"todos,omitempty"`
	BackgroundTasks []agentTaskRow `json:"background_tasks,omitempty"`
	opts            messageFormatOptions
	showSummary     bool
}

// QuietLines prints the sequence number of each returned message, one per line.
func (l agentMessageList) QuietLines() []string {
	out := make([]string, 0, len(l.Messages))
	for _, m := range l.Messages {
		out = append(out, strconv.FormatInt(m.Seq, 10))
	}
	return out
}

// RenderText prints one formatted message per entry (or separated Markdown sections),
// plus an optional goal/todos/tasks summary footer when --summary is set.
func (l agentMessageList) RenderText(w io.Writer) error {
	for i, m := range l.Messages {
		if l.opts.markdown && i > 0 {
			if _, err := fmt.Fprintln(w); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintln(w, cli.SanitizeTerminalText(m.formatText(l.opts))); err != nil {
			return err
		}
	}
	if !l.showSummary {
		return nil
	}
	var summary []string
	if l.Goal != nil && l.Goal.Objective != "" {
		line := "goal: " + cli.SanitizeSingleLine(l.Goal.Objective)
		if l.Goal.Status != "" {
			line += " [" + cli.SanitizeSingleLine(l.Goal.Status) + "]"
		}
		summary = append(summary, line)
	}
	if len(l.Todos) > 0 {
		done := 0
		for _, td := range l.Todos {
			if td.Status == "completed" {
				done++
			}
		}
		summary = append(summary, fmt.Sprintf("todos: %d/%d completed", done, len(l.Todos)))
	}
	if len(l.BackgroundTasks) > 0 {
		running := 0
		for _, tk := range l.BackgroundTasks {
			if tk.Status == "running" || tk.Status == "pending" {
				running++
			}
		}
		summary = append(summary, fmt.Sprintf("background tasks: %d active (%d total)", running, len(l.BackgroundTasks)))
	}
	if len(summary) > 0 {
		if len(l.Messages) > 0 {
			if _, err := fmt.Fprintln(w); err != nil {
				return err
			}
		}
		for _, line := range summary {
			if _, err := fmt.Fprintln(w, line); err != nil {
				return err
			}
		}
	}
	return nil
}

// renderAgentMessage flattens an AgentChatMessage: the zstd-compressed
// `content` payload is decompressed and parsed as JSON (or kept as a string
// when the payload isn't JSON), and the `span_lines` proto field — which on
// the wire is a JSON-encoded string — is parsed into structured JSON. The
// `content_compression` field is dropped because it no longer describes
// what's in `content` after decompression.
func renderAgentMessage(m *leapmuxv1.AgentChatMessage) agentMessage {
	out := agentMessage{
		ID:           m.GetId(),
		Seq:          m.GetSeq(),
		CreatedAt:    m.GetCreatedAt(),
		Source:       enumToken(m.GetSource()),
		Provider:     agentlabels.CLIAlias(m.GetAgentProvider()),
		Sender:       messageSenderOfRow(m),
		PreviousSeq:  m.GetPreviousSeq(),
		Depth:        m.GetDepth(),
		ParentSpanID: m.GetParentSpanId(),
		SpanID:       m.GetSpanId(),
		SpanType:     m.GetSpanType(),
		SpanColor:    m.GetSpanColor(),
		ReplyText:    m.GetReplyText(),
	}
	if raw := m.GetContent(); len(raw) > 0 {
		decoded, err := msgcodec.Decompress(raw, m.GetContentCompression())
		if err != nil {
			out.ContentError = err.Error()
			out.ContentRaw = raw
		} else if parsed, ok := decodeJSON(decoded); ok {
			out.Content = parsed
		} else {
			// Payload isn't JSON — fall back to a string so the
			// caller still gets something legible. This covers
			// providers that emit plain-text deltas or markers.
			out.Content = string(decoded)
		}
	}
	if sl := m.GetSpanLines(); sl != "" {
		if parsed, ok := decodeJSON([]byte(sl)); ok {
			out.SpanLines = parsed
		} else {
			// Worker shipped a non-JSON span_lines value (older
			// snapshots, partial migration). Keep it as a string
			// rather than dropping it on the floor.
			out.SpanLines = sl
		}
	}
	return out
}

// decodeJSON parses one JSON value and preserves the original number tokens.
// Content and span_lines fall back to the original text on a parse failure.
func decodeJSON(data []byte) (any, bool) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var v any
	if err := decoder.Decode(&v); err != nil {
		return nil, false
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, false
	}
	return v, true
}

// reconnectBackoff is the capped exponential backoff the follower loop applies
// between WatchEvents reconnects. It is ACTIVITY-based: a session that delivered
// at least one event did real work, so the next reconnect resets to the floor
// instead of inheriting the backoff an earlier flap had grown; a session that
// delivered nothing keeps the backoff climbing toward the cap. This mirrors the
// frontend resetting its backoff on stream activity -- a session that streamed
// real data is healthy even if it dropped before outliving the max backoff,
// where the old duration-only rule kept reconnect latency pinned high through a
// run of sub-maxBackoff flaps.
type reconnectBackoff struct {
	inner *backoffutil.Backoff
	// pending is the wait the NEXT afterSession returns. It exists because this
	// loop's contract is "wait the current value, then grow", whereas
	// Backoff.Next grows as it returns -- so the first value has to be taken
	// eagerly and each later call returns what the previous one produced.
	pending time.Duration
}

func newReconnectBackoff(initial, maxInterval time.Duration) reconnectBackoff {
	// Deterministic (no jitter): the follower is one process reconnecting to
	// its own worker, not a fleet, and the sequence is asserted directly.
	b := backoffutil.NewBackoff(initial, maxInterval, 0)
	return reconnectBackoff{inner: b, pending: b.Next()}
}

// afterSession returns how long to wait before the next reconnect and advances
// the backoff: reset to the floor when the just-ended session delivered an
// event, otherwise wait the current value and double it (capped at max). The
// wait uses the post-reset value, then grows -- matching the prior loop's
// "reset, wait, double" order.
//
// The cap is the shared helper's now, which is the point: the clamp this used
// to do by hand existed because `cur` overshoots `max` whenever `max` is not a
// power-of-two multiple of the floor (cur=4s, max=5s -> 8s).
func (b *reconnectBackoff) afterSession(deliveredEvent bool) time.Duration {
	if deliveredEvent {
		b.inner.Reset()
		b.pending = b.inner.Next()
	}
	wait := b.pending
	b.pending = b.inner.Next()
	return wait
}

// messagePager fetches one ascending page of messages with seq > afterSeq and
// reports whether more remain past it. Injected so the reconnect drain loop is
// unit-testable without a live worker RPC.
type messagePager func(ctx context.Context, afterSeq int64) (msgs []*leapmuxv1.AgentChatMessage, hasMore bool, err error)
type messageEmitter func(*leapmuxv1.AgentChatMessage) error

// drainBacklog forwards every message between the cursor and the live tail that
// the capped WatchEvents replay (at most contracts.MessagePageLimit per reconnect) would
// otherwise skip. It pages forward from the cursor until caught up, emitting each
// message and advancing the cursor so the subsequent subscription replay only has
// to cover the small remaining gap.
//
// Best-effort: a fetch error or ctx cancellation returns early and lets the live
// subscription take over. Each row advances the cursor atomically BEFORE emission,
// matching Subscription.dispatch, so a late WatchEvents frame for the same seq
// cannot race the drain and be forwarded twice. If emission fails, the error is
// returned and the follow command stops instead of reconnecting past data stdout
// never accepted. The read cursor advances over retracted rows in a stale
// page without moving the delivery cursor. A page that fails to advance the
// read cursor ends the drain even if it reports more rows.
//
// Returns whether it emitted any message, so the reconnect loop can count a drain
// that forwarded real history as session activity: a transport that serves the
// ListAgentMessages drain but immediately drops the live subscription (delivering no
// frame of its own) still did real work, and the backoff should reset to the floor
// rather than climbing toward the cap as if the session were a pure flap.
func drainBacklog(ctx context.Context, agentID string, cursor *streamevents.AgentCursor,
	fetch messagePager, emit messageEmitter,
) (bool, error) {
	emitted := false
	readThrough := cursor.Get(agentID)
	for {
		if ctx.Err() != nil {
			return emitted, nil
		}
		before := max(readThrough, cursor.Get(agentID))
		msgs, hasMore, err := fetch(ctx, before)
		if err != nil || len(msgs) == 0 {
			return emitted, nil
		}
		readThrough = before
		for _, m := range msgs {
			// Only persisted rows (seq >= 0) advance the cursor; the fetch returns
			// only persisted rows, but the guard mirrors the subscription dedup.
			if seq := m.GetSeq(); seq >= 0 {
				readThrough = max(readThrough, seq)
				if seq <= before || !cursor.Advance(agentID, seq) {
					continue
				}
			}
			if err := emit(m); err != nil {
				return emitted, err
			}
			emitted = true
		}
		if !hasMore || readThrough <= before {
			return emitted, nil
		}
	}
}

// agentMessagesReconnectTimerTag identifies the reconnect timer of
// `agent messages --follow` to a test clock.
const agentMessagesReconnectTimerTag = "agent-messages-reconnect"

// drainMessagesRequest builds the ListAgentMessagesRequest for one backlog
// drain page, requesting Worker-extracted ReplyText for both initial OLDEST
// drains and AFTER-cursor catch-up pages.
func drainMessagesRequest(agentID string, afterSeq int64) *leapmuxv1.ListAgentMessagesRequest {
	req := &leapmuxv1.ListAgentMessagesRequest{
		AgentId:          agentID,
		Limit:            contracts.MessagePageLimit,
		IncludeReplyText: true,
	}
	if streamevents.IsResumeCursor(afterSeq) {
		req.Anchor = leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_AFTER
		req.CursorSeq = afterSeq
	} else {
		req.Anchor = leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_OLDEST
	}
	return req
}

// tailAgentMessages streams `WatchEvents` for a single agent and
// prints each `AgentChatMessage` as one stream event. On
// transport disconnect it reconnects with capped exponential backoff,
// resuming from the latest `seq` it observed. Before each RECONNECT it
// drains any backlog the capped replay would skip (see drainBacklog), so
// messages generated during a long disconnect aren't lost.
//
// Each line contains a rendered chat message or the sequence numbers
// removed by a live retraction.
func tailAgentMessages(ctx context.Context, c control.Peer, workerID, agentID string,
	startSeq int64, em *lineEmitter,
) error {
	// Reconnect backlog drain: share one E2EE worker channel across all pages of
	// a drainBacklog run, paging ListAgentMessages forward from the cursor. A
	// still-zero cursor (no message seen yet) drains from the very beginning via
	// the OLDEST anchor; once the cursor has advanced, page forward via AFTER.
	withDrain := func(ctx context.Context, run func(messagePager) error) error {
		if ctx.Err() != nil {
			return nil
		}
		openCtx, cancelOpen := rpcDeadline(ctx)
		defer cancelOpen()
		var runErr error
		_ = withWorkerChannel(openCtx, c, workerID, func(w workerCall) error {
			fetch := func(ctx context.Context, afterSeq int64) ([]*leapmuxv1.AgentChatMessage, bool, error) {
				callCtx, cancelCall := rpcDeadline(ctx)
				defer cancelCall()
				req := drainMessagesRequest(agentID, afterSeq)
				var resp leapmuxv1.ListAgentMessagesResponse
				if err := w.Call(callCtx, leapmuxv1connect.WorkerAgentServiceListAgentMessagesProcedure, req, &resp); err != nil {
					return nil, false, err
				}
				return resp.GetMessages(), resp.GetHasMore(), nil
			}
			runErr = run(fetch)
			return nil
		})
		return runErr
	}
	openTransport := func(ctx context.Context) (*workerWatchTransport, error) {
		return newWorkerWatchTransport(ctx, c, workerID)
	}
	return tailAgentMessagesWith(ctx, agentID, startSeq, openTransport, withDrain, em, quartz.NewReal())
}

func tailAgentMessagesWith(ctx context.Context, agentID string, startSeq int64,
	openTransport func(context.Context) (*workerWatchTransport, error),
	withDrain func(context.Context, func(messagePager) error) error,
	em *lineEmitter, clock quartz.Clock,
) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	cursor := streamevents.NewAgentCursor()
	cursor.Track(agentID, startSeq)
	// terminals cursor stays empty — we don't subscribe terminals here.
	terminals := streamevents.NewTerminalCursor()

	// Track whether this subscription delivered a record before reconnecting.
	var delivered atomic.Bool
	onAgent := func(ae *leapmuxv1.AgentEvent) {
		emitted, err := em.emitFollowEvent(ae)
		if err != nil {
			cancel()
			return
		}
		if emitted {
			delivered.Store(true)
		}
	}
	onCursorReset := func(_ string) { /* not relevant for agent-only follow */ }

	var (
		transport *workerWatchTransport
		sub       *streamevents.Subscription
	)
	closeCurrent := func() {
		if sub != nil {
			sub.Cancel()
			sub = nil
		}
		if transport != nil {
			transport.close()
			transport = nil
		}
	}
	defer closeCurrent()

	var err error
	transport, err = openTransport(ctx)
	if err != nil {
		return inBandError(err)
	}
	sub = streamevents.NewSubscription(transport.transport, cursor, terminals, onAgent, nil, onCursorReset)

	// Reconnect loop. Each iteration drains any missed backlog, opens a fresh
	// transport when the prior stream ended, subscribes using the latest cursor
	// value, and waits for the stream to end. Permanent refusals return
	// immediately instead of retrying forever.
	const (
		initialBackoff = 250 * time.Millisecond
		maxBackoff     = 8 * time.Second
	)
	backoff := newReconnectBackoff(initialBackoff, maxBackoff)
	waitBackoff := func(didWork bool) bool {
		timer := clock.NewTimer(backoff.afterSession(didWork), agentMessagesReconnectTimerTag)
		select {
		case <-ctx.Done():
			timer.Stop(agentMessagesReconnectTimerTag)
			return false
		case <-timer.C:
			return true
		}
	}
	for {
		// Before each subscription, bridge the gap that the capped WatchEvents replay
		// cannot cover. The replay sends at most contracts.MessagePageLimit messages.
		// ListAgentMessages pages forward from the cursor. On a reconnect, it fills the
		// disconnect gap. On the first connection, it fills messages created between the
		// page 1 snapshot and watcher registration. Without the drain, a first-connect
		// burst past the cap is lost silently: the replay keeps only the newest page,
		// and the live stream resumes from the newest message. The drain uses AFTER, so
		// it does not emit a page 1 message again. A failed fetch returns early. The
		// next reconnect retries from the last message that the command emitted.
		var drained bool
		drainErr := withDrain(ctx, func(fetch messagePager) error {
			var err error
			drained, err = drainBacklog(ctx, agentID, cursor, fetch, em.emitMessage)
			return err
		})
		if drainErr != nil {
			return drainErr
		}
		if ctx.Err() != nil {
			return em.Err()
		}
		if transport == nil {
			transport, err = openTransport(ctx)
			if err != nil {
				if ctx.Err() != nil {
					return em.Err()
				}
				err = inBandError(err)
				if refusedForGood(err) {
					return err
				}
				_ = em.printer.Notice("connecting to agent %s failed, retrying: %v", agentID, err)
				if !waitBackoff(drained) {
					return em.Err()
				}
				continue
			}
			sub = streamevents.NewSubscription(transport.transport, cursor, terminals, onAgent, nil, onCursorReset)
		}
		// A still-zero cursor (page-1 was empty and not an explicit AFTER
		// resume) subscribes fresh (LATEST); an advanced cursor resumes
		// AFTER_CURSOR. AgentWatchEntry owns that mapping.
		entry := streamevents.AgentWatchEntry(agentID, cursor.Get(agentID))
		entry.IncludeReplyText = true
		req := &leapmuxv1.WatchEventsRequest{
			Agents: []*leapmuxv1.WatchAgentEntry{entry},
		}
		// Reset before the session opens. For the in-process transport the prior
		// session has fully drained by sub.Done() (its onAgent runs on the same
		// goroutine that closes done), so no late frame can flip this. The channel
		// transport delivers via a separate demux goroutine, so in a narrow teardown
		// window a late frame could set it just after this reset -- harmless: the
		// only consequence is the next reconnect backoff starting from its floor for
		// what was actually a flap.
		delivered.Store(false)
		if err := sub.Update(ctx, req); err != nil {
			if ctx.Err() != nil {
				return em.Err()
			}
			// ErrSubscriptionClosed means the Subscription was Cancelled (the
			// process is shutting down via the deferred Cancel); it is not a
			// subscribe failure and must not surface to the user as one.
			if errors.Is(err, streamevents.ErrSubscriptionClosed) {
				return em.Err()
			}
			err = inBandError(err)
			if refusedForGood(err) {
				return err
			}
			// A failed subscribe is retried below on a fresh transport, so it is a
			// notice on stderr rather than the command's failure.
			_ = em.printer.Notice("subscribing to agent %s failed, retrying: %v", agentID, err)
			closeCurrent()
		} else {
			// Block until the subscription's transport ends
			// (channel closed, RPC error, ctx cancelled).
			select {
			case <-ctx.Done():
				return em.Err()
			case <-sub.Done():
				endErr := inBandError(sub.EndErr())
				if refusedForGood(endErr) {
					return endErr
				}
				if endErr != nil {
					_ = em.printer.Notice("subscription to agent %s ended, reconnecting: %v", agentID, endErr)
				}
				// Tear down the ended subscription and transport so the next
				// iteration opens a fresh channel instead of retrying on a dead one.
				closeCurrent()
			}
		}
		if ctx.Err() != nil {
			return em.Err()
		}
		// Brief backoff before reconnecting -- reset to the floor if this cycle did
		// real work, else keep climbing. Real work is either a live frame this session
		// delivered OR a backlog the pre-subscribe drain forwarded: a transport that
		// drains history but drops the subscription before any frame is still moving
		// messages, so it shouldn't be treated as a flap.
		if !waitBackoff(delivered.Load() || drained) {
			return em.Err()
		}
	}
}

// workerWatchTransport packages the per-mode plumbing of a WatchEvents
// stream to one Worker, so `agent messages --follow`, `agent wait` and
// `agent send --wait` need not know whether they run over an E2EE channel
// or the local IPC socket.
type workerWatchTransport struct {
	transport streamevents.Transport
	caller    workerCall
	close     func()
}

// Call invokes an inner Worker RPC over the transport's existing connection,
// reusing the open E2EE channel in hub-bound mode instead of performing a
// second handshake.
func (t *workerWatchTransport) Call(ctx context.Context, method string, req, resp proto.Message) error {
	if t == nil {
		return &codedRPCError{Code: "invalid_request", Cause: errors.New("watch transport is nil")}
	}
	return t.caller.Call(ctx, method, req, resp)
}

func newWorkerWatchTransport(ctx context.Context, p control.Peer, workerID string) (*workerWatchTransport, error) {
	switch c := p.(type) {
	case *control.WorkerIPCClient:
		// Local-IPC mode: route via ControlIPCService.StreamInner.
		// workerID may be empty; the router resolves the spawning
		// worker from the bearer scope, mirroring the existing
		// localIPCCallInnerBest behaviour.
		// The STREAM lane: OpenWatchEvents holds the subscription open until
		// the caller cancels, and the unary lane's timeout would end it on a
		// clock rather than at the end of the stream.
		ipc, err := c.ControlIPCStreamService()
		if err != nil {
			return nil, err
		}
		return &workerWatchTransport{
			transport: streamevents.NewLocalIPCTransport(ipc, workerID, slog.Default()),
			caller:    workerCall{ipc: c, workerID: workerID},
			close:     func() {},
		}, nil
	case *control.HubClient:
		openCtx, cancelOpen := rpcDeadline(ctx)
		ch, err := openHubE2EEChannel(openCtx, context.WithoutCancel(ctx), c, workerID)
		cancelOpen()
		if err != nil {
			return nil, err
		}
		return &workerWatchTransport{
			transport: streamevents.NewChannelTransport(ch, slog.Default()),
			caller:    workerCall{hub: c, workerID: workerID, ch: ch},
			close: func() {
				ch.Close()
			},
		}, nil
	default:
		return nil, fmt.Errorf("unsupported control peer %T", p)
	}
}

// lineEmitter serializes stream writes under one mutex, so the concurrent
// emitters in --follow mode (the subscription callback and the
// reconnect-drain loop) can't interleave bytes mid-line. Bundling the
// printer and the mutex together means a caller can't write without holding
// the lock -- the locking discipline stops being every call site's
// responsibility.
type lineEmitter struct {
	mu      sync.Mutex
	printer *cli.Printer
	opts    messageFormatOptions
	err     error
}

// emit prints one stream event under the lock.
func (e *lineEmitter) emit(v any) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.err != nil {
		return e.err
	}
	if err := e.printer.Event(v); err != nil {
		e.err = err
		return err
	}
	return nil
}

// emitMessage renders and emits one chat message with the emitter's text options.
func (e *lineEmitter) emitMessage(m *leapmuxv1.AgentChatMessage) error {
	return e.emit(formattedAgentMessage{
		agentMessage: renderAgentMessage(m),
		opts:         e.opts,
	})
}

// emitFollowEvent reports whether the event produces a stream record.
func (e *lineEmitter) emitFollowEvent(event *leapmuxv1.AgentEvent) (bool, error) {
	if message := event.GetAgentMessage(); message != nil {
		return true, e.emitMessage(message)
	}
	if removed := event.GetMessagesRetracted().GetSeqs(); len(removed) > 0 {
		return true, e.emit(map[string]any{
			"type": "messages_retracted",
			"seqs": removed,
		})
	}
	return false, nil
}

func (e *lineEmitter) Err() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.err
}
