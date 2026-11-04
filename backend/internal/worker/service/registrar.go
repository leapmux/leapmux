package service

import (
	"context"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/authscope"
	"github.com/leapmux/leapmux/internal/rpcaccess"
	"github.com/leapmux/leapmux/internal/worker/channel"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"google.golang.org/protobuf/proto"
)

// methodGate names HOW a method's access is enforced.
//
// Every method RegisterAll wires must record exactly one gate via the
// registrar helpers (or ownerOnlyRegistrar). A method with no recorded
// gate fails TestEveryRegisteredMethodIsClassified; a duplicate
// registration panics in registrar.register. The gate is a property of
// WHERE the handler is registered rather than a line each author must
// remember.
//
// There are only two values, and that is the point. A Worker serves exactly
// one user and stores no workspace id, so there is no sub-owner axis left to
// narrow on: every row it holds belongs to its registrant. What the table
// still buys is that `gateNone` cannot spread by omission -- an ungated method
// is a deliberate entry, not a forgotten wrapper.
type methodGate int

const (
	guardOwnerOnly methodGate = iota // only the worker's registrant may call
	gateNone                         // liveness probe that does no work, ungated by design
)

// The SCOPE a method requires and the SHAPE it answers in are declared in the
// proto, on the method's (leapmux.v1.access) option and in its signature (see
// proto/leapmux/v1/access.proto). The registrar reads both from the descriptor
// index (internal/rpcaccess) and takes neither as an argument, so a
// registration cannot state a scope or a reply shape that disagrees with the
// declaration.
//
// The scope check is NOT a wrap inside the owner gate, and the distinction is
// load-bearing. `gateNone` exists (Ping), so a check that lived inside the
// owner gate would be skippable by choosing registerUnguarded -- which is
// precisely how a method ends up unenforced by accident. register() applies the
// scope to every registration, including an unguarded one.
//
// A method the proto does not declare, declares on a non-WORKER service, or
// declares without a grantable scope PANICS at registration, matching the
// duplicate-registration panic beside it: a worker that cannot say what a
// method requires must not start serving it, because the alternative is a
// method that quietly reaches every app.

// registrar wraps a dispatcher and records each method's gate kind.
//
// It is passed by value; the gate map is shared. register() panics on a
// duplicate method, and so does the dispatcher.
type registrar struct {
	d   *channel.Dispatcher
	svc *Service
	// lookup resolves a procedure path to its declaration. Production uses
	// rpcaccess.Lookup; a test substitutes one that also declares test-only
	// procedures.
	lookup func(procedure string) (rpcaccess.Method, bool)
	gates  map[string]methodGate
}

func newRegistrar(d *channel.Dispatcher, svc *Service) registrar {
	return newRegistrarWithLookup(d, svc, rpcaccess.Lookup)
}

func newRegistrarWithLookup(d *channel.Dispatcher, svc *Service, lookup func(string) (rpcaccess.Method, bool)) registrar {
	return registrar{
		d:      d,
		svc:    svc,
		lookup: lookup,
		gates:  make(map[string]methodGate),
	}
}

// dispatchMode selects whether Shutdown waits for in-flight invocations of a
// method. It is the one registration property the proto cannot state.
type dispatchMode int

const (
	dispatchPlain   dispatchMode = iota // fire-and-forget on Shutdown
	dispatchTracked                     // Shutdown waits for in-flight calls
)

// declaration is what the proto states about one worker method.
//
// streaming decides the reply shape of the method's dispatcher registration, and
// the dispatcher then answers every SendError of the method in that shape (see
// channel.Registration). A method that answers in the wrong shape still compiles
// and passes every test that checks only the code of its refusals, so the shape
// comes from the declaration and never from an argument of the registration.
type declaration struct {
	scope     leapmuxv1.Scope
	streaming bool
}

// declared returns a worker method's declaration, and panics when the method
// cannot be served: not declared, declared on a service whose host is not
// WORKER, or declared without a grantable scope.
func (r registrar) declared(procedure string) declaration {
	m, ok := r.lookup(procedure)
	if !ok {
		panic("worker method " + procedure + " is not declared by any leapmux.v1 service")
	}
	if m.Host != leapmuxv1.ServiceHost_SERVICE_HOST_WORKER {
		panic("worker method " + procedure + " belongs to a " + m.Host.String() + " service, not a SERVICE_HOST_WORKER one")
	}
	req, ok := m.Access.GetRequirement().(*leapmuxv1.MethodAccess_Scope)
	if !ok || !authscope.IsGrantable(req.Scope) {
		panic("worker method " + procedure + " states no grantable scope in its (leapmux.v1.access) option")
	}
	return declaration{scope: req.Scope, streaming: m.ServerStreaming}
}

// register records a method's gate, applies its scope check, and wires it into
// the dispatcher with the reply shape the proto declares.
func (r registrar) register(procedure string, gate methodGate, mode dispatchMode, handler channel.HandlerFunc) {
	// The dispatcher panics on a duplicate too; checking here first keeps the
	// gate map from recording a second entry for the same method.
	if _, exists := r.gates[procedure]; exists {
		panic("duplicate method registration: " + procedure)
	}
	decl := r.declared(procedure)
	r.gates[procedure] = gate

	r.d.Handle(procedure, channel.Registration{
		Tracked:   mode == dispatchTracked,
		Streaming: decl.streaming,
	}, scopeGuard(decl.scope, handler))
}

// scopeGuard wraps a handler with its scope check.
//
// It runs BEFORE the owner gate, and the order is the same rule the Hub's
// interceptor follows: an app is refused for its own grant rather than told
// whether the caller happens to be the worker's registrant.
func scopeGuard(scope leapmuxv1.Scope, handler channel.HandlerFunc) channel.HandlerFunc {
	return func(ctx context.Context, caller channel.Caller, req *leapmuxv1.InnerRpcRequest, sender channel.ResponseWriter) {
		if refuseUngranted(sender, caller, scope) {
			return
		}
		handler(ctx, caller, req, sender)
	}
}

// refuseUngranted answers PermissionDenied unless the caller's grant holds
// scope, and reports whether it answered. The denial states the scope in the
// words authscope.NotGrantedDenial gives, so the method-level check and a
// handler's own check (requireCloseScopes) read the same.
func refuseUngranted(sender channel.ResponseWriter, caller channel.Caller, scope leapmuxv1.Scope) bool {
	if caller.Allows(scope) {
		return false
	}
	sendPermissionDenied(sender, authscope.NotGrantedDenial(scope))
	return true
}

// decodedRequest constrains a request message to a proto.Message pointer whose
// zero value the helper can allocate itself, so a handler never restates the
// unmarshal. agentScopedRequest, terminalScopedRequest and connRequest
// (tunnel.go) narrow it with the id each guard reads.
type decodedRequest[T any] interface {
	*T
	proto.Message
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

// registerOwnerGuarded registers an owner-guarded handler for a decoded request.
// Wrapper: owner gate → unmarshal → INVALID_ARGUMENT "invalid request" → fn
// with the decoded request. The gate is a property of WHERE the handler is
// registered, and the dispatcher answers every refusal in the shape the method
// declares.
//
// It registers every owner-only method that decodes its request and loads no
// row first.
func registerOwnerGuarded[T any, PT decodedRequest[T]](
	o ownerOnlyRegistrar,
	procedure string,
	mode dispatchMode,
	fn func(ctx context.Context, caller channel.Caller, req PT, sender channel.ResponseWriter),
) {
	o.RegisterMode(procedure, mode, decodeInto[T, PT](fn))
}

// decodeInto builds the unmarshal → fn wrapper shared by the owner-guarded
// helpers. A decode failure answers InvalidArgument and never reaches fn.
//
// It is the one caller of unmarshalRequest in this package
// (TestOnlyDecodeIntoUnmarshalsARequest), so the decode refusal exists once.
func decodeInto[T any, PT decodedRequest[T]](
	fn func(ctx context.Context, caller channel.Caller, req PT, sender channel.ResponseWriter),
) channel.HandlerFunc {
	return func(ctx context.Context, caller channel.Caller, req *leapmuxv1.InnerRpcRequest, sender channel.ResponseWriter) {
		var msg T
		decoded := PT(&msg)
		if err := unmarshalRequest(req, decoded); err != nil {
			sendInvalidArgument(sender, "invalid request")
			return
		}
		fn(ctx, caller, decoded, sender)
	}
}

// refuseArchivedWrite answers a write RPC that names a row in an archived
// workspace, and reports whether it answered. Read it as the one place the rule
// "an archived workspace takes no mutation" lives on this side of the wire.
//
// The row-guard registrars call it rather than the handlers, so a new write
// handler that one of them registers is refused by construction. The old
// per-handler guards missed write paths such as RenameAgent, queue mutations,
// RenameTerminal, and the agent and terminal closes.
//
// handleCloseTab is the one handler that calls it by hand. CloseTab requires
// only worker:read, because the scope a close needs depends on the tab type, so
// no registrar can apply the guard for it. Each case of its tab-type switch
// applies the guard itself, and a new case must do the same.
//
// It keys on the SCOPE, not on the method, because the scope already states
// whether a handler mutates. A read handler stays reachable, which is what
// makes an archived workspace browsable rather than merely frozen.
//
// OpenAgent and OpenTerminal are deliberately NOT covered, and cannot be: they
// create the row this guard reads, and a Worker stores no workspace id (see
// OpenAgentRequest in agent.proto). The reconciler applies the Hub's
// authoritative state to such a row on its next pass.
func refuseArchivedWrite(sender channel.ResponseWriter, scope leapmuxv1.Scope, archived bool, subject string) bool {
	if !archived {
		return false
	}
	switch scope {
	case leapmuxv1.Scope_SCOPE_AGENT_WRITE, leapmuxv1.Scope_SCOPE_TERMINAL_WRITE:
		sendFailedPrecondition(sender, subject+" belongs to an archived workspace")
		return true
	default:
		return false
	}
}

// agentGuardedHandler builds the unmarshal → requireAgent → fn wrapper used by
// registerAgentGuarded. The helpers pass explicit type arguments to it and to
// decodeInto, which sidesteps constraint-inference edge cases in nested generic
// calls.
func agentGuardedHandler[T any, PT agentScopedRequest[T]](
	svc *Service,
	scope leapmuxv1.Scope,
	fn func(ctx context.Context, caller channel.Caller, req PT, row db.Agent, sender channel.ResponseWriter),
) channel.HandlerFunc {
	return decodeInto[T, PT](func(ctx context.Context, caller channel.Caller, decoded PT, sender channel.ResponseWriter) {
		row, ok := svc.requireAgent(sender, decoded.GetAgentId())
		if !ok {
			return
		}
		if refuseArchivedWrite(sender, scope, row.WorkspaceArchived, "agent") {
			return
		}
		fn(ctx, caller, decoded, row, sender)
	})
}

// registerAgentGuarded registers a handler for a request that carries an agent id. fn
// receives the loaded row so the body never double-fetches.
func registerAgentGuarded[T any, PT agentScopedRequest[T]](
	o ownerOnlyRegistrar,
	procedure string,
	fn func(ctx context.Context, caller channel.Caller, req PT, row db.Agent, sender channel.ResponseWriter),
) {
	scope := o.r.declared(procedure).scope
	o.Register(procedure, agentGuardedHandler[T, PT](o.r.svc, scope, fn))
}

// agentGuardedByIDHandler builds the unmarshal → requireAgentID → fn wrapper
// used by registerAgentGuardedByID. The existence probe reads only the id
// column, so it is for handlers that never read the row; ones that do use
// registerAgentGuarded instead and receive the loaded row.
func agentGuardedByIDHandler[T any, PT agentScopedRequest[T]](
	svc *Service,
	scope leapmuxv1.Scope,
	fn func(ctx context.Context, caller channel.Caller, req PT, sender channel.ResponseWriter),
) channel.HandlerFunc {
	return decodeInto[T, PT](func(ctx context.Context, caller channel.Caller, decoded PT, sender channel.ResponseWriter) {
		archived, ok := svc.requireAgentID(sender, decoded.GetAgentId())
		if !ok {
			return
		}
		if refuseArchivedWrite(sender, scope, archived, "agent") {
			return
		}
		fn(ctx, caller, decoded, sender)
	})
}

// registerAgentGuardedByID registers a handler for a request that carries an agent id,
// resolved through an id-only existence probe — no full-row load for a handler
// that only needs "does this agent exist?".
func registerAgentGuardedByID[T any, PT agentScopedRequest[T]](
	o ownerOnlyRegistrar,
	procedure string,
	mode dispatchMode,
	fn func(ctx context.Context, caller channel.Caller, req PT, sender channel.ResponseWriter),
) {
	scope := o.r.declared(procedure).scope
	o.RegisterMode(procedure, mode, agentGuardedByIDHandler[T, PT](o.r.svc, scope, fn))
}

// terminalGuardedHandler builds the unmarshal → requireTerminal → fn wrapper
// used by registerTerminalGuarded.
func terminalGuardedHandler[T any, PT terminalScopedRequest[T]](
	svc *Service,
	scope leapmuxv1.Scope,
	fn func(ctx context.Context, caller channel.Caller, req PT, row db.Terminal, sender channel.ResponseWriter),
) channel.HandlerFunc {
	return decodeInto[T, PT](func(ctx context.Context, caller channel.Caller, decoded PT, sender channel.ResponseWriter) {
		row, ok := svc.requireTerminal(sender, decoded.GetTerminalId())
		if !ok {
			return
		}
		if refuseArchivedWrite(sender, scope, row.WorkspaceArchived, "terminal") {
			return
		}
		fn(ctx, caller, decoded, row, sender)
	})
}

// registerTerminalGuarded registers a handler for a request naming a terminal.
// fn receives the loaded row so the body never double-fetches.
func registerTerminalGuarded[T any, PT terminalScopedRequest[T]](
	o ownerOnlyRegistrar,
	procedure string,
	fn func(ctx context.Context, caller channel.Caller, req PT, row db.Terminal, sender channel.ResponseWriter),
) {
	scope := o.r.declared(procedure).scope
	o.Register(procedure, terminalGuardedHandler[T, PT](o.r.svc, scope, fn))
}

// terminalGuardedByIDHandler builds the unmarshal → requireTerminalID → fn
// wrapper used by registerTerminalGuardedByID. Mirror of
// agentGuardedByIDHandler: id-only existence probe (no screen BLOB) for
// handlers that never read the row.
func terminalGuardedByIDHandler[T any, PT terminalScopedRequest[T]](
	svc *Service,
	scope leapmuxv1.Scope,
	fn func(ctx context.Context, caller channel.Caller, req PT, sender channel.ResponseWriter),
) channel.HandlerFunc {
	return decodeInto[T, PT](func(ctx context.Context, caller channel.Caller, decoded PT, sender channel.ResponseWriter) {
		archived, ok := svc.requireTerminalID(sender, decoded.GetTerminalId())
		if !ok {
			return
		}
		if refuseArchivedWrite(sender, scope, archived, "terminal") {
			return
		}
		fn(ctx, caller, decoded, sender)
	})
}

// registerTerminalGuardedByID registers a handler for a request that identifies
// a terminal, resolved through an id-only existence probe — no screen-BLOB load
// for a handler that only needs "does this terminal exist?" (SendInput and
// ResizeTerminal fire per keystroke / per resize).
func registerTerminalGuardedByID[T any, PT terminalScopedRequest[T]](
	o ownerOnlyRegistrar,
	procedure string,
	mode dispatchMode,
	fn func(ctx context.Context, caller channel.Caller, req PT, sender channel.ResponseWriter),
) {
	scope := o.r.declared(procedure).scope
	o.RegisterMode(procedure, mode, terminalGuardedByIDHandler[T, PT](o.r.svc, scope, fn))
}

// registerTerminalForRestartGuarded is the sole user of
// db.GetTerminalForRestartRow: unmarshal → requireTerminalForRestart → fn with
// the narrow row (metadata + length(screen), no screen BLOB).
func registerTerminalForRestartGuarded(
	o ownerOnlyRegistrar,
	procedure string,
	fn func(ctx context.Context, caller channel.Caller, req *leapmuxv1.RestartTerminalRequest, row db.GetTerminalForRestartRow, sender channel.ResponseWriter),
) {
	scope := o.r.declared(procedure).scope
	o.Register(procedure, decodeInto[leapmuxv1.RestartTerminalRequest, *leapmuxv1.RestartTerminalRequest](func(ctx context.Context, caller channel.Caller, decoded *leapmuxv1.RestartTerminalRequest, sender channel.ResponseWriter) {
		row, ok := o.r.svc.requireTerminalForRestart(sender, decoded.GetTerminalId())
		if !ok {
			return
		}
		if refuseArchivedWrite(sender, scope, row.WorkspaceArchived, "terminal") {
			return
		}
		fn(ctx, caller, decoded, row, sender)
	}))
}

// ownerOnly is the single constructor for the owner gate, so the
// `ownerOnlyRegistrar{r: r}` literal appears once instead of at every family
// registration.
func (r registrar) ownerOnly() ownerOnlyRegistrar {
	return ownerOnlyRegistrar{r: r}
}

// registerUnguarded records gateNone and registers without wrapping. Reserved
// for a probe that does no work and discloses nothing (Ping's liveness check);
// anything that reads or enumerates machine state must register through
// ownerOnlyRegistrar (registerOwnerGuarded, or a row guard above).
func registerUnguarded(r registrar, procedure string, handler channel.HandlerFunc) {
	r.register(procedure, gateNone, dispatchPlain, handler)
}
