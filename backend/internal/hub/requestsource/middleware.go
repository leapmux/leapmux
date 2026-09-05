package requestsource

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
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

// Middleware records the verified client IP and effective URL scheme. It
// leaves Request.RemoteAddr and Request.TLS unchanged.
func Middleware(manager *settings.Manager, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		trusted := TrustedRanges{}
		if manager != nil {
			trusted = KeyTrustedProxyRanges.Of(manager.Snapshot(r.Context()))
		}
		clientIP, scheme := resolve(r, trusted)
		ctx := peer.WithClientIP(r.Context(), clientIP)

		// A SHALLOW copy plus a copy of the URL, not r.Clone. This runs on
		// every request the hub serves, and Clone deep-copies the header map,
		// the trailer, the transfer encoding and the parsed forms -- a slice
		// allocation per header to change one string. The only field this
		// middleware writes is URL.Scheme, so the URL is the one value that
		// must not stay shared with the caller's request.
		request := r.WithContext(ctx)
		url := *r.URL
		url.Scheme = scheme
		request.URL = &url
		next.ServeHTTP(w, request)
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
	if !trusted.Contains(peerAddr) {
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
	// A REBUILT header carrying the `for` values alone. The library splits the
	// raw header on every comma, quoted or not, so a quoted comma inside any
	// other parameter (`host="edge,one"`) would split one element into two
	// there. The local parser above respects the quoting, and this hands the
	// library a form that cannot be split wrongly.
	clientIP := trusted.forwarded.ClientIP(forwardedIPHeader(elements), r.RemoteAddr)
	// An empty clientIP parses to no address, so this ALSO covers the "no
	// client address in the chain" case.
	index := rightmostAddressIndex(elements, clientIP)
	if index < 0 {
		return "", "http"
	}
	return clientIP, validProtocol(elements[index].protocol)
}

// resolveXForwarded answers from X-Forwarded-For and X-Forwarded-Proto, which
// carry no element metadata: the protocol list stands beside the address list
// rather than inside it.
func resolveXForwarded(r *http.Request, trusted TrustedRanges) (string, string) {
	addresses, err := parseXForwardedFor(r.Header[xForwardedForHeader])
	if err != nil {
		return "", "http"
	}
	clientIP := trusted.xForwardedFor.ClientIP(r.Header, r.RemoteAddr)
	index := rightmostAddressIndex(addresses, clientIP)
	if index < 0 {
		return "", "http"
	}
	return clientIP, xForwardedProtocol(r.Header[xForwardedProtocolHeader], index, len(addresses))
}

type forwardedElement struct {
	address  netip.Addr
	protocol string
	forValue string
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
		seen := make(map[string]struct{}, len(parts))
		for _, part := range parts {
			name, rawValue, ok := strings.Cut(strings.TrimSpace(part), "=")
			name = strings.ToLower(strings.TrimSpace(name))
			rawValue = strings.TrimSpace(rawValue)
			if !ok || !isToken(name) || rawValue == "" {
				return nil, fmt.Errorf("invalid Forwarded parameter")
			}
			if _, duplicate := seen[name]; duplicate {
				return nil, fmt.Errorf("duplicate Forwarded parameter %q", name)
			}
			seen[name] = struct{}{}
			value, quoted, err := parseParameterValue(rawValue)
			if err != nil {
				return nil, err
			}
			switch name {
			case "for":
				element.forValue = rawValue
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

func forwardedIPHeader(elements []forwardedElement) http.Header {
	values := make([]string, len(elements))
	for index, element := range elements {
		value := element.forValue
		if value == "" {
			value = "unknown"
		}
		values[index] = "for=" + value
	}
	return http.Header{forwardedHeader: []string{strings.Join(values, ", ")}}
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
// its own elements, and a quoted string never crosses a line: joining the
// lines into one string first would make the joining comma part of an element
// instead of a separator, and a two-line chain would parse as one malformed
// address. Splitting per line also matches how the IP library walks the same
// header, so the index this package selects addresses the same element.
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

func rightmostAddressIndex(elements []forwardedElement, clientIP string) int {
	wanted, err := netip.ParseAddr(clientIP)
	if err != nil {
		return -1
	}
	wanted = wanted.Unmap()
	for index := len(elements) - 1; index >= 0; index-- {
		if elements[index].address.IsValid() && elements[index].address.Unmap() == wanted {
			return index
		}
	}
	return -1
}

func xForwardedProtocol(values []string, selected, addressCount int) string {
	items, err := splitElements(values)
	if err != nil {
		return "http"
	}
	if len(items) == 1 {
		return validProtocol(items[0])
	}
	if len(items) == addressCount && selected >= 0 && selected < len(items) {
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
