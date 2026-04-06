package dynamodb

import (
	"context"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/ptrconv"
)

type workerStore struct{ s *dynamoStore }

var _ store.WorkerStore = (*workerStore)(nil)

func (st *workerStore) table() string { return st.s.table(tableWorkers) }

func workerToItem(p store.CreateWorkerParams, now time.Time) map[string]ddbtypes.AttributeValue {
	return map[string]ddbtypes.AttributeValue{
		"id":                attrS(p.ID),
		"auth_token":        attrS(p.AuthToken),
		"registered_by":     attrS(p.RegisteredBy),
		"status":            attrN(int64(leapmuxv1.WorkerStatus_WORKER_STATUS_ACTIVE)),
		"created_at":        attrS(timeToStr(now)),
		"public_key":        attrB(p.PublicKey),
		"mlkem_public_key":  attrB(p.MlkemPublicKey),
		"slhdsa_public_key": attrB(p.SlhdsaPublicKey),
		"deleted":           attrS(deletedFalse),
	}
}

func itemToWorker(item map[string]ddbtypes.AttributeValue) *store.Worker {
	return &store.Worker{
		ID:              getS(item, "id"),
		AuthToken:       getS(item, "auth_token"),
		RegisteredBy:    getS(item, "registered_by"),
		Status:          leapmuxv1.WorkerStatus(getN(item, "status")),
		CreatedAt:       getTime(item, "created_at"),
		LastSeenAt:      getTimePtr(item, "last_seen_at"),
		PublicKey:       getBytes(item, "public_key"),
		MlkemPublicKey:  getBytes(item, "mlkem_public_key"),
		SlhdsaPublicKey: getBytes(item, "slhdsa_public_key"),
		DeletedAt:       getTimePtr(item, "deleted_at"),
	}
}

func (st *workerStore) Create(ctx context.Context, p store.CreateWorkerParams) error {
	now := time.Now().UTC()
	_, err := st.s.putItem(ctx, &dynamodb.PutItemInput{
		TableName:           aws.String(st.table()),
		Item:                workerToItem(p, now),
		ConditionExpression: aws.String("attribute_not_exists(id)"),
	}, "id")
	return mapErr(err)
}

func (st *workerStore) GetByID(ctx context.Context, id string) (*store.Worker, error) {
	out, err := st.s.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(st.table()),
		Key:       map[string]ddbtypes.AttributeValue{"id": attrS(id)},
	})
	if err != nil {
		return nil, mapErr(err)
	}
	if out.Item == nil {
		return nil, store.ErrNotFound
	}
	return itemToWorker(out.Item), nil
}

func (st *workerStore) GetByAuthToken(ctx context.Context, token string) (*store.Worker, error) {
	out, err := st.s.client.Query(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(st.table()),
		IndexName:              aws.String(gsiAuthToken),
		KeyConditionExpression: aws.String("auth_token = :t"),
		FilterExpression:       aws.String("attribute_not_exists(deleted_at)"),
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":t": attrS(token),
		},
	})
	if err != nil {
		return nil, mapErr(err)
	}
	if len(out.Items) == 0 {
		return nil, store.ErrNotFound
	}
	return itemToWorker(out.Items[0]), nil
}

func (st *workerStore) GetPublicKey(ctx context.Context, id string) (*store.WorkerPublicKeys, error) {
	out, err := st.s.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:            aws.String(st.table()),
		Key:                  map[string]ddbtypes.AttributeValue{"id": attrS(id)},
		ProjectionExpression: aws.String("public_key, mlkem_public_key, slhdsa_public_key, deleted_at"),
	})
	if err != nil {
		return nil, mapErr(err)
	}
	if out.Item == nil {
		return nil, store.ErrNotFound
	}
	if getTimePtr(out.Item, "deleted_at") != nil {
		return nil, store.ErrNotFound
	}
	return &store.WorkerPublicKeys{
		PublicKey:       getBytes(out.Item, "public_key"),
		MlkemPublicKey:  getBytes(out.Item, "mlkem_public_key"),
		SlhdsaPublicKey: getBytes(out.Item, "slhdsa_public_key"),
	}, nil
}

func (st *workerStore) GetOwned(ctx context.Context, p store.GetOwnedWorkerParams) (*store.Worker, error) {
	return store.GetOwnedWorker(ctx, p, st.GetByID, st.hasAccess)
}

func (st *workerStore) hasAccess(ctx context.Context, workerID, userID string) (bool, error) {
	grantOut, err := st.s.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(st.s.table(tableWorkerGrants)),
		Key: map[string]ddbtypes.AttributeValue{
			"worker_id": attrS(workerID),
			"user_id":   attrS(userID),
		},
	})
	if err != nil {
		return false, mapErr(err)
	}
	return grantOut.Item != nil, nil
}

func (st *workerStore) ListByUserID(ctx context.Context, p store.ListWorkersByUserIDParams) ([]store.Worker, error) {
	keyExpr := "registered_by = :rb"
	exprValues := map[string]ddbtypes.AttributeValue{
		":rb":     attrS(p.RegisteredBy),
		":active": attrN(int64(leapmuxv1.WorkerStatus_WORKER_STATUS_ACTIVE)),
	}
	if p.Cursor != "" {
		cursorTime, _, err := store.ParseCursorTime(p.Cursor)
		if err != nil {
			return nil, err
		}
		keyExpr = "registered_by = :rb AND created_at < :cursor"
		exprValues[":cursor"] = attrS(timeToStr(cursorTime))
	}

	var result []store.Worker
	remaining := p.Limit
	err := st.s.queryPages(ctx, &dynamodb.QueryInput{
		TableName:                 aws.String(st.table()),
		IndexName:                 aws.String(gsiRegisteredBy),
		KeyConditionExpression:    aws.String(keyExpr),
		FilterExpression:          aws.String("attribute_not_exists(deleted_at) AND #st = :active"),
		ExpressionAttributeValues: exprValues,
		ExpressionAttributeNames:  map[string]string{"#st": "status"},
		ScanIndexForward:          aws.Bool(false),
	}, func(item map[string]ddbtypes.AttributeValue) bool {
		result = append(result, *itemToWorker(item))
		remaining--
		return remaining > 0
	})
	if err != nil {
		return nil, err
	}
	return ptrconv.NonNil(result), nil
}

func (st *workerStore) ListOwned(ctx context.Context, p store.ListOwnedWorkersParams) ([]store.Worker, error) {
	cursorTime, hasCursor, err := store.ParseCursorTime(p.Cursor)
	if err != nil {
		return nil, err
	}

	// List workers registered by user + workers they have access grants for.
	workerMap := make(map[string]*store.Worker)

	// 1. Workers registered by the user.
	// The registered_by-index GSI has SK=created_at, so push the cursor
	// into the key condition to reduce read capacity usage.
	keyExpr := "registered_by = :rb"
	exprValues := map[string]ddbtypes.AttributeValue{
		":rb":     attrS(p.UserID),
		":active": attrN(int64(leapmuxv1.WorkerStatus_WORKER_STATUS_ACTIVE)),
	}
	if hasCursor {
		keyExpr += " AND created_at < :cursor"
		exprValues[":cursor"] = attrS(timeToStr(cursorTime))
	}
	err = st.s.queryPages(ctx, &dynamodb.QueryInput{
		TableName:                 aws.String(st.table()),
		IndexName:                 aws.String(gsiRegisteredBy),
		KeyConditionExpression:    aws.String(keyExpr),
		FilterExpression:          aws.String("attribute_not_exists(deleted_at) AND #st = :active"),
		ExpressionAttributeValues: exprValues,
		ExpressionAttributeNames:  map[string]string{"#st": "status"},
	}, func(item map[string]ddbtypes.AttributeValue) bool {
		w := itemToWorker(item)
		workerMap[w.ID] = w
		return true
	})
	if err != nil {
		return nil, err
	}

	// 2. Workers the user has access grants for.
	var grantedWorkerIDs []string
	err = st.s.queryPages(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(st.s.table(tableWorkerGrants)),
		IndexName:              aws.String(gsiUserID),
		KeyConditionExpression: aws.String("user_id = :uid"),
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":uid": attrS(p.UserID),
		},
	}, func(item map[string]ddbtypes.AttributeValue) bool {
		workerID := getS(item, "worker_id")
		if _, exists := workerMap[workerID]; !exists {
			grantedWorkerIDs = append(grantedWorkerIDs, workerID)
		}
		return true
	})
	if err != nil {
		return nil, err
	}

	// Batch-fetch granted workers.
	if len(grantedWorkerIDs) > 0 {
		keys := make([]map[string]ddbtypes.AttributeValue, len(grantedWorkerIDs))
		for i, id := range grantedWorkerIDs {
			keys[i] = map[string]ddbtypes.AttributeValue{"id": attrS(id)}
		}
		items, err := st.s.batchGetItems(ctx, st.table(), keys)
		if err != nil {
			return nil, err
		}
		for _, item := range items {
			w := itemToWorker(item)
			if w.DeletedAt == nil {
				workerMap[w.ID] = w
			}
		}
	}

	var all []*store.Worker
	for _, w := range workerMap {
		all = append(all, w)
	}

	paged, err := store.SortAndPaginateWorkers(all, p.Cursor, p.Limit)
	if err != nil {
		return nil, err
	}
	result := make([]store.Worker, len(paged))
	for i, w := range paged {
		result[i] = *w
	}
	return result, nil
}

func (st *workerStore) ListAdmin(ctx context.Context, p store.ListWorkersAdminParams) ([]store.WorkerWithOwner, error) {
	var workers []*store.Worker

	if p.UserID != nil {
		// Use registered_by-index GSI when filtering by user.
		var err error
		workers, err = st.listAdminByUser(ctx, p)
		if err != nil {
			return nil, err
		}
	} else {
		// Use deleted-created_at-index GSI for unfiltered listing.
		var err error
		workers, err = st.listAdminAll(ctx, p)
		if err != nil {
			return nil, err
		}
	}

	userIDs := make([]string, len(workers))
	for i, w := range workers {
		userIDs[i] = w.RegisteredBy
	}
	usernames, err := st.s.lookupUsernames(ctx, userIDs)
	if err != nil {
		return nil, err
	}

	derefed := make([]store.Worker, len(workers))
	for i, w := range workers {
		derefed[i] = *w
	}
	return store.WorkersToWithOwner(derefed, usernames), nil
}

func (st *workerStore) listAdminAll(ctx context.Context, p store.ListWorkersAdminParams) ([]*store.Worker, error) {
	keyExpr, exprValues, err := buildNotDeletedCursorExpr(p.Cursor)
	if err != nil {
		return nil, err
	}

	var filterExpr *string
	var exprNames map[string]string
	if p.Status != nil {
		filterExpr = aws.String("#st = :status")
		exprValues[":status"] = attrN(int64(*p.Status))
		exprNames = map[string]string{"#st": "status"}
	}

	input := &dynamodb.QueryInput{
		TableName:                 aws.String(st.table()),
		IndexName:                 aws.String(gsiDeletedCreatedAt),
		KeyConditionExpression:    aws.String(keyExpr),
		ExpressionAttributeValues: exprValues,
		ScanIndexForward:          aws.Bool(false),
	}
	if filterExpr != nil {
		input.FilterExpression = filterExpr
	}
	if len(exprNames) > 0 {
		input.ExpressionAttributeNames = exprNames
	}

	var workers []*store.Worker
	err = st.s.queryPages(ctx, input, func(item map[string]ddbtypes.AttributeValue) bool {
		workers = append(workers, itemToWorker(item))
		return p.Limit <= 0 || int64(len(workers)) < p.Limit
	})
	if err != nil {
		return nil, err
	}

	return workers, nil
}

func (st *workerStore) listAdminByUser(ctx context.Context, p store.ListWorkersAdminParams) ([]*store.Worker, error) {
	keyExpr := "registered_by = :uid"
	exprValues := map[string]ddbtypes.AttributeValue{
		":uid": attrS(*p.UserID),
	}
	exprNames := map[string]string{}

	if p.Cursor != "" {
		cursorTime, _, err := store.ParseCursorTime(p.Cursor)
		if err != nil {
			return nil, err
		}
		keyExpr = "registered_by = :uid AND created_at < :cursor"
		exprValues[":cursor"] = attrS(timeToStr(cursorTime))
	}

	var filterParts []string
	filterParts = append(filterParts, "attribute_not_exists(deleted_at)")
	if p.Status != nil {
		filterParts = append(filterParts, "#st = :status")
		exprValues[":status"] = attrN(int64(*p.Status))
		exprNames["#st"] = "status"
	}

	filterExpr := filterParts[0]
	for _, part := range filterParts[1:] {
		filterExpr += " AND " + part
	}

	input := &dynamodb.QueryInput{
		TableName:                 aws.String(st.table()),
		IndexName:                 aws.String(gsiRegisteredBy),
		KeyConditionExpression:    aws.String(keyExpr),
		FilterExpression:          aws.String(filterExpr),
		ExpressionAttributeValues: exprValues,
		ScanIndexForward:          aws.Bool(false),
	}
	if len(exprNames) > 0 {
		input.ExpressionAttributeNames = exprNames
	}

	var workers []*store.Worker
	err := st.s.queryPages(ctx, input, func(item map[string]ddbtypes.AttributeValue) bool {
		workers = append(workers, itemToWorker(item))
		return p.Limit <= 0 || int64(len(workers)) < p.Limit
	})
	if err != nil {
		return nil, err
	}

	return workers, nil
}

func (st *workerStore) SetStatus(ctx context.Context, p store.SetWorkerStatusParams) error {
	_, err := st.s.updateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:           aws.String(st.table()),
		Key:                 map[string]ddbtypes.AttributeValue{"id": attrS(p.ID)},
		UpdateExpression:    aws.String("SET #st = :status"),
		ConditionExpression: aws.String("attribute_exists(id) AND attribute_not_exists(deleted_at)"),
		ExpressionAttributeNames: map[string]string{
			"#st": "status",
		},
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":status": attrN(int64(p.Status)),
		},
	})
	if isConditionFailed(err) {
		return nil
	}
	return mapErr(err)
}

func (st *workerStore) UpdateLastSeen(ctx context.Context, id string) error {
	now := timeToStr(time.Now().UTC())
	_, err := st.s.updateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:           aws.String(st.table()),
		Key:                 map[string]ddbtypes.AttributeValue{"id": attrS(id)},
		UpdateExpression:    aws.String("SET last_seen_at = :now"),
		ConditionExpression: aws.String("attribute_exists(id) AND attribute_not_exists(deleted_at)"),
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":now": attrS(now),
		},
	})
	return mapErr(err)
}

func (st *workerStore) UpdatePublicKey(ctx context.Context, p store.UpdateWorkerPublicKeyParams) error {
	_, err := st.s.updateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:           aws.String(st.table()),
		Key:                 map[string]ddbtypes.AttributeValue{"id": attrS(p.ID)},
		UpdateExpression:    aws.String("SET public_key = :pk, mlkem_public_key = :mk, slhdsa_public_key = :sk"),
		ConditionExpression: aws.String("attribute_exists(id) AND attribute_not_exists(deleted_at)"),
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":pk": attrB(p.PublicKey),
			":mk": attrB(p.MlkemPublicKey),
			":sk": attrB(p.SlhdsaPublicKey),
		},
	})
	return mapErr(err)
}

func (st *workerStore) Deregister(ctx context.Context, p store.DeregisterWorkerParams) (int64, error) {
	out, err := st.s.updateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:           aws.String(st.table()),
		Key:                 map[string]ddbtypes.AttributeValue{"id": attrS(p.ID)},
		UpdateExpression:    aws.String("SET #st = :deregistering"),
		ConditionExpression: aws.String("attribute_exists(id) AND registered_by = :rb AND attribute_not_exists(deleted_at) AND #st <> :deregistering"),
		ExpressionAttributeNames: map[string]string{
			"#st": "status",
		},
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":deregistering": attrN(int64(leapmuxv1.WorkerStatus_WORKER_STATUS_DEREGISTERING)),
			":rb":            attrS(p.RegisteredBy),
		},
		ReturnValues: ddbtypes.ReturnValueAllNew,
	})
	if err != nil {
		if isConditionFailed(err) {
			return 0, nil
		}
		return 0, mapErr(err)
	}
	if out.Attributes == nil {
		return 0, nil
	}
	return 1, nil
}

func (st *workerStore) ForceDeregister(ctx context.Context, id string) (int64, error) {
	out, err := st.s.updateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:           aws.String(st.table()),
		Key:                 map[string]ddbtypes.AttributeValue{"id": attrS(id)},
		UpdateExpression:    aws.String("SET #st = :deregistering"),
		ConditionExpression: aws.String("attribute_exists(id) AND attribute_not_exists(deleted_at) AND #st <> :deregistering"),
		ExpressionAttributeNames: map[string]string{
			"#st": "status",
		},
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":deregistering": attrN(int64(leapmuxv1.WorkerStatus_WORKER_STATUS_DEREGISTERING)),
		},
		ReturnValues: ddbtypes.ReturnValueAllNew,
	})
	if err != nil {
		if isConditionFailed(err) {
			return 0, nil
		}
		return 0, mapErr(err)
	}
	if out.Attributes == nil {
		return 0, nil
	}
	return 1, nil
}

func (st *workerStore) MarkDeleted(ctx context.Context, id string) error {
	now := timeToStr(time.Now().UTC())
	_, err := st.s.updateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:           aws.String(st.table()),
		Key:                 map[string]ddbtypes.AttributeValue{"id": attrS(id)},
		UpdateExpression:    aws.String("SET deleted_at = :now, deleted = :del"),
		ConditionExpression: aws.String("attribute_exists(id) AND attribute_not_exists(deleted_at)"),
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":now": attrS(now),
			":del": attrS(deletedTrue),
		},
	})
	if isConditionFailed(err) {
		return nil
	}
	return mapErr(err)
}

func (st *workerStore) MarkAllDeletedByUser(ctx context.Context, registeredBy string) error {
	now := timeToStr(time.Now().UTC())

	// Collect all worker IDs from paginated GSI query.
	var ids []string
	err := st.s.queryPages(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(st.table()),
		IndexName:              aws.String(gsiRegisteredBy),
		KeyConditionExpression: aws.String("registered_by = :rb"),
		FilterExpression:       aws.String("attribute_not_exists(deleted_at)"),
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":rb": attrS(registeredBy),
		},
		ProjectionExpression: aws.String("id"),
	}, func(item map[string]ddbtypes.AttributeValue) bool {
		ids = append(ids, getS(item, "id"))
		return true
	})
	if err != nil {
		return err
	}

	for _, id := range ids {
		if _, err := st.s.updateItem(ctx, &dynamodb.UpdateItemInput{
			TableName:        aws.String(st.table()),
			Key:              map[string]ddbtypes.AttributeValue{"id": attrS(id)},
			UpdateExpression: aws.String("SET deleted_at = :now, deleted = :del"),
			ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
				":now": attrS(now),
				":del": attrS(deletedTrue),
			},
		}); err != nil {
			return mapErr(err)
		}
	}
	return nil
}
