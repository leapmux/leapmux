package service

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connectproto"
	"github.com/coder/quartz"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/channelmgr"
	"github.com/leapmux/leapmux/internal/hub/crdt"
	"github.com/leapmux/leapmux/internal/hub/notifier"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/workermgr"
	"github.com/leapmux/leapmux/internal/metrics"
	"github.com/leapmux/leapmux/internal/sendq"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/workerkeys"
)

// CRDTRegistry is the subset of *crdt.Registry that
// WorkerConnectorService consumes — used by the worker tab-sync path
// to fetch a manager and submit hub-internal tombstones for tabs the
// worker no longer hosts, and by Register to tell the registrant's
// /ws/userevents subscribers that its worker list changed. Modeled as an
// interface so tests can pass a nil-equivalent stub and so the wiring in
// hub/server.go doesn't require constructing the full registry before this
// service is reachable.
type CRDTRegistry interface {
	Get(ctx context.Context, userID string) (*crdt.Manager, error)
	// BroadcastWorkersChanged reaches the user's live subscribers whose
	// credential holds worker:read, through an already-loaded manager, and
	// never bootstraps one.
	BroadcastWorkersChanged(userID string)
}

type WorkerConnectorService struct {
	store        store.Store
	workerMgr    *workermgr.Manager
	channelMgr   *channelmgr.Manager
	pending      *workermgr.PendingRequests
	notifier     *notifier.Notifier
	crdtRegistry CRDTRegistry
	// clock drives the hello deadline and the receive-idle timer of every
	// Connect stream, so a test can move both deadlines instead of waiting.
	clock quartz.Clock
	// identityDeliveries orders every WorkerIdentity frame, the greeting
	// included; see ownerDeliveryLocks.
	identityDeliveries ownerDeliveryLocks
	// queuePool is the WORKER outbound queue budget every Connect stream's
	// writer draws from. Deliberately not shared with the frontend relays:
	// reclaiming can only ever cost a member of the same pool, and dropping a
	// worker discards every user's channels on that machine where dropping a
	// tab costs a reconnect. See sendq.Pool on membership as a blast radius.
	queuePool *sendq.Pool
	// challenges holds the outstanding registration-recovery
	// challenges. In memory only: a Hub restart invalidates them, and a
	// client obtains a fresh one through its durable attempt record.
	challenges *registrationChallengeStore
	// deregisterEffects runs the out-of-database half of the worker's
	// self-deregistration, exactly as every other removal surface runs
	// it. Nil in unit tests that exercise only the store half.
	deregisterEffects *WorkerDeregisterEffects
	// maxWorkersPerUser bounds how many ACTIVE worker rows one account may hold,
	// so a machine is turned away at registration -- where the operator can be
	// told which key to raise -- rather than at its first Connect. Zero is
	// unlimited.
	//
	// It does NOT bound queuePool membership, and must not be read as if it did:
	// the pool member is created per Connect stream, behind an auth-token lookup
	// that admits a DEREGISTERING worker (that stream is how the worker is told
	// to tear itself down) while ListByUserID no longer counts it. Registering,
	// deregistering and registering again would therefore add members without
	// ever exceeding this. The bound on live membership is its twin,
	// workermgr.Manager.SetMaxWorkersPerUser, which counts in the same critical
	// section that publishes the connection.
	maxWorkersPerUser atomic.Int64
}

// NewWorkerConnectorService creates a new WorkerConnectorService.
// `registry` may be nil in unit tests; production deployments wire in
// the user-CRDT registry so worker tab-sync can drive manager-side
// tombstones for orphaned tabs the worker no longer hosts.
func NewWorkerConnectorService(
	st store.Store,
	mgr *workermgr.Manager,
	cMgr *channelmgr.Manager,
	pr *workermgr.PendingRequests,
	n *notifier.Notifier,
	registry CRDTRegistry,
	queuePool *sendq.Pool,
	clock quartz.Clock,
) *WorkerConnectorService {
	if queuePool == nil {
		panic("service: NewWorkerConnectorService requires a queue pool")
	}
	if clock == nil {
		panic("service: NewWorkerConnectorService requires a clock")
	}
	return &WorkerConnectorService{
		store:        st,
		workerMgr:    mgr,
		channelMgr:   cMgr,
		pending:      pr,
		notifier:     n,
		crdtRegistry: registry,
		queuePool:    queuePool,
		clock:        clock,
		challenges:   newRegistrationChallengeStore(clock),
	}
}

// SetDeregisterEffects binds the out-of-database deregistration
// effects. Production wires the same instance every other removal
// surface uses; the setter keeps the constructor stable for the test
// environments that exercise only one half.
func (s *WorkerConnectorService) SetDeregisterEffects(effects *WorkerDeregisterEffects) {
	s.deregisterEffects = effects
}

// workerIdleTimeout is how long a Connect stream may stay silent: before the
// worker's hello arrives, and between two messages afterwards. The worker
// sends a heartbeat well inside it.
const workerIdleTimeout = 10 * time.Second

// Timer tags for the two Connect deadlines, so a test can trap each one.
const (
	workerHelloTimerTag = "worker-hello"
	workerIdleTimerTag  = "worker-idle"
)

// SetMaxWorkersPerUser bounds how many Workers one user may have registered at
// once; 0 (the zero value) is unlimited. Call once at startup before serving.
//
// A setter rather than a tenth positional constructor argument, matching
// AuthContextRegistry.SetMaxConnectionsPerUser: the services this package builds
// directly in tests must come out unlimited, which the zero value gives for free.
//
// Wire workermgr.Manager.SetMaxWorkersPerUser from the same config value: this
// one caps the rows, that one caps the live connections, and only the second is
// a bound on what the worker send queue pool has to honour. See the
// maxWorkersPerUser field.
func (s *WorkerConnectorService) SetMaxWorkersPerUser(n int64) {
	s.maxWorkersPerUser.Store(n)
}

type workerRegistrationLimitError struct {
	limit int64
}

func (e *workerRegistrationLimitError) Error() string {
	return fmt.Sprintf(
		"this account already has %d workers registered, which is the configured maximum "+
			"(max_workers_per_user); deregister one first", e.limit)
}

// refuseIfAtWorkerCap fails the registration when the user already holds as many
// Workers as max_workers_per_user allows.
//
// The registration-time half of the cap, kept because a machine told at
// `leapmux worker register` time that the account is full is a far better
// diagnostic than one that registers and then cannot hold a stream. The half
// that actually bounds pool membership is in workermgr.Manager.Register.
//
// Counting by asking for exactly `cap` rows rather than adding a COUNT query to
// three SQL dialects: the question is only ever "are there at least this many",
// and a page bounded by the cap answers it exactly, reading at most one row past
// it (store.FetchLimit adds a probe row to detect a next page).
//
// That page is the cost of the cap being a number rather than a counter, and it
// scales with the CONFIGURED cap rather than with the fleet: an operator who
// raises max_workers_per_user into the thousands makes every registration read
// that many rows inside the transaction that consumed the key. Unlimited (0)
// skips the query entirely.
func (s *WorkerConnectorService) refuseIfAtWorkerCap(
	ctx context.Context, tx store.Store, owner userid.UserID,
) error {
	limit := s.maxWorkersPerUser.Load()
	if limit <= 0 {
		return nil
	}
	page, err := tx.Workers().ListByUserID(ctx, store.ListWorkersByUserIDParams{
		RegisteredBy: owner,
		PageParams:   store.PageParams{Limit: limit},
	})
	if err != nil {
		cause := fmt.Errorf("count workers: %w", err)
		return wrappedConnectError(connect.CodeInternal, cause)
	}
	if int64(len(page.Rows)) < limit {
		return nil
	}
	// The typed cause is what Register's errors.As matches to count the
	// refusal; sending its text alone would drop that.
	limitErr := &workerRegistrationLimitError{limit: limit}
	return wrappedConnectError(connect.CodeResourceExhausted, limitErr)
}

// newWorkerConnectRefusalError builds a Connect refusal that reconnecting
// cannot fix, tagged with the WorkerConnectRefusal detail the worker acts on.
//
// The detail is what makes a refusal actionable: a proxy in front of the Hub can
// answer 401 or 403 by itself and a non-200 response carries no details, so the
// worker must never infer "I was deleted" from a status code alone.
func newWorkerConnectRefusalError(
	code connect.Code, reason leapmuxv1.WorkerConnectRefusalReason, message string,
) *connect.Error {
	connectErr := connect.NewError(code, message)
	detail, err := connectproto.NewErrorDetail(&leapmuxv1.WorkerConnectRefusal{Reason: reason})
	if err != nil {
		// Marshalling a one-field message this file owns cannot fail; a bare
		// error still beats a panic in a request path.
		return connectErr
	}
	return connectErr.WithDetail(detail)
}

// Register handles the worker → hub registration RPC.
//
// The session-cookie auth interceptor lets this RPC through (it's in the
// public allowlist) because workers don't have a hub session — they
// authenticate by presenting a registration key as a bearer credential
// in the Authorization header. The hub atomically consumes the key and
// creates the worker row in one transaction.
func (s *WorkerConnectorService) Register(
	ctx context.Context,
	req *leapmuxv1.RegisterRequest,
) (*leapmuxv1.RegisterResponse, error) {
	info := serverCallInfo(ctx)
	regKey, ok := auth.BearerToken(info.RequestHeader().Get("Authorization"))
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, "registration key required")
	}

	// Before the registration key is read, let alone consumed: a malformed key
	// set must not burn it. The keys are the Worker's identity, and the Hub pins
	// them to the row this call creates.
	if err := workerkeys.Validate(req.GetKeys()); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err.Error())
	}
	// The durable attempt identity: 32 lowercase hexadecimal characters
	// the worker minted and saved before its first network call. It is
	// what turns a lost response into a recoverable attempt instead of a
	// consumed key with no credentials.
	attemptID := req.GetAttemptId()
	if !validateAttemptIdentity(attemptID) {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			"attempt_id must hold 32 lowercase hexadecimal characters")
	}
	keyDigest := registrationKeyDigest(regKey)

	// A repeated Register with the same attempt is the committed attempt
	// it already served, not a new worker. The digest and the pinned
	// public keys must both agree; a disagreement on either is a
	// different key or a different worker trying this attempt, and
	// neither recovers anything.
	if existing, err := s.store.Workers().GetByRegistrationAttemptID(ctx, attemptID); err == nil {
		if subtle.ConstantTimeCompare(existing.RegistrationKeyHash, keyDigest) == 1 &&
			workerkeys.Equal(req.GetKeys(), existing.PublicKeys.Proto()) {
			return nil, connect.NewError(connect.CodeAlreadyExists,
				"this registration attempt already created a worker; recover its credentials")
		}
		return nil, connect.NewError(connect.CodeUnauthenticated,
			"registration attempt unknown or already consumed")
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, wrappedConnectError(connect.CodeInternal, fmt.Errorf("read the registration attempt: %w", err))
	}

	workerID := id.Generate()
	authToken := id.Generate()

	// Resolve the owner before opening the transaction. In MySQL, an ordinary
	// read inside the transaction could establish its snapshot before the owner
	// lock, hiding a registration that commits while this request waits.
	key, err := s.store.RegistrationKeys().GetByID(ctx, regKey)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, connect.NewError(connect.CodeUnauthenticated, "registration key invalid or already consumed")
		}
		return nil, wrappedConnectError(connect.CodeInternal, fmt.Errorf("read registration key: %w", err))
	}
	registeredBy := key.CreatedBy
	registrantUID, mintOK := userid.New(registeredBy)
	if !mintOK {
		return nil, connect.NewError(connect.CodeInternal, "registration key has a blank creator")
	}
	err = s.store.RunInUserAuthTransaction(ctx, registrantUID, func(tx store.Store) error {
		// Atomic consume: returns the row only if expires_at > now and
		// flips it into the soft-deleted state. Any concurrent caller
		// loses the race and sees ErrNotFound.
		row, err := tx.RegistrationKeys().Consume(ctx, regKey)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return connect.NewError(connect.CodeUnauthenticated, "registration key invalid or already consumed")
			}
			return wrappedConnectError(connect.CodeInternal, fmt.Errorf("consume registration key: %w", err))
		}

		if !registrantUID.Matches(row.CreatedBy) {
			return connect.NewError(connect.CodeUnauthenticated, "registration key creator changed")
		}
		// The live owner's lock serializes this count and insert against other
		// registrations and account deletion. Different keys otherwise lock
		// different rows and can both observe a count below the maximum.
		if err := s.refuseIfAtWorkerCap(ctx, tx, registrantUID); err != nil {
			return err
		}
		if err := tx.Workers().Create(ctx, store.CreateWorkerParams{
			ID:                    workerID,
			AuthToken:             authToken,
			RegisteredBy:          registrantUID,
			PublicKeys:            store.WorkerPublicKeysFromProto(req.GetKeys()),
			RegistrationAttemptID: attemptID,
			RegistrationKeyHash:   keyDigest,
		}); err != nil {
			return wrappedConnectError(connect.CodeInternal, fmt.Errorf("create worker: %w", err))
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, connect.NewError(connect.CodeUnauthenticated, "registration key creator no longer exists")
		}
		// A transaction callback can run more than once. Publish only its final refusal.
		var limitError *workerRegistrationLimitError
		if errors.As(err, &limitError) {
			slog.Warn("refusing worker registration: user is at its worker cap",
				"user_id", registeredBy, "limit", limitError.limit)
			metrics.CountWorkerAdmissionRefused(metrics.WorkerStageRegister)
		}
		return nil, err
	}

	slog.Info("worker registered",
		"worker_id", workerID,
		"registered_by", registeredBy,
	)
	// After the commit, never inside the transaction: a subscriber that
	// re-fetches the list at once must see the new worker.
	s.announceWorkersChanged(registeredBy)

	// registered_by is deliberately NOT returned here. The worker learns its owner
	// from WorkerIdentity on every Connect instead: handing it over once at
	// registration made the worker's local copy a second source of truth, and a state
	// file that predated the field, or was hand-edited or truncated, left the worker
	// running with no owner and every machine-scoped family dead for its own user.
	return &leapmuxv1.RegisterResponse{
		WorkerId:  workerID,
		AuthToken: authToken,
	}, nil
}

// workerIdentityReadTimeout caps each username read, because each one runs
// under the owner's delivery lock: Connect's greeting read, and the read of
// RedeliverWorkerIdentity, which runs inside a user-info observer and has no
// request context of its own. A stalled read would otherwise hold every delivery
// for the owners on that stripe, and the user-info observers and the revocation
// watcher that run them. With the cap it holds the stripe for at most this long.
const workerIdentityReadTimeout = 5 * time.Second

// workerIdentityReadTimerTag tags the timer of workerIdentityReadTimeout, so a
// test can move it.
const workerIdentityReadTimerTag = "worker-identity-read"

// identityReadContext returns the context of one username read under the
// owner's delivery lock. It ends when parent ends, or workerIdentityReadTimeout
// after this call on s.clock. Call the returned function when the read is
// done; it stops the timer and releases the context.
func (s *WorkerConnectorService) identityReadContext(parent context.Context) (context.Context, func()) {
	ctx, cancel := context.WithCancelCause(parent)
	timer := s.clock.AfterFunc(workerIdentityReadTimeout, func() {
		cancel(context.DeadlineExceeded)
	}, workerIdentityReadTimerTag)
	return ctx, func() {
		timer.Stop(workerIdentityReadTimerTag)
		cancel(context.Canceled)
	}
}

// ownerDeliveryLocks serializes, per owner, every identity delivery as one
// critical section: read the username, then publish the name. Connect publishes
// by registering the connection, whose greeting carries the name (see
// registerGreetedConn). RedeliverWorkerIdentity takes its snapshot of the owner's
// connections inside the section, reads, and enqueues to each one.
//
// Frames then reach a worker in the order of their reads. A rename's re-delivery
// starts after the rename commits, so its read sees the new name, and a delivery
// whose read started earlier finishes before it. The last frame a worker
// receives therefore never holds a name older than the newest rename:
//   - A Connect that read the old name holds the lock until the registry holds
//     its connection, so the re-delivery's snapshot finds that connection.
//   - A Connect that starts after the re-delivery reads the new name.
//
// Without it, a read that returned the old name could enqueue after the
// re-delivery of the new one, or a re-delivery could miss a connection that was
// about to register. The worker would keep the old name until the next account
// change.
//
// The locks are striped by a hash of the owner id, so memory stays fixed
// however many accounts exist. Two owners on one stripe only wait for each
// other. The zero value is ready to use.
type ownerDeliveryLocks struct {
	stripes [64]sync.Mutex
}

// lock takes owner's stripe and returns the function that releases it.
func (l *ownerDeliveryLocks) lock(owner string) (unlock func()) {
	mu := l.forOwner(owner)
	mu.Lock()
	return mu.Unlock
}

func (l *ownerDeliveryLocks) forOwner(owner string) *sync.Mutex {
	h := fnv.New32a()
	_, _ = h.Write([]byte(owner))
	return &l.stripes[h.Sum32()%uint32(len(l.stripes))]
}

// workerIdentityFrame is the WorkerIdentity message for a worker that owner
// registered. ownerUsername is empty when the username is unknown.
func workerIdentityFrame(owner, ownerUsername string) *leapmuxv1.ConnectResponse {
	return &leapmuxv1.ConnectResponse{
		Payload: &leapmuxv1.ConnectResponse_WorkerIdentity{
			WorkerIdentity: &leapmuxv1.WorkerIdentity{RegisteredBy: owner, RegisteredByUsername: ownerUsername},
		},
	}
}

// ownerUsername reads the username of the account that registered a worker,
// and reports whether the read succeeded.
//
// The username is display only, so a failed read never refuses or drops a
// connection: the caller sends no username, or keeps the one it sent. A
// missing account is not logged, because a DEREGISTERING worker can outlive
// its deleted owner; any other failure is.
func (s *WorkerConnectorService) ownerUsername(ctx context.Context, owner string) (string, bool) {
	user, err := s.store.Users().GetByID(ctx, owner)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			// The cause separates the read cap from a disconnect. ctx.Err() reads
			// context.Canceled for both, because identityReadContext cancels ctx.
			slog.Warn("read the worker owner's username", "owner", owner, "error", err, "cause", context.Cause(ctx))
		}
		return "", false
	}
	return user.Username, true
}

// RedeliverWorkerIdentity sends the WorkerIdentity frame again to every worker
// that owner registered and that is connected to this Hub, so each follows a
// rename of its owner. It is a user-info observer (auth.WithUserInfoObserver):
// UserInfoInvalidated runs for a change in this process and, through the
// revocation watcher, for one in another Hub process, so every Hub updates the
// workers it holds.
//
// A user-info event also fires for changes that keep the username, such as an
// administrator flag. The frame is then identical to the last one, and the
// worker stores the same value again.
//
// It can wait behind a Connect's greeting read for the owner, or for another
// owner on the stripe, for at most workerIdentityReadTimeout per read.
func (s *WorkerConnectorService) RedeliverWorkerIdentity(owner string) {
	unlock := s.identityDeliveries.lock(owner)
	defer unlock()
	conns := s.workerMgr.OwnerConnsForTrustedPath(owner)
	if len(conns) == 0 {
		return
	}
	ctx, release := s.identityReadContext(context.Background())
	defer release()
	username, ok := s.ownerUsername(ctx, owner)
	if !ok {
		// Keep the username each worker holds: a failed read is no news.
		return
	}
	frame := workerIdentityFrame(owner, username)
	for _, conn := range conns {
		if err := conn.SendControl(frame); err != nil {
			slog.Debug("re-deliver the worker identity", "owner", owner, "error", err)
		}
	}
}

// registerGreetedConn reads the username of the worker's owner, builds the
// connection that greets the worker with it, and registers the connection. It
// returns the connection and its pump, and what workermgr.Manager.Register
// answers.
//
// The read and the registration are one critical section of the owner's delivery
// lock; see ownerDeliveryLocks. The section ends when Register returns, on both
// outcomes, and the deferred unlock also covers a panic in the read or in
// Register. A failed read greets with no username; see ownerUsername. The worker
// then holds none until the next account change or reconnect.
//
// The greeting goes through Register rather than a send here, because Register
// enqueues it before it publishes the conn. With a single handler drain it is
// mechanically the first frame written, which the worker needs: the worker's
// owner gate guards every machine-scoped family on the owner, and a session can
// exist the moment the conn is reachable.
//
// worker.RegisteredBy is already in hand from Connect's
// AuthenticateWorkerBearer call, so neither the greeting's owner nor the owner
// handed to NewConn costs a query.
func (s *WorkerConnectorService) registerGreetedConn(
	connCtx context.Context,
	cancelConn context.CancelFunc,
	worker *store.Worker,
	hello *leapmuxv1.WorkerHello,
	stream leapmuxv1connect.WorkerConnectorServiceConnectServerStream,
) (conn *workermgr.Conn, pump *workermgr.SendPump, replaced bool, err error) {
	owner := worker.RegisteredBy
	unlock := s.identityDeliveries.lock(owner)
	defer unlock()
	readCtx, release := s.identityReadContext(connCtx)
	defer release()
	username, _ := s.ownerUsername(readCtx, owner)
	conn, pump = workermgr.NewConn(connCtx, cancelConn, worker.ID, owner, s.queuePool, stream.Send,
		workerIdentityFrame(owner, username), hello)
	replaced, err = s.workerMgr.Register(conn)
	return conn, pump, replaced, err
}

// announceWorkersChanged tells ownerID's live /ws/userevents subscribers whose
// credential holds worker:read that the owner's worker list reads differently:
// a worker registered, came online or went offline. A client then re-fetches
// the list. The online bit matters as much as membership, because the client
// loads a worker's name only while the list reads it online, and a worker
// registers before it connects.
func (s *WorkerConnectorService) announceWorkersChanged(ownerID string) {
	if s.crdtRegistry != nil {
		s.crdtRegistry.BroadcastWorkersChanged(ownerID)
	}
}

func (s *WorkerConnectorService) Connect(
	ctx context.Context,
	stream leapmuxv1connect.WorkerConnectorServiceConnectServerStream,
) error {
	info := serverCallInfo(ctx)
	// One credential rule for every worker path: AuthenticateWorkerBearer.
	worker, err := auth.AuthenticateWorkerBearer(ctx, s.store, info.RequestHeader().Get("Authorization"))
	if err != nil {
		switch {
		case errors.Is(err, auth.ErrWorkerDeleted):
			// Positive proof only: the Hub holds a row with this token and its
			// deleted_at is set. A token the Hub has never seen gets a bare
			// Unauthenticated below, because a worker pointed at the wrong Hub, a
			// Hub restored from an older backup, or a proxy that rewrote the
			// Authorization header must not destroy the worker's identity.
			return newWorkerConnectRefusalError(connect.CodeUnauthenticated,
				leapmuxv1.WorkerConnectRefusalReason_WORKER_CONNECT_REFUSAL_REASON_WORKER_DELETED,
				"this worker was deleted")
		case auth.IsWorkerCredentialError(err):
			return wrappedConnectError(connect.CodeUnauthenticated, err)
		default:
			return wrappedConnectError(connect.CodeInternal, err)
		}
	}
	// A live row proceeds, whatever its state: a DEREGISTERING worker must still
	// be able to hold this stream, because that is how it receives the deregister
	// instruction and wipes its local state. Refusing it here would leave the
	// machine running with nobody able to tell it to stop.

	// The connection's context exists before its hello, because the one receive
	// pump of this stream reads under it. Replacement cancels this derived
	// context to terminate the superseded handler without affecting the request
	// context of the newly connected worker.
	connCtx, cancelConn := context.WithCancel(ctx)
	defer cancelConn()
	received := startWorkerReceivePump(connCtx, stream)

	// The hello comes before registration. Until a connection's hello is read
	// and validated, the registry keeps the connection it already holds, so
	// GetWorkerHandshakeParams can never pair this connection's mode with the
	// previous one's keys. Nothing is persisted, so no write can fail or
	// interleave with a concurrent connection.
	hello, err := s.receiveHello(connCtx, received)
	if err != nil {
		// Logged, because the worker retries every refusal of this phase, and
		// nothing else on the Hub would record why it keeps reconnecting.
		slog.Warn("worker stream ended before a valid hello", "worker_id", worker.ID, "reason", err)
		return err
	}
	if hello == nil {
		slog.Info("worker closed the stream before its hello", "worker_id", worker.ID)
		return nil
	}

	// The pin. A stolen auth token must not attach other keys to this worker:
	// without this check it could replace the live connection and every client
	// without a pin would trust the attacker's keys. Refusing here, before
	// workermgr.NewConn, is what keeps the refused connection from replacing the
	// live one.
	pinned := worker.PublicKeys.Proto()
	if !workerkeys.Equal(hello.GetKeys(), pinned) {
		slog.Warn("refusing a worker whose hello keys differ from the keys it registered with",
			"worker_id", worker.ID,
			"registered_fingerprint", workerkeys.Fingerprint(pinned),
			"presented_fingerprint", workerkeys.Fingerprint(hello.GetKeys()))
		return newWorkerConnectRefusalError(connect.CodePermissionDenied,
			leapmuxv1.WorkerConnectRefusalReason_WORKER_CONNECT_REFUSAL_REASON_KEYS_DIFFER,
			"the worker's public keys differ from the keys it registered with")
	}

	conn, pump, replaced, err := s.registerGreetedConn(connCtx, cancelConn, worker, hello, stream)
	if err != nil {
		if errors.Is(err, workermgr.ErrRegistryFenced) {
			// This connection passed the shutdown interceptor a moment before
			// the Hub began fencing. Unavailable, not Internal: nothing is
			// wrong with the worker or the request, and a server that is going
			// away is what the code means. A worker retries every refusal that
			// carries no WorkerConnectRefusal detail, this one included, so this
			// is for the operator reading logs and for any proxy or client that
			// distinguishes the two.
			return wrappedConnectError(connect.CodeUnavailable, err)
		}
		if errors.Is(err, workermgr.ErrTooManyWorkers) {
			// Counted on the same series as a refusal at Register: both are one
			// account's Worker turned away by max_workers_per_user, and an
			// operator asking "is this cap biting?" wants one number rather than
			// two that have to be summed. The `stage` label is what keeps the two
			// tellable apart within that one number. ResourceExhausted for the
			// reason Register's refusal is: the credential is fine and the
			// condition clears when another of this account's connections goes
			// away.
			metrics.CountWorkerAdmissionRefused(metrics.WorkerStageConnect)
			return wrappedConnectError(connect.CodeResourceExhausted, err)
		}
		return wrappedConnectError(connect.CodeInternal, fmt.Errorf("greet worker: %w", err))
	}
	// Teardown order is deliberate and lives in ONE defer so it cannot drift
	// via LIFO comment mistakes:
	//  1. Unregister removes the conn from the registry (no new Hub sends).
	//  2. Drain writes every remaining frame while this handler still owns
	//     the stream (write-after-finish proof).
	//  3. Fence closes the queue and cancels Done.
	//  4. closeWorkerChannels and the offline announcement only if we were
	//     still the registered conn. A superseded conn leaves the worker
	//     online under its replacement.
	// It is deferred right after Register succeeds, so a panic anywhere below
	// still removes the conn: a conn left registered reads the worker online
	// through a queue that nobody drains.
	defer func() {
		removed := s.workerMgr.Unregister(worker.ID, conn)
		_ = pump.Drain()
		conn.Fence()
		if removed {
			s.closeWorkerChannels(worker.ID)
			s.announceWorkersChanged(worker.RegisteredBy)
		}
	}()
	if replaced {
		// A new worker process replaced an older connection. The old
		// connection's Unregister will return false (it's no longer the
		// current conn), so closeWorkerChannels won't run from its defer.
		// We must close old channels now so the Frontend detects the
		// disconnect and opens fresh channels to the new worker.
		s.closeWorkerChannels(worker.ID)
	} else {
		// The worker came online. A replacement leaves it online, so only
		// this case changes what the owner's worker list reads.
		s.announceWorkersChanged(worker.RegisteredBy)
	}
	// The request context, kept before ctx is shadowed by connCtx below. The
	// ctx.Done exit distinguishes its two causes by it: a request context
	// that ALSO ended means the Hub or the transport closed the stream, while
	// a live one means this conn was fenced or replaced out from under it.
	reqCtx := ctx
	ctx = connCtx

	// Read the worker again, now that Register made the connection reachable,
	// and decide on the DEREGISTER delivery from this read, never from the
	// authentication read above. A deregistration, or a deletion with the
	// worker's user, can commit between that read and Register. Its
	// SendDeregister then finds no connection and tells nobody.
	//   - A change that commits before this read shows here, and Connect tells
	//     the worker.
	//   - A change that commits after this read runs its SendDeregister after
	//     Register, so SendDeregister finds the connection and tells the
	//     worker.
	// A change that commits before this read can also run its SendDeregister
	// after Register. The worker then receives DEREGISTER twice, which is
	// harmless: it shuts down once.
	current, err := s.store.Workers().GetByIDIncludeDeleted(ctx, worker.ID)
	if err != nil {
		return wrappedConnectError(connect.CodeInternal, fmt.Errorf("read worker after register: %w", err))
	}

	// Update last seen.
	if err := s.store.Workers().UpdateLastSeen(ctx, worker.ID); err != nil {
		slog.Warn("failed to update worker last seen", "worker_id", worker.ID, "error", err)
	}

	slog.Info("worker connected", "worker_id", worker.ID, "status", current.Status)
	// exitReason names WHY the handler is returning, set on every exit path
	// below so "worker disconnected" says what ended the stream rather than
	// only that it ended. It exists because the quiet exits are the ones an
	// operator cannot otherwise diagnose: the worker's own close, a fenced or
	// replaced connection, and a transport-level failure all return nil from
	// here, and before this the log could not tell them apart.
	var exitReason string
	defer func() {
		if exitReason == "" {
			exitReason = "handler returned without classifying its exit"
		}
		slog.Info("worker disconnected", "worker_id", worker.ID, "reason", exitReason)
	}()

	// A deregistering worker is told to stop on every connect: its persisted
	// deregistering_at is the intent, and this connection is the delivery. A
	// worker deleted with its user while it connected is told to stop too.
	// The delivery runs beside the main loop rather than before it, because
	// the DEREGISTER instruction reaches the wire through the loop's drain and
	// its acknowledgement through the loop's receive (pending.Complete only
	// runs from the loop). notifyDone ends the connection once the delivery
	// returns, whatever its outcome. An ACTIVE worker starts no delivery
	// goroutine.
	var notifyDone chan struct{} // nil for a normal worker: a nil select case blocks forever
	if s.notifier != nil && current.Status != leapmuxv1.WorkerStatus_WORKER_STATUS_ACTIVE {
		notifyDone = make(chan struct{})
		// Delivery owns a detached MarkDeleted write after acknowledgement.
		// Every return joins it before unregistering, so store shutdown cannot
		// leave the worker's local registration gone and its Hub row pending.
		// Cancel first to release SendAndWait when no acknowledgement arrived.
		defer func() {
			cancelConn()
			<-notifyDone
		}()
		go func() {
			defer close(notifyDone)
			s.notifier.DeliverOnConnect(ctx, current, conn)
		}()
	}

	// Main message loop: process what the receive pump reads, and detect idle
	// timeouts (dead workers that didn't close the TCP connection cleanly).
	idleTimer := s.clock.NewTimer(workerIdleTimeout, workerIdleTimerTag)
	defer idleTimer.Stop(workerIdleTimerTag)
	// Deregistering workers must not compete with the receive-idle timer:
	// DeliverOnConnect parks in SendAndWait and a quiet worker would otherwise
	// be torn down mid-ack. Nil select case blocks forever.
	var idleC <-chan time.Time
	if notifyDone == nil {
		idleC = idleTimer.C
	}

	// resetIdle stops + drains + re-arms the idle timer. Folded into a
	// helper so every successful receive (both branches) reuses one
	// implementation instead of repeating the drain dance.
	resetIdle := func() {
		if notifyDone != nil {
			return
		}
		if !idleTimer.Stop(workerIdleTimerTag) {
			select {
			case <-idleTimer.C:
			default:
			}
		}
		idleTimer.Reset(workerIdleTimeout, workerIdleTimerTag)
	}

	// recvErr holds the receive error that ended the stream, written by
	// handle (handler goroutine only) and read by the exit paths below.
	var recvErr error
	handle := func(result workerReceiveResult) error {
		if result.err != nil {
			recvErr = result.err
			return errWorkerStreamClosed
		}
		resetIdle()
		if err := s.processWorkerMessage(ctx, conn, worker.ID, worker.RegisteredBy, result.msg); err != nil {
			return wrappedConnectError(connect.CodeInvalidArgument, err)
		}
		return nil
	}

	// onHandleErr classifies handle's terminal errors into an exitReason,
	// shared by both select arms that run it. A stream-closed error is a
	// clean exit that still deserves a cause (EOF vs a transport failure vs
	// a refusal); a processing error is returned and reaches the worker in
	// the trailers, and is named here so the disconnect log matches.
	onHandleErr := func(err error) error {
		if errors.Is(err, errWorkerStreamClosed) {
			exitReason = streamEndReason(recvErr)
			return nil
		}
		exitReason = fmt.Sprintf("invalid worker message: %v", err)
		return err
	}

	for {
		select {
		case result := <-received:
			if err := handle(result); err != nil {
				return onHandleErr(err)
			}

		case <-idleC:
			// A message may have arrived at the same instant the timer
			// fired. Go's select picks randomly among ready cases, so
			// drain received before deciding to disconnect.
			select {
			case result := <-received:
				if err := handle(result); err != nil {
					return onHandleErr(err)
				}
				continue
			default:
			}
			exitReason = fmt.Sprintf("idle timeout: no messages from worker in %s", workerIdleTimeout)
			slog.Warn("worker idle timeout, assuming disconnected", "worker_id", worker.ID)
			return nil

		case <-pump.Ready():
			// Bounded turn so a large outbound backlog yields to receives /
			// idle / ctx. Remaining frames re-signal Ready. Teardown uses
			// the full Drain in the defer above.
			if err := pump.DrainTurn(); err != nil {
				exitReason = "outbound queue gave up (see preceding writer warning)"
				return nil // give-up already fenced + cancelled
			}

		case <-notifyDone:
			exitReason = "DEREGISTER delivery finished"
			return nil

		case <-ctx.Done():
			// connCtx ends either because the stream's own request context
			// ended (hub shutdown, transport close) or because this conn was
			// fenced or replaced — the request context is the tell between
			// them. The deferred delivery join covers this and every other
			// return path. SendAndWait observes connection cancellation,
			// while an acknowledged deletion keeps its own API deadline.
			if reqCtx.Err() != nil {
				exitReason = "request context ended (hub shutdown or transport closed)"
			} else {
				exitReason = "connection cancelled (fenced or replaced)"
			}
			return nil
		}
	}
}

// workerReceiveResult is one outcome of a Receive on a Connect stream.
type workerReceiveResult struct {
	msg *leapmuxv1.ConnectRequest
	err error
}

// workerMessageSource is the one method of a Connect stream that the receive
// pump calls.
type workerMessageSource interface {
	Receive() (*leapmuxv1.ConnectRequest, error)
}

// startWorkerReceivePump starts the one goroutine that reads a Connect stream,
// and returns the channel that carries each outcome in order. The hello wait and
// the main loop read the same channel, so a stream has one reader by
// construction. The pump stops after it delivers a receive error.
//
// Receive cannot be interrupted, so the handler waits on this channel instead,
// beside its deadlines. The pump reads one message ahead of the handler, and the
// channel holds one more, so a message that arrives during the pin check waits
// here. A refused connection drops it.
//
// The goroutine never leaks. Pass the connection's context, which Connect
// cancels as it returns. Connect's return also ends the stream, which unblocks
// Receive, and a message that Receive then returns finds the context ended.
func startWorkerReceivePump(ctx context.Context, stream workerMessageSource) <-chan workerReceiveResult {
	received := make(chan workerReceiveResult, 1)
	go func() {
		for {
			msg, err := stream.Receive()
			select {
			case received <- workerReceiveResult{msg, err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	return received
}

// receiveHello takes the first message of a Connect stream from its receive pump
// and returns its validated WorkerHello. It returns (nil, nil) when the worker
// closed the stream before sending anything. It waits at most workerIdleTimeout.
func (s *WorkerConnectorService) receiveHello(
	ctx context.Context,
	received <-chan workerReceiveResult,
) (*leapmuxv1.WorkerHello, error) {
	timer := s.clock.NewTimer(workerIdleTimeout, workerHelloTimerTag)
	defer timer.Stop(workerHelloTimerTag)
	select {
	case result := <-received:
		if errors.Is(result.err, io.EOF) {
			return nil, nil
		}
		if result.err != nil {
			return nil, result.err
		}
		hello, err := validateWorkerHello(result.msg)
		if err != nil {
			return nil, wrappedConnectError(connect.CodeInvalidArgument, err)
		}
		return hello, nil
	case <-timer.C:
		return nil, connect.Errorf(connect.CodeDeadlineExceeded,
			"the worker sent no hello within %s", workerIdleTimeout)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// validateWorkerHello checks that a Connect stream's first message is a
// WorkerHello a client can open a channel with.
//
// The key set must be COMPLETE whatever the mode. The three keys are the
// Worker's identity, not this session's cipher choice, so a partial set can
// never equal the set the Worker registered with and is refused here rather
// than at the pin comparison. UNSPECIFIED and any unknown mode are refused: the
// mode stays on the connection because it selects the handshake.
func validateWorkerHello(msg *leapmuxv1.ConnectRequest) (*leapmuxv1.WorkerHello, error) {
	hello := msg.GetHello()
	if hello == nil {
		return nil, errors.New("the first message on a Connect stream must be a hello")
	}
	if err := workerkeys.Validate(hello.GetKeys()); err != nil {
		return nil, err
	}
	switch mode := hello.GetEncryptionMode(); mode {
	case leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM,
		leapmuxv1.EncryptionMode_ENCRYPTION_MODE_CLASSIC:
	default:
		return nil, fmt.Errorf("hello encryption mode %s is not supported", mode)
	}
	return hello, nil
}

// streamEndReason names why the worker's Connect stream stopped producing
// messages. A nil error means the receive loop never observed one (the
// handler exited through another select arm and handle classified the close
// secondhand), and io.EOF is the worker closing its own stream — the one
// ordinary, healthy end. Anything else is a transport-level failure worth
// reading verbatim: resets, protocol errors, and the i/o timeouts that
// used to make solo's hub drop its worker every ten seconds.
func streamEndReason(err error) string {
	if err == nil || errors.Is(err, io.EOF) {
		return "worker closed the stream"
	}
	return fmt.Sprintf("worker stream failed: %v", err)
}

// errWorkerStreamClosed is the sentinel `handle` returns on receive
// error — distinguishes a clean worker disconnect (return nil) from a
// process-level abort.
var errWorkerStreamClosed = errors.New("worker stream closed")

// processWorkerMessage handles a single message from the worker stream.
// Returns a non-nil error to terminate the connection (e.g. invalid config).
//
// registeredBy is the worker's registrant, already in hand from Connect's
// AuthenticateWorkerBearer call. It is threaded here purely for the tab-sync path, which needs
// the owner axis workspace_tab_owned is keyed by.
func (s *WorkerConnectorService) processWorkerMessage(
	ctx context.Context,
	conn *workermgr.Conn,
	workerID, registeredBy string,
	msg *leapmuxv1.ConnectRequest,
) error {
	// A hello opens the stream and is read before the main loop starts, so one
	// here is a second hello -- a protocol error.
	if msg.GetHello() != nil {
		return errors.New("worker sent a second hello on one Connect stream")
	}

	// Update last seen periodically on heartbeats.
	if msg.GetHeartbeat() != nil {
		if err := s.store.Workers().UpdateLastSeen(ctx, workerID); err != nil && ctx.Err() == nil {
			slog.Warn("failed to update worker last seen on heartbeat", "worker_id", workerID, "error", err)
		}
		// Heartbeat is a must-deliver control frame.
		if err := conn.SendControl(&leapmuxv1.ConnectResponse{
			Payload: &leapmuxv1.ConnectResponse_Heartbeat{
				Heartbeat: &leapmuxv1.Heartbeat{},
			},
		}); err != nil {
			slog.Debug("failed to send heartbeat response", "worker_id", workerID, "error", err)
		}
		return nil
	}

	// Try to complete pending request-response pairs (file operations).
	if s.pending != nil && msg.GetRequestId() != "" {
		if s.pending.Complete(msg.GetRequestId(), msg) {
			return nil
		}
	}

	// Handle workspace tab sync from worker. The response is sent
	// back on the same bidi stream with the matching request_id so
	// the worker can correlate it with its outbound message.
	if tabSync := msg.GetWorkerTabInventory(); tabSync != nil {
		s.handleWorkspaceTabsSync(ctx, conn, workerID, registeredBy, msg.GetRequestId(), tabSync)
		return nil
	}

	// Route channel messages from worker to frontend.
	if chMsg := msg.GetChannelMessageResp(); chMsg != nil {
		if s.channelMgr != nil {
			matched, connID, err := s.channelMgr.RelayWorkerMessage(chMsg, workerID)
			if !matched {
				slog.Warn("channel relay: worker sent message for an unowned channel",
					"worker_id", workerID,
					"channel_id", chMsg.GetChannelId(),
				)
				return nil
			}
			if err != nil {
				slog.Warn("channel relay: terminal worker-to-frontend failure",
					"worker_id", workerID,
					"channel_id", chMsg.GetChannelId(),
					"correlation_id", chMsg.GetCorrelationId(),
					"error", err,
				)
				s.closeWorkerChannel(conn, workerID, connID, chMsg.GetChannelId())
				return nil
			}

			slog.Debug("relaying channel message from worker",
				"worker_id", workerID,
				"channel_id", chMsg.GetChannelId(),
				"correlation_id", chMsg.GetCorrelationId(),
			)
		}
		return nil
	}

	slog.Debug("unhandled worker message",
		"worker_id", workerID,
		"request_id", msg.GetRequestId(),
	)
	return nil
}

// closeWorkerChannel tears down a channel whose frontend delivery failed.
//
// connID is the connection the failed relay was addressed to, and the
// predicate checks it as well as the worker. The frontend relay flips its
// writer closed a few instructions before UnbindUserAndCleanup runs, so a
// tab that reconnects inside that window can rebind this channel to a
// fresh connection before this close lands -- and matching on worker
// identity alone would tear down that new binding, which is exactly what
// UnbindUserAndCleanup's own ConnID predicate exists to avoid.
//
// An empty connID means the relay never resolved a binding, so there is
// nothing newer to protect and the worker check stands alone.
func (s *WorkerConnectorService) closeWorkerChannel(conn *workermgr.Conn, workerID, connID, channelID string) {
	closed := s.channelMgr.CloseByIDIf(channelID, func(info channelmgr.ChannelInfo) bool {
		if info.WorkerID != workerID {
			return false
		}
		return connID == "" || info.ConnID == connID
	})
	if len(closed) == 0 {
		return
	}
	if err := conn.SendControl(newChannelCloseResponse(channelID)); err != nil {
		slog.Debug("failed to close terminal worker channel",
			"worker_id", workerID, "channel_id", channelID, "error", err)
	}
}

// handleWorkspaceTabsSync compares the worker's reported tab state
// against the CRDT-derived workspace_tab_owned view, computes the
// authoritative classification, and:
//
//   - Emits an EMPTY WorkerTabInventoryResponse on the bidi stream. It carries no
//     classification: its only effect is to make the worker trigger a reconcile
//     pass, which derives what to drop from ListOwnedTabsForWorker instead. It
//     once listed orphans and cross-workspace reassignments; nothing consumed
//     them, and there is no workspace id on the worker left to reassign.
//   - For tabs the CRDT knows about that the worker doesn't,
//     submits a TombstoneTab op via SubmitInternal so the CRDT side
//     converges to the worker's authoritative view (the worker is
//     the source of truth for live agent / terminal liveness).
//
// `requestID` is the ConnectRequest envelope id; the response carries
// the same id so the worker correlates it.
//
// `registeredBy` is the worker's registrant and the owner the whole exchange is
// scoped to. ListOwnedByWorker binds it, because workspace_tab_owned is keyed
// by (user_id, tab_id) and worker_id alone selects across tenants -- nothing in
// the schema ties a row's user_id to the registrant of the worker it names.
//
// That scoping is also why a blank registrant aborts the handler below instead
// of falling through: the orphan classification reads "in the worker's report,
// absent from hubTabs" as "the CRDT dropped it", so an empty hubTabs would tell
// the worker to drop every agent and terminal it hosts.
func (s *WorkerConnectorService) handleWorkspaceTabsSync(
	ctx context.Context,
	conn *workermgr.Conn,
	workerID, registeredBy, requestID string,
	sync *leapmuxv1.WorkerTabInventory,
) {
	owner, ok := userid.New(registeredBy)
	if !ok {
		slog.Error("workspace tab sync: worker has a blank registrant, cannot scope the owned-tab view",
			"worker_id", workerID)
		return
	}
	hubTabs, err := s.store.WorkspaceTabIndex().ListOwnedByWorker(ctx, store.ListOwnedTabsByWorkerParams{
		UserID:   owner,
		WorkerID: workerID,
	})
	if err != nil {
		slog.Error("failed to list hub-owned tabs for worker during sync", "worker_id", workerID, "error", err)
		return
	}
	type tabKey struct {
		tabType leapmuxv1.TabType
		tabID   string
	}
	// Build a single index keyed by (tab_type, tab_id). Matched entries
	// are removed during the worker-side scan so the post-scan
	// leftovers ARE the stale-tombstone set — no third pass over the
	// worker keys needed.
	// (tab_type, tab_id) is a safe key here ONLY because the query above is
	// owner-scoped: within one owner the table is unique on tab_id, so no two
	// rows can collide. It would not be safe across owners -- tab ids are
	// client-chosen, and the worker's report carries no user axis at all, so a
	// cross-owner collision would be unresolvable on this side.
	hubByKey := make(map[tabKey]store.WorkerTabStateRow, len(hubTabs))
	for _, ht := range hubTabs {
		hubByKey[tabKey{tabType: ht.TabType, tabID: ht.TabID}] = ht
	}

	resp := &leapmuxv1.WorkerTabInventoryResponse{}
	// The scan's only remaining product is the leftovers in hubByKey. The
	// per-tab classification the response used to carry (orphan_tab_ids,
	// reassignments) is gone: no worker ever read it -- OnTabSyncResponse
	// discards the payload and triggers a reconcile pass -- and it could not
	// have been made owner-safe anyway, because the worker's report carries no
	// user axis. OrphanReconciler makes both decisions from
	// ListOwnedTabsForWorker, which does.
	for _, t := range sync.GetTabs() {
		delete(hubByKey, tabKey{tabType: t.GetTabType(), tabID: t.GetTabId()})
	}

	// Whatever survived hubByKey above is a CRDT row the worker doesn't
	// host anymore — tombstone via the manager so subscribers observe a
	// consistent state.
	//
	// Group by owner and submit ONE BATCH PER TENANT, each to that tenant's own
	// manager. A single flat batch handed to whichever owner's manager Go's
	// randomized map iteration yielded first would carry every tombstone into
	// that one manager, and nothing downstream can catch it: a submit names no
	// tenant of its own -- the manager it lands on IS the tenant -- so there is
	// nothing left to compare it against, and SubmitInternal marks the batch
	// internal, which is exactly the flag that skips the per-op auth check. So
	// the foreign tombstone COMMITS -- applyTombstoneTab materializes a record
	// for a tab that owner has never seen, while the real owner's index row is
	// left in place (the projection diff keys deletes by the projected row's own
	// owner, i.e. the winner's) and its tabs live forever.
	//
	// Not reachable now that ListOwnedByWorker binds the owner: hubByKey holds
	// exactly one owner's rows, and it is `owner` -- the value the query was
	// bound with, already minted above.
	//
	// So the tombstones go to THAT manager, in ONE batch. The previous shape
	// grouped by each row's own user_id and handed the resulting raw string to
	// Registry.Get, which made this the last production path reaching the
	// registry with an unvalidated column value. Deriving the target from the
	// bound owner instead makes the batch's tenancy a property of the query
	// rather than of data the loop re-reads -- and the row-level owner is still
	// checked, but as an assertion that says so when it fails rather than a
	// silent route into whatever manager a foreign string names.
	if s.crdtRegistry != nil && len(hubByKey) > 0 {
		tombstones := make([]*leapmuxv1.CrdtOp, 0, len(hubByKey))
		for _, ht := range hubByKey {
			if !owner.Matches(ht.UserID) {
				slog.Error("workspace tab sync: owner-bound query returned a foreign row; refusing to tombstone",
					"worker_id", workerID, "scope_owner", owner.String(), "row_owner", ht.UserID, "tab_id", ht.TabID)
				continue
			}
			tombstones = append(tombstones, &leapmuxv1.CrdtOp{
				OpId: id.Generate(),
				Body: &leapmuxv1.CrdtOp_TombstoneTab{
					TombstoneTab: &leapmuxv1.TombstoneTabOp{
						Tab: &leapmuxv1.TabIdent{TabType: ht.TabType, TabId: ht.TabID},
					},
				},
			})
		}
		if len(tombstones) > 0 {
			mgr, err := s.crdtRegistry.Get(ctx, owner.String())
			if err != nil {
				slog.Warn("workspace tab sync: get manager failed",
					"worker_id", workerID, "user_id", owner.String(), "error", err)
			} else {
				batch := &leapmuxv1.OpBatch{
					// The owner is part of the batch id so the entry names its
					// own tenant in the journal: id.Generate() already makes it
					// unique, but the dedup table is keyed per user, and an id
					// that carries the tenant is diagnosable rather than opaque.
					BatchId: "worker-sync-" + workerID + "-" + owner.String() + "-" + id.Generate(),
					Ops:     tombstones,
				}
				if _, err := mgr.SubmitInternal(ctx, crdt.SubmitInput{
					Batches:     []*leapmuxv1.OpBatch{batch},
					PrincipalID: crdt.HubReservedPrincipal,
				}); err != nil {
					slog.Warn("workspace tab sync: submit tombstones failed",
						"worker_id", workerID, "user_id", owner.String(), "error", err)
				}
			}
		}
	}

	// Always send a response even when both lists are empty so the
	// worker can rely on the round-trip to mark its initial sync
	// complete.
	if err := conn.SendControl(&leapmuxv1.ConnectResponse{
		RequestId: requestID,
		Payload: &leapmuxv1.ConnectResponse_WorkerTabInventoryResp{
			WorkerTabInventoryResp: resp,
		},
	}); err != nil {
		slog.Debug("failed to send workspace tabs sync response",
			"worker_id", workerID, "error", err)
	}

	slog.Info("workspace tab sync handled",
		"worker_id", workerID,
		"worker_tabs", len(sync.GetTabs()),
		"hub_tabs", len(hubTabs),
		"stale_hub_rows", len(hubByKey),
	)
}

// closeWorkerChannels unregisters every channel a disconnected worker owned.
//
// Named for that one job rather than the general "cleanup" it used to promise:
// with the DB work gone there is nothing else here, and a hook whose name
// invites unrelated teardown is how the shutdown special case got in.
//
// There is deliberately no shutdown special case. One used to skip this
// entirely while the Hub was going down, justified by DB work that has since
// moved out of here -- what is left is in-memory channel bookkeeping and a
// best-effort close frame, the identical work every ordinary disconnect does.
// Keeping the skip meant a second teardown path that only ever ran at shutdown,
// and an INFO line announcing it, usually after the relay disconnect had
// already closed the very channels it claimed to be skipping.
func (s *WorkerConnectorService) closeWorkerChannels(workerID string) {
	// Close all channels associated with this worker.
	if s.channelMgr != nil {
		removed := s.channelMgr.UnregisterByWorker(workerID)
		if len(removed) > 0 {
			slog.Info("closed channels for disconnected worker",
				"worker_id", workerID,
				"count", len(removed),
			)
		}
	}
}
