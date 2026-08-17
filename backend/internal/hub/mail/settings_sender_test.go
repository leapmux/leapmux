package mail_test

import (
	"context"
	"net"
	"testing"

	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/hub/mail"
	"github.com/leapmux/leapmux/internal/hub/settings"
	"github.com/leapmux/leapmux/internal/hub/store/sqlite"
	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newSettingsSender builds a settings-backed sender over its own
// in-memory store, seeded (or not) with the SMTP document the test
// passes. tls_mode "none" against the loopback fake keeps the exchange
// plaintext without tripping the credentials-over-cleartext rule (no
// username is set).
func newSettingsSender(t *testing.T, smtpDoc string) mail.Sender {
	t.Helper()
	st, err := sqlite.Open(":memory:", sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	key, err := keystore.GenerateKey()
	require.NoError(t, err)
	ks, err := keystore.New(map[uint32][32]byte{1: key})
	require.NoError(t, err)

	m := settings.NewManager(st, ks, settings.CoreDescriptors())
	require.NoError(t, m.Load(context.Background()))
	if smtpDoc != "" {
		require.NoError(t, m.Update(context.Background(), settings.KeySMTP, []byte(smtpDoc)))
	}
	return mail.NewSettingsSender(m)
}

// probeMessage returns the payload every disabled-path case sends. None
// of these cases reaches a relay, so the content only has to be well
// formed. It is a function rather than a shared value so no case can
// change what the next one sends.
func probeMessage() mail.Message {
	return mail.Message{To: "user@example.com", Subject: "s", Body: "b"}
}

func TestSettingsSenderDisabledWithNothingStored(t *testing.T) {
	sender := newSettingsSender(t, "")
	err := sender.Send(context.Background(), probeMessage())
	assert.ErrorIs(t, err, mail.ErrEmailDisabled, "an unconfigured hub reports email disabled, loudly")
}

// TestSettingsSenderDisabledWithHostButNoFromAddress pins the HALF-STAGED
// relay, which is the state validateSMTP deliberately accepts: the admin
// surface writes one field per row, so the host is stored before the from
// address exists. SMTPValue.Enabled() -- not the validator -- is what
// keeps that document away from Send.
//
// The relay here is a LIVE fake on the loopback address, so the refusal
// cannot be confused with an unreachable host. Nothing must arrive on it,
// and the error must stay the matchable ErrEmailDisabled that every caller
// tests with errors.Is. A guard that asks only for the host turns this
// case into an opaque per-message failure instead.
func TestSettingsSenderDisabledWithHostButNoFromAddress(t *testing.T) {
	srv := newFakeSMTPServer(t, withoutSTARTTLS())
	host, port := hostPort(t, srv.addr())
	sender := newSettingsSender(t, `{"host":"`+host+`","port":`+port+`,"tls_mode":"none"}`)

	err := sender.Send(context.Background(), probeMessage())
	assert.ErrorIs(t, err, mail.ErrEmailDisabled, "a relay host without a from address is half staged, not usable")
	assert.Nil(t, srv.lastMessage(), "the half-staged relay was never contacted")
}

// TestSettingsSenderDisabledWithFromAddressButNoHost pins the mirror
// half-staged document: the from address is stored first and no relay
// exists to carry it.
func TestSettingsSenderDisabledWithFromAddressButNoHost(t *testing.T) {
	sender := newSettingsSender(t, `{"from_address":"hub@example.com","tls_mode":"none"}`)
	err := sender.Send(context.Background(), probeMessage())
	assert.ErrorIs(t, err, mail.ErrEmailDisabled, "a from address without a relay host is half staged, not usable")
}

func TestSettingsSenderDeliversThroughConfiguredSMTP(t *testing.T) {
	srv := newFakeSMTPServer(t, withoutSTARTTLS())
	host, port := hostPort(t, srv.addr())
	sender := newSettingsSender(t,
		`{"host":"`+host+`","port":`+port+`,"from_address":"hub@example.com","tls_mode":"none"}`)

	msg := mail.Message{To: "user@example.com", Subject: "hello", Body: "world"}
	require.NoError(t, sender.Send(context.Background(), msg))

	got := srv.lastMessage()
	require.NotNil(t, got, "the message reached the relay")
	assert.Equal(t, "hub@example.com", got.from)
	assert.Contains(t, got.to, "user@example.com")
	assert.Contains(t, got.data, "Subject: hello")
	assert.Contains(t, got.data, "world")
	assert.False(t, srv.sawAuth(), "no username configured means no AUTH attempt")
}

// hostPort splits the fake server's "127.0.0.1:PORT" address.
func hostPort(t *testing.T, addr string) (string, string) {
	t.Helper()
	host, port, err := net.SplitHostPort(addr)
	require.NoError(t, err)
	return host, port
}
