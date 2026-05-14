package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"strings"
	"sync"
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
	var afterSeq, beforeSeq int64
	var limit int
	var follow bool
	return withResolvedAgent(rawCtx, args, agentScaffoldOpts{
		setup: func(fs *flag.FlagSet) {
			fs.Int64Var(&afterSeq, "after-seq", 0, "return messages with seq > after_seq")
			fs.Int64Var(&beforeSeq, "before-seq", 0, "return messages with seq < before_seq")
			fs.IntVar(&limit, "limit", 5, "max messages per page (hub caps at 50)")
			fs.BoolVar(&follow, "follow", false, "tail new messages indefinitely")
		},
		noDeadline: true,
		body: func(ctx context.Context, c *remote.Client, workerID, agentID, workspaceID string) error {
			// Page 1: history pulled via ListAgentMessages so the user
			// sees prior context before the live tail starts. WatchEvents
			// can replay from after_seq=0 too, but ListAgentMessages is
			// shaped for paginated history (cap = 50/page), so we keep it
			// for the single-page show-history-first behaviour.
			var resp leapmuxv1.ListAgentMessagesResponse
			if err := callInnerRPC(ctx, c, workerID, "ListAgentMessages", &leapmuxv1.ListAgentMessagesRequest{
				AgentId:   agentID,
				AfterSeq:  afterSeq,
				BeforeSeq: beforeSeq,
				Limit:     int32(limit),
			}, &resp); err != nil {
				return err
			}
			if !follow {
				rendered := make([]map[string]any, 0, len(resp.GetMessages()))
				for _, m := range resp.GetMessages() {
					rendered = append(rendered, renderAgentMessage(m))
				}
				return remote.EmitData(rendered)
			}

			// Follow mode: emit page-1 then stream live via WatchEvents.
			enc := json.NewEncoder(remote.Out)
			emitMu := &sync.Mutex{}
			for _, m := range resp.GetMessages() {
				_ = enc.Encode(renderAgentMessage(m))
			}
			cursor := afterSeq
			if n := len(resp.GetMessages()); n > 0 {
				cursor = resp.GetMessages()[n-1].GetSeq()
			}

			return tailAgentMessages(ctx, c, workerID, agentID, workspaceID, cursor, enc, emitMu)
		},
	})
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

// tailAgentMessages streams `WatchEvents` for a single agent and
// emits each `AgentChatMessage` as a JSON line on stdout. On
// transport disconnect it reconnects with capped exponential backoff,
// resuming from the latest `seq` it observed so messages aren't lost.
//
// Output format: each line is the AgentChatMessage proto rendered via
// the same encoder the polling implementation used, so external
// scripts written against the old behaviour keep working byte-for-byte.
func tailAgentMessages(ctx context.Context, c *remote.Client, workerID, agentID, workspaceID string,
	startSeq int64, enc *json.Encoder, emitMu *sync.Mutex,
) error {
	cursor := streamevents.NewAgentCursor()
	cursor.Track(agentID, startSeq)
	// terminals cursor stays empty — we don't subscribe terminals here.
	terminals := streamevents.NewTerminalCursor()

	transport, err := newAgentMessagesTransport(ctx, c, workerID, workspaceID)
	if err != nil {
		return remote.EmitErrorWith("subscribe_failed", err)
	}
	defer transport.close()

	onAgent := func(ae *leapmuxv1.AgentEvent) {
		msg := ae.GetAgentMessage()
		if msg == nil {
			return
		}
		emitMu.Lock()
		_ = enc.Encode(renderAgentMessage(msg))
		emitMu.Unlock()
	}
	onCursorReset := func(_ string) { /* not relevant for agent-only follow */ }
	sub := streamevents.NewSubscription(transport.transport, cursor, terminals, onAgent, nil, onCursorReset)
	defer sub.Cancel()

	// Reconnect loop. Each iteration opens a fresh subscription
	// using the latest cursor value, then waits for it to terminate
	// (either via ctx cancellation or the transport ending). We back
	// off briefly between reconnects so a worker that's flapping
	// doesn't pin the CLI's CPU.
	backoff := 250 * time.Millisecond
	for {
		req := &leapmuxv1.WatchEventsRequest{
			Agents: []*leapmuxv1.WatchAgentEntry{
				{AgentId: agentID, AfterSeq: cursor.Get(agentID)},
			},
		}
		if err := sub.Update(ctx, req); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			emitErrorLine(enc, emitMu, agentID, "subscribe_failed", err)
		} else {
			// Block until the subscription's transport ends
			// (channel closed, RPC error, ctx cancelled).
			select {
			case <-ctx.Done():
				return nil
			case <-sub.Done():
			}
		}
		if ctx.Err() != nil {
			return nil
		}
		// Brief backoff before reconnecting.
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		if backoff < 8*time.Second {
			backoff *= 2
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
			transport: streamevents.NewLocalIPCTransport(c.RemoteIPCService(), workspaceID, workerID),
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
		transport: streamevents.NewChannelTransport(ch),
		close: func() {
			ch.Close()
		},
	}, nil
}

// emitErrorLine writes a `{"source":"error",...}` JSON line under mu
// so it doesn't interleave with concurrent event encodes.
func emitErrorLine(enc *json.Encoder, mu *sync.Mutex, contextID, code string, err error) {
	mu.Lock()
	defer mu.Unlock()
	_ = enc.Encode(map[string]any{
		"source":  "error",
		"context": contextID,
		"code":    code,
		"message": err.Error(),
	})
}

// RunAgentSet updates --model / --effort / --permission-mode /
// --extra-setting key=value (repeatable).
func RunAgentSet(rawCtx any, args []string) error {
	var model, effort, permissionMode string
	extras := stringSliceFlag{}
	settings := &leapmuxv1.AgentSettings{ExtraSettings: map[string]string{}}
	return withResolvedAgent(rawCtx, args, agentScaffoldOpts{
		setup: func(fs *flag.FlagSet) {
			fs.StringVar(&model, "model", "", "model id (empty = no change)")
			fs.StringVar(&effort, "effort", "", "effort id (empty = no change)")
			fs.StringVar(&permissionMode, "permission-mode", "", "permission mode (empty = no change)")
			fs.Var(&extras, "extra-setting", "extra setting in key=value form (repeatable)")
		},
		validate: func() error {
			settings.Model = model
			settings.Effort = effort
			settings.PermissionMode = permissionMode
			for _, kv := range extras.values {
				k, v, err := splitKV(kv)
				if err != nil {
					return remote.EmitErrorWith("invalid_request", err)
				}
				settings.ExtraSettings[k] = v
			}
			return nil
		},
		body: func(ctx context.Context, c *remote.Client, workerID, agentID, _ string) error {
			if err := callInnerRPC(ctx, c, workerID, "UpdateAgentSettings", &leapmuxv1.UpdateAgentSettingsRequest{
				AgentId:  agentID,
				Settings: settings,
			}, nil); err != nil {
				return err
			}
			return remote.EmitData(map[string]any{"agent_id": agentID, "applied": map[string]any{
				"model":          model,
				"effort":         effort,
				"permissionMode": permissionMode,
				"extras":         settings.ExtraSettings,
			}})
		},
	})
}

// RunAgentSendControlResponse forwards a raw control_response payload
// to a Claude-Code-style agent.
func RunAgentSendControlResponse(rawCtx any, args []string) error {
	var content string
	return withResolvedAgent(rawCtx, args, agentScaffoldOpts{
		setup: func(fs *flag.FlagSet) {
			fs.StringVar(&content, "content", "", "raw control_response JSON (required)")
		},
		validate: func() error {
			if content == "" {
				return remote.EmitError("invalid_request", "--content is required")
			}
			return nil
		},
		body: func(ctx context.Context, c *remote.Client, workerID, agentID, _ string) error {
			if err := callInnerRPC(ctx, c, workerID, "SendControlResponse", &leapmuxv1.SendControlResponseRequest{
				AgentId: agentID,
				Content: []byte(content),
			}, nil); err != nil {
				return err
			}
			return remote.EmitData(map[string]string{"agent_id": agentID})
		},
	})
}

// stringSliceFlag implements flag.Value for repeatable string flags.
type stringSliceFlag struct {
	values []string
}

func (s *stringSliceFlag) String() string { return fmt.Sprintf("%v", s.values) }
func (s *stringSliceFlag) Set(v string) error {
	s.values = append(s.values, v)
	return nil
}

func splitKV(s string) (string, string, error) {
	k, v, ok := strings.Cut(s, "=")
	if !ok {
		return "", "", fmt.Errorf("expected key=value, got %q", s)
	}
	return k, v, nil
}
