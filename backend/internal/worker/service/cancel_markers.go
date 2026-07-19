package service

import (
	"time"
)

// cancelMarkers records conn_ids whose close arrived BEFORE (or during) their open,
// so a racing beginOpen/store drops the conn the client already gave up on.
//
// It is its own type because its expiry rule is the subtlest logic on the tunnel
// manager and has nothing to do with conns, dials, or sockets: a marker is
// expirable only by its OWN deadline. A conn_id that is marked, cleared early by an
// open finishing, then re-marked leaves the first (now-orphaned) expiry entry
// behind, and a sweep keyed on conn_id alone would drop the second, LIVE marker up
// to a full TTL early and un-fence its close. The map and the expiry slice share
// POINTER identity to the same entry, so sweep decides "is this expiry entry still
// the live marker for this conn_id?" by pointer equality (`markers[id] == entry`)
// rather than by comparing duplicated timestamps -- the rule is then a property of
// the type system the next edit cannot quietly break by letting the two disagree.
//
// Not internally locked: the tunnel manager calls it under its own mutex, which
// already covers the marker set and the in-flight dial map together.
type cancelMarkers struct {
	// markers maps a conn_id to the live entry whose deadline may be swept. It is
	// the source of truth for "is this conn_id still marked, and by which entry?"
	markers map[string]*cancelMarkerEntry
	// expiry orders markers by deadline so sweep can GC without a per-marker timer.
	// Callers append with a constant TTL, so the slice is ordered by expiry. The
	// entries are pointers shared with the map, so a re-mark (which replaces the
	// map pointer) leaves the orphaned slice entry identifiable by pointer
	// inequality.
	expiry []*cancelMarkerEntry
}

type cancelMarkerEntry struct {
	connID    string
	expiresAt time.Time
}

func newCancelMarkers() cancelMarkers {
	return cancelMarkers{markers: make(map[string]*cancelMarkerEntry)}
}

// mark records a close for connID, expiring at expiresAt. It is for a marker
// with NO in-flight open to clear it: conn_ids are unique per open, so such a
// marker would otherwise never be cleared and would leak -- the common case
// being a target that EOFs and has its conn removed before the client's
// CloseTunnelConn arrives. A marker racing an in-flight open belongs to
// markDuringOpen instead.
//
// The caller supplies the deadline rather than the type deriving it from a stored
// TTL: sweep needs only the instants already recorded, so keeping the TTL policy at
// the call site means this type holds no clock and no configuration, and a caller
// that retunes the TTL (tests do) takes effect on the next mark rather than only on
// a freshly constructed set.
func (c *cancelMarkers) mark(connID string, expiresAt time.Time) {
	c.markDuringOpen(connID, expiresAt)
	// The entry carries the same instant as the marker so sweep can match them.
	entry := &cancelMarkerEntry{connID: connID, expiresAt: expiresAt}
	c.expiry = append(c.expiry, entry)
	// Replace the map pointer AFTER appending, so it names the same entry the
	// slice holds. markDuringOpen set a bare map entry (no slice slot); this
	// overwrites it with the addressable one sweep will compare against.
	c.markers[connID] = entry
}

// markDuringOpen records a close for connID WITHOUT a sweep entry: a marker
// racing an in-flight open is cleared by the store/abortOpen that ends the
// open, so no deadline of its own may reap it early -- sweeping it would
// un-fence a close whose open is still dialing. The entry still carries its
// own deadline because sweep matches an expiry entry against the live map
// entry by POINTER identity (not by timestamp), so an orphaned slice entry
// whose conn_id was re-marked during an open is recognised as stale and left
// alone.
func (c *cancelMarkers) markDuringOpen(connID string, expiresAt time.Time) {
	c.markers[connID] = &cancelMarkerEntry{connID: connID, expiresAt: expiresAt}
}

// take reports whether connID was marked, consuming the marker if so.
func (c *cancelMarkers) take(connID string) bool {
	if _, ok := c.markers[connID]; !ok {
		return false
	}
	delete(c.markers, connID)
	return true
}

// clear drops connID's marker unconditionally. Used when an open finishes and any
// marker set during it is moot.
func (c *cancelMarkers) clear(connID string) { delete(c.markers, connID) }

// sweep drops markers whose TTL has elapsed. Because every entry is appended with
// the same TTL, expiry is ordered by deadline, so this pops the expired prefix --
// amortized O(1) per marker (appended once, popped once). It replaces a per-marker
// time.AfterFunc, which would spawn a runtime timer per closed-before-open conn
// under connection churn, with an opportunistic sweep on a mutex the caller already
// holds and no background goroutine.
//
// Pointer identity decides whether an entry is still live: a re-marked conn_id
// (marked, cleared by an open, then re-marked) leaves the FIRST entry orphaned in
// the slice, and comparing timestamps could mis-pair it with the second LIVE marker
// when the two share a deadline (a re-mark within timer resolution). Comparing the
// map's current pointer to the slice entry -- `markers[id] == entry` -- is uniquely
// decodable regardless of timestamp collision, since the map holds the one live
// entry and the orphan's pointer differs by construction.
//
// The popped entries are compacted out of the backing slice rather than left
// reachable behind a resliced header: each entry is a pointer to a struct carrying
// a string conn_id, and under connection churn a long-lived tunnelManager appends
// one entry per closed-before-open conn for the process's whole life. Without
// compaction the backing array grows to peak churn size and never frees.
func (c *cancelMarkers) sweep(now time.Time) {
	i := 0
	for i < len(c.expiry) && !now.Before(c.expiry[i].expiresAt) {
		entry := c.expiry[i]
		// Delete the marker only if THIS entry is still the live one for its
		// conn_id. A later re-mark replaced the map pointer, so the orphan here
		// has markers[entry.connID] != entry and must not drop the live marker.
		if live, ok := c.markers[entry.connID]; ok && live == entry {
			delete(c.markers, entry.connID)
		}
		i++
	}
	if i > 0 {
		copy(c.expiry, c.expiry[i:])
		for j := len(c.expiry) - i; j < len(c.expiry); j++ {
			c.expiry[j] = nil
		}
		c.expiry = c.expiry[:len(c.expiry)-i]
	}
}

// len reports how many markers are held. For tests.
func (c *cancelMarkers) len() int { return len(c.markers) }

// has reports whether connID is currently marked, without consuming it. For tests.
func (c *cancelMarkers) has(connID string) bool {
	_, ok := c.markers[connID]
	return ok
}
