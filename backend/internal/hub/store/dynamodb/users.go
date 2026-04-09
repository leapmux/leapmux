package dynamodb

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/ptrconv"
)

type userStore struct{ s *dynamoStore }

var _ store.UserStore = (*userStore)(nil)

func (st *userStore) table() string { return st.s.table(tableUsers) }

func userToItem(p store.CreateUserParams, now time.Time) map[string]ddbtypes.AttributeValue {
	return map[string]ddbtypes.AttributeValue{
		attrID:            attrS(p.ID),
		attrOrgID:         attrS(p.OrgID),
		attrUsername:      attrS(store.NormalizeUsername(p.Username)),
		attrPasswordHash:  attrS(p.PasswordHash),
		attrDisplayName:   attrS(p.DisplayName),
		attrEmail:         attrS(store.NormalizeEmail(p.Email)),
		attrEmailVerified: attrBool(p.EmailVerified),
		attrPasswordSet:   attrBool(p.PasswordSet),
		attrIsAdmin:       attrBool(p.IsAdmin),
		attrPrefs:         attrS("{}"),
		attrCreatedAt:     attrS(timeToStr(now)),
		attrUpdatedAt:     attrS(timeToStr(now)),
		attrDeleted:       attrS(deletedFalse),
		// pending_email, pending_email_token, pending_email_expires_at
		// are omitted (not set on create).
	}
}

func itemToUser(item map[string]ddbtypes.AttributeValue) (store.User, error) {
	id, err := mustGetS(item, attrID)
	if err != nil {
		return store.User{}, err
	}
	orgID, err := mustGetS(item, attrOrgID)
	if err != nil {
		return store.User{}, err
	}
	username, err := mustGetS(item, attrUsername)
	if err != nil {
		return store.User{}, err
	}
	passwordHash, err := mustGetS(item, attrPasswordHash)
	if err != nil {
		return store.User{}, err
	}
	displayName, err := mustGetS(item, attrDisplayName)
	if err != nil {
		return store.User{}, err
	}
	email, err := mustGetS(item, attrEmail)
	if err != nil {
		return store.User{}, err
	}
	emailVerified, err := mustGetBool(item, attrEmailVerified)
	if err != nil {
		return store.User{}, err
	}
	passwordSet, err := mustGetBool(item, attrPasswordSet)
	if err != nil {
		return store.User{}, err
	}
	isAdmin, err := mustGetBool(item, attrIsAdmin)
	if err != nil {
		return store.User{}, err
	}
	prefs, err := mustGetS(item, attrPrefs)
	if err != nil {
		return store.User{}, err
	}
	createdAt, err := mustGetTime(item, attrCreatedAt)
	if err != nil {
		return store.User{}, err
	}
	updatedAt, err := mustGetTime(item, attrUpdatedAt)
	if err != nil {
		return store.User{}, err
	}
	return store.User{
		ID:                    id,
		OrgID:                 orgID,
		Username:              username,
		PasswordHash:          passwordHash,
		DisplayName:           displayName,
		Email:                 email,
		EmailVerified:         emailVerified,
		PendingEmail:          getS(item, attrPendingEmail),
		PendingEmailToken:     getS(item, attrPendingEmailToken),
		PendingEmailExpiresAt: getTimePtr(item, attrPendingEmailExpiresAt),
		PasswordSet:           passwordSet,
		IsAdmin:               isAdmin,
		Prefs:                 prefs,
		CreatedAt:             createdAt,
		UpdatedAt:             updatedAt,
		DeletedAt:             getTimePtr(item, attrDeletedAt),
	}, nil
}

func (st *userStore) Create(ctx context.Context, p store.CreateUserParams) error {
	now := time.Now().UTC()
	item := userToItem(p, now)
	constraintTable := st.s.table(tableUniqueConstraints)

	// userToItem already normalizes username and email.
	username := getS(item, attrUsername)
	email := getS(item, attrEmail)

	txItems := []ddbtypes.TransactWriteItem{
		{
			Put: &ddbtypes.Put{
				TableName:           aws.String(st.table()),
				Item:                item,
				ConditionExpression: aws.String("attribute_not_exists(id)"),
			},
		},
		putConstraint(constraintTable, "user", "username", username),
	}
	if email != "" {
		txItems = append(txItems, putConstraint(constraintTable, "user", "email", email))
	}

	_, err := st.s.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		TransactItems: txItems,
	})
	if err != nil {
		return mapErr(err)
	}
	if t := st.s.txTracker; t != nil {
		t.recordPut(st.table(), map[string]ddbtypes.AttributeValue{attrID: item[attrID]}, nil)
		t.recordPut(constraintTable, map[string]ddbtypes.AttributeValue{
			attrConstraintValue: attrS(constraintKey("user", "username", username)),
		}, nil)
		if email != "" {
			t.recordPut(constraintTable, map[string]ddbtypes.AttributeValue{
				attrConstraintValue: attrS(constraintKey("user", "email", email)),
			}, nil)
		}
	}
	return nil
}

func (st *userStore) GetByID(ctx context.Context, id string) (*store.User, error) {
	u, err := st.GetByIDIncludeDeleted(ctx, id)
	if err != nil {
		return nil, err
	}
	if u.DeletedAt != nil {
		return nil, store.ErrNotFound
	}
	return u, nil
}

func (st *userStore) GetByIDIncludeDeleted(ctx context.Context, id string) (*store.User, error) {
	out, err := st.s.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(st.table()),
		Key:       map[string]ddbtypes.AttributeValue{attrID: attrS(id)},
	})
	if err != nil {
		return nil, mapErr(err)
	}
	if out.Item == nil {
		return nil, store.ErrNotFound
	}
	u, err := itemToUser(out.Item)
	if err != nil {
		return nil, err
	}
	return &u, nil
}

func (st *userStore) GetByUsername(ctx context.Context, username string) (*store.User, error) {
	return st.getByGSI(ctx, gsiUsername, attrUsername, store.NormalizeUsername(username))
}

func (st *userStore) GetByEmail(ctx context.Context, email string) (*store.User, error) {
	return st.getByGSI(ctx, gsiEmail, attrEmail, store.NormalizeEmail(email))
}

func (st *userStore) GetByPendingEmailToken(ctx context.Context, token string) (*store.User, error) {
	return st.getByGSI(ctx, gsiPendingEmailToken, attrPendingEmailToken, token)
}

func (st *userStore) getByGSI(ctx context.Context, indexName, keyName, keyValue string) (*store.User, error) {
	out, err := st.s.client.Query(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(st.table()),
		IndexName:              aws.String(indexName),
		KeyConditionExpression: aws.String("#k = :v"),
		FilterExpression:       aws.String("attribute_not_exists(deleted_at)"),
		ExpressionAttributeNames: map[string]string{
			"#k": keyName,
		},
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":v": attrS(keyValue),
		},
	})
	if err != nil {
		return nil, mapErr(err)
	}
	if len(out.Items) == 0 {
		return nil, store.ErrNotFound
	}
	u, err := itemToUser(out.Items[0])
	if err != nil {
		return nil, err
	}
	return &u, nil
}

func (st *userStore) GetPrefs(ctx context.Context, id string) (string, error) {
	out, err := st.s.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:            aws.String(st.table()),
		Key:                  map[string]ddbtypes.AttributeValue{attrID: attrS(id)},
		ProjectionExpression: aws.String(attrPrefs + ", " + attrDeletedAt),
	})
	if err != nil {
		return "", mapErr(err)
	}
	if out.Item == nil {
		return "", store.ErrNotFound
	}
	if getTimePtr(out.Item, attrDeletedAt) != nil {
		return "", store.ErrNotFound
	}
	return getS(out.Item, attrPrefs), nil
}

func (st *userStore) HasAny(ctx context.Context) (bool, error) {
	out, err := st.s.client.Query(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(st.table()),
		IndexName:              aws.String(gsiDeletedCreatedAt),
		KeyConditionExpression: aws.String("deleted = :del"),
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":del": attrS(deletedFalse),
		},
		Limit:  aws.Int32(1),
		Select: ddbtypes.SelectCount,
	})
	if err != nil {
		return false, mapErr(err)
	}
	return out.Count > 0, nil
}

func (st *userStore) Count(ctx context.Context) (int64, error) {
	var count int64
	var lastKey map[string]ddbtypes.AttributeValue
	for {
		out, err := st.s.client.Query(ctx, &dynamodb.QueryInput{
			TableName:              aws.String(st.table()),
			IndexName:              aws.String(gsiDeletedCreatedAt),
			KeyConditionExpression: aws.String("deleted = :del"),
			ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
				":del": attrS(deletedFalse),
			},
			Select:            ddbtypes.SelectCount,
			ExclusiveStartKey: lastKey,
		})
		if err != nil {
			return 0, mapErr(err)
		}
		count += int64(out.Count)
		if out.LastEvaluatedKey == nil {
			break
		}
		lastKey = out.LastEvaluatedKey
	}
	return count, nil
}

func (st *userStore) ListByOrgID(ctx context.Context, orgID string) ([]store.User, error) {
	var users []store.User
	err := st.s.queryPages(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(st.table()),
		IndexName:              aws.String(gsiOrgID),
		KeyConditionExpression: aws.String("org_id = :orgID"),
		FilterExpression:       aws.String("attribute_not_exists(deleted_at)"),
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":orgID": attrS(orgID),
		},
	}, func(item map[string]ddbtypes.AttributeValue) bool {
		u, err := itemToUser(item)
		if err != nil {
			return false
		}
		users = append(users, u)
		return true
	})
	if err != nil {
		return nil, err
	}
	return ptrconv.NonNil(users), nil
}

func (st *userStore) ListAll(ctx context.Context, p store.ListAllUsersParams) ([]store.User, error) {
	keyExpr, exprValues, err := buildNotDeletedCursorExpr(p.Cursor)
	if err != nil {
		return nil, err
	}

	input := &dynamodb.QueryInput{
		TableName:                 aws.String(st.table()),
		IndexName:                 aws.String(gsiDeletedCreatedAt),
		KeyConditionExpression:    aws.String(keyExpr),
		ExpressionAttributeValues: exprValues,
		ScanIndexForward:          aws.Bool(false),
	}
	if p.Limit > 0 {
		input.Limit = aws.Int32(int32(p.Limit))
	}

	var all []store.User
	err = st.s.queryPages(ctx, input, func(item map[string]ddbtypes.AttributeValue) bool {
		u, err := itemToUser(item)
		if err != nil {
			return false
		}
		all = append(all, u)
		return p.Limit <= 0 || int64(len(all)) < p.Limit
	})
	if err != nil {
		return nil, err
	}

	return ptrconv.NonNil(all), nil
}

func (st *userStore) Search(ctx context.Context, p store.SearchUsersParams) ([]store.User, error) {
	keyExpr, exprValues, err := buildNotDeletedCursorExpr(p.Cursor)
	if err != nil {
		return nil, err
	}

	var q string
	if p.Query != nil && *p.Query != "" {
		q = strings.ToLower(*p.Query)
	}

	var all []store.User
	var examined int
	err = st.s.queryPages(ctx, &dynamodb.QueryInput{
		TableName:                 aws.String(st.table()),
		IndexName:                 aws.String(gsiDeletedCreatedAt),
		KeyConditionExpression:    aws.String(keyExpr),
		ExpressionAttributeValues: exprValues,
		ScanIndexForward:          aws.Bool(false),
		Limit:                     aws.Int32(int32(store.SearchPageSize)),
	}, func(item map[string]ddbtypes.AttributeValue) bool {
		examined++
		if q != "" && !prefixMatchUserItem(item, q) {
			return examined < store.SearchMaxExamine
		}
		u, err := itemToUser(item)
		if err != nil {
			return false
		}
		all = append(all, u)
		return (p.Limit <= 0 || int64(len(all)) < p.Limit) && examined < store.SearchMaxExamine
	})
	if err != nil {
		return nil, err
	}

	return ptrconv.NonNil(all), nil
}

// prefixMatchUserItem checks whether a raw DynamoDB item matches the search
// query by reading only the search-relevant fields (username, display_name,
// email) directly from the attribute map, avoiding the cost of full
// itemToUser deserialization for non-matching items.
func prefixMatchUserItem(item map[string]ddbtypes.AttributeValue, loweredQuery string) bool {
	return strings.HasPrefix(strings.ToLower(getS(item, attrUsername)), loweredQuery) ||
		strings.HasPrefix(strings.ToLower(getS(item, attrDisplayName)), loweredQuery) ||
		strings.HasPrefix(strings.ToLower(getS(item, attrEmail)), loweredQuery)
}

func (st *userStore) UpdateProfile(ctx context.Context, p store.UpdateUserProfileParams) error {
	// Read current user to get the old username for constraint swap.
	user, err := st.GetByID(ctx, p.ID)
	if err != nil {
		return err
	}

	username := store.NormalizeUsername(p.Username)
	now := timeToStr(time.Now().UTC())
	constraintTable := st.s.table(tableUniqueConstraints)

	txItems := []ddbtypes.TransactWriteItem{
		{
			Update: &ddbtypes.Update{
				TableName:           aws.String(st.table()),
				Key:                 map[string]ddbtypes.AttributeValue{attrID: attrS(p.ID)},
				UpdateExpression:    aws.String("SET username = :u, display_name = :d, updated_at = :now"),
				ConditionExpression: aws.String("attribute_exists(id) AND attribute_not_exists(deleted_at)"),
				ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
					":u":   attrS(username),
					":d":   attrS(p.DisplayName),
					":now": attrS(now),
				},
			},
		},
	}
	if user.Username != username {
		txItems = append(txItems,
			deleteConstraint(constraintTable, "user", "username", user.Username),
			putConstraint(constraintTable, "user", "username", username),
		)
	}

	_, err = st.s.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		TransactItems: txItems,
	})
	return mapErr(err)
}

func (st *userStore) UpdatePassword(ctx context.Context, p store.UpdateUserPasswordParams) error {
	now := timeToStr(time.Now().UTC())
	_, err := st.s.updateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:           aws.String(st.table()),
		Key:                 map[string]ddbtypes.AttributeValue{attrID: attrS(p.ID)},
		UpdateExpression:    aws.String("SET password_hash = :h, password_set = :t, updated_at = :now"),
		ConditionExpression: aws.String("attribute_exists(id) AND attribute_not_exists(deleted_at)"),
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":h":   attrS(p.PasswordHash),
			":t":   attrBool(true),
			":now": attrS(now),
		},
	})
	return mapErr(err)
}

func (st *userStore) UpdateEmail(ctx context.Context, p store.UpdateUserEmailParams) error {
	// Read current user to get the old email for constraint swap.
	user, err := st.GetByID(ctx, p.ID)
	if err != nil {
		return err
	}

	email := store.NormalizeEmail(p.Email)
	now := timeToStr(time.Now().UTC())
	constraintTable := st.s.table(tableUniqueConstraints)

	txItems := []ddbtypes.TransactWriteItem{
		{
			Update: &ddbtypes.Update{
				TableName:           aws.String(st.table()),
				Key:                 map[string]ddbtypes.AttributeValue{attrID: attrS(p.ID)},
				UpdateExpression:    aws.String("SET email = :e, email_verified = :v, updated_at = :now"),
				ConditionExpression: aws.String("attribute_exists(id) AND attribute_not_exists(deleted_at)"),
				ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
					":e":   attrS(email),
					":v":   attrBool(p.EmailVerified),
					":now": attrS(now),
				},
			},
		},
	}
	if user.Email != email {
		if user.Email != "" {
			txItems = append(txItems, deleteConstraint(constraintTable, "user", "email", user.Email))
		}
		if email != "" {
			txItems = append(txItems, putConstraint(constraintTable, "user", "email", email))
		}
	}

	_, err = st.s.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		TransactItems: txItems,
	})
	return mapErr(err)
}

func (st *userStore) UpdateEmailVerified(ctx context.Context, p store.UpdateUserEmailVerifiedParams) error {
	now := timeToStr(time.Now().UTC())
	_, err := st.s.updateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:           aws.String(st.table()),
		Key:                 map[string]ddbtypes.AttributeValue{attrID: attrS(p.ID)},
		UpdateExpression:    aws.String("SET email_verified = :v, updated_at = :now"),
		ConditionExpression: aws.String("attribute_exists(id) AND attribute_not_exists(deleted_at)"),
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":v":   attrBool(p.EmailVerified),
			":now": attrS(now),
		},
	})
	return mapErr(err)
}

func (st *userStore) UpdateAdmin(ctx context.Context, p store.UpdateUserAdminParams) error {
	now := timeToStr(time.Now().UTC())
	_, err := st.s.updateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:           aws.String(st.table()),
		Key:                 map[string]ddbtypes.AttributeValue{attrID: attrS(p.ID)},
		UpdateExpression:    aws.String("SET is_admin = :a, updated_at = :now"),
		ConditionExpression: aws.String("attribute_exists(id) AND attribute_not_exists(deleted_at)"),
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":a":   attrBool(p.IsAdmin),
			":now": attrS(now),
		},
	})
	return mapErr(err)
}

func (st *userStore) UpdatePrefs(ctx context.Context, p store.UpdateUserPrefsParams) error {
	now := timeToStr(time.Now().UTC())
	_, err := st.s.updateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:           aws.String(st.table()),
		Key:                 map[string]ddbtypes.AttributeValue{attrID: attrS(p.ID)},
		UpdateExpression:    aws.String("SET prefs = :p, updated_at = :now"),
		ConditionExpression: aws.String("attribute_exists(id) AND attribute_not_exists(deleted_at)"),
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":p":   attrS(p.Prefs),
			":now": attrS(now),
		},
	})
	return mapErr(err)
}

func (st *userStore) SetPendingEmail(ctx context.Context, p store.SetPendingEmailParams) error {
	now := timeToStr(time.Now().UTC())
	_, err := st.s.updateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:           aws.String(st.table()),
		Key:                 map[string]ddbtypes.AttributeValue{attrID: attrS(p.ID)},
		UpdateExpression:    aws.String("SET pending_email = :pe, pending_email_token = :pt, pending_email_expires_at = :exp, updated_at = :now"),
		ConditionExpression: aws.String("attribute_exists(id) AND attribute_not_exists(deleted_at)"),
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":pe":  attrS(store.NormalizeEmail(p.PendingEmail)),
			":pt":  attrS(p.PendingEmailToken),
			":exp": attrS(timePtrToStr(p.PendingEmailExpiresAt)),
			":now": attrS(now),
		},
	})
	return mapErr(err)
}

func (st *userStore) PromotePendingEmail(ctx context.Context, id string) error {
	// First, get the user to read pending_email and current email.
	u, err := st.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if u.PendingEmail == "" {
		return nil
	}

	now := timeToStr(time.Now().UTC())
	constraintTable := st.s.table(tableUniqueConstraints)

	txItems := []ddbtypes.TransactWriteItem{
		{
			Update: &ddbtypes.Update{
				TableName:           aws.String(st.table()),
				Key:                 map[string]ddbtypes.AttributeValue{attrID: attrS(id)},
				UpdateExpression:    aws.String("SET email = pending_email, email_verified = :t, updated_at = :now REMOVE pending_email, pending_email_token, pending_email_expires_at"),
				ConditionExpression: aws.String("attribute_exists(id) AND attribute_not_exists(deleted_at)"),
				ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
					":t":   attrBool(true),
					":now": attrS(now),
				},
			},
		},
	}
	if u.Email != u.PendingEmail {
		if u.Email != "" {
			txItems = append(txItems, deleteConstraint(constraintTable, "user", "email", u.Email))
		}
		txItems = append(txItems, putConstraint(constraintTable, "user", "email", u.PendingEmail))
	}

	_, err = st.s.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		TransactItems: txItems,
	})
	return mapErr(err)
}

func (st *userStore) ClearPendingEmail(ctx context.Context, id string) error {
	now := timeToStr(time.Now().UTC())
	_, err := st.s.updateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:           aws.String(st.table()),
		Key:                 map[string]ddbtypes.AttributeValue{attrID: attrS(id)},
		UpdateExpression:    aws.String("SET updated_at = :now REMOVE pending_email, pending_email_token, pending_email_expires_at"),
		ConditionExpression: aws.String("attribute_exists(id) AND attribute_not_exists(deleted_at)"),
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":now": attrS(now),
		},
	})
	return mapErr(err)
}

func (st *userStore) ClearCompetingPendingEmails(ctx context.Context, p store.ClearCompetingPendingEmailsParams) error {
	// Query GSI for users with matching pending_email, excluding the given user ID.
	var clearErr error
	err := st.s.queryPages(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(st.table()),
		IndexName:              aws.String(gsiPendingEmail),
		KeyConditionExpression: aws.String("pending_email = :pe"),
		FilterExpression:       aws.String("id <> :excludeID AND attribute_not_exists(deleted_at)"),
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":pe":        attrS(store.NormalizeEmail(p.PendingEmail)),
			":excludeID": attrS(p.ExcludeID),
		},
	}, func(item map[string]ddbtypes.AttributeValue) bool {
		userID := getS(item, attrID)
		if clearErr = st.ClearPendingEmail(ctx, userID); clearErr != nil {
			return false
		}
		return true
	})
	if err != nil {
		return err
	}
	return clearErr
}

func (st *userStore) Delete(ctx context.Context, id string) error {
	// Get the user to find username/email for constraint cleanup.
	user, err := st.GetByID(ctx, id)
	if err != nil {
		// Already deleted — treat as no-op.
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return err
	}

	now := timeToStr(time.Now().UTC())
	constraintTable := st.s.table(tableUniqueConstraints)

	txItems := []ddbtypes.TransactWriteItem{
		{
			Update: &ddbtypes.Update{
				TableName:           aws.String(st.table()),
				Key:                 map[string]ddbtypes.AttributeValue{attrID: attrS(id)},
				UpdateExpression:    aws.String("SET deleted_at = :now, updated_at = :now, deleted = :del"),
				ConditionExpression: aws.String("attribute_exists(id) AND attribute_not_exists(deleted_at)"),
				ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
					":now": attrS(now),
					":del": attrS(deletedTrue),
				},
			},
		},
		deleteConstraint(constraintTable, "user", "username", user.Username),
	}
	if user.Email != "" {
		txItems = append(txItems, deleteConstraint(constraintTable, "user", "email", user.Email))
	}

	_, err = st.s.client.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{
		TransactItems: txItems,
	})
	return mapErr(err)
}
