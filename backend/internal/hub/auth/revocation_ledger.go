package auth

import (
	"sync"
	"sync/atomic"
	"time"
)

type revocationMark struct {
	generation uint64
	recordedAt time.Time
}

type userRevocationMark struct {
	revocationMark
	userAuthGeneration int64
}

type revocationLedger struct {
	sessionRevocations  sync.Map // sessionID -> revocationMark
	userRevocations     sync.Map // userID -> userRevocationMark
	userInvalidations   sync.Map // userID -> revocationMark (cache-only, not auth rejection)
	bearerRevocations   sync.Map // bearer kind + tokenID -> revocationMark
	bearerInvalidations sync.Map // bearer kind + tokenID -> revocationMark (cache-only)
	revocationGen       atomic.Uint64
}

// bumpGeneration advances the ordering sequence used to compare validation
// snapshots with identity-scoped revocation and invalidation marks.
func (r *revocationLedger) bumpGeneration() uint64 {
	return r.revocationGen.Add(1)
}

// ShouldEvictForUserGeneration reports whether a cached credential minted at
// cachedGeneration must be dropped by a user-wide revocation at
// revokeGeneration. A non-positive revokeGeneration means "the committed
// generation is unknown, drop every current credential" (fail safe); otherwise
// only credentials strictly older than the committed generation are dropped, so
// a credential re-minted at the new generation survives. The four
// generation-selective sweeps (leases, sessions, bearers in this package, and
// the channel manager's CloseByUserRevocation) share this rule so they cannot
// drift out of agreement -- which is why it is exported.
func ShouldEvictForUserGeneration(cachedGeneration, revokeGeneration int64) bool {
	return revokeGeneration <= 0 || cachedGeneration < revokeGeneration
}

func userRevokedAfter(m *sync.Map, user *UserInfo, cacheGeneration uint64) bool {
	if m == nil || user == nil || user.ID == "" {
		return false
	}
	v, ok := m.Load(user.ID)
	if !ok {
		return false
	}
	switch mark := v.(type) {
	case userRevocationMark:
		if mark.userAuthGeneration > 0 {
			return user.UserAuthGeneration < mark.userAuthGeneration
		}
		return cacheGeneration < mark.generation
	case revocationMark:
		return cacheGeneration < mark.generation
	default:
		return false
	}
}

func recordRevocation(m *sync.Map, key string, gen uint64) {
	if m == nil || key == "" || gen == 0 {
		return
	}
	m.Store(key, revocationMark{generation: gen, recordedAt: time.Now()})
}

func recordBearerRevocation(m *sync.Map, ref BearerRef, gen uint64) {
	if m == nil || !ref.IsValid() || gen == 0 {
		return
	}
	m.Store(ref, revocationMark{generation: gen, recordedAt: time.Now()})
}

func bearerRevokedAfter(m *sync.Map, ref BearerRef, gen uint64) bool {
	if m == nil || !ref.IsValid() {
		return false
	}
	return revocationMarkAfter(m, ref, gen)
}

func revokedAfter(m *sync.Map, key string, gen uint64) bool {
	if m == nil || key == "" {
		return false
	}
	return revocationMarkAfter(m, key, gen)
}

func revocationMarkAfter(m *sync.Map, key any, gen uint64) bool {
	v, ok := m.Load(key)
	if !ok {
		return false
	}
	switch mark := v.(type) {
	case revocationMark:
		return gen < mark.generation
	default:
		return false
	}
}

func userInvalidatedAfter(m *sync.Map, userID string, validationGen uint64) bool {
	return revokedAfter(m, userID, validationGen)
}

func sweepRevocationMarks(m *sync.Map, cutoff time.Time) {
	m.Range(func(key, value any) bool {
		var recordedAt time.Time
		switch mark := value.(type) {
		case revocationMark:
			recordedAt = mark.recordedAt
		case userRevocationMark:
			recordedAt = mark.recordedAt
		}
		if !recordedAt.IsZero() && recordedAt.Before(cutoff) {
			m.Delete(key)
		}
		return true
	})
}
