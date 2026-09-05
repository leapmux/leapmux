// Package requestsource verifies the client address and protocol of an HTTP
// request. It trusts forwarding headers only when the transport peer matches a
// configured reverse-proxy range.
package requestsource

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync"

	providerranges "github.com/realclientip/realclientip-go/ranges"
	"go4.org/netipx"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/hub/settings"
)

// TrustedRanges is the stored selector list and its decoded address set. JSON
// contains only the canonical selectors. Provider ranges stay symbolic there.
type TrustedRanges struct {
	selectors []string
	prefixes  []netip.Prefix
	set       *netipx.IPSet
	// invalid records why a STORED selector list could not expand.
	//
	// UnmarshalJSON keeps the list and sets this instead of returning an
	// error, because the settings framework treats a decode failure and a
	// validation failure differently. A decode failure is a HARD refusal in
	// mergeForUpdate, so it would refuse every later write to this key and
	// leave `Reset` -- which discards the operator's whole list -- as the only
	// way out. A validation failure degrades the write's base to the default
	// and logs, which is the recovery every other setting has.
	//
	// The trigger is realistic: the bundled provider ranges are a generated
	// snapshot, so a dependency bump that widens a Cloudflare prefix over an
	// operator's manual selector makes the stored pair overlap, and the row
	// stops expanding. Lowering MaxTrustedProxySelectors does the same.
	//
	// A degraded value trusts NOBODY -- Contains guards a nil set -- so the
	// recovery is fail-closed.
	invalid error
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
	// The ACCEPTED selectors' own sets, and the overlap test runs against
	// them rather than against a combined set rebuilt on each step.
	//
	// `IPSetBuilder.IPSet()` sorts and merges every accumulated range and
	// copies the whole list into a fresh slice, so calling it inside the loop
	// made an O(n) selector list cost O(n^2 log n). That is not a startup
	// cost: the settings snapshot re-decodes this key on every cache refresh,
	// so a hub with `cloudfront` configured -- 241 bundled prefixes -- redid
	// the whole rebuild every refresh, for ever, although the stored value
	// never changed.
	//
	// `IPSet.Overlaps` is itself a nested walk over two range lists, so asking
	// each accepted set separately answers the same question over the same
	// ranges. The combined set is built ONCE, after the loop.
	accepted := make([]*netipx.IPSet, 0, len(selectors))
	for index, raw := range selectors {
		selector, selectorSet, err := parseSelector(raw)
		if err != nil {
			return TrustedRanges{}, fmt.Errorf("trusted proxy selector %d (%q): %w", index+1, raw, err)
		}
		if _, ok := seen[selector]; ok {
			return TrustedRanges{}, fmt.Errorf("trusted proxy selector %d (%q) duplicates %q", index+1, raw, selector)
		}
		// EVERY earlier selector, not the previous one.
		if slices.ContainsFunc(accepted, func(earlier *netipx.IPSet) bool { return earlier.Overlaps(selectorSet) }) {
			return TrustedRanges{}, fmt.Errorf("trusted proxy selector %d (%q) overlaps an earlier selector", index+1, raw)
		}
		seen[selector] = struct{}{}
		canonical = append(canonical, selector)
		accepted = append(accepted, selectorSet)
		combinedBuilder.AddSet(selectorSet)
	}
	combined, err := combinedBuilder.IPSet()
	if err != nil {
		return TrustedRanges{}, fmt.Errorf("combine the trusted proxy selectors: %w", err)
	}

	return TrustedRanges{
		selectors: slices.Clone(canonical),
		prefixes:  combined.Prefixes(),
		set:       combined,
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

// UnmarshalJSON expands the stored selector list during snapshot decode. The
// snapshot then carries a ready-to-use address set.
//
// The SHAPE is a decode error, because a value that is not a list of strings
// is not a selector list at all and no later write could merge onto it. A list
// that will not EXPAND is a validation failure instead: see TrustedRanges.
// invalid for why the two must not share an answer.
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
		// The raw selectors are kept so the admin surface can still SHOW the
		// operator what the stored row says while it refuses to use it.
		*r = TrustedRanges{selectors: slices.Clone(selectors), invalid: err}
		return nil
	}
	*r = decoded
	return nil
}

// Validate reports why a stored selector list could not expand.
func (r TrustedRanges) Validate() error {
	return r.invalid
}

// KeyTrustedProxyRanges controls which transport peers can supply forwarding
// headers. It is hot because the middleware reads a settings snapshot for each
// request.
var KeyTrustedProxyRanges = settings.NewKey[TrustedRanges]("trusted_proxy_ranges").
	WithValidate(TrustedRanges.Validate).
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
	if set, ok := providerRanges(lower); ok {
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

// providerTables binds every token in the generated catalogue to its bundled
// prefixes.
//
// It is DERIVED from `contracts.TrustedProxyProviders` rather than written
// beside it, so a token the contract adds and this map does not bind fails
// TestProviderCatalogueIsBound instead of failing an operator at write time
// with "unknown provider" for a token the settings editor offered them.
//
// Each table is parsed ONCE. The tables are compile-time constants -- 22
// prefixes for Cloudflare and 241 for CloudFront -- and the settings snapshot
// re-decodes this key on every cache refresh, so parsing them per decode
// re-did the same work for ever.
var providerTables = sync.OnceValue(func() map[string]*netipx.IPSet {
	sources := map[string][]string{
		contracts.TrustedProxyProviderCloudflare: providerranges.Cloudflare,
		contracts.TrustedProxyProviderCloudFront: providerranges.CloudFront,
	}
	tables := make(map[string]*netipx.IPSet, len(sources))
	for token, values := range sources {
		set, err := prefixSet(values)
		if err != nil {
			// A bundled table that will not parse is a build-time fault in a
			// vendored constant, not an operator's input, and the selector
			// would silently stop matching. TestProviderCatalogueIsBound
			// exercises every token, so this cannot reach a release.
			panic(fmt.Sprintf("bundled provider ranges for %q do not parse: %v", token, err))
		}
		tables[token] = set
	}
	return tables
})

func providerRanges(token string) (*netipx.IPSet, bool) {
	set, ok := providerTables()[token]
	return set, ok
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
