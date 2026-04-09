package dynamodb

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	smithy "github.com/aws/smithy-go"

	"github.com/leapmux/leapmux/internal/hub/store"
)

// conditionCheckFailedCode is the cancellation reason code returned by DynamoDB
// when a condition expression in a TransactWriteItems request fails.
const conditionCheckFailedCode = "ConditionalCheckFailed"

// Sparse GSI partition key sentinels.
const (
	// deletedFalse / deletedTrue are the two values of the "deleted"
	// attribute, mirroring the SQL convention. The attribute serves as the
	// partition key for two GSIs with different sort keys:
	//   deleted-created_at-index  (deleted="0", SK=created_at) — active listing
	//   deleted-deleted_at-index  (deleted="1", SK=deleted_at) — cleanup
	deletedFalse = "0"
	deletedTrue  = "1"

	// sentinelActive is written to "not_expired" for session GSIs.
	sentinelActive = "1"

	sentinelExpiryGroup = "T" // partition key for the oauth-token expiry GSI
)

// --- Time encoding/decoding ---

const timeFormat = time.RFC3339Nano

func timeToStr(t time.Time) string {
	return t.UTC().Format(timeFormat)
}

func strToTime(s string) (time.Time, error) {
	return time.Parse(timeFormat, s)
}

func timePtrToStr(t *time.Time) string {
	if t == nil {
		return ""
	}
	return timeToStr(*t)
}

func strToTimePtr(s string) *time.Time {
	if s == "" {
		return nil
	}
	t, err := strToTime(s)
	if err != nil {
		return nil
	}
	return &t
}

// --- Attribute helpers ---

func attrS(v string) types.AttributeValue {
	return &types.AttributeValueMemberS{Value: v}
}

func attrN(v int64) types.AttributeValue {
	return &types.AttributeValueMemberN{Value: strconv.FormatInt(v, 10)}
}

func attrBool(v bool) types.AttributeValue {
	return &types.AttributeValueMemberBOOL{Value: v}
}

func attrB(v []byte) types.AttributeValue {
	if v == nil {
		return &types.AttributeValueMemberNULL{Value: true}
	}
	return &types.AttributeValueMemberS{Value: base64.StdEncoding.EncodeToString(v)}
}

func getS(item map[string]types.AttributeValue, key string) string {
	v, ok := item[key]
	if !ok {
		return ""
	}
	if sv, ok := v.(*types.AttributeValueMemberS); ok {
		return sv.Value
	}
	return ""
}

func getN(item map[string]types.AttributeValue, key string) int64 {
	v, ok := item[key]
	if !ok {
		return 0
	}
	if nv, ok := v.(*types.AttributeValueMemberN); ok {
		n, _ := strconv.ParseInt(nv.Value, 10, 64)
		return n
	}
	return 0
}

func getBool(item map[string]types.AttributeValue, key string) bool {
	v, ok := item[key]
	if !ok {
		return false
	}
	if bv, ok := v.(*types.AttributeValueMemberBOOL); ok {
		return bv.Value
	}
	return false
}

func getTime(item map[string]types.AttributeValue, key string) time.Time {
	s := getS(item, key)
	if s == "" {
		return time.Time{}
	}
	t, _ := strToTime(s)
	return t
}

func getTimePtr(item map[string]types.AttributeValue, key string) *time.Time {
	return strToTimePtr(getS(item, key))
}

// getBytes extracts []byte from a base64-encoded S attribute.
func getBytes(item map[string]types.AttributeValue, key string) []byte {
	v, ok := item[key]
	if !ok {
		return nil
	}
	switch tv := v.(type) {
	case *types.AttributeValueMemberS:
		if tv.Value == "" {
			return nil
		}
		b, err := base64.StdEncoding.DecodeString(tv.Value)
		if err != nil {
			return nil
		}
		return b
	case *types.AttributeValueMemberB:
		return tv.Value
	case *types.AttributeValueMemberNULL:
		return nil
	}
	return nil
}

// getSAsInt64 extracts an int64 from a DynamoDB string attribute that
// stores a numeric value as a string (used for GSI key attributes that
// must be of type S).
func getSAsInt64(item map[string]types.AttributeValue, key string) int64 {
	s := getS(item, key)
	if s == "" {
		return 0
	}
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

// --- Must-get attribute helpers (return errors) ---

func mustGetS(item map[string]types.AttributeValue, key string) (string, error) {
	v, ok := item[key]
	if !ok {
		return "", fmt.Errorf("attribute %q: missing", key)
	}
	sv, ok := v.(*types.AttributeValueMemberS)
	if !ok {
		return "", fmt.Errorf("attribute %q: not a string", key)
	}
	return sv.Value, nil
}

func mustGetN(item map[string]types.AttributeValue, key string) (int64, error) {
	v, ok := item[key]
	if !ok {
		return 0, fmt.Errorf("attribute %q: missing", key)
	}
	nv, ok := v.(*types.AttributeValueMemberN)
	if !ok {
		return 0, fmt.Errorf("attribute %q: not a number", key)
	}
	n, err := strconv.ParseInt(nv.Value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("attribute %q: %w", key, err)
	}
	return n, nil
}

func mustGetBool(item map[string]types.AttributeValue, key string) (bool, error) {
	v, ok := item[key]
	if !ok {
		return false, fmt.Errorf("attribute %q: missing", key)
	}
	bv, ok := v.(*types.AttributeValueMemberBOOL)
	if !ok {
		return false, fmt.Errorf("attribute %q: not a bool", key)
	}
	return bv.Value, nil
}

func mustGetTime(item map[string]types.AttributeValue, key string) (time.Time, error) {
	s, err := mustGetS(item, key)
	if err != nil {
		return time.Time{}, err
	}
	t, err := strToTime(s)
	if err != nil {
		return time.Time{}, fmt.Errorf("attribute %q: %w", key, err)
	}
	return t, nil
}

func mustGetBytes(item map[string]types.AttributeValue, key string) ([]byte, error) {
	v, ok := item[key]
	if !ok {
		return nil, fmt.Errorf("attribute %q: missing", key)
	}
	switch tv := v.(type) {
	case *types.AttributeValueMemberS:
		if tv.Value == "" {
			return nil, fmt.Errorf("attribute %q: empty string", key)
		}
		b, err := base64.StdEncoding.DecodeString(tv.Value)
		if err != nil {
			return nil, fmt.Errorf("attribute %q: %w", key, err)
		}
		return b, nil
	case *types.AttributeValueMemberB:
		return tv.Value, nil
	case *types.AttributeValueMemberNULL:
		return nil, fmt.Errorf("attribute %q: null", key)
	}
	return nil, fmt.Errorf("attribute %q: unexpected type %T", key, v)
}

func mustGetSAsInt64(item map[string]types.AttributeValue, key string) (int64, error) {
	s, err := mustGetS(item, key)
	if err != nil {
		return 0, err
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("attribute %q: %w", key, err)
	}
	return n, nil
}

// --- Error mapping ---

// isConditionFailed returns true if the error is a DynamoDB ConditionalCheckFailedException.
func isConditionFailed(err error) bool {
	var condErr *types.ConditionalCheckFailedException
	return errors.As(err, &condErr)
}

// mapErr converts DynamoDB API errors to store sentinel errors.
func mapErr(err error) error {
	if err == nil {
		return nil
	}

	var condErr *types.ConditionalCheckFailedException
	if errors.As(err, &condErr) {
		return store.ErrConflict
	}

	var txCancelErr *types.TransactionCanceledException
	if errors.As(err, &txCancelErr) {
		// Check if any reason is ConditionalCheckFailed.
		for _, reason := range txCancelErr.CancellationReasons {
			if reason.Code != nil && *reason.Code == conditionCheckFailedCode {
				return store.ErrConflict
			}
		}
		return fmt.Errorf("transaction canceled: %w", err)
	}

	var notFoundErr *types.ResourceNotFoundException
	if errors.As(err, &notFoundErr) {
		return store.ErrNotFound
	}

	var oe *smithy.OperationError
	if errors.As(err, &oe) {
		return fmt.Errorf("%s: %w", oe.Operation(), oe.Err)
	}

	return err
}

// --- Composite key helpers ---

// tabSK builds the composite sort key for workspace_tabs: "tab_type#tab_id"
func tabSK(tabType, tabID string) string {
	return tabType + "#" + tabID
}

// parseTabSK splits a composite sort key "tab_type#tab_id".
func parseTabSK(sk string) (tabType, tabID string) {
	parts := strings.SplitN(sk, "#", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return sk, ""
}
