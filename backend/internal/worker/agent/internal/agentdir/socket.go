package agentdir

import "runtime"

// MaxSocketPathBytes is the longest path that a Unix domain socket can take on
// this platform: the size of sun_path, less one byte for the NUL that ends the
// path. The BSD family, macOS included, holds 104 bytes, and Linux and Windows
// hold 108.
//
// New measures Spec.SocketName against it, so a provider states the name of
// its socket and never the limit.
func MaxSocketPathBytes() int {
	switch runtime.GOOS {
	case "darwin", "ios", "freebsd", "netbsd", "openbsd", "dragonfly":
		return 103
	default:
		return 107
	}
}
