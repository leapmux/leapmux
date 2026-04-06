package mongodb

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/leapmux/leapmux/internal/hub/store"
)

func docToOAuthState(m bson.M) store.OAuthState {
	return store.OAuthState{
		State:        getS(m, "_id"),
		ProviderID:   getS(m, "provider_id"),
		PkceVerifier: getS(m, "pkce_verifier"),
		RedirectURI:  getS(m, "redirect_uri"),
		ExpiresAt:    getTime(m, "expires_at"),
		CreatedAt:    getTime(m, "created_at"),
	}
}

func (st *oauthStateStore) Create(ctx context.Context, p store.CreateOAuthStateParams) error {
	now := truncateMS(time.Now().UTC())
	doc := bson.D{
		{Key: "_id", Value: p.State},
		{Key: "provider_id", Value: p.ProviderID},
		{Key: "pkce_verifier", Value: p.PkceVerifier},
		{Key: "redirect_uri", Value: p.RedirectURI},
		{Key: "expires_at", Value: truncateMS(p.ExpiresAt)},
		{Key: "created_at", Value: now},
	}
	_, err := st.s.collection(colOAuthStates).InsertOne(ctx, doc)
	if err != nil {
		return mapErr(err)
	}
	st.s.trackInsert(colOAuthStates, p.State)
	return nil
}

func (st *oauthStateStore) Get(ctx context.Context, state string) (*store.OAuthState, error) {
	var m bson.M
	err := st.s.collection(colOAuthStates).FindOne(ctx, bson.D{{Key: "_id", Value: state}}).Decode(&m)
	if err != nil {
		return nil, mapErr(err)
	}
	s := docToOAuthState(m)
	return &s, nil
}

func (st *oauthStateStore) Delete(ctx context.Context, state string) error {
	_, err := st.s.collection(colOAuthStates).DeleteOne(ctx, bson.D{{Key: "_id", Value: state}})
	return mapErr(err)
}
