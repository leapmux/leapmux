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
