package service

import (
	"context"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/settings"
)

// SystemService implements the leapmux.v1.SystemService ConnectRPC handler:
// what the hub offers before sign-in, and the timeouts every client applies.
//
// It holds the AuthService, which builds the system information from the facts
// of the sign-in flows, and the settings manager, which supplies the timeouts.
type SystemService struct {
	auth     *AuthService
	settings *settings.Manager
}

// NewSystemService builds the service over the hub's AuthService and its
// settings manager. Neither has an absent form: a nil one would fail every call
// of a method, so it panics here rather than on a request.
func NewSystemService(authSvc *AuthService, set *settings.Manager) *SystemService {
	if authSvc == nil {
		panic("service: NewSystemService requires an AuthService")
	}
	if set == nil {
		panic("service: NewSystemService requires a settings manager")
	}
	return &SystemService{auth: authSvc, settings: set}
}

func (sys *SystemService) GetSystemInfo(ctx context.Context, _ *leapmuxv1.GetSystemInfoRequest) (*leapmuxv1.GetSystemInfoResponse, error) {
	return sys.auth.systemInfo(ctx)
}

// GetTimeouts reports the deadline a client applies to one API call.
func (sys *SystemService) GetTimeouts(ctx context.Context, _ *leapmuxv1.GetTimeoutsRequest) (*leapmuxv1.GetTimeoutsResponse, error) {
	if _, err := auth.MustGetUser(ctx); err != nil {
		return nil, err
	}

	t := settings.KeyTimeouts.Of(sys.settings.Snapshot(ctx))
	// The narrowing conversion is safe because validateTimeouts caps every
	// budget at settings.MaxTimeoutSeconds, and the snapshot degrades a value
	// that fails validation to the default. Widen that cap and the wire type
	// has to widen with it.
	return &leapmuxv1.GetTimeoutsResponse{
		ApiTimeoutSeconds: int32(t.APITimeoutSeconds),
	}, nil
}
