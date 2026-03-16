package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

const maxBufferedRecords = 500

// webviewHandler is a slog.Handler that wraps an inner handler and
// additionally forwards log records to a WebView console via execJS.
// Before the WebView is ready, JS strings are buffered (capped at
// maxBufferedRecords). After SetReady is called, buffered strings are
// flushed and subsequent records are forwarded directly.
type webviewHandler struct {
	inner  slog.Handler
	shared *webviewHandlerShared
	attrs  []slog.Attr
	group  string
}

type webviewHandlerShared struct {
	mu     sync.Mutex
	execJS func(string)
	ready  bool
	buffer []string
}

func newWebviewHandler(inner slog.Handler, execJS func(string)) *webviewHandler {
	return &webviewHandler{
		inner: inner,
		shared: &webviewHandlerShared{
			execJS: execJS,
		},
	}
}

func (h *webviewHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *webviewHandler) Handle(ctx context.Context, r slog.Record) error {
	err := h.inner.Handle(ctx, r)

	js := h.formatConsoleJS(r)

	h.shared.mu.Lock()
	defer h.shared.mu.Unlock()

	if h.shared.ready {
		h.shared.execJS(js)
	} else {
		if len(h.shared.buffer) >= maxBufferedRecords {
			// Drop oldest to make room.
			h.shared.buffer = h.shared.buffer[1:]
		}
		h.shared.buffer = append(h.shared.buffer, js)
	}

	return err
}

func (h *webviewHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &webviewHandler{
		inner:  h.inner.WithAttrs(attrs),
		shared: h.shared,
		attrs:  append(append([]slog.Attr(nil), h.attrs...), attrs...),
		group:  h.group,
	}
}

func (h *webviewHandler) WithGroup(name string) slog.Handler {
	prefix := name
	if h.group != "" {
		prefix = h.group + "." + name
	}
	return &webviewHandler{
		inner:  h.inner.WithGroup(name),
		shared: h.shared,
		attrs:  h.attrs,
		group:  prefix,
	}
}

// SetReady flushes the buffer and marks the handler as ready for direct
// forwarding.
func (h *webviewHandler) SetReady() {
	h.shared.mu.Lock()
	defer h.shared.mu.Unlock()

	for _, js := range h.shared.buffer {
		h.shared.execJS(js)
	}
	h.shared.buffer = nil
	h.shared.ready = true
}

func (h *webviewHandler) formatConsoleJS(r slog.Record) string {
	method := "info"
	switch {
	case r.Level >= slog.LevelError:
		method = "error"
	case r.Level >= slog.LevelWarn:
		method = "warn"
	case r.Level < slog.LevelInfo:
		method = "debug"
	}

	var args []string
	args = append(args, jsonString(r.Time.Format(time.TimeOnly)))
	args = append(args, jsonString(r.Message))

	// Include pre-set attrs from WithAttrs.
	for _, a := range h.attrs {
		args = append(args, jsonString(h.formatAttr(a)))
	}

	// Include record-level attrs.
	r.Attrs(func(a slog.Attr) bool {
		args = append(args, jsonString(h.formatAttr(a)))
		return true
	})

	return fmt.Sprintf("console.%s(%s)", method, strings.Join(args, ","))
}

func (h *webviewHandler) formatAttr(a slog.Attr) string {
	key := a.Key
	if h.group != "" {
		key = h.group + "." + key
	}
	return key + "=" + a.Value.String()
}

// jsonString returns s as a JSON-encoded string literal (valid JS).
func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
