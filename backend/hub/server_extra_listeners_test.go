package hub

import (
	"context"
	"encoding/json"
	"net"
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/settings"
	"github.com/leapmux/leapmux/locallisten/locallistentest"
)

// startTestServerIn is startTestServer over a STATED data directory and local
// IPC name, so two runs can share one database.
//
// It returns a STOP function beside the server, because a restart test has to
// end the first run before it starts the second -- both bind the same address.
// Stop is idempotent and also runs as a cleanup, so a test that fails before
// stopping leaks nothing.
func startTestServerIn(t *testing.T, cfg *config.Config, dataDir, localListen string) (*Server, func()) {
	t.Helper()
	cfg.DataDir = dataDir
	cfg.LocalListen = localListen
	cfg.Storage = config.StorageConfig{Type: config.StorageTypeSQLite}

	srv, err := NewServer(cfg)
	require.NoError(t, err)

	serveCtx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- srv.Serve(serveCtx) }()

	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			<-served
		})
	}
	t.Cleanup(stop)
	return srv, stop
}

// writeExtraListeners stores an address list through the settings manager, the
// same path AdminSettingsService.UpdateSetting takes.
//
// The document is built by hand rather than by marshalling ExtraListenValue,
// and that is deliberate: `addresses` carries `omitempty`, so an EMPTY list
// marshals to `{}` -- a partial that specifies no field, which the merge
// refuses. The panel sends `{"addresses":[]}`, which is what clears the list,
// so the test has to send what the panel sends.
func writeExtraListeners(t *testing.T, srv *Server, addresses ...string) {
	t.Helper()
	list, err := json.Marshal(addresses)
	if addresses == nil {
		list = []byte("[]")
	}
	require.NoError(t, err)
	doc := append(append([]byte(`{"addresses":`), list...), '}')
	require.NoError(t, srv.SettingsManager().Update(
		context.Background(), settings.KeyExtraListenAddresses, json.RawMessage(doc)))
}

// servingAddresses is what the hub answers on right now.
func servingAddresses(srv *Server) []string {
	out := []string{}
	for _, b := range srv.ListenerStatus() {
		if b.Err == "" {
			out = append(out, b.Addr.String())
		}
	}
	return out
}

// A settings write must reach the sockets, with no restart. That is the whole
// point of the key: a restart-class one would store the intent and serve
// nothing.
func TestServer_ASettingsWriteBindsAndUnbindsWhileServing(t *testing.T) {
	ports := freePorts(t, 2)
	basePort, extraPort := ports[0], ports[1]
	base := "127.0.0.1:" + strconv.Itoa(basePort)
	extra := "127.0.0.1:" + strconv.Itoa(extraPort)

	srv := startTestServer(t, &config.Config{Listen: base, SoloMode: true})
	requireAnswers(t, base)

	writeExtraListeners(t, srv, extra)
	requireAnswers(t, extra)
	requireAnswers(t, base)
	assert.ElementsMatch(t, []string{base, extra}, servingAddresses(srv))

	// And removing it closes the socket. An empty list is the ordinary way to
	// stop publishing an address, so it must not read as "leave things alone".
	writeExtraListeners(t, srv)
	requireStopsAnswering(t, extra)
	// The -listen address is never dropped: Apply merges it back in every time,
	// so an empty settings row cannot take the hub off the air.
	requireAnswers(t, base)
	assert.Equal(t, []string{base}, servingAddresses(srv))
}

// The requirement's own example, through the real server: an operator asks for
// every interface on the port -listen already holds, and one socket serves
// both.
func TestServer_AWildcardMergesTheListenAddress(t *testing.T) {
	port := freePorts(t, 1)[0]
	base := "127.0.0.1:" + strconv.Itoa(port)

	srv := startTestServer(t, &config.Config{Listen: base, SoloMode: true})
	requireAnswers(t, base)

	writeExtraListeners(t, srv, "*:"+strconv.Itoa(port))

	assert.Equal(t, []string{"*:" + strconv.Itoa(port)}, servingAddresses(srv))
	// Still answering where -listen pointed, from the wider socket.
	requireAnswers(t, base)

	status := srv.ListenerStatus()
	require.Len(t, status, 1)
	assert.Equal(t, SourceMerged, status[0].Source,
		"the panel must be able to say the -listen address is served by this one, not that it is gone")

	// The hub reports the DIAL form of the merged address, because
	// settings.browserHostForListen reads a wildcard as an empty host with a
	// port; the canonical "*:4327" would produce a link to a machine named "*".
	assert.Equal(t, ":"+strconv.Itoa(port), srv.PrimaryListenAddr())
	assert.True(t, srv.HasNonLoopbackAddress(),
		"a wildcard answers on every interface, so the hub is reachable from another machine")
}

// A stored address the hub cannot bind must not stop it starting: an address
// stops existing whenever a VPN drops or a lease moves, and the operator needs
// the hub running to correct the setting.
func TestServer_AStoredAddressThatCannotBindDoesNotStopTheHub(t *testing.T) {
	ports := freePorts(t, 2)
	basePort, occupiedPort := ports[0], ports[1]
	base := "127.0.0.1:" + strconv.Itoa(basePort)
	occupied := "127.0.0.1:" + strconv.Itoa(occupiedPort)

	srv := startTestServer(t, &config.Config{Listen: base, SoloMode: true})
	requireAnswers(t, base)

	blocker, err := net.Listen("tcp", occupied)
	require.NoError(t, err)
	t.Cleanup(func() { _ = blocker.Close() })

	writeExtraListeners(t, srv, occupied)

	requireAnswers(t, base)
	// Reported against its own address, with the operating system's reason, so
	// the panel can print it rather than showing the address as simply absent.
	var failed *BoundAddress
	status := srv.ListenerStatus()
	for i := range status {
		if status[i].Err != "" {
			failed = &status[i]
		}
	}
	require.NotNil(t, failed, "an address that could not bind must be reported")
	assert.Equal(t, occupied, failed.Addr.String())
	assert.Contains(t, failed.Err, "address already in use")
}

// An unrelated settings write must not close and rebind every socket: the
// subscriber fires on EVERY snapshot change.
func TestServer_AnUnrelatedSettingsWriteLeavesTheListenersAlone(t *testing.T) {
	ports := freePorts(t, 2)
	basePort, extraPort := ports[0], ports[1]
	base := "127.0.0.1:" + strconv.Itoa(basePort)
	extra := "127.0.0.1:" + strconv.Itoa(extraPort)

	srv := startTestServer(t, &config.Config{Listen: base, SoloMode: true})
	writeExtraListeners(t, srv, extra)
	requireAnswers(t, extra)

	// A connection the rebind would drop, so this observes the sockets rather
	// than the report derived from them.
	conn, err := net.Dial("tcp", extra)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	require.NoError(t, srv.SettingsManager().Update(context.Background(),
		settings.KeyPublicURL, json.RawMessage(`"https://hub.example.com"`)))

	requireAnswers(t, extra)
	requireAnswers(t, base)
	assert.ElementsMatch(t, []string{base, extra}, servingAddresses(srv))
}

// The stored addresses are bound again on the NEXT start. "Persisted across
// restarts" is the requirement's own headline, and the live-apply tests above
// cannot see it: they all run inside one process.
func TestServer_StoredAddressesBindAgainOnTheNextStart(t *testing.T) {
	ports := freePorts(t, 2)
	basePort, extraPort := ports[0], ports[1]
	base := "127.0.0.1:" + strconv.Itoa(basePort)
	extra := "127.0.0.1:" + strconv.Itoa(extraPort)

	// One data directory across both runs: the setting lives in the hub's
	// database, so a second run over a fresh directory would prove nothing.
	dataDir := t.TempDir()
	localListen := locallistentest.UniqueListenURL(t, "lmx-hub-restart")

	first, stopFirst := startTestServerIn(t, &config.Config{Listen: base, SoloMode: true}, dataDir, localListen)
	writeExtraListeners(t, first, extra)
	requireAnswers(t, extra)
	stopFirst()
	requireStopsAnswering(t, extra)

	second, _ := startTestServerIn(t, &config.Config{Listen: base, SoloMode: true}, dataDir, localListen)
	requireAnswers(t, extra)
	requireAnswers(t, base)
	assert.ElementsMatch(t, []string{base, extra}, servingAddresses(second))
}
