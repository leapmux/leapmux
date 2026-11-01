package service

import (
	"context"
	"fmt"

	"connectrpc.com/connect/v2"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/settings"
	"github.com/leapmux/leapmux/util/version"
)

// SystemService implements the leapmux.v1.SystemService ConnectRPC handler:
// what the hub offers before sign-in, and the timeouts every client applies.
//
// It holds the AuthService because the facts GetSystemInfo reports -- the
// signup gate, the one-way setup latch, passkey availability for an origin,
// solo access and the captcha state -- belong to the sign-in flows, which use
// them too. A second copy of the setup latch here would be a second source of
// truth for whether the hub has its first account.
type SystemService struct {
	auth *AuthService
}

// NewSystemService builds the service over the hub's AuthService.
func NewSystemService(authSvc *AuthService) *SystemService {
	if authSvc == nil {
		panic("service: NewSystemService requires an AuthService")
	}
	return &SystemService{auth: authSvc}
}

func (sys *SystemService) GetSystemInfo(ctx context.Context, _ *leapmuxv1.GetSystemInfoRequest) (*leapmuxv1.GetSystemInfoResponse, error) {
	s := sys.auth
	providers, _ := s.store.OAuthProviders().ListEnabled(ctx)

	var setupRequired bool
	if !s.cfg.SoloMode {
		hasUser, err := s.checkHasAnyUser(ctx)
		if err != nil {
			return nil, wrappedConnectError(connect.CodeInternal, fmt.Errorf("check users: %w", err))
		}
		setupRequired = !hasUser
	}

	// A captcha-config read failure reports captcha as ENABLED, matching
	// the interceptor's fail-closed enforcement on the same store: the
	// opposite polarity would unblock a payload-less submit that the hub
	// then denies, so the form would loop for ever on a mislabeled error. The
	// provider is zero (UNSPECIFIED) on that degraded path — never a
	// wrong concrete provider — and clients treat anything but a known
	// enum as altcha. The rest of the system info stays usable, mirroring
	// the providers flag above.
	captchaCfg := s.captcha.Describe(ctx)
	captchaEnabled := captchaCfg.Enabled
	captchaProvider := captchaCfg.Provider
	captchaSiteKey := captchaCfg.SiteKey()
	altchaAlgorithm := captchaCfg.AltchaAlgorithm()

	// Decide what URL workers should target. Precedence:
	//   1. An explicit public_url setting wins (admin's canonical external URL,
	//      typically used when the hub is behind a reverse proxy).
	//   2. If the bind set names no TCP address (desktop's NoTCP mode), the
	//      browser origin is `tauri://localhost`, which is unusable. Such a hub
	//      can still hold a TCP address the extra_listen_addresses setting
	//      added, so prefer that one and fall back to the local unix-socket /
	//      named-pipe address, which workers can always dial locally.
	//   3. Otherwise leave it empty — the frontend falls back to
	//      window.location.origin, which already reflects whatever proxy or
	//      hostname the user connects through.
	//
	// Case 2 tests the CONFIGURED bind set and NOT the live address. They
	// differ on exactly the deployment this matters for: a desktop hub that
	// gained an extra address has a live address, so testing that one dropped
	// the case and answered "" -- and the frontend's window.location.origin
	// fallback, which case 3 rests on, is `tauri://localhost` there.
	var workerHubURL string
	snap := s.snap(ctx)
	switch {
	case settings.KeyPublicURL.Of(snap) != "":
		workerHubURL = settings.KeyPublicURL.Of(snap)
	case s.cfg.PrimaryTCPListen() == "":
		if addr := s.listenAddr(); addr != "" {
			workerHubURL = settings.BaseURL(snap, addr)
		} else if local, err := s.cfg.LocalListenURLs(); err == nil && len(local) > 0 {
			// The first of several. The worker dials one endpoint; the rest
			// of the local listeners still answer.
			workerHubURL = local[0]
		}
	}

	soloPasswordSet := s.soloGate.PasswordSet(ctx)

	return &leapmuxv1.GetSystemInfoResponse{
		SignupEnabled:   s.signupEnabled(ctx),
		SoloMode:        s.cfg.SoloMode,
		SetupRequired:   setupRequired,
		Build:           version.BuildInfoProto(),
		OauthEnabled:    len(providers) > 0,
		WorkerHubUrl:    workerHubURL,
		EmailEnabled:    settings.KeySMTP.Of(snap).Enabled(),
		PasskeyEnabled:  s.passkeysRunnableForOrigin(ctx, originFromRequest(ctx)),
		CaptchaEnabled:  captchaEnabled,
		SoloAccess:      s.soloAccess(ctx, soloPasswordSet),
		SoloPasswordSet: soloPasswordSet,
		AltchaAlgorithm: altchaAlgorithm,
		CaptchaProvider: captchaProvider,
		CaptchaSiteKey:  captchaSiteKey,
	}, nil
}

// GetTimeouts reports the deadline a client applies to one API call.
func (sys *SystemService) GetTimeouts(ctx context.Context, _ *leapmuxv1.GetTimeoutsRequest) (*leapmuxv1.GetTimeoutsResponse, error) {
	if _, err := auth.MustGetUser(ctx); err != nil {
		return nil, err
	}

	t := settings.KeyTimeouts.Of(sys.auth.snap(ctx))
	// The narrowing conversion is safe because validateTimeouts caps every
	// budget at settings.MaxTimeoutSeconds, and the snapshot degrades a value
	// that fails validation to the default. Widen that cap and the wire type
	// has to widen with it.
	return &leapmuxv1.GetTimeoutsResponse{
		ApiTimeoutSeconds: int32(t.APITimeoutSeconds),
	}, nil
}
