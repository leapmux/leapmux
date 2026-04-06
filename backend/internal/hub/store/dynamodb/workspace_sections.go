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

type workspaceSectionStore struct{ s *dynamoStore }

var _ store.WorkspaceSectionStore = (*workspaceSectionStore)(nil)

func (st *workspaceSectionStore) table() string { return st.s.table(tableWorkspaceSections) }

func sectionToItem(p store.CreateWorkspaceSectionParams, now time.Time) map[string]ddbtypes.AttributeValue {
	return map[string]ddbtypes.AttributeValue{
		"id":           attrS(p.ID),
		"user_id":      attrS(p.UserID),
		"name":         attrS(p.Name),
		"position":     attrS(p.Position),
		"section_type": attrN(int64(p.SectionType)),
		"sidebar":      attrN(int64(p.Sidebar)),
		"created_at":   attrS(timeToStr(now)),
	}
}

func itemToWorkspaceSection(item map[string]ddbtypes.AttributeValue) *store.WorkspaceSection {
	return &store.WorkspaceSection{
		ID:          getS(item, "id"),
		UserID:      getS(item, "user_id"),
		Name:        getS(item, "name"),
		Position:    getS(item, "position"),
		SectionType: leapmuxv1.SectionType(getN(item, "section_type")),
		Sidebar:     leapmuxv1.Sidebar(getN(item, "sidebar")),
		CreatedAt:   getTime(item, "created_at"),
	}
}

func (st *workspaceSectionStore) Create(ctx context.Context, p store.CreateWorkspaceSectionParams) error {
	now := time.Now().UTC()
	_, err := st.s.putItem(ctx, &dynamodb.PutItemInput{
		TableName:           aws.String(st.table()),
		Item:                sectionToItem(p, now),
		ConditionExpression: aws.String("attribute_not_exists(id)"),
	}, "id")
	return mapErr(err)
}

func (st *workspaceSectionStore) GetByID(ctx context.Context, id string) (*store.WorkspaceSection, error) {
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
	return itemToWorkspaceSection(out.Item), nil
}

func (st *workspaceSectionStore) ListByUserID(ctx context.Context, userID string) ([]store.WorkspaceSection, error) {
	var sections []store.WorkspaceSection
	err := st.s.queryPages(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(st.table()),
		IndexName:              aws.String(gsiUserID),
		KeyConditionExpression: aws.String("user_id = :uid"),
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":uid": attrS(userID),
		},
	}, func(item map[string]ddbtypes.AttributeValue) bool {
		sections = append(sections, *itemToWorkspaceSection(item))
		return true
	})
	if err != nil {
		return nil, err
	}
	return ptrconv.NonNil(sections), nil
}

func (st *workspaceSectionStore) Rename(ctx context.Context, p store.RenameWorkspaceSectionParams) (int64, error) {
	out, err := st.s.updateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:           aws.String(st.table()),
		Key:                 map[string]ddbtypes.AttributeValue{"id": attrS(p.ID)},
		UpdateExpression:    aws.String("SET #n = :name"),
		ConditionExpression: aws.String("attribute_exists(id) AND user_id = :uid"),
		ExpressionAttributeNames: map[string]string{
			"#n": "name",
		},
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":name": attrS(p.Name),
			":uid":  attrS(p.UserID),
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

func (st *workspaceSectionStore) UpdatePosition(ctx context.Context, p store.UpdateWorkspaceSectionPositionParams) error {
	_, err := st.s.updateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:           aws.String(st.table()),
		Key:                 map[string]ddbtypes.AttributeValue{"id": attrS(p.ID)},
		UpdateExpression:    aws.String("SET #p = :pos"),
		ConditionExpression: aws.String("attribute_exists(id) AND user_id = :uid"),
		ExpressionAttributeNames: map[string]string{
			"#p": "position",
		},
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":pos": attrS(p.Position),
			":uid": attrS(p.UserID),
		},
	})
	return mapErr(err)
}

func (st *workspaceSectionStore) UpdateSidebarPosition(ctx context.Context, p store.UpdateWorkspaceSectionSidebarPositionParams) error {
	_, err := st.s.updateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:           aws.String(st.table()),
		Key:                 map[string]ddbtypes.AttributeValue{"id": attrS(p.ID)},
		UpdateExpression:    aws.String("SET sidebar = :sb, #p = :pos"),
		ConditionExpression: aws.String("attribute_exists(id) AND user_id = :uid"),
		ExpressionAttributeNames: map[string]string{
			"#p": "position",
		},
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":sb":  attrN(int64(p.Sidebar)),
			":pos": attrS(p.Position),
			":uid": attrS(p.UserID),
		},
	})
	if isConditionFailed(err) {
		// Wrong user or non-existent section — treat as no-op.
		return nil
	}
	return mapErr(err)
}

func (st *workspaceSectionStore) Delete(ctx context.Context, p store.DeleteWorkspaceSectionParams) (int64, error) {
	out, err := st.s.deleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName:           aws.String(st.table()),
		Key:                 map[string]ddbtypes.AttributeValue{"id": attrS(p.ID)},
		ConditionExpression: aws.String("attribute_exists(id) AND user_id = :uid AND section_type = :custom"),
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":uid":    attrS(p.UserID),
			":custom": attrN(int64(leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_CUSTOM)),
		},
		ReturnValues: ddbtypes.ReturnValueAllOld,
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

func (st *workspaceSectionStore) HasDefaultForUser(ctx context.Context, userID string) (bool, error) {
	customType := strconv.FormatInt(int64(leapmuxv1.SectionType_SECTION_TYPE_WORKSPACES_CUSTOM), 10)
	out, err := st.s.client.Query(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(st.table()),
		IndexName:              aws.String(gsiUserID),
		KeyConditionExpression: aws.String("user_id = :uid"),
		FilterExpression:       aws.String("section_type <> :st"),
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":uid": attrS(userID),
			":st":  &ddbtypes.AttributeValueMemberN{Value: customType},
		},
		Select: ddbtypes.SelectCount,
	})
	if err != nil {
		return false, mapErr(err)
	}
	return out.Count > 0, nil
}
