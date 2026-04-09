package mongodb

import (
	"context"
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/ptrconv"
)

func grantToDoc(p store.GrantWorkerAccessParams, now time.Time) bson.D {
	return bson.D{
		{Key: "_id", Value: compoundID(p.WorkerID, p.UserID)},
		{Key: "worker_id", Value: p.WorkerID},
		{Key: "user_id", Value: p.UserID},
		{Key: "granted_by", Value: p.GrantedBy},
		{Key: "created_at", Value: now},
	}
}

func docToGrant(m bson.M) store.WorkerAccessGrant {
	return store.WorkerAccessGrant{
		WorkerID:  getS(m, "worker_id"),
		UserID:    getS(m, "user_id"),
		GrantedBy: getS(m, "granted_by"),
		CreatedAt: getTime(m, "created_at"),
	}
}

func (st *workerAccessGrantStore) Grant(ctx context.Context, p store.GrantWorkerAccessParams) error {
	now := truncateMS(time.Now().UTC())
	doc := grantToDoc(p, now)
	_, err := st.s.collection(colWorkerAccessGrants).InsertOne(ctx, doc)
	if err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return nil
		}
		return mapErr(err)
	}
	st.s.trackInsert(colWorkerAccessGrants, compoundID(p.WorkerID, p.UserID))
	return nil
}

func (st *workerAccessGrantStore) Revoke(ctx context.Context, p store.RevokeWorkerAccessParams) error {
	filter := bson.D{{Key: "_id", Value: compoundID(p.WorkerID, p.UserID)}}
	st.s.trackBeforeDelete(ctx, colWorkerAccessGrants, filter)
	_, err := st.s.collection(colWorkerAccessGrants).DeleteOne(ctx, filter)
	return mapErr(err)
}

func (st *workerAccessGrantStore) List(ctx context.Context, workerID string) ([]store.WorkerAccessGrant, error) {
	filter := bson.D{{Key: "worker_id", Value: workerID}}
	cursor, err := st.s.collection(colWorkerAccessGrants).Find(ctx, filter)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = cursor.Close(ctx) }()

	var grants []store.WorkerAccessGrant
	for cursor.Next(ctx) {
		var m bson.M
		if err := cursor.Decode(&m); err != nil {
			return nil, mapErr(err)
		}
		grants = append(grants, docToGrant(m))
	}
	return ptrconv.NonNil(grants), mapErr(cursor.Err())
}

func (st *workerAccessGrantStore) HasAccess(ctx context.Context, p store.HasWorkerAccessParams) (bool, error) {
	filter := bson.D{{Key: "_id", Value: compoundID(p.WorkerID, p.UserID)}}
	err := st.s.collection(colWorkerAccessGrants).FindOne(ctx, filter, options.FindOne().SetProjection(bson.D{{Key: "_id", Value: 1}})).Err()
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return false, nil
		}
		return false, mapErr(err)
	}
	return true, nil
}

func (st *workerAccessGrantStore) DeleteByWorker(ctx context.Context, workerID string) error {
	filter := bson.D{{Key: "worker_id", Value: workerID}}
	_, err := st.s.collection(colWorkerAccessGrants).DeleteMany(ctx, filter)
	return mapErr(err)
}

func (st *workerAccessGrantStore) DeleteByUser(ctx context.Context, userID string) error {
	filter := bson.D{{Key: "user_id", Value: userID}}
	_, err := st.s.collection(colWorkerAccessGrants).DeleteMany(ctx, filter)
	return mapErr(err)
}

func (st *workerAccessGrantStore) DeleteByUserInOrg(ctx context.Context, p store.DeleteWorkerAccessGrantsByUserInOrgParams) error {
	// 1. Collect all grant worker IDs for this user.
	grantFilter := bson.D{{Key: "user_id", Value: p.UserID}}
	opts := options.Find().SetProjection(bson.D{{Key: "worker_id", Value: 1}})
	cursor, err := st.s.collection(colWorkerAccessGrants).Find(ctx, grantFilter, opts)
	if err != nil {
		return mapErr(err)
	}
	defer func() { _ = cursor.Close(ctx) }()

	var workerIDs []string
	for cursor.Next(ctx) {
		var m bson.M
		if err := cursor.Decode(&m); err != nil {
			return mapErr(err)
		}
		workerIDs = append(workerIDs, getS(m, "worker_id"))
	}
	if err := cursor.Err(); err != nil {
		return mapErr(err)
	}
	if len(workerIDs) == 0 {
		return nil
	}

	// 2. Batch-fetch registered_by for all workers.
	wFilter := bson.D{
		{Key: "_id", Value: bson.D{{Key: "$in", Value: workerIDs}}},
	}
	wOpts := options.Find().SetProjection(bson.D{
		{Key: "registered_by", Value: 1},
	})
	wCursor, err := st.s.collection(colWorkers).Find(ctx, wFilter, wOpts)
	if err != nil {
		return mapErr(err)
	}
	defer func() { _ = wCursor.Close(ctx) }()

	workerOwner := make(map[string]string)
	var ownerIDs []string
	seenOwners := make(map[string]bool)
	for wCursor.Next(ctx) {
		var wm bson.M
		if err := wCursor.Decode(&wm); err != nil {
			return mapErr(err)
		}
		wid := getS(wm, "_id")
		rb := getS(wm, "registered_by")
		workerOwner[wid] = rb
		if !seenOwners[rb] {
			seenOwners[rb] = true
			ownerIDs = append(ownerIDs, rb)
		}
	}
	if err := wCursor.Err(); err != nil {
		return mapErr(err)
	}

	// 3. Batch-check which owners are members of the org.
	memberIDs := make([]string, len(ownerIDs))
	for i, uid := range ownerIDs {
		memberIDs[i] = compoundID(p.OrgID, uid)
	}
	mFilter := bson.D{{Key: "_id", Value: bson.D{{Key: "$in", Value: memberIDs}}}}
	mOpts := options.Find().SetProjection(bson.D{{Key: "_id", Value: 1}})
	mCursor, err := st.s.collection(colOrgMembers).Find(ctx, mFilter, mOpts)
	if err != nil {
		return mapErr(err)
	}
	defer func() { _ = mCursor.Close(ctx) }()

	// Build a reverse lookup from compound ID to user ID.
	compoundToOwner := make(map[string]string, len(ownerIDs))
	for _, uid := range ownerIDs {
		compoundToOwner[compoundID(p.OrgID, uid)] = uid
	}

	orgMembers := make(map[string]bool)
	for mCursor.Next(ctx) {
		var mm bson.M
		if err := mCursor.Decode(&mm); err != nil {
			return mapErr(err)
		}
		if uid, ok := compoundToOwner[getS(mm, "_id")]; ok {
			orgMembers[uid] = true
		}
	}
	if err := mCursor.Err(); err != nil {
		return mapErr(err)
	}

	// 4. Delete grants whose worker owner is in the org.
	var deleteIDs []string
	for _, wid := range workerIDs {
		if orgMembers[workerOwner[wid]] {
			deleteIDs = append(deleteIDs, compoundID(wid, p.UserID))
		}
	}
	if len(deleteIDs) == 0 {
		return nil
	}
	_, err = st.s.collection(colWorkerAccessGrants).DeleteMany(ctx,
		bson.D{{Key: "_id", Value: bson.D{{Key: "$in", Value: deleteIDs}}}},
	)
	return mapErr(err)
}
