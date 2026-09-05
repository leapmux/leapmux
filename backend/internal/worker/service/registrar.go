package service

import (
	"context"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/authscope"
	"github.com/leapmux/leapmux/internal/worker/channel"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"google.golang.org/grpc/codes"
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
	gateOwnerOnly methodGate = iota // only the worker's registrant may call
	gateNone                        // liveness probe that does no work, ungated by design
)

// The SCOPE a method requires is the second axis of every registration, and it
// is an ARGUMENT rather than a wrap inside the owner gate.
//
// The distinction is load-bearing. `gateNone` exists (Ping), so a check that
// lived inside the owner gate would be skippable by choosing registerUngated --
// which is precisely how a method ends up unenforced by accident. Passing the
// scope to register() means every registration states one, including an
// unguarded one, and a value nobody stated is the proto zero, which panics at boot.
//
// SCOPE_UNSPECIFIED PANICS, matching the duplicate-registration panic beside
// it: a worker that cannot say what a method requires must not start serving
// it, because the alternative is a method that quietly reaches every app.

// registrar wraps a dispatcher and records each method's gate kind.
//
// It is passed by value; the maps are shared. register() panics on a
// duplicate method — Dispatcher.Register silently overwrites, and without
// the panic the disjointness check that TestEveryRegisteredMethodIsClassified
// previously enforced by hand would be lost.
type registrar struct {
	d      *channel.Dispatcher
	svc    *Service
	gates  map[string]methodGate
	scopes map[string]leapmuxv1.Scope
	shapes map[string]methodShape
}

// methodShape states how a method ANSWERS, as methodGate states how it is
// guarded.
//
// Reply shape needs recording for the same reason access does. A method
// that answers with stream frames but is registered through a unary
// helper still compiles, still passes its tests, and fails only at
// runtime in the one way nothing reports: the browser holds the
// correlation id as a stream, so a unary error frame is dropped on
// arrival and the caller waits on a subscription that will never carry
// anything and never errors.
//
// WatchWorkerPrivateEvents shipped exactly that way. The gate table
// already turned "did the author remember to guard this?" into a test
// failure; this turns "did the author remember RegisterStream?" into one.
type methodShape int

const (
	shapeUnary  methodShape = iota // answers with an InnerRpcResponse
	shapeStream                    // answers with InnerStreamMessage frames
)

func newRegistrar(d *channel.Dispatcher, svc *Service) registrar {
	return registrar{
		d:      d,
		svc:    svc,
		gates:  make(map[string]methodGate),
		scopes: make(map[string]leapmuxv1.Scope),
		shapes: make(map[string]methodShape),
	}
}

// dispatchMode selects which Dispatcher entry point a registration uses.
//
// It is the second axis of every registration, crossing the gate kind
// above. Passing it lets the two vary independently instead of being
// enumerated as one named helper per combination, so a new pairing --
// streaming + tracked, say -- costs an argument rather than another
// exported function.
//
// Each guarded helper therefore TAKES this rather than existing twice. It
// used to be enumerated after all: three hand-written `*Tracked` twins sat
// beside their siblings differing by one method call, which is exactly what
// the paragraph above says the design avoids.
//
// dispatchStreaming is not reachable through that argument. The streaming
// helpers call RegisterStream directly, because a streaming registration must
// apply the stream-frame denial (gateStream) rather than the unary one -- and
// because TestNoUnaryHandlerSendsStreamFrames identifies streaming registrations
// by the helper NAME, so a mode-parameterised streaming call would be scanned as
// unary and its SendStream flagged.
type dispatchMode int

const (
	dispatchPlain     dispatchMode = iota // Dispatcher.Register
	dispatchTracked                       // Dispatcher.RegisterTracked
	dispatchStreaming                     // Dispatcher.RegisterStream
)

// register records a method's gate and reply shape, then wires it through
// the dispatcher entry point mode names.
//
// Deriving the shape from the mode is the point: they were previously two
// independent lines at each call site, so a handler could be registered
// through RegisterStream while recording shapeUnary (or the reverse) and
// nothing would notice -- which is precisely the class of mistake the
// shape table exists to catch.
func (r registrar) register(method string, gate methodGate, scope leapmuxv1.Scope, mode dispatchMode, handler channel.HandlerFunc) {
	// Dispatcher.Register silently overwrites, so a duplicate would
	// otherwise replace a handler with no sign that anything happened.
	if _, exists := r.gates[method]; exists {
		panic("duplicate method registration: " + method)
	}
	if !authscope.IsGrantable(scope) {
		panic("method " + method + " states no grantable scope: " + scope.String())
	}
	shape := shapeUnary
	if mode == dispatchStreaming {
		shape = shapeStream
	}
	r.gates[method] = gate
	r.scopes[method] = scope
	r.shapes[method] = shape

	guarded := r.scopeGuard(method, scope, mode, handler)
	switch mode {
	case dispatchTracked:
		r.d.RegisterTracked(method, guarded)
	case dispatchStreaming:
		r.d.RegisterStream(method, guarded)
	case dispatchPlain:
		r.d.Register(method, guarded)
	}
}

// scopeGuard wraps a handler with its scope check.
//
// It runs BEFORE the owner gate, and the order is the same rule the Hub's
// interceptor follows: an app is refused for its own grant rather than told
// whether the caller happens to be the worker's registrant.
//
// The denial ENCODING follows the reply shape, exactly as the owner gate's
// does: a streaming method answers with a stream frame, because a unary
// InnerRpcResponse on a correlation id the client holds as a stream is dropped
// on arrival and leaves the caller waiting on something that never errors.
func (r registrar) scopeGuard(method string, scope leapmuxv1.Scope, mode dispatchMode, handler channel.HandlerFunc) channel.HandlerFunc {
	streaming := mode == dispatchStreaming
	return func(ctx context.Context, caller channel.Caller, req *leapmuxv1.InnerRpcRequest, sender channel.ResponseWriter) {
		if !caller.Allows(scope) {
			msg := authscope.NotGrantedDenial(scope)
			if streaming {
				sendStreamError(sender, codes.PermissionDenied, msg)
			} else {
				sendPermissionDenied(sender, msg)
			}
			return
		}
		handler(ctx, caller, req, sender)
	}
}

// decodedRequest constrains a request message to a proto.Message pointer whose
// zero value the helper can allocate itself, so a handler never restates the
// unmarshal — same idiom as connRequest / registerConnHandler in tunnel.go.
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

// registerOwnerGated registers an owner-guarded handler for a decoded request.
// Wrapper: owner gate → unmarshal → INVALID_ARGUMENT "invalid request" → fn
// with the decoded request. The gate is a property of WHERE the handler is
// registered.
func registerOwnerGated[T any, PT decodedRequest[T]](
	r registrar,
	method string,
	scope leapmuxv1.Scope,
	mode dispatchMode,
	fn func(ctx context.Context, caller channel.Caller, req PT, sender channel.ResponseWriter),
) {
	r.ownerOnly().RegisterMode(method, scope, mode, decodeInto[T, PT](fn))
}

// decodeInto builds the unmarshal → fn wrapper shared by the owner-guarded
// helpers. A decode failure answers InvalidArgument and never reaches fn.
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
// The registrars call it rather than the handlers, so a new write handler is
// refused by construction. The old per-handler guards missed write paths such
// as RenameAgent, queue mutations, UpdateTerminalTitle, and both closes.
//
// It keys on the SCOPE, not on the method, because the scope already states
// whether a handler mutates. A read handler stays reachable, which is what
// makes an archived workspace browsable rather than merely frozen.
//
// OpenAgent and OpenTerminal are deliberately NOT covered, and cannot be: they
// create the row this guard reads, and a Worker stores no workspace id (see
// OpenAgentRequest in agent.proto). The reconciler applies the Hub's
// authoritative state to such a row on its next pass.
func refuseArchivedWrite(sender channel.ResponseWriter, scope leapmuxv1.Scope, archived int64, subject string) bool {
	if archived == 0 {
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

// agentGatedHandler builds the unmarshal → requireAgent → fn wrapper used by
// registerAgentGated. Explicit type args sidestep constraint-inference edge
// cases in nested generic calls (mirroring Dispatcher / ownerOnlyRegistrar
// style).
func agentGatedHandler[T any, PT agentScopedRequest[T]](
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

// registerAgentGated registers a handler for a request naming an agent. fn
// receives the loaded row so the body never double-fetches.
func registerAgentGated[T any, PT agentScopedRequest[T]](
	r registrar,
	method string,
	scope leapmuxv1.Scope,
	fn func(ctx context.Context, caller channel.Caller, req PT, row db.Agent, sender channel.ResponseWriter),
) {
	r.ownerOnly().Register(method, scope, agentGatedHandler[T, PT](r.svc, scope, fn))
}

// agentGatedByIDHandler builds the unmarshal → requireAgentID → fn wrapper
// shared by registerAgentGatedByID and registerAgentGatedByIDTracked. The
// existence probe reads only the id column, so these are for handlers that
// never read the row; ones that do use registerAgentGated instead and receive
// the loaded row.
func agentGatedByIDHandler[T any, PT agentScopedRequest[T]](
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

// registerAgentGatedByID registers a handler for a request naming an agent,
// resolved through an id-only existence probe — no full-row load for a handler
// that only needs "does this agent exist?".
func registerAgentGatedByID[T any, PT agentScopedRequest[T]](
	r registrar,
	method string,
	scope leapmuxv1.Scope,
	mode dispatchMode,
	fn func(ctx context.Context, caller channel.Caller, req PT, sender channel.ResponseWriter),
) {
	r.ownerOnly().RegisterMode(method, scope, mode, agentGatedByIDHandler[T, PT](r.svc, scope, fn))
}

// terminalGatedHandler builds the unmarshal → requireTerminal → fn wrapper
// used by registerTerminalGated.
func terminalGatedHandler[T any, PT terminalScopedRequest[T]](
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

// registerTerminalGated registers a handler for a request naming a terminal.
// fn receives the loaded row so the body never double-fetches.
func registerTerminalGated[T any, PT terminalScopedRequest[T]](
	r registrar,
	method string,
	scope leapmuxv1.Scope,
	fn func(ctx context.Context, caller channel.Caller, req PT, row db.Terminal, sender channel.ResponseWriter),
) {
	r.ownerOnly().Register(method, scope, terminalGatedHandler[T, PT](r.svc, scope, fn))
}

// terminalGatedByIDHandler builds the unmarshal → requireTerminalID → fn
// wrapper shared by registerTerminalGatedByID and its Tracked variant. Mirror
// of agentGatedByIDHandler: id-only existence probe (no screen BLOB) for
// handlers that never read the row.
func terminalGatedByIDHandler[T any, PT terminalScopedRequest[T]](
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

// registerTerminalGatedByID registers a handler for a request naming a
// terminal, resolved through an id-only existence probe — no screen-BLOB load
// for a handler that only needs "does this terminal exist?" (SendInput and
// ResizeTerminal fire per keystroke / per resize).
func registerTerminalGatedByID[T any, PT terminalScopedRequest[T]](
	r registrar,
	method string,
	scope leapmuxv1.Scope,
	mode dispatchMode,
	fn func(ctx context.Context, caller channel.Caller, req PT, sender channel.ResponseWriter),
) {
	r.ownerOnly().RegisterMode(method, scope, mode, terminalGatedByIDHandler[T, PT](r.svc, scope, fn))
}

// registerTerminalForRestartGated is the sole user of
// db.GetTerminalForRestartRow: unmarshal → requireTerminalForRestart → fn with
// the narrow row (metadata + length(screen), no screen BLOB).
func registerTerminalForRestartGated(
	r registrar,
	method string,
	scope leapmuxv1.Scope,
	fn func(ctx context.Context, caller channel.Caller, req *leapmuxv1.RestartTerminalRequest, row db.GetTerminalForRestartRow, sender channel.ResponseWriter),
) {
	registerOwnerGated(r, method, scope, dispatchPlain, func(ctx context.Context, caller channel.Caller, decoded *leapmuxv1.RestartTerminalRequest, sender channel.ResponseWriter) {
		row, ok := r.svc.requireTerminalForRestart(sender, decoded.GetTerminalId())
		if !ok {
			return
		}
		if refuseArchivedWrite(sender, scope, row.WorkspaceArchived, "terminal") {
			return
		}
		fn(ctx, caller, decoded, row, sender)
	})
}

// registerOwnerGatedStream is registerOwnerGated for a method that answers
// with stream frames.
//
// It differs in both halves that decide reply shape, because both matter and
// they are easy to fix by halves. Registering through RegisterStream makes the
// DISPATCHER report a panic as a stream frame; routing this wrapper's own
// rejections through sendStreamError makes the GATE do the same.
//
// A unary frame is not simply lost -- the browser routes a unary ERROR on a
// stream correlation id to the listener's onError (see channelRpc's
// deliverResponse), and the CLI's stream transport treats it as terminal. The
// reason to answer in-band anyway is that only a stream frame carries `End`, so
// a receiver can terminate on it generically instead of special-casing two reply
// shapes per method; and a unary NON-error payload on a stream id genuinely is
// dropped, so the two halves must agree or the shape depends on which failure
// occurred.
func registerOwnerGatedStream[T any, PT decodedRequest[T]](
	r registrar,
	method string,
	scope leapmuxv1.Scope,
	fn func(ctx context.Context, caller channel.Caller, req PT, sender channel.ResponseWriter),
) {
	r.ownerOnly().RegisterStream(method, scope, func(ctx context.Context, caller channel.Caller, req *leapmuxv1.InnerRpcRequest, sender channel.ResponseWriter) {
		var msg T
		decoded := PT(&msg)
		if err := unmarshalRequest(req, decoded); err != nil {
			sendStreamError(sender, codes.InvalidArgument, "invalid request")
			return
		}
		fn(ctx, caller, decoded, sender)
	})
}

// registerOwnerGatedStreamRaw is registerOwnerGatedStream for a handler that
// owns its own unmarshal (WatchEvents decodes a request whose per-entity
// rejections it reports in-band).
func registerOwnerGatedStreamRaw(r registrar, method string, scope leapmuxv1.Scope, handler channel.HandlerFunc) {
	r.ownerOnly().RegisterStream(method, scope, handler)
}

// registerOwnerOnly records gateOwnerOnly and restricts the handler to the
// worker's registered owner, reusing ownerOnlyRegistrar's gate. It exists
// for methods that own their own unmarshal (the capability probes
// ListAvailableShells / ListAvailableProviders) rather than being registered
// wholesale through ownerOnlyRegistrar as a family.
func registerOwnerOnly(r registrar, method string, scope leapmuxv1.Scope, handler channel.HandlerFunc) {
	r.ownerOnly().Register(method, scope, handler)
}

// ownerOnly is the single constructor for the owner gate, so the
// `ownerOnlyRegistrar{r: r}` literal appears once instead of at every family
// registration.
func (r registrar) ownerOnly() ownerOnlyRegistrar {
	return ownerOnlyRegistrar{r: r}
}

// registerUngated records gateNone and registers without wrapping. Reserved
// for a probe that does no work and discloses nothing (Ping's liveness check);
// anything that reads or enumerates machine state must use registerOwnerOnly.
func registerUngated(r registrar, method string, scope leapmuxv1.Scope, handler channel.HandlerFunc) {
	r.register(method, gateNone, scope, dispatchPlain, handler)
}
