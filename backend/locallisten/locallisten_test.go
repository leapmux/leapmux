package locallisten

import (
	"errors"
	"testing"
)

func TestParse_UnixScheme(t *testing.T) {
	scheme, target, err := Parse("unix:/tmp/leapmux/hub.sock")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if scheme != SchemeUnix {
		t.Errorf("scheme = %q, want %q", scheme, SchemeUnix)
	}
	if target != "/tmp/leapmux/hub.sock" {
		t.Errorf("target = %q, want %q", target, "/tmp/leapmux/hub.sock")
	}
}

func TestParse_NpipeSchemeShortForm(t *testing.T) {
	scheme, target, err := Parse("npipe:leapmux-hub-S-1-5-21")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if scheme != SchemeNpipe {
		t.Errorf("scheme = %q, want %q", scheme, SchemeNpipe)
	}
	if target != "leapmux-hub-S-1-5-21" {
		t.Errorf("target = %q", target)
	}
}

func TestParse_NpipeSchemeFullNTPath(t *testing.T) {
	// Users sometimes paste the NT pipe path from Windows tooling; preserving
	// backslashes means the Listen path accepts it as-is.
	scheme, target, err := Parse(`npipe:\\.\pipe\leapmux-hub`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if scheme != SchemeNpipe {
		t.Errorf("scheme = %q", scheme)
	}
	if target != `\\.\pipe\leapmux-hub` {
		t.Errorf("target = %q", target)
	}
}

func TestParse_RejectsEmpty(t *testing.T) {
	_, _, err := Parse("")
	if !errors.Is(err, ErrUnsupportedScheme) {
		t.Fatalf("got %v, want ErrUnsupportedScheme", err)
	}
}

func TestParse_RejectsMissingTarget(t *testing.T) {
	for _, url := range []string{"unix:", "npipe:"} {
		_, _, err := Parse(url)
		if !errors.Is(err, ErrMissingTarget) {
			t.Errorf("%s: got %v, want ErrMissingTarget", url, err)
		}
		// A supported scheme with empty target is NOT an unsupported-scheme
		// error; the sentinels must be distinct.
		if errors.Is(err, ErrUnsupportedScheme) {
			t.Errorf("%s: error %v should not match ErrUnsupportedScheme", url, err)
		}
	}
}

func TestParse_RejectsUnknownSchemes(t *testing.T) {
	cases := []string{
		"http://localhost:8080",
		"tcp://host:1",
		"/plain/path",
		"leapmux-hub",
	}
	for _, url := range cases {
		_, _, err := Parse(url)
		if !errors.Is(err, ErrUnsupportedScheme) {
			t.Errorf("%s: got %v, want ErrUnsupportedScheme", url, err)
		}
	}
}
