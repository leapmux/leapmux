package requestsource_test

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/hub/requestsource"
)

func TestTrustedRanges_CanonicalizesSupportedSelectors(t *testing.T) {
	t.Parallel()
	configured := []string{
		"192.0.2.10",
		"2001:0DB8::10",
		"192.168.0.1-100",
		"198.51.100.1-198.51.100.100",
		"2001:db8:1::1-2001:db8:1::ffff",
		"203.0.113.17/24",
		"2001:db8:2::17/64",
	}
	ranges, err := requestsource.NewTrustedRanges(configured)
	require.NoError(t, err)
	assert.Equal(t, []string{
		"192.0.2.10",
		"2001:db8::10",
		"192.168.0.1-192.168.0.100",
		"198.51.100.1-198.51.100.100",
		"2001:db8:1::1-2001:db8:1::ffff",
		"203.0.113.0/24",
		"2001:db8:2::/64",
	}, ranges.Selectors())
}

func TestTrustedRanges_RejectsInvalidSelectors(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		selectors []string
		message   string
	}{
		{"empty", []string{""}, "empty"},
		{"IPv4 port", []string{"192.0.2.1:443"}, "invalid IP"},
		{"IPv6 port", []string{"[2001:db8::1]:443"}, "invalid IP"},
		{"zone", []string{"fe80::1%en0"}, "invalid IP"},
		{"mapped CIDR", []string{"::ffff:192.0.2.0/120"}, "ambiguous"},
		{"unknown provider", []string{"fastly"}, "unknown provider"},
		{"mixed families", []string{"192.0.2.1-2001:db8::1"}, "different address families"},
		{"reversed range", []string{"192.0.2.100-1"}, "precedes"},
		{"bad range", []string{"192.0.2.1-2-3"}, "inclusive range"},
		{"duplicate address", []string{"192.0.2.1", "192.0.2.1"}, "duplicates"},
		{"duplicate provider", []string{"cloudflare", "CLOUDFLARE"}, "duplicates"},
		{"CIDR overlap", []string{"192.0.2.0/24", "192.0.2.20"}, "overlaps"},
		{"range overlap", []string{"192.0.2.1-100", "192.0.2.50-150"}, "overlaps"},
		{"provider overlap", []string{"cloudflare", "104.16.0.1"}, "overlaps"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := requestsource.NewTrustedRanges(test.selectors)
			require.Error(t, err)
			assert.Contains(t, err.Error(), test.message)
		})
	}
}

func TestTrustedRanges_EnforcesConfiguredSelectorCap(t *testing.T) {
	t.Parallel()
	selectors := make([]string, contracts.MaxTrustedProxySelectors+1)
	for index := range selectors {
		selectors[index] = fmt.Sprintf("192.0.2.%d", index+1)
	}
	_, err := requestsource.NewTrustedRanges(selectors)
	require.Error(t, err)
	assert.Contains(t, err.Error(), fmt.Sprintf("at most %d", contracts.MaxTrustedProxySelectors))
}

func TestTrustedRanges_ProvidersStaySymbolicAndExpandBothFamilies(t *testing.T) {
	t.Parallel()
	for _, token := range []string{"cloudflare", "cloudfront"} {
		t.Run(token, func(t *testing.T) {
			t.Parallel()
			trusted, err := requestsource.NewTrustedRanges([]string{strings.ToUpper(token)})
			require.NoError(t, err)
			assert.Equal(t, []string{token}, trusted.Selectors())

			prefixes := trusted.Prefixes()
			assert.True(t, slices.ContainsFunc(prefixes, func(prefix netip.Prefix) bool { return prefix.Addr().Is4() }))
			assert.True(t, slices.ContainsFunc(prefixes, func(prefix netip.Prefix) bool { return prefix.Addr().Is6() }))
			if token == "cloudfront" {
				assert.Greater(t, len(prefixes), contracts.MaxTrustedProxySelectors,
					"expanded provider prefixes must not count against the configured selector cap")
			}

			encoded, err := json.Marshal(trusted)
			require.NoError(t, err)
			assert.JSONEq(t, fmt.Sprintf("[%q]", token), string(encoded))
		})
	}
}

func TestTrustedRanges_ManualSelectorCanSupplementProvider(t *testing.T) {
	t.Parallel()
	trusted, err := requestsource.NewTrustedRanges([]string{"cloudflare", "192.0.2.10"})
	require.NoError(t, err)
	assert.Equal(t, []string{"cloudflare", "192.0.2.10"}, trusted.Selectors())
	assert.True(t, trusted.Contains(netip.MustParseAddr("192.0.2.10")))
}

func TestTrustedRanges_JSONDecodeValidatesAndCanonicalizes(t *testing.T) {
	t.Parallel()
	var trusted requestsource.TrustedRanges
	require.NoError(t, json.Unmarshal([]byte(`["CLOUDFRONT","192.0.2.1-20"]`), &trusted))
	assert.Equal(t, []string{"cloudfront", "192.0.2.1-192.0.2.20"}, trusted.Selectors())

	err := json.Unmarshal([]byte(`["192.0.2.1","192.0.2.0/24"]`), &trusted)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "overlaps")
}

func TestTrustedRanges_JSONDecodeRejectsNull(t *testing.T) {
	t.Parallel()
	var trusted requestsource.TrustedRanges
	err := json.Unmarshal([]byte(`null`), &trusted)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "JSON list of strings")
}
