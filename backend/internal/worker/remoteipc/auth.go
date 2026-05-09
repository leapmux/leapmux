// Package remoteipc implements the worker's local-IPC ConnectRPC server
// and authentication map. Spawned agents (and opt-in terminals) talk to
// this server over a per-process Unix-domain socket / named pipe; the
// server forwards calls either to the local worker handlers, to a
// sibling worker via the cross-worker channel, or to the hub via the
// worker's hub client (with a delegation token).
package remoteipc

import (
	"crypto/sha256"
	"errors"
	"sync"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// TokenInfo describes the credentials a spawned process presents over
// the local IPC socket.
type TokenInfo struct {
	UserID            string
	OrgID             string // The user's org. Stable for the lifetime of the spawn — workspaces don't move between orgs.
	WorkspaceID       string
	WorkerID          string            // The spawning worker.
	TabID             string            // The spawned tab (agent or terminal). Anchor for LocateTab-based derivations.
	TabType           leapmuxv1.TabType // Determines which inner-RPC the CLI uses to derive working_dir / etc.
	IssuedAt          time.Time
	DelegationTokenID string // Set lazily when a delegation token has been minted.

	// Context snapshot baked into LEAPMUX_REMOTE_* env vars so the
	// spawned process can default CLI flags. These are stable for the
	// lifetime of the spawn — working dir and provider are set at
	// agent creation and don't change. Workspace id and tile id are
	// intentionally NOT here: they're derivable from the stable tab id
	// via a single hub LocateTab call, so there's no env var to go
	// stale on tab move / cross-workspace move. Org id IS in here
	// (and emitted as LEAPMUX_REMOTE_ORG_ID) because workspaces don't
	// migrate between orgs, so it's safe and avoids a round-trip.
	WorkingDir    string
	AgentProvider string // Empty for terminals.
}

// TokenStore maps LEAPMUX_REMOTE_TOKEN values to their TokenInfo. The
// store is per-process, in-memory, and tied to the worker's lifetime —
// agents that survive the worker process get fresh sockets+tokens on
// the next launch.
type TokenStore struct {
	mu     sync.RWMutex
	byHash map[[32]byte]TokenInfo
}

// NewTokenStore returns an empty store.
func NewTokenStore() *TokenStore {
	return &TokenStore{byHash: map[[32]byte]TokenInfo{}}
}

// Register adds a token. Returns the raw token; callers should set it
// in the spawned process's environment as LEAPMUX_REMOTE_TOKEN. We hash
// the raw token before storing so a memory dump doesn't expose the
// bearer in a directly-usable form.
func (s *TokenStore) Register(rawToken string, info TokenInfo) {
	hash := hashToken(rawToken)
	s.mu.Lock()
	s.byHash[hash] = info
	s.mu.Unlock()
}

// Lookup returns the TokenInfo for the given raw token, or
// ErrUnknownToken if the token isn't registered. The comparison is
// constant-time.
func (s *TokenStore) Lookup(rawToken string) (TokenInfo, error) {
	hash := hashToken(rawToken)
	s.mu.RLock()
	info, ok := s.byHash[hash]
	s.mu.RUnlock()
	if !ok {
		return TokenInfo{}, ErrUnknownToken
	}
	return info, nil
}

// SetDelegationTokenID records that a delegation token has been minted
// for this raw token (so subsequent hub/cross-worker calls reuse the
// same delegation rather than minting per-call).
func (s *TokenStore) SetDelegationTokenID(rawToken, delegationTokenID string) {
	hash := hashToken(rawToken)
	s.mu.Lock()
	if info, ok := s.byHash[hash]; ok {
		info.DelegationTokenID = delegationTokenID
		s.byHash[hash] = info
	}
	s.mu.Unlock()
}

// Revoke removes a token. Idempotent.
func (s *TokenStore) Revoke(rawToken string) {
	hash := hashToken(rawToken)
	s.mu.Lock()
	delete(s.byHash, hash)
	s.mu.Unlock()
}

// hashToken returns a deterministic 32-byte hash of the raw token so
// the in-memory map can compare without storing the bearer verbatim.
// Uses SHA-256 for collision resistance: the previous XOR-window
// reduction collided trivially (any two tokens that differed only at
// positions 32 apart hashed identically), which would let a forged
// token resolve to a real bearer's TokenInfo.
func hashToken(raw string) [32]byte {
	return sha256.Sum256([]byte(raw))
}

// ErrUnknownToken is returned by Lookup when the raw token is not
// registered.
var ErrUnknownToken = errors.New("remoteipc: unknown token")
