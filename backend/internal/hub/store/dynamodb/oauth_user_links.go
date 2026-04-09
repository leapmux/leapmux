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

func itemToOAuthUserLink(item map[string]ddbtypes.AttributeValue) (store.OAuthUserLink, error) {
	userID, err := mustGetS(item, attrUserID)
	if err != nil {
		return store.OAuthUserLink{}, err
	}
	providerID, err := mustGetS(item, attrProviderID)
	if err != nil {
		return store.OAuthUserLink{}, err
	}
	providerSubject, err := mustGetS(item, attrProviderSubject)
	if err != nil {
		return store.OAuthUserLink{}, err
	}
	createdAt, err := mustGetTime(item, attrCreatedAt)
	if err != nil {
		return store.OAuthUserLink{}, err
	}
	return store.OAuthUserLink{
		UserID:          userID,
		ProviderID:      providerID,
		ProviderSubject: providerSubject,
		CreatedAt:       createdAt,
	}, nil
}

func (st *oauthUserLinkStore) Create(ctx context.Context, p store.CreateOAuthUserLinkParams) error {
	now := time.Now().UTC()
	_, err := st.s.putItem(ctx, &dynamodb.PutItemInput{
		TableName:           aws.String(st.table()),
		ConditionExpression: aws.String("attribute_not_exists(user_id) AND attribute_not_exists(provider_id)"),
		Item: map[string]ddbtypes.AttributeValue{
			attrUserID:          attrS(p.UserID),
			attrProviderID:      attrS(p.ProviderID),
			attrProviderSubject: attrS(p.ProviderSubject),
			attrCreatedAt:       attrS(timeToStr(now)),
		},
	}, attrUserID, attrProviderID)
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
	link, err := itemToOAuthUserLink(out.Items[0])
	if err != nil {
		return nil, err
	}
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
		link, err := itemToOAuthUserLink(item)
		if err != nil {
			return false
		}
		links = append(links, link)
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
			attrUserID:     attrS(p.UserID),
			attrProviderID: attrS(p.ProviderID),
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
			attrUserID:     attrS(getS(item, attrUserID)),
			attrProviderID: attrS(getS(item, attrProviderID)),
		})
		return true
	})
	if err != nil {
		return err
	}
	return st.s.batchDelete(ctx, st.table(), keys)
}
