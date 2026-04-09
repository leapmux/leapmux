package dynamodb

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/leapmux/leapmux/internal/hub/store"
)

// testableDynamoStore extends dynamoStore with test helper operations.
type testableDynamoStore struct {
	*dynamoStore
}

var _ store.TestableStore = (*testableDynamoStore)(nil)

// NewTestable creates a DynamoDB-backed store that also implements
// store.TestableStore. Intended for use in tests only.
func NewTestable(ctx context.Context, opts Options) (store.TestableStore, error) {
	st, err := New(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &testableDynamoStore{dynamoStore: st.(*dynamoStore)}, nil
}

func (s *testableDynamoStore) TestHelper() store.TestHelper {
	return &dynamoTestHelper{s: s.dynamoStore}
}

type dynamoTestHelper struct {
	s *dynamoStore
}

// entityTableMap maps TestEntity values to DynamoDB table suffixes.
// Only entities with a simple "id" partition key are supported, since
// SetDeletedAt/SetCreatedAt accept a single ID.
var entityTableMap = map[store.TestEntity]string{
	store.EntityUsers:               tableUsers,
	store.EntityOrgs:                tableOrgs,
	store.EntityWorkers:             tableWorkers,
	store.EntityWorkspaces:          tableWorkspaces,
	store.EntityWorkerRegistrations: tableRegistrations,
	store.EntitySessions:            tableSessions,
}

func (h *dynamoTestHelper) SetDeletedAt(ctx context.Context, entity store.TestEntity, id string, deletedAt time.Time) error {
	if err := store.ValidateEntity(entity); err != nil {
		return err
	}
	tableSuffix, ok := entityTableMap[entity]
	if !ok {
		return fmt.Errorf("unsupported entity %q for DynamoDB SetDeletedAt", entity)
	}
	tableName := h.s.table(tableSuffix)
	// Set deleted_at and deleted="1" for the deleted-* GSIs.
	_, err := h.s.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(tableName),
		Key: map[string]ddbtypes.AttributeValue{
			attrID: attrS(id),
		},
		UpdateExpression: aws.String("SET deleted_at = :v, deleted = :del"),
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":v":   attrS(timeToStr(deletedAt)),
			":del": attrS(deletedTrue),
		},
	})
	return mapErr(err)
}

func (h *dynamoTestHelper) SetCreatedAt(ctx context.Context, entity store.TestEntity, id string, createdAt time.Time) error {
	return h.setTimeAttr(ctx, entity, id, attrCreatedAt, createdAt)
}

func (h *dynamoTestHelper) setTimeAttr(ctx context.Context, entity store.TestEntity, id string, attr string, t time.Time) error {
	if err := store.ValidateEntity(entity); err != nil {
		return err
	}
	tableSuffix, ok := entityTableMap[entity]
	if !ok {
		return fmt.Errorf("unsupported entity %q for DynamoDB setTimeAttr", entity)
	}
	tableName := h.s.table(tableSuffix)
	_, err := h.s.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(tableName),
		Key: map[string]ddbtypes.AttributeValue{
			attrID: attrS(id),
		},
		UpdateExpression: aws.String("SET #a = :v"),
		ExpressionAttributeNames: map[string]string{
			"#a": attr,
		},
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":v": attrS(timeToStr(t)),
		},
	})
	return mapErr(err)
}

func (h *dynamoTestHelper) TruncateAll(ctx context.Context) error {
	for _, td := range allTables() {
		if td.name == tableMeta {
			continue // preserve migration metadata
		}
		if err := h.truncateTable(ctx, td); err != nil {
			return err
		}
	}
	return nil
}

// truncateTable scans all items in a table and deletes them using BatchWriteItem.
func (h *dynamoTestHelper) truncateTable(ctx context.Context, td tableDef) error {
	tableName := h.s.table(td.name)

	// Determine key attribute names for this table.
	keyAttrs := []string{td.pk}
	if td.sk != "" {
		keyAttrs = append(keyAttrs, td.sk)
	}

	// Build projection expression with ExpressionAttributeNames for attribute
	// names that contain special characters (e.g. "tab_type#tab_id").
	proj, exprNames := buildProjection(keyAttrs)

	// Scan all items (only project key attributes).
	var lastKey map[string]ddbtypes.AttributeValue
	for {
		input := &dynamodb.ScanInput{
			TableName:            aws.String(tableName),
			ProjectionExpression: aws.String(proj),
		}
		if len(exprNames) > 0 {
			input.ExpressionAttributeNames = exprNames
		}
		if lastKey != nil {
			input.ExclusiveStartKey = lastKey
		}

		out, err := h.s.client.Scan(ctx, input)
		if err != nil {
			return err
		}

		reqs := make([]ddbtypes.WriteRequest, 0, len(out.Items))
		for _, item := range out.Items {
			key := make(map[string]ddbtypes.AttributeValue, len(keyAttrs))
			for _, attr := range keyAttrs {
				key[attr] = item[attr]
			}
			reqs = append(reqs, ddbtypes.WriteRequest{
				DeleteRequest: &ddbtypes.DeleteRequest{Key: key},
			})
		}
		if err := h.s.batchWrite(ctx, tableName, reqs); err != nil {
			return err
		}

		if out.LastEvaluatedKey == nil {
			break
		}
		lastKey = out.LastEvaluatedKey
	}
	return nil
}

// buildProjection returns a projection expression and ExpressionAttributeNames
// for the given attributes. All attributes are aliased to avoid conflicts with
// DynamoDB reserved keywords (e.g. "state", "status", "name", "token", "type").
func buildProjection(attrs []string) (string, map[string]string) {
	parts := make([]string, len(attrs))
	exprNames := make(map[string]string, len(attrs))
	for i, a := range attrs {
		alias := "#k_" + strings.ReplaceAll(a, "#", "_")
		exprNames[alias] = a
		parts[i] = alias
	}
	return strings.Join(parts, ", "), exprNames
}
