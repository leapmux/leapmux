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

// SoloGate decides whether a caller may use the solo account without a
// credential.
//
// It is a type rather than a method because two authentication ladders ask the
// question and must answer alike: the ConnectRPC interceptor, and
// AuthenticateHTTP, which serves the WebSocket relays. One instance per hub,
// so the latch below is shared and the answer cannot differ between a request
// and the socket it opens.
//
// Only local IPC receives credential-free access. A TCP caller can use the
// public first-password procedure while the account has no password. It cannot
// use the synthetic solo administrator.
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
	// While it is false, GetSystemInfo reads the store for each request, and
	// SetInitialSoloPassword is the path that ends that state. The read is one
	// indexed read of a table with one row, in the process that owns the
	// database, and it is what makes a password set through any other path arm
	// the rule on the very next request. A caller that needs the answer
	// SEVERAL times in one request reads it once and passes it down;
	// GetSystemInfo does.
	passwordSet atomic.Bool
}

// NewSoloGate builds the gate for a hub in mode `solo`, over st.
//
// A gate that is NOT solo refuses every caller and reports no password. There
// is no solo account on such a hub and no rule for it to decide.
//
// A nil store still admits local IPC. PasswordSet reports false because it
// cannot read the solo account.
func NewSoloGate(solo bool, st store.Store) *SoloGate {
	return &SoloGate{solo: solo, store: st}
}

// CredentialFree reports whether the request arrived through local IPC on a
// solo hub. The password state does not affect this decision.
func (g *SoloGate) CredentialFree(ctx context.Context) bool {
	if g == nil {
		return peer.IsLocalIPC(ctx)
	}
	if !g.solo {
		return false
	}
	return peer.IsLocalIPC(ctx)
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
