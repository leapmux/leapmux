package main

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	desktoppb "github.com/leapmux/leapmux/generated/proto/leapmux/desktop/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestApp_OpenChannelRelay_Solo(t *testing.T) {
	home, err := os.MkdirTemp("/tmp", "leapmux-home-")
	if err != nil {
		t.Fatalf("MkdirTemp() failed: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	t.Setenv("HOME", filepath.Clean(home))

	app := NewApp(nil)
	defer app.shutdown()

	if err := app.ConnectSolo(); err != nil {
		t.Fatalf("ConnectSolo() failed: %v", err)
	}

	if err := app.OpenChannelRelay(); err != nil {
		t.Fatalf("OpenChannelRelay() failed: %v", err)
	}

	if err := app.CloseChannelRelay(); err != nil {
		t.Fatalf("CloseChannelRelay() failed: %v", err)
	}
}

func TestApp_SidecarLogEvents_EmittedAfterSoloStart(t *testing.T) {
	home, err := os.MkdirTemp("/tmp", "leapmux-home-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	t.Setenv("HOME", filepath.Clean(home))

	var mu sync.Mutex
	var events []*desktoppb.SidecarLogEvent
	emitFunc := func(event *desktoppb.Event) {
		if log := event.GetSidecarLog(); log != nil {
			mu.Lock()
			events = append(events, log)
			mu.Unlock()
		}
	}

	app := NewApp(emitFunc)
	defer app.shutdown()

	require.NoError(t, app.ConnectSolo())
	require.NoError(t, app.OpenChannelRelay())

	// Give the hub a moment to process the WebSocket upgrade and log.
	time.Sleep(100 * time.Millisecond)

	require.NoError(t, app.CloseChannelRelay())

	// Wait for disconnect log.
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	captured := append([]*desktoppb.SidecarLogEvent(nil), events...)
	mu.Unlock()

	// We should have at least "channel relay connected" emitted.
	require.NotEmpty(t, captured, "expected sidecar:log events to be emitted")

	var messages []string
	for _, e := range captured {
		messages = append(messages, e.Message)
	}
	assert.Contains(t, messages, "channel relay connected")
}
