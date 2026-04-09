package dynamodb

import (
	"context"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/leapmux/leapmux/internal/hub/store"
)

type pendingOAuthSignupStore struct{ s *dynamoStore }

var _ store.PendingOAuthSignupStore = (*pendingOAuthSignupStore)(nil)

func (st *pendingOAuthSignupStore) table() string { return st.s.table(tablePendingOAuthSignups) }

func itemToPendingOAuthSignup(item map[string]ddbtypes.AttributeValue) (*store.PendingOAuthSignup, error) {
	token, err := mustGetS(item, attrToken)
	if err != nil {
		return nil, err
	}
	providerID, err := mustGetS(item, attrProviderID)
	if err != nil {
		return nil, err
	}
	providerSubject, err := mustGetS(item, attrProviderSubject)
	if err != nil {
		return nil, err
	}
	email, err := mustGetS(item, attrEmail)
	if err != nil {
		return nil, err
	}
	displayName, err := mustGetS(item, attrDisplayName)
	if err != nil {
		return nil, err
	}
	tokenType, err := mustGetS(item, attrTokenType)
	if err != nil {
		return nil, err
	}
	tokenExpiresAt, err := mustGetTime(item, attrTokenExpiresAt)
	if err != nil {
		return nil, err
	}
	keyVersion, err := mustGetN(item, attrKeyVersion)
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
	return &store.PendingOAuthSignup{
		Token:           token,
		ProviderID:      providerID,
		ProviderSubject: providerSubject,
		Email:           email,
		DisplayName:     displayName,
		AccessToken:     getBytes(item, attrAccessToken),
		RefreshToken:    getBytes(item, attrRefreshToken),
		TokenType:       tokenType,
		TokenExpiresAt:  tokenExpiresAt,
		KeyVersion:      keyVersion,
		RedirectURI:     redirectURI,
		ExpiresAt:       expiresAt,
		CreatedAt:       createdAt,
	}, nil
}

func (st *pendingOAuthSignupStore) Create(ctx context.Context, p store.CreatePendingOAuthSignupParams) error {
	now := time.Now().UTC()
	_, err := st.s.putItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(st.table()),
		Item: map[string]ddbtypes.AttributeValue{
			attrToken:           attrS(p.Token),
			attrProviderID:      attrS(p.ProviderID),
			attrProviderSubject: attrS(p.ProviderSubject),
			attrEmail:           attrS(store.NormalizeEmail(p.Email)),
			attrDisplayName:     attrS(p.DisplayName),
			attrAccessToken:     attrB(p.AccessToken),
			attrRefreshToken:    attrB(p.RefreshToken),
			attrTokenType:       attrS(p.TokenType),
			attrTokenExpiresAt:  attrS(timeToStr(p.TokenExpiresAt)),
			attrKeyVersion:      attrN(p.KeyVersion),
			attrRedirectURI:     attrS(p.RedirectURI),
			attrExpiresAt:       attrS(timeToStr(p.ExpiresAt)),
			attrCreatedAt:       attrS(timeToStr(now)),
			attrActive:          attrS(sentinelActive),
			attrTTL:             attrN(p.ExpiresAt.Unix()),
		},
	}, attrToken)
	return mapErr(err)
}

func (st *pendingOAuthSignupStore) Get(ctx context.Context, token string) (*store.PendingOAuthSignup, error) {
	out, err := st.s.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(st.table()),
		Key:       map[string]ddbtypes.AttributeValue{attrToken: attrS(token)},
	})
	if err != nil {
		return nil, mapErr(err)
	}
	if out.Item == nil {
		return nil, store.ErrNotFound
	}
	s, err := itemToPendingOAuthSignup(out.Item)
	if err != nil {
		return nil, err
	}
	return s, nil
}

func (st *pendingOAuthSignupStore) Delete(ctx context.Context, token string) error {
	_, err := st.s.deleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(st.table()),
		Key:       map[string]ddbtypes.AttributeValue{attrToken: attrS(token)},
	})
	return mapErr(err)
}
