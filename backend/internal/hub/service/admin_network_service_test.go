package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"path/filepath"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/bootstrap"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/listenset"
	"github.com/leapmux/leapmux/internal/hub/peer"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/servicetest"
	"github.com/leapmux/leapmux/internal/hub/settings"
	"github.com/leapmux/leapmux/internal/hub/store"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/hub/usernames"
)

// fakeListenReporter states a hub's listeners without binding a socket.
type fakeListenReporter struct {
	bound   []listenset.Bound
	primary string
	// baseTCP and local are the two INDEPENDENT endpoint lists the status RPC
	// reports: the TCP merge input in bind order, and the local IPC URLs a
	// co-located CLI discovers. Neither is derivable from the other.
	baseTCP []string
	local   []string
}

func tcpCtx(ip string) context.Context {
	return peer.WithTransportAddr(context.Background(), &net.TCPAddr{IP: net.ParseIP(ip), Port: 51234})
}

func ipcCtxForTest() context.Context {
	return peer.WithTransportAddr(peer.WithLocalIPC(context.Background()),
		&net.UnixAddr{Name: "/tmp/hub.sock", Net: "unix"})
}

func (f fakeListenReporter) Bound() []listenset.Bound  { return f.bound }
func (f fakeListenReporter) PrimaryListenAddr() string { return f.primary }
func (f fakeListenReporter) BaseTCPAddresses() []string {
	return append([]string(nil), f.baseTCP...)
}
func (f fakeListenReporter) LocalIPCAddresses() []string {
	return append([]string(nil), f.local...)
}

func newNetworkService(t *testing.T, cfg *config.Config, listen service.ListenReporter) (*service.AdminNetworkService, store.Store, *settings.Manager) {
	t.Helper()
	st := hubtestutil.OpenTestStore(t)
	require.NoError(t, bootstrap.Run(context.Background(), st, cfg.SoloMode))
	set := servicetest.NewSettingsManager(t, st, nil)
	return service.NewAdminNetworkService(service.AdminNetworkServiceDeps{
		Config: cfg, Settings: set, Listen: listen,
	}), st, set
}

func TestAdminNetworkService_ReportsTheLiveListeners(t *testing.T) {
	listen := fakeListenReporter{
		baseTCP: []string{"127.0.0.1:4327"},
		local:   []string{"unix:/tmp/hub.sock"},
		bound: []listenset.Bound{
			{Addr: listenset.MustParse("*:4327"), Source: listenset.SourceMerged},
			{Addr: listenset.MustParse("192.168.1.24:8080"), Source: listenset.SourceExtra, Err: "address already in use"},
		},
	}
	svc, _, set := newNetworkService(t, &config.Config{SoloMode: true, Listen: []string{"127.0.0.1:4327"}}, listen)

	require.NoError(t, set.Update(context.Background(), settings.KeyExtraListenAddresses,
		json.RawMessage(`{"addresses":["*:4327","192.168.1.24:8080"]}`)))

	resp, err := svc.GetListenStatus(context.Background(), &leapmuxv1.GetListenStatusRequest{})
	require.NoError(t, err)

	assert.Equal(t, []string{"127.0.0.1:4327"}, resp.GetBaseTcpAddresses(),
		"--listen is reported read-only: it is a command-line option, never a setting")
	assert.Equal(t, []string{"unix:/tmp/hub.sock"}, resp.GetLocalIpcAddresses(),
		"the local endpoints are startup facts, reported beside the TCP bases")
	assert.Equal(t, []string{"*:4327", "192.168.1.24:8080"}, resp.GetConfigured())

	require.Len(t, resp.GetBound(), 2)
	assert.Equal(t, "*:4327", resp.GetBound()[0].GetAddress())
	// "merged" is what lets the panel say the --listen address is served by
	// this one rather than gone.
	assert.Equal(t, "merged", resp.GetBound()[0].GetSource())
	assert.Empty(t, resp.GetBound()[0].GetError())

	// A configured address the hub could not bind is REPORTED, not dropped:
	// the panel prints the operating system's reason beside it.
	assert.Equal(t, "192.168.1.24:8080", resp.GetBound()[1].GetAddress())
	assert.Equal(t, "address already in use", resp.GetBound()[1].GetError())
}

// The two endpoint lists are INDEPENDENT answers, one per call: the TCP merge
// input carries every -listen base in bind order, and the local list carries
// the startup endpoints. Neither folds into the other, and a caller cannot
// rewrite the reporter's record by mutating what it was handed.
func TestAdminNetworkService_ReportsIndependentTCPAndLocalLists(t *testing.T) {
	listen := fakeListenReporter{
		baseTCP: []string{"127.0.0.1:4327", "127.0.0.1:9000"},
		local:   []string{"unix:/tmp/hub.sock", "npipe:leapmux-hub"},
	}
	svc, _, _ := newNetworkService(t, &config.Config{SoloMode: true}, listen)

	resp, err := svc.GetListenStatus(context.Background(), &leapmuxv1.GetListenStatusRequest{})
	require.NoError(t, err)
	assert.Equal(t, []string{"127.0.0.1:4327", "127.0.0.1:9000"}, resp.GetBaseTcpAddresses(),
		"the full merge input, in bind order")
	assert.Equal(t, []string{"unix:/tmp/hub.sock", "npipe:leapmux-hub"}, resp.GetLocalIpcAddresses())

	resp.BaseTcpAddresses[0] = "rewritten"
	resp.LocalIpcAddresses[0] = "rewritten"
	second, err := svc.GetListenStatus(context.Background(), &leapmuxv1.GetListenStatusRequest{})
	require.NoError(t, err)
	assert.Equal(t, []string{"127.0.0.1:4327", "127.0.0.1:9000"}, second.GetBaseTcpAddresses())
	assert.Equal(t, []string{"unix:/tmp/hub.sock", "npipe:leapmux-hub"}, second.GetLocalIpcAddresses())
}

// A hub that binds no TCP address reports an empty TCP list and keeps its
// local endpoints: the desktop's ordinary state, not a failure.
func TestAdminNetworkService_ALocalOnlyHubReportsNoTCPBases(t *testing.T) {
	listen := fakeListenReporter{local: []string{"npipe:leapmux-hub"}}
	svc, _, _ := newNetworkService(t, &config.Config{SoloMode: true, Listen: []string{"npipe:leapmux-hub"}}, listen)

	resp, err := svc.GetListenStatus(context.Background(), &leapmuxv1.GetListenStatusRequest{})
	require.NoError(t, err)
	assert.Empty(t, resp.GetBaseTcpAddresses())
	assert.Empty(t, resp.GetBound())
	assert.Equal(t, []string{"npipe:leapmux-hub"}, resp.GetLocalIpcAddresses())
}

// The stored list is echoed CANONICALLY, so the picker matches an entry
// against the address it offers whichever spelling the row holds.
func TestAdminNetworkService_CanonicalisesTheConfiguredAddresses(t *testing.T) {
	svc, _, set := newNetworkService(t, &config.Config{SoloMode: true}, fakeListenReporter{})
	require.NoError(t, set.Update(context.Background(), settings.KeyExtraListenAddresses,
		json.RawMessage(`{"addresses":[":4327","[::0]:9000"]}`)))

	resp, err := svc.GetListenStatus(context.Background(), &leapmuxv1.GetListenStatusRequest{})
	require.NoError(t, err)
	assert.Equal(t, []string{"*:4327", "[::]:9000"}, resp.GetConfigured())
}

// The picker needs the machine's addresses, and it needs them stable: a list
// that reorders between two openings moves the entry under the pointer.
func TestAdminNetworkService_ListsTheMachineInterfaces(t *testing.T) {
	svc, _, _ := newNetworkService(t, &config.Config{SoloMode: true}, fakeListenReporter{})

	resp, err := svc.GetListenStatus(context.Background(), &leapmuxv1.GetListenStatusRequest{})
	require.NoError(t, err)
	require.NotEmpty(t, resp.GetInterfaces(), "every machine has at least a loopback interface")

	var names []string
	sawLoopback := false
	for _, iface := range resp.GetInterfaces() {
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

	second, err := svc.GetListenStatus(context.Background(), &leapmuxv1.GetListenStatusRequest{})
	require.NoError(t, err)
	assert.Equal(t, len(resp.GetInterfaces()), len(second.GetInterfaces()))
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
// than panic: the dialog can be opened at any moment. The base list then comes
// from the configured bind set, with no local endpoints to report.
func TestAdminNetworkService_ToleratesNoListenerSet(t *testing.T) {
	svc, _, _ := newNetworkService(t, &config.Config{SoloMode: true, Listen: []string{"127.0.0.1:4327"}}, nil)

	resp, err := svc.GetListenStatus(context.Background(), &leapmuxv1.GetListenStatusRequest{})
	require.NoError(t, err)
	assert.Empty(t, resp.GetBound())
	assert.Equal(t, []string{"127.0.0.1:4327"}, resp.GetBaseTcpAddresses())
	assert.Empty(t, resp.GetLocalIpcAddresses(),
		"a reporter with no live sockets knows no endpoint a client can dial")
}

// The fallback without live sockets reports the configured TCP bind set as
// the merge input: the explicit list in bind order, the launcher's default
// when the set lists none, and nothing at all for a local-only set.
func TestAdminNetworkService_TheFallbackSplitsTheConfiguredBindSet(t *testing.T) {
	t.Setenv("LEAPMUX_HUB_LISTEN", "")
	launcherConfig, _, err := config.LoadWithOptions(nil, config.LoadOptions{
		DefaultConfigFile: filepath.Join(t.TempDir(), "hub.yaml"),
		DefaultListen:     "127.0.0.1:44321",
		SoloMode:          true,
	})
	require.NoError(t, err)
	require.Empty(t, launcherConfig.Listen, "the launcher default applies to an empty configured bind set")

	for _, tc := range []struct {
		name string
		cfg  *config.Config
		want []string
	}{
		{
			name: "an explicit list keeps its order",
			cfg:  &config.Config{SoloMode: true, Listen: []string{":8080", "unix:/x", ":9090"}},
			want: []string{":8080", ":9090"},
		},
		{
			name: "an empty list takes the built-in TCP default",
			cfg:  &config.Config{SoloMode: true},
			want: []string{":4327"},
		},
		{
			name: "an empty list takes the launcher's TCP default",
			cfg:  launcherConfig,
			want: []string{"127.0.0.1:44321"},
		},
		{
			name: "a local-only list reports no TCP address",
			cfg:  &config.Config{SoloMode: true, Listen: []string{"unix:/x"}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, _, _ := newNetworkService(t, tc.cfg, nil)

			resp, err := svc.GetListenStatus(context.Background(), &leapmuxv1.GetListenStatusRequest{})
			require.NoError(t, err)
			assert.Equal(t, tc.want, resp.GetBaseTcpAddresses())
			assert.Empty(t, resp.GetLocalIpcAddresses())
		})
	}
}

// A machine whose interfaces cannot be read is an internal failure, not an
// empty machine: reporting no interfaces would make the picker offer only the
// wildcard and look like a machine with no network.
func TestAdminNetworkService_ReportsAnInterfaceReadFailure(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	require.NoError(t, bootstrap.Run(context.Background(), st, true))
	svc := service.NewAdminNetworkService(service.AdminNetworkServiceDeps{
		Config: &config.Config{SoloMode: true}, Settings: servicetest.NewSettingsManager(t, st, nil),
		Listen:     fakeListenReporter{},
		Interfaces: func() ([]net.Interface, error) { return nil, errors.New("no such device") },
	})

	_, err := svc.GetListenStatus(context.Background(), &leapmuxv1.GetListenStatusRequest{})
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
	deps := servicetest.AuthServiceDeps(st, &config.Config{SoloMode: true, Listen: []string{"127.0.0.1:4327"}},
		servicetest.NewSettingsManager(t, st, nil), auth.NewCredentialLifecycleEffects(nil, nil, nil))
	deps.Listen = listen
	deps.SoloGate = auth.NewSoloGate(true, st)

	// A bare method call carries no CallInfo, and GetSystemInfo reads the
	// request's Origin from one; callUnderServerInfo supplies the handle a
	// transport would, over the peer-carrying context under test.
	var resp *leapmuxv1.GetSystemInfoResponse
	_, err := callUnderServerInfo(ctx, func(callCtx context.Context) error {
		var callErr error
		resp, callErr = service.NewSystemService(newInitializedAuthService(t, deps), deps.Settings).GetSystemInfo(callCtx, &leapmuxv1.GetSystemInfoRequest{})
		return callErr
	})
	require.NoError(t, err)
	return resp
}

// SoloAccess gives each connection one exclusive access state.
func TestGetSystemInfo_SoloAccessFollowsTheTransportAndThePassword(t *testing.T) {
	loopbackOnly := fakeListenReporter{primary: "127.0.0.1:4327"}

	assert.Equal(t, leapmuxv1.SoloAccess_SOLO_ACCESS_PASSWORD_SETUP,
		systemInfoForSolo(t, loopbackOnly, tcpCtx("127.0.0.1"), false).GetSoloAccess())
	assert.Equal(t, leapmuxv1.SoloAccess_SOLO_ACCESS_CREDENTIAL_FREE,
		systemInfoForSolo(t, loopbackOnly, ipcCtxForTest(), false).GetSoloAccess())
	assert.Equal(t, leapmuxv1.SoloAccess_SOLO_ACCESS_CREDENTIAL_FREE,
		systemInfoForSolo(t, loopbackOnly, ipcCtxForTest(), true).GetSoloAccess())
	assert.Equal(t, leapmuxv1.SoloAccess_SOLO_ACCESS_SIGN_IN_REQUIRED,
		systemInfoForSolo(t, loopbackOnly, tcpCtx("127.0.0.1"), true).GetSoloAccess())
	assert.Equal(t, leapmuxv1.SoloAccess_SOLO_ACCESS_SIGN_IN_REQUIRED,
		systemInfoForSolo(t, loopbackOnly, tcpCtx("192.168.1.24"), true).GetSoloAccess())
}

// TCP setup is restricted even on loopback. The listener set does not change
// the authentication rule.
func TestGetSystemInfo_PasswordSetupDoesNotDependOnListenerExposure(t *testing.T) {
	loopbackOnly := fakeListenReporter{primary: "127.0.0.1:4327"}
	exposed := fakeListenReporter{primary: ":4327", bound: []listenset.Bound{
		{Addr: listenset.MustParse("*:4327"), Source: listenset.SourceListen},
	}}
	ctx := tcpCtx("127.0.0.1")

	assert.Equal(t, leapmuxv1.SoloAccess_SOLO_ACCESS_PASSWORD_SETUP,
		systemInfoForSolo(t, exposed, ctx, false).GetSoloAccess())
	assert.Equal(t, leapmuxv1.SoloAccess_SOLO_ACCESS_SIGN_IN_REQUIRED,
		systemInfoForSolo(t, exposed, ctx, true).GetSoloAccess())
	assert.Equal(t, leapmuxv1.SoloAccess_SOLO_ACCESS_PASSWORD_SETUP,
		systemInfoForSolo(t, loopbackOnly, ctx, false).GetSoloAccess())
	assert.Equal(t, leapmuxv1.SoloAccess_SOLO_ACCESS_SIGN_IN_REQUIRED,
		systemInfoForSolo(t, loopbackOnly, ctx, true).GetSoloAccess())
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
	deps := servicetest.AuthServiceDeps(st, &config.Config{Listen: []string{":4327"}},
		servicetest.NewSettingsManager(t, st, nil), auth.NewCredentialLifecycleEffects(nil, nil, nil))
	deps.Listen = fakeListenReporter{primary: ":4327", bound: []listenset.Bound{
		{Addr: listenset.MustParse("*:4327"), Source: listenset.SourceListen},
	}}

	var resp *leapmuxv1.GetSystemInfoResponse
	_, err := callUnderServerInfo(tcpCtx("192.168.1.24"), func(callCtx context.Context) error {
		var callErr error
		resp, callErr = service.NewSystemService(newInitializedAuthService(t, deps), deps.Settings).GetSystemInfo(callCtx, &leapmuxv1.GetSystemInfoRequest{})
		return callErr
	})
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.SoloAccess_SOLO_ACCESS_UNSPECIFIED, resp.GetSoloAccess())
	assert.False(t, resp.GetSoloPasswordSet())
}

// And it does not ASK the gate at all.
//
// NewInterceptor builds a gate on every hub, and `solo` is reserved in every
// creation path -- so an unguarded read looked up a row that can never exist
// and could never latch: one indexed miss on every page load, for ever, on a
// deployment with no solo rule to decide.
func TestGetSystemInfo_DoesNotReadTheSoloAccountOnAMultiUserHub(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	hubtestutil.CreateTestAdmin(t, st)
	counted := &countingUserStore{Store: st}
	deps := servicetest.AuthServiceDeps(counted, &config.Config{Listen: []string{":4327"}},
		servicetest.NewSettingsManager(t, st, nil), auth.NewCredentialLifecycleEffects(nil, nil, nil))
	deps.Listen = fakeListenReporter{primary: ":4327", bound: []listenset.Bound{
		{Addr: listenset.MustParse("*:4327"), Source: listenset.SourceListen},
	}}
	deps.SoloGate = auth.NewSoloGate(false, counted)

	_, err := callUnderServerInfo(tcpCtx("192.168.1.24"), func(callCtx context.Context) error {
		_, callErr := service.NewSystemService(newInitializedAuthService(t, deps), deps.Settings).GetSystemInfo(callCtx, &leapmuxv1.GetSystemInfoRequest{})
		return callErr
	})
	require.NoError(t, err)
	assert.Zero(t, counted.soloLookups.Load(), "a multi-user hub has no solo account to look up")
}

// countingUserStore counts the lookups of the solo account, so a test can
// assert that a code path never asks for a row that cannot exist.
type countingUserStore struct {
	store.Store
	soloLookups atomic.Int64
}

func (s *countingUserStore) Users() store.UserStore {
	return countingUsers{UserStore: s.Store.Users(), parent: s}
}

type countingUsers struct {
	store.UserStore
	parent *countingUserStore
}

func (u countingUsers) GetByUsername(ctx context.Context, username string) (*store.User, error) {
	if username == usernames.Solo {
		u.parent.soloLookups.Add(1)
	}
	return u.UserStore.GetByUsername(ctx, username)
}
