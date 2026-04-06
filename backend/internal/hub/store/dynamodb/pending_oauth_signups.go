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

func (st *pendingOAuthSignupStore) Create(ctx context.Context, p store.CreatePendingOAuthSignupParams) error {
	now := time.Now().UTC()
	_, err := st.s.putItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(st.table()),
		Item: map[string]ddbtypes.AttributeValue{
			"token":            attrS(p.Token),
			"provider_id":      attrS(p.ProviderID),
			"provider_subject": attrS(p.ProviderSubject),
			"email":            attrS(store.NormalizeEmail(p.Email)),
			"display_name":     attrS(p.DisplayName),
			"access_token":     attrB(p.AccessToken),
			"refresh_token":    attrB(p.RefreshToken),
			"token_type":       attrS(p.TokenType),
			"token_expires_at": attrS(timeToStr(p.TokenExpiresAt)),
			"key_version":      attrN(p.KeyVersion),
			"redirect_uri":     attrS(p.RedirectURI),
			"expires_at":       attrS(timeToStr(p.ExpiresAt)),
			"created_at":       attrS(timeToStr(now)),
			"active":           attrS(sentinelActive),
			"ttl":              attrN(p.ExpiresAt.Unix()),
		},
	}, "token")
	return mapErr(err)
}

func (st *pendingOAuthSignupStore) Get(ctx context.Context, token string) (*store.PendingOAuthSignup, error) {
	out, err := st.s.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(st.table()),
		Key:       map[string]ddbtypes.AttributeValue{"token": attrS(token)},
	})
	if err != nil {
		return nil, mapErr(err)
	}
	if out.Item == nil {
		return nil, store.ErrNotFound
	}
	return &store.PendingOAuthSignup{
		Token:           getS(out.Item, "token"),
		ProviderID:      getS(out.Item, "provider_id"),
		ProviderSubject: getS(out.Item, "provider_subject"),
		Email:           getS(out.Item, "email"),
		DisplayName:     getS(out.Item, "display_name"),
		AccessToken:     getBytes(out.Item, "access_token"),
		RefreshToken:    getBytes(out.Item, "refresh_token"),
		TokenType:       getS(out.Item, "token_type"),
		TokenExpiresAt:  getTime(out.Item, "token_expires_at"),
		KeyVersion:      getN(out.Item, "key_version"),
		RedirectURI:     getS(out.Item, "redirect_uri"),
		ExpiresAt:       getTime(out.Item, "expires_at"),
		CreatedAt:       getTime(out.Item, "created_at"),
	}, nil
}

func (st *pendingOAuthSignupStore) Delete(ctx context.Context, token string) error {
	_, err := st.s.deleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(st.table()),
		Key:       map[string]ddbtypes.AttributeValue{"token": attrS(token)},
	})
	return mapErr(err)
}
