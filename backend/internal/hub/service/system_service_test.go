package service_test

import (
	"context"
	"encoding/json"
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/servicetest"
	"github.com/leapmux/leapmux/internal/hub/settings"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/util/version"
)

func TestNewSystemServiceRequiresAnAuthService(t *testing.T) {
	t.Parallel()
	assert.PanicsWithValue(t, "service: NewSystemService requires an AuthService", func() {
		service.NewSystemService(nil)
	})
}

// GetTimeouts reports the configured API timeout to an authenticated caller,
// and refuses a caller with no identity: the method requires account:read.
func TestSystemService_GetTimeouts(t *testing.T) {
	t.Parallel()

	st := hubtestutil.OpenTestStore(t)
	set := servicetest.NewSettingsManager(t, st, nil)
	require.NoError(t, set.Update(context.Background(), settings.KeyTimeouts, json.RawMessage(`{"api_seconds":11}`)))
	svc := service.NewSystemService(service.NewAuthService(
		servicetest.AuthServiceDeps(st, testConfig(), set, auth.NewCredentialLifecycleEffects(nil, nil, nil))))

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

	client, st, _ := setupEmptyAuthTestServer(t, testConfig(), nil)
	info, err := client.GetSystemInfo(context.Background(), &leapmuxv1.GetSystemInfoRequest{})
	require.NoError(t, err)
	assert.True(t, info.GetSetupRequired())

	hubtestutil.CreateTestAdmin(t, st)
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
	svc := service.NewSystemService(service.NewAuthService(servicetest.AuthServiceDeps(
		st, testConfig(), servicetest.NewSettingsManager(t, st, nil), auth.NewCredentialLifecycleEffects(nil, nil, nil))))

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
	assert.True(t, info.GetSoloMode())
	assert.False(t, info.GetSetupRequired())
}
