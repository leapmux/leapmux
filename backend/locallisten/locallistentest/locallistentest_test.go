package locallistentest

import (
	"net"
	"runtime"
	"strings"
	"testing"
)

// TestUniqueListenURL_BindsSuccessfully is a regression test for a bug where
// UniqueListenURL returned a path rooted at t.TempDir() that exceeded the
// 104-byte AF_UNIX sun_path limit on macOS (t.TempDir() embeds the full test
// name + a sequence counter, which combined with the caller's prefix and the
// ".sock" suffix overflows the limit). Callers of this helper must always
// receive a URL they can actually bind.
func TestUniqueListenURL_BindsSuccessfully(t *testing.T) {
	// Use a realistically long prefix to match real callers like the
	// "leapmux-solo-test" suite. The helper must still produce a bind-able
	// URL on every supported platform.
	url := UniqueListenURL(t, "leapmux-unique-url-probe")

	if runtime.GOOS == "windows" {
		if !strings.HasPrefix(url, "npipe:") {
			t.Fatalf("windows: want npipe:<name>, got %q", url)
		}
		// Named pipes don't have a strict path-length limit relevant here;
		// skip the bind probe (no test server required).
		return
	}

	const prefix = "unix:"
	if !strings.HasPrefix(url, prefix) {
		t.Fatalf("unix: want unix:<path>, got %q", url)
	}
	path := url[len(prefix):]

	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("bind %s (len=%d): %v", path, len(path), err)
	}
	_ = ln.Close()
}
