package agent

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestJSONRPCControlRequestIdentityValidation(t *testing.T) {
	for _, invalid := range []string{"", " ", "null", "true", "false", "[]", "{}", "01", "NaN", `"broken`} {
		_, valid := NewControlRequestIdentity(json.RawMessage(invalid))
		require.False(t, valid, invalid)
	}
	for _, valid := range []string{`0`, `-1`, `1e3`, `9007199254740993`, `""`, `"7"`} {
		identity, ok := NewControlRequestIdentity(json.RawMessage(valid))
		key := identity.Key
		require.True(t, ok, valid)
		require.NotEmpty(t, key)
	}
	// Two ids that DIFFER must reach two keys, whatever their magnitude. Every pair
	// below lands inside one float64 once the digits run out.
	for _, pair := range [][2]string{
		{`9223372036854775807`, `9223372036854775808`},
		{`9007199254740993`, `9007199254740992`},
		{`18446744073709551615`, `18446744073709551614`},
	} {
		leftIdentity, ok := NewControlRequestIdentity(json.RawMessage(pair[0]))
		left := leftIdentity.Key
		require.True(t, ok, pair[0])
		rightIdentity, ok := NewControlRequestIdentity(json.RawMessage(pair[1]))
		right := rightIdentity.Key
		require.True(t, ok, pair[1])
		require.NotEqual(t, left, right, "two distinct ids must not share one key: %s and %s", pair[0], pair[1])
	}
}

func TestJSONRPCControlRequestIdentityStringsStayDistinct(t *testing.T) {
	t.Parallel()
	literalIdentity, ok := NewControlRequestIdentity(json.RawMessage(`"request"`))
	literal := literalIdentity.Key
	require.True(t, ok)
	escapedIdentity, ok := NewControlRequestIdentity(json.RawMessage(` "\u0072equest" `))
	escaped := escapedIdentity.Key
	require.True(t, ok)
	require.Equal(t, literal, escaped)
}

// Two frames can spell the SAME number differently -- the request payload and the
// withdrawal notification are separate frames -- so every spelling of one value has to
// give one key, and a string id must stay a different request.
func TestJSONRPCControlRequestIdentityCanonicalizesNumbers(t *testing.T) {
	t.Parallel()
	for _, group := range [][]string{
		{`12`, `12.0`, `1.2e1`, `0.12e2`},
		{`0`, `-0`, `0.0`, `-0.0`, `0e5`},
		{`-7`, `-7.0`, `-0.7e1`},
		{`1000`, `1e3`, `1000.0`},
		// From 1e6 upward a float64 canonicalization turns to exponent notation,
		// so these two spellings of one id reached two keys and a cancel matched
		// nothing. A timestamp-shaped id sits far above that threshold.
		{`1000000`, `1000000.0`, `1.0e6`, `1e6`},
		{`1758000000000`, `1.758e12`, `1758000000000.0`},
		// Past 2^53 a float64 has no digits left to tell two ids apart, so the
		// same canonicalization COLLIDED them onto one key.
		{`9223372036854775808`, `9223372036854775808.0`},
	} {
		firstIdentity, ok := NewControlRequestIdentity(json.RawMessage(group[0]))
		first := firstIdentity.Key
		require.True(t, ok, group[0])
		for _, spelling := range group[1:] {
			identity, ok := NewControlRequestIdentity(json.RawMessage(spelling))
			key := identity.Key
			require.True(t, ok, spelling)
			require.Equal(t, first, key, spelling)
		}
	}
	// A 64-bit integer id keeps every digit: float64 cannot hold this one.
	wideIdentity, ok := NewControlRequestIdentity(json.RawMessage(`9007199254740993`))
	wide := wideIdentity.Key
	require.True(t, ok)
	require.Equal(t, "jsonrpc:9007199254740993", wide)
	// The string branch keeps its quotation marks, so "12" and 12 stay two requests.
	numberIdentity, ok := NewControlRequestIdentity(json.RawMessage(`12`))
	number := numberIdentity.Key
	require.True(t, ok)
	textIdentity, ok := NewControlRequestIdentity(json.RawMessage(`"12"`))
	text := textIdentity.Key
	require.True(t, ok)
	require.NotEqual(t, number, text)
}

// A JSON number is valid at any magnitude, so the id of an inbound control request is
// attacker-shaped input: eight bytes of literal expand to a megabyte of key. The old
// float64 parse refused those by accident, through ErrRange; big.Rat accepts them and
// pays for the expansion on the goroutine that drains the provider's stdout.
func TestControlRequestIdentityRefusesAnUnrenderableNumber(t *testing.T) {
	for _, refused := range []string{
		`1e999999`,
		`1E999999`,
		`1e-999999`,
		`-1e999999`,
		`1e65`,
		`1e-65`,
		`123456789012345678901234567890123456789012345678901234567890123456789`,
	} {
		start := time.Now()
		identity, ok := NewControlRequestIdentity(json.RawMessage(refused))
		key := identity.Key
		require.Falsef(t, ok, "%s must be refused", refused)
		require.Empty(t, key)
		require.Lessf(t, time.Since(start), time.Second, "%s must be refused without rendering it", refused)
	}
	// The limit must not refuse an id a real runtime sends: a counter, a timestamp in
	// nanoseconds, a value past 2^53, and exponent notation within the limit.
	for _, accepted := range []string{`7`, `1763212800000000000`, `9007199254740993`, `1.2e1`, `1e64`, `-42`, `0`} {
		identity, ok := NewControlRequestIdentity(json.RawMessage(accepted))
		key := identity.Key
		require.Truef(t, ok, "%s must be accepted", accepted)
		require.NotEmpty(t, key)
		require.Lessf(t, len(key), 128, "%s must canonicalize to a short key", accepted)
	}
	// 1.2e1 and 12 are the same value, so they must reach the same key.
	scientificIdentity, ok := NewControlRequestIdentity(json.RawMessage(`1.2e1`))
	scientific := scientificIdentity.Key
	require.True(t, ok)
	plainIdentity, ok := NewControlRequestIdentity(json.RawMessage(`12`))
	plain := plainIdentity.Key
	require.True(t, ok)
	require.Equal(t, plain, scientific)
}
