package dynamodb

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/ptrconv"
)

type orgStore struct{ s *dynamoStore }

var _ store.OrgStore = (*orgStore)(nil)

func (st *orgStore) table() string { return st.s.table(tableOrgs) }

func orgToItem(p store.CreateOrgParams, now time.Time) map[string]ddbtypes.AttributeValue {
	return map[string]ddbtypes.AttributeValue{
		"id":          attrS(p.ID),
		"name":        attrS(p.Name),
		"is_personal": attrBool(p.IsPersonal),
		"created_at":  attrS(timeToStr(now)),
		"deleted":     attrS(deletedFalse),
	}
}

func itemToOrg(item map[string]ddbtypes.AttributeValue) store.Org {
	return store.Org{
		ID:         getS(item, "id"),
		Name:       getS(item, "name"),
		IsPersonal: getBool(item, "is_personal"),
		CreatedAt:  getTime(item, "created_at"),
		DeletedAt:  getTimePtr(item, "deleted_at"),
	}
}

func (st *orgStore) Create(ctx context.Context, p store.CreateOrgParams) error {
	now := time.Now().UTC()
	item := orgToItem(p, now)
	constraintTable := st.s.table(tableUniqueConstraints)
	_, err := st.s.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		TransactItems: []ddbtypes.TransactWriteItem{
			{
				Put: &ddbtypes.Put{
					TableName:           aws.String(st.table()),
					Item:                item,
					ConditionExpression: aws.String("attribute_not_exists(id)"),
				},
			},
			putConstraint(constraintTable, "org", "name", p.Name),
		},
	})
	if err != nil {
		return mapErr(err)
	}
	if t := st.s.txTracker; t != nil {
		t.recordPut(st.table(), map[string]ddbtypes.AttributeValue{"id": item["id"]}, nil)
		t.recordPut(constraintTable, map[string]ddbtypes.AttributeValue{
			"constraint_value": attrS(constraintKey("org", "name", p.Name)),
		}, nil)
	}
	return nil
}

func (st *orgStore) GetByID(ctx context.Context, id string) (*store.Org, error) {
	o, err := st.GetByIDIncludeDeleted(ctx, id)
	if err != nil {
		return nil, err
	}
	if o.DeletedAt != nil {
		return nil, store.ErrNotFound
	}
	return o, nil
}

func (st *orgStore) GetByIDIncludeDeleted(ctx context.Context, id string) (*store.Org, error) {
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
	o := itemToOrg(out.Item)
	return &o, nil
}

func (st *orgStore) GetByName(ctx context.Context, name string) (*store.Org, error) {
	out, err := st.s.client.Query(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(st.table()),
		IndexName:              aws.String(gsiOrgName),
		KeyConditionExpression: aws.String("#n = :name"),
		FilterExpression:       aws.String("attribute_not_exists(deleted_at)"),
		ExpressionAttributeNames: map[string]string{
			"#n": "name",
		},
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":name": attrS(name),
		},
	})
	if err != nil {
		return nil, mapErr(err)
	}
	if len(out.Items) == 0 {
		return nil, store.ErrNotFound
	}
	o := itemToOrg(out.Items[0])
	return &o, nil
}

func (st *orgStore) HasAny(ctx context.Context) (bool, error) {
	out, err := st.s.client.Query(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(st.table()),
		IndexName:              aws.String(gsiDeletedCreatedAt),
		KeyConditionExpression: aws.String("deleted = :del"),
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":del": attrS(deletedFalse),
		},
		Limit:  aws.Int32(1),
		Select: ddbtypes.SelectCount,
	})
	if err != nil {
		return false, mapErr(err)
	}
	return out.Count > 0, nil
}

func (st *orgStore) ListAll(ctx context.Context, p store.ListAllOrgsParams) ([]store.Org, error) {
	keyExpr, exprValues, err := buildNotDeletedCursorExpr(p.Cursor)
	if err != nil {
		return nil, err
	}

	input := &dynamodb.QueryInput{
		TableName:                 aws.String(st.table()),
		IndexName:                 aws.String(gsiDeletedCreatedAt),
		KeyConditionExpression:    aws.String(keyExpr),
		ExpressionAttributeValues: exprValues,
		ScanIndexForward:          aws.Bool(false),
	}
	if p.Limit > 0 {
		input.Limit = aws.Int32(int32(p.Limit))
	}

	var all []store.Org
	err = st.s.queryPages(ctx, input, func(item map[string]ddbtypes.AttributeValue) bool {
		all = append(all, itemToOrg(item))
		return p.Limit <= 0 || int64(len(all)) < p.Limit
	})
	if err != nil {
		return nil, err
	}

	return ptrconv.NonNil(all), nil
}

func (st *orgStore) Search(ctx context.Context, p store.SearchOrgsParams) ([]store.Org, error) {
	keyExpr, exprValues, err := buildNotDeletedCursorExpr(p.Cursor)
	if err != nil {
		return nil, err
	}

	var q string
	if p.Query != nil && *p.Query != "" {
		q = strings.ToLower(*p.Query)
	}

	var all []store.Org
	var examined int
	err = st.s.queryPages(ctx, &dynamodb.QueryInput{
		TableName:                 aws.String(st.table()),
		IndexName:                 aws.String(gsiDeletedCreatedAt),
		KeyConditionExpression:    aws.String(keyExpr),
		ExpressionAttributeValues: exprValues,
		ScanIndexForward:          aws.Bool(false),
		Limit:                     aws.Int32(int32(store.SearchPageSize)),
	}, func(item map[string]ddbtypes.AttributeValue) bool {
		examined++
		o := itemToOrg(item)
		if q != "" && !store.PrefixMatchOrg(o, q) {
			return examined < store.SearchMaxExamine
		}
		all = append(all, o)
		return (p.Limit <= 0 || int64(len(all)) < p.Limit) && examined < store.SearchMaxExamine
	})
	if err != nil {
		return nil, err
	}

	return ptrconv.NonNil(all), nil
}

func (st *orgStore) UpdateName(ctx context.Context, p store.UpdateOrgNameParams) error {
	// Read current org to get the old name for constraint cleanup.
	org, err := st.GetByID(ctx, p.ID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil // Non-existent — treat as no-op.
		}
		return err
	}

	constraintTable := st.s.table(tableUniqueConstraints)
	_, err = st.s.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		TransactItems: []ddbtypes.TransactWriteItem{
			{
				Update: &ddbtypes.Update{
					TableName:           aws.String(st.table()),
					Key:                 map[string]ddbtypes.AttributeValue{"id": attrS(p.ID)},
					UpdateExpression:    aws.String("SET #n = :name"),
					ConditionExpression: aws.String("attribute_exists(id) AND attribute_not_exists(deleted_at)"),
					ExpressionAttributeNames: map[string]string{
						"#n": "name",
					},
					ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
						":name": attrS(p.Name),
					},
				},
			},
			deleteConstraint(constraintTable, "org", "name", org.Name),
			putConstraint(constraintTable, "org", "name", p.Name),
		},
	})
	if isConditionFailed(err) {
		return nil
	}
	return mapErr(err)
}

func (st *orgStore) SoftDelete(ctx context.Context, id string) error {
	// Get the org to find its name for constraint cleanup.
	org, err := st.GetByIDIncludeDeleted(ctx, id)
	if err != nil {
		return err
	}

	now := timeToStr(time.Now().UTC())
	constraintTable := st.s.table(tableUniqueConstraints)
	_, err = st.s.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		TransactItems: []ddbtypes.TransactWriteItem{
			{
				Update: &ddbtypes.Update{
					TableName:           aws.String(st.table()),
					Key:                 map[string]ddbtypes.AttributeValue{"id": attrS(id)},
					UpdateExpression:    aws.String("SET deleted_at = :now, deleted = :del"),
					ConditionExpression: aws.String("attribute_exists(id)"),
					ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
						":now": attrS(now),
						":del": attrS(deletedTrue),
					},
				},
			},
			deleteConstraint(constraintTable, "org", "name", org.Name),
		},
	})
	if isConditionFailed(err) {
		return store.ErrNotFound
	}
	return mapErr(err)
}

func (st *orgStore) SoftDeleteNonPersonal(ctx context.Context, id string) error {
	// Get the org to check if it qualifies and to find its name for constraint cleanup.
	org, err := st.GetByIDIncludeDeleted(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return err
	}
	if org.IsPersonal || org.DeletedAt != nil {
		return nil
	}

	now := timeToStr(time.Now().UTC())
	constraintTable := st.s.table(tableUniqueConstraints)
	_, err = st.s.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		TransactItems: []ddbtypes.TransactWriteItem{
			{
				Update: &ddbtypes.Update{
					TableName:           aws.String(st.table()),
					Key:                 map[string]ddbtypes.AttributeValue{"id": attrS(id)},
					UpdateExpression:    aws.String("SET deleted_at = :now, deleted = :del"),
					ConditionExpression: aws.String("attribute_exists(id) AND attribute_not_exists(deleted_at) AND is_personal = :false"),
					ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
						":now":   attrS(now),
						":del":   attrS(deletedTrue),
						":false": attrBool(false),
					},
				},
			},
			deleteConstraint(constraintTable, "org", "name", org.Name),
		},
	})
	if isConditionFailed(err) {
		// Condition failed because org is personal or already deleted — not an error.
		return nil
	}
	return mapErr(err)
}
