package dynamodb

import (
	"context"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/store"
)

type cleanupStore struct{ s *dynamoStore }

var _ store.CleanupStore = (*cleanupStore)(nil)

func (st *cleanupStore) HardDeleteExpiredSessions(ctx context.Context) (int64, error) {
	now := timeToStr(time.Now().UTC())

	// Query the not_expired-expires_at-index GSI to find expired sessions
	// without a full table scan.
	var keys []map[string]ddbtypes.AttributeValue
	err := st.s.queryPages(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(st.s.table(tableSessions)),
		IndexName:              aws.String(gsiNotExpiredExpiresAt),
		KeyConditionExpression: aws.String("not_expired = :ne AND expires_at < :now"),
		ProjectionExpression:   aws.String(attrID),
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":ne":  attrS(sentinelActive),
			":now": attrS(now),
		},
	}, func(item map[string]ddbtypes.AttributeValue) bool {
		keys = append(keys, map[string]ddbtypes.AttributeValue{attrID: item[attrID]})
		return len(keys) < store.CleanupBatchLimit
	})
	if err != nil {
		return 0, err
	}
	if len(keys) == 0 {
		return 0, nil
	}

	if err := st.s.batchDelete(ctx, st.s.table(tableSessions), keys); err != nil {
		return 0, mapErr(err)
	}
	return int64(len(keys)), nil
}

func (st *cleanupStore) HardDeleteWorkspacesBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	// Query the deleted-deleted_at-index GSI instead of scanning.
	ids, err := st.queryDeletedIDs(ctx, st.s.table(tableWorkspaces), cutoff)
	if err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, nil
	}

	// Cascade to child tables (best-effort).
	for _, wsID := range ids {
		// workspace_access (PK=workspace_id, SK=user_id)
		_ = deleteAllByPK(ctx, st.s, st.s.table(tableWorkspaceAccess), attrWorkspaceID, wsID, attrUserID)
		// workspace_tabs (PK=workspace_id, SK=tab_type#tab_id)
		_ = deleteAllByPK(ctx, st.s, st.s.table(tableWorkspaceTabs), attrWorkspaceID, wsID, attrTabTypeSK)
		// workspace_layouts (PK=workspace_id, no SK)
		_ = st.s.batchDelete(ctx, st.s.table(tableWorkspaceLayouts), []map[string]ddbtypes.AttributeValue{
			{attrWorkspaceID: attrS(wsID)},
		})
		// workspace_section_items (PK=user_id, SK=workspace_id, GSI=workspace_id-index)
		_ = deleteAllByGSI(ctx, st.s, st.s.table(tableWorkspaceSectionItems), gsiWorkspaceID, attrWorkspaceID, wsID, attrUserID, attrWorkspaceID)
	}

	return st.batchDeleteByIDs(ctx, st.s.table(tableWorkspaces), ids)
}

func (st *cleanupStore) HardDeleteWorkersBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	// Query the deleted-deleted_at-index GSI instead of scanning.
	ids, err := st.queryDeletedIDs(ctx, st.s.table(tableWorkers), cutoff)
	if err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, nil
	}

	// Cascade to child tables (best-effort).
	for _, wkID := range ids {
		// worker_access_grants (PK=worker_id, SK=user_id)
		_ = deleteAllByPK(ctx, st.s, st.s.table(tableWorkerGrants), attrWorkerID, wkID, attrUserID)
		// worker_notifications (PK=id, GSI worker_id-status-index on worker_id)
		_ = deleteAllByGSI(ctx, st.s, st.s.table(tableWorkerNotifications), gsiWorkerIDStatus, attrWorkerID, wkID, attrID, "")
		// workspace_tabs (GSI worker_id-index on worker_id)
		_ = deleteAllByGSI(ctx, st.s, st.s.table(tableWorkspaceTabs), gsiWorkerID, attrWorkerID, wkID, attrWorkspaceID, attrTabTypeSK)
	}

	return st.batchDeleteByIDs(ctx, st.s.table(tableWorkers), ids)
}

func (st *cleanupStore) HardDeleteExpiredRegistrationsBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	cutoffStr := timeToStr(cutoff)
	expiredStatus := strconv.FormatInt(int64(leapmuxv1.RegistrationStatus_REGISTRATION_STATUS_EXPIRED), 10)
	approvedStatus := strconv.FormatInt(int64(leapmuxv1.RegistrationStatus_REGISTRATION_STATUS_APPROVED), 10)

	// Query expired and approved registrations separately via the status GSI.
	var keys []map[string]ddbtypes.AttributeValue
	for _, status := range []string{expiredStatus, approvedStatus} {
		err := st.s.queryPages(ctx, &dynamodb.QueryInput{
			TableName:              aws.String(st.s.table(tableRegistrations)),
			IndexName:              aws.String(gsiStatus),
			KeyConditionExpression: aws.String("#st = :status"),
			FilterExpression:       aws.String("created_at < :cutoff"),
			ProjectionExpression:   aws.String(attrID),
			ExpressionAttributeNames: map[string]string{
				"#st": attrStatus,
			},
			ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
				":status": attrS(status),
				":cutoff": attrS(cutoffStr),
			},
		}, func(item map[string]ddbtypes.AttributeValue) bool {
			keys = append(keys, map[string]ddbtypes.AttributeValue{attrID: item[attrID]})
			return len(keys) < store.CleanupBatchLimit
		})
		if err != nil {
			return 0, err
		}
		if len(keys) >= store.CleanupBatchLimit {
			break
		}
	}

	if len(keys) == 0 {
		return 0, nil
	}
	if err := st.s.batchDelete(ctx, st.s.table(tableRegistrations), keys); err != nil {
		return 0, mapErr(err)
	}
	return int64(len(keys)), nil
}

func (st *cleanupStore) HardDeleteUsersBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	// Query the deleted-deleted_at-index GSI instead of scanning.
	ids, err := st.queryDeletedIDs(ctx, st.s.table(tableUsers), cutoff)
	if err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, nil
	}

	// Cascade to child tables (best-effort).
	// Sessions, workspaces, and workers are cleaned before users, so only
	// remaining children need to be handled here.
	for _, userID := range ids {
		// org_members (PK=org_id, SK=user_id; GSI user_id-index)
		_ = deleteAllByGSI(ctx, st.s, st.s.table(tableOrgMembers), gsiUserID, attrUserID, userID, attrOrgID, attrUserID)
		// workspace_sections (PK=id; GSI user_id-index)
		_ = deleteAllByGSI(ctx, st.s, st.s.table(tableWorkspaceSections), gsiUserID, attrUserID, userID, attrID, "")
		// workspace_section_items (PK=user_id, SK=workspace_id)
		_ = deleteAllByPK(ctx, st.s, st.s.table(tableWorkspaceSectionItems), attrUserID, userID, attrWorkspaceID)
		// worker_access_grants (PK=worker_id, SK=user_id; GSI user_id-index)
		_ = deleteAllByGSI(ctx, st.s, st.s.table(tableWorkerGrants), gsiUserID, attrUserID, userID, attrWorkerID, attrUserID)
		// workspace_access (PK=workspace_id, SK=user_id; GSI user_id-index)
		_ = deleteAllByGSI(ctx, st.s, st.s.table(tableWorkspaceAccess), gsiUserID, attrUserID, userID, attrWorkspaceID, attrUserID)
		// oauth_tokens (PK=user_id, SK=provider_id)
		_ = deleteAllByPK(ctx, st.s, st.s.table(tableOAuthTokens), attrUserID, userID, attrProviderID)
		// oauth_user_links (PK=user_id, SK=provider_id)
		_ = deleteAllByPK(ctx, st.s, st.s.table(tableOAuthUserLinks), attrUserID, userID, attrProviderID)
	}

	return st.batchDeleteByIDs(ctx, st.s.table(tableUsers), ids)
}

func (st *cleanupStore) HardDeleteOrgsBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	// Query the deleted-deleted_at-index GSI instead of scanning.
	ids, err := st.queryDeletedIDs(ctx, st.s.table(tableOrgs), cutoff)
	if err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, nil
	}

	// Cascade to child tables (best-effort).
	for _, orgID := range ids {
		// org_members (PK=org_id, SK=user_id)
		_ = deleteAllByPK(ctx, st.s, st.s.table(tableOrgMembers), attrOrgID, orgID, attrUserID)
	}

	return st.batchDeleteByIDs(ctx, st.s.table(tableOrgs), ids)
}

// batchDeleteByIDs deletes items with simple "id" primary keys.
func (st *cleanupStore) batchDeleteByIDs(ctx context.Context, tableName string, ids []string) (int64, error) {
	keys := make([]map[string]ddbtypes.AttributeValue, len(ids))
	for i, id := range ids {
		keys[i] = map[string]ddbtypes.AttributeValue{attrID: attrS(id)}
	}
	if err := st.s.batchDelete(ctx, tableName, keys); err != nil {
		return 0, mapErr(err)
	}
	return int64(len(ids)), nil
}

func (st *cleanupStore) DeleteExpiredOAuthStates(ctx context.Context) (int64, error) {
	return st.deleteExpiredByActiveGSI(ctx, tableOAuthStates, attrState)
}

func (st *cleanupStore) DeleteExpiredPendingOAuthSignups(ctx context.Context) (int64, error) {
	return st.deleteExpiredByActiveGSI(ctx, tablePendingOAuthSignups, attrToken)
}

// deleteExpiredByActiveGSI queries the active-expires_at-index GSI for
// expired items and batch-deletes them. The pkAttr is the primary key
// attribute name used for deletion (e.g. "state" or "token").
func (st *cleanupStore) deleteExpiredByActiveGSI(ctx context.Context, table, pkAttr string) (int64, error) {
	now := timeToStr(time.Now().UTC())
	tableName := st.s.table(table)

	// Always alias the key attribute to avoid DynamoDB reserved word conflicts
	// (both "state" and "token" are reserved).
	exprNames := map[string]string{"#k": pkAttr}
	proj := "#k"

	var keys []map[string]ddbtypes.AttributeValue
	err := st.s.queryPages(ctx, &dynamodb.QueryInput{
		TableName:                aws.String(tableName),
		IndexName:                aws.String(gsiActiveExpiresAt),
		KeyConditionExpression:   aws.String("active = :a AND expires_at < :now"),
		ProjectionExpression:     aws.String(proj),
		ExpressionAttributeNames: exprNames,
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":a":   attrS(sentinelActive),
			":now": attrS(now),
		},
	}, func(item map[string]ddbtypes.AttributeValue) bool {
		keys = append(keys, map[string]ddbtypes.AttributeValue{pkAttr: item[pkAttr]})
		return len(keys) < store.CleanupBatchLimit
	})
	if err != nil {
		return 0, err
	}
	if len(keys) == 0 {
		return 0, nil
	}
	if err := st.s.batchDelete(ctx, tableName, keys); err != nil {
		return 0, mapErr(err)
	}
	return int64(len(keys)), nil
}

// queryDeletedIDs queries the deleted-deleted_at-index GSI to find
// soft-deleted items (deleted="1") with deleted_at before the cutoff.
// This avoids full table scans by leveraging the GSI.
func (st *cleanupStore) queryDeletedIDs(ctx context.Context, tableName string, cutoff time.Time) ([]string, error) {
	cutoffStr := timeToStr(cutoff)
	var ids []string
	err := st.s.queryPages(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(tableName),
		IndexName:              aws.String(gsiDeletedDeletedAt),
		KeyConditionExpression: aws.String("deleted = :del AND deleted_at < :cutoff"),
		ProjectionExpression:   aws.String(attrID),
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":del":    attrS(deletedTrue),
			":cutoff": attrS(cutoffStr),
		},
	}, func(item map[string]ddbtypes.AttributeValue) bool {
		ids = append(ids, getS(item, attrID))
		return len(ids) < store.CleanupBatchLimit
	})
	if err != nil {
		return nil, err
	}
	return ids, nil
}
