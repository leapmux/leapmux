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
	"github.com/leapmux/leapmux/internal/util/ptrconv"
)

type orgMemberStore struct{ s *dynamoStore }

var _ store.OrgMemberStore = (*orgMemberStore)(nil)

func (st *orgMemberStore) table() string { return st.s.table(tableOrgMembers) }

func orgMemberToItem(p store.CreateOrgMemberParams, now time.Time) map[string]ddbtypes.AttributeValue {
	return map[string]ddbtypes.AttributeValue{
		attrOrgID:    attrS(p.OrgID),
		attrUserID:   attrS(p.UserID),
		attrRole:     attrN(int64(p.Role)),
		attrJoinedAt: attrS(timeToStr(now)),
	}
}

func itemToOrgMember(item map[string]ddbtypes.AttributeValue) (store.OrgMember, error) {
	orgID, err := mustGetS(item, attrOrgID)
	if err != nil {
		return store.OrgMember{}, err
	}
	userID, err := mustGetS(item, attrUserID)
	if err != nil {
		return store.OrgMember{}, err
	}
	role, err := mustGetN(item, attrRole)
	if err != nil {
		return store.OrgMember{}, err
	}
	joinedAt, err := mustGetTime(item, attrJoinedAt)
	if err != nil {
		return store.OrgMember{}, err
	}
	return store.OrgMember{
		OrgID:    orgID,
		UserID:   userID,
		Role:     leapmuxv1.OrgMemberRole(role),
		JoinedAt: joinedAt,
	}, nil
}

func (st *orgMemberStore) Create(ctx context.Context, p store.CreateOrgMemberParams) error {
	now := time.Now().UTC()
	_, err := st.s.putItem(ctx, &dynamodb.PutItemInput{
		TableName:           aws.String(st.table()),
		Item:                orgMemberToItem(p, now),
		ConditionExpression: aws.String("attribute_not_exists(org_id) AND attribute_not_exists(user_id)"),
	}, attrOrgID, attrUserID)
	return mapErr(err)
}

func (st *orgMemberStore) GetByOrgAndUser(ctx context.Context, orgID, userID string) (*store.OrgMember, error) {
	out, err := st.s.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(st.table()),
		Key: map[string]ddbtypes.AttributeValue{
			attrOrgID:  attrS(orgID),
			attrUserID: attrS(userID),
		},
	})
	if err != nil {
		return nil, mapErr(err)
	}
	if out.Item == nil {
		return nil, store.ErrNotFound
	}
	m, err := itemToOrgMember(out.Item)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

func (st *orgMemberStore) ListByOrgID(ctx context.Context, orgID string) ([]store.OrgMemberWithUser, error) {
	// Collect all org members first.
	var members []store.OrgMember
	err := st.s.queryPages(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(st.table()),
		KeyConditionExpression: aws.String("org_id = :orgID"),
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":orgID": attrS(orgID),
		},
	}, func(item map[string]ddbtypes.AttributeValue) bool {
		m, err := itemToOrgMember(item)
		if err != nil {
			return false
		}
		members = append(members, m)
		return true
	})
	if err != nil {
		return nil, err
	}

	if len(members) == 0 {
		return []store.OrgMemberWithUser{}, nil
	}

	// Batch-fetch all referenced users.
	userKeys := make([]map[string]ddbtypes.AttributeValue, len(members))
	for i, m := range members {
		userKeys[i] = map[string]ddbtypes.AttributeValue{attrID: attrS(m.UserID)}
	}
	userItems, err := st.s.batchGetItems(ctx, st.s.table(tableUsers), userKeys)
	if err != nil {
		return nil, err
	}
	userMap := make(map[string]store.User, len(userItems))
	for _, item := range userItems {
		if getTimePtr(item, attrDeletedAt) != nil {
			continue // Skip soft-deleted users.
		}
		u, err := itemToUser(item)
		if err != nil {
			return nil, err
		}
		userMap[u.ID] = u
	}

	// Join members with their user data.
	var result []store.OrgMemberWithUser
	for _, m := range members {
		u, ok := userMap[m.UserID]
		if !ok {
			continue // Skip members whose user was deleted.
		}
		result = append(result, store.OrgMemberWithUser{
			OrgMember:   m,
			Username:    u.Username,
			DisplayName: u.DisplayName,
			Email:       u.Email,
		})
	}
	return ptrconv.NonNil(result), nil
}

func (st *orgMemberStore) ListOrgsByUserID(ctx context.Context, userID string) ([]store.Org, error) {
	// Collect all org IDs from the membership index.
	var orgIDs []string
	err := st.s.queryPages(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(st.table()),
		IndexName:              aws.String(gsiUserID),
		KeyConditionExpression: aws.String("user_id = :uid"),
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":uid": attrS(userID),
		},
	}, func(item map[string]ddbtypes.AttributeValue) bool {
		orgIDs = append(orgIDs, getS(item, attrOrgID))
		return true
	})
	if err != nil {
		return nil, err
	}

	if len(orgIDs) == 0 {
		return []store.Org{}, nil
	}

	// Batch-fetch all referenced orgs.
	orgKeys := make([]map[string]ddbtypes.AttributeValue, len(orgIDs))
	for i, id := range orgIDs {
		orgKeys[i] = map[string]ddbtypes.AttributeValue{attrID: attrS(id)}
	}
	orgItems, err := st.s.batchGetItems(ctx, st.s.table(tableOrgs), orgKeys)
	if err != nil {
		return nil, err
	}

	var orgs []store.Org
	for _, item := range orgItems {
		o, err := itemToOrg(item)
		if err != nil {
			return nil, err
		}
		if o.DeletedAt != nil {
			continue
		}
		orgs = append(orgs, o)
	}
	return ptrconv.NonNil(orgs), nil
}

func (st *orgMemberStore) UpdateRole(ctx context.Context, p store.UpdateOrgMemberRoleParams) error {
	_, err := st.s.updateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(st.table()),
		Key: map[string]ddbtypes.AttributeValue{
			attrOrgID:  attrS(p.OrgID),
			attrUserID: attrS(p.UserID),
		},
		UpdateExpression:    aws.String("SET #r = :role"),
		ConditionExpression: aws.String("attribute_exists(org_id)"),
		ExpressionAttributeNames: map[string]string{
			"#r": attrRole,
		},
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":role": attrN(int64(p.Role)),
		},
	})
	return mapErr(err)
}

func (st *orgMemberStore) Delete(ctx context.Context, p store.DeleteOrgMemberParams) error {
	_, err := st.s.deleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(st.table()),
		Key: map[string]ddbtypes.AttributeValue{
			attrOrgID:  attrS(p.OrgID),
			attrUserID: attrS(p.UserID),
		},
	})
	return mapErr(err)
}

func (st *orgMemberStore) CountByRole(ctx context.Context, p store.CountOrgMembersByRoleParams) (int64, error) {
	var count int64
	var lastKey map[string]ddbtypes.AttributeValue
	roleStr := strconv.FormatInt(int64(p.Role), 10)
	for {
		out, err := st.s.client.Query(ctx, &dynamodb.QueryInput{
			TableName:              aws.String(st.table()),
			KeyConditionExpression: aws.String("org_id = :orgID"),
			FilterExpression:       aws.String("#r = :role"),
			ExpressionAttributeNames: map[string]string{
				"#r": attrRole,
			},
			ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
				":orgID": attrS(p.OrgID),
				":role":  &ddbtypes.AttributeValueMemberN{Value: roleStr},
			},
			Select:            ddbtypes.SelectCount,
			ExclusiveStartKey: lastKey,
		})
		if err != nil {
			return 0, mapErr(err)
		}
		count += int64(out.Count)
		// Callers only need to distinguish 0, 1, or >1, so stop early.
		if count >= 2 || out.LastEvaluatedKey == nil {
			break
		}
		lastKey = out.LastEvaluatedKey
	}
	return count, nil
}

func (st *orgMemberStore) IsMember(ctx context.Context, p store.IsOrgMemberParams) (bool, error) {
	out, err := st.s.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(st.table()),
		Key: map[string]ddbtypes.AttributeValue{
			attrOrgID:  attrS(p.OrgID),
			attrUserID: attrS(p.UserID),
		},
		ProjectionExpression: aws.String(attrOrgID),
	})
	if err != nil {
		return false, mapErr(err)
	}
	return out.Item != nil, nil
}
