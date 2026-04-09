package mongodb

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/ptrconv"
)

func sessionToDoc(p store.CreateSessionParams, now time.Time) bson.D {
	return bson.D{
		{Key: "_id", Value: p.ID},
		{Key: "user_id", Value: p.UserID},
		{Key: "expires_at", Value: truncateMS(p.ExpiresAt)},
		{Key: "created_at", Value: now},
		{Key: "last_active_at", Value: now},
		{Key: "user_agent", Value: p.UserAgent},
		{Key: "ip_address", Value: p.IPAddress},
	}
}

func docToSession(m bson.M) store.UserSession {
	return store.UserSession{
		ID:           getS(m, "_id"),
		UserID:       getS(m, "user_id"),
		ExpiresAt:    getTime(m, "expires_at"),
		CreatedAt:    getTime(m, "created_at"),
		LastActiveAt: getTime(m, "last_active_at"),
		UserAgent:    getS(m, "user_agent"),
		IPAddress:    getS(m, "ip_address"),
	}
}

func (st *sessionStore) Create(ctx context.Context, p store.CreateSessionParams) error {
	now := truncateMS(time.Now().UTC())
	doc := sessionToDoc(p, now)
	_, err := st.s.collection(colSessions).InsertOne(ctx, doc)
	if err != nil {
		return mapErr(err)
	}
	st.s.trackInsert(colSessions, p.ID)
	return nil
}

func (st *sessionStore) GetByID(ctx context.Context, id string) (*store.UserSession, error) {
	now := truncateMS(time.Now().UTC())
	filter := bson.D{
		{Key: "_id", Value: id},
		{Key: "expires_at", Value: bson.D{{Key: "$gt", Value: now}}},
	}
	var m bson.M
	err := st.s.collection(colSessions).FindOne(ctx, filter).Decode(&m)
	if err != nil {
		return nil, mapErr(err)
	}
	sess := docToSession(m)
	return &sess, nil
}

func (st *sessionStore) Touch(ctx context.Context, p store.TouchSessionParams) error {
	now := truncateMS(time.Now().UTC())
	filter := bson.D{
		{Key: "_id", Value: p.ID},
		{Key: "last_active_at", Value: bson.D{{Key: "$lt", Value: truncateMS(p.LastActiveAt)}}},
	}
	st.s.trackBeforeUpdate(ctx, colSessions, filter)
	update := bson.D{
		{Key: "$set", Value: bson.D{
			{Key: "last_active_at", Value: now},
			{Key: "expires_at", Value: truncateMS(p.ExpiresAt)},
		}},
	}
	_, err := st.s.collection(colSessions).UpdateOne(ctx, filter, update)
	return mapErr(err)
}

func (st *sessionStore) Delete(ctx context.Context, id string) (int64, error) {
	filter := bson.D{{Key: "_id", Value: id}}
	st.s.trackBeforeDelete(ctx, colSessions, filter)
	res, err := st.s.collection(colSessions).DeleteOne(ctx, filter)
	if err != nil {
		return 0, mapErr(err)
	}
	return res.DeletedCount, nil
}

func (st *sessionStore) DeleteByUser(ctx context.Context, userID string) error {
	_, err := st.s.collection(colSessions).DeleteMany(ctx, bson.D{{Key: "user_id", Value: userID}})
	return mapErr(err)
}

func (st *sessionStore) DeleteOthers(ctx context.Context, p store.DeleteOtherSessionsParams) error {
	filter := bson.D{
		{Key: "user_id", Value: p.UserID},
		{Key: "_id", Value: bson.D{{Key: "$ne", Value: p.KeepID}}},
	}
	_, err := st.s.collection(colSessions).DeleteMany(ctx, filter)
	return mapErr(err)
}

func (st *sessionStore) ListByUserID(ctx context.Context, userID string) ([]store.UserSession, error) {
	now := truncateMS(time.Now().UTC())
	filter := bson.D{
		{Key: "user_id", Value: userID},
		{Key: "expires_at", Value: bson.D{{Key: "$gt", Value: now}}},
	}
	opts := options.Find().
		SetSort(bson.D{{Key: "last_active_at", Value: -1}}).
		SetLimit(1000)

	cursor, err := st.s.collection(colSessions).Find(ctx, filter, opts)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = cursor.Close(ctx) }()

	var sessions []store.UserSession
	for cursor.Next(ctx) {
		var m bson.M
		if err := cursor.Decode(&m); err != nil {
			return nil, mapErr(err)
		}
		sessions = append(sessions, docToSession(m))
	}
	return ptrconv.NonNil(sessions), mapErr(cursor.Err())
}

func (st *sessionStore) ListAllActive(ctx context.Context, p store.ListAllActiveSessionsParams) ([]store.ActiveSession, error) {
	now := truncateMS(time.Now().UTC())
	filter := bson.D{
		{Key: "expires_at", Value: bson.D{{Key: "$gt", Value: now}}},
	}

	// Apply cursor-based pagination using last_active_at.
	if cursorTime, ok, err := store.ParseCursorTime(p.Cursor); err != nil {
		return nil, err
	} else if ok {
		filter = append(filter, bson.E{
			Key:   "last_active_at",
			Value: bson.D{{Key: "$lt", Value: cursorTime}},
		})
	}

	opts := options.Find().
		SetSort(bson.D{{Key: "last_active_at", Value: -1}}).
		SetLimit(p.Limit)

	cursor, err := st.s.collection(colSessions).Find(ctx, filter, opts)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = cursor.Close(ctx) }()

	var sessions []store.UserSession
	for cursor.Next(ctx) {
		var m bson.M
		if err := cursor.Decode(&m); err != nil {
			return nil, mapErr(err)
		}
		sessions = append(sessions, docToSession(m))
	}
	if err := cursor.Err(); err != nil {
		return nil, mapErr(err)
	}

	// Batch-fetch all usernames.
	userIDs := store.MapSlice(sessions, func(s store.UserSession) string { return s.UserID })
	usernames, err := st.s.lookupUsernames(ctx, userIDs)
	if err != nil {
		return nil, err
	}

	return store.SessionsToActive(sessions, usernames), nil
}

func (st *sessionStore) ValidateWithUser(ctx context.Context, id string) (*store.SessionWithUser, error) {
	sess, err := st.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}

	filter := bson.D{
		{Key: "_id", Value: sess.UserID},
		{Key: "deleted_at", Value: nil},
	}
	proj := bson.D{
		{Key: "_id", Value: 1},
		{Key: "org_id", Value: 1},
		{Key: "username", Value: 1},
		{Key: "is_admin", Value: 1},
		{Key: "email_verified", Value: 1},
	}
	var m bson.M
	err = st.s.collection(colUsers).FindOne(ctx, filter,
		options.FindOne().SetProjection(proj),
	).Decode(&m)
	if err != nil {
		return nil, mapErr(err)
	}

	return &store.SessionWithUser{
		UserID:        getS(m, "_id"),
		OrgID:         getS(m, "org_id"),
		Username:      getS(m, "username"),
		IsAdmin:       getBool(m, "is_admin"),
		EmailVerified: getBool(m, "email_verified"),
	}, nil
}
