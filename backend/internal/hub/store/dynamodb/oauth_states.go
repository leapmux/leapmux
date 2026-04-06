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

func (st *oauthStateStore) Create(ctx context.Context, p store.CreateOAuthStateParams) error {
	now := time.Now().UTC()
	_, err := st.s.putItem(ctx, &dynamodb.PutItemInput{
		TableName:           aws.String(st.table()),
		ConditionExpression: aws.String("attribute_not_exists(#s)"),
		ExpressionAttributeNames: map[string]string{
			"#s": "state",
		},
		Item: map[string]ddbtypes.AttributeValue{
			"state":         attrS(p.State),
			"provider_id":   attrS(p.ProviderID),
			"pkce_verifier": attrS(p.PkceVerifier),
			"redirect_uri":  attrS(p.RedirectURI),
			"expires_at":    attrS(timeToStr(p.ExpiresAt)),
			"created_at":    attrS(timeToStr(now)),
			"active":        attrS(sentinelActive),
			"ttl":           attrN(p.ExpiresAt.Unix()),
		},
	}, "state")
	return mapErr(err)
}

func (st *oauthStateStore) Get(ctx context.Context, state string) (*store.OAuthState, error) {
	out, err := st.s.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(st.table()),
		Key:       map[string]ddbtypes.AttributeValue{"state": attrS(state)},
	})
	if err != nil {
		return nil, mapErr(err)
	}
	if out.Item == nil {
		return nil, store.ErrNotFound
	}
	return &store.OAuthState{
		State:        getS(out.Item, "state"),
		ProviderID:   getS(out.Item, "provider_id"),
		PkceVerifier: getS(out.Item, "pkce_verifier"),
		RedirectURI:  getS(out.Item, "redirect_uri"),
		ExpiresAt:    getTime(out.Item, "expires_at"),
		CreatedAt:    getTime(out.Item, "created_at"),
	}, nil
}

func (st *oauthStateStore) Delete(ctx context.Context, state string) error {
	_, err := st.s.deleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(st.table()),
		Key:       map[string]ddbtypes.AttributeValue{"state": attrS(state)},
	})
	return mapErr(err)
}
