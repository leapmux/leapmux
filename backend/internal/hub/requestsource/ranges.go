// Package requestsource verifies the client address and protocol of an HTTP
// request. It trusts forwarding headers only when the transport peer matches a
// configured reverse-proxy range.
package requestsource

import (
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"github.com/realclientip/realclientip-go"
	providerranges "github.com/realclientip/realclientip-go/ranges"
	"go4.org/netipx"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/hub/settings"
)

// TrustedRanges is the stored selector list and its decoded address set. JSON
// contains only the canonical selectors. Provider ranges stay symbolic there.
type TrustedRanges struct {
	selectors     []string
	prefixes      []netip.Prefix
	set           *netipx.IPSet
	forwarded     realclientip.RightmostTrustedRangeStrategy
	xForwardedFor realclientip.RightmostTrustedRangeStrategy
}

// NewTrustedRanges validates and canonicalizes configured selectors.
func NewTrustedRanges(selectors []string) (TrustedRanges, error) {
	if len(selectors) > contracts.MaxTrustedProxySelectors {
		return TrustedRanges{}, fmt.Errorf("at most %d trusted proxy selectors are allowed; got %d",
			contracts.MaxTrustedProxySelectors, len(selectors))
	}

	canonical := make([]string, 0, len(selectors))
	seen := make(map[string]struct{}, len(selectors))
	var combinedBuilder netipx.IPSetBuilder
	combined, err := combinedBuilder.IPSet()
	if err != nil {
		return TrustedRanges{}, fmt.Errorf("create the trusted proxy set: %w", err)
	}
	for index, raw := range selectors {
		selector, selectorSet, err := parseSelector(raw)
		if err != nil {
			return TrustedRanges{}, fmt.Errorf("trusted proxy selector %d (%q): %w", index+1, raw, err)
		}
		if _, ok := seen[selector]; ok {
			return TrustedRanges{}, fmt.Errorf("trusted proxy selector %d (%q) duplicates %q", index+1, raw, selector)
		}
		if combined.Overlaps(selectorSet) {
			return TrustedRanges{}, fmt.Errorf("trusted proxy selector %d (%q) overlaps an earlier selector", index+1, raw)
		}
		seen[selector] = struct{}{}
		canonical = append(canonical, selector)
		combinedBuilder.AddSet(selectorSet)
		combined, err = combinedBuilder.IPSet()
		if err != nil {
			return TrustedRanges{}, fmt.Errorf("combine trusted proxy selector %d: %w", index+1, err)
		}
	}

	prefixes := combined.Prefixes()
	ipNets := make([]net.IPNet, 0, len(prefixes))
	for _, prefix := range prefixes {
		ipNets = append(ipNets, *netipx.PrefixIPNet(prefix))
	}
	forwarded, err := realclientip.NewRightmostTrustedRangeStrategy("Forwarded", ipNets)
	if err != nil {
		return TrustedRanges{}, fmt.Errorf("create the Forwarded strategy: %w", err)
	}
	xForwardedFor, err := realclientip.NewRightmostTrustedRangeStrategy("X-Forwarded-For", ipNets)
	if err != nil {
		return TrustedRanges{}, fmt.Errorf("create the X-Forwarded-For strategy: %w", err)
	}
	return TrustedRanges{
		selectors:     slices.Clone(canonical),
		prefixes:      prefixes,
		set:           combined,
		forwarded:     forwarded,
		xForwardedFor: xForwardedFor,
	}, nil
}

// Selectors returns the canonical configured selectors.
func (r TrustedRanges) Selectors() []string {
	return slices.Clone(r.selectors)
}

// Prefixes returns the expanded and normalized trusted prefixes.
func (r TrustedRanges) Prefixes() []netip.Prefix {
	return slices.Clone(r.prefixes)
}

// Contains reports whether the expanded trusted set contains an address.
func (r TrustedRanges) Contains(addr netip.Addr) bool {
	return r.set != nil && r.set.Contains(addr.Unmap())
}

// MarshalJSON preserves symbolic provider selectors in storage and listings.
func (r TrustedRanges) MarshalJSON() ([]byte, error) {
	selectors := r.selectors
	if selectors == nil {
		selectors = []string{}
	}
	return json.Marshal(selectors)
}

// UnmarshalJSON validates and expands the stored selector list during snapshot
// decode. The snapshot then carries a ready-to-use address set.
func (r *TrustedRanges) UnmarshalJSON(data []byte) error {
	var selectors []string
	if err := json.Unmarshal(data, &selectors); err != nil {
		return fmt.Errorf("trusted_proxy_ranges must be a JSON list of strings: %w", err)
	}
	if selectors == nil {
		return fmt.Errorf("trusted_proxy_ranges must be a JSON list of strings")
	}
	decoded, err := NewTrustedRanges(selectors)
	if err != nil {
		return err
	}
	*r = decoded
	return nil
}

// KeyTrustedProxyRanges controls which transport peers can supply forwarding
// headers. It is hot because the middleware reads a settings snapshot for each
// request.
var KeyTrustedProxyRanges = settings.NewKey[TrustedRanges]("trusted_proxy_ranges").
	WithUI(settings.UIMeta{
		Category: "network",
		Title:    "Trusted reverse proxies",
		Summary:  "transport peers whose Forwarded or X-Forwarded headers can identify the client",
		Fields: []settings.Field{{
			Name: "", Label: "Trusted reverse proxies", Kind: settings.FieldCustom,
			CustomID: "trustedProxies",
			Help:     "Trust only reverse proxies that remove client-supplied forwarding headers or append verified values.",
		}},
	})

// SettingsDescriptors lists the request-source settings.
func SettingsDescriptors() []settings.Descriptor {
	return []settings.Descriptor{KeyTrustedProxyRanges}
}

func parseSelector(raw string) (string, *netipx.IPSet, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", nil, fmt.Errorf("the selector is empty")
	}
	lower := strings.ToLower(value)
	if providerValues, ok := providerRanges(lower); ok {
		set, err := prefixSet(providerValues)
		if err != nil {
			return "", nil, fmt.Errorf("expand provider %q: %w", lower, err)
		}
		return lower, set, nil
	}
	if strings.Contains(value, "/") {
		prefix, err := netip.ParsePrefix(value)
		if err != nil || prefix.Addr().Zone() != "" {
			return "", nil, fmt.Errorf("invalid CIDR")
		}
		if prefix.Addr().Is4In6() {
			return "", nil, fmt.Errorf("an IPv4-mapped IPv6 CIDR is ambiguous; use its IPv4 form")
		}
		prefix = prefix.Masked()
		if prefix.Addr().IsUnspecified() && prefix.Bits() == prefix.Addr().BitLen() {
			return "", nil, fmt.Errorf("an unspecified address is not a proxy")
		}
		set, err := prefixSet([]string{prefix.String()})
		return prefix.String(), set, err
	}
	if strings.Contains(value, "-") {
		return parseInclusiveRange(value)
	}
	addr, err := parseAddress(value)
	if err != nil {
		return "", nil, fmt.Errorf("unknown provider or invalid IP address")
	}
	prefix := netip.PrefixFrom(addr, addr.BitLen())
	set, err := prefixSet([]string{prefix.String()})
	return addr.String(), set, err
}

func providerRanges(token string) ([]string, bool) {
	switch token {
	case contracts.TrustedProxyProviderCloudflare:
		return providerranges.Cloudflare, true
	case contracts.TrustedProxyProviderCloudFront:
		return providerranges.CloudFront, true
	default:
		return nil, false
	}
}

func parseInclusiveRange(value string) (string, *netipx.IPSet, error) {
	parts := strings.Split(value, "-")
	if len(parts) != 2 {
		return "", nil, fmt.Errorf("invalid inclusive range")
	}
	first, err := parseAddress(parts[0])
	if err != nil {
		return "", nil, fmt.Errorf("invalid range start")
	}
	last, err := parseAddress(parts[1])
	if err != nil {
		last, err = shortIPv4End(first, parts[1])
		if err != nil {
			return "", nil, fmt.Errorf("invalid range end")
		}
	}
	if first.BitLen() != last.BitLen() {
		return "", nil, fmt.Errorf("range addresses use different address families")
	}
	if last.Less(first) {
		return "", nil, fmt.Errorf("range end precedes range start")
	}
	ipRange := netipx.IPRangeFrom(first, last)
	if !ipRange.IsValid() {
		return "", nil, fmt.Errorf("invalid inclusive range")
	}
	var builder netipx.IPSetBuilder
	builder.AddRange(ipRange)
	set, err := builder.IPSet()
	if err != nil {
		return "", nil, fmt.Errorf("decompose inclusive range: %w", err)
	}
	return ipRange.String(), set, nil
}

func parseAddress(value string) (netip.Addr, error) {
	if strings.Contains(value, "%") {
		return netip.Addr{}, fmt.Errorf("zones are not allowed")
	}
	addr, err := netip.ParseAddr(value)
	if err != nil {
		return netip.Addr{}, err
	}
	addr = addr.Unmap()
	if addr.IsUnspecified() {
		return netip.Addr{}, fmt.Errorf("an unspecified address is not a proxy")
	}
	return addr, nil
}

func shortIPv4End(first netip.Addr, value string) (netip.Addr, error) {
	if !first.Is4() || value == "" || strings.TrimSpace(value) != value {
		return netip.Addr{}, fmt.Errorf("not a short IPv4 range")
	}
	octet, err := strconv.ParseUint(value, 10, 8)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("parse the final IPv4 octet: %w", err)
	}
	bytes := first.As4()
	bytes[3] = byte(octet)
	return netip.AddrFrom4(bytes), nil
}

func prefixSet(values []string) (*netipx.IPSet, error) {
	var builder netipx.IPSetBuilder
	for _, value := range values {
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			return nil, fmt.Errorf("parse prefix %q: %w", value, err)
		}
		builder.AddPrefix(prefix.Masked())
	}
	return builder.IPSet()
}
