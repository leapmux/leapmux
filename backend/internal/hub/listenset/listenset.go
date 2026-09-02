// Package listenset answers one question: given the address `-listen` gives
// and the extra addresses an administrator stored, which sockets does the hub
// bind?
//
// The answer is not the union of the two. A wildcard bind already answers on
// every address of its family, so binding `*:4327` beside `127.0.0.1:4327`
// fails with EADDRINUSE rather than serving both. Merge folds a covered
// address into the address that covers it, so the caller states what it wants
// and this package states what the operating system will accept.
package listenset

import (
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/hub/httpsec"
)

// Kind classifies what an address SPECIFIES, which is what decides whether one
// address covers another. The order is widest first, and Merge sorts by it.
type Kind int

const (
	// KindAny is the family-neutral wildcard: an empty host or "*". It binds
	// as ":port", which Go serves over IPv4 and IPv6 alike.
	KindAny Kind = iota
	// KindAnyV4 is "0.0.0.0" and its spellings: every IPv4 address.
	KindAnyV4
	// KindAnyV6 is "::" and its spellings: every IPv6 address.
	KindAnyV6
	// KindIP is one IP literal, with an optional zone ("fe80::1%en0").
	KindIP
	// KindHost is a name. Only `-listen` can produce one -- the settings
	// validator refuses it, and the address picker cannot offer it -- and it
	// covers nothing but an identical address, because the names it resolves
	// to are not knowable here.
	KindHost
)

// String renders the kind for diagnostics.
func (k Kind) String() string {
	switch k {
	case KindAny:
		return "any"
	case KindAnyV4:
		return "any-ipv4"
	case KindAnyV6:
		return "any-ipv6"
	case KindIP:
		return "ip"
	case KindHost:
		return "host"
	default:
		return "unknown"
	}
}

// Addr is one canonical listen address. The fields are unexported and every
// value comes from Parse, because Kind is DERIVED from the host and a struct
// literal could state a kind the host contradicts -- and Covers, the whole
// merge rule, reads nothing but the kind.
//
// The zero value is not a usable address; callers hold a *Addr where "no
// address" is a case (the hub has no TCP base under NoTCP).
type Addr struct {
	host string     // canonical host: "" for KindAny, else the folded literal or name
	ip   netip.Addr // valid for KindAnyV4, KindAnyV6 and KindIP
	port int
	kind Kind
}

// Parse reads a listen address. It requires a port, because every caller binds
// the result and a port is not optional there. This is why it does not use
// httpsec.SplitBindHostPort, which exists for the opposite case: reporting
// what an address specifies when the port may be absent.
//
// Accepted: ":4327", "*:4327", "0.0.0.0:4327", "[::]:4327", "192.168.1.24:8080",
// "[::1]:4327", "[fe80::1%en0]:4327", "hub.example:4327".
//
// An unbracketed IPv6 literal with a port ("::1:4327") is REFUSED, and
// deliberately: its last colon is indistinguishable from a port separator, so
// accepting it would mean guessing which of two valid readings the operator
// meant.
func Parse(s string) (Addr, error) {
	host, portStr, err := net.SplitHostPort(strings.TrimSpace(s))
	if err != nil {
		return Addr{}, fmt.Errorf("listen address %q is not host:port (bracket an IPv6 literal, e.g. [::1]:4327): %w", s, err)
	}
	// net.LookupPort, not strconv.Atoi, because net.Listen resolves the port
	// this way and Parse must accept every address that used to bind. It reads
	// the machine's services table, so `-listen 127.0.0.1:http` gives 80, and
	// it maps an EMPTY port to 0, so `-listen localhost:` still asks the
	// operating system to choose. It also does the range check, and refuses a
	// service name the machine does not list.
	//
	// The resolved NUMBER is what the address carries from here on. A stored
	// or logged address then states the port a client can dial, which the
	// service name does not on a machine with a different services table.
	port, err := net.LookupPort("tcp", portStr)
	if err != nil {
		return Addr{}, fmt.Errorf("listen address %q has no usable port %q: %w", s, portStr, err)
	}
	// Port 0 is ACCEPTED here, and it means "let the operating system choose".
	// It is what a test harness or an embedded launcher passes to -listen to
	// get a free port, so refusing it would take a working option away.
	//
	// It is refused where it is meaningless instead: a STORED extra address of
	// port 0 specifies a port nobody can be told, so settings.validateExtraListen
	// rejects it. The listener set resolves the real port from the bound
	// socket, so nothing downstream ever reports ":0" as a served address.

	host = strings.TrimSpace(host)
	if host == "" || host == contracts.ListenAnyHost {
		return Addr{port: port, kind: KindAny}, nil
	}

	// netip rather than net.ParseIP: net.ParseIP returns nil for a zoned
	// link-local address ("fe80::1%en0"), which is a real thing to bind to on
	// a machine with more than one link-local interface.
	//
	// The host reaches netip WITH ITS CASE. An IPv6 zone is an interface name,
	// and net.Listen resolves it through net.InterfaceByName, which matches
	// exactly -- so folding the case here would turn `%Ethernet` on Windows
	// into `%ethernet`, which no interface answers to, and every link-local
	// address the picker offers on such a machine would fail to bind for ever.
	// netip lower-cases the hexadecimal digits itself and leaves the zone
	// alone, which is the split this package wants.
	ip, ipErr := netip.ParseAddr(host)
	if ipErr != nil {
		// A NAME is case-insensitive, so it folds. Only a name reaches here.
		return Addr{host: strings.ToLower(host), port: port, kind: KindHost}, nil
	}
	// ip.String() and never the host as written, for EVERY kind. The canonical
	// string is this package's identity for an address -- Merge dedupes on it
	// and the listener set keys its live listeners by it -- and one socket has
	// to have one identity. "[::0]:4327" and "[0:0:0:0:0:0:0:0]:4327" are the
	// same bind, so keeping the two spellings apart would put them both in a
	// merge result and make the second bind fail with EADDRINUSE.
	switch {
	case ip.IsUnspecified() && isV4(ip):
		return Addr{host: ip.String(), ip: ip, port: port, kind: KindAnyV4}, nil
	case ip.IsUnspecified():
		return Addr{host: ip.String(), ip: ip, port: port, kind: KindAnyV6}, nil
	default:
		return Addr{host: ip.String(), ip: ip, port: port, kind: KindIP}, nil
	}
}

// MustParse is Parse for a literal a test or a package-level declaration knows
// is well formed. It panics on a bad address.
func MustParse(s string) Addr {
	a, err := Parse(s)
	if err != nil {
		panic(err)
	}
	return a
}

// isV4 reports whether the address is IPv4 or an IPv4-mapped IPv6 address.
// A mapped address binds the IPv4 stack, so the two answer the same question
// everywhere in this package.
func isV4(ip netip.Addr) bool { return ip.Is4() || ip.Is4In6() }

// WithPort returns the same address on another port.
//
// It exists for ONE caller: the listener set, which asks for port 0 and must
// then report the port the operating system chose. Resolving the whole address
// from the bound socket instead would change a wildcard's identity -- Go
// reports a ":4327" bind as "[::]:4327" -- and the identity is what the merge
// and the live-listener map are keyed on.
func (a Addr) WithPort(port int) Addr {
	a.port = port
	return a
}

// Port returns the port.
func (a Addr) Port() int { return a.port }

// Kind returns what the address specifies.
func (a Addr) Kind() Kind { return a.kind }

// String is the canonical spelling, and it is the IDENTITY of an address:
// Merge dedupes on it, and the listener set keys its live listeners by it. The
// family-neutral wildcard prints as "*" rather than as an empty host, so a
// reader of a log line or of the settings row sees a host in every entry.
func (a Addr) String() string {
	if a.kind == KindAny {
		return contracts.ListenAnyHost + ":" + strconv.Itoa(a.port)
	}
	return net.JoinHostPort(a.host, strconv.Itoa(a.port))
}

// DialAddr is what net.Listen("tcp", ...) takes. It differs from String in one
// place: the family-neutral wildcard is ":port", because "*" is this package's
// spelling and not the net package's.
func (a Addr) DialAddr() string {
	if a.kind == KindAny {
		return ":" + strconv.Itoa(a.port)
	}
	return net.JoinHostPort(a.host, strconv.Itoa(a.port))
}

// IsLoopback reports whether the address answers ONLY on this machine.
//
// Every wildcard is false, including KindAnyV4 and KindAnyV6: they answer on
// the loopback interface AND on every other one, so treating them as loopback
// would call an exposed hub private.
//
// A NAME is false, with one exception. The addresses a name resolves to are not
// knowable here, so the safe answer to "is this exposed" is yes -- but
// "localhost" identifies this machine by a convention every operating system ships,
// and `-listen localhost:4327` exposes nothing. Calling it exposed would demand
// a password for a hub nobody else can reach. httpsec.LoopbackHosts is where
// that convention lives, and reading it here keeps the two answers from
// drifting apart. The residual: an operator who redefines localhost in
// /etc/hosts to a routable address gets no warning, on a machine they control.
//
// An IP literal takes netip's own predicate rather than httpsec.IsLoopbackHost,
// which answers a narrower question: that one matches the three literals an
// OAuth redirect may name, while a bind address may be any of 127.0.0.0/8.
func (a Addr) IsLoopback() bool {
	switch a.kind {
	case KindIP:
		return a.ip.IsLoopback()
	case KindHost:
		return httpsec.IsLoopbackHost(a.host)
	default:
		return false
	}
}

// Covers reports whether binding a makes binding b both redundant and
// impossible: they share a port, and a already answers on every address b
// would.
//
// This is the whole merge rule, and it is deliberately small. It is also
// deliberately incomplete in one place: on a host with net.ipv6.bindv6only=0,
// "::" takes the IPv4 stack as well, so "[::]:4327" and "192.168.1.24:4327"
// collide although neither covers the other here. A sysctl is not readable
// from an address, so Merge cannot fold that pair; the bind fails with
// EADDRINUSE and the caller reports it. Only a hand-written -listen reaches
// the case, because the address picker offers "*" and never "::".
func (a Addr) Covers(b Addr) bool {
	if a.port != b.port {
		return false
	}
	switch a.kind {
	case KindAny:
		return true
	case KindAnyV4:
		if b.kind == KindAnyV4 {
			return true
		}
		return b.kind == KindIP && isV4(b.ip)
	case KindAnyV6:
		if b.kind == KindAnyV6 {
			return true
		}
		return b.kind == KindIP && !isV4(b.ip)
	default:
		return a.String() == b.String()
	}
}

// Merge returns the addresses the hub actually binds: base and extras, with
// every duplicate removed and every covered address folded into the address
// that covers it.
//
// The dedupe runs FIRST, and it has to. Two equal addresses cover each other,
// so a coverage pass over a list holding both would drop both and leave the
// hub with no socket at all.
//
// The result is sorted -- port, then widest kind, then host -- so a caller can
// compare two results, and so a log line and the panel's "serving now" list
// read in the same order every time.
func Merge(base *Addr, extras []Addr) []Addr {
	all := make([]Addr, 0, len(extras)+1)
	if base != nil {
		all = append(all, *base)
	}
	all = append(all, extras...)

	seen := make(map[string]bool, len(all))
	uniq := make([]Addr, 0, len(all))
	for _, a := range all {
		s := a.String()
		if seen[s] {
			continue
		}
		seen[s] = true
		uniq = append(uniq, a)
	}

	out := make([]Addr, 0, len(uniq))
	for i, a := range uniq {
		covered := false
		for j, b := range uniq {
			if i == j || !b.Covers(a) {
				continue
			}
			// A mutual cover cannot arise from two DIFFERENT canonical
			// strings today, because only an equal pair covers both ways and
			// the dedupe removed those. Keeping the earlier entry anyway
			// means a kind added later cannot make the two drop each other.
			if a.Covers(b) && j > i {
				continue
			}
			covered = true
			break
		}
		if !covered {
			out = append(out, a)
		}
	}

	slices.SortFunc(out, func(x, y Addr) int {
		if x.port != y.port {
			return x.port - y.port
		}
		if x.kind != y.kind {
			return int(x.kind) - int(y.kind)
		}
		return strings.Compare(x.host, y.host)
	})
	return out
}

// ParseAll parses every address in order and reports the first failure with
// its index, so a caller validating a stored list can say which entry is bad.
func ParseAll(addrs []string) ([]Addr, error) {
	out := make([]Addr, 0, len(addrs))
	for i, s := range addrs {
		a, err := Parse(s)
		if err != nil {
			return nil, fmt.Errorf("address %d: %w", i+1, err)
		}
		out = append(out, a)
	}
	return out, nil
}

// Strings renders a list canonically, for a log field or a wire message.
func Strings(addrs []Addr) []string {
	out := make([]string, len(addrs))
	for i, a := range addrs {
		out[i] = a.String()
	}
	return out
}

// Source says why the hub binds an address, for the administration surface
// that reports what is serving.
//
// The tokens come from contracts/listen.json, because the browser renders one
// label per token and the proto field carries them as a plain string: nothing
// but the contract keeps the two sides on the same three words.
type Source string

const (
	// SourceListen is the address -listen gives, bound alone.
	SourceListen Source = contracts.AddressSourceListen
	// SourceExtra is an address the extra_listen_addresses setting adds.
	SourceExtra Source = contracts.AddressSourceExtra
	// SourceMerged is an address that is BOTH: the operator asked for it and
	// it also covers what -listen gives, so one socket serves both. The panel
	// says so, because "127.0.0.1:4327 is gone" and "127.0.0.1:4327 is served
	// by *:4327" look identical in a list that only states what is bound.
	SourceMerged Source = contracts.AddressSourceMerged
)

// Bound is one entry of a hub's listener report: an address it serves on, or
// one it was asked to serve on and could not bind.
//
// It lives HERE, beside the address model, because both sides of the report
// already import this package -- the hub that owns the sockets and the service
// that answers the administration RPC. A mirrored struct in the service
// package cost a conversion at the boundary and stringified the address, which
// takes String() and DialAddr() away from every consumer: those two differ
// exactly at the wildcard, and the difference is what decides between a
// working mail link and `http://*:4327`.
type Bound struct {
	Addr   Addr
	Source Source
	// Err is why this address is NOT serving, and empty when it is. A stored
	// address whose interface went away must not fail the hub, so the failure
	// travels to the administration surface instead of to a startup error.
	Err string
}

// AnyNonLoopback reports whether any SERVING address in the report answers on
// an address another machine can reach.
//
// A failed entry does not count: the hub is not reachable at an address it
// could not bind.
func AnyNonLoopback(bound []Bound) bool {
	for _, b := range bound {
		if b.Err == "" && !b.Addr.IsLoopback() {
			return true
		}
	}
	return false
}
