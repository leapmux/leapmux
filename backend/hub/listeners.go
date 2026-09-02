package hub

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/listenset"
	"github.com/leapmux/leapmux/internal/hub/settings"
	"github.com/leapmux/leapmux/internal/hub/usernames"
)

// The hub's own spellings for the shared listener vocabulary, so this file
// reads as it always did while `listenset` owns the type. A report crosses to
// the service package unconverted now: the mirror it used to be converted into
// stringified the address, which takes String() and DialAddr() away from every
// consumer on the far side.
type (
	AddressSource = listenset.Source
	BoundAddress  = listenset.Bound
)

const (
	SourceListen = listenset.SourceListen
	SourceExtra  = listenset.SourceExtra
	SourceMerged = listenset.SourceMerged
)

// removedListener is an address Apply closed, kept so a failure later in the
// same call can bind it again.
type removedListener struct {
	addr   listenset.Addr
	source AddressSource
}

// failedAddress is one address the hub was asked to serve and could not bind,
// with the operating system's reason and why the hub wanted it.
type failedAddress struct {
	addr   listenset.Addr
	source AddressSource
	err    string
}

// bindFailure is the one address Apply could not bind. ApplyBestEffort reads it
// to withhold exactly that entry and ask again for everything else, so an
// address that already serves is never closed for another address's failure.
type bindFailure struct {
	addr listenset.Addr
	err  error
}

func (e *bindFailure) Error() string { return fmt.Sprintf("listen %s: %v", e.addr, e.err) }
func (e *bindFailure) Unwrap() error { return e.err }

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
	// base is the address -listen gave, or nil under NoTCP. It is never
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
	//
	// It holds the ADDRESS and the SOURCE, not a string to parse back. Bound()
	// used to re-parse the map key, with a silent `continue` on a parse error
	// -- a branch that could only hide the failure the map exists to report --
	// and it labelled every entry SourceExtra, so a -listen socket a rollback
	// could not restore was reported to the operator as an "extra" they never
	// configured and cannot remove.
	failed map[string]failedAddress
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
	// lastExtras is the canonical spelling of the extras ApplyBestEffort last
	// settled on, and applied says whether it settled at all; see
	// sameAsLastRequest.
	lastExtras string
	applied    bool
	wg         sync.WaitGroup
}

// newListenerSet builds the set around an already-bound base listener.
//
// The base is bound by the caller, BEFORE the database opens, and that order
// is deliberate: two hubs racing for the same port must fail fast, with no
// window in which one of them has done database work. The extras cannot join
// that early, because the row that holds them is not readable until the store
// is open.
//
// The http.Server arrives LATER, through setServer. It is built from the mux
// and the CSP the whole service wiring produces, and the set is built before
// any of that so the reporter can hold it by value: a set resolved through a
// getter forced every read to carry a nil branch that nothing could reach.
func newListenerSet(baseLn net.Listener, base *listenset.Addr, serveErr chan<- error) *listenerSet {
	s := &listenerSet{
		base:     base,
		serveErr: serveErr,
		active:   make(map[string]*boundListener),
		failed:   make(map[string]failedAddress),
	}
	if baseLn != nil && base != nil {
		resolved := resolvePort(*base, baseLn)
		s.base = &resolved
		s.active[resolved.String()] = &boundListener{ln: baseLn, addr: resolved, source: SourceListen}
	}
	return s
}

// setServer attaches the http.Server the set serves its listeners with.
//
// It is called once, before Serve. A set with no server binds and reports
// perfectly well: serveLocked spawns nothing while `serving` is false, and
// Serve is what sets it -- which is strictly after this.
func (s *listenerSet) setServer(server *http.Server) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.server = server
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

// closeLocked closes one listener and takes it out of the live set, returning
// the operating system's error unfiltered so each caller can decide what a
// failure means. The caller holds the lock.
//
// The `closing` mark goes on FIRST and is what the three callers share: without
// it the serve goroutine reads its own Serve return as a listener that died on
// its own and tears the hub down, so a mark that one caller forgot would turn
// every reconfiguration into a fatal fault.
func (s *listenerSet) closeLocked(key string, bl *boundListener) error {
	bl.closing = true
	err := bl.ln.Close()
	delete(s.active, key)
	return err
}

// bindLocked opens one address, registers it and starts serving it. The caller
// holds the lock.
//
// It returns the listener it registered, whose addr carries the port the
// operating system chose when the caller asked for 0.
func (s *listenerSet) bindLocked(addr listenset.Addr, source AddressSource) (*boundListener, error) {
	ln, err := net.Listen("tcp", addr.DialAddr())
	if err != nil {
		return nil, err
	}
	bl := &boundListener{ln: ln, addr: resolvePort(addr, ln), source: source}
	s.active[bl.addr.String()] = bl
	s.serveLocked(bl)
	return bl, nil
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
// would otherwise leave the hub unreachable at the address its operator gave.
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
		if err := s.closeLocked(key, bl); err != nil {
			slog.Warn("closing a listener the hub no longer serves failed",
				"address", bl.addr.String(), "error", err)
		}
		closed = append(closed, removedListener{addr: bl.addr, source: bl.source})
	}

	var opened []string
	for _, addr := range want {
		if _, live := s.active[addr.String()]; live {
			continue
		}
		// A stored extra address never carries port 0 (the settings validator
		// refuses it), so bindLocked only ever resolves a port for the base.
		bl, err := s.bindLocked(addr, s.sourceFor(addr))
		if err != nil {
			return errors.Join(&bindFailure{addr: addr, err: err}, s.rollbackLocked(opened, closed))
		}
		opened = append(opened, bl.addr.String())
	}

	// A clean apply clears the failures that STOPPED being true, and only
	// those: an address the set now answers on, and an address this call no
	// longer asks for. What it must not clear is an address the operator still
	// wants and the hub still cannot bind -- rollbackLocked records exactly
	// that, the -listen socket among them, and clearing the whole map here let
	// the next successful apply inside ApplyBestEffort erase it, so the panel
	// reported no problems on a hub that had lost the address its operator
	// gave on the command line.
	for key, f := range s.failed {
		if !s.requested(extras, f.addr) || s.servesLocked(f.addr) {
			delete(s.failed, key)
		}
	}
	return nil
}

// requested reports whether the caller still asks for this address: the base,
// or one of the extras it passed. The caller holds the lock.
//
// It reads the list as WRITTEN rather than the merged result, because an
// address the merge folded away is still one the operator asked for -- and an
// address they deleted must stop being reported the moment they delete it.
func (s *listenerSet) requested(extras []listenset.Addr, addr listenset.Addr) bool {
	if s.base != nil && s.base.String() == addr.String() {
		return true
	}
	for _, e := range extras {
		if e.String() == addr.String() {
			return true
		}
	}
	return false
}

// servesLocked reports whether a live listener answers on addr. The caller
// holds the lock.
//
// It asks Covers rather than the map keys, because an address the merge folded
// into a wider one IS served -- an extra of 127.0.0.1:4327 beside a bound
// *:4327 has no key of its own, and reading it as absent would blame a working
// address for a failure elsewhere.
func (s *listenerSet) servesLocked(addr listenset.Addr) bool {
	for _, bl := range s.active {
		if bl.addr.Covers(addr) {
			return true
		}
	}
	return false
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
		if err := s.closeLocked(key, bl); err != nil {
			errs = append(errs, fmt.Errorf("roll back %s: %w", key, err))
		}
	}
	for _, r := range closed {
		if _, err := s.bindLocked(r.addr, r.source); err != nil {
			slog.Error("the hub could not bind an address again after a failed reconfiguration; it is no longer served",
				"address", r.addr.String(), "error", err)
			errs = append(errs, fmt.Errorf("restore %s: %w", r.addr, err))
			// With its own source, not SourceExtra: this is the address the
			// operator gave on the command line, and reporting it as an extra
			// would point them at a settings row they cannot remove it from.
			s.failed[r.addr.String()] = failedAddress{addr: r.addr, source: r.source, err: err.Error()}
		}
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
	if s.sameAsLastRequest(extras) {
		return
	}
	// Withhold the ONE address each attempt could not bind, and ask again for
	// everything else. Apply is diff-based, so an address that already serves
	// keeps the socket it holds: only the refused entry moves.
	//
	// Rebuilding the list from EMPTY instead -- one address, then two -- asked
	// Apply to close every other extra on the first attempt and to bind them
	// again on the later ones. Each close/bind pair opened a window for another
	// process to take the port, and a bind that lost that race was not rolled
	// back, because the attempt had closed nothing of its own to restore. So
	// one unbindable NEW address took a healthy published address away for
	// good.
	//
	// The failed address comes from the error rather than from the loop
	// counter, so Apply's own merge decides which entry is at fault.
	remaining := extras
	failures := make(map[string]failedAddress, len(extras))
	var err error
	// One attempt for each address that can fail, plus the one that settles.
	for range len(extras) + 1 {
		err = s.Apply(remaining)
		if err == nil {
			break
		}
		var bind *bindFailure
		if !errors.As(err, &bind) {
			break
		}
		slog.Warn("the hub could not bind a stored listen address; it stays configured and is not served",
			"address", bind.addr.String(), "error", err)
		failures[bind.addr.String()] = failedAddress{addr: bind.addr, source: SourceExtra, err: err.Error()}
		next := make([]listenset.Addr, 0, len(remaining))
		for _, a := range remaining {
			if a.String() != bind.addr.String() {
				next = append(next, a)
			}
		}
		if len(next) == len(remaining) {
			// The address that failed is not one of the extras: it is the
			// -listen socket a rollback could not restore. There is no entry
			// left to withhold, and withholding nothing would loop.
			break
		}
		remaining = next
	}
	if err != nil {
		slog.Error("the hub could not reach the stored listen configuration; some addresses are not served",
			"error", err)
		for _, addr := range s.unserved(remaining) {
			failures[addr.String()] = failedAddress{addr: addr, source: SourceExtra, err: err.Error()}
		}
	}
	// AFTER the loop, never inside it. A clean Apply prunes the failures that
	// stopped being true, so recording as we went would let the last success
	// erase what the earlier attempts found, and the panel would show a hub
	// with no problems and a missing address.
	s.recordFailures(failures)
}

// sameAsLastRequest reports whether this is the extras list the previous call
// already settled on, and remembers the list when it is not.
//
// The settings manager fires EVERY subscriber on every write, because a
// subscriber is registered for the whole snapshot rather than for one key. So
// a hub with one permanently unbindable stored address -- a VPN that is down,
// a port another program holds -- ran the per-address retry loop above on
// every unrelated settings save, closing and re-binding every healthy socket
// each time, inside the settings manager's own reload lock. An operator saving
// a session duration waited on bind attempts for addresses they never touched,
// and each close/bind pair opened a window for another process to take the
// port.
//
// The failed addresses are NOT retried here. A write that changes the list
// retries them, and so does a restart; an unrelated settings write is not a
// retry policy anybody chose.
func (s *listenerSet) sameAsLastRequest(extras []listenset.Addr) bool {
	key := strings.Join(listenset.Strings(extras), ",")
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.applied && s.lastExtras == key {
		return true
	}
	s.applied = true
	s.lastExtras = key
	return false
}

// unserved reports which of addrs no live listener answers on.
func (s *listenerSet) unserved(addrs []listenset.Addr) []listenset.Addr {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []listenset.Addr
	for _, addr := range addrs {
		if !s.servesLocked(addr) {
			out = append(out, addr)
		}
	}
	return out
}

// recordFailures adds to the report of addresses the hub could not bind.
//
// It ADDS rather than replaces, so it keeps what rollbackLocked recorded. That map holds one thing the loop
// above can never produce: an address the hub was serving a moment ago and
// could not bind again -- the -listen socket among them -- and clearing it
// here reported a hub with no problems at the one address whose loss the
// operator most needs to see. A key the loop also failed on wins, because its
// reason is the newer one.
func (s *listenerSet) recordFailures(failures map[string]failedAddress) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, f := range failures {
		s.failed[key] = f
	}
}

// Bound reports what the hub serves right now, plus every configured address
// it could not bind. It is sorted, so two reads of an unchanged set are equal.
func (s *listenerSet) Bound() []listenset.Bound {
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
	out := make([]listenset.Bound, 0, len(s.active)+len(s.failed))
	for _, addr := range listenset.Merge(nil, live) {
		out = append(out, listenset.Bound{Addr: addr, Source: sources[addr.String()]})
	}
	for _, f := range s.failed {
		out = append(out, listenset.Bound{Addr: f.addr, Source: f.source, Err: f.err})
	}
	return out
}

// PrimaryListenAddr is the address a browser-facing URL should give: the
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
// a port (":4327"), so the canonical "*:4327" would read as a machine called
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
// It holds the set BY VALUE. The set is built at the top of NewServer, before
// the services that ask, and takes its http.Server later -- so there is no
// window in which a consumer could reach a set that does not exist, and no
// unreachable nil branch to write here for one.
//
// It owns the FALLBACK. "The live primary address, and the one -listen
// gave when nothing is bound" is one rule, and every consumer used to carry
// its own copy of it -- the auth service, the mail renderer and the OAuth
// issuer URL each spelled it -- which is how the links in an email and the
// address GetSystemInfo reports could disagree about the same hub.
//
// The adapter exists to hold that fallback and the lazy getter, and nothing
// else: the REPORT itself crosses unconverted, because `listenset` owns the
// type and both packages already import it.
type listenReporter struct {
	set *listenerSet
	// configured is the address -listen gave, for a hub with nothing bound.
	configured string
}

func (r listenReporter) Bound() []listenset.Bound { return r.set.Bound() }

func (r listenReporter) PrimaryListenAddr() string {
	if addr := r.set.PrimaryListenAddr(); addr != "" {
		return addr
	}
	return r.configured
}

// refuseUnguardedExposure is the cross-key settings rule that keeps a solo hub
// from publishing an address it cannot authenticate anybody on.
//
// The panel already asks for the password before it will apply such an
// address, but a client-side rule is not enforcement: `leapmux control admin
// settings set extra_listen_addresses '{"addresses":["0.0.0.0:4327"]}'` is a
// write like any other, the key is HOT, and a credential-free solo caller is
// admitted to write it -- so one command published an unauthenticated
// administrator on the LAN, with no refusal and no warning. Before this
// feature the same exposure needed `-listen 0.0.0.0:4327` on the command line,
// which printed the startup warning.
//
// It runs on every settings write, which is what makes the panel and the CLI
// obey one rule. The rule reads the ACCOUNT, so it clears the moment the
// password lands: the panel writes the password first and the addresses
// second, and that order is why its Apply succeeds.
//
// Outside solo mode it admits everything. The key is HiddenInHub, so no other
// deployment can hold a value for it, and the `solo` account those hubs would
// look for does not exist.
func refuseUnguardedExposure(soloMode bool, gate *auth.SoloGate) func(*settings.Snapshot) error {
	return func(s *settings.Snapshot) error {
		if !soloMode {
			return nil
		}
		addrs, err := settings.ExtraListenAddresses(s)
		if err != nil {
			// An unreadable document is the value validator's refusal to
			// report, not this rule's; it states one thing only.
			return nil
		}
		exposed := make([]string, 0, len(addrs))
		for _, addr := range addrs {
			if !addr.IsLoopback() {
				exposed = append(exposed, addr.String())
			}
		}
		if len(exposed) == 0 {
			return nil
		}
		// The account read happens ONLY here, after the candidate is known to
		// hold an exposing address. A cross-key rule runs inside the settings
		// write transaction, so this is a store read on another connection
		// while that transaction is open -- safe under WAL, and rare because
		// an ordinary settings save never reaches this line.
		if gate.PasswordSet(context.Background()) {
			return nil
		}
		return fmt.Errorf(
			"extra_listen_addresses %v would answer other machines while the %q account has no password, "+
				"so anyone who reached the port would hold the administrator; set the password first",
			exposed, usernames.Solo)
	}
}
