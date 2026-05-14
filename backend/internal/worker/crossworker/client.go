package crossworker

import (
	"context"
	"errors"
	"fmt"
	"sync"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/tunnel"
)

// DelegationScope identifies the (user, workspace) the bearer is
// minted against, plus the spawn provenance (agent_id OR terminal_id —
// the hub uses these for the audit log). UserID and WorkspaceID are
// required; the spawn identifiers may be empty for hub-facing calls
// without a specific spawn provenance.
type DelegationScope struct {
	UserID      string
	WorkspaceID string
	AgentID     string
	TerminalID  string
}

// DelegationProvider supplies a fresh delegation-token bearer for the
// (user, workspace) pair the spawning worker needs to act on. The
// implementation calls the hub's /worker/delegation-tokens/mint
// endpoint with the worker's own AuthToken and caches the result.
type DelegationProvider interface {
	GetBearer(ctx context.Context, scope DelegationScope) (string, error)
}

// Client maintains a pool of E2EE channels keyed by (target_worker, user)
// so multiple cross-worker calls share a single Noise_NK session.
//
// All hub calls (GetWorkerHandshakeParams, OpenChannel, /ws/channel)
// authenticate with a delegation token obtained via DelegationProvider.
type Client struct {
	HubURL     string
	Pins       *PinStore
	Delegation DelegationProvider

	mu       sync.Mutex
	channels map[clientKey]*pooledChannel
}

type clientKey struct {
	WorkerID    string
	UserID      string
	WorkspaceID string
}

type pooledChannel struct {
	ch *tunnel.Channel
}

// New returns a ready-to-use Client.
func New(hubURL string, pins *PinStore, dp DelegationProvider) *Client {
	return &Client{
		HubURL:     hubURL,
		Pins:       pins,
		Delegation: dp,
		channels:   make(map[clientKey]*pooledChannel),
	}
}

// channelFor returns a (cached) E2EE channel to targetWorkerID for
// scope.UserID. Mints a fresh delegation token + channel on cache miss.
//
// scope.WorkspaceID is forwarded to the delegation mint call so the
// token's scope matches the eventual call site.
func (c *Client) channelFor(ctx context.Context, targetWorkerID string, scope DelegationScope) (*tunnel.Channel, error) {
	if targetWorkerID == "" {
		return nil, errors.New("crossworker: target_worker_id required")
	}
	if scope.UserID == "" {
		return nil, errors.New("crossworker: user_id required")
	}
	if scope.WorkspaceID == "" {
		return nil, errors.New("crossworker: workspace_id required")
	}
	key := clientKey{WorkerID: targetWorkerID, UserID: scope.UserID, WorkspaceID: scope.WorkspaceID}

	c.mu.Lock()
	if existing, ok := c.channels[key]; ok && existing.ch != nil && !existing.ch.Closed() {
		c.mu.Unlock()
		return existing.ch, nil
	}
	c.mu.Unlock()

	bearer, err := c.Delegation.GetBearer(ctx, scope)
	if err != nil {
		return nil, fmt.Errorf("delegation token: %w", err)
	}

	openOpts := &tunnel.OpenChannelOptions{
		BearerToken: bearer,
		KeyPin:      c.Pins,
	}
	ch, err := tunnel.OpenChannel(ctx, c.HubURL, scope.UserID, targetWorkerID, openOpts)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	c.channels[key] = &pooledChannel{ch: ch}
	c.mu.Unlock()

	return ch, nil
}

// CallInner sends a unary inner RPC to a sibling worker. workspaceID
// is the delegation scope used both for minting the bearer and for
// keying the channel pool — the same `(user, worker)` pair on a
// different workspace gets a separate Noise_NK session.
func (c *Client) CallInner(ctx context.Context, targetWorkerID, userID, workspaceID, method string, payload []byte) ([]byte, error) {
	ch, err := c.channelFor(ctx, targetWorkerID, DelegationScope{UserID: userID, WorkspaceID: workspaceID})
	if err != nil {
		return nil, err
	}
	resp, err := ch.CallRPC(method, payload)
	if err != nil {
		return nil, err
	}
	return resp.GetPayload(), nil
}

// StreamInner subscribes to a server-streaming inner RPC and invokes
// onMsg for every message. Returns when the stream ends or ctx is
// cancelled. workspaceID semantics match CallInner.
func (c *Client) StreamInner(ctx context.Context, targetWorkerID, userID, workspaceID, method string, payload []byte, onMsg func(*leapmuxv1.InnerStreamMessage)) error {
	ch, err := c.channelFor(ctx, targetWorkerID, DelegationScope{UserID: userID, WorkspaceID: workspaceID})
	if err != nil {
		return err
	}
	reqID, err := ch.SendRPCNoWait(method, payload)
	if err != nil {
		return err
	}
	defer ch.UnregisterStream(reqID)

	done := make(chan struct{})
	ch.RegisterStream(reqID, func(m *leapmuxv1.InnerStreamMessage) {
		onMsg(m)
		if m.GetEnd() {
			select {
			case <-done:
			default:
				close(done)
			}
		}
	})
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-ch.Context().Done():
		return ch.Context().Err()
	}
}

// Close terminates all pooled channels.
func (c *Client) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, p := range c.channels {
		if p.ch != nil {
			p.ch.Close()
		}
	}
	c.channels = make(map[clientKey]*pooledChannel)
}
