package dynamodb

import (
	"context"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/leapmux/leapmux/internal/hub/store"
)

type oauthStateStore struct{ s *dynamoStore }

var _ store.OAuthStateStore = (*oauthStateStore)(nil)

func (st *oauthStateStore) table() string { return st.s.table(tableOAuthStates) }

func itemToOAuthState(item map[string]ddbtypes.AttributeValue) (*store.OAuthState, error) {
	state, err := mustGetS(item, attrState)
	if err != nil {
		return nil, err
	}
	providerID, err := mustGetS(item, attrProviderID)
	if err != nil {
		return nil, err
	}
	pkceVerifier, err := mustGetS(item, attrPKCEVerifier)
	if err != nil {
		return nil, err
	}
	redirectURI, err := mustGetS(item, attrRedirectURI)
	if err != nil {
		return nil, err
	}
	expiresAt, err := mustGetTime(item, attrExpiresAt)
	if err != nil {
		return nil, err
	}
	createdAt, err := mustGetTime(item, attrCreatedAt)
	if err != nil {
		return nil, err
	}
	return &store.OAuthState{
		State:        state,
		ProviderID:   providerID,
		PkceVerifier: pkceVerifier,
		RedirectURI:  redirectURI,
		ExpiresAt:    expiresAt,
		CreatedAt:    createdAt,
	}, nil
}

func (st *oauthStateStore) Create(ctx context.Context, p store.CreateOAuthStateParams) error {
	now := time.Now().UTC()
	_, err := st.s.putItem(ctx, &dynamodb.PutItemInput{
		TableName:           aws.String(st.table()),
		ConditionExpression: aws.String("attribute_not_exists(#s)"),
		ExpressionAttributeNames: map[string]string{
			"#s": attrState,
		},
		Item: map[string]ddbtypes.AttributeValue{
			attrState:        attrS(p.State),
			attrProviderID:   attrS(p.ProviderID),
			attrPKCEVerifier: attrS(p.PkceVerifier),
			attrRedirectURI:  attrS(p.RedirectURI),
			attrExpiresAt:    attrS(timeToStr(p.ExpiresAt)),
			attrCreatedAt:    attrS(timeToStr(now)),
			attrActive:       attrS(sentinelActive),
			attrTTL:          attrN(p.ExpiresAt.Unix()),
		},
	}, attrState)
	return mapErr(err)
}

func (st *oauthStateStore) Get(ctx context.Context, state string) (*store.OAuthState, error) {
	out, err := st.s.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(st.table()),
		Key:       map[string]ddbtypes.AttributeValue{attrState: attrS(state)},
	})
	if err != nil {
		return nil, mapErr(err)
	}
	if out.Item == nil {
		return nil, store.ErrNotFound
	}
	s, err := itemToOAuthState(out.Item)
	if err != nil {
		return nil, err
	}
	return s, nil
}

func (st *oauthStateStore) Delete(ctx context.Context, state string) error {
	_, err := st.s.deleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(st.table()),
		Key:       map[string]ddbtypes.AttributeValue{attrState: attrS(state)},
	})
	return mapErr(err)
}
