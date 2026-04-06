package dynamodb

import (
	"context"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/ptrconv"
)

type oauthTokenStore struct{ s *dynamoStore }

var _ store.OAuthTokenStore = (*oauthTokenStore)(nil)

func (st *oauthTokenStore) table() string { return st.s.table(tableOAuthTokens) }

func itemToOAuthToken(item map[string]ddbtypes.AttributeValue) store.OAuthToken {
	return store.OAuthToken{
		UserID:       getS(item, "user_id"),
		ProviderID:   getS(item, "provider_id"),
		AccessToken:  getBytes(item, "access_token"),
		RefreshToken: getBytes(item, "refresh_token"),
		TokenType:    getS(item, "token_type"),
		ExpiresAt:    getTime(item, "expires_at"),
		KeyVersion:   getSAsInt64(item, "key_version"),
		UpdatedAt:    getTime(item, "updated_at"),
	}
}

func (st *oauthTokenStore) Upsert(ctx context.Context, p store.UpsertOAuthTokensParams) error {
	now := time.Now().UTC()
	_, err := st.s.putItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(st.table()),
		Item: map[string]ddbtypes.AttributeValue{
			"user_id":          attrS(p.UserID),
			"provider_id":      attrS(p.ProviderID),
			"access_token":     attrB(p.AccessToken),
			"refresh_token":    attrB(p.RefreshToken),
			"token_type":       attrS(p.TokenType),
			"expires_at":       attrS(timeToStr(p.ExpiresAt.UTC())),
			"key_version":      attrS(strconv.FormatInt(p.KeyVersion, 10)),
			"expiry_partition": attrS(sentinelExpiryGroup),
			"updated_at":       attrS(timeToStr(now)),
		},
	}, "user_id", "provider_id")
	return mapErr(err)
}

func (st *oauthTokenStore) Get(ctx context.Context, p store.GetOAuthTokensParams) (*store.OAuthToken, error) {
	out, err := st.s.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(st.table()),
		Key: map[string]ddbtypes.AttributeValue{
			"user_id":     attrS(p.UserID),
			"provider_id": attrS(p.ProviderID),
		},
	})
	if err != nil {
		return nil, mapErr(err)
	}
	if out.Item == nil {
		return nil, store.ErrNotFound
	}
	t := itemToOAuthToken(out.Item)
	return &t, nil
}

func (st *oauthTokenStore) ListExpiring(ctx context.Context) ([]store.OAuthToken, error) {
	// Tokens expiring within 5 minutes.
	threshold := timeToStr(time.Now().UTC().Add(5 * time.Minute))
	var tokens []store.OAuthToken
	err := st.s.queryPages(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(st.table()),
		IndexName:              aws.String(gsiExpiry),
		KeyConditionExpression: aws.String("expiry_partition = :p AND expires_at < :threshold"),
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":p":         attrS(sentinelExpiryGroup),
			":threshold": attrS(threshold),
		},
	}, func(item map[string]ddbtypes.AttributeValue) bool {
		tokens = append(tokens, itemToOAuthToken(item))
		return true
	})
	if err != nil {
		return nil, err
	}
	return ptrconv.NonNil(tokens), nil
}

func (st *oauthTokenStore) ListByKeyVersion(ctx context.Context, keyVersion int64) ([]store.OAuthToken, error) {
	kv := strconv.FormatInt(keyVersion, 10)
	var tokens []store.OAuthToken
	err := st.s.queryPages(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(st.table()),
		IndexName:              aws.String(gsiKeyVersion),
		KeyConditionExpression: aws.String("key_version = :kv"),
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":kv": attrS(kv),
		},
	}, func(item map[string]ddbtypes.AttributeValue) bool {
		tokens = append(tokens, itemToOAuthToken(item))
		return true
	})
	if err != nil {
		return nil, err
	}
	return ptrconv.NonNil(tokens), nil
}

func (st *oauthTokenStore) CountByKeyVersion(ctx context.Context, keyVersion int64) (int64, error) {
	kv := strconv.FormatInt(keyVersion, 10)
	var count int64
	var lastKey map[string]ddbtypes.AttributeValue
	for {
		out, err := st.s.client.Query(ctx, &dynamodb.QueryInput{
			TableName:              aws.String(st.table()),
			IndexName:              aws.String(gsiKeyVersion),
			KeyConditionExpression: aws.String("key_version = :kv"),
			ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
				":kv": attrS(kv),
			},
			Select:            ddbtypes.SelectCount,
			ExclusiveStartKey: lastKey,
		})
		if err != nil {
			return 0, mapErr(err)
		}
		count += int64(out.Count)
		if out.LastEvaluatedKey == nil {
			break
		}
		lastKey = out.LastEvaluatedKey
	}
	return count, nil
}

func (st *oauthTokenStore) DeleteByProvider(ctx context.Context, providerID string) error {
	return deleteAllByGSI(ctx, st.s, st.table(), gsiProviderID, "provider_id", providerID, "user_id", "provider_id")
}

func (st *oauthTokenStore) DeleteByUser(ctx context.Context, userID string) error {
	return deleteAllByPK(ctx, st.s, st.table(), "user_id", userID, "provider_id")
}

func (st *oauthTokenStore) DeleteByUserAndProvider(ctx context.Context, p store.DeleteOAuthTokensByUserAndProviderParams) error {
	_, err := st.s.deleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(st.table()),
		Key: map[string]ddbtypes.AttributeValue{
			"user_id":     attrS(p.UserID),
			"provider_id": attrS(p.ProviderID),
		},
	})
	return mapErr(err)
}
