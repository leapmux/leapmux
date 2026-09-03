package auth_test

import (
	"context"
	"net"
	"sync/atomic"
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
// statement lists.
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
			assert.Equal(t, tc.want, auth.NewSoloGate(true, st).CredentialFree(tc.ctx))
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
	require.True(t, user.FirstCredentialExempt, "precondition: the bootstrap claims a password")
	require.False(t, password.IsUsable(user.PasswordHash), "precondition: no hash backs the claim")

	assert.True(t, auth.NewSoloGate(true, st).CredentialFree(tcpCtx("127.0.0.1")),
		"a hub with no usable password must stay reachable, whatever the column says")
	assert.False(t, auth.NewSoloGate(true, st).PasswordSet(context.Background()))
}

// The gate must see a password the moment it is stored, because that write is
// also the moment every TCP caller starts needing one.
func TestSoloGate_SeesAPasswordWrittenAfterItRead(t *testing.T) {
	t.Parallel()
	st := soloStore(t)
	gate := auth.NewSoloGate(true, st)

	require.True(t, gate.CredentialFree(tcpCtx("127.0.0.1")), "precondition: no password yet")

	setSoloPassword(t, st)
	assert.False(t, gate.CredentialFree(tcpCtx("127.0.0.1")),
		"the gate re-reads the store while it has seen no password")
	assert.True(t, gate.PasswordSet(context.Background()))
}

func TestSoloGate_AStoreFailureAsksForCredentials(t *testing.T) {
	t.Parallel()
	st := soloStore(t)
	require.NoError(t, st.Close())

	gate := auth.NewSoloGate(true, st)
	assert.False(t, gate.CredentialFree(tcpCtx("127.0.0.1")),
		"an unreadable store must refuse the credential-free path")
	// The local socket answers before the store is consulted at all, so a
	// broken database cannot lock the desktop app out of its own hub.
	assert.True(t, gate.CredentialFree(ipcCtx()))
}

// A gate that cannot READ the account admits the local socket and nothing else.
//
// Two shapes cannot read it: a nil gate, and a gate built over a nil store.
// Both used to answer "credential-free" for every transport, which put a
// fail-open one forgotten field away -- HTTPAuthOpts carries SoloUser,
// SoloGate and Store independently, so a caller that set the first and neither
// of the others got a gate that knew nothing and admitted everything.
//
// The local IPC socket still comes in, and that is the point of testing it
// here: a hub with no gate must not lock the desktop app out of itself.
func TestSoloGate_ThatCannotReadTheAccountAdmitsOnlyTheLocalSocket(t *testing.T) {
	t.Parallel()
	for name, gate := range map[string]*auth.SoloGate{
		"a nil gate":           nil,
		"a gate with no store": auth.NewSoloGate(true, nil),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.False(t, gate.CredentialFree(tcpCtx("127.0.0.1")),
				"a gate that cannot tell whether a password exists must not hand out the administrator")
			assert.True(t, gate.CredentialFree(ipcCtx()),
				"the local socket answers before the store, so a hub with no gate cannot lock the desktop app out")
			assert.False(t, gate.PasswordSet(context.Background()),
				"no password is what an unconfigured gate honestly knows")

			free, set := gate.CredentialFreeAndPasswordSet(tcpCtx("127.0.0.1"))
			assert.False(t, free)
			assert.False(t, set)
			free, set = gate.CredentialFreeAndPasswordSet(ipcCtx())
			assert.True(t, free)
			assert.False(t, set)

		})
	}
}

// An ABSENT account holds no password, and that is not the same answer as an
// unreadable store.
//
// Two deployments have no `solo` row. `leapmux hub` and `leapmux dev` never
// create one -- the username is reserved in every creation path -- so a gate
// built there answered "has a password" on an account that does not exist, and
// logged a warning about a sign-in decision those hubs never make on every
// call of the administration surface that reads it. And a solo hub whose row
// an administrator deleted demanded a password no account could hold, for
// every TCP address at once, with no way back short of a restart.
func TestSoloGate_AnAbsentAccountHoldsNoPassword(t *testing.T) {
	t.Parallel()
	// A store with no solo row at all: what `leapmux hub` opens.
	st := hubtestutil.OpenTestStore(t)

	gate := auth.NewSoloGate(true, st)
	assert.False(t, gate.PasswordSet(context.Background()),
		"an account that does not exist cannot hold a password")
	assert.True(t, gate.CredentialFree(tcpCtx("127.0.0.1")),
		"a missing account must not demand a password nobody can present")
}

// GetSystemInfo reports three solo facts that all rest on one row, so it reads
// them together. The two answers must agree with the single-question methods,
// or the app's view of the connection and the hub's own rule could differ.
func TestSoloGate_CredentialFreeAndPasswordSet(t *testing.T) {
	t.Parallel()
	st := soloStore(t)

	gate := auth.NewSoloGate(true, st)
	free, set := gate.CredentialFreeAndPasswordSet(tcpCtx("127.0.0.1"))
	assert.True(t, free)
	assert.False(t, set)

	setSoloPassword(t, st)
	free, set = gate.CredentialFreeAndPasswordSet(tcpCtx("127.0.0.1"))
	assert.False(t, free, "TCP asks for the password the account now holds")
	assert.True(t, set)

	// The local socket stays credential-free on the same answer.
	free, set = gate.CredentialFreeAndPasswordSet(ipcCtx())
	assert.True(t, free, "the desktop app cannot present a password and must not be asked")
	assert.True(t, set)

}

// A gate for a hub that is NOT solo answers no to everything, and reads
// nothing to do it.
//
// `leapmux hub` and `leapmux dev` have no solo account -- the username is
// reserved in every creation path -- so a gate that looked anyway found
// nothing, could never latch, and paid one indexed miss on every call of the
// public GetSystemInfo. The local socket is refused too: those hubs have no
// credential-free rung for the desktop app to take.
func TestSoloGate_OutsideSoloModeAnswersNoAndReadsNothing(t *testing.T) {
	t.Parallel()
	inner := soloStore(t)
	setSoloPassword(t, inner)
	st := &countingSoloStore{Store: inner}

	gate := auth.NewSoloGate(false, st)
	assert.False(t, gate.CredentialFree(tcpCtx("127.0.0.1")))
	assert.False(t, gate.CredentialFree(ipcCtx()),
		"a hub with no solo rung admits nobody through it, the local socket included")
	assert.False(t, gate.PasswordSet(context.Background()),
		"there is no solo account to hold a password")

	free, set := gate.CredentialFreeAndPasswordSet(tcpCtx("127.0.0.1"))
	assert.False(t, free)
	assert.False(t, set)

	assert.Zero(t, st.soloLookups.Load(), "a hub with no solo account must not look one up")
}

// countingSoloStore counts lookups of the solo account, so a test can assert
// that a path never asks for a row that cannot exist.
type countingSoloStore struct {
	store.Store
	soloLookups atomic.Int64
}

func (s *countingSoloStore) Users() store.UserStore {
	return countingSoloUsers{UserStore: s.Store.Users(), parent: s}
}

type countingSoloUsers struct {
	store.UserStore
	parent *countingSoloStore
}

func (u countingSoloUsers) GetByUsername(ctx context.Context, username string) (*store.User, error) {
	if username == usernames.Solo {
		u.parent.soloLookups.Add(1)
	}
	return u.UserStore.GetByUsername(ctx, username)
}
