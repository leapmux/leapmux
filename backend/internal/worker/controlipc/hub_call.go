package controlipc

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/hubtransport"
	"github.com/leapmux/leapmux/internal/hubrpc"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/crossworker"
)

// HubUnaryBridge implements HubBridge by talking to the hub's
// WorkspaceService, WorkerManagementService, and UserCRDT services over
// ConnectRPC, authenticated with a per-user delegation-token
// bearer.
//
// Method dispatch is by bare method name (the "hub." namespace prefix
// is stripped by the Router before CallHub is invoked) and follows the
// shared `internal/hubrpc.Registry` — the CLI's `hubCallDirect` walks
// the same table, so adding a hub method requires editing one entry.
//
// **Why a method table instead of a single transparent proxy?**
// Not for security -- the delegation scope is uniform now (`{UserID}`), and
// this type never inspects a payload. The table is a TYPING requirement:
// CallHub receives raw bytes and has to produce a typed Connect call, so it
// needs each method's request/response constructors and invoker. That is what
// `hubrpc.Registry` supplies, and sharing it with the CLI's `hubCallDirect`
// means adding a hub method is one entry rather than two parallel switches.
//
// **Why mirror HubEventStreamer instead of folding both into one
// type?** Unary calls use ConnectRPC; HubEventStreamer consumes a long-lived
// WebSocket. Each needs its own HTTP client because unary calls require an
// overall timeout and subscriptions require none. They share the delegation
// provider and the endpoint's connection pools while retaining those policies.
type HubUnaryBridge struct {
	Delegation crossworker.DelegationProvider
	HTTPClient *http.Client
	ConnectURL string
}

// NewHubUnaryBridge returns a bridge that mints delegation
// bearers via dp and forwards unary hub RPCs to the endpoint.
//
// It takes its OWN client, which it must not share with HubEventStreamer:
// this lane needs a timeout, because a hub that accepts a connection and never
// answers otherwise hangs an agent's `tab list` for ever, and the WebSocket
// lane must have none. The two lanes also need different protocols. They share
// the Endpoint, so they still share one connection pool per protocol and one
// h2c verdict.
func NewHubUnaryBridge(endpoint *hubtransport.Endpoint, dp crossworker.DelegationProvider) *HubUnaryBridge {
	return &HubUnaryBridge{
		Delegation: dp,
		HTTPClient: endpoint.UnaryClient(hubtransport.DefaultUnaryTimeout),
		ConnectURL: endpoint.BaseURL(),
	}
}

// CallHub satisfies HubBridge. method is the bare hub method name (e.g.
// "GetTab", "AddTab", "ListWorkspaces"); the delegation bearer is minted for
// userID alone.
func (b *HubUnaryBridge) CallHub(ctx context.Context, userID userid.UserID, method string, payload []byte) ([]byte, error) {
	if b.Delegation == nil {
		return nil, errors.New("controlipc: delegation provider not configured")
	}
	if userID.IsZero() {
		return nil, errors.New("controlipc: user_id required for hub call")
	}
	desc, err := hubrpc.Lookup(method)
	if err != nil {
		return nil, fmt.Errorf("controlipc: %w", err)
	}
	in := desc.NewRequest()
	out := desc.NewResponse()
	if err := proto.Unmarshal(payload, in); err != nil {
		return nil, fmt.Errorf("controlipc: decode %s request: %w", method, err)
	}
	bearer, err := b.Delegation.GetBearer(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("delegation bearer: %w", err)
	}
	client := connect.NewClient(
		connecthttp.NewTransport(b.HTTPClient, b.ConnectURL, connecthttp.WithReadMaxBytes(0)),
		bearerInterceptor(bearer),
	)
	if err := desc.Invoke(ctx, client, in, out); err != nil {
		return nil, err
	}
	return proto.Marshal(out)
}

// bearerInterceptor sets `Authorization: Bearer <token>` on every
// outbound call. Connect v2 builds the interceptor chain once per
// connect.NewClient, so it costs nothing to construct per call -- and
// the bearer here is minted per call anyway.
func bearerInterceptor(bearer string) connect.ClientInterceptor {
	return func(next connect.ClientFunc) connect.ClientFunc {
		return func(ctx context.Context, spec connect.Spec) (connect.ClientStream, error) {
			// Client call methods attach the CallInfo before the chain
			// runs; make one if this ClientFunc is reached without them.
			info, ok := connect.CallInfoForClientContext(ctx)
			if !ok {
				ctx, info = connect.NewClientContext(ctx)
			}
			info.RequestHeader().Set("Authorization", "Bearer "+bearer)
			return next(ctx, spec)
		}
	}
}
