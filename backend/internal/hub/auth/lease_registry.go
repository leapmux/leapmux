package auth

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/leapmux/leapmux/channelwire"
)

type authenticatedLease struct {
	user   UserInfo
	cancel context.CancelFunc
	timer  *time.Timer
	// expiryEpoch is bumped whenever the expiry timer is (re)armed. A fired
	// timer only expires the lease when its captured epoch still matches, so a
	// reschedule that races an already-fired old timer wins.
	expiryEpoch uint64
}

type authenticatedLeaseRegistry struct {
	nextLeaseID     uint64
	leases          map[uint64]*authenticatedLease
	leasesBySession map[string]map[uint64]struct{}
	leasesByBearer  map[BearerRef]map[uint64]struct{}
	leasesByUser    map[string]map[uint64]struct{}
}

// ChannelExpiryRescheduler reschedules the teardown deadline of the channels
// tied to a credential whose lifetime was extended -- a sliding cookie session
// or a rotated bearer. Unifying both triggers behind one interface (the channel
// manager implements it) keeps a credential's leases and its channels extended
// together, so a new extension event cannot renew the leases while leaving the
// channels to expire at the stale connect-time deadline. Wired in after both are
// constructed (see AuthContextRegistry.SetChannelExpiryRescheduler).
type ChannelExpiryRescheduler interface {
	RescheduleExpiryBySession(sessionID string, newExpiry CredentialDeadline)
	RescheduleExpiryByBearer(ref BearerRef, newExpiry CredentialDeadline)
}

// LeaseOutcome says whether a lease was granted and, when it was not, WHY.
//
// A bare bool used to answer this, and the one caller turned every false into
// "authentication expired or revoked" -- true for the only refusal there was.
// A connection cap is a second, unrelated reason to say no, and telling a user
// their credential expired when they simply have too many tabs open is a lie
// that costs them a re-login they do not need.
type LeaseOutcome int

const (
	// LeaseGranted: the lease is registered and the release function is live.
	LeaseGranted LeaseOutcome = iota
	// LeaseRefusedCredential: the credential is expired or revoked. The client
	// has to re-authenticate; retrying the same connection cannot help.
	LeaseRefusedCredential
	// LeaseRefusedTooManyConnections: this user already holds MaxConnectionsPerUser
	// long-lived connections. Nothing is wrong with the credential, and the
	// condition clears the moment any of them closes.
	LeaseRefusedTooManyConnections
)

// Label returns the outcome's stable token. It is BOTH the value of the
// `reason` label on leapmux_connections_refused_total and the WebSocket close
// reason the Hub sends when it refuses the connection, so an operator's metric
// and the client's branch are reading one vocabulary rather than two spellings
// of it that can disagree.
//
// Callers must use this rather than a literal, and every value must stay a valid
// close reason: RFC 6455 caps one at 123 bytes and coder/websocket enforces that
// on send, so a descriptive sentence here would not go out at all. The refusal
// tokens are also client-facing contract -- "close a tab" and "re-authenticate"
// are opposite advice -- so renaming one is a wire change, not a cosmetic one.
func (o LeaseOutcome) Label() string {
	switch o {
	case LeaseGranted:
		return "granted"
	case LeaseRefusedCredential:
		return "credential"
	case LeaseRefusedTooManyConnections:
		// The token channelwire already defines for this close, not a second
		// copy of the same string: nothing checked that the two agreed, and the
		// frontend asserts against channelwire's pinned testdata.
		return channelwire.CloseReasonTooManyConnections
	default:
		return "unknown"
	}
}

// SetMaxConnectionsPerUser bounds how many long-lived authenticated connections
// one user may hold at once. Zero (the default) is unlimited. Call once at
// startup before serving requests.
//
// A setter rather than a constructor argument for the same reason
// SetChannelExpiryRescheduler is one: the two interceptor constructors have
// twenty call sites between them, and a registry built directly as
// &AuthContextRegistry{state: &authState{}} -- which most of this package's
// tests do -- must come out unlimited. The zero value gives that for free.
func (c *AuthContextRegistry) SetMaxConnectionsPerUser(n int64) {
	if c == nil || c.state == nil {
		return
	}
	c.state.maxConnectionsPerUser.Store(n)
}

// RegisterAuthenticatedLease ties a long-lived connection to the concrete
// credential that authenticated it. Registration and revocation checks share
// one lock with mark publication, closing the upgrade/revocation race. The
// returned release function removes the lease without canceling the caller.
func (c *AuthContextRegistry) RegisterAuthenticatedLease(ctx context.Context, user *UserInfo, cancel context.CancelFunc) (func(), LeaseOutcome) {
	noop := func() {}
	if c == nil || c.state == nil {
		return noop, LeaseGranted
	}
	if user == nil || cancel == nil {
		if cancel != nil {
			cancel()
		}
		return noop, LeaseRefusedCredential
	}

	// Resolve the credential's CURRENT deadline OFF the lock first, so a session
	// whose cache row was evicted (e.g. by a concurrent user_info change) after a
	// slide still resolves the AUTHORITATIVE DB expiry via CurrentCredentialExpiry's
	// fallback -- this WebSocket lease is the twin of the channel guard, so it must
	// fall back to the DB the same way. Without it a lease racing a slide whose cache
	// row is gone degrades to the stale connect-time value and the still-valid
	// userevents / channel-relay socket is torn down early. The DB read runs off
	// revocationMu, so it never holds the hot auth lock across store I/O.
	floor := c.CurrentCredentialExpiry(ctx, user)

	c.state.revocationMu.Lock()
	// Merge that floor with a read taken under revocationMu -- the same lock a slide
	// holds while advancing the cached deadline and renewing indexed leases -- so a
	// slide that lands between the off-lock read and here is still caught (the renew
	// sweep walks only already-indexed leases, so a slide racing this registration is
	// otherwise out of its reach). .Later() keeps the most permissive of the two, so
	// the lease is never armed at (or liveness-checked against) a deadline older than
	// any this process knows. Mirrors the channel path's OpenChannel guard.
	effectiveExpiry := c.state.currentCredentialExpiryLocked(user).Later(floor)
	if !effectiveExpiry.IsCurrent(time.Now()) || !c.isAuthContextCurrentLocked(user) {
		c.state.revocationMu.Unlock()
		cancel()
		return noop, LeaseRefusedCredential
	}
	// The cap is tested against leasesByUser and committed by addLeaseLocked
	// inside ONE critical section, which is what makes it a bound rather than a
	// suggestion: a check taken before the lock would let every connection in a
	// burst read the same under-cap count and all of them register.
	//
	// AFTER the credential guards on purpose. A user at the cap whose session has
	// also expired is told the credential is the problem, because that is the one
	// they must act on -- closing tabs would not get them back in.
	//
	// The count is taken ONCE, into a local the branch and the log line share. A
	// second len() is a second observation even under the same lock, so the
	// number an operator reads has to be the one that actually gated the
	// decision -- and this is the process-wide lock every connect and every
	// revocation serializes on, so the map lookup and the user-id formatting it
	// needs happen once instead of twice. Nested inside the cap check rather than
	// hoisted above it so an unlimited registry -- the zero value, which every
	// embedder that never calls the setter gets -- does neither.
	if maxPerUser := c.state.maxConnectionsPerUser.Load(); maxPerUser > 0 {
		if held := int64(len(c.state.leasesByUser[user.ID.String()])); held >= maxPerUser {
			c.state.revocationMu.Unlock()
			cancel()
			// Logged where the count and the cap are both in hand, and at Warn
			// because it is the operator's to act on: either a client is leaking
			// sockets or the cap is under what this fleet's users legitimately hold.
			// After the unlock, so a slow log handler cannot hold the auth lock.
			slog.Warn("refusing connection: user is at its concurrent-connection cap",
				"user_id", user.ID, "held", held, "limit", maxPerUser)
			return noop, LeaseRefusedTooManyConnections
		}
	}
	lease := &authenticatedLease{user: *user, cancel: cancel}
	lease.user.CredentialExpiresAt = effectiveExpiry
	id := c.state.addLeaseLocked(lease)
	if at, ok := effectiveExpiry.At(); ok {
		epoch := lease.expiryEpoch
		lease.timer = time.AfterFunc(time.Until(at), func() {
			c.state.expireLease(id, epoch)
		})
	}
	c.state.revocationMu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			c.state.revocationMu.Lock()
			lease, ok := c.state.leases[id]
			if ok {
				c.state.removeLeaseLocked(id, lease)
			}
			c.state.revocationMu.Unlock()
			if ok && lease.timer != nil {
				lease.timer.Stop()
			}
		})
	}, LeaseGranted
}

// currentCredentialExpiryLocked returns the most permissive teardown deadline
// this process knows for user's credential, read while revocationMu is already
// held. It is the lock-held analogue of AuthContextRegistry.CurrentCredentialExpiry
// (which acquires the lock itself and may fall back to an authoritative DB read):
// a session reads the deadline on its cached row -- which a concurrent slide
// advances by storing a fresh cached row under revocationMu -- and a bearer consults its recorded
// per-rotation extension. It never does store I/O, which would be unsafe under
// this hot lock, so a session cache miss degrades to the connect-time value.
// Taking the more permissive of the connect-time and current deadlines never
// arms earlier than the value the caller validated against.
func (s *authState) currentCredentialExpiryLocked(user *UserInfo) CredentialDeadline {
	if sessionID := user.Credential.SessionID(); sessionID != "" {
		if v, ok := s.sessions.Load(sessionID); ok {
			if cached, ok := v.(cachedSession); ok && cached.user != nil {
				return user.CredentialExpiresAt.Later(cached.user.CredentialExpiresAt)
			}
		}
		return user.CredentialExpiresAt
	}
	if ref, ok := user.Credential.BearerRef(); ok {
		if v, ok := s.bearerExpiries.Load(ref); ok {
			return user.CredentialExpiresAt.Later(v.(CredentialDeadline))
		}
	}
	return user.CredentialExpiresAt
}

func (s *authState) expireLease(id, epoch uint64) {
	s.revocationMu.Lock()
	lease, ok := s.leases[id]
	// A reschedule (session slide / bearer rotation) bumps expiryEpoch and arms
	// a fresh timer; a stale timer whose epoch no longer matches must not tear
	// down the extended lease.
	if !ok || lease.expiryEpoch != epoch {
		s.revocationMu.Unlock()
		return
	}
	s.removeLeaseLocked(id, lease)
	s.revocationMu.Unlock()
	lease.cancel()
}

// rescheduleLeaseLocked re-times a lease's expiry to newExpiry (or disarms it
// when newExpiry is NeverExpires/Unset), bumping the epoch so a stale
// already-fired timer is ignored by expireLease. Caller holds revocationMu.
func (s *authState) rescheduleLeaseLocked(id uint64, lease *authenticatedLease, newExpiry CredentialDeadline) {
	// Never regress a still-later finite deadline to an earlier one an
	// out-of-order concurrent extension delivered (its channel-side twin
	// rescheduleExpiryLocked and the cache-side RecordBearerExpiry guard the
	// same way), so the acting lease is not torn down early.
	next := lease.user.CredentialExpiresAt.AdoptReschedule(newExpiry)
	lease.user.CredentialExpiresAt = next
	lease.expiryEpoch++
	epoch := lease.expiryEpoch
	if lease.timer != nil {
		lease.timer.Stop()
	}
	at, ok := next.At()
	if !ok {
		// NeverExpires (or Unset): the lease carries no teardown timer.
		lease.timer = nil
		return
	}
	lease.timer = time.AfterFunc(time.Until(at), func() {
		s.expireLease(id, epoch)
	})
}

func (s *authState) renewLeasesLocked(ids map[uint64]struct{}, newExpiry CredentialDeadline) {
	for id := range ids {
		if lease := s.leases[id]; lease != nil {
			s.rescheduleLeaseLocked(id, lease, newExpiry)
		}
	}
}

// RestampSessionLeaseGeneration advances the recorded user-auth generation of
// every authenticated lease tied to sessionID to newGeneration, so a following
// user-wide revocation at that generation spares the surviving session's own
// connections. This is the lease-side mirror of
// channelmgr.RestampSessionGeneration: a password change keeps the acting
// session alive but bumps the user generation, and the acting session's leases
// still carry their connect-time (older) generation, so without this restamp
// RevokeUserAuthContextAtGeneration would cancel the acting user's own
// userevents / channel-relay WebSockets. Only leases at an older generation are
// advanced, so a concurrently-opened newer lease is left untouched. Call it
// before UserRevoked, alongside the channel restamp.
func (c *AuthContextRegistry) RestampSessionLeaseGeneration(sessionID string, newGeneration int64) {
	if c == nil || c.state == nil || sessionID == "" || newGeneration <= 0 {
		return
	}
	c.state.revocationMu.Lock()
	defer c.state.revocationMu.Unlock()
	for id := range c.state.leasesBySession[sessionID] {
		if lease := c.state.leases[id]; lease != nil && lease.user.UserAuthGeneration < newGeneration {
			lease.user.UserAuthGeneration = newGeneration
		}
	}
}

// RenewBearerLeases re-times the teardown deadline of every lease authenticated
// by the given bearer to newExpiry after a refresh rotates and extends it. A
// NeverExpires newExpiry clears the deadline, matching rescheduleLeaseLocked and
// its channel-side twin RescheduleExpiryByBearer -- with the deadline now a typed
// CredentialDeadline the lease and channel sides of a rotation cannot even
// express disagreement about what a zero meant.
func (c *AuthContextRegistry) RenewBearerLeases(ref BearerRef, newExpiry CredentialDeadline) {
	if c == nil || c.state == nil || !ref.IsValid() {
		return
	}
	c.state.revocationMu.Lock()
	defer c.state.revocationMu.Unlock()
	c.state.renewLeasesLocked(c.state.leasesByBearer[ref], newExpiry)
}

// SetChannelExpiryRescheduler wires the channel manager so a sliding session or
// a rotated bearer can extend its already-open channels' expiry. Call once at
// startup before serving requests.
func (c *AuthContextRegistry) SetChannelExpiryRescheduler(r ChannelExpiryRescheduler) {
	if c == nil || c.state == nil || r == nil {
		return
	}
	c.state.channelRescheduler.Store(&r)
}

func indexLease[K comparable](index map[K]map[uint64]struct{}, key K, id uint64) {
	ids := index[key]
	if ids == nil {
		ids = make(map[uint64]struct{})
		index[key] = ids
	}
	ids[id] = struct{}{}
}

func removeLeaseIndex[K comparable](index map[K]map[uint64]struct{}, key K, id uint64) {
	ids := index[key]
	delete(ids, id)
	if len(ids) == 0 {
		delete(index, key)
	}
}

// addLeaseLocked inserts lease under a fresh id and populates every reverse
// index. Exact mirror of removeLeaseLocked; keeping insert and teardown on one
// type stops a future reverse index from being added on registration but missed
// on teardown (a lease leak). Caller holds revocationMu.
func (r *authenticatedLeaseRegistry) addLeaseLocked(lease *authenticatedLease) uint64 {
	if r.leases == nil {
		r.leases = make(map[uint64]*authenticatedLease)
		r.leasesBySession = make(map[string]map[uint64]struct{})
		r.leasesByBearer = make(map[BearerRef]map[uint64]struct{})
		r.leasesByUser = make(map[string]map[uint64]struct{})
	}
	r.nextLeaseID++
	id := r.nextLeaseID
	r.leases[id] = lease
	indexLease(r.leasesByUser, lease.user.ID.String(), id)
	if sessionID := lease.user.Credential.SessionID(); sessionID != "" {
		indexLease(r.leasesBySession, sessionID, id)
	}
	if ref, ok := lease.user.Credential.BearerRef(); ok {
		indexLease(r.leasesByBearer, ref, id)
	}
	return id
}

func (r *authenticatedLeaseRegistry) removeLeaseLocked(id uint64, lease *authenticatedLease) {
	delete(r.leases, id)
	removeLeaseIndex(r.leasesByUser, lease.user.ID.String(), id)
	if sessionID := lease.user.Credential.SessionID(); sessionID != "" {
		removeLeaseIndex(r.leasesBySession, sessionID, id)
	}
	if ref, ok := lease.user.Credential.BearerRef(); ok {
		removeLeaseIndex(r.leasesByBearer, ref, id)
	}
}

// removeAllLeasesLocked scans every registered lease and removes those matching.
// Used by teardown paths (Stop) that have no reverse index to scope the sweep.
func (r *authenticatedLeaseRegistry) removeAllLeasesLocked(matches func(*authenticatedLease) bool) []*authenticatedLease {
	var removed []*authenticatedLease
	for id, lease := range r.leases {
		if matches(lease) {
			r.removeLeaseLocked(id, lease)
			removed = append(removed, lease)
		}
	}
	return removed
}

// removeIndexedLeasesLocked removes the matching leases named by index (a
// session/bearer/user reverse-index bucket), so the sweep touches only the
// candidate leases rather than the whole registry. A nil/empty index removes
// nothing. Split from removeAllLeasesLocked -- rather than folded into one
// variadic -- so "scan all" versus "scan this index" is expressed in the
// signature and a caller cannot accidentally pass an extra index that is
// silently ignored.
func (r *authenticatedLeaseRegistry) removeIndexedLeasesLocked(matches func(*authenticatedLease) bool, index map[uint64]struct{}) []*authenticatedLease {
	var removed []*authenticatedLease
	for id := range index {
		if lease := r.leases[id]; lease != nil && matches(lease) {
			r.removeLeaseLocked(id, lease)
			removed = append(removed, lease)
		}
	}
	return removed
}

func (r *authenticatedLeaseRegistry) cancelLeases(leases []*authenticatedLease) {
	for _, lease := range leases {
		if lease.timer != nil {
			lease.timer.Stop()
		}
		lease.cancel()
	}
}
