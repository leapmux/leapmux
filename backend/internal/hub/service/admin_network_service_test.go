package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/bootstrap"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/servicetest"
	"github.com/leapmux/leapmux/internal/hub/settings"
	"github.com/leapmux/leapmux/internal/hub/store"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
)

// fakeListenReporter states a hub's listeners without binding a socket.
type fakeListenReporter struct {
	bound       []service.BoundListenAddress
	primary     string
	nonLoopback bool
}

func (f fakeListenReporter) Bound() []service.BoundListenAddress { return f.bound }
func (f fakeListenReporter) PrimaryListenAddr() string           { return f.primary }
func (f fakeListenReporter) HasNonLoopbackAddress() bool         { return f.nonLoopback }

func newNetworkService(t *testing.T, cfg *config.Config, listen service.ListenReporter) (*service.AdminNetworkService, store.Store, *settings.Manager) {
	t.Helper()
	st := hubtestutil.OpenTestStore(t)
	require.NoError(t, bootstrap.Run(context.Background(), st, cfg.SoloMode))
	set := servicetest.NewSettingsManager(t, st, nil)
	return service.NewAdminNetworkService(cfg, set, auth.NewSoloGate(st), listen), st, set
}

func TestAdminNetworkService_ReportsTheLiveListeners(t *testing.T) {
	listen := fakeListenReporter{
		bound: []service.BoundListenAddress{
			{Address: "*:4327", Source: "merged"},
			{Address: "192.168.1.24:8080", Source: "extra", Err: "address already in use"},
		},
	}
	svc, _, set := newNetworkService(t, &config.Config{SoloMode: true, Listen: "127.0.0.1:4327"}, listen)

	require.NoError(t, set.Update(context.Background(), settings.KeyExtraListenAddresses,
		json.RawMessage(`{"addresses":["*:4327","192.168.1.24:8080"]}`)))

	resp, err := svc.GetListenStatus(context.Background(), connect.NewRequest(&leapmuxv1.GetListenStatusRequest{}))
	require.NoError(t, err)

	assert.Equal(t, "127.0.0.1:4327", resp.Msg.GetDefaultAddress(),
		"-listen is reported read-only: it is a command-line option, never a setting")
	assert.Equal(t, []string{"*:4327", "192.168.1.24:8080"}, resp.Msg.GetConfigured())

	require.Len(t, resp.Msg.GetBound(), 2)
	assert.Equal(t, "*:4327", resp.Msg.GetBound()[0].GetAddress())
	// "merged" is what lets the panel say the -listen address is served by
	// this one rather than gone.
	assert.Equal(t, "merged", resp.Msg.GetBound()[0].GetSource())
	assert.Empty(t, resp.Msg.GetBound()[0].GetError())

	// A configured address the hub could not bind is REPORTED, not dropped:
	// the panel prints the operating system's reason beside it.
	assert.Equal(t, "192.168.1.24:8080", resp.Msg.GetBound()[1].GetAddress())
	assert.Equal(t, "address already in use", resp.Msg.GetBound()[1].GetError())
}

// The stored list is echoed CANONICALLY, so the picker matches an entry
// against the address it offers whichever spelling the row holds.
func TestAdminNetworkService_CanonicalisesTheConfiguredAddresses(t *testing.T) {
	svc, _, set := newNetworkService(t, &config.Config{SoloMode: true}, fakeListenReporter{})
	require.NoError(t, set.Update(context.Background(), settings.KeyExtraListenAddresses,
		json.RawMessage(`{"addresses":[":4327","[::0]:9000"]}`)))

	resp, err := svc.GetListenStatus(context.Background(), connect.NewRequest(&leapmuxv1.GetListenStatusRequest{}))
	require.NoError(t, err)
	assert.Equal(t, []string{"*:4327", "[::]:9000"}, resp.Msg.GetConfigured())
}

func TestAdminNetworkService_ReportsWhetherTheAccountHoldsAPassword(t *testing.T) {
	svc, st, _ := newNetworkService(t, &config.Config{SoloMode: true}, fakeListenReporter{})

	resp, err := svc.GetListenStatus(context.Background(), connect.NewRequest(&leapmuxv1.GetListenStatusRequest{}))
	require.NoError(t, err)
	assert.False(t, resp.Msg.GetPasswordSet(),
		"the bootstrapped account claims password_set with no hash behind it; the report must read the hash")

	setSoloPasswordForTest(t, st)
	resp, err = svc.GetListenStatus(context.Background(), connect.NewRequest(&leapmuxv1.GetListenStatusRequest{}))
	require.NoError(t, err)
	assert.True(t, resp.Msg.GetPasswordSet())
}

// The picker needs the machine's addresses, and it needs them stable: a list
// that reorders between two openings moves the entry under the pointer.
func TestAdminNetworkService_ListsTheMachineInterfaces(t *testing.T) {
	svc, _, _ := newNetworkService(t, &config.Config{SoloMode: true}, fakeListenReporter{})

	resp, err := svc.GetListenStatus(context.Background(), connect.NewRequest(&leapmuxv1.GetListenStatusRequest{}))
	require.NoError(t, err)
	require.NotEmpty(t, resp.Msg.GetInterfaces(), "every machine has at least a loopback interface")

	var names []string
	sawLoopback := false
	for _, iface := range resp.Msg.GetInterfaces() {
		names = append(names, iface.GetName())
		assert.NotEmpty(t, iface.GetAddresses(), "an interface with no address is omitted, never listed empty")
		for _, addr := range iface.GetAddresses() {
			assert.NotEmpty(t, addr.GetIp())
			parsed := net.ParseIP(stripZone(addr.GetIp()))
			require.NotNilf(t, parsed, "%q must be an IP literal the hub could bind", addr.GetIp())
			assert.Equal(t, parsed.IsLoopback(), addr.GetLoopback())
			if addr.GetLoopback() {
				sawLoopback = true
			}
		}
	}
	assert.True(t, sawLoopback, "the loopback address must be offered: it is the hub's default")
	assert.IsIncreasing(t, names, "the picker must not reorder itself between openings")

	second, err := svc.GetListenStatus(context.Background(), connect.NewRequest(&leapmuxv1.GetListenStatusRequest{}))
	require.NoError(t, err)
	assert.Equal(t, len(resp.Msg.GetInterfaces()), len(second.Msg.GetInterfaces()))
}

// stripZone removes an interface zone so net.ParseIP can read the literal.
func stripZone(ip string) string {
	for i := range len(ip) {
		if ip[i] == '%' {
			return ip[:i]
		}
	}
	return ip
}

// A hub with no listener set yet (the window before Serve) must answer rather
// than panic: the dialog can be opened at any moment.
func TestAdminNetworkService_ToleratesNoListenerSet(t *testing.T) {
	svc, _, _ := newNetworkService(t, &config.Config{SoloMode: true, Listen: "127.0.0.1:4327"}, nil)

	resp, err := svc.GetListenStatus(context.Background(), connect.NewRequest(&leapmuxv1.GetListenStatusRequest{}))
	require.NoError(t, err)
	assert.Empty(t, resp.Msg.GetBound())
	assert.Equal(t, "127.0.0.1:4327", resp.Msg.GetDefaultAddress())
}

// A machine whose interfaces cannot be read is an internal failure, not an
// empty machine: reporting no interfaces would make the picker offer only the
// wildcard and look like a machine with no network.
func TestAdminNetworkService_ReportsAnInterfaceReadFailure(t *testing.T) {
	svc, _, _ := newNetworkService(t, &config.Config{SoloMode: true}, fakeListenReporter{})
	service.SetInterfaceSourceForTest(svc, func() ([]net.Interface, error) {
		return nil, errors.New("no such device")
	})

	_, err := svc.GetListenStatus(context.Background(), connect.NewRequest(&leapmuxv1.GetListenStatusRequest{}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err))
}

// systemInfoForSolo builds a solo AuthService over a stated listener set and
// returns GetSystemInfo's answer for a request from the given transport.
func systemInfoForSolo(t *testing.T, listen service.ListenReporter, ctx context.Context, withPassword bool) *leapmuxv1.GetSystemInfoResponse {
	t.Helper()
	st := hubtestutil.OpenTestStore(t)
	require.NoError(t, bootstrap.Run(context.Background(), st, true))
	if withPassword {
		setSoloPasswordForTest(t, st)
	}
	deps := servicetest.AuthServiceDeps(st, &config.Config{SoloMode: true, Listen: "127.0.0.1:4327"},
		servicetest.NewSettingsManager(t, st, nil), auth.NewCredentialLifecycleEffects(nil, nil, nil))
	deps.Listen = listen
	deps.SoloGate = auth.NewSoloGate(st)

	resp, err := service.NewAuthService(deps).GetSystemInfo(ctx, connect.NewRequest(&leapmuxv1.GetSystemInfoRequest{}))
	require.NoError(t, err)
	return resp.Msg
}

// auto_authenticated is the fact the app reads to decide whether to fall back
// to the login form. It is per CONNECTION, so the whole rule table has to
// reach it.
func TestGetSystemInfo_AutoAuthenticatedFollowsTheTransportAndThePassword(t *testing.T) {
	loopbackOnly := fakeListenReporter{primary: "127.0.0.1:4327"}

	assert.True(t, systemInfoForSolo(t, loopbackOnly, tcpCtx("127.0.0.1"), false).GetAutoAuthenticated(),
		"the bootstrap state: a browser reaches a solo hub over TCP and nothing else")
	assert.True(t, systemInfoForSolo(t, loopbackOnly, ipcCtxForTest(), false).GetAutoAuthenticated())
	assert.True(t, systemInfoForSolo(t, loopbackOnly, ipcCtxForTest(), true).GetAutoAuthenticated(),
		"the desktop app is never asked for a password, whatever the account holds")
	assert.False(t, systemInfoForSolo(t, loopbackOnly, tcpCtx("127.0.0.1"), true).GetAutoAuthenticated(),
		"once a password exists every TCP address asks for it, 127.0.0.1 included")
	assert.False(t, systemInfoForSolo(t, loopbackOnly, tcpCtx("192.168.1.24"), true).GetAutoAuthenticated())
}

// password_setup_required blocks the whole app, so its condition is EXPOSURE
// without a credential and nothing weaker.
func TestGetSystemInfo_PasswordSetupRequiredNeedsExposureAndNoPassword(t *testing.T) {
	loopbackOnly := fakeListenReporter{primary: "127.0.0.1:4327"}
	exposed := fakeListenReporter{primary: ":4327", nonLoopback: true}
	ctx := tcpCtx("127.0.0.1")

	assert.True(t, systemInfoForSolo(t, exposed, ctx, false).GetPasswordSetupRequired(),
		"reachable from another machine and nobody can sign in: the one useful thing left is to ask for a password")
	assert.False(t, systemInfoForSolo(t, exposed, ctx, true).GetPasswordSetupRequired(),
		"a password answers it")
	assert.False(t, systemInfoForSolo(t, loopbackOnly, ctx, false).GetPasswordSetupRequired(),
		"a loopback-only hub exposes nothing, so the demand would be friction with nothing behind it")
	assert.False(t, systemInfoForSolo(t, loopbackOnly, ctx, true).GetPasswordSetupRequired())
}

func TestGetSystemInfo_SoloPasswordSet(t *testing.T) {
	listen := fakeListenReporter{primary: "127.0.0.1:4327"}
	ctx := tcpCtx("127.0.0.1")

	assert.False(t, systemInfoForSolo(t, listen, ctx, false).GetSoloPasswordSet())
	assert.True(t, systemInfoForSolo(t, listen, ctx, true).GetSoloPasswordSet())
}

// Every passkey ceremony is refused in solo, so offering one would give the
// sign-in form a button that can only fail.
func TestGetSystemInfo_PasskeysAreNeverRunnableInSolo(t *testing.T) {
	info := systemInfoForSolo(t, fakeListenReporter{primary: "127.0.0.1:4327"}, tcpCtx("127.0.0.1"), true)
	assert.False(t, info.GetPasskeyEnabled())
}

// The three fields are solo-only facts: a multi-user hub authenticates every
// caller, and each account answers for its own password.
func TestGetSystemInfo_TheSoloFieldsAreFalseOnAMultiUserHub(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	hubtestutil.CreateTestAdmin(t, st)
	deps := servicetest.AuthServiceDeps(st, &config.Config{Listen: ":4327"},
		servicetest.NewSettingsManager(t, st, nil), auth.NewCredentialLifecycleEffects(nil, nil, nil))
	deps.Listen = fakeListenReporter{primary: ":4327", nonLoopback: true}

	resp, err := service.NewAuthService(deps).GetSystemInfo(tcpCtx("192.168.1.24"),
		connect.NewRequest(&leapmuxv1.GetSystemInfoRequest{}))
	require.NoError(t, err)
	assert.False(t, resp.Msg.GetAutoAuthenticated())
	assert.False(t, resp.Msg.GetPasswordSetupRequired(),
		"an exposed multi-user hub already asks every caller to sign in")
	assert.False(t, resp.Msg.GetSoloPasswordSet())
}
