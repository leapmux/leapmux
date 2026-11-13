package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/servicetest"
	"github.com/leapmux/leapmux/internal/hub/settings"
	"github.com/leapmux/leapmux/internal/hub/store"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/util/version"
)

func TestNewSystemServiceRequiresBothDependencies(t *testing.T) {
	t.Parallel()

	st := hubtestutil.OpenTestStore(t)
	set := servicetest.NewSettingsManager(t, st, nil)
	authSvc := newInitializedAuthService(t,
		servicetest.AuthServiceDeps(st, testConfig(), set, auth.NewCredentialLifecycleEffects(nil, nil, nil)))

	assert.PanicsWithValue(t, "service: NewSystemService requires an AuthService", func() {
		service.NewSystemService(nil, set)
	})
	assert.PanicsWithValue(t, "service: NewSystemService requires a settings manager", func() {
		service.NewSystemService(authSvc, nil)
	})
	assert.NotPanics(t, func() { service.NewSystemService(authSvc, set) })
}

// GetTimeouts reports the configured API timeout to an authenticated caller,
// and refuses a caller with no identity: the method requires account:read.
func TestSystemService_GetTimeouts(t *testing.T) {
	t.Parallel()

	st := hubtestutil.OpenTestStore(t)
	set := servicetest.NewSettingsManager(t, st, nil)
	require.NoError(t, set.Update(context.Background(), settings.KeyTimeouts, json.RawMessage(`{"api_seconds":11}`)))
	svc := service.NewSystemService(newInitializedAuthService(t,
		servicetest.AuthServiceDeps(st, testConfig(), set, auth.NewCredentialLifecycleEffects(nil, nil, nil))), set)

	ctx := auth.WithUser(context.Background(), &auth.UserInfo{ID: userid.MustNew("u-timeouts")})
	resp, err := svc.GetTimeouts(ctx, &leapmuxv1.GetTimeoutsRequest{})
	require.NoError(t, err)
	assert.EqualValues(t, 11, resp.GetApiTimeoutSeconds())

	_, err = svc.GetTimeouts(context.Background(), &leapmuxv1.GetTimeoutsRequest{})
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

// The system information comes from the AuthService the service holds, so it
// answers the setup question the sign-in flows decide: an empty hub requires
// setup, and one with an account does not.
func TestSystemService_GetSystemInfoReadsTheAuthServicesSetupState(t *testing.T) {
	t.Parallel()

	client, _, _ := setupEmptyAuthTestServer(t, testConfig(), nil)
	info, err := client.GetSystemInfo(context.Background(), &leapmuxv1.GetSystemInfoRequest{})
	require.NoError(t, err)
	assert.True(t, info.GetSetupRequired())

	created, err := client.SignUp(t.Context(), &leapmuxv1.SignUpRequest{Username: "initial-admin", Password: "initial-admin-password"})
	require.NoError(t, err)
	require.True(t, created.GetSessionReport().GetUser().GetIsAdmin())
	info, err = client.GetSystemInfo(context.Background(), &leapmuxv1.GetSystemInfoRequest{})
	require.NoError(t, err)
	assert.False(t, info.GetSetupRequired())
}

// The hub reports its build in the BuildInfo message that a worker's system
// information carries, built by the one helper in util/version.
func TestSystemService_GetSystemInfoReportsTheBuild(t *testing.T) {
	t.Parallel()

	client, _, _ := setupEmptyAuthTestServer(t, testConfig(), nil)
	info, err := client.GetSystemInfo(context.Background(), &leapmuxv1.GetSystemInfoRequest{})
	require.NoError(t, err)

	build := info.GetBuild()
	require.NotNil(t, build, "the reply carries the build message")
	assert.Equal(t, version.Value, build.GetVersion())
	assert.Equal(t, version.CommitHash, build.GetCommitHash())
	assert.Equal(t, version.CommitTime, build.GetCommitTime())
	assert.Equal(t, version.BuildTime, build.GetBuildTime())
	assert.Equal(t, version.Branch, build.GetBranch())
}

// A hub with no stored timeouts reports the default API timeout, never zero,
// which a client would read as "no deadline".
func TestSystemService_GetTimeoutsReportsTheDefaultWhenUnset(t *testing.T) {
	t.Parallel()

	st := hubtestutil.OpenTestStore(t)
	set := servicetest.NewSettingsManager(t, st, nil)
	svc := service.NewSystemService(newInitializedAuthService(t, servicetest.AuthServiceDeps(
		st, testConfig(), set, auth.NewCredentialLifecycleEffects(nil, nil, nil))), set)

	ctx := auth.WithUser(context.Background(), &auth.UserInfo{ID: userid.MustNew("u-default-timeouts")})
	resp, err := svc.GetTimeouts(ctx, &leapmuxv1.GetTimeoutsRequest{})
	require.NoError(t, err)
	assert.EqualValues(t, settings.DefaultTimeouts.APITimeoutSeconds, resp.GetApiTimeoutSeconds())
	assert.Positive(t, resp.GetApiTimeoutSeconds())
}

// A solo hub has its one account from the start, so it never asks for setup,
// even though no administrator signed up.
func TestSystemService_GetSystemInfoNeverRequiresSetupInSolo(t *testing.T) {
	t.Parallel()

	info := systemInfoForSolo(t, fakeListenReporter{primary: "127.0.0.1:4327"}, tcpCtx("127.0.0.1"), false)
	assert.NotEqual(t, leapmuxv1.SoloAccess_SOLO_ACCESS_UNSPECIFIED, info.GetSoloAccess(), "a solo hub reports a solo access state")
	assert.False(t, info.GetSetupRequired())
}

// A failed read of the enabled OAuth providers degrades: the sign-in page
// hides the provider buttons rather than failing the whole system
// information. The failure must still reach the log, or an operator has no
// trace of why the buttons are gone.
//
// Not parallel: it captures slog's process-global default logger.
func TestSystemService_GetSystemInfoLogsAFailedProviderRead(t *testing.T) {
	logs := testutil.CaptureDefaultLogger(t)
	st := hubtestutil.OpenTestStore(t)
	hubtestutil.CreateTestAdmin(t, st)
	set := servicetest.NewSettingsManager(t, st, nil)
	svc := service.NewSystemService(newInitializedAuthService(t, servicetest.AuthServiceDeps(
		failingProviderListStore{st}, testConfig(), set, auth.NewCredentialLifecycleEffects(nil, nil, nil))), set)

	info, err := svc.GetSystemInfo(context.Background(), &leapmuxv1.GetSystemInfoRequest{})
	require.NoError(t, err, "a failed provider read degrades rather than failing the request")
	assert.False(t, info.GetOauthEnabled(), "an unread provider list reports OAuth as off")
	assert.Contains(t, logs.String(), "level=WARN")
	assert.Contains(t, logs.String(), errProviderListFailed.Error(), "the log carries the read's error")
}

var errProviderListFailed = errors.New("provider list read failed")

// failingProviderListStore fails every read of the enabled OAuth providers.
type failingProviderListStore struct{ store.Store }

func (s failingProviderListStore) OAuthProviders() store.OAuthProviderStore {
	return failingProviderList{s.Store.OAuthProviders()}
}

type failingProviderList struct{ store.OAuthProviderStore }

func (failingProviderList) ListEnabled(context.Context) ([]store.OAuthProviderSummary, error) {
	return nil, errProviderListFailed
}
