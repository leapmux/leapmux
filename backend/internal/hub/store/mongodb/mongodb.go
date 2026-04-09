// Package mongodb implements the Hub store backed by MongoDB.
//
// Each domain entity has its own collection within a single database.
// Queries are hand-written using the official Go driver v2. Transactions
// use a mutex-serialized approach with compensating rollback (similar
// to the DynamoDB backend).
package mongodb

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readconcern"
	"go.mongodb.org/mongo-driver/v2/mongo/writeconcern"

	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/store"
)

// mongoStore implements store.Store backed by MongoDB.
type mongoStore struct {
	client  *mongo.Client
	db      *mongo.Database
	mig     *mongoMigrator
	mu      sync.Mutex // serializes RunInTransaction
	tracker *txTracker // non-nil inside transaction callback for rollback

	// Pre-allocated sub-stores to avoid per-call heap allocation.
	orgs                  orgStore
	users                 userStore
	sessions              sessionStore
	orgMembers            orgMemberStore
	workers               workerStore
	workerAccessGrants    workerAccessGrantStore
	workerNotifications   workerNotificationStore
	registrations         registrationStore
	workspaces            workspaceStore
	workspaceAccess       workspaceAccessStore
	workspaceTabs         workspaceTabStore
	workspaceLayouts      workspaceLayoutStore
	workspaceSections     workspaceSectionStore
	workspaceSectionItems workspaceSectionItemStore
	oauthProviders        oauthProviderStore
	oauthStates           oauthStateStore
	oauthTokens           oauthTokenStore
	oauthUserLinks        oauthUserLinkStore
	pendingOAuthSignups   pendingOAuthSignupStore
	cleanup               cleanupStore
}

var _ store.Store = (*mongoStore)(nil)

// New creates a new MongoDB-backed Store and runs migrations before
// returning. Use ctx to control the timeout for the migration step.
func New(ctx context.Context, opts config.MongoDBConfig) (store.Store, error) {
	clientOpts := options.Client().ApplyURI(opts.URI)
	if opts.MaxPoolSize > 0 {
		clientOpts.SetMaxPoolSize(uint64(opts.MaxPoolSize))
	}
	if opts.MinPoolSize > 0 {
		clientOpts.SetMinPoolSize(uint64(opts.MinPoolSize))
	}
	if opts.MaxConnIdleTimeSeconds > 0 {
		clientOpts.SetMaxConnIdleTime(time.Duration(opts.MaxConnIdleTimeSeconds) * time.Second)
	}
	if opts.ServerSelectionTimeoutSeconds > 0 {
		clientOpts.SetServerSelectionTimeout(time.Duration(opts.ServerSelectionTimeoutSeconds) * time.Second)
	}
	if opts.TimeoutSeconds > 0 {
		clientOpts.SetTimeout(time.Duration(opts.TimeoutSeconds) * time.Second)
	}
	if opts.ReadConcern != "" {
		clientOpts.SetReadConcern(&readconcern.ReadConcern{Level: opts.ReadConcern})
	}
	if opts.WriteConcern != "" {
		clientOpts.SetWriteConcern(parseWriteConcern(opts.WriteConcern))
	}
	if opts.RetryWrites != nil {
		clientOpts.SetRetryWrites(*opts.RetryWrites)
	}

	client, err := mongo.Connect(clientOpts)
	if err != nil {
		return nil, fmt.Errorf("connect mongodb: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := client.Ping(pingCtx, nil); err != nil {
		return nil, fmt.Errorf("ping mongodb: %w", err)
	}

	db := client.Database(opts.Database)
	st := &mongoStore{
		client: client,
		db:     db,
		mig:    newMigrator(db),
	}
	initSubStores(st)
	if err := st.mig.Migrate(ctx); err != nil {
		disconnectCtx, disconnectCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer disconnectCancel()
		_ = client.Disconnect(disconnectCtx)
		return nil, fmt.Errorf("mongodb migrate: %w", err)
	}
	return st, nil
}

// collection returns a collection handle.
func (s *mongoStore) collection(name string) *mongo.Collection {
	return s.db.Collection(name)
}

// trackInsert records an insert for potential rollback.
func (s *mongoStore) trackInsert(collection string, id interface{}) {
	if s.tracker == nil {
		return
	}
	s.tracker.recordInsert(collection, id)
}

// trackBeforeUpdate captures the before-image of a document before an
// UpdateOne or ReplaceOne operation so it can be restored on rollback.
// Must be called BEFORE the actual mutation. No-op outside transactions.
func (s *mongoStore) trackBeforeUpdate(ctx context.Context, collection string, filter bson.D) {
	if s.tracker == nil {
		return
	}
	var oldDoc bson.M
	if err := s.collection(collection).FindOne(ctx, filter).Decode(&oldDoc); err != nil {
		return // doc not found — nothing to undo
	}
	s.tracker.recordUpdate(collection, oldDoc["_id"], oldDoc)
}

// trackBeforeDelete captures the before-image of a document before a
// DeleteOne operation so it can be re-inserted on rollback.
// Must be called BEFORE the actual mutation. No-op outside transactions.
func (s *mongoStore) trackBeforeDelete(ctx context.Context, collection string, filter bson.D) {
	if s.tracker == nil {
		return
	}
	var oldDoc bson.M
	if err := s.collection(collection).FindOne(ctx, filter).Decode(&oldDoc); err != nil {
		return // doc not found — nothing to undo
	}
	s.tracker.recordDelete(collection, oldDoc)
}

// trackBeforeUpsert handles ReplaceOne with upsert=true. If the document
// exists, captures a before-image (update). If not, records the id so the
// new document is deleted on rollback (insert). No-op outside transactions.
func (s *mongoStore) trackBeforeUpsert(ctx context.Context, collection string, filter bson.D, id interface{}) {
	if s.tracker == nil {
		return
	}
	var oldDoc bson.M
	if err := s.collection(collection).FindOne(ctx, filter).Decode(&oldDoc); err != nil {
		// Doc doesn't exist — this will be a new insert.
		s.tracker.recordInsert(collection, id)
		return
	}
	// Doc exists — this is an update.
	s.tracker.recordUpdate(collection, oldDoc["_id"], oldDoc)
}

// --- Sub-store accessors ---

func initSubStores(s *mongoStore) {
	s.orgs = orgStore{s: s}
	s.users = userStore{s: s}
	s.sessions = sessionStore{s: s}
	s.orgMembers = orgMemberStore{s: s}
	s.workers = workerStore{s: s}
	s.workerAccessGrants = workerAccessGrantStore{s: s}
	s.workerNotifications = workerNotificationStore{s: s}
	s.registrations = registrationStore{s: s}
	s.workspaces = workspaceStore{s: s}
	s.workspaceAccess = workspaceAccessStore{s: s}
	s.workspaceTabs = workspaceTabStore{s: s}
	s.workspaceLayouts = workspaceLayoutStore{s: s}
	s.workspaceSections = workspaceSectionStore{s: s}
	s.workspaceSectionItems = workspaceSectionItemStore{s: s}
	s.oauthProviders = oauthProviderStore{s: s}
	s.oauthStates = oauthStateStore{s: s}
	s.oauthTokens = oauthTokenStore{s: s}
	s.oauthUserLinks = oauthUserLinkStore{s: s}
	s.pendingOAuthSignups = pendingOAuthSignupStore{s: s}
	s.cleanup = cleanupStore{s: s}
}

func (s *mongoStore) Orgs() store.OrgStore                             { return &s.orgs }
func (s *mongoStore) Users() store.UserStore                           { return &s.users }
func (s *mongoStore) Sessions() store.SessionStore                     { return &s.sessions }
func (s *mongoStore) OrgMembers() store.OrgMemberStore                 { return &s.orgMembers }
func (s *mongoStore) Workers() store.WorkerStore                       { return &s.workers }
func (s *mongoStore) WorkerAccessGrants() store.WorkerAccessGrantStore { return &s.workerAccessGrants }
func (s *mongoStore) WorkerNotifications() store.WorkerNotificationStore {
	return &s.workerNotifications
}
func (s *mongoStore) Registrations() store.RegistrationStore         { return &s.registrations }
func (s *mongoStore) Workspaces() store.WorkspaceStore               { return &s.workspaces }
func (s *mongoStore) WorkspaceAccess() store.WorkspaceAccessStore    { return &s.workspaceAccess }
func (s *mongoStore) WorkspaceTabs() store.WorkspaceTabStore         { return &s.workspaceTabs }
func (s *mongoStore) WorkspaceLayouts() store.WorkspaceLayoutStore   { return &s.workspaceLayouts }
func (s *mongoStore) WorkspaceSections() store.WorkspaceSectionStore { return &s.workspaceSections }
func (s *mongoStore) WorkspaceSectionItems() store.WorkspaceSectionItemStore {
	return &s.workspaceSectionItems
}
func (s *mongoStore) OAuthProviders() store.OAuthProviderStore { return &s.oauthProviders }
func (s *mongoStore) OAuthStates() store.OAuthStateStore       { return &s.oauthStates }
func (s *mongoStore) OAuthTokens() store.OAuthTokenStore       { return &s.oauthTokens }
func (s *mongoStore) OAuthUserLinks() store.OAuthUserLinkStore { return &s.oauthUserLinks }
func (s *mongoStore) PendingOAuthSignups() store.PendingOAuthSignupStore {
	return &s.pendingOAuthSignups
}
func (s *mongoStore) Cleanup() store.CleanupStore { return &s.cleanup }
func (s *mongoStore) Migrator() store.Migrator    { return s.mig }

// RunInTransaction executes fn with mutex serialization. Compensating
// writes are used for rollback on error: inserts are deleted, updates
// and deletes are restored from before-images. Native MongoDB
// transactions require a replica set and the session context cannot be
// propagated through the store.Store interface, so we use the
// mutex-based approach for all topologies.
//
// Note: bulk mutations (UpdateMany, DeleteMany) called inside the
// transaction callback are NOT tracked and will not be rolled back.
func (s *mongoStore) RunInTransaction(ctx context.Context, fn func(tx store.Store) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tracker := &txTracker{}
	txStore := &mongoStore{
		client:  s.client,
		db:      s.db,
		mig:     s.mig,
		tracker: tracker,
	}
	initSubStores(txStore)
	err := fn(txStore)
	if err != nil {
		// Best-effort rollback: undo all tracked writes in reverse order.
		tracker.rollback(ctx, s.db)
		return err
	}
	return nil
}

// parseWriteConcern converts a string like "majority" or "1" into a
// *writeconcern.WriteConcern. Numeric strings are treated as W values.
func parseWriteConcern(wc string) *writeconcern.WriteConcern {
	if wc == writeconcern.WCMajority {
		return writeconcern.Majority()
	}
	if n, err := strconv.Atoi(wc); err == nil {
		return &writeconcern.WriteConcern{W: n}
	}
	// Treat unknown strings as tag-set names.
	return &writeconcern.WriteConcern{W: wc}
}

// lookupUsernames fetches usernames for the given user IDs in a single query.
// Returns a map from user ID to username. Missing users are omitted.
func (s *mongoStore) lookupUsernames(ctx context.Context, userIDs []string) (map[string]string, error) {
	if len(userIDs) == 0 {
		return nil, nil
	}
	unique := store.UniqueStrings(userIDs)
	filter := bson.D{
		{Key: "_id", Value: bson.D{{Key: "$in", Value: unique}}},
		{Key: "deleted_at", Value: nil},
	}
	opts := options.Find().SetProjection(bson.D{{Key: "username", Value: 1}})
	cursor, err := s.collection(colUsers).Find(ctx, filter, opts)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = cursor.Close(ctx) }()

	result := make(map[string]string, len(unique))
	for cursor.Next(ctx) {
		var m bson.M
		if err := cursor.Decode(&m); err != nil {
			return nil, mapErr(err)
		}
		result[getS(m, "_id")] = getS(m, "username")
	}
	return result, mapErr(cursor.Err())
}

// Close disconnects the MongoDB client.
func (s *mongoStore) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.client.Disconnect(ctx)
}

// --- Transaction tracker for rollback ---

type txActionType int

const (
	txInsert txActionType = iota // rollback = delete
	txUpdate                     // rollback = replace with before-image
	txDelete                     // rollback = re-insert before-image
)

// txAction describes how to undo a single write operation.
type txAction struct {
	actionType txActionType
	collection string
	id         interface{} // _id of the document (used for insert/update rollback)
	oldDoc     bson.M      // before-image (nil for inserts)
}

// txTracker records write operations during a transaction for rollback.
type txTracker struct {
	actions []txAction
}

func (t *txTracker) recordInsert(collection string, id interface{}) {
	t.actions = append(t.actions, txAction{actionType: txInsert, collection: collection, id: id})
}

func (t *txTracker) recordUpdate(collection string, id interface{}, oldDoc bson.M) {
	t.actions = append(t.actions, txAction{actionType: txUpdate, collection: collection, id: id, oldDoc: oldDoc})
}

func (t *txTracker) recordDelete(collection string, oldDoc bson.M) {
	t.actions = append(t.actions, txAction{actionType: txDelete, collection: collection, oldDoc: oldDoc})
}

func (t *txTracker) rollback(ctx context.Context, db *mongo.Database) {
	// Undo in reverse order to handle dependencies.
	for i := len(t.actions) - 1; i >= 0; i-- {
		a := t.actions[i]
		switch a.actionType {
		case txInsert:
			_, _ = db.Collection(a.collection).DeleteOne(ctx, bson.D{{Key: "_id", Value: a.id}})
		case txUpdate:
			_, _ = db.Collection(a.collection).ReplaceOne(ctx, bson.D{{Key: "_id", Value: a.id}}, a.oldDoc)
		case txDelete:
			_, _ = db.Collection(a.collection).InsertOne(ctx, a.oldDoc)
		}
	}
}
