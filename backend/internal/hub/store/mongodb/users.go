package mongodb

import (
	"context"
	"regexp"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/ptrconv"
)

func userToDoc(p store.CreateUserParams, now time.Time) bson.D {
	return bson.D{
		{Key: "_id", Value: p.ID},
		{Key: "org_id", Value: p.OrgID},
		{Key: "username", Value: store.NormalizeUsername(p.Username)},
		{Key: "password_hash", Value: p.PasswordHash},
		{Key: "display_name", Value: p.DisplayName},
		{Key: "email", Value: store.NormalizeEmail(p.Email)},
		{Key: "email_verified", Value: p.EmailVerified},
		{Key: "password_set", Value: p.PasswordSet},
		{Key: "is_admin", Value: p.IsAdmin},
		{Key: "prefs", Value: "{}"},
		{Key: "created_at", Value: now},
		{Key: "updated_at", Value: now},
		{Key: "deleted_at", Value: nil},
	}
}

func docToUser(m bson.M) store.User {
	return store.User{
		ID:                    getS(m, "_id"),
		OrgID:                 getS(m, "org_id"),
		Username:              getS(m, "username"),
		PasswordHash:          getS(m, "password_hash"),
		DisplayName:           getS(m, "display_name"),
		Email:                 getS(m, "email"),
		EmailVerified:         getBool(m, "email_verified"),
		PendingEmail:          getS(m, "pending_email"),
		PendingEmailToken:     getS(m, "pending_email_token"),
		PendingEmailExpiresAt: getTimePtr(m, "pending_email_expires_at"),
		PasswordSet:           getBool(m, "password_set"),
		IsAdmin:               getBool(m, "is_admin"),
		Prefs:                 getS(m, "prefs"),
		CreatedAt:             getTime(m, "created_at"),
		UpdatedAt:             getTime(m, "updated_at"),
		DeletedAt:             getTimePtr(m, "deleted_at"),
	}
}

func (st *userStore) Create(ctx context.Context, p store.CreateUserParams) error {
	now := truncateMS(time.Now().UTC())
	doc := userToDoc(p, now)
	_, err := st.s.collection(colUsers).InsertOne(ctx, doc)
	if err != nil {
		return mapErr(err)
	}
	st.s.trackInsert(colUsers, p.ID)
	return nil
}

func (st *userStore) GetByID(ctx context.Context, id string) (*store.User, error) {
	filter := bson.D{
		{Key: "_id", Value: id},
		{Key: "deleted_at", Value: nil},
	}
	var m bson.M
	err := st.s.collection(colUsers).FindOne(ctx, filter).Decode(&m)
	if err != nil {
		return nil, mapErr(err)
	}
	u := docToUser(m)
	return &u, nil
}

func (st *userStore) GetByIDIncludeDeleted(ctx context.Context, id string) (*store.User, error) {
	filter := bson.D{{Key: "_id", Value: id}}
	var m bson.M
	err := st.s.collection(colUsers).FindOne(ctx, filter).Decode(&m)
	if err != nil {
		return nil, mapErr(err)
	}
	u := docToUser(m)
	return &u, nil
}

func (st *userStore) GetByUsername(ctx context.Context, username string) (*store.User, error) {
	filter := bson.D{
		{Key: "username", Value: store.NormalizeUsername(username)},
		{Key: "deleted_at", Value: nil},
	}
	var m bson.M
	err := st.s.collection(colUsers).FindOne(ctx, filter).Decode(&m)
	if err != nil {
		return nil, mapErr(err)
	}
	u := docToUser(m)
	return &u, nil
}

func (st *userStore) GetByEmail(ctx context.Context, email string) (*store.User, error) {
	if email == "" {
		return nil, store.ErrNotFound
	}
	filter := bson.D{
		{Key: "email", Value: store.NormalizeEmail(email)},
		{Key: "deleted_at", Value: nil},
	}
	var m bson.M
	err := st.s.collection(colUsers).FindOne(ctx, filter).Decode(&m)
	if err != nil {
		return nil, mapErr(err)
	}
	u := docToUser(m)
	return &u, nil
}

func (st *userStore) GetByPendingEmailToken(ctx context.Context, token string) (*store.User, error) {
	if token == "" {
		return nil, store.ErrNotFound
	}
	filter := bson.D{
		{Key: "pending_email_token", Value: token},
		{Key: "deleted_at", Value: nil},
	}
	var m bson.M
	err := st.s.collection(colUsers).FindOne(ctx, filter).Decode(&m)
	if err != nil {
		return nil, mapErr(err)
	}
	u := docToUser(m)
	return &u, nil
}

func (st *userStore) GetPrefs(ctx context.Context, id string) (string, error) {
	filter := bson.D{
		{Key: "_id", Value: id},
		{Key: "deleted_at", Value: nil},
	}
	var m bson.M
	err := st.s.collection(colUsers).FindOne(ctx, filter, options.FindOne().SetProjection(bson.D{
		{Key: "prefs", Value: 1},
	})).Decode(&m)
	if err != nil {
		return "", mapErr(err)
	}
	return getS(m, "prefs"), nil
}

func (st *userStore) HasAny(ctx context.Context) (bool, error) {
	count, err := st.s.collection(colUsers).CountDocuments(ctx, notDeleted(),
		options.Count().SetLimit(1))
	if err != nil {
		return false, mapErr(err)
	}
	return count > 0, nil
}

func (st *userStore) Count(ctx context.Context) (int64, error) {
	count, err := st.s.collection(colUsers).CountDocuments(ctx, notDeleted())
	if err != nil {
		return 0, mapErr(err)
	}
	return count, nil
}

func (st *userStore) ListByOrgID(ctx context.Context, orgID string) ([]store.User, error) {
	filter := bson.D{
		{Key: "org_id", Value: orgID},
		{Key: "deleted_at", Value: nil},
	}
	cursor, err := st.s.collection(colUsers).Find(ctx, filter)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = cursor.Close(ctx) }()

	var users []store.User
	for cursor.Next(ctx) {
		var m bson.M
		if err := cursor.Decode(&m); err != nil {
			return nil, mapErr(err)
		}
		users = append(users, docToUser(m))
	}
	return ptrconv.NonNil(users), mapErr(cursor.Err())
}

func (st *userStore) ListAll(ctx context.Context, p store.ListAllUsersParams) ([]store.User, error) {
	filter := notDeleted()
	if p.Cursor != "" {
		cursorTime, _, err := store.ParseCursorTime(p.Cursor)
		if err != nil {
			return nil, err
		}
		filter = append(filter, bson.E{Key: "created_at", Value: bson.D{{Key: "$lt", Value: cursorTime}}})
	}
	opts := options.Find().
		SetSort(bson.D{{Key: "created_at", Value: -1}}).
		SetLimit(p.Limit)

	cursor, err := st.s.collection(colUsers).Find(ctx, filter, opts)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = cursor.Close(ctx) }()

	var users []store.User
	for cursor.Next(ctx) {
		var m bson.M
		if err := cursor.Decode(&m); err != nil {
			return nil, mapErr(err)
		}
		users = append(users, docToUser(m))
	}
	return ptrconv.NonNil(users), mapErr(cursor.Err())
}

func (st *userStore) Search(ctx context.Context, p store.SearchUsersParams) ([]store.User, error) {
	filter := bson.D{{Key: "deleted_at", Value: nil}}

	if p.Query != nil && *p.Query != "" {
		regex := bson.D{{Key: "$regex", Value: "^" + regexp.QuoteMeta(*p.Query)}, {Key: "$options", Value: "i"}}
		filter = append(filter, bson.E{Key: "$or", Value: bson.A{
			bson.D{{Key: "username", Value: regex}},
			bson.D{{Key: "display_name", Value: regex}},
			bson.D{{Key: "email", Value: regex}},
		}})
	}

	if p.Cursor != "" {
		cursorTime, _, err := store.ParseCursorTime(p.Cursor)
		if err != nil {
			return nil, err
		}
		filter = append(filter, bson.E{Key: "created_at", Value: bson.D{{Key: "$lt", Value: cursorTime}}})
	}

	opts := options.Find().
		SetSort(bson.D{{Key: "created_at", Value: -1}}).
		SetLimit(p.Limit)

	cursor, err := st.s.collection(colUsers).Find(ctx, filter, opts)
	if err != nil {
		return nil, mapErr(err)
	}
	defer func() { _ = cursor.Close(ctx) }()

	var users []store.User
	for cursor.Next(ctx) {
		var m bson.M
		if err := cursor.Decode(&m); err != nil {
			return nil, mapErr(err)
		}
		users = append(users, docToUser(m))
	}
	return ptrconv.NonNil(users), mapErr(cursor.Err())
}

func (st *userStore) UpdateProfile(ctx context.Context, p store.UpdateUserProfileParams) error {
	now := truncateMS(time.Now().UTC())
	filter := bson.D{
		{Key: "_id", Value: p.ID},
		{Key: "deleted_at", Value: nil},
	}
	st.s.trackBeforeUpdate(ctx, colUsers, filter)
	update := bson.D{
		{Key: "$set", Value: bson.D{
			{Key: "username", Value: store.NormalizeUsername(p.Username)},
			{Key: "display_name", Value: p.DisplayName},
			{Key: "updated_at", Value: now},
		}},
	}
	_, err := st.s.collection(colUsers).UpdateOne(ctx, filter, update)
	return mapErr(err)
}

func (st *userStore) UpdatePassword(ctx context.Context, p store.UpdateUserPasswordParams) error {
	now := truncateMS(time.Now().UTC())
	filter := bson.D{
		{Key: "_id", Value: p.ID},
		{Key: "deleted_at", Value: nil},
	}
	st.s.trackBeforeUpdate(ctx, colUsers, filter)
	update := bson.D{
		{Key: "$set", Value: bson.D{
			{Key: "password_hash", Value: p.PasswordHash},
			{Key: "password_set", Value: true},
			{Key: "updated_at", Value: now},
		}},
	}
	_, err := st.s.collection(colUsers).UpdateOne(ctx, filter, update)
	return mapErr(err)
}

func (st *userStore) UpdateEmail(ctx context.Context, p store.UpdateUserEmailParams) error {
	now := truncateMS(time.Now().UTC())
	filter := bson.D{
		{Key: "_id", Value: p.ID},
		{Key: "deleted_at", Value: nil},
	}
	st.s.trackBeforeUpdate(ctx, colUsers, filter)
	update := bson.D{
		{Key: "$set", Value: bson.D{
			{Key: "email", Value: store.NormalizeEmail(p.Email)},
			{Key: "email_verified", Value: p.EmailVerified},
			{Key: "updated_at", Value: now},
		}},
	}
	_, err := st.s.collection(colUsers).UpdateOne(ctx, filter, update)
	return mapErr(err)
}

func (st *userStore) UpdateEmailVerified(ctx context.Context, p store.UpdateUserEmailVerifiedParams) error {
	now := truncateMS(time.Now().UTC())
	filter := bson.D{
		{Key: "_id", Value: p.ID},
		{Key: "deleted_at", Value: nil},
	}
	st.s.trackBeforeUpdate(ctx, colUsers, filter)
	update := bson.D{
		{Key: "$set", Value: bson.D{
			{Key: "email_verified", Value: p.EmailVerified},
			{Key: "updated_at", Value: now},
		}},
	}
	_, err := st.s.collection(colUsers).UpdateOne(ctx, filter, update)
	return mapErr(err)
}

func (st *userStore) UpdateAdmin(ctx context.Context, p store.UpdateUserAdminParams) error {
	now := truncateMS(time.Now().UTC())
	filter := bson.D{
		{Key: "_id", Value: p.ID},
		{Key: "deleted_at", Value: nil},
	}
	st.s.trackBeforeUpdate(ctx, colUsers, filter)
	update := bson.D{
		{Key: "$set", Value: bson.D{
			{Key: "is_admin", Value: p.IsAdmin},
			{Key: "updated_at", Value: now},
		}},
	}
	_, err := st.s.collection(colUsers).UpdateOne(ctx, filter, update)
	return mapErr(err)
}

func (st *userStore) UpdatePrefs(ctx context.Context, p store.UpdateUserPrefsParams) error {
	now := truncateMS(time.Now().UTC())
	filter := bson.D{
		{Key: "_id", Value: p.ID},
		{Key: "deleted_at", Value: nil},
	}
	st.s.trackBeforeUpdate(ctx, colUsers, filter)
	update := bson.D{
		{Key: "$set", Value: bson.D{
			{Key: "prefs", Value: p.Prefs},
			{Key: "updated_at", Value: now},
		}},
	}
	_, err := st.s.collection(colUsers).UpdateOne(ctx, filter, update)
	return mapErr(err)
}

func (st *userStore) SetPendingEmail(ctx context.Context, p store.SetPendingEmailParams) error {
	now := truncateMS(time.Now().UTC())
	filter := bson.D{
		{Key: "_id", Value: p.ID},
		{Key: "deleted_at", Value: nil},
	}
	st.s.trackBeforeUpdate(ctx, colUsers, filter)
	update := bson.D{
		{Key: "$set", Value: bson.D{
			{Key: "pending_email", Value: store.NormalizeEmail(p.PendingEmail)},
			{Key: "pending_email_token", Value: p.PendingEmailToken},
			{Key: "pending_email_expires_at", Value: timePtrVal(p.PendingEmailExpiresAt)},
			{Key: "updated_at", Value: now},
		}},
	}
	_, err := st.s.collection(colUsers).UpdateOne(ctx, filter, update)
	return mapErr(err)
}

func (st *userStore) PromotePendingEmail(ctx context.Context, id string) error {
	u, err := st.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if u.PendingEmail == "" {
		return nil
	}

	now := truncateMS(time.Now().UTC())
	filter := bson.D{
		{Key: "_id", Value: id},
		{Key: "deleted_at", Value: nil},
	}
	st.s.trackBeforeUpdate(ctx, colUsers, filter)
	update := bson.D{
		{Key: "$set", Value: bson.D{
			{Key: "email", Value: u.PendingEmail},
			{Key: "email_verified", Value: true},
			{Key: "updated_at", Value: now},
		}},
		{Key: "$unset", Value: bson.D{
			{Key: "pending_email", Value: ""},
			{Key: "pending_email_token", Value: ""},
			{Key: "pending_email_expires_at", Value: ""},
		}},
	}
	_, err = st.s.collection(colUsers).UpdateOne(ctx, filter, update)
	return mapErr(err)
}

func (st *userStore) ClearPendingEmail(ctx context.Context, id string) error {
	now := truncateMS(time.Now().UTC())
	filter := bson.D{
		{Key: "_id", Value: id},
		{Key: "deleted_at", Value: nil},
	}
	st.s.trackBeforeUpdate(ctx, colUsers, filter)
	update := bson.D{
		{Key: "$set", Value: bson.D{
			{Key: "updated_at", Value: now},
		}},
		{Key: "$unset", Value: bson.D{
			{Key: "pending_email", Value: ""},
			{Key: "pending_email_token", Value: ""},
			{Key: "pending_email_expires_at", Value: ""},
		}},
	}
	_, err := st.s.collection(colUsers).UpdateOne(ctx, filter, update)
	return mapErr(err)
}

func (st *userStore) ClearCompetingPendingEmails(ctx context.Context, p store.ClearCompetingPendingEmailsParams) error {
	now := truncateMS(time.Now().UTC())
	filter := bson.D{
		{Key: "pending_email", Value: store.NormalizeEmail(p.PendingEmail)},
		{Key: "_id", Value: bson.D{{Key: "$ne", Value: p.ExcludeID}}},
		{Key: "deleted_at", Value: nil},
	}
	update := bson.D{{Key: "$set", Value: bson.D{
		{Key: "pending_email", Value: ""},
		{Key: "pending_email_token", Value: ""},
		{Key: "pending_email_expires_at", Value: nil},
		{Key: "updated_at", Value: now},
	}}}
	_, err := st.s.collection(colUsers).UpdateMany(ctx, filter, update)
	return mapErr(err)
}

func (st *userStore) Delete(ctx context.Context, id string) error {
	now := truncateMS(time.Now().UTC())
	filter := bson.D{
		{Key: "_id", Value: id},
		{Key: "deleted_at", Value: nil},
	}
	st.s.trackBeforeUpdate(ctx, colUsers, filter)
	update := bson.D{
		{Key: "$set", Value: bson.D{
			{Key: "deleted_at", Value: now},
			{Key: "updated_at", Value: now},
		}},
	}
	_, err := st.s.collection(colUsers).UpdateOne(ctx, filter, update)
	return mapErr(err)
}
