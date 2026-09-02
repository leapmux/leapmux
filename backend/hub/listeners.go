package hub

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"

	"github.com/leapmux/leapmux/internal/hub/listenset"
	"github.com/leapmux/leapmux/internal/hub/service"
)

// AddressSource says why the hub binds an address, for the administration
// surface that reports what is serving.
type AddressSource string

const (
	// SourceListen is the address -listen names, bound alone.
	SourceListen AddressSource = "listen"
	// SourceExtra is an address the extra_listen_addresses setting adds.
	SourceExtra AddressSource = "extra"
	// SourceMerged is an address that is BOTH: the operator asked for it and
	// it also covers what -listen named, so one socket serves both. The panel
	// says so, because "127.0.0.1:4327 is gone" and "127.0.0.1:4327 is served
	// by *:4327" look identical in a list that only names what is bound.
	SourceMerged AddressSource = "merged"
)

// BoundAddress is one entry of the listener set's report.
type BoundAddress struct {
	Addr   listenset.Addr
	Source AddressSource
	// Err is why this address is NOT serving, and empty when it is. A stored
	// address whose interface went away must not fail the hub, so the failure
	// travels to the administration surface instead of to a startup error.
	Err string
}

// removedListener is an address Apply closed, kept so a failure later in the
// same call can bind it again.
type removedListener struct {
	addr   listenset.Addr
	source AddressSource
}

// boundListener is one live socket and how it was opened.
type boundListener struct {
	ln     net.Listener
	addr   listenset.Addr
	source AddressSource
	// closing is set before a deliberate Close, so the serve goroutine can
	// tell an intended teardown from a listener that died on its own.
	closing bool
}

// listenerSet owns every TCP socket the hub serves on and rebinds them while
// the hub runs.
//
// It exists because the set is not fixed at startup: an administrator adds an
// address in the preferences dialog and the hub must answer on it without a
// restart. The whole set is served by ONE http.Server, so a new listener needs
// nothing but its own Serve goroutine -- the handler, the timeouts, the h2c
// configuration and the shutdown drain are already shared.
//
// The local IPC listener is NOT in here. It is bound once, never rebound, and
// it is the one transport that authenticates a caller by existing, so keeping
// it out means no reconfiguration path can ever close it by mistake.
type listenerSet struct {
	server *http.Server
	// base is the address -listen named, or nil under NoTCP. It is never
	// dropped from the wanted set: Apply merges it with the extras, so an
	// operator can widen it but never take it away, and a settings row can
	// never leave the hub with no socket its operator asked for.
	base *listenset.Addr
	// serveErr carries a listener that stopped WITHOUT being asked to. A
	// deliberate close is filtered out here rather than at the reader, so
	// Serve's select cannot mistake a reconfiguration for a fatal fault.
	serveErr chan<- error

	mu sync.Mutex
	// active is keyed by the address's canonical string, which is its
	// identity: two spellings of one socket fold to one key in listenset, so
	// they cannot both appear here.
	active map[string]*boundListener
	// failed records the addresses the last Apply could not bind, so the
	// administration surface can state the reason instead of showing the
	// address as simply absent.
	failed map[string]string
	// serving is false until Serve runs, and serveLocked spawns nothing while
	// it is.
	//
	// The extras are applied inside NewServer, before the hub seeds its
	// revocation watcher and starts its background loops -- the same window in
	// which the BASE listener is bound and deliberately not served. A request
	// answered there would reach a hub that is not ready, so Apply records the
	// listener and Serve starts every goroutine at once.
	serving bool
	closed  bool
	wg      sync.WaitGroup
}

// newListenerSet builds the set around an already-bound base listener.
//
// The base is bound by the caller, BEFORE the database opens, and that order
// is deliberate: two hubs racing for the same port must fail fast, with no
// window in which one of them has done database work. The extras cannot join
// that early, because the row that names them is not readable until the store
// is open.
func newListenerSet(server *http.Server, baseLn net.Listener, base *listenset.Addr, serveErr chan<- error) *listenerSet {
	s := &listenerSet{
		server:   server,
		base:     base,
		serveErr: serveErr,
		active:   make(map[string]*boundListener),
		failed:   make(map[string]string),
	}
	if baseLn != nil && base != nil {
		resolved := resolvePort(*base, baseLn)
		s.base = &resolved
		s.active[resolved.String()] = &boundListener{ln: baseLn, addr: resolved, source: SourceListen}
	}
	return s
}

// resolvePort replaces a port the caller left to the operating system with the
// one it chose. Everything else about the address is kept, because Go reports
// a wildcard bind as "[::]:4327" and the requested spelling is the identity
// the merge and the live-listener map are keyed on.
//
// -listen may ask for port 0 (a test harness picking a free port), and after
// this the banner, the merge and the administration surface all name the port
// a client can actually connect to.
func resolvePort(addr listenset.Addr, ln net.Listener) listenset.Addr {
	if addr.Port() != 0 {
		return addr
	}
	tcpAddr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		return addr
	}
	return addr.WithPort(tcpAddr.Port)
}

// Serve starts a goroutine for every listener the set already holds. It is
// called once, when the hub begins serving; a listener added later starts its
// own goroutine inside Apply.
func (s *listenerSet) Serve() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.serving = true
	for _, bl := range s.active {
		s.serveLocked(bl)
	}
}

// serveLocked starts one listener's goroutine, unless the hub has not begun
// serving yet -- then Serve starts it. The caller holds the lock.
func (s *listenerSet) serveLocked(bl *boundListener) {
	if !s.serving {
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		err := s.server.Serve(bl.ln)
		if s.serveEnded(bl, err) {
			return
		}
		select {
		case s.serveErr <- fmt.Errorf("serve %s: %w", bl.addr, err):
		default:
			// Serve reports the FIRST fault and tears the hub down, so a
			// second one has nowhere to go and nothing to add.
		}
	}()
}

// serveEnded reports whether a Serve return is an ordinary end rather than a
// fault to report.
//
// Three endings are ordinary, and only the third needs this method's state:
// the http.Server shut down, the listener was closed under it, or THIS set
// closed the listener on purpose to rebind an address. A reconfiguration that
// reported itself as a listener failure would tear the hub down every time an
// operator widened an address.
func (s *listenerSet) serveEnded(bl *boundListener, err error) bool {
	if err == nil || errors.Is(err, http.ErrServerClosed) {
		return true
	}
	// net.ErrClosed is NOT ordinary on its own, and that is the point. It is
	// what Serve returns for every closed listener, so accepting it blind
	// would silence the one failure this channel exists to report: a socket
	// closed by something outside this set, after which the hub answers on
	// that address no more and says nothing. The set's own closes are marked,
	// so only an unmarked one gets through.
	s.mu.Lock()
	defer s.mu.Unlock()
	return bl.closing || s.closed
}

// Apply makes the live listener set match base merged with extras.
//
// Closing runs BEFORE binding, and it has to: an operator who asks for
// `*:4327` on a hub already holding `127.0.0.1:4327` needs the specific socket
// released before the wildcard can take the port. No ordering avoids the
// window between the two, because the operating system offers none.
//
// A failure rolls the whole call back: everything opened here is closed, and
// everything closed here is bound again. The base is what that protects -- a
// merge that closes the -listen socket and then fails to bind its replacement
// would otherwise leave the hub unreachable at the address its operator named.
func (s *listenerSet) Apply(extras []listenset.Addr) error {
	want := listenset.Merge(s.base, extras)

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("listener set is closed")
	}

	wanted := make(map[string]listenset.Addr, len(want))
	for _, a := range want {
		wanted[a.String()] = a
	}

	// Close what is no longer wanted, keeping each one so a failure below can
	// put it back.
	var closed []removedListener
	for key, bl := range s.active {
		if _, keep := wanted[key]; keep {
			continue
		}
		bl.closing = true
		if err := bl.ln.Close(); err != nil {
			slog.Warn("closing a listener the hub no longer serves failed",
				"address", bl.addr.String(), "error", err)
		}
		closed = append(closed, removedListener{addr: bl.addr, source: bl.source})
		delete(s.active, key)
	}

	var opened []string
	for _, addr := range want {
		key := addr.String()
		if _, live := s.active[key]; live {
			continue
		}
		ln, err := net.Listen("tcp", addr.DialAddr())
		if err != nil {
			bindErr := fmt.Errorf("listen %s: %w", addr, err)
			return errors.Join(bindErr, s.rollbackLocked(opened, closed))
		}
		// A stored extra address never carries port 0 (the settings validator
		// refuses it), so this only ever resolves the base.
		bound := resolvePort(addr, ln)
		key = bound.String()
		bl := &boundListener{ln: ln, addr: bound, source: s.sourceFor(bound)}
		s.active[key] = bl
		opened = append(opened, key)
		s.serveLocked(bl)
	}

	// A clean apply clears the previous failures: every address the caller
	// asked for is serving.
	clear(s.failed)
	return nil
}

// rollbackLocked undoes a partial Apply: it closes what this call opened and
// binds again what this call closed. The caller holds the lock.
//
// A rollback that itself fails is reported, never swallowed. It is the case
// where the hub loses an address it was serving a moment ago, and an operator
// reading only "listen 192.168.1.24:8080 failed" would have no way to learn
// that the -listen socket went with it.
func (s *listenerSet) rollbackLocked(opened []string, closed []removedListener) error {
	var errs []error
	for _, key := range opened {
		bl, ok := s.active[key]
		if !ok {
			continue
		}
		bl.closing = true
		if err := bl.ln.Close(); err != nil {
			errs = append(errs, fmt.Errorf("roll back %s: %w", key, err))
		}
		delete(s.active, key)
	}
	for _, r := range closed {
		ln, err := net.Listen("tcp", r.addr.DialAddr())
		if err != nil {
			slog.Error("the hub could not bind an address again after a failed reconfiguration; it is no longer served",
				"address", r.addr.String(), "error", err)
			errs = append(errs, fmt.Errorf("restore %s: %w", r.addr, err))
			s.failed[r.addr.String()] = err.Error()
			continue
		}
		bl := &boundListener{ln: ln, addr: r.addr, source: r.source}
		s.active[r.addr.String()] = bl
		s.serveLocked(bl)
	}
	return errors.Join(errs...)
}

// sourceFor says why an address is bound: the -listen address alone, an extra
// alone, or one address that covers both.
func (s *listenerSet) sourceFor(addr listenset.Addr) AddressSource {
	if s.base == nil {
		return SourceExtra
	}
	switch {
	case addr.String() == s.base.String():
		return SourceListen
	case addr.Covers(*s.base):
		return SourceMerged
	default:
		return SourceExtra
	}
}

// ApplyBestEffort binds what it can and reports what it could not, instead of
// rolling the whole change back.
//
// It is for STARTUP and for a settings write that already committed. A stored
// address stops existing whenever a VPN drops or a lease moves, and a hub that
// refuses to start for that is worse than one that starts and says so. The
// same reasoning covers the settings subscriber: the row is written by then, so
// refusing to bind the rest would only hide which entry is the problem.
//
// The failures are kept for the administration surface to report, so the panel
// prints the operating system's own reason beside the address.
func (s *listenerSet) ApplyBestEffort(extras []listenset.Addr) {
	if err := s.Apply(extras); err == nil {
		return
	}
	// One address at a time, so a single bad entry cannot withhold the others.
	// Apply is diff-based, so the addresses that already bound stay bound and
	// this pass only adds what is missing.
	usable := make([]listenset.Addr, 0, len(extras))
	failures := make(map[string]string, len(extras))
	for _, addr := range extras {
		candidate := append(append([]listenset.Addr(nil), usable...), addr)
		if err := s.Apply(candidate); err != nil {
			slog.Warn("the hub could not bind a stored listen address; it stays configured and is not served",
				"address", addr.String(), "error", err)
			failures[addr.String()] = err.Error()
			continue
		}
		usable = append(usable, addr)
	}
	// AFTER the loop, never inside it. A successful Apply clears the failure
	// record -- it means every address the caller asked for is serving -- so
	// recording as we went would let the last success erase the failures the
	// earlier attempts found, and the panel would show a hub with no problems
	// and a missing address.
	s.recordFailures(failures)
}

// recordFailures replaces the report of addresses the hub could not bind.
func (s *listenerSet) recordFailures(failures map[string]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	clear(s.failed)
	for addr, msg := range failures {
		s.failed[addr] = msg
	}
}

// Bound reports what the hub serves right now, plus every configured address
// it could not bind. It is sorted, so two reads of an unchanged set are equal.
func (s *listenerSet) Bound() []BoundAddress {
	s.mu.Lock()
	defer s.mu.Unlock()

	live := make([]listenset.Addr, 0, len(s.active))
	sources := make(map[string]AddressSource, len(s.active))
	for key, bl := range s.active {
		live = append(live, bl.addr)
		sources[key] = bl.source
	}
	// Merge with no extras sorts the live set without changing it: every
	// address here is already bound, so none can cover another.
	out := make([]BoundAddress, 0, len(s.active)+len(s.failed))
	for _, addr := range listenset.Merge(nil, live) {
		out = append(out, BoundAddress{Addr: addr, Source: sources[addr.String()]})
	}
	for key, msg := range s.failed {
		addr, err := listenset.Parse(key)
		if err != nil {
			continue
		}
		out = append(out, BoundAddress{Addr: addr, Source: SourceExtra, Err: msg})
	}
	return out
}

// PrimaryListenAddr is the address a browser-facing URL should name: the
// -listen address when it is still bound, else the first extra the hub bound.
//
// The fallback is what makes a desktop hub work. It runs with no TCP base at
// all, so before this feature "no -listen" and "no reachable address" were the
// same thing; an extra address makes them different, and a hub answering on
// 192.168.1.24:8080 must not keep reporting that it has no browser origin.
//
// It returns the DIAL form, which is what -listen itself carries and what
// every helper downstream reads. The canonical form must NOT be used here:
// settings.browserHostForListen recognises the wildcard as an EMPTY host with
// a port (":4327"), so the canonical "*:4327" would read as a machine named
// "*" and produce "http://*:4327" in a mail link.
//
// It returns "" when nothing is bound, which is still the desktop's ordinary
// state and which every caller already handles.
func (s *listenerSet) PrimaryListenAddr() string {
	if s.base != nil {
		s.mu.Lock()
		_, live := s.active[s.base.String()]
		s.mu.Unlock()
		if live {
			return s.base.DialAddr()
		}
	}
	for _, b := range s.Bound() {
		if b.Err == "" {
			return b.Addr.DialAddr()
		}
	}
	return ""
}

// HasNonLoopbackAddress reports whether the hub answers on an address another
// machine can reach. The administration surface asks, so it can require a
// password before an exposed hub has none.
func (s *listenerSet) HasNonLoopbackAddress() bool {
	for _, b := range s.Bound() {
		if b.Err == "" && !b.Addr.IsLoopback() {
			return true
		}
	}
	return false
}

// Close shuts every listener down and waits for its serve goroutine.
//
// It is a SAFEGUARD, not the primary teardown: http.Server.Shutdown closes
// every listener it serves. This makes the set's own bookkeeping agree with
// that, so a later Apply cannot resurrect a socket on a hub that is exiting.
func (s *listenerSet) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		s.wg.Wait()
		return nil
	}
	s.closed = true
	var errs []error
	for key, bl := range s.active {
		bl.closing = true
		if err := bl.ln.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			errs = append(errs, fmt.Errorf("close %s: %w", bl.addr, err))
		}
		delete(s.active, key)
	}
	s.mu.Unlock()

	s.wg.Wait()
	return errors.Join(errs...)
}

// boundAddressesForLog renders the serving addresses for one log field. The
// failures are omitted: they are already logged where they happened, with the
// operating system's reason, and this line states what the hub answers on.
func boundAddressesForLog(bound []BoundAddress) []string {
	out := make([]string, 0, len(bound))
	for _, b := range bound {
		if b.Err == "" {
			out = append(out, b.Addr.String())
		}
	}
	return out
}

// listenReporter adapts the listener set to service.ListenReporter.
//
// It resolves the set LAZILY, through a getter, because the services that ask
// are constructed before the set exists: the base listener binds before the
// database opens, and the http.Server the set wraps is built after every
// handler. Each method answers as if the hub had no TCP address until then,
// which is the desktop's ordinary state and which every caller already handles.
//
// The adapter exists at all because the service package sits BELOW this one --
// hub imports service -- so the two cannot share a struct without a third
// package neither of them owns.
type listenReporter struct {
	get func() *listenerSet
}

func (r listenReporter) Bound() []service.BoundListenAddress {
	set := r.get()
	if set == nil {
		return nil
	}
	bound := set.Bound()
	out := make([]service.BoundListenAddress, 0, len(bound))
	for _, b := range bound {
		out = append(out, service.BoundListenAddress{
			Address: b.Addr.String(),
			Source:  string(b.Source),
			Err:     b.Err,
		})
	}
	return out
}

func (r listenReporter) PrimaryListenAddr() string {
	set := r.get()
	if set == nil {
		return ""
	}
	return set.PrimaryListenAddr()
}

func (r listenReporter) HasNonLoopbackAddress() bool {
	set := r.get()
	if set == nil {
		return false
	}
	return set.HasNonLoopbackAddress()
}
