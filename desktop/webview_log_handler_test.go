package main

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// mockExecJS collects all JS strings passed to it.
type mockExecJS struct {
	mu   sync.Mutex
	calls []string
}

func (m *mockExecJS) fn(js string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, js)
}

func (m *mockExecJS) getCalls() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.calls...)
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
func (h *noopHandler) WithAttrs(attrs []slog.Attr) slog.Handler { return h }
func (h *noopHandler) WithGroup(name string) slog.Handler       { return h }
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

func TestBufferingBeforeReady(t *testing.T) {
	mock := &mockExecJS{}
	inner := &noopHandler{}
	h := newWebviewHandler(inner, mock.fn)

	r := newTestRecord(slog.LevelInfo, "starting")
	_ = h.Handle(context.TODO(), r)

	if got := mock.getCalls(); len(got) != 0 {
		t.Fatalf("expected no execJS calls before ready, got %d", len(got))
	}
	if inner.getHandled() != 1 {
		t.Fatal("inner handler should have been called")
	}
}

func TestFlushOnSetReady(t *testing.T) {
	mock := &mockExecJS{}
	inner := &noopHandler{}
	h := newWebviewHandler(inner, mock.fn)

	_ = h.Handle(context.TODO(), newTestRecord(slog.LevelInfo, "msg1"))
	_ = h.Handle(context.TODO(), newTestRecord(slog.LevelWarn, "msg2"))

	h.SetReady()

	calls := mock.getCalls()
	if len(calls) != 2 {
		t.Fatalf("expected 2 flushed calls, got %d", len(calls))
	}
	if !strings.Contains(calls[0], "console.info") {
		t.Errorf("first flushed call should be console.info, got %s", calls[0])
	}
	if !strings.Contains(calls[1], "console.warn") {
		t.Errorf("second flushed call should be console.warn, got %s", calls[1])
	}
}

func TestDirectForwardingAfterReady(t *testing.T) {
	mock := &mockExecJS{}
	h := newWebviewHandler(&noopHandler{}, mock.fn)
	h.SetReady()

	_ = h.Handle(context.TODO(), newTestRecord(slog.LevelError, "boom"))

	calls := mock.getCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	if !strings.Contains(calls[0], "console.error") {
		t.Errorf("expected console.error, got %s", calls[0])
	}
}

func TestBufferCap(t *testing.T) {
	mock := &mockExecJS{}
	h := newWebviewHandler(&noopHandler{}, mock.fn)

	// Fill buffer beyond cap.
	for i := range maxBufferedRecords + 50 {
		r := newTestRecord(slog.LevelInfo, "msg", slog.Int("i", i))
		_ = h.Handle(context.TODO(), r)
	}

	h.SetReady()

	calls := mock.getCalls()
	if len(calls) != maxBufferedRecords {
		t.Fatalf("expected %d buffered calls, got %d", maxBufferedRecords, len(calls))
	}
	// Oldest should have been dropped; first flushed should be i=50.
	if !strings.Contains(calls[0], "i=50") {
		t.Errorf("expected first flushed record to have i=50, got %s", calls[0])
	}
}

func TestJSEscaping(t *testing.T) {
	mock := &mockExecJS{}
	h := newWebviewHandler(&noopHandler{}, mock.fn)
	h.SetReady()

	r := newTestRecord(slog.LevelInfo, "line1\nline2\"quote")
	_ = h.Handle(context.TODO(), r)

	calls := mock.getCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	// json.Marshal escapes newlines and quotes.
	if strings.Contains(calls[0], "\n") {
		t.Error("newline should be escaped in JS output")
	}
	if !strings.Contains(calls[0], `\n`) {
		t.Error("expected escaped newline (\\n) in output")
	}
	if !strings.Contains(calls[0], `\"`) {
		t.Error("expected escaped quote in output")
	}
}

func TestLevelMapping(t *testing.T) {
	tests := []struct {
		level  slog.Level
		method string
	}{
		{slog.LevelDebug, "console.debug"},
		{slog.LevelInfo, "console.info"},
		{slog.LevelWarn, "console.warn"},
		{slog.LevelError, "console.error"},
	}

	for _, tt := range tests {
		mock := &mockExecJS{}
		h := newWebviewHandler(&noopHandler{}, mock.fn)
		h.SetReady()

		_ = h.Handle(context.TODO(), newTestRecord(tt.level, "test"))

		calls := mock.getCalls()
		if len(calls) != 1 {
			t.Fatalf("level %v: expected 1 call, got %d", tt.level, len(calls))
		}
		if !strings.HasPrefix(calls[0], tt.method+"(") {
			t.Errorf("level %v: expected %s, got %s", tt.level, tt.method, calls[0])
		}
	}
}

func TestInnerHandlerAlwaysCalled(t *testing.T) {
	inner := &noopHandler{}
	h := newWebviewHandler(inner, (&mockExecJS{}).fn)

	// Before ready.
	_ = h.Handle(context.TODO(), newTestRecord(slog.LevelInfo, "a"))
	// After ready.
	h.SetReady()
	_ = h.Handle(context.TODO(), newTestRecord(slog.LevelInfo, "b"))

	if inner.getHandled() != 2 {
		t.Fatalf("expected inner handler called 2 times, got %d", inner.getHandled())
	}
}

func TestWithAttrsAppearsInOutput(t *testing.T) {
	mock := &mockExecJS{}
	h := newWebviewHandler(&noopHandler{}, mock.fn)
	h.SetReady()

	h2 := h.WithAttrs([]slog.Attr{slog.String("component", "hub")})
	_ = h2.Handle(context.TODO(), newTestRecord(slog.LevelInfo, "ready"))

	calls := mock.getCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	if !strings.Contains(calls[0], "component=hub") {
		t.Errorf("expected component=hub in output, got %s", calls[0])
	}
}

func TestWithGroupAppearsInOutput(t *testing.T) {
	mock := &mockExecJS{}
	h := newWebviewHandler(&noopHandler{}, mock.fn)
	h.SetReady()

	h2 := h.WithGroup("server").WithAttrs([]slog.Attr{slog.String("addr", ":8080")})
	_ = h2.Handle(context.TODO(), newTestRecord(slog.LevelInfo, "listening"))

	calls := mock.getCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	if !strings.Contains(calls[0], "server.addr=:8080") {
		t.Errorf("expected server.addr=:8080 in output, got %s", calls[0])
	}
}
