package main

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	desktoppb "github.com/leapmux/leapmux/generated/proto/leapmux/desktop/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockEmitter collects all emitted SidecarLogEvent payloads.
type mockEmitter struct {
	mu     sync.Mutex
	events []*desktoppb.SidecarLogEvent
}

func (m *mockEmitter) fn(event *desktoppb.Event) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if log := event.GetSidecarLog(); log != nil {
		m.events = append(m.events, log)
	}
}

func (m *mockEmitter) getEvents() []*desktoppb.SidecarLogEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]*desktoppb.SidecarLogEvent(nil), m.events...)
}

// noopHandler is a minimal slog.Handler that records whether Handle was called.
type noopHandler struct {
	mu      sync.Mutex
	handled int
}

func (h *noopHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }
func (h *noopHandler) Handle(_ context.Context, _ slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.handled++
	return nil
}
func (h *noopHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *noopHandler) WithGroup(_ string) slog.Handler      { return h }
func (h *noopHandler) getHandled() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.handled
}

func newTestRecord(level slog.Level, msg string, attrs ...slog.Attr) slog.Record {
	r := slog.NewRecord(time.Date(2026, 3, 16, 14, 30, 45, 0, time.UTC), level, msg, 0)
	r.AddAttrs(attrs...)
	return r
}

func TestEmitsEventImmediately(t *testing.T) {
	mock := &mockEmitter{}
	inner := &noopHandler{}
	h := newWebviewHandler(inner, mock.fn)

	r := newTestRecord(slog.LevelInfo, "starting")
	_ = h.Handle(context.TODO(), r)

	events := mock.getEvents()
	require.Len(t, events, 1)
	assert.Equal(t, "info", events[0].Level)
	assert.Equal(t, "starting", events[0].Message)
	assert.Equal(t, 1, inner.getHandled())
}

func TestLevelMapping(t *testing.T) {
	tests := []struct {
		level    slog.Level
		expected string
	}{
		{slog.LevelDebug, "debug"},
		{slog.LevelInfo, "info"},
		{slog.LevelWarn, "warn"},
		{slog.LevelError, "error"},
	}

	for _, tt := range tests {
		mock := &mockEmitter{}
		h := newWebviewHandler(&noopHandler{}, mock.fn)

		_ = h.Handle(context.TODO(), newTestRecord(tt.level, "test"))

		events := mock.getEvents()
		require.Len(t, events, 1, "level %v", tt.level)
		assert.Equal(t, tt.expected, events[0].Level, "level %v", tt.level)
	}
}

func TestInnerHandlerAlwaysCalled(t *testing.T) {
	inner := &noopHandler{}
	h := newWebviewHandler(inner, (&mockEmitter{}).fn)

	_ = h.Handle(context.TODO(), newTestRecord(slog.LevelInfo, "a"))
	_ = h.Handle(context.TODO(), newTestRecord(slog.LevelInfo, "b"))

	assert.Equal(t, 2, inner.getHandled())
}

func TestWithAttrsAppearsInOutput(t *testing.T) {
	mock := &mockEmitter{}
	h := newWebviewHandler(&noopHandler{}, mock.fn)

	h2 := h.WithAttrs([]slog.Attr{slog.String("component", "hub")})
	_ = h2.Handle(context.TODO(), newTestRecord(slog.LevelInfo, "ready"))

	events := mock.getEvents()
	require.Len(t, events, 1)
	assert.Contains(t, events[0].Attrs, "component=hub")
}

func TestWithGroupAppearsInOutput(t *testing.T) {
	mock := &mockEmitter{}
	h := newWebviewHandler(&noopHandler{}, mock.fn)

	h2 := h.WithGroup("server").WithAttrs([]slog.Attr{slog.String("addr", ":8080")})
	_ = h2.Handle(context.TODO(), newTestRecord(slog.LevelInfo, "listening"))

	events := mock.getEvents()
	require.Len(t, events, 1)
	assert.Contains(t, events[0].Attrs, "server.addr=:8080")
}

func TestWithAttrsSiblingIsolation(t *testing.T) {
	mock := &mockEmitter{}
	parent := newWebviewHandler(&noopHandler{}, mock.fn)

	child1 := parent.WithAttrs([]slog.Attr{slog.String("child", "1")})
	child2 := parent.WithAttrs([]slog.Attr{slog.String("child", "2")})

	_ = child1.Handle(context.TODO(), newTestRecord(slog.LevelInfo, "from1"))
	_ = child2.Handle(context.TODO(), newTestRecord(slog.LevelInfo, "from2"))

	events := mock.getEvents()
	require.Len(t, events, 2)
	assert.Contains(t, events[0].Attrs, "child=1")
	assert.NotContains(t, events[0].Attrs, "child=2")
	assert.Contains(t, events[1].Attrs, "child=2")
}

func TestTimeFormatting(t *testing.T) {
	mock := &mockEmitter{}
	h := newWebviewHandler(&noopHandler{}, mock.fn)

	_ = h.Handle(context.TODO(), newTestRecord(slog.LevelInfo, "test"))

	events := mock.getEvents()
	require.Len(t, events, 1)
	assert.Equal(t, "14:30:45", events[0].Time)
}

func TestRecordAttrs(t *testing.T) {
	mock := &mockEmitter{}
	h := newWebviewHandler(&noopHandler{}, mock.fn)

	r := newTestRecord(slog.LevelInfo, "test", slog.Int("port", 8080), slog.String("host", "localhost"))
	_ = h.Handle(context.TODO(), r)

	events := mock.getEvents()
	require.Len(t, events, 1)
	assert.Contains(t, events[0].Attrs, "port=8080")
	assert.Contains(t, events[0].Attrs, "host=localhost")
}
