package dynamodb

import (
	"context"
	"errors"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/ptrconv"
)

type workspaceSectionItemStore struct{ s *dynamoStore }

var _ store.WorkspaceSectionItemStore = (*workspaceSectionItemStore)(nil)

func (st *workspaceSectionItemStore) table() string { return st.s.table(tableWorkspaceSectionItems) }

func (st *workspaceSectionItemStore) Set(ctx context.Context, p store.SetWorkspaceSectionItemParams) error {
	_, err := st.s.putItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(st.table()),
		Item: map[string]ddbtypes.AttributeValue{
			"user_id":      attrS(p.UserID),
			"workspace_id": attrS(p.WorkspaceID),
			"section_id":   attrS(p.SectionID),
			"position":     attrS(p.Position),
		},
	}, "user_id", "workspace_id")
	return mapErr(err)
}

func (st *workspaceSectionItemStore) Get(ctx context.Context, p store.GetWorkspaceSectionItemParams) (*store.WorkspaceSectionItem, error) {
	out, err := st.s.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(st.table()),
		Key: map[string]ddbtypes.AttributeValue{
			"user_id":      attrS(p.UserID),
			"workspace_id": attrS(p.WorkspaceID),
		},
	})
	if err != nil {
		return nil, mapErr(err)
	}
	if out.Item == nil {
		return nil, store.ErrNotFound
	}
	return &store.WorkspaceSectionItem{
		UserID:      getS(out.Item, "user_id"),
		WorkspaceID: getS(out.Item, "workspace_id"),
		SectionID:   getS(out.Item, "section_id"),
		Position:    getS(out.Item, "position"),
	}, nil
}

func (st *workspaceSectionItemStore) ListByUser(ctx context.Context, userID string) ([]store.WorkspaceSectionItem, error) {
	var items []store.WorkspaceSectionItem
	err := st.s.queryPages(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(st.table()),
		KeyConditionExpression: aws.String("user_id = :uid"),
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":uid": attrS(userID),
		},
	}, func(item map[string]ddbtypes.AttributeValue) bool {
		items = append(items, store.WorkspaceSectionItem{
			UserID:      getS(item, "user_id"),
			WorkspaceID: getS(item, "workspace_id"),
			SectionID:   getS(item, "section_id"),
			Position:    getS(item, "position"),
		})
		return true
	})
	if err != nil {
		return nil, err
	}
	return ptrconv.NonNil(items), nil
}

func (st *workspaceSectionItemStore) Delete(ctx context.Context, p store.DeleteWorkspaceSectionItemParams) error {
	_, err := st.s.deleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(st.table()),
		Key: map[string]ddbtypes.AttributeValue{
			"user_id":      attrS(p.UserID),
			"workspace_id": attrS(p.WorkspaceID),
		},
	})
	return mapErr(err)
}

func (st *workspaceSectionItemStore) DeleteBySection(ctx context.Context, sectionID string) error {
	return deleteAllByGSI(ctx, st.s, st.table(), gsiSectionID, "section_id", sectionID, "user_id", "workspace_id")
}

func (st *workspaceSectionItemStore) MoveToSection(ctx context.Context, p store.MoveWorkspaceSectionItemsToSectionParams) error {
	// Collect all item keys from paginated GSI query.
	type itemKey struct{ userID, wsID string }
	var keys []itemKey
	err := st.s.queryPages(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(st.table()),
		IndexName:              aws.String(gsiSectionID),
		KeyConditionExpression: aws.String("section_id = :sid"),
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":sid": attrS(p.FromSectionID),
		},
		ProjectionExpression: aws.String("user_id, workspace_id"),
	}, func(item map[string]ddbtypes.AttributeValue) bool {
		keys = append(keys, itemKey{getS(item, "user_id"), getS(item, "workspace_id")})
		return true
	})
	if err != nil {
		return err
	}

	for _, k := range keys {
		if _, err := st.s.updateItem(ctx, &dynamodb.UpdateItemInput{
			TableName: aws.String(st.table()),
			Key: map[string]ddbtypes.AttributeValue{
				"user_id":      attrS(k.userID),
				"workspace_id": attrS(k.wsID),
			},
			UpdateExpression: aws.String("SET section_id = :toSid"),
			ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
				":toSid": attrS(p.ToSectionID),
			},
		}); err != nil {
			return mapErr(err)
		}
	}
	return nil
}

func (st *workspaceSectionItemStore) HasItemsBySection(ctx context.Context, sectionID string) (bool, error) {
	out, err := st.s.client.Query(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(st.table()),
		IndexName:              aws.String(gsiSectionID),
		KeyConditionExpression: aws.String("section_id = :sid"),
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":sid": attrS(sectionID),
		},
		Limit:  aws.Int32(1),
		Select: ddbtypes.SelectCount,
	})
	if err != nil {
		return false, mapErr(err)
	}
	return out.Count > 0, nil
}

func (st *workspaceSectionItemStore) IsInArchivedSection(ctx context.Context, p store.IsWorkspaceInArchivedSectionParams) (bool, error) {
	// Get the section item.
	item, err := st.Get(ctx, store.GetWorkspaceSectionItemParams(p))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return false, nil
		}
		return false, err
	}

	// Look up the section to check its type.
	secOut, err := st.s.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(st.s.table(tableWorkspaceSections)),
		Key:       map[string]ddbtypes.AttributeValue{"id": attrS(item.SectionID)},
	})
	if err != nil || secOut.Item == nil {
		return false, mapErr(err)
	}

	sectionType := getN(secOut.Item, "section_type")
	return leapmuxv1.SectionType(sectionType) == leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_ARCHIVED, nil
}
