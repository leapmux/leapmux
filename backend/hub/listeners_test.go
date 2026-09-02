package hub

import (
	"net"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/listenset"
)

// freePorts returns n DISTINCT ports nothing holds.
//
// Every listener is held open until all n are chosen, then closed. Taking them
// one at a time returns the same port twice on most systems -- the kernel
// hands back the port it just freed -- and two test addresses that are secretly
// one address make a merge test pass for the wrong reason.
//
// Every address in this file is 127.0.0.1 on a port of its own, never
// 127.0.0.2. Linux assigns the whole 127.0.0.0/8 to loopback and macOS assigns
// only 127.0.0.1, so a second loopback literal fails there with "can't assign
// requested address" -- a failure about the machine rather than about the code
// under test.
func freePorts(t *testing.T, n int) []int {
	t.Helper()
	held := make([]net.Listener, 0, n)
	ports := make([]int, 0, n)
	for range n {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		held = append(held, ln)
		ports = append(ports, ln.Addr().(*net.TCPAddr).Port)
	}
	for _, ln := range held {
		require.NoError(t, ln.Close())
	}
	return ports
}

// freePort is freePorts for the single-address tests.
func freePort(t *testing.T) int {
	t.Helper()
	return freePorts(t, 1)[0]
}

// newTestSet builds a listener set over a server that answers "ok", with the
// base already bound. It returns the set and the channel a listener that dies
// unasked reports on.
func newTestSet(t *testing.T, baseAddr string) (*listenerSet, chan error) {
	t.Helper()
	server := &http.Server{
		ReadHeaderTimeout: 5 * time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("ok"))
		}),
	}

	var baseLn net.Listener
	var base *listenset.Addr
	if baseAddr != "" {
		parsed := listenset.MustParse(baseAddr)
		base = &parsed
		var err error
		baseLn, err = net.Listen("tcp", parsed.DialAddr())
		require.NoError(t, err)
	}

	serveErr := make(chan error, 1)
	set := newListenerSet(server, baseLn, base, serveErr)
	set.Serve()
	t.Cleanup(func() {
		_ = set.Close()
		_ = server.Close()
	})
	return set, serveErr
}

// answers reports whether the address ACCEPTS a new connection and serves it.
//
// A fresh transport with keep-alives off, never http.DefaultTransport. Closing
// a listener stops it accepting and leaves every connection it already
// accepted open -- which is exactly right, and which makes a pooled connection
// answer long after the address stopped serving. The shared default pool is
// per-process, so one earlier probe in one test would hold that connection for
// every later one.
func answers(t *testing.T, addr string) bool {
	t.Helper()
	client := &http.Client{
		Timeout:   2 * time.Second,
		Transport: &http.Transport{DisableKeepAlives: true},
	}
	defer client.CloseIdleConnections()
	resp, err := client.Get("http://" + addr + "/")
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return true
}

// requireAnswers polls until the address serves, so a test never depends on
// how quickly a fresh goroutine reaches Accept.
func requireAnswers(t *testing.T, addr string) {
	t.Helper()
	require.Eventually(t, func() bool { return answers(t, addr) }, 3*time.Second, 10*time.Millisecond,
		"%s must serve", addr)
}

func requireStopsAnswering(t *testing.T, addr string) {
	t.Helper()
	require.Eventually(t, func() bool { return !answers(t, addr) }, 3*time.Second, 10*time.Millisecond,
		"%s must stop serving", addr)
}

// boundStrings renders the serving addresses for an assertion.
func boundStrings(set *listenerSet) []string {
	out := []string{}
	for _, b := range set.Bound() {
		if b.Err == "" {
			out = append(out, b.Addr.String())
		}
	}
	return out
}

func TestListenerSet_AppliesAnExtraAddressWhileServing(t *testing.T) {
	ports := freePorts(t, 2)
	basePort, extraPort := ports[0], ports[1]
	set, _ := newTestSet(t, "127.0.0.1:"+strconv.Itoa(basePort))
	requireAnswers(t, "127.0.0.1:"+strconv.Itoa(basePort))

	require.NoError(t, set.Apply([]listenset.Addr{
		listenset.MustParse("127.0.0.1:" + strconv.Itoa(extraPort)),
	}))

	requireAnswers(t, "127.0.0.1:"+strconv.Itoa(extraPort))
	requireAnswers(t, "127.0.0.1:"+strconv.Itoa(basePort))
	assert.ElementsMatch(t, []string{
		"127.0.0.1:" + strconv.Itoa(basePort),
		"127.0.0.1:" + strconv.Itoa(extraPort),
	}, boundStrings(set))
}

func TestListenerSet_RemovingAnExtraStopsItServing(t *testing.T) {
	ports := freePorts(t, 2)
	basePort, extraPort := ports[0], ports[1]
	set, _ := newTestSet(t, "127.0.0.1:"+strconv.Itoa(basePort))

	extra := listenset.MustParse("127.0.0.1:" + strconv.Itoa(extraPort))
	require.NoError(t, set.Apply([]listenset.Addr{extra}))
	requireAnswers(t, extra.String())

	require.NoError(t, set.Apply(nil))
	requireStopsAnswering(t, extra.String())
	// The -listen address is never dropped: Apply merges it back in every
	// time, so an empty settings row cannot take the hub off the air.
	requireAnswers(t, "127.0.0.1:"+strconv.Itoa(basePort))
	assert.Equal(t, []string{"127.0.0.1:" + strconv.Itoa(basePort)}, boundStrings(set))
}

// The requirement's own example: an operator asks for every interface on the
// port -listen already holds, so the specific socket must be released before
// the wildcard can take it.
func TestListenerSet_AWildcardMergesTheBaseAddress(t *testing.T) {
	port := freePort(t)
	set, _ := newTestSet(t, "127.0.0.1:"+strconv.Itoa(port))
	requireAnswers(t, "127.0.0.1:"+strconv.Itoa(port))

	require.NoError(t, set.Apply([]listenset.Addr{listenset.MustParse("*:" + strconv.Itoa(port))}))

	// ONE socket now, and it still answers on the address -listen named.
	assert.Equal(t, []string{"*:" + strconv.Itoa(port)}, boundStrings(set))
	requireAnswers(t, "127.0.0.1:"+strconv.Itoa(port))

	bound := set.Bound()
	require.Len(t, bound, 1)
	assert.Equal(t, SourceMerged, bound[0].Source,
		"the panel must be able to say the -listen address is served by this one, not that it is gone")
}

// A failure must leave the hub exactly as it was. The base is what this
// protects: a merge closes it first, so a rollback that forgot to rebind would
// take the hub off the address its operator named on the command line.
func TestListenerSet_AFailedApplyRollsBackAndKeepsTheBase(t *testing.T) {
	ports := freePorts(t, 2)
	basePort, occupiedPort := ports[0], ports[1]
	blocker, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(occupiedPort))
	require.NoError(t, err)
	t.Cleanup(func() { _ = blocker.Close() })

	set, serveErr := newTestSet(t, "127.0.0.1:"+strconv.Itoa(basePort))
	requireAnswers(t, "127.0.0.1:"+strconv.Itoa(basePort))

	// The wildcard sorts first (same port as the base, widest kind), so the
	// base is CLOSED and replaced before the second address fails. That is the
	// ordering that makes the rollback matter.
	err = set.Apply([]listenset.Addr{
		listenset.MustParse("*:" + strconv.Itoa(basePort)),
		listenset.MustParse("127.0.0.1:" + strconv.Itoa(occupiedPort)),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), strconv.Itoa(occupiedPort))

	assert.Equal(t, []string{"127.0.0.1:" + strconv.Itoa(basePort)}, boundStrings(set),
		"the set must hold exactly what it held before the failed call")
	requireAnswers(t, "127.0.0.1:"+strconv.Itoa(basePort))

	// The deliberate closes on the rollback path must not read as a hub
	// failure, or every refused reconfiguration would stop the hub.
	select {
	case got := <-serveErr:
		t.Fatalf("a rolled-back apply reported a listener failure: %v", got)
	case <-time.After(200 * time.Millisecond):
	}
}

// ApplyBestEffort is what startup and the settings subscriber use: the row is
// already stored, so one unreachable address must not withhold the others.
func TestListenerSet_ApplyBestEffortKeepsTheUsableAddresses(t *testing.T) {
	ports := freePorts(t, 3)
	basePort, goodPort, occupiedPort := ports[0], ports[1], ports[2]
	blocker, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(occupiedPort))
	require.NoError(t, err)
	t.Cleanup(func() { _ = blocker.Close() })

	set, _ := newTestSet(t, "127.0.0.1:"+strconv.Itoa(basePort))

	set.ApplyBestEffort([]listenset.Addr{
		listenset.MustParse("127.0.0.1:" + strconv.Itoa(occupiedPort)),
		listenset.MustParse("127.0.0.1:" + strconv.Itoa(goodPort)),
	})

	requireAnswers(t, "127.0.0.1:"+strconv.Itoa(goodPort))
	requireAnswers(t, "127.0.0.1:"+strconv.Itoa(basePort))

	// The failure is reported against its own address rather than dropped, so
	// the panel can print the operating system's reason beside it.
	var failed *BoundAddress
	for i, b := range set.Bound() {
		if b.Err != "" {
			failed = &set.Bound()[i]
		}
	}
	require.NotNil(t, failed, "an address that could not bind must be reported, not silently absent")
	assert.Equal(t, "127.0.0.1:"+strconv.Itoa(occupiedPort), failed.Addr.String())
	assert.Contains(t, failed.Err, "address already in use")
}

// The settings subscriber fires on EVERY snapshot change, so an unrelated
// write must not close and rebind every socket the hub holds.
func TestListenerSet_ApplyIsANoOpWhenNothingChanged(t *testing.T) {
	ports := freePorts(t, 2)
	basePort, extraPort := ports[0], ports[1]
	set, serveErr := newTestSet(t, "127.0.0.1:"+strconv.Itoa(basePort))

	extras := []listenset.Addr{listenset.MustParse("127.0.0.1:" + strconv.Itoa(extraPort))}
	require.NoError(t, set.Apply(extras))
	requireAnswers(t, extras[0].String())

	// The same connection must survive the second apply: a rebind would drop
	// it, and a keep-alive client would see the socket close for no reason.
	conn, err := net.Dial("tcp", extras[0].String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	for range 3 {
		require.NoError(t, set.Apply(extras))
	}
	requireAnswers(t, extras[0].String())
	assert.ElementsMatch(t, []string{
		"127.0.0.1:" + strconv.Itoa(basePort),
		extras[0].String(),
	}, boundStrings(set))

	select {
	case got := <-serveErr:
		t.Fatalf("a no-op apply reported a listener failure: %v", got)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestListenerSet_CloseReleasesEverySocket(t *testing.T) {
	ports := freePorts(t, 2)
	basePort, extraPort := ports[0], ports[1]
	set, serveErr := newTestSet(t, "127.0.0.1:"+strconv.Itoa(basePort))
	require.NoError(t, set.Apply([]listenset.Addr{
		listenset.MustParse("127.0.0.1:" + strconv.Itoa(extraPort)),
	}))
	requireAnswers(t, "127.0.0.1:"+strconv.Itoa(extraPort))

	require.NoError(t, set.Close())

	// Both ports are free again, which is what a restart needs.
	for _, addr := range []string{
		"127.0.0.1:" + strconv.Itoa(basePort),
		"127.0.0.1:" + strconv.Itoa(extraPort),
	} {
		ln, err := net.Listen("tcp", addr)
		require.NoErrorf(t, err, "%s must be free after Close", addr)
		require.NoError(t, ln.Close())
	}

	select {
	case got := <-serveErr:
		t.Fatalf("Close reported a listener failure: %v", got)
	case <-time.After(200 * time.Millisecond):
	}

	// Idempotent, and a closed set refuses further reconfiguration rather
	// than resurrecting a socket on a hub that is exiting.
	require.NoError(t, set.Close())
	assert.Error(t, set.Apply(nil))
}

// A listener that dies WITHOUT being asked to is a hub failure, and Serve
// selects on this channel to tear the hub down.
func TestListenerSet_ReportsAnUnaskedListenerFailure(t *testing.T) {
	basePort := freePort(t)
	set, serveErr := newTestSet(t, "127.0.0.1:"+strconv.Itoa(basePort))
	requireAnswers(t, "127.0.0.1:"+strconv.Itoa(basePort))

	// Close the socket behind the set's back, so `closing` is not set and the
	// return reads as a fault rather than as a reconfiguration.
	set.mu.Lock()
	bl := set.active["127.0.0.1:"+strconv.Itoa(basePort)]
	set.mu.Unlock()
	require.NotNil(t, bl)
	require.NoError(t, bl.ln.Close())

	select {
	case got := <-serveErr:
		require.Error(t, got)
		assert.Contains(t, got.Error(), "127.0.0.1:"+strconv.Itoa(basePort),
			"the report must name which listener died")
	case <-time.After(3 * time.Second):
		t.Fatal("a listener that stopped unasked must be reported")
	}
}

func TestListenerSet_PrimaryListenAddr(t *testing.T) {
	t.Run("the base wins while it is bound", func(t *testing.T) {
		port := freePort(t)
		set, _ := newTestSet(t, "127.0.0.1:"+strconv.Itoa(port))
		assert.Equal(t, "127.0.0.1:"+strconv.Itoa(port), set.PrimaryListenAddr())
	})

	t.Run("a merged wildcard reports the dial form", func(t *testing.T) {
		port := freePort(t)
		set, _ := newTestSet(t, "127.0.0.1:"+strconv.Itoa(port))
		require.NoError(t, set.Apply([]listenset.Addr{listenset.MustParse("*:" + strconv.Itoa(port))}))
		// ":4327" and never "*:4327": settings.browserHostForListen reads the
		// wildcard as an EMPTY host with a port, so the canonical spelling
		// would produce a link to a machine named "*".
		assert.Equal(t, ":"+strconv.Itoa(port), set.PrimaryListenAddr())
	})

	t.Run("a desktop hub gains an address from its extras", func(t *testing.T) {
		port := freePort(t)
		// No base at all: the NoTCP desktop.
		set, _ := newTestSet(t, "")
		assert.Equal(t, "", set.PrimaryListenAddr(), "no TCP address is the desktop's ordinary state")

		require.NoError(t, set.Apply([]listenset.Addr{
			listenset.MustParse("127.0.0.1:" + strconv.Itoa(port)),
		}))
		assert.Equal(t, "127.0.0.1:"+strconv.Itoa(port), set.PrimaryListenAddr(),
			"a hub that answers on an address must stop reporting that it has none")
	})
}

func TestListenerSet_HasNonLoopbackAddress(t *testing.T) {
	ports := freePorts(t, 2)
	basePort, wildcardPort := ports[0], ports[1]
	set, _ := newTestSet(t, "127.0.0.1:"+strconv.Itoa(basePort))
	assert.False(t, set.HasNonLoopbackAddress(), "a loopback -listen exposes nothing")

	require.NoError(t, set.Apply([]listenset.Addr{
		listenset.MustParse("*:" + strconv.Itoa(wildcardPort)),
	}))
	assert.True(t, set.HasNonLoopbackAddress(),
		"a wildcard answers on every interface, so the hub is reachable from another machine")
}

// -listen may ask for port 0 (a harness picking a free port). Every surface
// must then report the port a client can actually connect to, not ":0".
func TestListenerSet_ResolvesAnEphemeralPort(t *testing.T) {
	set, _ := newTestSet(t, "127.0.0.1:0")

	addr := set.PrimaryListenAddr()
	host, port, err := net.SplitHostPort(addr)
	require.NoError(t, err)
	assert.Equal(t, "127.0.0.1", host)
	assert.NotEqual(t, "0", port, "the chosen port must replace the request for one")
	requireAnswers(t, addr)
	assert.Equal(t, []string{addr}, boundStrings(set))

	// And the resolved address is the set's identity, so a later apply
	// recognises it as already bound rather than binding it twice.
	require.NoError(t, set.Apply(nil))
	assert.Equal(t, []string{addr}, boundStrings(set))
	requireAnswers(t, addr)
}

func TestResolvePort(t *testing.T) {
	t.Parallel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	chosen := ln.Addr().(*net.TCPAddr).Port

	got := resolvePort(listenset.MustParse("127.0.0.1:0"), ln)
	assert.Equal(t, "127.0.0.1:"+strconv.Itoa(chosen), got.String())

	// A stated port is kept verbatim, so the identity a caller asked for is
	// the identity the set keys on.
	stated := listenset.MustParse("127.0.0.1:4327")
	assert.Equal(t, stated.String(), resolvePort(stated, ln).String())

	// A wildcard keeps its KIND. Reading the whole address off the socket
	// would turn "*:0" into "[::]:<port>", which the merge would then treat
	// as a different address from the one it asked for.
	wildcard := resolvePort(listenset.MustParse("*:0"), ln)
	assert.Equal(t, listenset.KindAny, wildcard.Kind())
	assert.Equal(t, "*:"+strconv.Itoa(chosen), wildcard.String())
}

// The report must say WHY each address is bound, because "127.0.0.1:4327 is
// gone" and "127.0.0.1:4327 is served by *:4327" look identical in a list that
// only names what is serving.
func TestListenerSet_BoundReportsWhyEachAddressIsServed(t *testing.T) {
	ports := freePorts(t, 2)
	basePort, extraPort := ports[0], ports[1]
	set, _ := newTestSet(t, "127.0.0.1:"+strconv.Itoa(basePort))

	require.NoError(t, set.Apply([]listenset.Addr{
		listenset.MustParse("127.0.0.1:" + strconv.Itoa(extraPort)),
	}))

	sources := map[string]AddressSource{}
	for _, b := range set.Bound() {
		sources[b.Addr.String()] = b.Source
	}
	assert.Equal(t, SourceListen, sources["127.0.0.1:"+strconv.Itoa(basePort)])
	assert.Equal(t, SourceExtra, sources["127.0.0.1:"+strconv.Itoa(extraPort)])
}

func TestBoundAddressesForLog(t *testing.T) {
	t.Parallel()
	bound := []BoundAddress{
		{Addr: listenset.MustParse("127.0.0.1:4327"), Source: SourceListen},
		{Addr: listenset.MustParse("192.168.1.24:8080"), Source: SourceExtra, Err: "address already in use"},
		{Addr: listenset.MustParse("*:9000"), Source: SourceExtra},
	}
	// The failures are already logged where they happened, with the operating
	// system's reason; this line states what the hub answers on.
	assert.Equal(t, []string{"127.0.0.1:4327", "*:9000"}, boundAddressesForLog(bound))
}

// An Apply BEFORE Serve must bind the socket and not answer on it yet.
//
// NewServer applies the stored extras before the hub seeds its revocation
// watcher and starts its background loops -- the same window in which the base
// listener is bound and deliberately not served. Answering there would reach a
// hub that is not ready.
func TestListenerSet_DoesNotServeBeforeServeRuns(t *testing.T) {
	ports := freePorts(t, 2)
	basePort, extraPort := ports[0], ports[1]

	server := &http.Server{
		ReadHeaderTimeout: 5 * time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("ok"))
		}),
	}
	base := listenset.MustParse("127.0.0.1:" + strconv.Itoa(basePort))
	baseLn, err := net.Listen("tcp", base.DialAddr())
	require.NoError(t, err)

	set := newListenerSet(server, baseLn, &base, make(chan error, 1))
	t.Cleanup(func() {
		_ = set.Close()
		_ = server.Close()
	})

	extra := listenset.MustParse("127.0.0.1:" + strconv.Itoa(extraPort))
	require.NoError(t, set.Apply([]listenset.Addr{extra}))

	// BOUND -- the port is taken, so a second hub still fails fast on it --
	// and not answering.
	_, err = net.Listen("tcp", extra.DialAddr())
	require.Error(t, err, "the address must be bound even before the hub serves")
	assert.False(t, answers(t, extra.String()), "nothing may be answered before Serve")
	assert.False(t, answers(t, base.String()))

	set.Serve()
	requireAnswers(t, extra.String())
	requireAnswers(t, base.String())
}
