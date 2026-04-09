package mongodb

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/ptrconv"
)

func oauthProviderToDoc(p store.CreateOAuthProviderParams, now time.Time) bson.D {
	return bson.D{
		{Key: "_id", Value: p.ID},
		{Key: "provider_type", Value: p.ProviderType},
		{Key: "name", Value: p.Name},
		{Key: "issuer_url", Value: p.IssuerURL},
		{Key: "client_id", Value: p.ClientID},
		{Key: "client_secret", Value: bytesVal(p.ClientSecret)},
		{Key: "scopes", Value: p.Scopes},
		{Key: "trust_email", Value: p.TrustEmail},
		{Key: "enabled", Value: p.Enabled},
		{Key: "created_at", Value: now},
	}
}

func docToOAuthProvider(m bson.M) store.OAuthProvider {
	return store.OAuthProvider{
		OAuthProviderSummary: docToOAuthProviderSummary(m),
		ClientSecret:         getBytes(m, "client_secret"),
	}
}

func docToOAuthProviderSummary(m bson.M) store.OAuthProviderSummary {
	return store.OAuthProviderSummary{
		ID:           getS(m, "_id"),
		ProviderType: getS(m, "provider_type"),
		Name:         getS(m, "name"),
		IssuerURL:    getS(m, "issuer_url"),
		ClientID:     getS(m, "client_id"),
		Scopes:       getS(m, "scopes"),
		TrustEmail:   getBool(m, "trust_email"),
		Enabled:      getBool(m, "enabled"),
		CreatedAt:    getTime(m, "created_at"),
	}
}

func (st *oauthProviderStore) Create(ctx context.Context, p store.CreateOAuthProviderParams) error {
	now := truncateMS(time.Now().UTC())
	doc := oauthProviderToDoc(p, now)
	_, err := st.s.collection(colOAuthProviders).InsertOne(ctx, doc)
	if err != nil {
		return mapErr(err)
	}
	st.s.trackInsert(colOAuthProviders, p.ID)
	return nil
}

func (st *oauthProviderStore) GetByID(ctx context.Context, id string) (*store.OAuthProvider, error) {
	var m bson.M
	err := st.s.collection(colOAuthProviders).FindOne(ctx, bson.D{{Key: "_id", Value: id}}).Decode(&m)
	if err != nil {
		return nil, mapErr(err)
	}
	p := docToOAuthProvider(m)
	return &p, nil
}

func (st *oauthProviderStore) ListEnabled(ctx context.Context) ([]store.OAuthProviderSummary, error) {
	filter := bson.D{{Key: "enabled", Value: true}}
	cursor, err := st.s.collection(colOAuthProviders).Find(ctx, filter)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = cursor.Close(ctx) }()

	var result []store.OAuthProviderSummary
	for cursor.Next(ctx) {
		var m bson.M
		if err := cursor.Decode(&m); err != nil {
			return nil, mapErr(err)
		}
		result = append(result, docToOAuthProviderSummary(m))
	}
	return ptrconv.NonNil(result), mapErr(cursor.Err())
}

func (st *oauthProviderStore) ListAll(ctx context.Context) ([]store.OAuthProviderSummary, error) {
	cursor, err := st.s.collection(colOAuthProviders).Find(ctx, bson.D{})
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = cursor.Close(ctx) }()

	var result []store.OAuthProviderSummary
	for cursor.Next(ctx) {
		var m bson.M
		if err := cursor.Decode(&m); err != nil {
			return nil, mapErr(err)
		}
		result = append(result, docToOAuthProviderSummary(m))
	}
	return ptrconv.NonNil(result), mapErr(cursor.Err())
}

func (st *oauthProviderStore) ListAllWithSecrets(ctx context.Context) ([]store.OAuthProvider, error) {
	cursor, err := st.s.collection(colOAuthProviders).Find(ctx, bson.D{})
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = cursor.Close(ctx) }()

	var result []store.OAuthProvider
	for cursor.Next(ctx) {
		var m bson.M
		if err := cursor.Decode(&m); err != nil {
			return nil, mapErr(err)
		}
		result = append(result, docToOAuthProvider(m))
	}
	return ptrconv.NonNil(result), mapErr(cursor.Err())
}

func (st *oauthProviderStore) UpdateEnabled(ctx context.Context, p store.UpdateOAuthProviderEnabledParams) error {
	filter := bson.D{{Key: "_id", Value: p.ID}}
	st.s.trackBeforeUpdate(ctx, colOAuthProviders, filter)
	update := bson.D{{Key: "$set", Value: bson.D{{Key: "enabled", Value: p.Enabled}}}}
	_, err := st.s.collection(colOAuthProviders).UpdateOne(ctx, filter, update)
	return mapErr(err)
}

func (st *oauthProviderStore) UpdateClientSecret(ctx context.Context, id string, clientSecret []byte) error {
	filter := bson.D{{Key: "_id", Value: id}}
	st.s.trackBeforeUpdate(ctx, colOAuthProviders, filter)
	update := bson.D{{Key: "$set", Value: bson.D{{Key: "client_secret", Value: clientSecret}}}}
	_, err := st.s.collection(colOAuthProviders).UpdateOne(ctx, filter, update)
	return mapErr(err)
}

func (st *oauthProviderStore) Delete(ctx context.Context, id string) error {
	filter := bson.D{{Key: "_id", Value: id}}
	st.s.trackBeforeDelete(ctx, colOAuthProviders, filter)
	_, err := st.s.collection(colOAuthProviders).DeleteOne(ctx, filter)
	if err != nil {
		return mapErr(err)
	}
	// Cascade to tokens and user links.
	_ = st.s.oauthTokens.DeleteByProvider(ctx, id)
	_ = st.s.oauthUserLinks.DeleteByProvider(ctx, id)
	return nil
}
