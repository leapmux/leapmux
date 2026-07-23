package audit

// workerReachKind names HOW a worker-registry touch is authorized.
//
// Mirrors worker/service methodGate: every call anywhere in the repo to an
// UNGATED live-worker registry method must appear in workerReachSites, and
// TestRepoInvariants fails any call whose enclosing function is not classified.
//
// Which methods those are is NOT a hand-written list. registryMethodKinds below
// classifies every exported *workermgr.Manager method that touches the
// live-worker maps, the walk fails if one is missing from it, and the scanned
// set is derived from the entries marked registryUngatedByID. A fourth
// accessor added to the Manager therefore cannot be born unscanned: it fails
// the classification before any of its call sites exist.
//
// They are in scope because they all leak the same bit: touching any of them
// with an arbitrary worker id turns the offline/online split into a
// cross-tenant liveness oracle. The liveness probes disclose only that bit
// rather than a sendable connection, but it is the bit that matters.
type workerReachKind int

const (
	// reachEstablishedChan: the worker id comes from an already-authorized
	// channel record, not a user-supplied bare id.
	reachEstablishedChan workerReachKind = iota
	// reachServerInitiated: trusted server flow with no user in the path
	// (channel teardown / close dispatch).
	reachServerInitiated
	// reachStoreScoped: the worker id comes from a row the store already
	// filtered to the caller (Workers().GetOwned / ListByUserID), so the
	// ownership check ran in SQL rather than through
	// service.WorkerReachAuthorizer. Valid ONLY for a row loaded that way --
	// never for a bare id off the request.
	reachStoreScoped
)

// workerReachSites classifies every function IN THE REPOSITORY that reads the
// live worker registry. Additions are reviewed decisions: route a user-gated path
// through requireOnlineWorker, or extend this map with the matching kind and a
// comment justifying why that kind applies.
// Keys are directory- and receiver-qualified ("dir.(*Recv).Method") so neither
// two same-named methods on different types nor two same-named functions in
// different packages can share one classification. The directory rather than
// the package NAME is load-bearing: internal/hub/service and
// internal/worker/service are both `package service`, so a name-keyed table
// would let a worker-side function be born carrying a hub-side blessing.
//
// The scope is the whole repo, not one package. When this table lived in
// hub/service it could not see hub/notifier, which reads ConnForTrustedPath
// twice -- so the rule advertised a coverage it did not have. Those two calls
// are the entries at the bottom.
var workerReachSites = map[string]workerReachKind{
	"internal/hub/service.(*ChannelRelayHandler).relayFrontendMessageToWorker": reachEstablishedChan,
	"internal/hub/service.(*workerCloseDispatcher).enqueueChannelCloses":       reachServerInitiated,
	"internal/hub/service.(*workerCloseDispatcher).deliverWorkerCloses":        reachServerInitiated,
	// workerToProto publishes the online bit on rows its two callers loaded
	// via Workers().GetOwned and Workers().ListByUserID, both of which scope
	// to the caller's user id in SQL.
	"internal/hub/service.(*WorkerManagementService).workerToProto": reachStoreScoped,
	// The notifier's worker ids come from an authorized store row or a trusted
	// server flow (deregister, reconnect flush), never from a user request, and
	// it holds a 3-method narrow interface rather than *workermgr.Manager -- so
	// it structurally cannot reach Register / WaitFor* either.
	"internal/hub/notifier.(*Notifier).SendOrQueue":                 reachServerInitiated,
	"internal/hub/notifier.(*Notifier).ProcessPendingNotifications": reachServerInitiated,
	// The deregistering FLAG is reached by worker id and no authorizer runs, so
	// its writers are classified exactly like its readers. This one is entered
	// from DeregisterWorker, which has already matched the row on
	// (id, registered_by = caller) before the notifier is told anything.
	"internal/hub/notifier.(*Notifier).SendDeregister": reachServerInitiated,
}

// registryMethodKind names WHY one exported *workermgr.Manager method that
// touches the live-worker maps (conns, deregistering) needs -- or does not need
// -- its call sites classified in workerReachSites.
//
// The point of classifying the METHODS, not just their call sites, is that the
// scanned set stops being a hand-written list of three names. That list was
// itself the convention the net replaces: a fourth ungated accessor was scanned
// by nothing, and every call to it was born unclassified and green.
type registryMethodKind int

const (
	// registryUngatedByID: takes a worker id, touches the live-worker maps, and
	// runs no authorizer. EVERY call site must be classified in workerReachSites.
	// Reads and writes alike: marking someone else's worker deregistering is
	// reached by the same arbitrary id as probing whether it is online.
	registryUngatedByID registryMethodKind = iota
	// registryGated: runs the ReachAuthorizer itself, so the check cannot be
	// skipped by a caller and no call-site classification is needed.
	registryGated
	// registryConnScoped: mutates only on behalf of a caller that already holds
	// the *Conn. A bare worker id cannot reach it, so it is not an oracle.
	registryConnScoped
	// registryBroadcast: touches every connection and takes no worker id, so it
	// discloses nothing about any particular one.
	registryBroadcast
)

// registryMethodKinds classifies every exported *workermgr.Manager method whose
// body reads or writes conns / deregistering. The walk in TestRepoInvariants
// fails on a method missing from this map, AND on an entry whose kind the
// source contradicts -- a registryUngatedByID that calls the authorizer, a
// registryConnScoped that takes no *Conn, a registryBroadcast that takes a
// worker id. So the kind is a claim about the code, not a comment with a type.
var registryMethodKinds = map[string]registryMethodKind{
	"ConnForTrustedPath":   registryUngatedByID,
	"OnlineForTrustedPath": registryUngatedByID,
	"IsDeregistering":      registryUngatedByID,
	"MarkDeregistering":    registryUngatedByID,
	"ClearDeregistering":   registryUngatedByID,
	"ConnForUser":          registryGated,
	"Register":             registryConnScoped,
	"Unregister":           registryConnScoped,
	"NotifyShutdown":       registryBroadcast,
}
