package settings

import (
	"context"
	"encoding/json"
	"math"
	"strconv"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
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
// those writes, with an error that specifies a field on a different row
// and no way for the operator to reach it. Enabled() is what keeps the pair
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

// TestEmailVerificationEffectiveFollowsSMTP pins that the verification
// requirement tracks SMTP.Enabled() only.
func TestEmailVerificationEffectiveFollowsSMTP(t *testing.T) {
	m, _, _ := newTestManagerWithStore(t)
	ctx := context.Background()

	assert.False(t, EmailVerificationEffective(m.Snapshot(ctx)))

	require.NoError(t, m.Update(ctx, KeySMTP, json.RawMessage(`{"host":"smtp.example.com"}`)))
	assert.False(t, EmailVerificationEffective(m.Snapshot(ctx)), "host alone is not enough")

	require.NoError(t, m.Update(ctx, KeySMTP, json.RawMessage(`{"from_address":"hub@example.com"}`)))
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
// config validator enforced and the settings rewrite dropped: an
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

// TestValidateMailLimitsBounds pins the knobs' bounds. The cooldown's
// ceiling is the resend cooldown it backs (60s): a longer window would
// leave one failed send blocking an account's mail longer than a
// successful send does, and failedSendBlockedUntil (service layer) clamps to
// the same bound as a second guard.
func TestValidateMailLimitsBounds(t *testing.T) {
	assert.NoError(t, validateMailLimits(DefaultMailLimits), "the shipped defaults must validate")
	assert.NoError(t, validateMailLimits(MailLimitsValue{RecipientWindowSeconds: 3600}),
		"zero cooldown and zero recipient max are the stored meanings of 'block nothing' and 'unlimited'; the window stays a real number because an omitted field merges the default back at decode")

	for name, v := range map[string]MailLimitsValue{
		"cooldown over the resend cooldown": {FailureCooldownSeconds: 61},
		"cooldown negative":                 {FailureCooldownSeconds: -1},
		"recipient max too high":            {RecipientMax: 1001},
		"recipient max negative":            {RecipientMax: -1},
		"window under a minute":             {RecipientWindowSeconds: 59},
		"window over a day":                 {RecipientWindowSeconds: 86401},
	} {
		assert.Error(t, validateMailLimits(v), "%s must be refused", name)
	}
	assert.NoError(t, validateMailLimits(MailLimitsValue{FailureCooldownSeconds: 60, RecipientWindowSeconds: 60}),
		"a cooldown of exactly the resend cooldown is the most blockade a failed send may leave")
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
	require.Error(t, validateMaxMessageSize(int64(contracts.MaxPlaintextPerChunk)-1), "just under the floor must be refused")
	require.NoError(t, validateMaxMessageSize(int64(contracts.MaxPlaintextPerChunk)))
	require.NoError(t, validateMaxMessageSize(int64(contracts.MaxMessageSize)))
	require.NoError(t, validateMaxMessageSize(int64(contracts.MaxConfigurableMessageSize)))
	require.Error(t, validateMaxMessageSize(int64(contracts.MaxConfigurableMessageSize)+1))
}

// TestTimeoutsRefuseAValueTheWireCannotCarry pins the ceiling that makes
// UserService.GetTimeouts safe. Without it a stored int64 wrapped on the
// int32 narrowing and handed the client an arbitrary — often negative —
// timeout, which every request budget derives from.
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

// TestValidateQueueBudgetReportsTheSameFieldOnEveryRun pins the ordered
// walk. The loop returns on the FIRST failure, and Go randomizes map
// iteration, so a document with two bad budgets used to report a different
// field on each run -- an operator correcting the reported field then hit
// the other one, in an order nobody could reproduce.
func TestValidateQueueBudgetReportsTheSameFieldOnEveryRun(t *testing.T) {
	bad := QueueBudgetValue{RelayBytes: -1, WorkerBytes: -1, UserEventsBytes: -1}
	first := validateQueueBudget(bad)
	require.Error(t, first)
	assert.Contains(t, first.Error(), "relay_bytes", "the loop reports the first declared field")
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

// TestBaseURLResolvesAWildcardBindToLoopback pins the address the hub prints
// into every mail link, into the device-code verification_uri the CLI shows,
// and registers as the OAuth redirect_uri.
//
// A wildcard bind specifies no host, so nothing can send a browser to it. Only
// the ":port" spelling resolved; "0.0.0.0:4327" and "[::]:4327" passed
// through and produced "http://0.0.0.0:4327", which every one of those
// readers then printed. The rest of the hub already treats a wildcard bind
// as a loopback deployment -- webauthn.servesLoopback accepts one, and the
// captcha secure-context gate reads it the same way.
func TestBaseURLResolvesAWildcardBindToLoopback(t *testing.T) {
	t.Parallel()

	m := NewManager(nil, nil, []Descriptor{KeyPublicURL, KeySecureCookies})
	plain := m.buildSnapshotWith(nil, nil)

	for name, tc := range map[string]struct{ listen, want string }{
		"port only":     {":4327", "http://localhost:4327"},
		"ipv4 wildcard": {"0.0.0.0:4327", "http://localhost:4327"},
		"ipv6 wildcard": {"[::]:4327", "http://localhost:4327"},
		// The CLASS, not the two spellings anybody writes. A literal pair
		// read these as real hosts and printed http://[::0]:4327 into every
		// verification mail; httpsec.IsWildcardHost answers for all of them.
		"ipv6 wildcard zero":    {"[::0]:4327", "http://localhost:4327"},
		"ipv6 wildcard spelled": {"[0:0:0:0:0:0:0:0]:4327", "http://localhost:4327"},
		// No TCP listener at all: hostless on purpose, so
		// RPConfigFromSettings reports passkeys unavailable rather than
		// running a ceremony against an origin the hub invented.
		"empty":             {"", "http://"},
		"explicit loopback": {"127.0.0.1:4327", "http://127.0.0.1:4327"},
		"explicit host":     {"hub.example.com:4327", "http://hub.example.com:4327"},
		"host with no port": {"hub.example.com", "http://hub.example.com"},
		"ipv6 literal":      {"[::1]:4327", "http://[::1]:4327"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, BaseURL(plain, tc.listen))
		})
	}

	// public_url still wins outright, and secure_cookies still picks the
	// scheme: the wildcard rule is the last resort, not a new first rung.
	published := m.buildSnapshotWith([]store.SettingRow{{
		Key: KeyPublicURL.Name(), Value: ptrconv.Ptr(`"https://hub.example.com"`),
	}}, nil)
	assert.Equal(t, "https://hub.example.com", BaseURL(published, "0.0.0.0:4327"))

	secure := m.buildSnapshotWith([]store.SettingRow{{
		Key: KeySecureCookies.Name(), Value: ptrconv.Ptr("true"),
	}}, nil)
	assert.Equal(t, "https://localhost:4327", BaseURL(secure, "0.0.0.0:4327"))
}

// TestValidateExtraListenAccepts pins what the address picker produces: the
// family-neutral wildcard, either family's wildcard, and any IP literal.
func TestValidateExtraListenAccepts(t *testing.T) {
	require.NoError(t, validateExtraListen(ExtraListenValue{}), "no addresses is the default")
	require.NoError(t, validateExtraListen(ExtraListenValue{Addresses: []string{
		"*:4327", ":9000", "0.0.0.0:8080", "[::]:8081",
		"192.168.1.24:8080", "127.0.0.1:4327", "[::1]:4327", "[fe80::1%en0]:4327",
	}}))
}

// TestValidateExtraListenRefuses covers every rule the write path enforces.
// The admin CLI writes this key too, so a refusal the picker makes
// unreachable still has to hold here.
func TestValidateExtraListenRefuses(t *testing.T) {
	for _, tc := range []struct {
		name    string
		in      []string
		wantMsg string
	}{
		{"unparseable", []string{"nonsense"}, "extra listen address 1"},
		{"no port", []string{"192.168.1.24"}, "extra listen address 1"},
		{"port zero", []string{"192.168.1.24:0"}, "extra listen address 1"},
		{"port above the range", []string{"192.168.1.24:65536"}, "extra listen address 1"},
		{"unbracketed IPv6 is ambiguous", []string{"::1:4327"}, "extra listen address 1"},
		// A name binds whatever it resolves to AT BIND TIME, so a DNS change
		// nobody made here could publish the hub on a public address.
		{"a host name", []string{"hub.example:4327"}, "is a host name"},
		{"localhost is still a name", []string{"localhost:4327"}, "is a host name"},
		// The index identifies the offending entry, so an operator editing a list
		// through the CLI is told which one to fix.
		{"the second entry is named", []string{"127.0.0.1:4327", "nonsense"}, "extra listen address 2"},
		{"an exact repeat", []string{"192.168.1.24:8080", "192.168.1.24:8080"}, "repeats"},
		// Two spellings of one socket are one address, so the repeat rule has
		// to read the canonical form rather than the string as written.
		{"a repeat in another spelling", []string{"[::0]:4327", "[0:0:0:0:0:0:0:0]:4327"}, "repeats"},
		{"a case-folded repeat", []string{"[::FFFF:1]:4327", "[::ffff:1]:4327"}, "repeats"},
		// A port a service name gives is the same address as its number, so a
		// list that states one of each way repeats.
		{"a service name repeating its number", []string{"192.168.1.24:http", "192.168.1.24:80"}, "repeats"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateExtraListen(ExtraListenValue{Addresses: tc.in})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantMsg)
		})
	}
}

func TestValidateExtraListenCapsTheCount(t *testing.T) {
	atCap := make([]string, 0, contracts.MaxExtraListenAddresses)
	for i := range contracts.MaxExtraListenAddresses {
		atCap = append(atCap, "127.0.0.1:"+strconv.Itoa(9000+i))
	}
	require.NoError(t, validateExtraListen(ExtraListenValue{Addresses: atCap}), "the cap itself is allowed")

	overCap := append(atCap, "127.0.0.1:9999")
	err := validateExtraListen(ExtraListenValue{Addresses: overCap})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "at most")
}

// The key defaults to no extra addresses, so an untouched hub binds exactly
// what -listen gave it.
func TestKeyExtraListenAddressesDefaultsToNothing(t *testing.T) {
	def, ok := KeyExtraListenAddresses.Default().(ExtraListenValue)
	require.True(t, ok)
	assert.Empty(t, def.Addresses)
}

// Addrs is what the hub binds from, so it must reject a document the
// validator would have refused rather than binding the entries it can parse.
func TestExtraListenValueAddrs(t *testing.T) {
	addrs, err := ExtraListenValue{Addresses: []string{"*:4327", "192.168.1.24:8080"}}.Addrs()
	require.NoError(t, err)
	require.Len(t, addrs, 2)
	assert.Equal(t, "*:4327", addrs[0].String())

	_, err = ExtraListenValue{Addresses: []string{"192.168.1.24:8080", "nonsense"}}.Addrs()
	require.Error(t, err, "one bad entry must fail the whole document, never bind the rest")
}
