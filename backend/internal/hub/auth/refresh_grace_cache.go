package auth

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"sync"
	"time"

	"github.com/leapmux/leapmux/internal/util/periodic"
)

// RefreshGraceCache holds the (access_bearer, refresh_bearer) pair issued
// during a refresh-token rotation, encrypted in process memory, so we can
// re-emit the *same* pair if the client retries the same refresh request
// within RefreshReuseGrace (e.g. after the original response was lost in
// transit).
//
// Without this cache the retry path could only return a token-id with no
// usable secret, breaking the legitimate recovery scenario the grace
// window exists for.
//
// Storage notes:
//   - The key material is generated at construction time with crypto/rand.
//     When the process restarts, all entries become unreadable — that is
//     acceptable: refresh retries that race a process restart fall through
//     to the standard "no cached pair" error path.
//   - Plaintext is `access_bearer + "\x00" + refresh_bearer`. Bearer strings
//     are base32 over an alphabet that never contains NUL.
//   - Each entry uses a fresh 12-byte GCM nonce.
type RefreshGraceCache struct {
	aead cipher.AEAD
	ttl  time.Duration

	mu      sync.Mutex
	entries map[string]*graceEntry
}

type graceEntry struct {
	nonce     []byte
	ct        []byte
	expiresAt time.Time
}

// ErrGraceCacheMiss is returned when the cache has no entry for the
// requested token id (either it was never written, has expired, or was
// lost across a process restart).
var ErrGraceCacheMiss = errors.New("auth: refresh grace cache miss")

// NewRefreshGraceCache constructs an in-process AEAD-protected cache.
// ttl is the upper bound on how long an entry survives; callers should
// pass the same value as RefreshReuseGrace.
func NewRefreshGraceCache(ttl time.Duration) (*RefreshGraceCache, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &RefreshGraceCache{
		aead:    aead,
		ttl:     ttl,
		entries: make(map[string]*graceEntry),
	}, nil
}

// Put stores the (access, refresh) pair under tokenID. Overwrites any
// prior entry. Bearers must not contain the NUL byte.
func (c *RefreshGraceCache) Put(tokenID, accessBearer, refreshBearer string) error {
	plaintext := []byte(accessBearer + "\x00" + refreshBearer)
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	ct := c.aead.Seal(nil, nonce, plaintext, []byte(tokenID))
	c.mu.Lock()
	c.entries[tokenID] = &graceEntry{nonce: nonce, ct: ct, expiresAt: time.Now().Add(c.ttl)}
	c.mu.Unlock()
	return nil
}

// Get retrieves and decrypts the cached pair for tokenID. Returns
// ErrGraceCacheMiss if the entry is absent or expired (expired entries
// are evicted as a side effect).
func (c *RefreshGraceCache) Get(tokenID string) (accessBearer, refreshBearer string, err error) {
	c.mu.Lock()
	entry, ok := c.entries[tokenID]
	if !ok {
		c.mu.Unlock()
		return "", "", ErrGraceCacheMiss
	}
	if time.Now().After(entry.expiresAt) {
		delete(c.entries, tokenID)
		c.mu.Unlock()
		return "", "", ErrGraceCacheMiss
	}
	c.mu.Unlock()
	pt, err := c.aead.Open(nil, entry.nonce, entry.ct, []byte(tokenID))
	if err != nil {
		// Treat decryption failure as a miss; should never happen with
		// the in-process key but stay defensive.
		return "", "", ErrGraceCacheMiss
	}
	for i := 0; i < len(pt); i++ {
		if pt[i] == 0x00 {
			return string(pt[:i]), string(pt[i+1:]), nil
		}
	}
	return "", "", ErrGraceCacheMiss
}

// Evict drops the entry for tokenID (used when the underlying API token
// is revoked).
func (c *RefreshGraceCache) Evict(tokenID string) {
	c.mu.Lock()
	delete(c.entries, tokenID)
	c.mu.Unlock()
}

// StartJanitor runs a periodic sweep that evicts expired entries. The
// cache works without it (Get evicts on miss), but a periodic sweep
// caps memory growth when expired entries are never read again.
func (c *RefreshGraceCache) StartJanitor(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		return
	}
	periodic.Start(ctx, periodic.Schedule{Interval: interval, SkipFirstRun: true}, func(context.Context) {
		c.sweep()
	})
}

func (c *RefreshGraceCache) sweep() {
	now := time.Now()
	c.mu.Lock()
	for k, e := range c.entries {
		if now.After(e.expiresAt) {
			delete(c.entries, k)
		}
	}
	c.mu.Unlock()
}
