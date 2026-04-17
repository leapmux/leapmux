package main

import (
	"sync"
	"testing"
	"time"

	desktoppb "github.com/leapmux/leapmux/generated/proto/leapmux/desktop/v1"
	"github.com/leapmux/leapmux/locallisten"
	"github.com/leapmux/leapmux/locallisten/locallistentest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setUniqueSoloLocalListen scopes the hub's local-listen URL per-test so
// multiple tests running in the same process don't collide on the
// per-platform default endpoint (the Windows default embeds the current
// user's SID, which is identical across tests as the same user).
func setUniqueSoloLocalListen(t *testing.T) {
	t.Helper()
	t.Setenv(locallisten.EnvLocalListen, locallistentest.UniqueListenURL(t, "leapmux-desktop-test"))
}

func TestApp_OpenChannelRelay_Solo(t *testing.T) {
	setUniqueSoloLocalListen(t)
	locallistentest.SandboxHome(t)

	app := NewApp("")
	defer app.Shutdown()

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
	setUniqueSoloLocalListen(t)
	locallistentest.SandboxHome(t)

	var mu sync.Mutex
	var events []*desktoppb.SidecarLogEvent
	emitFunc := func(event *desktoppb.Event) {
		if log := event.GetSidecarLog(); log != nil {
			mu.Lock()
			events = append(events, log)
			mu.Unlock()
		}
	}

	app := NewApp("")
	app.SetEventSink(emitFunc)
	defer app.Shutdown()

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

// TestApp_OpenChannelRelay_ReusesAliveRelay reproduces the dev-refresh bug:
// when the persistent sidecar is asked to open the channel relay again, it
// must reuse the existing live relay rather than churning the connection.
// Churning would tear down the hub's relay binding and (combined with the
// race in UnregisterUnboundByUser) wipe channels the new page just opened.
func TestApp_OpenChannelRelay_ReusesAliveRelay(t *testing.T) {
	setUniqueSoloLocalListen(t)
	locallistentest.SandboxHome(t)

	var mu sync.Mutex
	var connectCount, disconnectCount int
	emitFunc := func(event *desktoppb.Event) {
		log := event.GetSidecarLog()
		if log == nil {
			return
		}
		mu.Lock()
		switch log.Message {
		case "channel relay connected":
			connectCount++
		case "channel relay disconnected":
			disconnectCount++
		}
		mu.Unlock()
	}

	app := NewApp("")
	app.SetEventSink(emitFunc)
	defer app.Shutdown()

	require.NoError(t, app.ConnectSolo())
	require.NoError(t, app.OpenChannelRelay())
	time.Sleep(100 * time.Millisecond)

	firstRelay := app.relay
	require.NotNil(t, firstRelay)

	// Second OpenChannelRelay simulates the page-refresh path. It must
	// reuse the existing relay rather than opening a new one.
	require.NoError(t, app.OpenChannelRelay())
	time.Sleep(100 * time.Millisecond)

	assert.Same(t, firstRelay, app.relay, "relay must be reused across OpenChannelRelay calls")

	mu.Lock()
	gotConnects := connectCount
	gotDisconnects := disconnectCount
	mu.Unlock()
	assert.Equal(t, 1, gotConnects, "hub should see exactly one relay connect")
	assert.Equal(t, 0, gotDisconnects, "hub should not see a disconnect")

	require.NoError(t, app.CloseChannelRelay())
}
