package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	desktoppb "github.com/leapmux/leapmux/generated/proto/leapmux/desktop/v1"
)

type webviewHandler struct {
	inner          slog.Handler
	emitEvent      func(*desktoppb.Event)
	formattedAttrs []string // pre-formatted "key=value" strings from WithAttrs
	group          string
}

func newWebviewHandler(inner slog.Handler, emitEvent func(*desktoppb.Event)) *webviewHandler {
	return &webviewHandler{
		inner:     inner,
		emitEvent: emitEvent,
	}
}

func (h *webviewHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *webviewHandler) Handle(ctx context.Context, r slog.Record) error {
	err := h.inner.Handle(ctx, r)
	if h.emitEvent != nil {
		h.emitEvent(&desktoppb.Event{
			Payload: &desktoppb.Event_SidecarLog{
				SidecarLog: h.buildLogEvent(r),
			},
		})
	}
	return err
}

func (h *webviewHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	formatted := make([]string, 0, len(h.formattedAttrs)+len(attrs))
	formatted = append(formatted, h.formattedAttrs...)
	for _, a := range attrs {
		formatted = append(formatted, h.formatAttr(a))
	}
	return &webviewHandler{
		inner:          h.inner.WithAttrs(attrs),
		emitEvent:      h.emitEvent,
		formattedAttrs: formatted,
		group:          h.group,
	}
}

func (h *webviewHandler) WithGroup(name string) slog.Handler {
	prefix := name
	if h.group != "" {
		prefix = h.group + "." + name
	}
	return &webviewHandler{
		inner:          h.inner.WithGroup(name),
		emitEvent:      h.emitEvent,
		formattedAttrs: h.formattedAttrs,
		group:          prefix,
	}
}

func (h *webviewHandler) buildLogEvent(r slog.Record) *desktoppb.SidecarLogEvent {
	level := "info"
	switch {
	case r.Level >= slog.LevelError:
		level = "error"
	case r.Level >= slog.LevelWarn:
		level = "warn"
	case r.Level < slog.LevelInfo:
		level = "debug"
	}

	attrs := append([]string(nil), h.formattedAttrs...)
	r.Attrs(func(a slog.Attr) bool {
		attrs = append(attrs, h.formatAttr(a))
		return true
	})

	return &desktoppb.SidecarLogEvent{
		Level:   level,
		Time:    r.Time.Format(time.TimeOnly),
		Message: r.Message,
		Attrs:   attrs,
	}
}

func (h *webviewHandler) formatAttr(a slog.Attr) string {
	key := a.Key
	if h.group != "" {
		key = h.group + "." + key
	}
	return fmt.Sprintf("%s=%s", key, a.Value.String())
}
