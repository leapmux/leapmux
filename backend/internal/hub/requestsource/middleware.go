package requestsource

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"

	"github.com/realclientip/realclientip-go"

	"github.com/leapmux/leapmux/internal/hub/peer"
	"github.com/leapmux/leapmux/internal/hub/settings"
)

const (
	forwardedHeader          = "Forwarded"
	xForwardedForHeader      = "X-Forwarded-For"
	xForwardedProtocolHeader = "X-Forwarded-Proto"
)

// TrustedRangesFunc supplies the configured trusted proxies for one request.
//
// It is a function rather than the settings manager itself so the middleware
// depends on the ONE value it reads instead of the whole settings surface, and
// so a test can state a trust set without opening a store. RangesFromSettings
// is the production spelling.
type TrustedRangesFunc func(context.Context) TrustedRanges

// RangesFromSettings reads the configured trusted proxies from a settings
// manager. A nil manager trusts nothing, which is the shipped default.
func RangesFromSettings(manager *settings.Manager) TrustedRangesFunc {
	if manager == nil {
		return func(context.Context) TrustedRanges { return TrustedRanges{} }
	}
	return func(ctx context.Context) TrustedRanges {
		return KeyTrustedProxyRanges.Of(manager.Snapshot(ctx))
	}
}

// Middleware records the verified client IP and protocol on the request
// context. It leaves Request.RemoteAddr, Request.TLS and Request.URL
// unchanged.
//
// Both answers go in the CONTEXT rather than onto the request, and the
// protocol used to be written to `URL.Scheme` instead. Nothing read it there:
// `URL.RequestURI()` ignores the scheme for a server request, and no handler
// in this hub consults it. The context is where every consumer that needs the
// answer already looks -- a Connect interceptor holds one, and a WebSocket
// handler holds a request whose context descends from the same connection --
// so one home for the fact is also the only home a caller can reach.
//
// A SHALLOW copy, not r.Clone. This runs on every request the hub serves, and
// Clone deep-copies the header map, the trailer, the transfer encoding and the
// parsed forms. Nothing here writes a field of the request itself, so
// `r.WithContext` is the whole copy that is needed.
func Middleware(trustedRanges TrustedRangesFunc, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		trusted := trustedRanges(r.Context())
		clientIP, scheme := resolve(r, trusted)
		ctx := peer.WithHTTPS(peer.WithClientIP(r.Context(), clientIP), scheme == "https")
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func resolve(r *http.Request, trusted TrustedRanges) (string, string) {
	directScheme := "http"
	if r.TLS != nil {
		directScheme = "https"
	}

	// The UNCHANGED transport peer decides everything below. A peer no
	// strategy can parse -- the local IPC socket, a test transport -- reports
	// an unknown client, and no header may supply one in its place.
	peerIP := realclientip.RemoteAddrStrategy{}.ClientIP(nil, r.RemoteAddr)
	peerAddr, err := netip.ParseAddr(peerIP)
	if err != nil {
		return "", directScheme
	}
	// The ZONE is dropped for the TRUST TEST alone. An accepted link-local
	// IPv6 connection carries one (`fe80::1%en0`), and netipx refuses a zoned
	// address outright -- `IPSet.Contains` returns false before it compares
	// anything. So a trusted range would never match a link-local proxy, and
	// the operator would see their configured proxy's headers ignored with no
	// error and no log line. A zone identifies the INTERFACE the address is
	// reachable on; the range is a set of addresses, and the two are separate
	// questions.
	//
	// The zone stays on peerIP, which is what the caller reports as the
	// client, because there it IS part of the identity: two peers on different
	// interfaces can carry the same link-local address.
	if !trusted.Contains(peerAddr.WithZone("")) {
		// Every forwarding header is caller-controlled here, so the peer
		// itself is the rightmost untrusted address and the connection states
		// its own protocol.
		return peerIP, directScheme
	}
	// Forwarded WINS whenever it exists, and there is no fallback: a proxy
	// that sends it owns the whole answer, so reading the X-Forwarded headers
	// after a malformed one would let a second writer supply the client.
	if _, exists := r.Header[forwardedHeader]; exists {
		return resolveForwarded(r, trusted)
	}
	return resolveXForwarded(r, trusted)
}

// resolveForwarded answers from RFC 7239 Forwarded. A malformed header, or a
// chain whose addresses are all trusted, reports an unknown client.
func resolveForwarded(r *http.Request, trusted TrustedRanges) (string, string) {
	elements, err := parseForwarded(r.Header[forwardedHeader])
	if err != nil {
		return "", "http"
	}
	index, ok := rightmostUntrusted(elements, trusted)
	if !ok {
		return "", "http"
	}
	return elements[index].address.Unmap().String(), validProtocol(elements[index].protocol)
}

// resolveXForwarded answers from X-Forwarded-For and X-Forwarded-Proto, which
// carry no element metadata: the protocol list stands beside the address list
// rather than inside it.
func resolveXForwarded(r *http.Request, trusted TrustedRanges) (string, string) {
	addresses, err := parseXForwardedFor(r.Header[xForwardedForHeader])
	if err != nil {
		return "", "http"
	}
	index, ok := rightmostUntrusted(addresses, trusted)
	if !ok {
		return "", "http"
	}
	return addresses[index].address.Unmap().String(),
		xForwardedProtocol(r.Header[xForwardedProtocolHeader], index, len(addresses))
}

// rightmostUntrusted walks the chain from the right and reports the first
// element that is not a trusted proxy: the client, as far as the trusted
// infrastructure can vouch for it.
//
// It walks the elements THIS package parsed, rather than handing a rebuilt
// header back to the IP library and then finding the chosen element again by
// matching its address as a string. That round trip needed two parsers to keep
// agreeing about where one element ends and the next begins, re-parsed every
// address, and re-tested every hop against a linear scan of the same range set
// this package already holds as a sorted set.
//
// The rule is the library's, unchanged. Skip an element whose address is valid
// AND trusted. Stop at the first element that is not skipped. An element with
// NO usable address stops the walk with no answer, because an obfuscated or
// `unknown` node hides exactly the address the caller would report -- so the
// chain names no client, and inventing one from further left would let a proxy
// nominate any address it liked. A chain that is empty, or trusted end to end,
// also names no client.
//
// The caller reaching here already proved the transport peer is trusted, so
// the library's own "untrusted peer, answer with the peer" branch is
// unreachable from this package and has no counterpart here.
func rightmostUntrusted(elements []forwardedElement, trusted TrustedRanges) (int, bool) {
	for index := len(elements) - 1; index >= 0; index-- {
		address := elements[index].address
		if address.IsValid() && trusted.Contains(address) {
			continue
		}
		return index, address.IsValid()
	}
	return 0, false
}

type forwardedElement struct {
	address  netip.Addr
	protocol string
}

func parseForwarded(values []string) ([]forwardedElement, error) {
	items, err := splitElements(values)
	if err != nil {
		return nil, fmt.Errorf("invalid Forwarded header")
	}
	elements := make([]forwardedElement, 0, len(items))
	for _, item := range items {
		parts, err := splitOutsideQuotes(item, ';')
		if err != nil {
			return nil, fmt.Errorf("invalid Forwarded element")
		}
		element := forwardedElement{}
		// A SLICE, not a map. RFC 7239 gives an element four parameter names,
		// so this scan is over a handful of entries, and a map would allocate
		// once per element of every header the hub parses.
		seen := make([]string, 0, len(parts))
		for _, part := range parts {
			name, rawValue, ok := strings.Cut(strings.TrimSpace(part), "=")
			name = strings.ToLower(strings.TrimSpace(name))
			rawValue = strings.TrimSpace(rawValue)
			if !ok || !isToken(name) || rawValue == "" {
				return nil, fmt.Errorf("invalid Forwarded parameter")
			}
			// A REPEATED parameter is refused rather than resolved. RFC 7239
			// forbids it, and two `for` values in one element are a request to
			// pick -- so any choice here would be a parser deciding which
			// client address an attacker meant.
			if slices.Contains(seen, name) {
				return nil, fmt.Errorf("duplicate Forwarded parameter %q", name)
			}
			seen = append(seen, name)
			value, quoted, err := parseParameterValue(rawValue)
			if err != nil {
				return nil, err
			}
			switch name {
			case "for":
				addr, present, err := parseForwardedNode(value, quoted)
				if err != nil {
					return nil, err
				}
				if present {
					element.address = addr
				}
			case "proto":
				element.protocol = value
			}
		}
		elements = append(elements, element)
	}
	return elements, nil
}

func parseXForwardedFor(values []string) ([]forwardedElement, error) {
	items, err := splitElements(values)
	if err != nil {
		return nil, fmt.Errorf("invalid X-Forwarded-For header")
	}
	elements := make([]forwardedElement, 0, len(items))
	for _, item := range items {
		addr, err := parseHeaderAddress(strings.TrimSpace(item))
		if err != nil {
			return nil, fmt.Errorf("invalid X-Forwarded-For address: %w", err)
		}
		elements = append(elements, forwardedElement{address: addr})
	}
	return elements, nil
}

// splitElements splits every LINE of one header into its list elements.
//
// net/http keeps one entry per header line, and RFC 9110 says that repeated
// lines carry the same meaning as one comma-joined line. So each line supplies
// its own elements. A quoted string never crosses a line. If the code joined
// the lines into one string first, the joining comma would become part of an
// element instead of a separator, and a two-line chain would parse as one
// malformed address. Splitting per line also matches how the IP library walks
// the same header, so the index this package selects addresses the same
// element.
func splitElements(values []string) ([]string, error) {
	var items []string
	for _, value := range values {
		lineItems, err := splitOutsideQuotes(value, ',')
		if err != nil {
			return nil, err
		}
		items = append(items, lineItems...)
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("the header carries no element")
	}
	return items, nil
}

// splitOutsideQuotes splits one header line on separator and keeps a separator
// inside a quoted string as ordinary text.
func splitOutsideQuotes(value string, separator byte) ([]string, error) {
	var items []string
	var current strings.Builder
	inQuote := false
	escaped := false
	for index := 0; index < len(value); index++ {
		char := value[index]
		if escaped {
			if char < 0x20 || char == 0x7f {
				return nil, fmt.Errorf("invalid quoted escape")
			}
			current.WriteByte(char)
			escaped = false
			continue
		}
		if inQuote && char == '\\' {
			current.WriteByte(char)
			escaped = true
			continue
		}
		if char == '"' {
			inQuote = !inQuote
			current.WriteByte(char)
			continue
		}
		if !inQuote && char == separator {
			item := strings.TrimSpace(current.String())
			if item == "" {
				return nil, fmt.Errorf("empty header element")
			}
			items = append(items, item)
			current.Reset()
			continue
		}
		if char == '\r' || char == '\n' {
			return nil, fmt.Errorf("invalid control character")
		}
		current.WriteByte(char)
	}
	if inQuote || escaped {
		return nil, fmt.Errorf("unterminated quoted value")
	}
	last := strings.TrimSpace(current.String())
	if last == "" {
		return nil, fmt.Errorf("empty header element")
	}
	items = append(items, last)
	return items, nil
}

func parseParameterValue(raw string) (string, bool, error) {
	if raw[0] != '"' {
		if !isToken(raw) {
			return "", false, fmt.Errorf("invalid Forwarded token")
		}
		return raw, false, nil
	}
	if len(raw) < 2 || raw[len(raw)-1] != '"' {
		return "", false, fmt.Errorf("unterminated Forwarded quoted value")
	}
	var value strings.Builder
	escaped := false
	for index := 1; index < len(raw)-1; index++ {
		char := raw[index]
		if escaped {
			if char < 0x20 || char == 0x7f {
				return "", false, fmt.Errorf("invalid Forwarded quoted escape")
			}
			value.WriteByte(char)
			escaped = false
			continue
		}
		if char == '\\' {
			escaped = true
			continue
		}
		if char == '"' || char < 0x20 || char == 0x7f {
			return "", false, fmt.Errorf("invalid Forwarded quoted value")
		}
		value.WriteByte(char)
	}
	if escaped {
		return "", false, fmt.Errorf("invalid Forwarded quoted value")
	}
	return value.String(), true, nil
}

func parseForwardedNode(value string, quoted bool) (netip.Addr, bool, error) {
	if strings.EqualFold(value, "unknown") || strings.HasPrefix(value, "_") {
		if !isToken(value) {
			return netip.Addr{}, false, fmt.Errorf("invalid obfuscated Forwarded node")
		}
		return netip.Addr{}, false, nil
	}
	if strings.HasPrefix(value, "[") {
		if !quoted {
			return netip.Addr{}, false, fmt.Errorf("an IPv6 Forwarded node must be quoted")
		}
		end := strings.IndexByte(value, ']')
		if end < 0 {
			return netip.Addr{}, false, fmt.Errorf("invalid bracketed Forwarded node")
		}
		rest := value[end+1:]
		if rest != "" && (!strings.HasPrefix(rest, ":") || !validNodePort(rest[1:])) {
			return netip.Addr{}, false, fmt.Errorf("invalid Forwarded node port")
		}
		addr, err := netip.ParseAddr(value[1:end])
		if err != nil || addr.Zone() != "" || !addr.Is6() || addr.IsUnspecified() {
			return netip.Addr{}, false, fmt.Errorf("invalid Forwarded IPv6 address")
		}
		return addr, true, nil
	}
	if strings.Count(value, ":") > 1 {
		return netip.Addr{}, false, fmt.Errorf("an IPv6 Forwarded node must use brackets")
	}
	addr, err := parseHeaderAddress(value)
	if err != nil || !addr.Is4() {
		return netip.Addr{}, false, fmt.Errorf("invalid Forwarded IPv4 address")
	}
	return addr, true, nil
}

func parseHeaderAddress(value string) (netip.Addr, error) {
	if strings.Contains(value, "%") || value == "" {
		return netip.Addr{}, fmt.Errorf("zones and empty addresses are not allowed")
	}
	if addr, err := netip.ParseAddr(value); err == nil {
		addr = addr.Unmap()
		if !addr.IsUnspecified() {
			return addr, nil
		}
	}
	if addrPort, err := netip.ParseAddrPort(value); err == nil {
		addr := addrPort.Addr().Unmap()
		if !addr.IsUnspecified() {
			return addr, nil
		}
	}
	host, port, err := net.SplitHostPort(value)
	if err != nil || port == "" {
		return netip.Addr{}, fmt.Errorf("not an IP address")
	}
	addr, err := netip.ParseAddr(host)
	if err != nil || addr.Zone() != "" || addr.IsUnspecified() {
		return netip.Addr{}, fmt.Errorf("not an IP address")
	}
	return addr.Unmap(), nil
}

// xForwardedProtocol reads the protocol that belongs to the selected address.
//
// X-Forwarded-Proto carries no element metadata, so the two lists are matched
// by POSITION. One value states the protocol for the whole chain. A list as
// long as the address list is per-hop, and the selected index picks from it.
// Any other length is a chain the hub cannot align, and an unaligned protocol
// is not a protocol -- reading one at the wrong index would report a hop's
// answer as the client's.
//
// `selected` is an index into the address list, which the caller obtained from
// the chain walk, so it is always in range for a list of equal length. No guard
// restates that here, because a guard on a state the caller excluded reads as
// though the state can occur.
func xForwardedProtocol(values []string, selected, addressCount int) string {
	items, err := splitElements(values)
	if err != nil {
		return "http"
	}
	if len(items) == 1 {
		return validProtocol(items[0])
	}
	if len(items) == addressCount {
		return validProtocol(items[selected])
	}
	return "http"
}

func validProtocol(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "https" {
		return "https"
	}
	return "http"
}

func validNodePort(value string) bool {
	if strings.HasPrefix(value, "_") {
		return isToken(value)
	}
	if value == "" {
		return false
	}
	for _, char := range []byte(value) {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func isToken(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range []byte(value) {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || strings.ContainsRune("!#$%&'*+-.^_`|~", rune(char)) {
			continue
		}
		return false
	}
	return true
}
