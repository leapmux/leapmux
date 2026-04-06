package mongodb

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

func (st *cleanupStore) HardDeleteExpiredSessions(ctx context.Context) (int64, error) {
	return st.deleteExpiredBefore(ctx, colSessions, "expires_at", time.Now().UTC())
}

func (st *cleanupStore) HardDeleteWorkspacesBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	ids, n, err := st.hardDeleteSoftDeletedBefore(ctx, colWorkspaces, cutoff)
	if err != nil || len(ids) == 0 {
		return n, err
	}

	// Cascade to children.
	inFilter := bson.D{{Key: "$in", Value: ids}}
	_ = st.cascadeDelete(ctx, colWorkspaceAccess, "workspace_id", inFilter)
	_ = st.cascadeDelete(ctx, colWorkspaceTabs, "workspace_id", inFilter)
	_ = st.cascadeDeleteByID(ctx, colWorkspaceLayouts, inFilter)
	_ = st.cascadeDelete(ctx, colWorkspaceSectionItems, "workspace_id", inFilter)

	return n, nil
}

func (st *cleanupStore) HardDeleteWorkersBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	ids, n, err := st.hardDeleteSoftDeletedBefore(ctx, colWorkers, cutoff)
	if err != nil || len(ids) == 0 {
		return n, err
	}

	// Cascade to children.
	inFilter := bson.D{{Key: "$in", Value: ids}}
	_ = st.cascadeDelete(ctx, colWorkerAccessGrants, "worker_id", inFilter)
	_ = st.cascadeDelete(ctx, colWorkerNotifications, "worker_id", inFilter)
	_ = st.cascadeDelete(ctx, colWorkspaceTabs, "worker_id", inFilter)

	return n, nil
}

func (st *cleanupStore) HardDeleteExpiredRegistrationsBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	filter := bson.D{
		{Key: "status", Value: bson.D{{Key: "$in", Value: bson.A{
			int32(leapmuxv1.RegistrationStatus_REGISTRATION_STATUS_EXPIRED),
			int32(leapmuxv1.RegistrationStatus_REGISTRATION_STATUS_APPROVED),
		}}}},
		{Key: "created_at", Value: bson.D{{Key: "$lt", Value: cutoff}}},
	}
	_, n, err := st.findAndDeleteLimited(ctx, colRegistrations, filter, 1000)
	return n, err
}

func (st *cleanupStore) HardDeleteUsersBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	ids, n, err := st.hardDeleteSoftDeletedBefore(ctx, colUsers, cutoff)
	if err != nil || len(ids) == 0 {
		return n, err
	}

	// Cascade to children. Sessions, workspaces, and workers are cleaned
	// before users by the caller's ordering, so only remaining children
	// need cleanup here.
	inFilter := bson.D{{Key: "$in", Value: ids}}
	_ = st.cascadeDelete(ctx, colOrgMembers, "user_id", inFilter)
	_ = st.cascadeDelete(ctx, colWorkspaceSections, "user_id", inFilter)
	_ = st.cascadeDelete(ctx, colWorkspaceSectionItems, "user_id", inFilter)
	_ = st.cascadeDelete(ctx, colWorkerAccessGrants, "user_id", inFilter)
	_ = st.cascadeDelete(ctx, colWorkspaceAccess, "user_id", inFilter)
	_ = st.cascadeDelete(ctx, colOAuthTokens, "user_id", inFilter)
	_ = st.cascadeDelete(ctx, colOAuthUserLinks, "user_id", inFilter)

	return n, nil
}

func (st *cleanupStore) HardDeleteOrgsBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	ids, n, err := st.hardDeleteSoftDeletedBefore(ctx, colOrgs, cutoff)
	if err != nil || len(ids) == 0 {
		return n, err
	}

	// Cascade to children.
	inFilter := bson.D{{Key: "$in", Value: ids}}
	_ = st.cascadeDelete(ctx, colOrgMembers, "org_id", inFilter)

	return n, nil
}

func (st *cleanupStore) DeleteExpiredOAuthStates(ctx context.Context) (int64, error) {
	return st.deleteExpiredBefore(ctx, colOAuthStates, "expires_at", time.Now().UTC())
}

func (st *cleanupStore) DeleteExpiredPendingOAuthSignups(ctx context.Context) (int64, error) {
	return st.deleteExpiredBefore(ctx, colPendingOAuthSignups, "expires_at", time.Now().UTC())
}

// deleteExpiredBefore deletes all documents where field is before the given time.
func (st *cleanupStore) deleteExpiredBefore(ctx context.Context, col, field string, before time.Time) (int64, error) {
	t := truncateMS(before)
	filter := bson.D{{Key: field, Value: bson.D{{Key: "$lt", Value: t}}}}
	res, err := st.s.collection(col).DeleteMany(ctx, filter)
	if err != nil {
		return 0, mapErr(err)
	}
	return res.DeletedCount, nil
}

// hardDeleteSoftDeletedBefore deletes up to 1000 soft-deleted documents
// whose deleted_at is before the cutoff. Returns the deleted IDs, the
// count, and any error.
func (st *cleanupStore) hardDeleteSoftDeletedBefore(ctx context.Context, col string, cutoff time.Time) ([]interface{}, int64, error) {
	filter := bson.D{
		{Key: "deleted_at", Value: bson.D{
			{Key: "$ne", Value: nil},
			{Key: "$lt", Value: cutoff},
		}},
	}
	return st.findAndDeleteLimited(ctx, col, filter, 1000)
}

// findAndDeleteLimited finds up to limit documents matching filter,
// collects their IDs, then deletes them. Returns the deleted IDs, the
// count deleted, and any error.
func (st *cleanupStore) findAndDeleteLimited(ctx context.Context, col string, filter bson.D, limit int64) ([]interface{}, int64, error) {
	opts := options.Find().
		SetProjection(bson.D{{Key: "_id", Value: 1}}).
		SetLimit(limit)

	cursor, err := st.s.collection(col).Find(ctx, filter, opts)
	if err != nil {
		return nil, 0, mapErr(err)
	}
	defer func() { _ = cursor.Close(ctx) }()

	var ids []interface{}
	for cursor.Next(ctx) {
		var m bson.M
		if err := cursor.Decode(&m); err != nil {
			return nil, 0, mapErr(err)
		}
		ids = append(ids, m["_id"])
	}
	if err := cursor.Err(); err != nil {
		return nil, 0, mapErr(err)
	}

	if len(ids) == 0 {
		return nil, 0, nil
	}

	delFilter := bson.D{{Key: "_id", Value: bson.D{{Key: "$in", Value: ids}}}}
	res, err := st.s.collection(col).DeleteMany(ctx, delFilter)
	if err != nil {
		return nil, 0, mapErr(err)
	}
	return ids, res.DeletedCount, nil
}

// cascadeDelete removes documents from the given collection where field
// matches any of the values in inFilter. Errors are intentionally ignored
// because cascade deletes are best-effort; the periodic cleanup will
// catch any stragglers on the next run.
func (st *cleanupStore) cascadeDelete(ctx context.Context, col, field string, inFilter bson.D) error {
	filter := bson.D{{Key: field, Value: inFilter}}
	_, err := st.s.collection(col).DeleteMany(ctx, filter)
	return err
}

// cascadeDeleteByID removes documents from the given collection where
// _id matches any of the values in inFilter.
func (st *cleanupStore) cascadeDeleteByID(ctx context.Context, col string, inFilter bson.D) error {
	return st.cascadeDelete(ctx, col, "_id", inFilter)
}
