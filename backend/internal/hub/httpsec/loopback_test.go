package httpsec

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// The loopback list exists so three subsystems answer one question the same
// way: the CLI redirect allowlist, the CSP's form-action, and the WebAuthn
// relying-party derivation. The folding these helpers apply is the whole
// mechanism -- a set that widens in one place and not the others is either a
// hole or an outage, and neither shows up until a CLI login hangs.
//
// The helpers had no test of their own, so a change that dropped the
// lower-casing or the bracket trim would have broken passkeys on a loopback
// deployment with a green suite.

func TestNormalizeHost(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct{ in, want string }{
		"already canonical":      {"localhost", "localhost"},
		"upper case":             {"LOCALHOST", "localhost"},
		"mixed case":             {"LocalHost", "localhost"},
		"surrounding space":      {"  localhost  ", "localhost"},
		"bracketed IPv6":         {"[::1]", "::1"},
		"bracketed and spaced":   {" [::1] ", "::1"},
		"bracketed upper IPv6":   {"[::FFFF:127.0.0.1]", "::ffff:127.0.0.1"},
		"empty":                  {"", ""},
		"trailing root dot":      {"localhost.", "localhost."},
		"IPv4 literal untouched": {"127.0.0.1", "127.0.0.1"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, NormalizeHost(tc.in))
		})
	}
}

func TestIsLoopbackHost(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		in   string
		want bool
	}{
		// A URL's host keeps the case its author typed: net/url lower-cases
		// the scheme and nothing else. A membership test that does not fold
		// refuses a redirect_uri the CSP's form-action would admit.
		"localhost":      {"localhost", true},
		"LOCALHOST":      {"LOCALHOST", true},
		"MiXeD":          {"LocalHost", true},
		"127.0.0.1":      {"127.0.0.1", true},
		"IPv6 bare":      {"::1", true},
		"IPv6 bracketed": {"[::1]", true},
		"spaced":         {" localhost ", true},
		"empty":          {"", false},
		// Deliberately NOT loopback for a CLI redirect, although a browser
		// grants the whole 127.0.0.0/8 block a secure context. The two
		// questions differ, and captcha.isSecureContextHost states its own
		// wider rules rather than reusing this list.
		"127.0.0.2":         {"127.0.0.2", false},
		"sub.localhost":     {"app.localhost", false},
		"trailing root dot": {"localhost.", false},
		"LAN address":       {"192.168.1.5", false},
		"public hostname":   {"hub.example.com", false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, IsLoopbackHost(tc.in))
		})
	}
}

func TestIsLoopbackRedirectScheme(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		in   string
		want bool
	}{
		"http":   {"http", true},
		"https":  {"https", true},
		"HTTP":   {"HTTP", true},
		"spaced": {" https ", true},
		"empty":  {"", false},
		// The value reaches a Location header, so a scheme that executes
		// must not slip past a lower-case literal.
		"javascript": {"javascript", false},
		"JavaScript": {"JavaScript", false},
		"data":       {"data", false},
		"file":       {"file", false},
		"custom app": {"leapmux", false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, IsLoopbackRedirectScheme(tc.in))
		})
	}
}

// TestSplitBindHostPort pins the split three packages depend on. The port
// keeps its leading colon, so a caller rebuilds the input by concatenating,
// and an IPv6 literal's own colons must not be read as the separator.
func TestSplitBindHostPort(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct{ in, host, port string }{
		"port only":          {":4327", "", ":4327"},
		"ipv4 wildcard":      {"0.0.0.0:4327", "0.0.0.0", ":4327"},
		"ipv6 wildcard":      {"[::]:4327", "[::]", ":4327"},
		"ipv6 loopback":      {"[::1]:4327", "[::1]", ":4327"},
		"bare ipv6 no port":  {"[::]", "[::]", ""},
		"host with port":     {"hub.example.com:4327", "hub.example.com", ":4327"},
		"host with no port":  {"hub.example.com", "hub.example.com", ""},
		"empty":              {"", "", ""},
		"surrounding space":  {"  0.0.0.0:4327  ", "0.0.0.0", ":4327"},
		"long ipv6 wildcard": {"[0:0:0:0:0:0:0:0]:4327", "[0:0:0:0:0:0:0:0]", ":4327"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			host, port := SplitBindHostPort(tc.in)
			assert.Equal(t, tc.host, host, "host")
			assert.Equal(t, tc.port, port, "port")
			assert.Equal(t, strings.TrimSpace(tc.in), host+port, "host+port must rebuild the input")
		})
	}
}

// TestIsWildcardHost pins the CLASS, not the two spellings anybody writes.
//
// Three call sites read a literal pair before this helper existed, so
// "[::0]:4327" printed http://[::0]:4327 into every verification mail and
// switched ALTCHA off on a hub that serves only loopback. net.IP.IsUnspecified
// answers all of them.
func TestIsWildcardHost(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		in   string
		want bool
	}{
		"ipv4 wildcard":         {"0.0.0.0", true},
		"ipv6 wildcard":         {"::", true},
		"ipv6 wildcard bracket": {"[::]", true},
		"ipv6 wildcard zero":    {"::0", true},
		"ipv6 wildcard spelled": {"0:0:0:0:0:0:0:0", true},
		"ipv6 wildcard padded":  {"0000:0000:0000:0000:0000:0000:0000:0000", true},
		"ipv6 wildcard spaced":  {" [::0] ", true},
		// Every real host, loopback included: a wildcard specifies no host,
		// and localhost specifies one.
		"loopback ipv4": {"127.0.0.1", false},
		"loopback ipv6": {"::1", false},
		"localhost":     {"localhost", false},
		"lan address":   {"192.168.1.10", false},
		"hostname":      {"hub.example.com", false},
		// Not an address at all. An empty host is the callers' own case,
		// and each states its own rule for it.
		"empty": {"", false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, IsWildcardHost(tc.in))
		})
	}
}
