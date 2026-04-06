package dynamodb

import (
	"context"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/ptrconv"
)

type oauthUserLinkStore struct{ s *dynamoStore }

var _ store.OAuthUserLinkStore = (*oauthUserLinkStore)(nil)

func (st *oauthUserLinkStore) table() string { return st.s.table(tableOAuthUserLinks) }

func itemToOAuthUserLink(item map[string]ddbtypes.AttributeValue) store.OAuthUserLink {
	return store.OAuthUserLink{
		UserID:          getS(item, "user_id"),
		ProviderID:      getS(item, "provider_id"),
		ProviderSubject: getS(item, "provider_subject"),
		CreatedAt:       getTime(item, "created_at"),
	}
}

func (st *oauthUserLinkStore) Create(ctx context.Context, p store.CreateOAuthUserLinkParams) error {
	now := time.Now().UTC()
	_, err := st.s.putItem(ctx, &dynamodb.PutItemInput{
		TableName:           aws.String(st.table()),
		ConditionExpression: aws.String("attribute_not_exists(user_id) AND attribute_not_exists(provider_id)"),
		Item: map[string]ddbtypes.AttributeValue{
			"user_id":          attrS(p.UserID),
			"provider_id":      attrS(p.ProviderID),
			"provider_subject": attrS(p.ProviderSubject),
			"created_at":       attrS(timeToStr(now)),
		},
	}, "user_id", "provider_id")
	return mapErr(err)
}

func (st *oauthUserLinkStore) Get(ctx context.Context, p store.GetOAuthUserLinkParams) (*store.OAuthUserLink, error) {
	// Look up by provider_id + provider_subject via GSI.
	out, err := st.s.client.Query(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(st.table()),
		IndexName:              aws.String(gsiProviderSubject),
		KeyConditionExpression: aws.String("provider_id = :pid AND provider_subject = :ps"),
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":pid": attrS(p.ProviderID),
			":ps":  attrS(p.ProviderSubject),
		},
		Limit: aws.Int32(1),
	})
	if err != nil {
		return nil, mapErr(err)
	}
	if len(out.Items) == 0 {
		return nil, store.ErrNotFound
	}
	link := itemToOAuthUserLink(out.Items[0])
	return &link, nil
}

func (st *oauthUserLinkStore) ListByUser(ctx context.Context, userID string) ([]store.OAuthUserLink, error) {
	var links []store.OAuthUserLink
	err := st.s.queryPages(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(st.table()),
		KeyConditionExpression: aws.String("user_id = :uid"),
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":uid": attrS(userID),
		},
	}, func(item map[string]ddbtypes.AttributeValue) bool {
		links = append(links, itemToOAuthUserLink(item))
		return true
	})
	if err != nil {
		return nil, err
	}
	return ptrconv.NonNil(links), nil
}

func (st *oauthUserLinkStore) Delete(ctx context.Context, p store.DeleteOAuthUserLinkParams) error {
	_, err := st.s.deleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(st.table()),
		Key: map[string]ddbtypes.AttributeValue{
			"user_id":     attrS(p.UserID),
			"provider_id": attrS(p.ProviderID),
		},
	})
	return mapErr(err)
}

func (st *oauthUserLinkStore) DeleteByProvider(ctx context.Context, providerID string) error {
	// Query the provider_subject-index for all items with this provider_id,
	// then batch-delete them.
	var keys []map[string]ddbtypes.AttributeValue
	err := st.s.queryPages(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(st.table()),
		IndexName:              aws.String(gsiProviderSubject),
		KeyConditionExpression: aws.String("provider_id = :pid"),
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":pid": attrS(providerID),
		},
	}, func(item map[string]ddbtypes.AttributeValue) bool {
		keys = append(keys, map[string]ddbtypes.AttributeValue{
			"user_id":     attrS(getS(item, "user_id")),
			"provider_id": attrS(getS(item, "provider_id")),
		})
		return true
	})
	if err != nil {
		return err
	}
	return st.s.batchDelete(ctx, st.table(), keys)
}
