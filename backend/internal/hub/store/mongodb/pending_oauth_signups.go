package mongodb

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/leapmux/leapmux/internal/hub/store"
)

func docToPendingOAuthSignup(m bson.M) store.PendingOAuthSignup {
	return store.PendingOAuthSignup{
		Token:           getS(m, "_id"),
		ProviderID:      getS(m, "provider_id"),
		ProviderSubject: getS(m, "provider_subject"),
		Email:           getS(m, "email"),
		DisplayName:     getS(m, "display_name"),
		AccessToken:     getBytes(m, "access_token"),
		RefreshToken:    getBytes(m, "refresh_token"),
		TokenType:       getS(m, "token_type"),
		TokenExpiresAt:  getTime(m, "token_expires_at"),
		KeyVersion:      getInt64(m, "key_version"),
		RedirectURI:     getS(m, "redirect_uri"),
		ExpiresAt:       getTime(m, "expires_at"),
		CreatedAt:       getTime(m, "created_at"),
	}
}

func (st *pendingOAuthSignupStore) Create(ctx context.Context, p store.CreatePendingOAuthSignupParams) error {
	now := truncateMS(time.Now().UTC())
	doc := bson.D{
		{Key: "_id", Value: p.Token},
		{Key: "provider_id", Value: p.ProviderID},
		{Key: "provider_subject", Value: p.ProviderSubject},
		{Key: "email", Value: store.NormalizeEmail(p.Email)},
		{Key: "display_name", Value: p.DisplayName},
		{Key: "access_token", Value: bytesVal(p.AccessToken)},
		{Key: "refresh_token", Value: bytesVal(p.RefreshToken)},
		{Key: "token_type", Value: p.TokenType},
		{Key: "token_expires_at", Value: truncateMS(p.TokenExpiresAt)},
		{Key: "key_version", Value: p.KeyVersion},
		{Key: "redirect_uri", Value: p.RedirectURI},
		{Key: "expires_at", Value: truncateMS(p.ExpiresAt)},
		{Key: "created_at", Value: now},
	}
	_, err := st.s.collection(colPendingOAuthSignups).InsertOne(ctx, doc)
	if err != nil {
		return mapErr(err)
	}
	st.s.trackInsert(colPendingOAuthSignups, p.Token)
	return nil
}

func (st *pendingOAuthSignupStore) Get(ctx context.Context, token string) (*store.PendingOAuthSignup, error) {
	var m bson.M
	err := st.s.collection(colPendingOAuthSignups).FindOne(ctx, bson.D{{Key: "_id", Value: token}}).Decode(&m)
	if err != nil {
		return nil, mapErr(err)
	}
	s := docToPendingOAuthSignup(m)
	return &s, nil
}

func (st *pendingOAuthSignupStore) Delete(ctx context.Context, token string) error {
	_, err := st.s.collection(colPendingOAuthSignups).DeleteOne(ctx, bson.D{{Key: "_id", Value: token}})
	return mapErr(err)
}
