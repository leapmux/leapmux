package mongodb

import (
	"context"
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/ptrconv"
)

func workerToDoc(p store.CreateWorkerParams, now time.Time) bson.D {
	return bson.D{
		{Key: "_id", Value: p.ID},
		{Key: "auth_token", Value: p.AuthToken},
		{Key: "registered_by", Value: p.RegisteredBy},
		{Key: "status", Value: int32(leapmuxv1.WorkerStatus_WORKER_STATUS_ACTIVE)},
		{Key: "created_at", Value: now},
		{Key: "public_key", Value: bytesVal(p.PublicKey)},
		{Key: "mlkem_public_key", Value: bytesVal(p.MlkemPublicKey)},
		{Key: "slhdsa_public_key", Value: bytesVal(p.SlhdsaPublicKey)},
	}
}

func docToWorker(m bson.M) store.Worker {
	return store.Worker{
		ID:              getS(m, "_id"),
		AuthToken:       getS(m, "auth_token"),
		RegisteredBy:    getS(m, "registered_by"),
		Status:          leapmuxv1.WorkerStatus(getInt32(m, "status")),
		CreatedAt:       getTime(m, "created_at"),
		LastSeenAt:      getTimePtr(m, "last_seen_at"),
		PublicKey:       getBytes(m, "public_key"),
		MlkemPublicKey:  getBytes(m, "mlkem_public_key"),
		SlhdsaPublicKey: getBytes(m, "slhdsa_public_key"),
		DeletedAt:       getTimePtr(m, "deleted_at"),
	}
}

func (st *workerStore) Create(ctx context.Context, p store.CreateWorkerParams) error {
	now := truncateMS(time.Now().UTC())
	doc := workerToDoc(p, now)
	_, err := st.s.collection(colWorkers).InsertOne(ctx, doc)
	if err != nil {
		return mapErr(err)
	}
	st.s.trackInsert(colWorkers, p.ID)
	return nil
}

func (st *workerStore) GetByID(ctx context.Context, id string) (*store.Worker, error) {
	filter := bson.D{{Key: "_id", Value: id}}
	var m bson.M
	err := st.s.collection(colWorkers).FindOne(ctx, filter).Decode(&m)
	if err != nil {
		return nil, mapErr(err)
	}
	w := docToWorker(m)
	return &w, nil
}

func (st *workerStore) GetByAuthToken(ctx context.Context, token string) (*store.Worker, error) {
	filter := bson.D{
		{Key: "auth_token", Value: token},
		{Key: "deleted_at", Value: nil},
	}
	var m bson.M
	err := st.s.collection(colWorkers).FindOne(ctx, filter).Decode(&m)
	if err != nil {
		return nil, mapErr(err)
	}
	w := docToWorker(m)
	return &w, nil
}

func (st *workerStore) GetPublicKey(ctx context.Context, id string) (*store.WorkerPublicKeys, error) {
	filter := bson.D{{Key: "_id", Value: id}, {Key: "deleted_at", Value: nil}}
	opts := options.FindOne().SetProjection(bson.D{
		{Key: "public_key", Value: 1},
		{Key: "mlkem_public_key", Value: 1},
		{Key: "slhdsa_public_key", Value: 1},
	})
	var m bson.M
	err := st.s.collection(colWorkers).FindOne(ctx, filter, opts).Decode(&m)
	if err != nil {
		return nil, mapErr(err)
	}
	return &store.WorkerPublicKeys{
		PublicKey:       getBytes(m, "public_key"),
		MlkemPublicKey:  getBytes(m, "mlkem_public_key"),
		SlhdsaPublicKey: getBytes(m, "slhdsa_public_key"),
	}, nil
}

func (st *workerStore) GetOwned(ctx context.Context, p store.GetOwnedWorkerParams) (*store.Worker, error) {
	return store.GetOwnedWorker(ctx, p, st.GetByID, st.hasAccess)
}

func (st *workerStore) hasAccess(ctx context.Context, workerID, userID string) (bool, error) {
	err := st.s.collection(colWorkerAccessGrants).FindOne(ctx,
		bson.D{{Key: "_id", Value: compoundID(workerID, userID)}},
		options.FindOne().SetProjection(bson.D{{Key: "_id", Value: 1}}),
	).Err()
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return false, nil
		}
		return false, mapErr(err)
	}
	return true, nil
}

func (st *workerStore) ListByUserID(ctx context.Context, p store.ListWorkersByUserIDParams) ([]store.Worker, error) {
	filter := bson.D{
		{Key: "registered_by", Value: p.RegisteredBy},
		{Key: "status", Value: int32(leapmuxv1.WorkerStatus_WORKER_STATUS_ACTIVE)},
		{Key: "deleted_at", Value: nil},
	}
	if p.Cursor != "" {
		cursorTime, _, err := store.ParseCursorTime(p.Cursor)
		if err != nil {
			return nil, err
		}
		filter = append(filter, bson.E{Key: "created_at", Value: bson.D{{Key: "$lt", Value: cursorTime}}})
	}
	opts := options.Find().
		SetSort(bson.D{{Key: "created_at", Value: -1}}).
		SetLimit(p.Limit)
	cursor, err := st.s.collection(colWorkers).Find(ctx, filter, opts)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = cursor.Close(ctx) }()

	var workers []store.Worker
	for cursor.Next(ctx) {
		var m bson.M
		if err := cursor.Decode(&m); err != nil {
			return nil, mapErr(err)
		}
		workers = append(workers, docToWorker(m))
	}
	return ptrconv.NonNil(workers), mapErr(cursor.Err())
}

func (st *workerStore) ListOwned(ctx context.Context, p store.ListOwnedWorkersParams) ([]store.Worker, error) {
	var cursorTime time.Time
	if p.Cursor != "" {
		var err error
		cursorTime, _, err = store.ParseCursorTime(p.Cursor)
		if err != nil {
			return nil, err
		}
	}

	// 1. Fetch workers registered by the user.
	workerMap := make(map[string]store.Worker)
	regFilter := bson.D{
		{Key: "registered_by", Value: p.UserID},
		{Key: "status", Value: int32(leapmuxv1.WorkerStatus_WORKER_STATUS_ACTIVE)},
		{Key: "deleted_at", Value: nil},
	}
	if p.Cursor != "" {
		regFilter = append(regFilter, bson.E{Key: "created_at", Value: bson.D{{Key: "$lt", Value: cursorTime}}})
	}
	regOpts := options.Find().
		SetSort(bson.D{{Key: "created_at", Value: -1}}).
		SetLimit(p.Limit)
	regCursor, err := st.s.collection(colWorkers).Find(ctx, regFilter, regOpts)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = regCursor.Close(ctx) }()

	for regCursor.Next(ctx) {
		var m bson.M
		if err := regCursor.Decode(&m); err != nil {
			return nil, mapErr(err)
		}
		w := docToWorker(m)
		workerMap[w.ID] = w
	}
	if err := regCursor.Err(); err != nil {
		return nil, mapErr(err)
	}

	// 2. Query worker_access_grants for additional worker IDs.
	grantFilter := bson.D{{Key: "user_id", Value: p.UserID}}
	grantCursor, err := st.s.collection(colWorkerAccessGrants).Find(ctx, grantFilter)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = grantCursor.Close(ctx) }()

	var grantedIDs []string
	for grantCursor.Next(ctx) {
		var m bson.M
		if err := grantCursor.Decode(&m); err != nil {
			return nil, mapErr(err)
		}
		wID := getS(m, "worker_id")
		if _, exists := workerMap[wID]; !exists {
			grantedIDs = append(grantedIDs, wID)
		}
	}
	if err := grantCursor.Err(); err != nil {
		return nil, mapErr(err)
	}

	// 3. Batch-fetch granted workers using $in.
	if len(grantedIDs) > 0 {
		grantedFilter := bson.D{
			{Key: "_id", Value: bson.D{{Key: "$in", Value: grantedIDs}}},
			{Key: "status", Value: int32(leapmuxv1.WorkerStatus_WORKER_STATUS_ACTIVE)},
		}
		gCursor, err := st.s.collection(colWorkers).Find(ctx, grantedFilter)
		if err != nil {
			return nil, mapErr(err)
		}
		defer func() { _ = gCursor.Close(ctx) }()

		for gCursor.Next(ctx) {
			var m bson.M
			if err := gCursor.Decode(&m); err != nil {
				return nil, mapErr(err)
			}
			w := docToWorker(m)
			workerMap[w.ID] = w
		}
		if err := gCursor.Err(); err != nil {
			return nil, mapErr(err)
		}
	}

	// 4. Collect, apply cursor filter, sort by created_at descending, and limit.
	ptrs := make([]*store.Worker, 0, len(workerMap))
	for k := range workerMap {
		w := workerMap[k]
		ptrs = append(ptrs, &w)
	}
	paged, err := store.SortAndPaginateWorkers(ptrs, p.Cursor, p.Limit)
	if err != nil {
		return nil, err
	}
	result := make([]store.Worker, len(paged))
	for i, w := range paged {
		result[i] = *w
	}
	return ptrconv.NonNil(result), nil
}

func (st *workerStore) ListAdmin(ctx context.Context, p store.ListWorkersAdminParams) ([]store.WorkerWithOwner, error) {
	filter := bson.D{
		{Key: "deleted_at", Value: nil},
	}
	if p.UserID != nil {
		filter = append(filter, bson.E{Key: "registered_by", Value: *p.UserID})
	}
	if p.Status != nil {
		filter = append(filter, bson.E{Key: "status", Value: int32(*p.Status)})
	}
	if p.Cursor != "" {
		cursorTime, _, err := store.ParseCursorTime(p.Cursor)
		if err != nil {
			return nil, err
		}
		filter = append(filter, bson.E{Key: "created_at", Value: bson.D{{Key: "$lt", Value: cursorTime}}})
	}

	opts := options.Find().
		SetSort(bson.D{{Key: "created_at", Value: -1}}).
		SetLimit(p.Limit)
	cursor, err := st.s.collection(colWorkers).Find(ctx, filter, opts)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = cursor.Close(ctx) }()

	var workers []store.Worker
	for cursor.Next(ctx) {
		var m bson.M
		if err := cursor.Decode(&m); err != nil {
			return nil, mapErr(err)
		}
		workers = append(workers, docToWorker(m))
	}
	if err := cursor.Err(); err != nil {
		return nil, mapErr(err)
	}

	// Batch-fetch all owner usernames.
	userIDs := store.PluckStrings(workers, func(w store.Worker) string { return w.RegisteredBy })
	usernames, err := st.s.lookupUsernames(ctx, userIDs)
	if err != nil {
		return nil, err
	}

	return store.WorkersToWithOwner(workers, usernames), nil
}

func (st *workerStore) SetStatus(ctx context.Context, p store.SetWorkerStatusParams) error {
	filter := bson.D{{Key: "_id", Value: p.ID}}
	st.s.trackBeforeUpdate(ctx, colWorkers, filter)
	update := bson.D{
		{Key: "$set", Value: bson.D{
			{Key: "status", Value: int32(p.Status)},
		}},
	}
	_, err := st.s.collection(colWorkers).UpdateOne(ctx, filter, update)
	return mapErr(err)
}

func (st *workerStore) UpdateLastSeen(ctx context.Context, id string) error {
	now := truncateMS(time.Now().UTC())
	filter := bson.D{{Key: "_id", Value: id}}
	st.s.trackBeforeUpdate(ctx, colWorkers, filter)
	update := bson.D{
		{Key: "$set", Value: bson.D{
			{Key: "last_seen_at", Value: now},
		}},
	}
	_, err := st.s.collection(colWorkers).UpdateOne(ctx, filter, update)
	return mapErr(err)
}

func (st *workerStore) UpdatePublicKey(ctx context.Context, p store.UpdateWorkerPublicKeyParams) error {
	filter := bson.D{{Key: "_id", Value: p.ID}}
	st.s.trackBeforeUpdate(ctx, colWorkers, filter)
	update := bson.D{
		{Key: "$set", Value: bson.D{
			{Key: "public_key", Value: bytesVal(p.PublicKey)},
			{Key: "mlkem_public_key", Value: bytesVal(p.MlkemPublicKey)},
			{Key: "slhdsa_public_key", Value: bytesVal(p.SlhdsaPublicKey)},
		}},
	}
	_, err := st.s.collection(colWorkers).UpdateOne(ctx, filter, update)
	return mapErr(err)
}

func (st *workerStore) Deregister(ctx context.Context, p store.DeregisterWorkerParams) (int64, error) {
	filter := bson.D{
		{Key: "_id", Value: p.ID},
		{Key: "registered_by", Value: p.RegisteredBy},
		{Key: "status", Value: int32(leapmuxv1.WorkerStatus_WORKER_STATUS_ACTIVE)},
	}
	return st.deregister(ctx, filter)
}

func (st *workerStore) ForceDeregister(ctx context.Context, id string) (int64, error) {
	filter := bson.D{
		{Key: "_id", Value: id},
		{Key: "status", Value: int32(leapmuxv1.WorkerStatus_WORKER_STATUS_ACTIVE)},
	}
	return st.deregister(ctx, filter)
}

func (st *workerStore) deregister(ctx context.Context, filter bson.D) (int64, error) {
	st.s.trackBeforeUpdate(ctx, colWorkers, filter)
	update := bson.D{
		{Key: "$set", Value: bson.D{
			{Key: "status", Value: int32(leapmuxv1.WorkerStatus_WORKER_STATUS_DEREGISTERING)},
		}},
	}
	res, err := st.s.collection(colWorkers).UpdateOne(ctx, filter, update)
	if err != nil {
		return 0, mapErr(err)
	}
	return res.MatchedCount, nil
}

func (st *workerStore) MarkDeleted(ctx context.Context, id string) error {
	now := truncateMS(time.Now().UTC())
	filter := bson.D{{Key: "_id", Value: id}}
	st.s.trackBeforeUpdate(ctx, colWorkers, filter)
	update := bson.D{
		{Key: "$set", Value: bson.D{
			{Key: "status", Value: int32(leapmuxv1.WorkerStatus_WORKER_STATUS_DELETED)},
			{Key: "deleted_at", Value: now},
		}},
	}
	_, err := st.s.collection(colWorkers).UpdateOne(ctx, filter, update)
	return mapErr(err)
}

func (st *workerStore) MarkAllDeletedByUser(ctx context.Context, registeredBy string) error {
	now := truncateMS(time.Now().UTC())
	filter := bson.D{
		{Key: "registered_by", Value: registeredBy},
		{Key: "status", Value: bson.D{{Key: "$ne", Value: int32(leapmuxv1.WorkerStatus_WORKER_STATUS_DELETED)}}},
	}
	update := bson.D{
		{Key: "$set", Value: bson.D{
			{Key: "status", Value: int32(leapmuxv1.WorkerStatus_WORKER_STATUS_DELETED)},
			{Key: "deleted_at", Value: now},
		}},
	}
	_, err := st.s.collection(colWorkers).UpdateMany(ctx, filter, update)
	return mapErr(err)
}
