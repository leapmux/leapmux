package auth_test

import (
	"context"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/bootstrap"
	"github.com/leapmux/leapmux/internal/hub/password"
	"github.com/leapmux/leapmux/internal/hub/peer"
	"github.com/leapmux/leapmux/internal/hub/store"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/hub/usernames"
)

// soloStore opens a store with the solo account bootstrapped.
func soloStore(t *testing.T) store.Store {
	t.Helper()
	st := hubtestutil.OpenTestStore(t)
	require.NoError(t, bootstrap.Run(context.Background(), st, true))
	return st
}

// setSoloPassword gives the solo account a real Argon2id hash.
func setSoloPassword(t *testing.T, st store.Store) {
	t.Helper()
	user, err := st.Users().GetByUsername(context.Background(), usernames.Solo)
	require.NoError(t, err)
	hash, err := password.Hash("correct-horse-battery-staple")
	require.NoError(t, err)
	require.NoError(t, st.Users().UpdatePassword(context.Background(), store.UpdateUserPasswordParams{
		PasswordHash: hash,
		ID:           user.ID,
	}))
}

// tcpCtx is a request that arrived over TCP, whatever the address. Loopback is
// included on purpose: it buys no exemption.
func tcpCtx(ip string) context.Context {
	return peer.WithRemoteAddr(context.Background(),
		&net.TCPAddr{IP: net.ParseIP(ip), Port: 51234})
}

// ipcCtx is a request that arrived on the local IPC socket.
func ipcCtx() context.Context {
	return peer.WithRemoteAddr(peer.WithLocalIPC(context.Background()),
		&net.UnixAddr{Name: "/tmp/hub.sock", Net: "unix"})
}

// The whole rule, as one table. Each row is a case the feature's design
// statement names.
func TestSoloGate_CredentialFree(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		passwordSet bool
		ctx         context.Context
		want        bool
	}{
		{"local IPC, no password", false, ipcCtx(), true},
		// The desktop app must never be asked for a password, whatever the
		// account holds: it reaches its own hub over a socket only this user
		// can open.
		{"local IPC, password set", true, ipcCtx(), true},

		// The bootstrap state. A hub outside the desktop app is reached from a
		// browser over TCP and nothing else, so refusing here would leave no
		// way to set the first password.
		{"TCP loopback, no password", false, tcpCtx("127.0.0.1"), true},
		{"TCP LAN, no password", false, tcpCtx("192.168.1.24"), true},

		// Once a password exists, EVERY TCP address asks for it. Loopback is
		// the case that changed, and it is the point: a merged `*:4327` socket
		// carries loopback and LAN traffic alike, so no address rule could
		// tell them apart.
		{"TCP loopback, password set", true, tcpCtx("127.0.0.1"), false},
		{"TCP IPv6 loopback, password set", true, tcpCtx("::1"), false},
		{"TCP LAN, password set", true, tcpCtx("192.168.1.24"), false},

		// An unmarked context is not local IPC, so it follows the TCP rows.
		{"unmarked, no password", false, context.Background(), true},
		{"unmarked, password set", true, context.Background(), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := soloStore(t)
			if tc.passwordSet {
				setSoloPassword(t, st)
			}
			assert.Equal(t, tc.want, auth.NewSoloGate(st).CredentialFree(tc.ctx))
		})
	}
}

// The bootstrapped solo row claims password_set = true with an EMPTY hash. A
// gate that believed the column would demand a sign-in no password could
// satisfy, on a hub whose owner never set one -- a lockout at first run.
func TestSoloGate_ReadsTheHashAndNotTheColumn(t *testing.T) {
	t.Parallel()
	st := soloStore(t)

	user, err := st.Users().GetByUsername(context.Background(), usernames.Solo)
	require.NoError(t, err)
	require.True(t, user.PasswordSet, "precondition: the bootstrap claims a password")
	require.False(t, password.IsUsable(user.PasswordHash), "precondition: no hash backs the claim")

	assert.True(t, auth.NewSoloGate(st).CredentialFree(tcpCtx("127.0.0.1")),
		"a hub with no usable password must stay reachable, whatever the column says")
	assert.False(t, auth.NewSoloGate(st).PasswordSet(context.Background()))
}

// The gate must see a password the moment it is stored, because that write is
// also the moment every TCP caller starts needing one.
func TestSoloGate_SeesAPasswordWrittenAfterItRead(t *testing.T) {
	t.Parallel()
	st := soloStore(t)
	gate := auth.NewSoloGate(st)

	require.True(t, gate.CredentialFree(tcpCtx("127.0.0.1")), "precondition: no password yet")

	setSoloPassword(t, st)
	assert.False(t, gate.CredentialFree(tcpCtx("127.0.0.1")),
		"the gate re-reads the store while it has seen no password")
	assert.True(t, gate.PasswordSet(context.Background()))
}

// NotePasswordSet closes the window between the write committing and the next
// store read. It must never widen access, only narrow it.
func TestSoloGate_NotePasswordSet(t *testing.T) {
	t.Parallel()
	st := soloStore(t)
	gate := auth.NewSoloGate(st)

	gate.NotePasswordSet()
	assert.False(t, gate.CredentialFree(tcpCtx("127.0.0.1")))
	assert.True(t, gate.PasswordSet(context.Background()))
	// The local socket is still exempt: the latch says the account has a
	// password, not that the desktop app must present one.
	assert.True(t, gate.CredentialFree(ipcCtx()))
}

// A store that cannot answer must not admit an unauthenticated caller. The
// opposite polarity would hand the administrator to anybody who could reach
// the port whenever the database hiccuped.
func TestSoloGate_AStoreFailureAsksForCredentials(t *testing.T) {
	t.Parallel()
	st := soloStore(t)
	require.NoError(t, st.Close())

	gate := auth.NewSoloGate(st)
	assert.False(t, gate.CredentialFree(tcpCtx("127.0.0.1")),
		"an unreadable store must refuse the credential-free path")
	// The local socket answers before the store is consulted at all, so a
	// broken database cannot lock the desktop app out of its own hub.
	assert.True(t, gate.CredentialFree(ipcCtx()))
}

// A nil gate is reachable only from a test that states solo mode and nothing
// else; it must not panic.
func TestSoloGate_NilIsUsable(t *testing.T) {
	t.Parallel()
	var gate *auth.SoloGate
	assert.True(t, gate.CredentialFree(tcpCtx("127.0.0.1")))
	assert.True(t, gate.CredentialFree(ipcCtx()))
	assert.False(t, gate.PasswordSet(context.Background()))
	gate.NotePasswordSet()
}
