package auth

import (
	"context"
	"log/slog"
	"sync/atomic"

	"github.com/leapmux/leapmux/internal/hub/password"
	"github.com/leapmux/leapmux/internal/hub/peer"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/usernames"
)

// SoloGate decides whether a caller may be authenticated as the solo user with
// NO credentials at all.
//
// It is a type rather than a method because two authentication ladders ask the
// question and must answer alike: the ConnectRPC interceptor, and
// AuthenticateHTTP, which serves the WebSocket relays. One instance per hub,
// so the latch below is shared and the answer cannot differ between a request
// and the socket it opens.
//
// THE RULE, in two cases:
//
//   - The LOCAL IPC socket, always. A unix socket is reachable only from this
//     machine, its file mode restricts it to this user, and it is how the
//     desktop app reaches its own hub. There is no third party to authenticate.
//   - Any transport while the account holds NO PASSWORD. This is the bootstrap
//     state: a hub run outside the desktop app is reached from a browser over
//     TCP and nothing else, so refusing here would leave no way to set the
//     first password.
//
// Once the account has a password, every TCP address asks for it -- the one
// -listen named, 127.0.0.1 included, and every extra address alike.
//
// Loopback buys no exemption, and that is deliberate. When an operator adds
// `*:4327` to a hub already serving `127.0.0.1:4327`, the two MERGE into one
// socket, so no per-listener or per-address rule could tell a local browser
// from a LAN one. Reading a loopback port also does not make a process the
// owner of the account.
type SoloGate struct {
	store store.Store
	// passwordSet latches TRUE once the account holds a password.
	//
	// One-way, because the value is one-way: every in-process path that
	// touches this account's password SETS one. The single path that CLEARS a
	// password -- FinishAccountRecoveryPasskey, which replaces it with a
	// passkey -- is refused in solo mode, and
	// TestSoloRefusesEveryPathThatCanClearAPassword pins that premise, so a
	// latch that never clears cannot go stale.
	//
	// While it is false the store is read per request. That is the bootstrap
	// state, it ends at the first password, and it costs one indexed read on a
	// hub with exactly one user.
	passwordSet atomic.Bool
}

// NewSoloGate builds the gate over st. A nil store gives a gate that reports
// every caller credential-free, which is what a solo hub with no database
// would be anyway.
func NewSoloGate(st store.Store) *SoloGate {
	return &SoloGate{store: st}
}

// CredentialFree reports whether this request may be authenticated as the solo
// user without presenting anything.
//
// A NIL gate answers true, and that is not a fail-open default: the gate is
// reached only after the caller has already established that this hub runs in
// solo mode, and a hub with no gate is one nothing configured a password on.
// Every construction path builds one, so nil is reachable only from a test
// that states solo mode and nothing else.
func (g *SoloGate) CredentialFree(ctx context.Context) bool {
	if peer.IsLocalIPC(ctx) {
		return true
	}
	if g == nil {
		return true
	}
	return !g.accountHasPassword(ctx)
}

// accountHasPassword reads the latch, falling back to the store.
//
// A store failure answers TRUE -- "the account has a password" -- so the
// caller is asked to sign in. Failing the other way would hand an
// unauthenticated TCP caller the administrator whenever the database hiccuped.
func (g *SoloGate) accountHasPassword(ctx context.Context) bool {
	if g.passwordSet.Load() {
		return true
	}
	if g.store == nil {
		return false
	}
	user, err := g.store.Users().GetByUsername(ctx, usernames.Solo)
	if err != nil {
		slog.Warn("could not read the solo account while deciding whether a caller must sign in; asking for credentials",
			"error", err)
		return true
	}
	// password.IsUsable, not user.PasswordSet. The column is a claim the
	// creating flow makes, and the solo bootstrap sets it TRUE with an empty
	// hash -- so reading it here would demand a sign-in that no password can
	// satisfy, on a hub whose owner never set one. The hash is the fact.
	if password.IsUsable(user.PasswordHash) {
		g.passwordSet.Store(true)
		return true
	}
	return false
}

// PasswordSet reports whether the solo account holds a password, for the
// administration surface and for GetSystemInfo. It reads the same latch the
// gate decides on, so the two can never disagree.
func (g *SoloGate) PasswordSet(ctx context.Context) bool {
	if g == nil {
		return false
	}
	return g.accountHasPassword(ctx)
}

// NotePasswordSet records that the account now holds a password.
//
// It is an OPTIMIZATION and not the enforcement, and the difference matters to
// anyone changing this file. What enforces the rule is the store read in
// accountHasPassword: while the latch is false every request asks the database,
// so a password committed by any path -- this process, the admin CLI, another
// hub on the same database -- is honored on the very next request whether or
// not anything called this. TestSoloGate_SeesAPasswordWrittenAfterItRead pins
// exactly that, with no call to this method.
//
// So calling it here buys one thing: the requests between the write and the
// first store read skip that read. Do NOT take the store fallback away in
// favour of this latch. A gate seeded only at construction and updated only
// here would never learn about a password set through any other path, and TCP
// would stay credential-free until the hub restarted.
func (g *SoloGate) NotePasswordSet() {
	if g == nil {
		return
	}
	g.passwordSet.Store(true)
}
