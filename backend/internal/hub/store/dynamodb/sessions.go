package dynamodb

import (
	"context"
	"errors"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/ptrconv"
)

type sessionStore struct{ s *dynamoStore }

var _ store.SessionStore = (*sessionStore)(nil)

func (st *sessionStore) table() string { return st.s.table(tableSessions) }

// sessionTTLBuffer is the minimum time a session item survives before
// DynamoDB TTL may delete it. This ensures the cleanup process has a
// window to discover and count expired sessions before TTL kicks in.
// Without this buffer, DynamoDB Local (which enforces TTL aggressively)
// can delete items before cleanup runs.
const sessionTTLBuffer = 10 * time.Minute

func sessionToItem(p store.CreateSessionParams, now time.Time) map[string]ddbtypes.AttributeValue {
	ttl := p.ExpiresAt.Unix()
	if minTTL := now.Add(sessionTTLBuffer).Unix(); ttl < minTTL {
		ttl = minTTL
	}
	return map[string]ddbtypes.AttributeValue{
		attrID:           attrS(p.ID),
		attrUserID:       attrS(p.UserID),
		attrExpiresAt:    attrS(timeToStr(p.ExpiresAt)),
		attrCreatedAt:    attrS(timeToStr(now)),
		attrLastActiveAt: attrS(timeToStr(now)),
		attrUserAgent:    attrS(p.UserAgent),
		attrIPAddress:    attrS(p.IPAddress),
		attrNotExpired:   attrS(sentinelActive),
		attrTTL:          attrN(ttl),
	}
}

func itemToSession(item map[string]ddbtypes.AttributeValue) (store.UserSession, error) {
	id, err := mustGetS(item, attrID)
	if err != nil {
		return store.UserSession{}, err
	}
	userID, err := mustGetS(item, attrUserID)
	if err != nil {
		return store.UserSession{}, err
	}
	expiresAt, err := mustGetTime(item, attrExpiresAt)
	if err != nil {
		return store.UserSession{}, err
	}
	createdAt, err := mustGetTime(item, attrCreatedAt)
	if err != nil {
		return store.UserSession{}, err
	}
	lastActiveAt, err := mustGetTime(item, attrLastActiveAt)
	if err != nil {
		return store.UserSession{}, err
	}
	userAgent, err := mustGetS(item, attrUserAgent)
	if err != nil {
		return store.UserSession{}, err
	}
	ipAddress, err := mustGetS(item, attrIPAddress)
	if err != nil {
		return store.UserSession{}, err
	}
	return store.UserSession{
		ID:           id,
		UserID:       userID,
		ExpiresAt:    expiresAt,
		CreatedAt:    createdAt,
		LastActiveAt: lastActiveAt,
		UserAgent:    userAgent,
		IPAddress:    ipAddress,
	}, nil
}

func (st *sessionStore) Create(ctx context.Context, p store.CreateSessionParams) error {
	now := time.Now().UTC()
	_, err := st.s.putItem(ctx, &dynamodb.PutItemInput{
		TableName:           aws.String(st.table()),
		Item:                sessionToItem(p, now),
		ConditionExpression: aws.String("attribute_not_exists(id)"),
	}, attrID)
	return mapErr(err)
}

func (st *sessionStore) GetByID(ctx context.Context, id string) (*store.UserSession, error) {
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
	sess, err := itemToSession(out.Item)
	if err != nil {
		return nil, err
	}
	if sess.ExpiresAt.Before(time.Now().UTC()) {
		return nil, store.ErrNotFound
	}
	return &sess, nil
}

func (st *sessionStore) Touch(ctx context.Context, p store.TouchSessionParams) error {
	lastActiveStr := timeToStr(p.LastActiveAt)
	_, err := st.s.updateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:           aws.String(st.table()),
		Key:                 map[string]ddbtypes.AttributeValue{attrID: attrS(p.ID)},
		UpdateExpression:    aws.String("SET last_active_at = :now, expires_at = :exp, #ttl = :ttl"),
		ConditionExpression: aws.String("attribute_exists(id) AND last_active_at < :lastActive"),
		ExpressionAttributeNames: map[string]string{
			"#ttl": attrTTL,
		},
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":now":        attrS(timeToStr(time.Now().UTC())),
			":exp":        attrS(timeToStr(p.ExpiresAt)),
			":ttl":        attrN(p.ExpiresAt.Unix()),
			":lastActive": attrS(lastActiveStr),
		},
	})
	if err != nil {
		// Condition failure (stale touch) is not an error.
		var condErr *ddbtypes.ConditionalCheckFailedException
		if errors.As(err, &condErr) {
			return nil
		}
		return mapErr(err)
	}
	return nil
}

func (st *sessionStore) Delete(ctx context.Context, id string) (int64, error) {
	out, err := st.s.deleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName:    aws.String(st.table()),
		Key:          map[string]ddbtypes.AttributeValue{attrID: attrS(id)},
		ReturnValues: ddbtypes.ReturnValueAllOld,
	})
	if err != nil {
		return 0, mapErr(err)
	}
	if out.Attributes == nil {
		return 0, nil
	}
	return 1, nil
}

func (st *sessionStore) DeleteByUser(ctx context.Context, userID string) error {
	return st.deleteByGSI(ctx, gsiUserID, attrUserID, userID, nil)
}

func (st *sessionStore) DeleteOthers(ctx context.Context, p store.DeleteOtherSessionsParams) error {
	return st.deleteByGSI(ctx, gsiUserID, attrUserID, p.UserID, &p.KeepID)
}

func (st *sessionStore) deleteByGSI(ctx context.Context, indexName, keyName, keyValue string, excludeID *string) error {
	var keys []map[string]ddbtypes.AttributeValue
	err := st.s.queryPages(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(st.table()),
		IndexName:              aws.String(indexName),
		KeyConditionExpression: aws.String("#k = :v"),
		ProjectionExpression:   aws.String(attrID),
		ExpressionAttributeNames: map[string]string{
			"#k": keyName,
		},
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":v": attrS(keyValue),
		},
	}, func(item map[string]ddbtypes.AttributeValue) bool {
		id := getS(item, attrID)
		if excludeID != nil && id == *excludeID {
			return true
		}
		keys = append(keys, map[string]ddbtypes.AttributeValue{attrID: attrS(id)})
		return true
	})
	if err != nil {
		return err
	}
	return st.s.batchDelete(ctx, st.table(), keys)
}

func (st *sessionStore) ListByUserID(ctx context.Context, userID string) ([]store.UserSession, error) {
	now := time.Now().UTC()
	var sessions []store.UserSession
	err := st.s.queryPages(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(st.table()),
		IndexName:              aws.String(gsiUserID),
		KeyConditionExpression: aws.String("user_id = :uid"),
		ExpressionAttributeValues: map[string]ddbtypes.AttributeValue{
			":uid": attrS(userID),
		},
	}, func(item map[string]ddbtypes.AttributeValue) bool {
		sess, err := itemToSession(item)
		if err != nil {
			return false
		}
		if !sess.ExpiresAt.Before(now) {
			sessions = append(sessions, sess)
		}
		return true
	})
	if err != nil {
		return nil, err
	}
	return ptrconv.NonNil(sessions), nil
}

func (st *sessionStore) ListAllActive(ctx context.Context, p store.ListAllActiveSessionsParams) ([]store.ActiveSession, error) {
	now := time.Now().UTC()
	nowStr := timeToStr(now)

	keyExpr := "not_expired = :ne"
	exprValues := map[string]ddbtypes.AttributeValue{
		":ne":  attrS(sentinelActive),
		":now": attrS(nowStr),
	}
	if p.Cursor != "" {
		cursorTime, _, err := store.ParseCursorTime(p.Cursor)
		if err != nil {
			return nil, err
		}
		keyExpr = "not_expired = :ne AND last_active_at < :cursor"
		exprValues[":cursor"] = attrS(timeToStr(cursorTime))
	}

	var sessions []store.UserSession
	err := st.s.queryPages(ctx, &dynamodb.QueryInput{
		TableName:                 aws.String(st.table()),
		IndexName:                 aws.String(gsiNotExpiredLastActiveAt),
		KeyConditionExpression:    aws.String(keyExpr),
		FilterExpression:          aws.String("expires_at > :now"),
		ExpressionAttributeValues: exprValues,
		ScanIndexForward:          aws.Bool(false),
	}, func(item map[string]ddbtypes.AttributeValue) bool {
		sess, err := itemToSession(item)
		if err != nil {
			return false
		}
		sessions = append(sessions, sess)
		return p.Limit <= 0 || int64(len(sessions)) < p.Limit
	})
	if err != nil {
		return nil, err
	}

	// Batch-fetch all usernames.
	userIDs := store.MapSlice(sessions, func(s store.UserSession) string { return s.UserID })
	usernames, err := st.s.lookupUsernames(ctx, userIDs)
	if err != nil {
		return nil, err
	}

	return ptrconv.NonNil(store.SessionsToActive(sessions, usernames)), nil
}

func (st *sessionStore) ValidateWithUser(ctx context.Context, id string) (*store.SessionWithUser, error) {
	sess, err := st.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}

	out, err := st.s.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(st.s.table(tableUsers)),
		Key:       map[string]ddbtypes.AttributeValue{attrID: attrS(sess.UserID)},
		ProjectionExpression: aws.String(
			"id, org_id, username, is_admin, email_verified, deleted_at",
		),
	})
	if err != nil {
		return nil, mapErr(err)
	}
	if out.Item == nil {
		return nil, store.ErrNotFound
	}

	if getTimePtr(out.Item, attrDeletedAt) != nil {
		return nil, store.ErrNotFound
	}

	return &store.SessionWithUser{
		UserID:        getS(out.Item, attrID),
		OrgID:         getS(out.Item, attrOrgID),
		Username:      getS(out.Item, attrUsername),
		IsAdmin:       getBool(out.Item, attrIsAdmin),
		EmailVerified: getBool(out.Item, attrEmailVerified),
	}, nil
}
