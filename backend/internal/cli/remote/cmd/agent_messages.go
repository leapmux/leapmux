package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli/remote"
	"github.com/leapmux/leapmux/internal/cli/remote/streamevents"
	"github.com/leapmux/leapmux/internal/util/msgcodec"
)

// RunAgentMessages prints messages for an agent. Without --follow it
// prints a single page; with --follow it streams new messages until
// ctrl-C using the worker's `WatchEvents` RPC (Phase 1a of the
// streaming-event migration). The streaming path replaces the
// previous 2-second polling loop, so users see messages within
// milliseconds of generation rather than every other second.
func RunAgentMessages(rawCtx any, args []string) error {
	var anchor string
	var cursorSeq int64
	var follow bool
	var limit int
	return withResolvedAgent(rawCtx, args, agentScaffoldOpts{
		setup: func(fs *flag.FlagSet) {
			// A single page anchor mirrors MessagePageAnchor: latest (default),
			// oldest, before, or after. `before`/`after` page relative to
			// --cursor-seq; `latest`/`oldest` ignore it. One selector instead of
			// three mutually-exclusive flags -- you can't pick two pages at once.
			fs.StringVar(&anchor, "anchor", "latest", "page to fetch: latest, oldest, before, or after")
			fs.Int64Var(&cursorSeq, "cursor-seq", 0, "exclusive seq bound for --anchor before/after")
			fs.IntVar(&limit, "limit", 50, "max messages per page (hub caps at 50)")
			fs.BoolVar(&follow, "follow", false, "tail new messages indefinitely")
		},
		noDeadline: true,
		body: func(ctx context.Context, c *remote.Client, workerID, agentID, workspaceID string) error {
			req, err := listMessagesPageRequest(agentID, anchor, cursorSeq, limit)
			if err != nil {
				return err
			}
			if err := followSelectorError(follow, req.GetAnchor()); err != nil {
				return err
			}

			// Page 1: history pulled via ListAgentMessages so the user sees
			// prior context before the live tail starts. WatchEvents can replay the
			// LATEST page too, but ListAgentMessages is shaped for paginated
			// history (cap = 50/page), so we keep it for the single-page
			// show-history-first behaviour.
			var resp leapmuxv1.ListAgentMessagesResponse
			if err := callInnerRPC(ctx, c, workerID, "ListAgentMessages", req, &resp); err != nil {
				return err
			}
			if !follow {
				rendered := make([]map[string]any, 0, len(resp.GetMessages()))
				for _, m := range resp.GetMessages() {
					rendered = append(rendered, renderAgentMessage(m))
				}
				return remote.EmitData(rendered)
			}

			// Follow mode: emit page-1 then stream live via WatchEvents. The
			// lineEmitter serializes every JSON-line write under one mutex so the
			// subscription callback and the reconnect-drain loop can't interleave.
			em := &lineEmitter{enc: json.NewEncoder(remote.Out)}
			for _, m := range resp.GetMessages() {
				if err := em.emit(renderAgentMessage(m)); err != nil {
					return err
				}
			}
			cursor := resumeCursorFor(req, resp.GetMessages(), resp.LatestSeq)

			return tailAgentMessages(ctx, c, workerID, agentID, workspaceID, cursor, em)
		},
	})
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
// MinInt32; both land outside the hub's [1,50] window and are clamped there.
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
	// A non-positive --limit would be silently clamped to the hub default (50);
	// reject it loudly so a typo fails the same way --cursor-seq and --anchor do
	// instead of quietly returning a full page.
	if limit <= 0 {
		return nil, errors.New("--limit must be greater than 0")
	}

	req := &leapmuxv1.ListAgentMessagesRequest{
		AgentId: agentID,
		// Clamp to the int32 wire range so a huge --limit can't wrap to a small
		// positive that slips past the hub's <=50 cap (e.g. int32(2^32+1)==1).
		// The hub does the real [1,50] clamp; the CLI only guards the conversion.
		Limit: clampInt32(limit),
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

// renderAgentMessage flattens an AgentChatMessage into a JSON-friendly
// map: the zstd-compressed `content` payload is decompressed and
// parsed as JSON (or surfaced as a string when the payload isn't JSON),
// and the `span_lines` proto field — which on the wire is a
// JSON-encoded string — is parsed into structured JSON. The
// `content_compression` field is dropped because it no longer
// describes what's in `content` after decompression.
//
// Non-string fields with proto3 zero values are omitted so the
// rendered output matches what `json.Encode` would produce for the
// proto struct, minus the encoded-blob fields the helper rewrites.
func renderAgentMessage(m *leapmuxv1.AgentChatMessage) map[string]any {
	out := map[string]any{
		"id":         m.GetId(),
		"seq":        m.GetSeq(),
		"created_at": m.GetCreatedAt(),
	}
	if name := messageSourceName(m.GetSource()); name != "" {
		out["source"] = name
	}
	if name := agentProviderName(m.GetAgentProvider()); name != "" {
		out["agent_provider"] = name
	}
	if de := m.GetDeliveryError(); de != "" {
		out["delivery_error"] = de
	}
	// A live reseq broadcast (notification-thread consolidation) re-emits an
	// already-seen id at a new higher seq. previous_seq marks it as a MOVE from that
	// older seq, so a --follow consumer can reconcile by id (update the row at
	// previous_seq) instead of seeing an unexplained duplicate. Only set on the live
	// broadcast; a single-page / replayed row carries 0 and the field is omitted.
	if ps := m.GetPreviousSeq(); ps != 0 {
		out["previous_seq"] = ps
	}
	if d := m.GetDepth(); d != 0 {
		out["depth"] = d
	}
	if pid := m.GetParentSpanId(); pid != "" {
		out["parent_span_id"] = pid
	}
	if sid := m.GetSpanId(); sid != "" {
		out["span_id"] = sid
	}
	if st := m.GetSpanType(); st != "" {
		out["span_type"] = st
	}
	if sc := m.GetSpanColor(); sc != 0 {
		out["span_color"] = sc
	}
	if raw := m.GetContent(); len(raw) > 0 {
		decoded, err := msgcodec.Decompress(raw, m.GetContentCompression())
		if err != nil {
			// Decompression failure surfaces as both an error
			// indicator and the raw bytes so callers can still
			// recover the payload manually.
			out["content_error"] = err.Error()
			out["content_raw"] = raw
		} else if parsed, ok := decodeJSON(decoded); ok {
			out["content"] = parsed
		} else {
			// Payload isn't JSON — fall back to a string so the
			// caller still gets something legible. This covers
			// providers that emit plain-text deltas or markers.
			out["content"] = string(decoded)
		}
	}
	if sl := m.GetSpanLines(); sl != "" {
		if parsed, ok := decodeJSON([]byte(sl)); ok {
			out["span_lines"] = parsed
		} else {
			// Worker shipped a non-JSON span_lines value (older
			// snapshots, partial migration). Keep it as a string
			// rather than dropping it on the floor.
			out["span_lines"] = sl
		}
	}
	return out
}

// decodeJSON returns the parsed JSON value when data is well-formed
// JSON, otherwise (nil, false). Centralised so the renderer's two
// JSON-ish fields (content, span_lines) share fallback semantics.
func decodeJSON(data []byte) (any, bool) {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
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
	cur, initial, max time.Duration
}

func newReconnectBackoff(initial, max time.Duration) reconnectBackoff {
	return reconnectBackoff{cur: initial, initial: initial, max: max}
}

// afterSession returns how long to wait before the next reconnect and advances
// the backoff: reset to the floor when the just-ended session delivered an
// event, otherwise wait the current value and double it (capped at max). The
// wait uses the post-reset value, then grows -- matching the prior loop's
// "reset, wait, double" order.
func (b *reconnectBackoff) afterSession(deliveredEvent bool) time.Duration {
	if deliveredEvent {
		b.cur = b.initial
	}
	wait := b.cur
	if b.cur < b.max {
		// Clamp after doubling: `cur` can overshoot `max` when `max` is not a
		// power-of-two multiple of `initial` (e.g. cur=4s, max=5s -> 8s), and the
		// next afterSession would then return a wait ABOVE the documented cap.
		b.cur *= 2
		if b.cur > b.max {
			b.cur = b.max
		}
	}
	return wait
}

// followDrainPageLimit bounds each backlog-drain page. The hub clamps the page
// size to 50 regardless; matching it keeps one page to one round-trip.
const followDrainPageLimit = 50

// messagePager fetches one ascending page of messages with seq > afterSeq and
// reports whether more remain past it. Injected so the reconnect drain loop is
// unit-testable without a live worker RPC.
type messagePager func(ctx context.Context, afterSeq int64) (msgs []*leapmuxv1.AgentChatMessage, hasMore bool, err error)
type messageEmitter func(*leapmuxv1.AgentChatMessage) error

// drainBacklog forwards every message between the cursor and the live tail that
// the capped WatchEvents replay (<= maxMessagePageLimit per reconnect) would
// otherwise skip. It pages forward from the cursor until caught up, emitting each
// message and advancing the cursor so the subsequent subscription replay only has
// to cover the small remaining gap.
//
// Best-effort: a fetch error or ctx cancellation returns early and lets the live
// subscription take over. Each row advances the cursor atomically BEFORE emission,
// matching Subscription.dispatch, so a late WatchEvents frame for the same seq
// cannot race the drain and be forwarded twice. If emission fails, the error is
// returned and the follow command stops instead of reconnecting past data stdout
// never accepted. The `before` re-read guards against a server returning rows at
// or below the bound: without it a hasMore=true page that fails to advance the
// cursor would spin forever.
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
	for {
		if ctx.Err() != nil {
			return emitted, nil
		}
		before := cursor.Get(agentID)
		msgs, hasMore, err := fetch(ctx, before)
		if err != nil || len(msgs) == 0 {
			return emitted, nil
		}
		for _, m := range msgs {
			// Only persisted rows (seq >= 0) advance the cursor; the fetch returns
			// only persisted rows, but the guard mirrors the subscription dedup.
			if seq := m.GetSeq(); seq >= 0 {
				if !cursor.Advance(agentID, seq) {
					continue
				}
			}
			if err := emit(m); err != nil {
				return emitted, err
			}
			emitted = true
		}
		if !hasMore || cursor.Get(agentID) <= before {
			return emitted, nil
		}
	}
}

// tailAgentMessages streams `WatchEvents` for a single agent and
// emits each `AgentChatMessage` as a JSON line on stdout. On
// transport disconnect it reconnects with capped exponential backoff,
// resuming from the latest `seq` it observed. Before each RECONNECT it
// drains any backlog the capped replay would skip (see drainBacklog), so
// messages generated during a long disconnect aren't lost.
//
// Output format: each line is the AgentChatMessage proto rendered via
// the same encoder the polling implementation used, so external
// scripts written against the old behaviour keep working byte-for-byte.
func tailAgentMessages(ctx context.Context, c *remote.Client, workerID, agentID, workspaceID string,
	startSeq int64, em *lineEmitter,
) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	cursor := streamevents.NewAgentCursor()
	cursor.Track(agentID, startSeq)
	// terminals cursor stays empty — we don't subscribe terminals here.
	terminals := streamevents.NewTerminalCursor()

	transport, err := newAgentMessagesTransport(ctx, c, workerID, workspaceID)
	if err != nil {
		return remote.EmitErrorWith("subscribe_failed", err)
	}
	defer transport.close()

	// Render one message to stdout. The subscription callback and the reconnect-drain
	// loop both emit; lineEmitter serializes them so lines can't interleave.
	emitMsg := func(m *leapmuxv1.AgentChatMessage) error {
		return em.emit(renderAgentMessage(m))
	}

	// Set on every delivered (non-duplicate) message so the reconnect loop can
	// tell a session that did real work from a flap. Reset per session below.
	var delivered atomic.Bool
	onAgent := func(ae *leapmuxv1.AgentEvent) {
		msg := ae.GetAgentMessage()
		if msg == nil {
			return
		}
		if err := emitMsg(msg); err != nil {
			cancel()
			return
		}
		delivered.Store(true)
	}
	onCursorReset := func(_ string) { /* not relevant for agent-only follow */ }
	sub := streamevents.NewSubscription(transport.transport, cursor, terminals, onAgent, nil, onCursorReset)
	defer sub.Cancel()

	// Reconnect backlog drain: page ListAgentMessages forward from the cursor. A
	// still-zero cursor (no message seen yet) drains from the very beginning via the
	// OLDEST anchor; once the cursor has advanced, page forward via AFTER from it.
	// This replaces the prior AFTER+cursor_seq==0 overload, which leaned on the
	// worker treating an AFTER bound of 0 as "from the oldest message".
	drainFetch := func(ctx context.Context, afterSeq int64) ([]*leapmuxv1.AgentChatMessage, bool, error) {
		req := &leapmuxv1.ListAgentMessagesRequest{
			AgentId: agentID,
			Limit:   followDrainPageLimit,
		}
		if streamevents.IsResumeCursor(afterSeq) {
			req.Anchor = leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_AFTER
			req.CursorSeq = afterSeq
		} else {
			req.Anchor = leapmuxv1.MessagePageAnchor_MESSAGE_PAGE_ANCHOR_OLDEST
		}
		var resp leapmuxv1.ListAgentMessagesResponse
		if err := callInnerRPC(ctx, c, workerID, "ListAgentMessages", req, &resp); err != nil {
			return nil, false, err
		}
		return resp.GetMessages(), resp.GetHasMore(), nil
	}

	// Reconnect loop. Each iteration opens a fresh subscription
	// using the latest cursor value, then waits for it to terminate
	// (either via ctx cancellation or the transport ending). We back
	// off briefly between reconnects so a worker that's flapping
	// doesn't pin the CLI's CPU.
	const (
		initialBackoff = 250 * time.Millisecond
		maxBackoff     = 8 * time.Second
	)
	backoff := newReconnectBackoff(initialBackoff, maxBackoff)
	for {
		// Before EVERY (re)subscribe -- including the first -- bridge the gap the capped
		// WatchEvents replay (<= maxMessagePageLimit per connect) can't cover, by paging
		// ListAgentMessages forward from the cursor. On a RECONNECT this is the
		// disconnect gap; on the FIRST connect it is a burst created between page-1's
		// snapshot and the watcher registering that overflows the 50-row replay -- page-1
		// shows recent history but does NOT cover a >50 in-flight burst, so without this
		// drain those middle messages are silently skipped (the replay caps at 50 and the
		// live stream resumes from the newest). The drain pages AFTER the cursor (strict
		// seq > cursor), so it never re-emits a page-1 message. Best-effort: if the drain
		// can't reach the worker yet it returns early and the resubscribe/backoff below
		// retries -- the cursor only advances past emitted messages, so the next
		// reconnect's drain re-attempts the gap.
		drained, drainErr := drainBacklog(ctx, agentID, cursor, drainFetch, emitMsg)
		if drainErr != nil {
			return drainErr
		}
		if ctx.Err() != nil {
			if err := em.Err(); err != nil {
				return err
			}
			return nil
		}
		req := &leapmuxv1.WatchEventsRequest{
			Agents: []*leapmuxv1.WatchAgentEntry{
				// A still-zero cursor (page-1 was empty and not an explicit AFTER
				// resume) subscribes fresh (LATEST); an advanced cursor resumes
				// AFTER_CURSOR. AgentWatchEntry owns that mapping.
				streamevents.AgentWatchEntry(agentID, cursor.Get(agentID)),
			},
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
				if err := em.Err(); err != nil {
					return err
				}
				return nil
			}
			if emitErr := em.emitError(agentID, "subscribe_failed", err); emitErr != nil {
				return emitErr
			}
		} else {
			// Block until the subscription's transport ends
			// (channel closed, RPC error, ctx cancelled).
			select {
			case <-ctx.Done():
				if err := em.Err(); err != nil {
					return err
				}
				return nil
			case <-sub.Done():
			}
		}
		if ctx.Err() != nil {
			if err := em.Err(); err != nil {
				return err
			}
			return nil
		}
		// Brief backoff before reconnecting -- reset to the floor if this cycle did
		// real work, else keep climbing. Real work is either a live frame this session
		// delivered OR a backlog the pre-subscribe drain forwarded: a transport that
		// drains history but drops the subscription before any frame is still moving
		// messages, so it shouldn't be treated as a flap.
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff.afterSession(delivered.Load() || drained)):
		}
	}
}

// agentMessagesTransport packages the per-mode plumbing for
// `agent messages --follow` so the reconnect loop above doesn't have
// to know whether it's running over an E2EE channel or the local
// IPC socket.
type agentMessagesTransport struct {
	transport streamevents.Transport
	close     func()
}

func newAgentMessagesTransport(ctx context.Context, c *remote.Client, workerID, workspaceID string) (*agentMessagesTransport, error) {
	if c.IsLocal() {
		// Local-IPC mode: route via RemoteIPCService.StreamInner.
		// workerID may be empty; the router resolves the spawning
		// worker from the bearer scope, mirroring the existing
		// localIPCCallInnerBest behaviour.
		return &agentMessagesTransport{
			transport: streamevents.NewLocalIPCTransport(c.RemoteIPCService(), workspaceID, workerID, slog.Default()),
			close:     func() {},
		}, nil
	}
	if workerID == "" {
		return nil, errors.New("worker_id required for hub-bound mode")
	}
	ch, err := c.OpenE2EEChannel(ctx, workerID)
	if err != nil {
		return nil, err
	}
	return &agentMessagesTransport{
		transport: streamevents.NewChannelTransport(ch, slog.Default()),
		close: func() {
			ch.Close()
		},
	}, nil
}

// lineEmitter serializes JSON-line writes to one encoder under one mutex, so the
// concurrent emitters in --follow mode (the subscription callback and the
// reconnect-drain loop) can't interleave bytes mid-line. Bundling the encoder and
// mutex together means a caller can't encode without holding the lock -- the locking
// discipline stops being every call site's responsibility.
type lineEmitter struct {
	mu  sync.Mutex
	enc *json.Encoder
	err error
}

// emit writes one JSON value as a line under the lock.
func (e *lineEmitter) emit(v any) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.err != nil {
		return e.err
	}
	if err := e.enc.Encode(v); err != nil {
		e.err = err
		return err
	}
	return nil
}

func (e *lineEmitter) Err() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.err
}

// emitError writes a `{"source":"error",...}` line under the lock so it doesn't
// interleave with concurrent event encodes. Panics on a nil err (a programmer error
// -- a nil message would be indistinguishable from a successful frame downstream).
func (e *lineEmitter) emitError(contextID, code string, err error) error {
	return e.emit(map[string]any{
		"source":  "error",
		"context": contextID,
		"code":    code,
		"message": err.Error(),
	})
}
