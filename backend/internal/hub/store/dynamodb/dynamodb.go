// Package dynamodb implements the Hub store backed by Amazon DynamoDB.
//
// Each domain entity has its own table, prefixed with a configurable string.
// Queries are hand-written using the AWS SDK v2. For our use cases (bootstrap:
// create org + user + member), a mutex-serialized approach is used where
// reads execute immediately and writes execute immediately within a
// serialized callback.
package dynamodb

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/leapmux/leapmux/internal/hub/store"
)

const (
	// tableWaitTimeout is the maximum time to wait for a table to become ACTIVE.
	tableWaitTimeout = 2 * time.Minute
)

// dynamoStore implements store.Store backed by DynamoDB.
type dynamoStore struct {
	client    *dynamodb.Client
	prefix    string
	mig       *dynamoMigrator
	mu        sync.Mutex // serializes RunInTransaction
	txTracker *txTracker // non-nil inside RunInTransaction for rollback

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

var _ store.Store = (*dynamoStore)(nil)

// Options configures the DynamoDB store.
type Options struct {
	// Client is the DynamoDB client to use.
	Client *dynamodb.Client
	// Prefix is prepended to all table names (e.g. "leapmux_").
	Prefix string
	// CreateTables controls whether tables are auto-created during migration.
	// Default: true.
	CreateTables bool
	// PointInTimeRecovery enables PITR on created tables.
	PointInTimeRecovery bool
}

// New creates a new DynamoDB-backed Store and runs migrations before
// returning. Use ctx to control the timeout for the migration step.
func New(ctx context.Context, opts Options) (store.Store, error) {
	mig := newMigrator(opts)
	st := &dynamoStore{
		client: opts.Client,
		prefix: opts.Prefix,
		mig:    mig,
	}
	initSubStores(st)
	if err := st.mig.Migrate(ctx); err != nil {
		return nil, fmt.Errorf("dynamodb migrate: %w", err)
	}
	return st, nil
}

func (s *dynamoStore) table(name string) string {
	return s.prefix + name
}

// putItem executes a PutItem and tracks it for rollback when inside a transaction.
// The keyAttrs are the attribute names that form the primary key (pk, and
// optionally sk), used to build a delete key for rollback of new items.
func (s *dynamoStore) putItem(ctx context.Context, input *dynamodb.PutItemInput, keyAttrs ...string) (*dynamodb.PutItemOutput, error) {
	if s.txTracker != nil {
		input.ReturnValues = ddbtypes.ReturnValueAllOld
	}
	out, err := s.client.PutItem(ctx, input)
	if err != nil {
		return out, err
	}
	if s.txTracker != nil {
		key := make(map[string]ddbtypes.AttributeValue, len(keyAttrs))
		for _, attr := range keyAttrs {
			if v, ok := input.Item[attr]; ok {
				key[attr] = v
			}
		}
		s.txTracker.recordPut(aws.ToString(input.TableName), key, out.Attributes)
	}
	return out, nil
}

// updateItem executes an UpdateItem and tracks it for rollback when inside a transaction.
func (s *dynamoStore) updateItem(ctx context.Context, input *dynamodb.UpdateItemInput) (*dynamodb.UpdateItemOutput, error) {
	if s.txTracker != nil {
		input.ReturnValues = ddbtypes.ReturnValueAllOld
	}
	out, err := s.client.UpdateItem(ctx, input)
	if err != nil {
		return out, err
	}
	if s.txTracker != nil {
		s.txTracker.recordUpdate(aws.ToString(input.TableName), input.Key, out.Attributes)
	}
	return out, nil
}

// deleteItem executes a DeleteItem and tracks it for rollback when inside a transaction.
func (s *dynamoStore) deleteItem(ctx context.Context, input *dynamodb.DeleteItemInput) (*dynamodb.DeleteItemOutput, error) {
	if s.txTracker != nil {
		input.ReturnValues = ddbtypes.ReturnValueAllOld
	}
	out, err := s.client.DeleteItem(ctx, input)
	if err != nil {
		return out, err
	}
	if s.txTracker != nil {
		s.txTracker.recordDelete(aws.ToString(input.TableName), out.Attributes)
	}
	return out, nil
}

func initSubStores(s *dynamoStore) {
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

func (s *dynamoStore) Orgs() store.OrgStore                             { return &s.orgs }
func (s *dynamoStore) Users() store.UserStore                           { return &s.users }
func (s *dynamoStore) Sessions() store.SessionStore                     { return &s.sessions }
func (s *dynamoStore) OrgMembers() store.OrgMemberStore                 { return &s.orgMembers }
func (s *dynamoStore) Workers() store.WorkerStore                       { return &s.workers }
func (s *dynamoStore) WorkerAccessGrants() store.WorkerAccessGrantStore { return &s.workerAccessGrants }
func (s *dynamoStore) WorkerNotifications() store.WorkerNotificationStore {
	return &s.workerNotifications
}
func (s *dynamoStore) Registrations() store.RegistrationStore         { return &s.registrations }
func (s *dynamoStore) Workspaces() store.WorkspaceStore               { return &s.workspaces }
func (s *dynamoStore) WorkspaceAccess() store.WorkspaceAccessStore    { return &s.workspaceAccess }
func (s *dynamoStore) WorkspaceTabs() store.WorkspaceTabStore         { return &s.workspaceTabs }
func (s *dynamoStore) WorkspaceLayouts() store.WorkspaceLayoutStore   { return &s.workspaceLayouts }
func (s *dynamoStore) WorkspaceSections() store.WorkspaceSectionStore { return &s.workspaceSections }
func (s *dynamoStore) WorkspaceSectionItems() store.WorkspaceSectionItemStore {
	return &s.workspaceSectionItems
}
func (s *dynamoStore) OAuthProviders() store.OAuthProviderStore { return &s.oauthProviders }
func (s *dynamoStore) OAuthStates() store.OAuthStateStore       { return &s.oauthStates }
func (s *dynamoStore) OAuthTokens() store.OAuthTokenStore       { return &s.oauthTokens }
func (s *dynamoStore) OAuthUserLinks() store.OAuthUserLinkStore { return &s.oauthUserLinks }
func (s *dynamoStore) PendingOAuthSignups() store.PendingOAuthSignupStore {
	return &s.pendingOAuthSignups
}
func (s *dynamoStore) Cleanup() store.CleanupStore { return &s.cleanup }
func (s *dynamoStore) Migrator() store.Migrator    { return s.mig }

// RunInTransaction serializes the callback with a mutex. DynamoDB does not
// support interactive transactions, so for our use cases (bootstrap: create
// org + user + member), serializing with a mutex and executing writes
// immediately is correct for single-instance hub deployments.
//
// All write operations (PutItem, UpdateItem, DeleteItem) performed during the
// callback are tracked with before-images for comprehensive best-effort
// rollback on error.
func (s *dynamoStore) RunInTransaction(ctx context.Context, fn func(tx store.Store) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tracker := &txTracker{}
	txStore := &dynamoStore{
		client:    s.client,
		prefix:    s.prefix,
		mig:       s.mig,
		txTracker: tracker,
	}
	initSubStores(txStore)
	err := fn(txStore)
	if err != nil {
		// Best-effort rollback: undo all writes in reverse order.
		tracker.rollback(ctx, s.client)
		return err
	}
	return nil
}

// txAction describes how to undo a single write operation.
type txAction struct {
	tableName string
	key       map[string]ddbtypes.AttributeValue
	oldItem   map[string]ddbtypes.AttributeValue // nil for new items (rollback = delete)
}

// txTracker records write operations during a transaction for rollback.
type txTracker struct {
	actions []txAction
}

func (t *txTracker) recordPut(tableName string, key, oldItem map[string]ddbtypes.AttributeValue) {
	t.actions = append(t.actions, txAction{tableName: tableName, key: key, oldItem: oldItem})
}

func (t *txTracker) recordUpdate(tableName string, key, oldItem map[string]ddbtypes.AttributeValue) {
	if oldItem == nil {
		return // update on non-existent item — nothing to undo
	}
	t.actions = append(t.actions, txAction{tableName: tableName, key: key, oldItem: oldItem})
}

func (t *txTracker) recordDelete(tableName string, oldItem map[string]ddbtypes.AttributeValue) {
	if oldItem == nil {
		return // item didn't exist — nothing to undo
	}
	t.actions = append(t.actions, txAction{tableName: tableName, oldItem: oldItem})
}

func (t *txTracker) rollback(ctx context.Context, client *dynamodb.Client) {
	for i := len(t.actions) - 1; i >= 0; i-- {
		a := t.actions[i]
		if a.oldItem != nil {
			// Restore the before-image.
			_, _ = client.PutItem(ctx, &dynamodb.PutItemInput{
				TableName: aws.String(a.tableName),
				Item:      a.oldItem,
			})
		} else {
			// New item — delete it.
			_, _ = client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
				TableName: aws.String(a.tableName),
				Key:       a.key,
			})
		}
	}
}

// lookupUsernames fetches usernames for the given user IDs in batch.
// Returns a map from user ID to username. Missing users are omitted.
func (s *dynamoStore) lookupUsernames(ctx context.Context, userIDs []string) (map[string]string, error) {
	if len(userIDs) == 0 {
		return nil, nil
	}
	unique := store.UniqueStrings(userIDs)
	keys := make([]map[string]ddbtypes.AttributeValue, len(unique))
	for i, uid := range unique {
		keys[i] = map[string]ddbtypes.AttributeValue{attrID: attrS(uid)}
	}

	items, err := s.batchGetItemsProjected(ctx, s.table(tableUsers), keys, attrID+", "+attrUsername+", "+attrDeletedAt)
	if err != nil {
		return nil, err
	}
	result := make(map[string]string, len(items))
	for _, item := range items {
		// Skip deleted users.
		if getTimePtr(item, attrDeletedAt) != nil {
			continue
		}
		result[getS(item, attrID)] = getS(item, attrUsername)
	}
	return result, nil
}

// Close is a no-op for DynamoDB (the SDK client has no Close method).
func (s *dynamoStore) Close() error {
	return nil
}

// batchGetMax is the DynamoDB BatchGetItem limit (100 keys per request).
const batchGetMax = 100

// batchRetryBaseDelay is the initial backoff delay for unprocessed items.
const batchRetryBaseDelay = 50 * time.Millisecond

// batchRetryMaxDelay is the maximum backoff delay for unprocessed items.
const batchRetryMaxDelay = 5 * time.Second

// batchGetItems fetches items from a single table by primary key in batches of 100,
// retrying any unprocessed keys with exponential backoff. Returns a slice of
// item attribute maps.
func (s *dynamoStore) batchGetItems(ctx context.Context, table string, keys []map[string]ddbtypes.AttributeValue) ([]map[string]ddbtypes.AttributeValue, error) {
	return s.batchGetItemsProjected(ctx, table, keys, "")
}

// batchGetItemsProjected is like batchGetItems but accepts an optional
// ProjectionExpression to limit which attributes are returned.
func (s *dynamoStore) batchGetItemsProjected(ctx context.Context, table string, keys []map[string]ddbtypes.AttributeValue, projection string) ([]map[string]ddbtypes.AttributeValue, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	var results []map[string]ddbtypes.AttributeValue
	for i := 0; i < len(keys); i += batchGetMax {
		end := i + batchGetMax
		if end > len(keys) {
			end = len(keys)
		}
		pending := keys[i:end]
		delay := batchRetryBaseDelay
		for len(pending) > 0 {
			ka := ddbtypes.KeysAndAttributes{Keys: pending}
			if projection != "" {
				ka.ProjectionExpression = aws.String(projection)
			}
			out, err := s.client.BatchGetItem(ctx, &dynamodb.BatchGetItemInput{
				RequestItems: map[string]ddbtypes.KeysAndAttributes{
					table: ka,
				},
			})
			if err != nil {
				return nil, mapErr(err)
			}
			results = append(results, out.Responses[table]...)
			unprocessed := out.UnprocessedKeys[table]
			if unprocessed.Keys == nil {
				break
			}
			pending = unprocessed.Keys
			if err := sleepWithContext(ctx, delay); err != nil {
				return nil, err
			}
			delay = min(delay*2, batchRetryMaxDelay)
		}
	}
	return results, nil
}

// batchWriteMax is the DynamoDB BatchWriteItem limit.
const batchWriteMax = 25

// batchWrite sends WriteRequests in chunks of 25, retrying unprocessed items
// with exponential backoff.
func (s *dynamoStore) batchWrite(ctx context.Context, table string, requests []ddbtypes.WriteRequest) error {
	for i := 0; i < len(requests); i += batchWriteMax {
		end := i + batchWriteMax
		if end > len(requests) {
			end = len(requests)
		}
		pending := requests[i:end]
		delay := batchRetryBaseDelay
		for len(pending) > 0 {
			out, err := s.client.BatchWriteItem(ctx, &dynamodb.BatchWriteItemInput{
				RequestItems: map[string][]ddbtypes.WriteRequest{table: pending},
			})
			if err != nil {
				return mapErr(err)
			}
			pending = out.UnprocessedItems[table]
			if len(pending) == 0 {
				break
			}
			if err := sleepWithContext(ctx, delay); err != nil {
				return err
			}
			delay = min(delay*2, batchRetryMaxDelay)
		}
	}
	return nil
}

// sleepWithContext sleeps for the given duration or until the context is canceled.
func sleepWithContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// queryPages executes a Query and calls fn for each item across all pages.
// If fn returns false, pagination stops early.
func (s *dynamoStore) queryPages(ctx context.Context, input *dynamodb.QueryInput, fn func(item map[string]ddbtypes.AttributeValue) bool) error {
	for {
		out, err := s.client.Query(ctx, input)
		if err != nil {
			return mapErr(err)
		}
		for _, item := range out.Items {
			if !fn(item) {
				return nil
			}
		}
		if out.LastEvaluatedKey == nil {
			return nil
		}
		input.ExclusiveStartKey = out.LastEvaluatedKey
	}
}

// scanPages executes a Scan and calls fn for each item across all pages.
// If fn returns false, pagination stops early.
func (s *dynamoStore) scanPages(ctx context.Context, input *dynamodb.ScanInput, fn func(item map[string]ddbtypes.AttributeValue) bool) error {
	for {
		out, err := s.client.Scan(ctx, input)
		if err != nil {
			return mapErr(err)
		}
		for _, item := range out.Items {
			if !fn(item) {
				return nil
			}
		}
		if out.LastEvaluatedKey == nil {
			return nil
		}
		input.ExclusiveStartKey = out.LastEvaluatedKey
	}
}

// buildNotDeletedCursorExpr builds a KeyConditionExpression and expression
// attribute values for querying the deleted-created_at-index GSI with
// optional cursor-based pagination.
func buildNotDeletedCursorExpr(cursor string) (string, map[string]ddbtypes.AttributeValue, error) {
	keyExpr := "deleted = :del"
	exprValues := map[string]ddbtypes.AttributeValue{":del": attrS(deletedFalse)}
	if cursor != "" {
		t, _, err := store.ParseCursorTime(cursor)
		if err != nil {
			return "", nil, err
		}
		keyExpr = "deleted = :del AND created_at < :cursor"
		exprValues[":cursor"] = attrS(timeToStr(t))
	}
	return keyExpr, exprValues, nil
}

// batchDelete deletes items from a single table by primary key using BatchWriteItem.
func (s *dynamoStore) batchDelete(ctx context.Context, table string, keys []map[string]ddbtypes.AttributeValue) error {
	if len(keys) == 0 {
		return nil
	}
	requests := make([]ddbtypes.WriteRequest, len(keys))
	for i, key := range keys {
		requests[i] = ddbtypes.WriteRequest{
			DeleteRequest: &ddbtypes.DeleteRequest{Key: key},
		}
	}
	return s.batchWrite(ctx, table, requests)
}

// deleteAllByQuery paginates through a Query, collecting keys in batches of
// batchWriteMax and deleting each batch before moving on. This keeps memory
// usage bounded regardless of the total number of matching items.
// If indexName is non-empty, the query targets that GSI.
func deleteAllByQuery(ctx context.Context, s *dynamoStore, tableName, indexName, queryKeyName, queryKeyValue, pkAttr, skAttr string) error {
	exprNames := map[string]string{"#qk": queryKeyName, "#pk": pkAttr}
	proj := "#pk"
	if skAttr != "" {
		exprNames["#sk"] = skAttr
		proj += ", #sk"
	}

	var idxName *string
	if indexName != "" {
		idxName = aws.String(indexName)
	}

	// Use a small page size so each query returns at most batchWriteMax items,
	// allowing us to delete immediately without buffering beyond one batch.
	limit := int32(batchWriteMax)

	var lastKey map[string]ddbtypes.AttributeValue
	for {
		out, err := s.client.Query(ctx, &dynamodb.QueryInput{
			TableName:                aws.String(tableName),
			IndexName:                idxName,
			KeyConditionExpression:   aws.String("#qk = :v"),
			ProjectionExpression:     aws.String(proj),
			ExpressionAttributeNames: exprNames,
			ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
				":v": attrS(queryKeyValue),
			},
			ExclusiveStartKey: lastKey,
			Limit:             &limit,
		})
		if err != nil {
			return mapErr(err)
		}
		if len(out.Items) > 0 {
			keys := make([]map[string]ddbtypes.AttributeValue, len(out.Items))
			for i, item := range out.Items {
				key := map[string]ddbtypes.AttributeValue{
					pkAttr: attrS(getS(item, pkAttr)),
				}
				if skAttr != "" {
					key[skAttr] = attrS(getS(item, skAttr))
				}
				keys[i] = key
			}
			if err := s.batchDelete(ctx, tableName, keys); err != nil {
				return err
			}
		}
		if out.LastEvaluatedKey == nil {
			return nil
		}
		lastKey = out.LastEvaluatedKey
	}
}

// deleteAllByPK queries all items with the given PK and batch-deletes them.
func deleteAllByPK(ctx context.Context, s *dynamoStore, tableName, pkName, pkValue, skName string) error {
	return deleteAllByQuery(ctx, s, tableName, "", pkName, pkValue, pkName, skName)
}

// deleteAllByGSI queries items from a GSI and batch-deletes them using the table's key.
func deleteAllByGSI(ctx context.Context, s *dynamoStore, tableName, indexName, gsiKeyName, gsiKeyValue, tablePK, tableSK string) error {
	return deleteAllByQuery(ctx, s, tableName, indexName, gsiKeyName, gsiKeyValue, tablePK, tableSK)
}
