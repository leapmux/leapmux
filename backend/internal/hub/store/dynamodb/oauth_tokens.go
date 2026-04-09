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

func itemToOAuthToken(item map[string]ddbtypes.AttributeValue) (store.OAuthToken, error) {
	userID, err := mustGetS(item, attrUserID)
	if err != nil {
		return store.OAuthToken{}, err
	}
	providerID, err := mustGetS(item, attrProviderID)
	if err != nil {
		return store.OAuthToken{}, err
	}
	tokenType, err := mustGetS(item, attrTokenType)
	if err != nil {
		return store.OAuthToken{}, err
	}
	expiresAt, err := mustGetTime(item, attrExpiresAt)
	if err != nil {
		return store.OAuthToken{}, err
	}
	keyVersion, err := mustGetSAsInt64(item, attrKeyVersion)
	if err != nil {
		return store.OAuthToken{}, err
	}
	updatedAt, err := mustGetTime(item, attrUpdatedAt)
	if err != nil {
		return store.OAuthToken{}, err
	}
	return store.OAuthToken{
		UserID:       userID,
		ProviderID:   providerID,
		AccessToken:  getBytes(item, attrAccessToken),
		RefreshToken: getBytes(item, attrRefreshToken),
		TokenType:    tokenType,
		ExpiresAt:    expiresAt,
		KeyVersion:   keyVersion,
		UpdatedAt:    updatedAt,
	}, nil
}

func (st *oauthTokenStore) Upsert(ctx context.Context, p store.UpsertOAuthTokensParams) error {
	now := time.Now().UTC()
	_, err := st.s.putItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(st.table()),
		Item: map[string]ddbtypes.AttributeValue{
			attrUserID:          attrS(p.UserID),
			attrProviderID:      attrS(p.ProviderID),
			attrAccessToken:     attrB(p.AccessToken),
			attrRefreshToken:    attrB(p.RefreshToken),
			attrTokenType:       attrS(p.TokenType),
			attrExpiresAt:       attrS(timeToStr(p.ExpiresAt.UTC())),
			attrKeyVersion:      attrS(strconv.FormatInt(p.KeyVersion, 10)),
			attrExpiryPartition: attrS(sentinelExpiryGroup),
			attrUpdatedAt:       attrS(timeToStr(now)),
		},
	}, attrUserID, attrProviderID)
	return mapErr(err)
}

func (st *oauthTokenStore) Get(ctx context.Context, p store.GetOAuthTokensParams) (*store.OAuthToken, error) {
	out, err := st.s.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(st.table()),
		Key: map[string]ddbtypes.AttributeValue{
			attrUserID:     attrS(p.UserID),
			attrProviderID: attrS(p.ProviderID),
		},
	})
	if err != nil {
		return nil, mapErr(err)
	}
	if out.Item == nil {
		return nil, store.ErrNotFound
	}
	t, err := itemToOAuthToken(out.Item)
	if err != nil {
		return nil, err
	}
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
		t, err := itemToOAuthToken(item)
		if err != nil {
			return false
		}
		tokens = append(tokens, t)
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
		t, err := itemToOAuthToken(item)
		if err != nil {
			return false
		}
		tokens = append(tokens, t)
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
	return deleteAllByGSI(ctx, st.s, st.table(), gsiProviderID, attrProviderID, providerID, attrUserID, attrProviderID)
}

func (st *oauthTokenStore) DeleteByUser(ctx context.Context, userID string) error {
	return deleteAllByPK(ctx, st.s, st.table(), attrUserID, userID, attrProviderID)
}

func (st *oauthTokenStore) DeleteByUserAndProvider(ctx context.Context, p store.DeleteOAuthTokensByUserAndProviderParams) error {
	_, err := st.s.deleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(st.table()),
		Key: map[string]ddbtypes.AttributeValue{
			attrUserID:     attrS(p.UserID),
			attrProviderID: attrS(p.ProviderID),
		},
	})
	return mapErr(err)
}
