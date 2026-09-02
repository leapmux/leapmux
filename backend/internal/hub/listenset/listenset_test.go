package listenset_test

import (
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/listenset"
)

func TestParse_Accepts(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		in       string
		wantKind listenset.Kind
		wantStr  string
		wantDial string
		wantPort int
	}{
		{":4327", listenset.KindAny, "*:4327", ":4327", 4327},
		{"*:4327", listenset.KindAny, "*:4327", ":4327", 4327},
		{"  *:4327  ", listenset.KindAny, "*:4327", ":4327", 4327},
		{"0.0.0.0:4327", listenset.KindAnyV4, "0.0.0.0:4327", "0.0.0.0:4327", 4327},
		{"[::]:4327", listenset.KindAnyV6, "[::]:4327", "[::]:4327", 4327},
		{"[::0]:4327", listenset.KindAnyV6, "[::]:4327", "[::]:4327", 4327},
		{"[0:0:0:0:0:0:0:0]:4327", listenset.KindAnyV6, "[::]:4327", "[::]:4327", 4327},
		{"127.0.0.1:4327", listenset.KindIP, "127.0.0.1:4327", "127.0.0.1:4327", 4327},
		{"192.168.1.24:8080", listenset.KindIP, "192.168.1.24:8080", "192.168.1.24:8080", 8080},
		{"[::1]:4327", listenset.KindIP, "[::1]:4327", "[::1]:4327", 4327},
		{"[fe80::1%en0]:4327", listenset.KindIP, "[fe80::1%en0]:4327", "[fe80::1%en0]:4327", 4327},
		{"hub.example:4327", listenset.KindHost, "hub.example:4327", "hub.example:4327", 4327},
		// Case folds, so two spellings of one host are one address.
		{"HUB.EXAMPLE:4327", listenset.KindHost, "hub.example:4327", "hub.example:4327", 4327},
		{"[::FFFF:127.0.0.1]:4327", listenset.KindIP, "[::ffff:127.0.0.1]:4327", "[::ffff:127.0.0.1]:4327", 4327},
		{"1.2.3.4:1", listenset.KindIP, "1.2.3.4:1", "1.2.3.4:1", 1},
		{"1.2.3.4:65535", listenset.KindIP, "1.2.3.4:65535", "1.2.3.4:65535", 65535},
		// Port 0 asks the operating system to choose. -listen may do that, so
		// Parse must accept it; the settings validator refuses it separately,
		// because a STORED address of port 0 names a port nobody can be told.
		{"127.0.0.1:0", listenset.KindIP, "127.0.0.1:0", "127.0.0.1:0", 0},
	} {
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			a, err := listenset.Parse(tc.in)
			require.NoError(t, err)
			assert.Equal(t, tc.wantKind, a.Kind(), "kind")
			assert.Equal(t, tc.wantStr, a.String(), "canonical string")
			assert.Equal(t, tc.wantDial, a.DialAddr(), "dial address")
			assert.Equal(t, tc.wantPort, a.Port(), "port")
		})
	}
}

func TestParse_Refuses(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"no port", "192.168.1.24"},
		{"bare port", "4327"},
		{"unbracketed IPv6 with port is ambiguous", "::1:4327"},
		{"port above the range", "127.0.0.1:65536"},
		{"negative port", "127.0.0.1:-1"},
		{"non-numeric port", "127.0.0.1:http"},
		{"empty port", "127.0.0.1:"},
		{"two ports", "127.0.0.1:80:90"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := listenset.Parse(tc.in)
			assert.Error(t, err, "expected %q to be refused", tc.in)
		})
	}
}

// A canonical DialAddr must be something net.Listen actually accepts. The
// wildcard spelling is this package's own, so the conversion is the one place
// it could be wrong, and a unit test on the string alone would not notice.
func TestDialAddr_BindsForReal(t *testing.T) {
	t.Parallel()
	for _, in := range []string{"*:0", ":0", "127.0.0.1:0", "[::1]:0"} {
		t.Run(in, func(t *testing.T) {
			t.Parallel()
			a, err := listenset.Parse(in)
			require.NoError(t, err)
			ln, err := net.Listen("tcp", a.DialAddr())
			require.NoError(t, err, "dial address %q must bind", a.DialAddr())
			require.NoError(t, ln.Close())
		})
	}
}

// WithPort is how the listener set reports the port the operating system
// chose. It must keep the KIND and the host, because a wildcard's identity is
// what the merge and the live-listener map are keyed on -- reading the whole
// address back from the socket would turn "*:0" into "[::]:54321".
func TestWithPort(t *testing.T) {
	t.Parallel()
	wildcard := listenset.MustParse("*:0").WithPort(54321)
	assert.Equal(t, "*:54321", wildcard.String())
	assert.Equal(t, ":54321", wildcard.DialAddr())
	assert.Equal(t, listenset.KindAny, wildcard.Kind())

	loopback := listenset.MustParse("127.0.0.1:0").WithPort(54321)
	assert.Equal(t, "127.0.0.1:54321", loopback.String())
	assert.True(t, loopback.IsLoopback(), "the host decides loopback, and the port did not change it")
}

func TestIsLoopback(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"127.0.0.1:4327", true},
		{"127.0.0.5:4327", true}, // the whole 127.0.0.0/8 block
		{"[::1]:4327", true},
		{"192.168.1.24:4327", false},
		{"100.64.1.2:4327", false}, // the Tailscale CGNAT range
		// Every wildcard answers on the loopback interface AND on others, so
		// calling one loopback would report an exposed hub as private.
		{"*:4327", false},
		{":4327", false},
		{"0.0.0.0:4327", false},
		{"[::]:4327", false},
		// A name resolves to addresses this package cannot enumerate, so the
		// safe answer to "is this exposed" is yes.
		{"localhost:4327", false},
		{"hub.example:4327", false},
	} {
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, listenset.MustParse(tc.in).IsLoopback())
		})
	}
}

func TestCovers(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		// A different port is never covered, whatever the hosts say.
		{"*:4327", "127.0.0.1:9000", false},
		{"0.0.0.0:4327", "0.0.0.0:9000", false},

		// The family-neutral wildcard covers everything on its port.
		{"*:4327", "127.0.0.1:4327", true},
		{"*:4327", "[::1]:4327", true},
		{"*:4327", "0.0.0.0:4327", true},
		{"*:4327", "[::]:4327", true},
		{"*:4327", "hub.example:4327", true},
		{"*:4327", "*:4327", true},

		// The IPv4 wildcard covers IPv4 and nothing else.
		{"0.0.0.0:4327", "192.168.1.24:4327", true},
		{"0.0.0.0:4327", "0.0.0.0:4327", true},
		{"0.0.0.0:4327", "[::ffff:127.0.0.1]:4327", true}, // mapped: the IPv4 stack
		{"0.0.0.0:4327", "[::1]:4327", false},
		{"0.0.0.0:4327", "[::]:4327", false},
		{"0.0.0.0:4327", "*:4327", false},
		{"0.0.0.0:4327", "hub.example:4327", false},

		// The IPv6 wildcard covers IPv6 and nothing else.
		{"[::]:4327", "[::1]:4327", true},
		{"[::]:4327", "[::]:4327", true},
		{"[::]:4327", "192.168.1.24:4327", false},
		{"[::]:4327", "[::ffff:127.0.0.1]:4327", false},
		{"[::]:4327", "0.0.0.0:4327", false},
		{"[::]:4327", "*:4327", false},

		// A literal and a name cover an equal address and nothing else.
		{"127.0.0.1:4327", "127.0.0.1:4327", true},
		{"127.0.0.1:4327", "127.0.0.2:4327", false},
		{"127.0.0.1:4327", "*:4327", false},
		{"hub.example:4327", "hub.example:4327", true},
		{"hub.example:4327", "127.0.0.1:4327", false},
	} {
		t.Run(tc.a+" covers "+tc.b, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, listenset.MustParse(tc.a).Covers(listenset.MustParse(tc.b)))
		})
	}
}

func TestMerge(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		base   string // "" means no base (the NoTCP desktop)
		extras []string
		want   []string
	}{
		{
			name: "no base and no extras binds nothing",
			want: nil,
		},
		{
			name: "the base alone",
			base: "127.0.0.1:4327",
			want: []string{"127.0.0.1:4327"},
		},
		{
			name:   "an extra alone, with no base",
			extras: []string{"192.168.1.24:8080"},
			want:   []string{"192.168.1.24:8080"},
		},
		{
			// The example from the requirement.
			name:   "a wildcard extra absorbs the loopback base on its port",
			base:   "127.0.0.1:4327",
			extras: []string{"*:4327"},
			want:   []string{"*:4327"},
		},
		{
			name:   "an extra equal to the base collapses to one",
			base:   "127.0.0.1:4327",
			extras: []string{"127.0.0.1:4327"},
			want:   []string{"127.0.0.1:4327"},
		},
		{
			name:   "two equal extras collapse to one, and neither drops the other",
			base:   "127.0.0.1:4327",
			extras: []string{"*:9000", "*:9000"},
			want:   []string{"127.0.0.1:4327", "*:9000"},
		},
		{
			name:   "a wildcard on another port leaves the base alone",
			base:   "127.0.0.1:4327",
			extras: []string{"*:9000"},
			want:   []string{"127.0.0.1:4327", "*:9000"},
		},
		{
			name:   "a specific extra survives beside a loopback base",
			base:   "127.0.0.1:4327",
			extras: []string{"192.168.1.24:8080"},
			want:   []string{"127.0.0.1:4327", "192.168.1.24:8080"},
		},
		{
			name:   "an IPv4 wildcard base absorbs an IPv4 extra and keeps an IPv6 one",
			base:   "0.0.0.0:4327",
			extras: []string{"192.168.1.24:4327", "[::1]:4327"},
			want:   []string{"0.0.0.0:4327", "[::1]:4327"},
		},
		{
			name:   "a wildcard extra absorbs both wildcards and every literal on its port",
			base:   "0.0.0.0:4327",
			extras: []string{"*:4327", "[::]:4327", "192.168.1.24:4327"},
			want:   []string{"*:4327"},
		},
		{
			name:   "the result is sorted by port, then widest kind, then host",
			extras: []string{"192.168.1.24:9000", "*:9000", "127.0.0.5:80", "10.0.0.5:80"},
			want:   []string{"10.0.0.5:80", "127.0.0.5:80", "*:9000"},
		},
		{
			name:   "a name is never absorbed by an address it may not resolve to",
			base:   "127.0.0.1:4327",
			extras: []string{"hub.example:9000"},
			want:   []string{"127.0.0.1:4327", "hub.example:9000"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var base *listenset.Addr
			if tc.base != "" {
				b := listenset.MustParse(tc.base)
				base = &b
			}
			extras, err := listenset.ParseAll(tc.extras)
			require.NoError(t, err)

			got := listenset.Strings(listenset.Merge(base, extras))
			assert.Equal(t, tc.want, emptyToNil(got))
		})
	}
}

// Merge returns an empty slice for an empty input; the table writes that case
// as nil, because "binds nothing" is what it means and the distinction carries
// nothing for a caller that ranges over the result.
func emptyToNil(v []string) []string {
	if len(v) == 0 {
		return nil
	}
	return v
}

// Merge must never return a set the OS refuses on the pair it DOES understand:
// no two results may cover each other. The table above pins the answers; this
// pins the property across all of them.
func TestMerge_ResultHoldsNoCoveringPair(t *testing.T) {
	t.Parallel()
	base := listenset.MustParse("127.0.0.1:4327")
	extras, err := listenset.ParseAll([]string{
		"*:4327", "0.0.0.0:4327", "[::]:4327", "192.168.1.24:4327",
		"192.168.1.24:8080", "*:8080", "[::1]:9000",
	})
	require.NoError(t, err)

	got := listenset.Merge(&base, extras)
	for i, a := range got {
		for j, b := range got {
			if i == j {
				continue
			}
			assert.Falsef(t, a.Covers(b), "%s covers %s, so the merge left a pair the OS refuses", a, b)
		}
	}
}

func TestParseAll_ReportsTheOffendingIndex(t *testing.T) {
	t.Parallel()
	_, err := listenset.ParseAll([]string{"127.0.0.1:4327", "nonsense"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "address 2:", "the message must name which entry failed")
}
