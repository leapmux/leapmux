package settings

import (
	"testing"

	"github.com/leapmux/leapmux/channelwire"
	"github.com/leapmux/leapmux/internal/hub/config"
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
