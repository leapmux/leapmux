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

// newWebAuthnService builds the per-request WebAuthn service shared by the
// auth and user services. The RP config derives from the current settings
// snapshot, so a public_url change applies without a restart. Both
// configuration failures (no keystore, no hub URL) return
// ErrPasskeysUnavailable wrapped with the remediation.
func newWebAuthnService(ctx context.Context, set *settings.Manager, cfg *config.Config, ks *keystore.Keystore, st store.Store) (*hubwebauthn.Service, error) {
	if ks == nil {
		return nil, fmt.Errorf("%w: passkey support is not configured", ErrPasskeysUnavailable)
	}
	rp, err := hubwebauthn.RPConfigFromSettings(set.Snapshot(ctx), cfg.Listen)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrPasskeysUnavailable, err.Error())
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

// passkeysAvailable reports whether passkey ceremonies can run on this hub,
// for the availability signal in GetSystemInfo.
func (s *AuthService) passkeysAvailable(ctx context.Context) bool {
	_, err := s.webauthnService(ctx)
	return err == nil
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
// -- a 500 for ordinary user input. A sentinel added to the webauthn
// package is now classified on every surface at once.
//
// The class decides the CODE only. The rate-limit interceptor accounts on
// the error SENTINEL (auth.ErrInvalidCurrentPassword and
// auth.ErrInvalidReauthProof), never on the connect code, so nothing here
// changes which attempts count against a budget.
//
// Each surface keeps its own message, log payload, and error wrap, because
// those genuinely differ:
// login answers a missing account and a passkey-less account identically
// so the error is not an enumeration oracle, and reauth re-labels a
// credential failure as auth.ErrInvalidReauthProof so the interceptor can
// key on it.
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
