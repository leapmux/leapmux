package service

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"

	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/hub/settings"
	"github.com/leapmux/leapmux/internal/hub/store"
	hubwebauthn "github.com/leapmux/leapmux/internal/hub/webauthn"
)

// ErrPasskeysUnavailable reports a hub configuration state in which passkey
// ceremonies cannot run: no keystore, or no usable browser origin (desktop
// local-only mode). Handlers map it to CodeFailedPrecondition once, so every
// passkey surface answers the same state the same way instead of mixing
// Internal, FailedPrecondition, and silently empty lists.
var ErrPasskeysUnavailable = errors.New("passkeys are not configured on this hub")

// passkeyRPConfig resolves the relying-party parameters for this request, or
// reports why the hub can run no ceremony at all. Both configuration failures
// (no keystore, no hub URL) return ErrPasskeysUnavailable wrapped with the
// remediation.
//
// It is separate from newWebAuthnService because one caller wants the ANSWER
// and not the engine: passkeysRunnableForOrigin asks whether a browser at an
// origin could run a ceremony, which RPConfig alone decides.
//
// The RP config derives from the current settings snapshot, so a public_url
// change applies without a restart.
func passkeyRPConfig(ctx context.Context, set *settings.Manager, cfg *config.Config, ks *keystore.Keystore) (hubwebauthn.RPConfig, error) {
	if ks == nil {
		return hubwebauthn.RPConfig{}, fmt.Errorf("%w: passkey support is not configured", ErrPasskeysUnavailable)
	}
	rp, err := hubwebauthn.RPConfigFromSettings(set.Snapshot(ctx), cfg.Listen)
	if err != nil {
		return hubwebauthn.RPConfig{}, fmt.Errorf("%w: %s", ErrPasskeysUnavailable, err.Error())
	}
	return rp, nil
}

// newWebAuthnService builds the per-request WebAuthn service shared by the
// auth and user services. Call it when a CEREMONY runs; a caller that only
// needs the relying-party answer takes passkeyRPConfig above.
func newWebAuthnService(ctx context.Context, set *settings.Manager, cfg *config.Config, ks *keystore.Keystore, st store.Store) (*hubwebauthn.Service, error) {
	rp, err := passkeyRPConfig(ctx, set, cfg, ks)
	if err != nil {
		return nil, err
	}
	return hubwebauthn.NewService(rp, st, ks)
}

func (s *UserService) webauthnService(ctx context.Context) (*hubwebauthn.Service, error) {
	return newWebAuthnService(ctx, s.set, s.cfg, s.keystore, s.store)
}

func (s *UserService) webauthnServiceWithStore(ctx context.Context, st store.Store) (*hubwebauthn.Service, error) {
	return newWebAuthnService(ctx, s.set, s.cfg, s.keystore, st)
}

func (s *AuthService) webauthnService(ctx context.Context) (*hubwebauthn.Service, error) {
	return newWebAuthnService(ctx, s.set, s.cfg, s.keystore, s.store)
}

// passkeysRunnableForOrigin reports whether passkey ceremonies can run for a
// browser at this origin. It feeds the passkey_enabled signal in
// GetSystemInfo.
//
// TWO conditions, and the second is what a hub-wide answer could not carry.
// The hub must be configured for passkeys (a keystore, and a usable hub URL),
// and the request origin must be one the hub serves. A hub reached by an
// address it does not publish runs no ceremony there: every Begin answers
// ErrOriginNotAllowed. The hub-wide answer stayed true in that state, so the
// sign-in form offered a Passkey option that could only fail, and the account
// panel offered an Add passkey button that could only fail.
//
// Clients show every passkey affordance only when this ONE flag is true, so
// the flag must answer the question those clients really ask: can THIS page
// run a ceremony?
//
// An empty origin keeps the hub-wide answer. A non-browser client sends no
// Origin header and has no browser ceremony to mislead; RPConfig.AllowsOrigin
// states the same rule from its own side.
//
// "Runnable" rather than "available" throughout: the two words read as two
// facts, and this is one.
//
// It resolves the RP config and asks it directly. Through the ceremony
// service, every call built a whole go-webauthn engine -- a settings
// snapshot, an RP resolution and a gowebauthn.New -- to reach a method that
// reads RPConfig.AllowsOrigin and never touches the engine, and then
// discarded it. GetSystemInfo calls this at every page load.
func (s *AuthService) passkeysRunnableForOrigin(ctx context.Context, origin string) bool {
	// A solo account can never HOLD a passkey: rejectSoloPasskeyManagement
	// refuses every management verb, and rejectSolo refuses sign-up and
	// account recovery, so nothing there can register one. The sign-in verbs
	// carry no refusal of their own and need none -- with no credential
	// registered they answer "no passkeys registered" -- but reporting the
	// configured answer offered that sign-in form a Passkey button whose only
	// possible outcome was that message.
	//
	// It was unreachable before this release, because a solo hub showed no
	// sign-in form at all; a solo hub that holds a password now does.
	if s.cfg.SoloMode {
		return false
	}
	rp, err := passkeyRPConfig(ctx, s.set, s.cfg, s.keystore)
	if err != nil {
		return false
	}
	return rp.AllowsOrigin(origin)
}

// originFromRequest returns the browser origin of a Connect request. The
// WebAuthn layer accepts only origins the hub itself configured; an absent
// header (non-browser client) falls back to the default RP ID.
func originFromRequest[T any](req *connect.Request[T]) string {
	return req.Header().Get("Origin")
}

// webAuthnErrorClass says how a passkey surface must answer a ceremony
// error. It is the ONE place that knows the hubwebauthn sentinels.
//
// Every passkey surface asked the same question and answered it
// separately: login and reauth each carried their own switch, and the
// passkey-management surface had no classifier at all, so a cancelled
// platform prompt (ErrAssertionRejected) and an unserved origin
// (ErrOriginNotAllowed) both left FinishPasskeyRegistration as CodeInternal
// -- a 500 for ordinary user input. Now one edit here classifies a new
// webauthn sentinel on every surface at once.
//
// The class decides the CODE only. The rate-limit interceptor accounts on
// the error SENTINEL (auth.ErrInvalidCurrentPassword and
// auth.ErrInvalidElevationAssertion), never on the connect code, so nothing
// here changes which attempts count against a budget.
//
// Each surface keeps its own message, log payload, and error wrap, because
// those genuinely differ:
// login answers a missing account and a passkey-less account identically
// so the error is not an enumeration oracle, and the elevation surface
// re-labels a credential failure as auth.ErrInvalidElevationAssertion so the
// interceptor can key on it.
type webAuthnErrorClass int

const (
	// webAuthnErrorInfrastructure is the default. A store, keystore, or
	// ceremony-session failure is not a credential failure, so it reports
	// CodeInternal rather than telling the caller their credential was
	// refused.
	webAuthnErrorInfrastructure webAuthnErrorClass = iota
	// webAuthnErrorCredential is a ceremony that ran and failed: a missing,
	// expired, or rejected credential. CodeUnauthenticated.
	webAuthnErrorCredential
	// webAuthnErrorClone is a sign-count clone warning. It is a security
	// event, not a login failure: the surface logs it and reports it as
	// itself rather than as a rejected credential.
	webAuthnErrorClone
	// webAuthnErrorUnavailable is a precondition the user can correct: the
	// hub runs no ceremonies, the account has no passkeys, or the browser
	// origin is one the hub does not serve. CodeFailedPrecondition.
	webAuthnErrorUnavailable
)

// classifyWebAuthnError maps a ceremony error to the class its surface must
// answer with. An error it does not recognize is infrastructure.
func classifyWebAuthnError(err error) webAuthnErrorClass {
	switch {
	case errors.Is(err, hubwebauthn.ErrCloneDetected):
		return webAuthnErrorClone
	case errors.Is(err, hubwebauthn.ErrCeremonyInvalid), errors.Is(err, hubwebauthn.ErrAssertionRejected):
		return webAuthnErrorCredential
	case errors.Is(err, hubwebauthn.ErrNoPasskeys), errors.Is(err, hubwebauthn.ErrOriginNotAllowed),
		errors.Is(err, ErrPasskeysUnavailable):
		return webAuthnErrorUnavailable
	default:
		return webAuthnErrorInfrastructure
	}
}
