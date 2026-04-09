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
		attrID:           attrS(p.ID),
		attrProviderType: attrS(p.ProviderType),
		attrName:         attrS(p.Name),
		attrIssuerURL:    attrS(p.IssuerURL),
		attrClientID:     attrS(p.ClientID),
		attrClientSecret: attrB(p.ClientSecret),
		attrScopes:       attrS(p.Scopes),
		attrTrustEmail:   attrBool(p.TrustEmail),
		attrEnabled:      attrBool(p.Enabled),
		attrCreatedAt:    attrS(timeToStr(now)),
	}
}

func itemToOAuthProvider(item map[string]ddbtypes.AttributeValue) (*store.OAuthProvider, error) {
	summary, err := itemToOAuthProviderSummary(item)
	if err != nil {
		return nil, err
	}
	return &store.OAuthProvider{
		OAuthProviderSummary: summary,
		ClientSecret:         getBytes(item, attrClientSecret),
	}, nil
}

func itemToOAuthProviderSummary(item map[string]ddbtypes.AttributeValue) (store.OAuthProviderSummary, error) {
	id, err := mustGetS(item, attrID)
	if err != nil {
		return store.OAuthProviderSummary{}, err
	}
	providerType, err := mustGetS(item, attrProviderType)
	if err != nil {
		return store.OAuthProviderSummary{}, err
	}
	name, err := mustGetS(item, attrName)
	if err != nil {
		return store.OAuthProviderSummary{}, err
	}
	issuerURL, err := mustGetS(item, attrIssuerURL)
	if err != nil {
		return store.OAuthProviderSummary{}, err
	}
	clientID, err := mustGetS(item, attrClientID)
	if err != nil {
		return store.OAuthProviderSummary{}, err
	}
	scopes, err := mustGetS(item, attrScopes)
	if err != nil {
		return store.OAuthProviderSummary{}, err
	}
	trustEmail, err := mustGetBool(item, attrTrustEmail)
	if err != nil {
		return store.OAuthProviderSummary{}, err
	}
	enabled, err := mustGetBool(item, attrEnabled)
	if err != nil {
		return store.OAuthProviderSummary{}, err
	}
	createdAt, err := mustGetTime(item, attrCreatedAt)
	if err != nil {
		return store.OAuthProviderSummary{}, err
	}
	return store.OAuthProviderSummary{
		ID:           id,
		ProviderType: providerType,
		Name:         name,
		IssuerURL:    issuerURL,
		ClientID:     clientID,
		Scopes:       scopes,
		TrustEmail:   trustEmail,
		Enabled:      enabled,
		CreatedAt:    createdAt,
	}, nil
}

func (st *oauthProviderStore) Create(ctx context.Context, p store.CreateOAuthProviderParams) error {
	now := time.Now().UTC()
	_, err := st.s.putItem(ctx, &dynamodb.PutItemInput{
		TableName:           aws.String(st.table()),
		Item:                oauthProviderToItem(p, now),
		ConditionExpression: aws.String("attribute_not_exists(id)"),
	}, attrID)
	return mapErr(err)
}

func (st *oauthProviderStore) GetByID(ctx context.Context, id string) (*store.OAuthProvider, error) {
	out, err := st.s.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(st.table()),
		Key:       map[string]ddbtypes.AttributeValue{attrID: attrS(id)},
	})
	if err != nil {
		return nil, mapErr(err)
	}
	if out.Item == nil {
		return nil, store.ErrNotFound
	}
	p, err := itemToOAuthProvider(out.Item)
	if err != nil {
		return nil, err
	}
	return p, nil
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
		s, err := itemToOAuthProviderSummary(item)
		if err != nil {
			return false
		}
		result = append(result, s)
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
		s, err := itemToOAuthProviderSummary(item)
		if err != nil {
			return false
		}
		result = append(result, s)
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
		p, err := itemToOAuthProvider(item)
		if err != nil {
			return false
		}
		result = append(result, *p)
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
		Key:                 map[string]ddbtypes.AttributeValue{attrID: attrS(p.ID)},
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
		Key:                 map[string]ddbtypes.AttributeValue{attrID: attrS(id)},
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
		Key:       map[string]ddbtypes.AttributeValue{attrID: attrS(id)},
	})
	if err != nil {
		return mapErr(err)
	}
	// Cascade to tokens and user links.
	_ = st.s.oauthTokens.DeleteByProvider(ctx, id)
	_ = st.s.oauthUserLinks.DeleteByProvider(ctx, id)
	return nil
}
