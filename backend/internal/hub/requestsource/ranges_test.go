package requestsource_test

import (
	"context"
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
	"github.com/leapmux/leapmux/internal/hub/settings"
	"github.com/leapmux/leapmux/internal/hub/store"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
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

// Overlap is checked against EVERY earlier selector, not the previous one.
//
// The set accumulates across the loop. A two-selector test cannot tell that
// apart from a set rebuilt on each step. With two selectors, "all the earlier
// ones" and "the previous one" are the same selector. The third below overlaps
// the FIRST and is disjoint from the second, so only the accumulated set
// catches it.
func TestTrustedRanges_DetectsAnOverlapWithAnySelectorNotOnlyTheLast(t *testing.T) {
	t.Parallel()
	_, err := requestsource.NewTrustedRanges([]string{
		"192.0.2.0/24",
		"198.51.100.0/24",
		"192.0.2.7",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "overlaps")
	assert.Contains(t, err.Error(), "selector 3", "the report must name the selector that collided")

	// The same three, with the collision removed, are accepted together -- so
	// the refusal above is the overlap and not the count.
	ranges, err := requestsource.NewTrustedRanges([]string{
		"192.0.2.0/24",
		"198.51.100.0/24",
		"203.0.113.7",
	})
	require.NoError(t, err)
	assert.True(t, ranges.Contains(netip.MustParseAddr("192.0.2.7")))
	assert.True(t, ranges.Contains(netip.MustParseAddr("198.51.100.9")))
	assert.True(t, ranges.Contains(netip.MustParseAddr("203.0.113.7")))
	assert.False(t, ranges.Contains(netip.MustParseAddr("203.0.113.8")))
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
}

// A list that will not EXPAND decodes, and reports itself through Validate.
//
// The split matters to the settings framework and nowhere else. A decode error
// is a hard refusal in mergeForUpdate, so a stored row that stopped expanding
// -- because a bundled provider range widened over a manual selector, or
// because the selector cap dropped -- would refuse every later write and leave
// Reset, which discards the operator's whole list, as the only way out. A
// validation failure degrades the write's base to the default and logs, which
// is the recovery every other settings key has.
func TestTrustedRanges_JSONDecodeDefersAnUnexpandableListToValidate(t *testing.T) {
	t.Parallel()
	var trusted requestsource.TrustedRanges
	require.NoError(t, json.Unmarshal([]byte(`["192.0.2.1","192.0.2.0/24"]`), &trusted),
		"an unexpandable list must decode, or the key becomes unwritable")

	err := trusted.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "overlaps")

	// FAIL-CLOSED: a degraded value trusts nobody, so the window between the
	// bad row and the operator's next write never widens the trust set.
	assert.False(t, trusted.Contains(netip.MustParseAddr("192.0.2.1")))
	assert.Empty(t, trusted.Prefixes())
	// The raw selectors survive, so the admin surface can still show the
	// operator what the stored row says.
	assert.Equal(t, []string{"192.0.2.1", "192.0.2.0/24"}, trusted.Selectors())
}

// A value the constructor accepted reports no validation error, so the
// framework never degrades a good row.
func TestTrustedRanges_ValidateAcceptsAnExpandedList(t *testing.T) {
	t.Parallel()
	var trusted requestsource.TrustedRanges
	require.NoError(t, json.Unmarshal([]byte(`["192.0.2.0/24"]`), &trusted))
	assert.NoError(t, trusted.Validate())
	assert.True(t, trusted.Contains(netip.MustParseAddr("192.0.2.7")))
}

func TestTrustedRanges_JSONDecodeRejectsNull(t *testing.T) {
	t.Parallel()
	var trusted requestsource.TrustedRanges
	err := json.Unmarshal([]byte(`null`), &trusted)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "JSON list of strings")
}

// The whole point of deferring the expansion failure to Validate: an operator
// whose STORED row stopped expanding can still write a new one.
//
// The trigger is realistic. `providerranges` is a generated snapshot, so a
// dependency bump that widens a bundled Cloudflare or CloudFront prefix over
// an operator's manual selector makes the stored pair overlap, and the row
// stops expanding. Before this, `mergeForUpdate` refused to decode the current
// value, so every later write to the key answered "decode current value of
// trusted_proxy_ranges" and only `Reset` -- which discards the whole list --
// escaped.
func TestKeyTrustedProxyRanges_StaysWritableAfterAnUnexpandableRow(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := hubtestutil.OpenTestStore(t)
	manager := settings.NewManager(st, nil, requestsource.SettingsDescriptors())

	// Written straight to the TABLE, which is how a row the current rules
	// refuse gets there: direct SQL, or an older hub whose bundled provider
	// ranges did not yet overlap this operator's manual selector. Every write
	// verb the admin surface exposes validates, so none of them can produce it.
	row := `["192.0.2.1","192.0.2.0/24"]`
	require.NoError(t, st.Settings().Upsert(ctx, store.UpsertSettingParams{
		Key:   "trusted_proxy_ranges",
		Value: &row,
	}))
	require.NoError(t, manager.Load(ctx))

	// The READ degrades to the default, so nothing is trusted meanwhile.
	degraded := requestsource.KeyTrustedProxyRanges.Of(manager.Snapshot(ctx))
	assert.False(t, degraded.Contains(netip.MustParseAddr("192.0.2.1")),
		"a row the rules refuse must trust nobody")

	// The WRITE succeeds, which is the property this test exists for.
	require.NoError(t, manager.Update(ctx, requestsource.KeyTrustedProxyRanges,
		json.RawMessage(`["198.51.100.0/24"]`)))
	repaired := requestsource.KeyTrustedProxyRanges.Of(manager.Snapshot(ctx))
	assert.Equal(t, []string{"198.51.100.0/24"}, repaired.Selectors())
	assert.True(t, repaired.Contains(netip.MustParseAddr("198.51.100.7")))

	// And an invalid NEW write is still refused, so the recovery did not widen
	// what an operator can store.
	require.Error(t, manager.Update(ctx, requestsource.KeyTrustedProxyRanges,
		json.RawMessage(`["203.0.113.1","203.0.113.0/24"]`)),
		"an overlapping list must still be refused on the way in")
}

// Every token in the generated catalogue must resolve to bundled ranges.
//
// The catalogue is what the settings editor renders its Add menu from, so a
// token the contract carries and this package does not bind is a menu entry
// that stages a selector the hub then refuses with "unknown provider or
// invalid IP address". The failure lands on the operator at write time, and
// nothing upstream catches it: the JSON schema and the generator's own check
// both pass for a well-formed entry.
func TestProviderCatalogueIsBound(t *testing.T) {
	t.Parallel()
	require.NotEmpty(t, contracts.TrustedProxyProviders, "an empty catalogue makes this test vacuous")
	for _, provider := range contracts.TrustedProxyProviders {
		t.Run(provider.Token, func(t *testing.T) {
			t.Parallel()
			trusted, err := requestsource.NewTrustedRanges([]string{provider.Token})
			require.NoErrorf(t, err, "the catalogue offers %q but the hub cannot expand it", provider.Token)
			assert.Equal(t, []string{provider.Token}, trusted.Selectors(),
				"a provider selector must stay symbolic in storage")
			assert.NotEmpty(t, trusted.Prefixes(), "%q expands to no ranges", provider.Token)
		})
	}
}
