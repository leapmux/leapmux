package settings

import (
	"context"
	"encoding/json"
	"math"
	"strconv"
	"testing"
	"time"

	"github.com/leapmux/leapmux/channelwire"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/ptrconv"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestValidatePublicURLGuards pins the canonical-form rules the key
// inherited from the old config loader: scheme+host and nothing else.
// BaseURL returns the stored string verbatim, so a trailing slash, a
// path, credentials, or a query would corrupt every derived URL (the
// hub appends its own routes onto the base).
func TestValidatePublicURLGuards(t *testing.T) {
	require.NoError(t, validatePublicURL(""))
	require.NoError(t, validatePublicURL("https://hub.example.com"))
	require.NoError(t, validatePublicURL("http://hub.example.com"))

	for _, raw := range []string{
		"https://hub.example.com/", // trailing slash: "//verify-email" links
		"https://hub.example.com/hub",
		"https://user:pass@hub.example.com", // credentials would leak into email links
		"https://hub.example.com?ref=1",
		"https://hub.example.com#section",
		"hub.example.com",       // no scheme
		"ftp://hub.example.com", // wrong scheme
		"https://",              // no host
	} {
		require.Error(t, validatePublicURL(raw), "public_url %q must be refused", raw)
	}
}

// TestValidateSMTPAlwaysChecksTLSMode pins the unconditional enum check:
// a typo staged on a hostless (piecemeal) document must fail at the write
// that introduced it, not at the later host write that first makes it
// reachable — which would blame the wrong write while the bad value sat
// stored.
func TestValidateSMTPAlwaysChecksTLSMode(t *testing.T) {
	err := validateSMTP(SMTPValue{TLSMode: "startls"})
	require.ErrorContains(t, err, "unsupported smtp tls mode")

	// The valid modes pass with no host staged; the empty string is not
	// one of them (an unset mode must resolve to the declared starttls
	// default, never store as "").
	for _, mode := range []string{SMTPTLSModeSTARTTLS, SMTPTLSModeImplicit, SMTPTLSModeNone} {
		assert.NoError(t, validateSMTP(SMTPValue{TLSMode: mode}))
	}
	require.ErrorContains(t, validateSMTP(SMTPValue{TLSMode: ""}), "unsupported smtp tls mode")
}

// TestSMTPConfiguresOneFieldAtATime pins the whole point of the staging
// rule: the Preferences dialog writes ONE field per row, so configuring
// SMTP from an empty hub means three separate writes in the dialog's own
// order — host, then port, then from address.
//
// Requiring a from address whenever a host is set refused the FIRST of
// those writes, with an error naming a field on a different row and no
// way for the operator to reach it. Enabled() is what keeps the pair
// coherent instead: it stays false until both fields are stored, so no
// consumer can dial a half-staged relay.
func TestSMTPConfiguresOneFieldAtATime(t *testing.T) {
	m, _, _ := newTestManagerWithStore(t)
	ctx := context.Background()
	read := func() SMTPValue { return KeySMTP.Of(m.Snapshot(ctx)) }

	require.NoError(t, m.Update(ctx, KeySMTP, json.RawMessage(`{"host":"smtp.example.com"}`)),
		"the host row must be writable before the from address exists")
	assert.False(t, read().Enabled(), "a host alone is not a usable relay")

	require.NoError(t, m.Update(ctx, KeySMTP, json.RawMessage(`{"port":2525}`)))
	assert.False(t, read().Enabled(), "still no from address")

	// A MALFORMED from address fails at the write that introduced it.
	err := m.Update(ctx, KeySMTP, json.RawMessage(`{"from_address":"not-an-email"}`))
	require.ErrorContains(t, err, "is not a valid email")
	assert.False(t, read().Enabled(), "the refused write stores nothing")

	require.NoError(t, m.Update(ctx, KeySMTP, json.RawMessage(`{"from_address":"hub@example.com"}`)))
	v := read()
	assert.True(t, v.Enabled(), "both fields are present now")
	assert.Equal(t, "smtp.example.com", v.Host)
	assert.Equal(t, 2525, v.Port)
	assert.Equal(t, "hub@example.com", v.FromAddress)
}

// TestValidateSMTPStagingRules pins the from-address rule directly, in
// both directions and on a HOSTLESS document. An operator who reaches the
// from address first must still learn about a typo at that write, not at
// the later host write that first makes it reachable.
func TestValidateSMTPStagingRules(t *testing.T) {
	staged := SMTPValue{TLSMode: SMTPTLSModeSTARTTLS, Port: 587}

	absent := staged
	assert.NoError(t, validateSMTP(absent), "an absent from address is staging, not a typo")
	assert.False(t, absent.Enabled())

	malformed := staged
	malformed.FromAddress = "not-an-email"
	require.ErrorContains(t, validateSMTP(malformed), "is not a valid email",
		"a malformed from address fails with no host staged")

	fromFirst := staged
	fromFirst.FromAddress = "hub@example.com"
	assert.NoError(t, validateSMTP(fromFirst))
	assert.False(t, fromFirst.Enabled(), "a from address alone is not a usable relay")

	both := fromFirst
	both.Host = "smtp.example.com"
	assert.NoError(t, validateSMTP(both))
	assert.True(t, both.Enabled())
}

// TestSMTPConfiguredCrossRuleNeedsBothFields pins that the cross-key rule
// got STRICTER, not weaker: email verification against a relay with no
// from address can never send, so the combination stays refused.
func TestSMTPConfiguredCrossRuleNeedsBothFields(t *testing.T) {
	m, _, _ := newTestManagerWithStore(t)
	ctx := context.Background()

	require.NoError(t, m.Update(ctx, KeySMTP, json.RawMessage(`{"host":"smtp.example.com"}`)))
	err := m.Update(ctx, KeyEmailVerificationRequired, json.RawMessage(`true`))
	require.ErrorContains(t, err, "needs the smtp host and from address",
		"a host without a from address cannot carry verification email")

	require.NoError(t, m.Update(ctx, KeySMTP, json.RawMessage(`{"from_address":"hub@example.com"}`)))
	require.NoError(t, m.Update(ctx, KeyEmailVerificationRequired, json.RawMessage(`true`)))
	assert.True(t, EmailVerificationEffective(m.Snapshot(ctx)))
}

// TestValidateSMTPPlainAuthLocalhost pins the exact localhost criteria:
// the validator may accept precisely the hosts Go's smtp.PlainAuth will
// send credentials to in plaintext. "*.localhost" suffixes and other
// loopback addresses (127.0.0.2) fail PlainAuth at Send time, so
// accepting them would pass a configuration that cannot work.
func TestValidateSMTPPlainAuthLocalhost(t *testing.T) {
	for _, host := range []string{"localhost", "127.0.0.1", "::1"} {
		assert.NoError(t, validateSMTP(SMTPValue{
			Host: host, Port: 25, TLSMode: SMTPTLSModeNone, Username: "u",
			FromAddress: "hub@example.com",
		}), "plaintext credentials against %q match smtp.PlainAuth's own criteria", host)
	}
	for _, host := range []string{"myapp.localhost", "127.0.0.2", "smtp.example.com"} {
		err := validateSMTP(SMTPValue{
			Host: host, Port: 25, TLSMode: SMTPTLSModeNone, Username: "u",
			FromAddress: "hub@example.com",
		})
		require.ErrorContains(t, err, "in the clear", "%q must be refused", host)
	}
}

// TestValidateQueueBudgetPerClassFloor pins the per-class floor the old
// config validator enforced and the settings rewrite had dropped: an
// explicit budget below the class minimum builds a degenerate pool (or
// refuses to build at all — sendq.NewPool panics when its floor exceeds
// the capacity), so the write path refuses it with the same number the
// resolver clamps auto-sized budgets to.
func TestValidateQueueBudgetPerClassFloor(t *testing.T) {
	assert.NoError(t, validateQueueBudget(QueueBudgetValue{}), "zero means auto and is always allowed")

	for field, v := range map[string]int64{
		"relay_bytes":      524288, // 512 KiB: under every class floor
		"worker_bytes":     1 << 20,
		"userevents_bytes": 4 << 20,
	} {
		value := QueueBudgetValue{RelayBytes: 0, WorkerBytes: 0, UserEventsBytes: 0}
		switch field {
		case "relay_bytes":
			value.RelayBytes = v
		case "worker_bytes":
			value.WorkerBytes = v
		case "userevents_bytes":
			value.UserEventsBytes = v
		}
		err := validateQueueBudget(value)
		require.ErrorContains(t, err, "at least", "%s=%d must be refused", field, v)

		floor, ferr := config.QueueBudgetFloor(field)
		require.NoError(t, ferr)
		switch field {
		case "relay_bytes":
			value.RelayBytes = floor
		case "worker_bytes":
			value.WorkerBytes = floor
		case "userevents_bytes":
			value.UserEventsBytes = floor
		}
		assert.NoError(t, validateQueueBudget(value), "%s at its floor is accepted", field)
	}

	// Negative and above-ceiling budgets stay refused.
	require.ErrorContains(t, validateQueueBudget(QueueBudgetValue{RelayBytes: -1}), "negative")
	require.ErrorContains(t, validateQueueBudget(QueueBudgetValue{RelayBytes: config.MaxQueueMemoryBudget + 1}), "ceiling")
}

// TestQueueBudgetDefaultMarshalsZeros pins that 0 (auto-size) survives
// JSON encoding. omitempty would drop it and the preferences dialog
// would render an empty number field instead of 0.
func TestQueueBudgetDefaultMarshalsZeros(t *testing.T) {
	b, err := json.Marshal(QueueBudgetValue{})
	require.NoError(t, err)
	assert.JSONEq(t, `{"relay_bytes":0,"worker_bytes":0,"userevents_bytes":0}`, string(b))
}

// TestValidateMaxMessageSizeDelegates pins that the settings validator
// and channelwire agree on the range: the floor is the largest frame the
// CRDT resume path must carry (MaxPlaintextPerChunk), the ceiling is
// channelwire's configurable maximum.
func TestValidateMaxMessageSizeDelegates(t *testing.T) {
	require.ErrorContains(t, validateMaxMessageSize(0), "positive")
	require.ErrorContains(t, validateMaxMessageSize(-1), "positive")
	require.Error(t, validateMaxMessageSize(int64(channelwire.MaxPlaintextPerChunk)-1), "just under the floor must be refused")
	require.NoError(t, validateMaxMessageSize(int64(channelwire.MaxPlaintextPerChunk)))
	require.NoError(t, validateMaxMessageSize(int64(channelwire.MaxMessageSize)))
	require.NoError(t, validateMaxMessageSize(int64(channelwire.MaxConfigurableMessageSize)))
	require.Error(t, validateMaxMessageSize(int64(channelwire.MaxConfigurableMessageSize)+1))
}

// TestTimeoutsRefuseAValueTheWireCannotCarry pins the ceiling that makes
// UserService.GetTimeouts safe. Without it a stored int64 wrapped on the
// int32 narrowing and handed the client an arbitrary — often negative —
// timeout, which every request budget is computed from.
func TestTimeoutsRefuseAValueTheWireCannotCarry(t *testing.T) {
	over := TimeoutsValue{
		APITimeoutSeconds:          MaxTimeoutSeconds + 1,
		AgentStartupTimeoutSeconds: 300,
		WorktreeCreateSecs:         60,
	}
	require.Error(t, KeyTimeouts.Validate(over), "a budget past the cap must be refused")

	// The exact boundary is legal, and it still fits the int32 the
	// GetTimeouts response carries.
	atMax := TimeoutsValue{
		APITimeoutSeconds:          MaxTimeoutSeconds,
		AgentStartupTimeoutSeconds: MaxTimeoutSeconds,
		WorktreeCreateSecs:         MaxTimeoutSeconds,
	}
	require.NoError(t, KeyTimeouts.Validate(atMax))
	assert.LessOrEqual(t, int64(MaxTimeoutSeconds), int64(math.MaxInt32),
		"the cap must stay inside the int32 the GetTimeouts response carries")

	// The floor is unchanged.
	require.Error(t, KeyTimeouts.Validate(TimeoutsValue{
		APITimeoutSeconds: 0, AgentStartupTimeoutSeconds: 300, WorktreeCreateSecs: 60,
	}))
}

// TestDecodeAndApplyPartialDoNotAliasTheirInputs pins the isolation that
// makes a slice-valued setting safe.
//
// encoding/json REUSES an existing slice's backing array when it decodes
// into one. Decoding straight onto the package-level default would let one
// stored row rewrite the default every later decode starts from, and
// merging straight onto the caller's value would rewrite what a reconciler
// compares against. Both go through a copy, so neither can happen.
func TestDecodeAndApplyPartialDoNotAliasTheirInputs(t *testing.T) {
	type listValue struct {
		Items []string `json:"items"`
	}
	def := listValue{Items: []string{"default-a", "default-b"}}
	key := NewKey[listValue]("test.list").WithDefault(def)

	// A stored row with a SHORTER list fits inside the default's backing
	// array, which is exactly when the decoder reuses it.
	decoded, err := key.Decode(Row{Value: json.RawMessage(`{"items":["stored"]}`)})
	require.NoError(t, err)
	assert.Equal(t, []string{"stored"}, decoded.(listValue).Items)
	assert.Equal(t, []string{"default-a", "default-b"}, def.Items,
		"decoding a stored row must not rewrite the key's default")

	current := listValue{Items: []string{"one", "two"}}
	merged, err := key.ApplyPartial(current, json.RawMessage(`{"items":["merged"]}`))
	require.NoError(t, err)
	assert.Equal(t, []string{"merged"}, merged.(listValue).Items)
	assert.Equal(t, []string{"one", "two"}, current.Items,
		"a partial merge must not rewrite the value it merged onto")
}

// TestValidateSessionDurationCaps pins BOTH ends of the session duration
// rule. The ceiling is what makes SessionDuration safe: it multiplies the
// stored seconds by time.Second, and an int64 with no ceiling WRAPS on
// that multiply into an arbitrary -- possibly negative -- duration, which
// reads as an already expired session and signs everyone out.
func TestValidateSessionDurationCaps(t *testing.T) {
	require.NoError(t, validateSessionDuration(MinSessionDurationSeconds))
	require.NoError(t, validateSessionDuration(MaxSessionDurationSeconds))

	require.Error(t, validateSessionDuration(MinSessionDurationSeconds-1))
	err := validateSessionDuration(MaxSessionDurationSeconds + 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "at most")

	// The value that made the multiply wrap. Without the ceiling it passed,
	// and the resulting duration was roughly 49 years instead of 634.
	overflowing := int64(20000000000)
	require.Error(t, validateSessionDuration(overflowing))
	assert.Less(t, (time.Duration(overflowing) * time.Second).Seconds(), float64(overflowing),
		"the guarded input really does wrap the multiply, silently shortening the session")
}

// TestSessionDurationStaysInsideTheDeclaredRange pins the consumer: every
// value the validator accepts converts to a positive duration no larger
// than the declared ceiling.
func TestSessionDurationStaysInsideTheDeclaredRange(t *testing.T) {
	m := NewManager(nil, nil, []Descriptor{KeySessionDurationSeconds})
	s := m.buildSnapshotWith([]store.SettingRow{{
		Key:   KeySessionDurationSeconds.Name(),
		Value: ptrconv.Ptr(strconv.FormatInt(MaxSessionDurationSeconds, 10)),
	}}, nil)
	got := SessionDuration(s)
	assert.Greater(t, got, time.Duration(0))
	assert.Equal(t, time.Duration(MaxSessionDurationSeconds)*time.Second, got)
}

// TestValidateQueueBudgetNamesTheSameFieldOnEveryRun pins the ordered
// walk. The loop returns on the FIRST failure, and Go randomizes map
// iteration, so a document with two bad budgets used to report a different
// field on each run -- an operator correcting the reported field then hit
// the other one, in an order nobody could reproduce.
func TestValidateQueueBudgetNamesTheSameFieldOnEveryRun(t *testing.T) {
	bad := QueueBudgetValue{RelayBytes: -1, WorkerBytes: -1, UserEventsBytes: -1}
	first := validateQueueBudget(bad)
	require.Error(t, first)
	assert.Contains(t, first.Error(), "relay_bytes", "the first declared field is the one reported")
	for range 50 {
		assert.Equal(t, first.Error(), validateQueueBudget(bad).Error(),
			"the reported field must not depend on map iteration order")
	}
}

// TestEnumAllowedIsTheOneAllowedSetForTLSMode pins the single-source rule:
// the SMTP validator's allowed set is the declared enum, so a mode added
// to the declaration is accepted without a second edit.
func TestEnumAllowedIsTheOneAllowedSetForTLSMode(t *testing.T) {
	for _, ev := range tlsModeEnumValues {
		assert.Truef(t, EnumAllowed(tlsModeEnumValues, ev.Value), "declared mode %q must be allowed", ev.Value)
		assert.NoErrorf(t, validateSMTP(SMTPValue{TLSMode: ev.Value}),
			"declared mode %q must pass the validator", ev.Value)
	}
	assert.False(t, EnumAllowed(tlsModeEnumValues, "startls"))
	assert.False(t, EnumAllowed(tlsModeEnumValues, ""))
	assert.Error(t, validateSMTP(SMTPValue{TLSMode: "startls"}))
}
