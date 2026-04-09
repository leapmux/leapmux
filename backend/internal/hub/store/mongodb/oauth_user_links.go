package mongodb

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/ptrconv"
)

func docToOAuthUserLink(m bson.M) store.OAuthUserLink {
	return store.OAuthUserLink{
		UserID:          getS(m, "user_id"),
		ProviderID:      getS(m, "provider_id"),
		ProviderSubject: getS(m, "provider_subject"),
		CreatedAt:       getTime(m, "created_at"),
	}
}

func (st *oauthUserLinkStore) Create(ctx context.Context, p store.CreateOAuthUserLinkParams) error {
	now := truncateMS(time.Now().UTC())
	id := compoundID(p.UserID, p.ProviderID)
	doc := bson.D{
		{Key: "_id", Value: id},
		{Key: "user_id", Value: p.UserID},
		{Key: "provider_id", Value: p.ProviderID},
		{Key: "provider_subject", Value: p.ProviderSubject},
		{Key: "created_at", Value: now},
	}
	_, err := st.s.collection(colOAuthUserLinks).InsertOne(ctx, doc)
	if err != nil {
		return mapErr(err)
	}
	st.s.trackInsert(colOAuthUserLinks, id)
	return nil
}

func (st *oauthUserLinkStore) Get(ctx context.Context, p store.GetOAuthUserLinkParams) (*store.OAuthUserLink, error) {
	filter := bson.D{
		{Key: "provider_id", Value: p.ProviderID},
		{Key: "provider_subject", Value: p.ProviderSubject},
	}
	var m bson.M
	err := st.s.collection(colOAuthUserLinks).FindOne(ctx, filter).Decode(&m)
	if err != nil {
		return nil, mapErr(err)
	}
	link := docToOAuthUserLink(m)
	return &link, nil
}

func (st *oauthUserLinkStore) ListByUser(ctx context.Context, userID string) ([]store.OAuthUserLink, error) {
	filter := bson.D{{Key: "user_id", Value: userID}}
	cursor, err := st.s.collection(colOAuthUserLinks).Find(ctx, filter)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = cursor.Close(ctx) }()

	var result []store.OAuthUserLink
	for cursor.Next(ctx) {
		var m bson.M
		if err := cursor.Decode(&m); err != nil {
			return nil, mapErr(err)
		}
		result = append(result, docToOAuthUserLink(m))
	}
	return ptrconv.NonNil(result), mapErr(cursor.Err())
}

func (st *oauthUserLinkStore) Delete(ctx context.Context, p store.DeleteOAuthUserLinkParams) error {
	id := compoundID(p.UserID, p.ProviderID)
	filter := bson.D{{Key: "_id", Value: id}}
	st.s.trackBeforeDelete(ctx, colOAuthUserLinks, filter)
	_, err := st.s.collection(colOAuthUserLinks).DeleteOne(ctx, filter)
	return mapErr(err)
}

func (st *oauthUserLinkStore) DeleteByProvider(ctx context.Context, providerID string) error {
	_, err := st.s.collection(colOAuthUserLinks).DeleteMany(ctx, bson.D{{Key: "provider_id", Value: providerID}})
	return mapErr(err)
}
