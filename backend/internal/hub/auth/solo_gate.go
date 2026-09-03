package auth

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"

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
// -listen gives, 127.0.0.1 included, and every extra address alike.
//
// Loopback buys no exemption, and that is deliberate. When an operator adds
// `*:4327` to a hub already serving `127.0.0.1:4327`, the two MERGE into one
// socket, so no per-listener or per-address rule could tell a local browser
// from a LAN one. Reading a loopback port also does not make a process the
// owner of the account.
type SoloGate struct {
	// solo is the hub's mode, and a gate that is not solo answers NO to every
	// question without reading anything.
	//
	// It lives here because every caller asked it anyway: GetSystemInfo spelled
	// `cfg.SoloMode &&` in front of two of its three reads and buried it inside
	// the third, and the administration surface forgot it entirely -- so a
	// `leapmux hub` looked up a `solo` row that can never exist (the username is
	// reserved in every creation path) on every page load, and could never
	// latch. One object answering for the whole rule is what makes "the solo
	// facts are false on a multi-user hub" a property rather than three call
	// sites that must stay in step.
	solo  bool
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
	// The STORE is the only thing that sets it. Nothing tells the gate a
	// password landed: accountHasPassword reads the row while the latch is
	// false, so a password committed by any path -- this process, the admin
	// CLI, another hub on the same database -- is honored on the very next
	// request. A setter beside that read would be a second way in for a value
	// with one source, and it could save at most the one read that sets it.
	//
	// While it is false the store is read per request, and that state does NOT
	// end on its own: a hub kept on loopback is never asked for a password --
	// soloPasswordSetupRequired demands one only for an address another
	// machine can reach -- so the default `leapmux solo` pays the read for the
	// life of the process. It is one indexed read of a table with one row, in
	// the process that owns the database, and it is what makes a password set
	// through any other path arm the rule on the very next request. A caller
	// that needs the answer SEVERAL times in one request reads it once and
	// passes it down; GetSystemInfo does.
	passwordSet atomic.Bool
}

// NewSoloGate builds the gate for a hub in mode `solo`, over st.
//
// A gate that is NOT solo refuses every caller and reports no password. There
// is no solo account on such a hub and no rule for it to decide.
//
// A NIL store gives a gate that admits the local IPC socket and refuses every
// other transport: it cannot read the account, and a gate that cannot read the
// account must not hand out the administrator. See CredentialFree.
func NewSoloGate(solo bool, st store.Store) *SoloGate {
	return &SoloGate{solo: solo, store: st}
}

// CredentialFree reports whether this request may be authenticated as the solo
// user without presenting anything.
//
// ONE RULE: only a gate that can read the account admits anybody. A nil gate
// and a gate over a nil store both answer FALSE, because neither can tell
// whether a password exists, and "I cannot tell" must never mean "come in as
// the administrator". Answering true instead put the fail-open combination one
// forgotten field away -- `HTTPAuthOpts` carries SoloUser, SoloGate and Store
// independently, so a caller that set the first and neither of the others got
// a gate that knew nothing and admitted everything. This makes that
// unreachable by construction rather than by an audit of the call sites.
//
// The LOCAL IPC test answers first, and it must. A unix socket is reachable
// only from this machine and its file mode restricts it to this user, so the
// desktop app is admitted whatever the gate can or cannot read -- a broken
// database, or a hub that never configured one, cannot lock it out of its own
// hub.
//
// PasswordSet fails the other way for the same gate: it answers false, because
// "no password" is what an unconfigured gate honestly knows. The two are not
// symmetric on purpose -- one decides an admission and the other reports a
// fact, and the safe answer differs.
//
// A gate that is not SOLO refuses before any of this, the local socket
// included: `leapmux hub` has no solo account and no credential-free rung, so
// the desktop app there signs in like every other client. A NIL gate is the
// different case -- nobody configured anything, so it keeps the local socket
// rather than locking the desktop app out of a hub that stated solo mode and
// nothing else.
func (g *SoloGate) CredentialFree(ctx context.Context) bool {
	if g == nil {
		return peer.IsLocalIPC(ctx)
	}
	if !g.solo {
		return false
	}
	if peer.IsLocalIPC(ctx) {
		return true
	}
	if g.store == nil {
		return false
	}
	return !g.accountHasPassword(ctx)
}

// accountHasPassword reads the latch, falling back to the store.
//
// A store failure answers TRUE -- "the account has a password" -- so the
// caller is asked to sign in. Failing the other way would hand an
// unauthenticated TCP caller the administrator whenever the database hiccuped.
//
// NOT FOUND is not a failure, and separating the two is what keeps the answer
// honest in the two deployments where the row is absent. `leapmux hub` and
// `leapmux dev` never create it -- the username is reserved everywhere -- so
// the fail-closed branch would log an authentication warning about a decision
// those hubs never make, on every call of the administration surface, and
// report a password on an account that does not exist. And a solo hub whose
// row an administrator deleted would demand a password no account holds, with
// no way back short of a restart. An absent account holds no password, which
// is what this answers.
func (g *SoloGate) accountHasPassword(ctx context.Context) bool {
	if g.passwordSet.Load() {
		return true
	}
	if g.store == nil {
		return false
	}
	user, err := g.store.Users().GetByUsername(ctx, usernames.Solo)
	if errors.Is(err, store.ErrNotFound) {
		return false
	}
	if err != nil {
		slog.Warn("could not read the solo account while deciding whether a caller must sign in; asking for credentials",
			"error", err)
		return true
	}
	// HasUsablePassword, not FirstCredentialExempt. That column is a claim the
	// creating flow makes, and the solo bootstrap sets it TRUE with an empty
	// hash -- so reading it here would demand a sign-in that no password can
	// satisfy, on a hub whose owner never set one. The hash is the fact.
	if user.HasUsablePassword() {
		g.passwordSet.Store(true)
		return true
	}
	return false
}

// PasswordSet reports whether the solo account holds a password, for the
// administration surface and for GetSystemInfo. It reads the same latch the
// gate decides on, so the two can never disagree.
func (g *SoloGate) PasswordSet(ctx context.Context) bool {
	if g == nil || !g.solo {
		return false
	}
	return g.accountHasPassword(ctx)
}

// CredentialFreeAndPasswordSet answers BOTH questions from one store read.
//
// GetSystemInfo reports three solo facts and every one of them rests on the
// same row. Asking through CredentialFree and PasswordSet separately costs a
// read each while the latch is false -- which is the whole life of a hub that
// never sets a password -- so the public, unauthenticated call every page load
// makes paid three round trips for one fact.
//
// It reads the store BEFORE the local-IPC test, unlike CredentialFree, and
// that costs the desktop app nothing: the caller wants the password answer as
// well, so the read happens either way. The property CredentialFree protects
// still holds, because an unreadable store answers "has a password" and the
// local socket is admitted on top of that answer rather than behind it.
//
// A gate that cannot read the account answers exactly as CredentialFree does:
// the local socket comes in, nothing else does, and the password is reported
// absent.
func (g *SoloGate) CredentialFreeAndPasswordSet(ctx context.Context) (credentialFree, passwordSet bool) {
	if g == nil {
		return peer.IsLocalIPC(ctx), false
	}
	if !g.solo {
		return false, false
	}
	if g.store == nil {
		return peer.IsLocalIPC(ctx), false
	}
	passwordSet = g.accountHasPassword(ctx)
	return peer.IsLocalIPC(ctx) || !passwordSet, passwordSet
}
