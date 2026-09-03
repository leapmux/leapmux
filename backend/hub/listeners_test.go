package hub

import (
	"math/rand/v2"
	"net"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/listenset"
)

// The band freePorts draws from. Below Linux's default ephemeral floor (32768)
// and below the BSD/macOS one (49152), so the kernel hands no port in it to
// anything else on this machine. See freePorts for why that matters.
const (
	testPortBandLow  = 20000
	testPortBandHigh = 29999
)

// freePorts returns n DISTINCT ports that nothing holds ON ANY INTERFACE.
//
// Every listener is held open until all n are chosen, then closed. The caller
// binds all n at once, so all n have to be free at one moment -- and taking
// them one at a time returns the same port twice on most systems, because the
// kernel hands back the port it just freed. Two test addresses that are
// secretly one address make a merge test pass for the wrong reason.
//
// Two things here depart from the obvious `net.Listen("127.0.0.1:0")`, and each
// closes a failure this file actually saw.
//
// THE BAND. `:0` draws from the operating system's ephemeral range, which is
// where every other process on the machine gets its ports too. Between this
// function closing its probe and the test binding the port itself there is a
// window the kernel may fill with a browser, a dev server, or a second test
// binary. That window cannot be closed -- the port has to be free for the test
// to take it -- so the fix is to leave the pool that everything else draws
// from. Nothing hands out a port in this band without being asked for it by
// number.
//
// THE WILDCARD. A port free on 127.0.0.1 is NOT free for a wildcard bind: a
// process holding 192.168.0.2:PORT does not collide with 127.0.0.1:PORT and
// does collide with *:PORT, which several tests below bind. Probing on the
// wildcard is what makes the answer mean what those callers need. It is also
// what the loopback probe got wrong: `bind: address already in use` on a
// *:PORT that this function had just declared free.
//
// Every address in this file is 127.0.0.1 on a port of its own, never
// 127.0.0.2. Linux assigns the whole 127.0.0.0/8 to loopback and macOS assigns
// only 127.0.0.1, so a second loopback literal fails there with "can't assign
// requested address" -- a failure about the machine rather than about the code
// under test.
func freePorts(t *testing.T, n int) []int {
	t.Helper()
	held := make([]net.Listener, 0, n)
	// Only for the give-up path below, which is a t.Fatal: the probes would
	// otherwise stay bound for the rest of the binary and take the band away
	// from every later test in it. The success path empties `held` first, so
	// this then closes nothing.
	t.Cleanup(func() {
		for _, ln := range held {
			_ = ln.Close()
		}
	})
	ports := make([]int, 0, n)
	// A random start, so two runs of this package at the same time -- one `go
	// test ./...` per checkout -- do not walk the band in the same order and
	// collide on every port of it.
	port := testPortBandLow + rand.IntN(testPortBandHigh-testPortBandLow+1)
	for range testPortBandHigh - testPortBandLow + 1 {
		if len(ports) == n {
			break
		}
		// Almost every port in the band is free, because nothing here is handed
		// out by the kernel. An in-use one is a service that asked for that
		// number, or another run of this package -- either way a reason to try
		// the next port rather than to fail.
		if ln, err := net.Listen("tcp", ":"+strconv.Itoa(port)); err == nil {
			held = append(held, ln)
			ports = append(ports, port)
		}
		port++
		if port > testPortBandHigh {
			port = testPortBandLow
		}
	}
	require.Len(t, ports, n, "no %d free ports in %d-%d; something is holding the whole band",
		n, testPortBandLow, testPortBandHigh)
	// Released together, so the caller finds every one of them free.
	for _, ln := range held {
		require.NoError(t, ln.Close())
	}
	held = held[:0]
	return ports
}

// freePort is freePorts for the single-address tests.
func freePort(t *testing.T) int {
	t.Helper()
	return freePorts(t, 1)[0]
}

// Every test in this file rests on freePorts, so its own contract is pinned
// rather than assumed. The BAND assertion is the deterministic guard: a probe
// that went back to `net.Listen("127.0.0.1:0")` would return ephemeral ports and
// fail it every time, on a quiet machine as loudly as on a busy one. That is the
// point -- the failure it replaces appeared only under load, which is where a
// flake is least welcome and hardest to reproduce.
func TestFreePorts_ReturnsDistinctWildcardBindablePortsOutsideTheEphemeralRange(t *testing.T) {
	ports := freePorts(t, 4)
	require.Len(t, ports, 4)

	seen := map[int]bool{}
	for _, port := range ports {
		assert.GreaterOrEqual(t, port, testPortBandLow, "port %d is below the band", port)
		assert.LessOrEqual(t, port, testPortBandHigh, "port %d is above the band", port)
		assert.False(t, seen[port], "port %d was returned twice", port)
		seen[port] = true

		// On the WILDCARD, which is what several tests below bind and what the
		// loopback probe could not answer for. Bound and released one at a time:
		// the caller's own binds are what must succeed, and holding all four
		// here would only re-prove what freePorts already proved.
		ln, err := net.Listen("tcp", ":"+strconv.Itoa(port))
		require.NoError(t, err, "port %d must be free on every interface", port)
		require.NoError(t, ln.Close())
	}
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
	set := newListenerSet(baseLn, base, serveErr)
	set.setServer(server)
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

func requireBoundFailure(t *testing.T, bound []BoundAddress, address string) BoundAddress {
	t.Helper()
	for _, candidate := range bound {
		if candidate.Addr.String() != address {
			continue
		}
		require.NotEmpty(t, candidate.Err, "the refused address must include the operating system's bind error")
		assert.Contains(t, candidate.Err, address, "the bind error must identify its refused address")
		return candidate
	}
	t.Fatalf("the refused address %s is absent from the listener status", address)
	return BoundAddress{}
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

	// ONE socket now, and it still answers on the address -listen gave.
	assert.Equal(t, []string{"*:" + strconv.Itoa(port)}, boundStrings(set))
	requireAnswers(t, "127.0.0.1:"+strconv.Itoa(port))

	bound := set.Bound()
	require.Len(t, bound, 1)
	assert.Equal(t, SourceMerged, bound[0].Source,
		"the panel must be able to say the -listen address is served by this one, not that it is gone")
}

// A failure must leave the hub exactly as it was. The base is what this
// protects: a merge closes it first, so a rollback that forgot to rebind would
// take the hub off the address its operator gave on the command line.
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
	occupied := "127.0.0.1:" + strconv.Itoa(occupiedPort)
	requireBoundFailure(t, set.Bound(), occupied)
}

// A best-effort apply must still REMOVE the addresses the new configuration
// drops, whatever else in it fails to bind.
//
// The two halves of one edit are independent: an operator who deletes a
// published address and adds an unreachable one in the same write must get the
// deletion. The failing half rolls itself back, and a rollback restores the set
// it started from -- which still holds the address that was deleted. Keeping it
// published would leave the hub answering at an address its operator removed,
// until the next write or the next restart.
func TestListenerSet_ApplyBestEffortStillRemovesADroppedAddress(t *testing.T) {
	ports := freePorts(t, 3)
	basePort, droppedPort, occupiedPort := ports[0], ports[1], ports[2]
	blocker, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(occupiedPort))
	require.NoError(t, err)
	t.Cleanup(func() { _ = blocker.Close() })

	set, _ := newTestSet(t, "127.0.0.1:"+strconv.Itoa(basePort))
	dropped := listenset.MustParse("127.0.0.1:" + strconv.Itoa(droppedPort))
	require.NoError(t, set.Apply([]listenset.Addr{dropped}))
	requireAnswers(t, dropped.String())

	// One write: the published address goes, an unbindable one arrives.
	set.ApplyBestEffort([]listenset.Addr{listenset.MustParse("127.0.0.1:" + strconv.Itoa(occupiedPort))})

	requireStopsAnswering(t, dropped.String())
	// The -listen address survives, as it must survive every reconfiguration.
	requireAnswers(t, "127.0.0.1:"+strconv.Itoa(basePort))
	assert.Equal(t, []string{"127.0.0.1:" + strconv.Itoa(basePort)}, boundStrings(set))
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

	// The LISTENER must survive, and identity is what proves it: closing a
	// listener does not close the connections it already accepted, so a
	// connection dialled before the applies would stay open through a full
	// close-and-rebind and prove nothing.
	before := listenerPointers(set)

	for range 3 {
		require.NoError(t, set.Apply(extras))
	}
	assert.Equal(t, before, listenerPointers(set),
		"a no-op apply must not close and re-open a socket: a keep-alive client "+
			"would see it close for no reason, and another process could take the port")
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
		// would produce a link to a machine called "*".
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

// "Is this hub reachable from another machine" is answered from the REPORT, by
// listenset.AnyNonLoopback -- the solo launcher's startup warning and the
// password-setup screen both read it, so the two cannot disagree about one hub.
func TestListenerSet_BoundReportsWhetherAnyAddressIsReachable(t *testing.T) {
	ports := freePorts(t, 2)
	basePort, wildcardPort := ports[0], ports[1]
	set, _ := newTestSet(t, "127.0.0.1:"+strconv.Itoa(basePort))
	assert.False(t, listenset.AnyNonLoopback(set.Bound()), "a loopback -listen exposes nothing")

	require.NoError(t, set.Apply([]listenset.Addr{
		listenset.MustParse("*:" + strconv.Itoa(wildcardPort)),
	}))
	assert.True(t, listenset.AnyNonLoopback(set.Bound()),
		"a wildcard answers on every interface, so the hub is reachable from another machine")
}

// unserved answers the settle failure in ApplyBestEffort, and it must not
// blame an address the merge folded into a wider one.
//
// Its interesting case is unreachable from Apply: reaching it needs a socket
// that bound a moment ago to refuse the next bind, which no fixture can stage.
// So the predicate is exercised directly.
func TestListenerSet_UnservedAsksCoverageNotIdentity(t *testing.T) {
	ports := freePorts(t, 2)
	basePort, wildcardPort := ports[0], ports[1]
	set, _ := newTestSet(t, "127.0.0.1:"+strconv.Itoa(basePort))
	require.NoError(t, set.Apply([]listenset.Addr{
		listenset.MustParse("*:" + strconv.Itoa(wildcardPort)),
	}))

	// Bound under a key of its own.
	assert.Empty(t, set.unserved([]listenset.Addr{
		listenset.MustParse("127.0.0.1:" + strconv.Itoa(basePort)),
	}))
	// Served by the wildcard, with NO key of its own. An identity test would
	// call this absent and report a working address as a failure.
	assert.Empty(t, set.unserved([]listenset.Addr{
		listenset.MustParse("127.0.0.1:" + strconv.Itoa(wildcardPort)),
	}))
	// Genuinely absent: a port nothing in the set covers.
	missing := listenset.MustParse("127.0.0.1:1")
	assert.Equal(t, []listenset.Addr{missing}, set.unserved([]listenset.Addr{missing}))
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
// only states what is serving.
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

	set := newListenerSet(baseLn, &base, make(chan error, 1))
	set.setServer(server)
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

// listenerPointers identifies each live socket by the listener object itself,
// so a test can tell "the same socket" from "a socket on the same address".
func listenerPointers(set *listenerSet) map[string]net.Listener {
	set.mu.Lock()
	defer set.mu.Unlock()
	out := make(map[string]net.Listener, len(set.active))
	for key, bl := range set.active {
		out[key] = bl.ln
	}
	return out
}

// An unrelated settings write must not touch the sockets.
//
// The settings manager fires EVERY subscriber on every write, so a hub with
// one permanently unbindable stored address ran the per-address retry loop on
// each save -- closing and re-binding every healthy socket, inside the
// manager's own reload lock, with a window for another process to take the
// port each time.
func TestListenerSet_ApplyBestEffortIgnoresAnUnchangedList(t *testing.T) {
	ports := freePorts(t, 3)
	set, _ := newTestSet(t, "127.0.0.1:"+strconv.Itoa(ports[0]))

	good := listenset.MustParse("127.0.0.1:" + strconv.Itoa(ports[1]))
	// An address nothing can bind: another socket already holds the port.
	occupied, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(ports[2]))
	require.NoError(t, err)
	t.Cleanup(func() { _ = occupied.Close() })
	dead := listenset.MustParse("127.0.0.1:" + strconv.Itoa(ports[2]))

	extras := []listenset.Addr{good, dead}
	set.ApplyBestEffort(extras)
	requireAnswers(t, good.String())
	before := listenerPointers(set)

	// The SAME list again, which is what every unrelated settings write
	// delivers. Nothing may move.
	set.ApplyBestEffort(extras)
	assert.Equal(t, before, listenerPointers(set),
		"an unrelated settings write must not close and re-bind a working socket")

	// The failure is still reported, so the panel keeps stating it.
	var failures []string
	for _, b := range set.Bound() {
		if b.Err != "" {
			failures = append(failures, b.Addr.String())
		}
	}
	assert.Equal(t, []string{dead.String()}, failures)

	// And a CHANGED list is applied.
	set.ApplyBestEffort([]listenset.Addr{good})
	requireAnswers(t, good.String())
	assert.ElementsMatch(t,
		[]string{"127.0.0.1:" + strconv.Itoa(ports[0]), good.String()},
		boundStrings(set))
}

// A rollback records the address it could not restore, and a later successful
// apply must not erase that record.
//
// The -listen socket is what this protects. ApplyBestEffort ends on a clean
// apply in the ordinary case, and clearing the whole failure map there
// reported a hub with no problems at the one address whose loss the operator
// most needs to see.
func TestListenerSet_AFailureThatIsStillTrueSurvivesTheNextApply(t *testing.T) {
	ports := freePorts(t, 2)
	set, _ := newTestSet(t, "127.0.0.1:"+strconv.Itoa(ports[0]))

	occupied, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(ports[1]))
	require.NoError(t, err)
	t.Cleanup(func() { _ = occupied.Close() })
	dead := listenset.MustParse("127.0.0.1:" + strconv.Itoa(ports[1]))

	set.ApplyBestEffort([]listenset.Addr{dead})
	failed := map[string]string{}
	for _, b := range set.Bound() {
		if b.Err != "" {
			failed[b.Addr.String()] = b.Err
		}
	}
	require.Contains(t, failed, dead.String())

	// The port frees up. An UNCHANGED list is still not retried -- that is the
	// whole point of skipping the unrelated settings write -- so the failure
	// stands until the list itself changes or the hub restarts.
	require.NoError(t, occupied.Close())
	set.ApplyBestEffort([]listenset.Addr{dead})
	assert.NotEmpty(t, set.Bound()[len(set.Bound())-1].Err,
		"an unchanged list is not a retry, and the failure it reports is still true")

	// A CHANGED list is applied, the address binds, and the failure stops
	// being reported because the set now answers on it.
	other := listenset.MustParse("127.0.0.1:" + strconv.Itoa(freePort(t)))
	set.ApplyBestEffort([]listenset.Addr{dead, other})
	requireAnswers(t, dead.String())
	for _, b := range set.Bound() {
		assert.Emptyf(t, b.Err, "%s bound, so it is no longer a failure", b.Addr)
	}
}

// A failure stops being reported the moment the operator deletes the address.
//
// The failure record is not cleared wholesale any more -- a rollback writes an
// address the hub could not bind AGAIN, and that one is still true after the
// next successful apply -- so the pruning has to answer "does the caller still
// ask for this?" as well as "does the hub serve it now?".
func TestListenerSet_ADeletedAddressStopsBeingReported(t *testing.T) {
	ports := freePorts(t, 2)
	set, _ := newTestSet(t, "127.0.0.1:"+strconv.Itoa(ports[0]))

	occupied, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(ports[1]))
	require.NoError(t, err)
	t.Cleanup(func() { _ = occupied.Close() })
	dead := listenset.MustParse("127.0.0.1:" + strconv.Itoa(ports[1]))

	set.ApplyBestEffort([]listenset.Addr{dead})
	require.NotEmpty(t, failedStrings(set), "an unbindable address must be reported")

	// The operator removes it. It is neither served nor asked for, so the panel
	// must stop showing it -- there is no row left to remove it from.
	set.ApplyBestEffort(nil)
	assert.Empty(t, failedStrings(set), "a deleted address is not a failure")
	assert.Equal(t, []string{"127.0.0.1:" + strconv.Itoa(ports[0])}, boundStrings(set))
}

// failedStrings renders the addresses the set reports as unbindable.
func failedStrings(set *listenerSet) []string {
	out := []string{}
	for _, b := range set.Bound() {
		if b.Err != "" {
			out = append(out, b.Addr.String())
		}
	}
	return out
}

// An address that cannot bind must not disturb the addresses that already
// serve.
//
// The set retried the list one entry at a time FROM EMPTY, so adding one
// unbindable address closed and re-bound every healthy socket -- and a re-bind
// that lost its port was not rolled back, because that attempt had closed
// nothing of its own to restore. One new address took a published one away.
func TestListenerSet_ApplyBestEffortLeavesTheServingSocketsAlone(t *testing.T) {
	ports := freePorts(t, 4)
	set, _ := newTestSet(t, "127.0.0.1:"+strconv.Itoa(ports[0]))

	first := listenset.MustParse("127.0.0.1:" + strconv.Itoa(ports[1]))
	second := listenset.MustParse("127.0.0.1:" + strconv.Itoa(ports[2]))
	set.ApplyBestEffort([]listenset.Addr{first, second})
	requireAnswers(t, first.String())
	requireAnswers(t, second.String())
	before := listenerPointers(set)

	blocker, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(ports[3]))
	require.NoError(t, err)
	t.Cleanup(func() { _ = blocker.Close() })
	dead := listenset.MustParse("127.0.0.1:" + strconv.Itoa(ports[3]))

	set.ApplyBestEffort([]listenset.Addr{first, second, dead})

	assert.Equal(t, before, listenerPointers(set),
		"an address that cannot bind must not close and re-open a socket that already serves")
	requireAnswers(t, first.String())
	requireAnswers(t, second.String())
	assert.Equal(t, []string{dead.String()}, failedStrings(set))
}

// A set with no server yet binds, reports and closes.
//
// NewServer builds the set at the top -- before the mux and the CSP its
// http.Server is built from exist -- so the reporter can hold it by value
// rather than through a getter with a nil branch nothing can reach. Nothing
// serves in that window: serveLocked spawns no goroutine while `serving` is
// false, and only Serve sets it, strictly after setServer.
func TestListenerSet_BindsBeforeItHasAServer(t *testing.T) {
	port := freePort(t)
	base := listenset.MustParse("127.0.0.1:" + strconv.Itoa(port))
	baseLn, err := net.Listen("tcp", base.DialAddr())
	require.NoError(t, err)

	set := newListenerSet(baseLn, &base, make(chan error, 1))
	t.Cleanup(func() { _ = set.Close() })

	extraPort := freePort(t)
	extra := listenset.MustParse("127.0.0.1:" + strconv.Itoa(extraPort))
	require.NoError(t, set.Apply([]listenset.Addr{extra}),
		"a set with no server must still bind what it is asked for")
	assert.ElementsMatch(t, []string{base.String(), extra.String()}, boundStrings(set))
	assert.Equal(t, base.DialAddr(), set.PrimaryListenAddr())
	assert.False(t, listenset.AnyNonLoopback(set.Bound()))
}
