package service

import (
	"context"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/channel"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"google.golang.org/protobuf/proto"
)

// methodGate names HOW a method's access is enforced.
//
// Every method RegisterAll wires must record exactly one gate via the
// registrar helpers (or ownerOnlyRegistrar). A method with no recorded
// gate fails TestEveryRegisteredMethodIsClassified; a duplicate record
// panics at registration time. The gate is a property of WHERE the
// handler is registered rather than a line each author must remember.
type methodGate int

const (
	gateOwnerOnly methodGate = iota // machine-scoped, ownerOnlyRegistrar
	gateWorkspace                   // structural workspace gate (request field or loaded row)
	gateInBody                      // heterogeneous in-body gate, probe-enforced
	gateSetFilter                   // returns only accessible rows; denial = empty result
	gateNone                        // machine-wide capability probe / liveness, ungated by design
)

// registrar wraps a dispatcher and records each method's gate kind.
//
// It is passed by value; the gates map is shared. record() panics on a
// duplicate method — Dispatcher.Register silently overwrites, and without
// the panic the disjointness check that TestEveryRegisteredMethodIsClassified
// previously enforced by hand would be lost.
type registrar struct {
	d     *channel.Dispatcher
	svc   *Context
	gates map[string]methodGate
}

func newRegistrar(d *channel.Dispatcher, svc *Context) registrar {
	return registrar{d: d, svc: svc, gates: make(map[string]methodGate)}
}

func (r registrar) record(method string, gate methodGate) {
	if _, exists := r.gates[method]; exists {
		panic("duplicate method registration: " + method)
	}
	r.gates[method] = gate
}

// workspaceScopedRequest constrains a request message to the shape the
// workspace-field gate needs: a proto.Message pointer that names a workspace.
// The *T element lets registerWorkspaceGated allocate the zero value itself,
// so a handler never restates the unmarshal — same idiom as connRequest /
// registerConnHandler in tunnel.go.
type workspaceScopedRequest[T any] interface {
	*T
	proto.Message
	GetWorkspaceId() string
}

// agentScopedRequest constrains a request to one that names an agent.
type agentScopedRequest[T any] interface {
	*T
	proto.Message
	GetAgentId() string
}

// terminalScopedRequest constrains a request to one that names a terminal.
type terminalScopedRequest[T any] interface {
	*T
	proto.Message
	GetTerminalId() string
}

// registerWorkspaceGated registers a handler gated on the request's
// workspace_id field. Wrapper: unmarshal → INVALID_ARGUMENT "invalid
// request"; empty workspace_id → INVALID_ARGUMENT "workspace_id is
// required"; inaccessible → PERMISSION_DENIED; then fn with the decoded
// request. The gate is a property of WHERE the handler is registered.
func registerWorkspaceGated[T any, PT workspaceScopedRequest[T]](
	r registrar,
	method string,
	fn func(ctx context.Context, userID string, req PT, sender *channel.Sender),
) {
	r.record(method, gateWorkspace)
	r.d.Register(method, func(ctx context.Context, userID string, req *leapmuxv1.InnerRpcRequest, sender *channel.Sender) {
		var msg T
		decoded := PT(&msg)
		if err := unmarshalRequest(req, decoded); err != nil {
			sendInvalidArgument(sender, "invalid request")
			return
		}
		if decoded.GetWorkspaceId() == "" {
			sendInvalidArgument(sender, "workspace_id is required")
			return
		}
		if !r.svc.requireAccessibleWorkspace(sender, decoded.GetWorkspaceId()) {
			return
		}
		fn(ctx, userID, decoded, sender)
	})
}

// agentGatedHandler builds the unmarshal → requireAccessibleAgent → fn
// wrapper used by registerAgentGated. Explicit type args sidestep
// constraint-inference edge cases in nested generic calls (mirroring
// Dispatcher / ownerOnlyRegistrar style).
func agentGatedHandler[T any, PT agentScopedRequest[T]](
	svc *Context,
	fn func(ctx context.Context, userID string, req PT, row db.Agent, sender *channel.Sender),
) channel.HandlerFunc {
	return func(ctx context.Context, userID string, req *leapmuxv1.InnerRpcRequest, sender *channel.Sender) {
		var msg T
		decoded := PT(&msg)
		if err := unmarshalRequest(req, decoded); err != nil {
			sendInvalidArgument(sender, "invalid request")
			return
		}
		row, ok := svc.requireAccessibleAgent(sender, decoded.GetAgentId())
		if !ok {
			return
		}
		fn(ctx, userID, decoded, row, sender)
	}
}

// registerAgentGated registers a handler gated on the agent row named by
// the request. fn receives the loaded row so the body never double-fetches.
func registerAgentGated[T any, PT agentScopedRequest[T]](
	r registrar,
	method string,
	fn func(ctx context.Context, userID string, req PT, row db.Agent, sender *channel.Sender),
) {
	r.record(method, gateWorkspace)
	r.d.Register(method, agentGatedHandler[T, PT](r.svc, fn))
}

// agentGatedByIDHandler builds the unmarshal → requireAccessibleAgentID → fn
// wrapper shared by registerAgentGatedByID and registerAgentGatedByIDTracked.
// The gate fetches only the agent's workspace_id, so these are for handlers
// that never read the row; ones that do use registerAgentGated instead and
// receive the loaded row.
func agentGatedByIDHandler[T any, PT agentScopedRequest[T]](
	svc *Context,
	fn func(ctx context.Context, userID string, req PT, sender *channel.Sender),
) channel.HandlerFunc {
	return func(ctx context.Context, userID string, req *leapmuxv1.InnerRpcRequest, sender *channel.Sender) {
		var msg T
		decoded := PT(&msg)
		if err := unmarshalRequest(req, decoded); err != nil {
			sendInvalidArgument(sender, "invalid request")
			return
		}
		if !svc.requireAccessibleAgentID(sender, decoded.GetAgentId()) {
			return
		}
		fn(ctx, userID, decoded, sender)
	}
}

// registerAgentGatedByID registers a handler gated on the agent named by the
// request via a workspace_id-only lookup — no full-row load for a handler
// that only needs the authorization decision.
func registerAgentGatedByID[T any, PT agentScopedRequest[T]](
	r registrar,
	method string,
	fn func(ctx context.Context, userID string, req PT, sender *channel.Sender),
) {
	r.record(method, gateWorkspace)
	r.d.Register(method, agentGatedByIDHandler[T, PT](r.svc, fn))
}

// registerAgentGatedByIDTracked is RegisterTracked + registerAgentGatedByID.
func registerAgentGatedByIDTracked[T any, PT agentScopedRequest[T]](
	r registrar,
	method string,
	fn func(ctx context.Context, userID string, req PT, sender *channel.Sender),
) {
	r.record(method, gateWorkspace)
	r.d.RegisterTracked(method, agentGatedByIDHandler[T, PT](r.svc, fn))
}

// terminalGatedHandler builds the unmarshal → requireAccessibleTerminal → fn
// wrapper used by registerTerminalGated.
func terminalGatedHandler[T any, PT terminalScopedRequest[T]](
	svc *Context,
	fn func(ctx context.Context, userID string, req PT, row db.Terminal, sender *channel.Sender),
) channel.HandlerFunc {
	return func(ctx context.Context, userID string, req *leapmuxv1.InnerRpcRequest, sender *channel.Sender) {
		var msg T
		decoded := PT(&msg)
		if err := unmarshalRequest(req, decoded); err != nil {
			sendInvalidArgument(sender, "invalid request")
			return
		}
		row, ok := svc.requireAccessibleTerminal(sender, decoded.GetTerminalId())
		if !ok {
			return
		}
		fn(ctx, userID, decoded, row, sender)
	}
}

// registerTerminalGated registers a handler gated on the terminal row named
// by the request. fn receives the loaded row so the body never double-fetches.
func registerTerminalGated[T any, PT terminalScopedRequest[T]](
	r registrar,
	method string,
	fn func(ctx context.Context, userID string, req PT, row db.Terminal, sender *channel.Sender),
) {
	r.record(method, gateWorkspace)
	r.d.Register(method, terminalGatedHandler[T, PT](r.svc, fn))
}

// terminalGatedByIDHandler builds the unmarshal → requireAccessibleTerminalID
// → fn wrapper shared by registerTerminalGatedByID and its Tracked variant.
// Mirror of agentGatedByIDHandler: workspace_id-only lookup (no screen BLOB)
// for handlers that never read the row.
func terminalGatedByIDHandler[T any, PT terminalScopedRequest[T]](
	svc *Context,
	fn func(ctx context.Context, userID string, req PT, sender *channel.Sender),
) channel.HandlerFunc {
	return func(ctx context.Context, userID string, req *leapmuxv1.InnerRpcRequest, sender *channel.Sender) {
		var msg T
		decoded := PT(&msg)
		if err := unmarshalRequest(req, decoded); err != nil {
			sendInvalidArgument(sender, "invalid request")
			return
		}
		if !svc.requireAccessibleTerminalID(sender, decoded.GetTerminalId()) {
			return
		}
		fn(ctx, userID, decoded, sender)
	}
}

// registerTerminalGatedByID registers a handler gated on the terminal named
// by the request via a workspace_id-only lookup — no screen-BLOB load for a
// handler that only needs the authorization decision (SendInput and
// ResizeTerminal fire per keystroke / per resize).
func registerTerminalGatedByID[T any, PT terminalScopedRequest[T]](
	r registrar,
	method string,
	fn func(ctx context.Context, userID string, req PT, sender *channel.Sender),
) {
	r.record(method, gateWorkspace)
	r.d.Register(method, terminalGatedByIDHandler[T, PT](r.svc, fn))
}

// registerTerminalGatedByIDTracked is RegisterTracked + registerTerminalGatedByID.
func registerTerminalGatedByIDTracked[T any, PT terminalScopedRequest[T]](
	r registrar,
	method string,
	fn func(ctx context.Context, userID string, req PT, sender *channel.Sender),
) {
	r.record(method, gateWorkspace)
	r.d.RegisterTracked(method, terminalGatedByIDHandler[T, PT](r.svc, fn))
}

// registerTerminalForRestartGated is the sole user of
// db.GetTerminalForRestartRow: unmarshal → requireAccessibleTerminalForRestart
// → fn with the narrow row (metadata + length(screen), no screen BLOB).
func registerTerminalForRestartGated(
	r registrar,
	method string,
	fn func(ctx context.Context, userID string, req *leapmuxv1.RestartTerminalRequest, row db.GetTerminalForRestartRow, sender *channel.Sender),
) {
	r.record(method, gateWorkspace)
	r.d.Register(method, func(ctx context.Context, userID string, req *leapmuxv1.InnerRpcRequest, sender *channel.Sender) {
		var decoded leapmuxv1.RestartTerminalRequest
		if err := unmarshalRequest(req, &decoded); err != nil {
			sendInvalidArgument(sender, "invalid request")
			return
		}
		row, ok := r.svc.requireAccessibleTerminalForRestart(sender, decoded.GetTerminalId())
		if !ok {
			return
		}
		fn(ctx, userID, &decoded, row, sender)
	})
}

// registerInBodyGated records gateInBody and registers without wrapping.
// The handler keeps its heterogeneous in-body gate; completeness is enforced
// by TestAccessControl_GatedMethodProbesAreComplete.
func registerInBodyGated(r registrar, method string, handler channel.HandlerFunc) {
	r.record(method, gateInBody)
	r.d.Register(method, handler)
}

// registerInBodyGatedTracked is RegisterTracked + registerInBodyGated.
func registerInBodyGatedTracked(r registrar, method string, handler channel.HandlerFunc) {
	r.record(method, gateInBody)
	r.d.RegisterTracked(method, handler)
}

// registerSetFiltered records gateSetFilter and registers without wrapping.
// Denial is an empty result via AccessibleSet(), not PERMISSION_DENIED.
func registerSetFiltered(r registrar, method string, handler channel.HandlerFunc) {
	r.record(method, gateSetFilter)
	r.d.Register(method, handler)
}

// registerOwnerOnly records gateOwnerOnly and gates the handler on the caller
// being the worker's registered owner, reusing ownerOnlyRegistrar's gate. It
// exists for the handful of machine-scoped methods that live in a
// workspace-family file (ListAvailableShells / ListAvailableProviders, which
// enumerate installed shells and agent CLIs) rather than in one of the
// owner-only families registered wholesale through ownerOnlyRegistrar.
func registerOwnerOnly(r registrar, method string, handler channel.HandlerFunc) {
	ownerOnlyRegistrar{r: r}.Register(method, handler)
}

// registerUngated records gateNone and registers without wrapping. Reserved
// for a probe that does no work and discloses nothing (Ping's liveness check);
// anything that reads or enumerates machine state must use registerOwnerOnly.
func registerUngated(r registrar, method string, handler channel.HandlerFunc) {
	r.record(method, gateNone)
	r.d.Register(method, handler)
}
