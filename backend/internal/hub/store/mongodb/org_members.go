package mongodb

import (
	"context"
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/ptrconv"
)

func orgMemberToDoc(p store.CreateOrgMemberParams, now time.Time) bson.D {
	return bson.D{
		{Key: "_id", Value: compoundID(p.OrgID, p.UserID)},
		{Key: "org_id", Value: p.OrgID},
		{Key: "user_id", Value: p.UserID},
		{Key: "role", Value: int32(p.Role)},
		{Key: "joined_at", Value: now},
	}
}

func docToOrgMember(m bson.M) store.OrgMember {
	return store.OrgMember{
		OrgID:    getS(m, "org_id"),
		UserID:   getS(m, "user_id"),
		Role:     leapmuxv1.OrgMemberRole(getInt32(m, "role")),
		JoinedAt: getTime(m, "joined_at"),
	}
}

func (st *orgMemberStore) Create(ctx context.Context, p store.CreateOrgMemberParams) error {
	now := truncateMS(time.Now().UTC())
	doc := orgMemberToDoc(p, now)
	_, err := st.s.collection(colOrgMembers).InsertOne(ctx, doc)
	if err != nil {
		return mapErr(err)
	}
	st.s.trackInsert(colOrgMembers, compoundID(p.OrgID, p.UserID))
	return nil
}

func (st *orgMemberStore) GetByOrgAndUser(ctx context.Context, orgID, userID string) (*store.OrgMember, error) {
	filter := bson.D{{Key: "_id", Value: compoundID(orgID, userID)}}
	var m bson.M
	err := st.s.collection(colOrgMembers).FindOne(ctx, filter).Decode(&m)
	if err != nil {
		return nil, mapErr(err)
	}
	om := docToOrgMember(m)
	return &om, nil
}

func (st *orgMemberStore) ListByOrgID(ctx context.Context, orgID string) ([]store.OrgMemberWithUser, error) {
	filter := bson.D{{Key: "org_id", Value: orgID}}
	cursor, err := st.s.collection(colOrgMembers).Find(ctx, filter)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = cursor.Close(ctx) }()

	var members []store.OrgMember
	for cursor.Next(ctx) {
		var m bson.M
		if err := cursor.Decode(&m); err != nil {
			return nil, mapErr(err)
		}
		members = append(members, docToOrgMember(m))
	}
	if err := cursor.Err(); err != nil {
		return nil, mapErr(err)
	}

	// Batch-fetch all users in a single query.
	userIDs := store.MapSlice(members, func(om store.OrgMember) string { return om.UserID })
	userFilter := bson.D{
		{Key: "_id", Value: bson.D{{Key: "$in", Value: userIDs}}},
		{Key: "deleted_at", Value: nil},
	}
	userCursor, err := st.s.collection(colUsers).Find(ctx, userFilter)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = userCursor.Close(ctx) }()

	userMap := make(map[string]store.User, len(userIDs))
	for userCursor.Next(ctx) {
		var m bson.M
		if err := userCursor.Decode(&m); err != nil {
			return nil, mapErr(err)
		}
		u := docToUser(m)
		userMap[u.ID] = u
	}
	if err := userCursor.Err(); err != nil {
		return nil, mapErr(err)
	}

	var result []store.OrgMemberWithUser
	for _, om := range members {
		u, ok := userMap[om.UserID]
		if !ok {
			continue
		}
		result = append(result, store.OrgMemberWithUser{
			OrgMember:   om,
			Username:    u.Username,
			DisplayName: u.DisplayName,
			Email:       u.Email,
		})
	}
	return ptrconv.NonNil(result), nil
}

func (st *orgMemberStore) ListOrgsByUserID(ctx context.Context, userID string) ([]store.Org, error) {
	filter := bson.D{{Key: "user_id", Value: userID}}
	cursor, err := st.s.collection(colOrgMembers).Find(ctx, filter)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = cursor.Close(ctx) }()

	var orgIDs []string
	for cursor.Next(ctx) {
		var m bson.M
		if err := cursor.Decode(&m); err != nil {
			return nil, mapErr(err)
		}
		orgIDs = append(orgIDs, getS(m, "org_id"))
	}
	if err := cursor.Err(); err != nil {
		return nil, mapErr(err)
	}

	if len(orgIDs) == 0 {
		return []store.Org{}, nil
	}

	// Batch-fetch all orgs in a single query, excluding deleted ones.
	orgFilter := bson.D{
		{Key: "_id", Value: bson.D{{Key: "$in", Value: orgIDs}}},
		{Key: "deleted_at", Value: nil},
	}
	orgCursor, err := st.s.collection(colOrgs).Find(ctx, orgFilter)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = orgCursor.Close(ctx) }()

	var orgs []store.Org
	for orgCursor.Next(ctx) {
		var m bson.M
		if err := orgCursor.Decode(&m); err != nil {
			return nil, mapErr(err)
		}
		orgs = append(orgs, docToOrg(m))
	}
	if err := orgCursor.Err(); err != nil {
		return nil, mapErr(err)
	}
	return ptrconv.NonNil(orgs), nil
}

func (st *orgMemberStore) UpdateRole(ctx context.Context, p store.UpdateOrgMemberRoleParams) error {
	filter := bson.D{{Key: "_id", Value: compoundID(p.OrgID, p.UserID)}}
	st.s.trackBeforeUpdate(ctx, colOrgMembers, filter)
	update := bson.D{
		{Key: "$set", Value: bson.D{
			{Key: "role", Value: int32(p.Role)},
		}},
	}
	_, err := st.s.collection(colOrgMembers).UpdateOne(ctx, filter, update)
	return mapErr(err)
}

func (st *orgMemberStore) Delete(ctx context.Context, p store.DeleteOrgMemberParams) error {
	filter := bson.D{{Key: "_id", Value: compoundID(p.OrgID, p.UserID)}}
	st.s.trackBeforeDelete(ctx, colOrgMembers, filter)
	_, err := st.s.collection(colOrgMembers).DeleteOne(ctx, filter)
	return mapErr(err)
}

func (st *orgMemberStore) CountByRole(ctx context.Context, p store.CountOrgMembersByRoleParams) (int64, error) {
	filter := bson.D{
		{Key: "org_id", Value: p.OrgID},
		{Key: "role", Value: int32(p.Role)},
	}
	count, err := st.s.collection(colOrgMembers).CountDocuments(ctx, filter)
	if err != nil {
		return 0, mapErr(err)
	}
	return count, nil
}

func (st *orgMemberStore) IsMember(ctx context.Context, p store.IsOrgMemberParams) (bool, error) {
	filter := bson.D{{Key: "_id", Value: compoundID(p.OrgID, p.UserID)}}
	err := st.s.collection(colOrgMembers).FindOne(ctx, filter, options.FindOne().SetProjection(bson.D{{Key: "_id", Value: 1}})).Err()
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return false, nil
		}
		return false, mapErr(err)
	}
	return true, nil
}
