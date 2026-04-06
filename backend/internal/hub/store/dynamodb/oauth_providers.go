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

type oauthProviderStore struct{ s *dynamoStore }

var _ store.OAuthProviderStore = (*oauthProviderStore)(nil)

func (st *oauthProviderStore) table() string { return st.s.table(tableOAuthProviders) }

func oauthProviderToItem(p store.CreateOAuthProviderParams, now time.Time) map[string]ddbtypes.AttributeValue {
	return map[string]ddbtypes.AttributeValue{
		"id":            attrS(p.ID),
		"provider_type": attrS(p.ProviderType),
		"name":          attrS(p.Name),
		"issuer_url":    attrS(p.IssuerURL),
		"client_id":     attrS(p.ClientID),
		"client_secret": attrB(p.ClientSecret),
		"scopes":        attrS(p.Scopes),
		"trust_email":   attrBool(p.TrustEmail),
		"enabled":       attrBool(p.Enabled),
		"created_at":    attrS(timeToStr(now)),
	}
}

func itemToOAuthProvider(item map[string]ddbtypes.AttributeValue) *store.OAuthProvider {
	return &store.OAuthProvider{
		OAuthProviderSummary: itemToOAuthProviderSummary(item),
		ClientSecret:         getBytes(item, "client_secret"),
	}
}

func itemToOAuthProviderSummary(item map[string]ddbtypes.AttributeValue) store.OAuthProviderSummary {
	return store.OAuthProviderSummary{
		ID:           getS(item, "id"),
		ProviderType: getS(item, "provider_type"),
		Name:         getS(item, "name"),
		IssuerURL:    getS(item, "issuer_url"),
		ClientID:     getS(item, "client_id"),
		Scopes:       getS(item, "scopes"),
		TrustEmail:   getBool(item, "trust_email"),
		Enabled:      getBool(item, "enabled"),
		CreatedAt:    getTime(item, "created_at"),
	}
}

func (st *oauthProviderStore) Create(ctx context.Context, p store.CreateOAuthProviderParams) error {
	now := time.Now().UTC()
	_, err := st.s.putItem(ctx, &dynamodb.PutItemInput{
		TableName:           aws.String(st.table()),
		Item:                oauthProviderToItem(p, now),
		ConditionExpression: aws.String("attribute_not_exists(id)"),
	}, "id")
	return mapErr(err)
}

func (st *oauthProviderStore) GetByID(ctx context.Context, id string) (*store.OAuthProvider, error) {
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
	return itemToOAuthProvider(out.Item), nil
}

func (st *oauthProviderStore) ListEnabled(ctx context.Context) ([]store.OAuthProviderSummary, error) {
	var result []store.OAuthProviderSummary
	err := st.s.scanPages(ctx, &dynamodb.ScanInput{
		TableName:        aws.String(st.table()),
		FilterExpression: aws.String("enabled = :true"),
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":true": attrBool(true),
		},
	}, func(item map[string]ddbtypes.AttributeValue) bool {
		result = append(result, itemToOAuthProviderSummary(item))
		return true
	})
	if err != nil {
		return nil, err
	}
	return ptrconv.NonNil(result), nil
}

func (st *oauthProviderStore) ListAll(ctx context.Context) ([]store.OAuthProviderSummary, error) {
	var result []store.OAuthProviderSummary
	err := st.s.scanPages(ctx, &dynamodb.ScanInput{
		TableName: aws.String(st.table()),
	}, func(item map[string]ddbtypes.AttributeValue) bool {
		result = append(result, itemToOAuthProviderSummary(item))
		return true
	})
	if err != nil {
		return nil, err
	}
	return ptrconv.NonNil(result), nil
}

func (st *oauthProviderStore) ListAllWithSecrets(ctx context.Context) ([]store.OAuthProvider, error) {
	var result []store.OAuthProvider
	err := st.s.scanPages(ctx, &dynamodb.ScanInput{
		TableName: aws.String(st.table()),
	}, func(item map[string]ddbtypes.AttributeValue) bool {
		result = append(result, *itemToOAuthProvider(item))
		return true
	})
	if err != nil {
		return nil, err
	}
	return ptrconv.NonNil(result), nil
}

func (st *oauthProviderStore) UpdateEnabled(ctx context.Context, p store.UpdateOAuthProviderEnabledParams) error {
	_, err := st.s.updateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:           aws.String(st.table()),
		Key:                 map[string]ddbtypes.AttributeValue{"id": attrS(p.ID)},
		UpdateExpression:    aws.String("SET enabled = :e"),
		ConditionExpression: aws.String("attribute_exists(id)"),
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":e": attrBool(p.Enabled),
		},
	})
	return mapErr(err)
}

func (st *oauthProviderStore) UpdateClientSecret(ctx context.Context, id string, clientSecret []byte) error {
	_, err := st.s.updateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:           aws.String(st.table()),
		Key:                 map[string]ddbtypes.AttributeValue{"id": attrS(id)},
		UpdateExpression:    aws.String("SET client_secret = :cs"),
		ConditionExpression: aws.String("attribute_exists(id)"),
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":cs": attrB(clientSecret),
		},
	})
	return mapErr(err)
}

func (st *oauthProviderStore) Delete(ctx context.Context, id string) error {
	_, err := st.s.deleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(st.table()),
		Key:       map[string]ddbtypes.AttributeValue{"id": attrS(id)},
	})
	if err != nil {
		return mapErr(err)
	}
	// Cascade to tokens and user links.
	_ = st.s.oauthTokens.DeleteByProvider(ctx, id)
	_ = st.s.oauthUserLinks.DeleteByProvider(ctx, id)
	return nil
}
