package main

import (
	"testing"

	desktoppb "github.com/leapmux/leapmux/generated/proto/leapmux/desktop/v1"
)

func TestSetWindowSizePersistsMode(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	a := &App{config: &DesktopConfig{WindowWidth: 1280, WindowHeight: 800, WindowMode: WindowModeNormal}}

	// Entering fullscreen: the frontend sends 0 dimensions so the last windowed
	// size survives while the mode flips.
	if err := a.SetWindowSize(0, 0, WindowModeFullscreen); err != nil {
		t.Fatalf("SetWindowSize: %v", err)
	}
	if a.config.WindowWidth != 1280 || a.config.WindowHeight != 800 {
		t.Fatalf("windowed size not preserved: got %dx%d", a.config.WindowWidth, a.config.WindowHeight)
	}
	if a.config.WindowMode != WindowModeFullscreen {
		t.Fatalf("mode = %q, want %q", a.config.WindowMode, WindowModeFullscreen)
	}

	// The change round-trips to disk.
	reloaded, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if reloaded.WindowMode != WindowModeFullscreen || reloaded.WindowWidth != 1280 || reloaded.WindowHeight != 800 {
		t.Fatalf("reloaded config = %+v", reloaded)
	}

	// Returning to a windowed size updates both dimensions and mode.
	if err := a.SetWindowSize(1000, 700, WindowModeNormal); err != nil {
		t.Fatalf("SetWindowSize: %v", err)
	}
	if a.config.WindowWidth != 1000 || a.config.WindowHeight != 700 || a.config.WindowMode != WindowModeNormal {
		t.Fatalf("windowed restore failed: %+v", a.config)
	}
}

func TestWindowModeProtoRoundTrip(t *testing.T) {
	for _, mode := range []string{WindowModeNormal, WindowModeMaximized, WindowModeFullscreen} {
		if got := windowModeFromProto(windowModeToProto(mode)); got != mode {
			t.Errorf("round-trip %q -> %q", mode, got)
		}
	}

	// Empty / unknown strings collapse to normal (fresh-config default).
	if got := windowModeToProto(""); got != desktoppb.WindowMode_WINDOW_MODE_NORMAL {
		t.Errorf("empty mode -> %v, want NORMAL", got)
	}
	if got := windowModeToProto("bogus"); got != desktoppb.WindowMode_WINDOW_MODE_NORMAL {
		t.Errorf("bogus mode -> %v, want NORMAL", got)
	}

	// UNSPECIFIED on the wire maps back to normal.
	if got := windowModeFromProto(desktoppb.WindowMode_WINDOW_MODE_UNSPECIFIED); got != WindowModeNormal {
		t.Errorf("unspecified -> %q, want %q", got, WindowModeNormal)
	}
}
