package mongodb

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/ptrconv"
)

func docToOAuthToken(m bson.M) store.OAuthToken {
	return store.OAuthToken{
		UserID:       getS(m, "user_id"),
		ProviderID:   getS(m, "provider_id"),
		AccessToken:  getBytes(m, "access_token"),
		RefreshToken: getBytes(m, "refresh_token"),
		TokenType:    getS(m, "token_type"),
		ExpiresAt:    getTime(m, "expires_at"),
		KeyVersion:   getInt64(m, "key_version"),
		UpdatedAt:    getTime(m, "updated_at"),
	}
}

func (st *oauthTokenStore) Upsert(ctx context.Context, p store.UpsertOAuthTokensParams) error {
	now := truncateMS(time.Now().UTC())
	id := compoundID(p.UserID, p.ProviderID)
	doc := bson.D{
		{Key: "_id", Value: id},
		{Key: "user_id", Value: p.UserID},
		{Key: "provider_id", Value: p.ProviderID},
		{Key: "access_token", Value: bytesVal(p.AccessToken)},
		{Key: "refresh_token", Value: bytesVal(p.RefreshToken)},
		{Key: "token_type", Value: p.TokenType},
		{Key: "expires_at", Value: truncateMS(p.ExpiresAt)},
		{Key: "key_version", Value: p.KeyVersion},
		{Key: "updated_at", Value: now},
	}
	opts := options.Replace().SetUpsert(true)
	_, err := st.s.collection(colOAuthTokens).ReplaceOne(ctx, bson.D{{Key: "_id", Value: id}}, doc, opts)
	return mapErr(err)
}

func (st *oauthTokenStore) Get(ctx context.Context, p store.GetOAuthTokensParams) (*store.OAuthToken, error) {
	id := compoundID(p.UserID, p.ProviderID)
	var m bson.M
	err := st.s.collection(colOAuthTokens).FindOne(ctx, bson.D{{Key: "_id", Value: id}}).Decode(&m)
	if err != nil {
		return nil, mapErr(err)
	}
	t := docToOAuthToken(m)
	return &t, nil
}

func (st *oauthTokenStore) ListExpiring(ctx context.Context) ([]store.OAuthToken, error) {
	threshold := time.Now().UTC().Add(5 * time.Minute)
	filter := bson.D{{Key: "expires_at", Value: bson.D{{Key: "$lte", Value: threshold}}}}
	cursor, err := st.s.collection(colOAuthTokens).Find(ctx, filter)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = cursor.Close(ctx) }()

	var tokens []store.OAuthToken
	for cursor.Next(ctx) {
		var m bson.M
		if err := cursor.Decode(&m); err != nil {
			return nil, mapErr(err)
		}
		tokens = append(tokens, docToOAuthToken(m))
	}
	return ptrconv.NonNil(tokens), mapErr(cursor.Err())
}

func (st *oauthTokenStore) ListByKeyVersion(ctx context.Context, keyVersion int64) ([]store.OAuthToken, error) {
	filter := bson.D{{Key: "key_version", Value: keyVersion}}
	cursor, err := st.s.collection(colOAuthTokens).Find(ctx, filter)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = cursor.Close(ctx) }()

	var tokens []store.OAuthToken
	for cursor.Next(ctx) {
		var m bson.M
		if err := cursor.Decode(&m); err != nil {
			return nil, mapErr(err)
		}
		tokens = append(tokens, docToOAuthToken(m))
	}
	return ptrconv.NonNil(tokens), mapErr(cursor.Err())
}

func (st *oauthTokenStore) CountByKeyVersion(ctx context.Context, keyVersion int64) (int64, error) {
	filter := bson.D{{Key: "key_version", Value: keyVersion}}
	count, err := st.s.collection(colOAuthTokens).CountDocuments(ctx, filter)
	if err != nil {
		return 0, mapErr(err)
	}
	return count, nil
}

func (st *oauthTokenStore) DeleteByProvider(ctx context.Context, providerID string) error {
	_, err := st.s.collection(colOAuthTokens).DeleteMany(ctx, bson.D{{Key: "provider_id", Value: providerID}})
	return mapErr(err)
}

func (st *oauthTokenStore) DeleteByUser(ctx context.Context, userID string) error {
	_, err := st.s.collection(colOAuthTokens).DeleteMany(ctx, bson.D{{Key: "user_id", Value: userID}})
	return mapErr(err)
}

func (st *oauthTokenStore) DeleteByUserAndProvider(ctx context.Context, p store.DeleteOAuthTokensByUserAndProviderParams) error {
	id := compoundID(p.UserID, p.ProviderID)
	_, err := st.s.collection(colOAuthTokens).DeleteOne(ctx, bson.D{{Key: "_id", Value: id}})
	return mapErr(err)
}
