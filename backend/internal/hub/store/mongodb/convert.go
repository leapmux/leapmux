package mongodb

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/leapmux/leapmux/internal/hub/store"
)

// --- BSON field extraction helpers ---

func getS(m bson.M, key string) string {
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

func getInt32(m bson.M, key string) int32 {
	v, ok := m[key]
	if !ok || v == nil {
		return 0
	}
	switch n := v.(type) {
	case int32:
		return n
	case int64:
		return int32(n)
	case float64:
		return int32(n)
	}
	return 0
}

func getInt64(m bson.M, key string) int64 {
	v, ok := m[key]
	if !ok || v == nil {
		return 0
	}
	switch n := v.(type) {
	case int64:
		return n
	case int32:
		return int64(n)
	case float64:
		return int64(n)
	}
	return 0
}

func getBool(m bson.M, key string) bool {
	v, ok := m[key]
	if !ok || v == nil {
		return false
	}
	if b, ok := v.(bool); ok {
		return b
	}
	return false
}

func getTime(m bson.M, key string) time.Time {
	v, ok := m[key]
	if !ok || v == nil {
		return time.Time{}
	}
	switch t := v.(type) {
	case time.Time:
		return t
	case bson.DateTime:
		return time.UnixMilli(int64(t)).UTC()
	}
	return time.Time{}
}

func getTimePtr(m bson.M, key string) *time.Time {
	v, ok := m[key]
	if !ok || v == nil {
		return nil
	}
	switch t := v.(type) {
	case time.Time:
		return &t
	case bson.DateTime:
		tt := time.UnixMilli(int64(t)).UTC()
		return &tt
	}
	return nil
}

func getBytes(m bson.M, key string) []byte {
	v, ok := m[key]
	if !ok || v == nil {
		return nil
	}
	if b, ok := v.(bson.Binary); ok {
		return b.Data
	}
	if b, ok := v.(bson.RawValue); ok {
		// Handle raw BSON binary values.
		var data []byte
		if err := b.Unmarshal(&data); err == nil {
			return data
		}
		return nil
	}
	if b, ok := v.([]byte); ok {
		return b
	}
	return nil
}

// truncateMS truncates a time to millisecond precision to match MongoDB's
// BSON Date type, which only stores milliseconds since the Unix epoch.
func truncateMS(t time.Time) time.Time {
	return t.Truncate(time.Millisecond)
}

// bytesVal converts a byte slice for storage as a BSON binary value.
// Returns nil if the input is nil (so the field is omitted from the document).
func bytesVal(b []byte) interface{} {
	if b == nil {
		return nil
	}
	return b
}

// timePtrVal converts a *time.Time for storage in BSON.
// Returns nil if the pointer is nil (so the field is omitted).
// The time is truncated to millisecond precision to match MongoDB's Date type.
func timePtrVal(t *time.Time) interface{} {
	if t == nil {
		return nil
	}
	truncated := truncateMS(*t)
	return truncated
}

// --- Composite ID helpers ---

// compoundID joins parts with "|" to form a composite document _id.
func compoundID(parts ...string) string {
	return strings.Join(parts, "|")
}

// parseCompoundID2 splits a 2-part compound ID.
func parseCompoundID2(id string) (string, string) {
	parts := strings.SplitN(id, "|", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return id, ""
}

// parseCompoundID3 splits a 3-part compound ID.
func parseCompoundID3(id string) (string, string, string) {
	parts := strings.SplitN(id, "|", 3)
	if len(parts) == 3 {
		return parts[0], parts[1], parts[2]
	}
	return id, "", ""
}

// --- Filters ---

// notDeleted returns a filter that excludes soft-deleted documents.
func notDeleted() bson.D {
	return bson.D{{Key: "deleted_at", Value: nil}}
}

// --- Error mapping ---

// mapErr converts MongoDB driver errors to store sentinel errors.
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, mongo.ErrNoDocuments) {
		return store.ErrNotFound
	}
	if mongo.IsDuplicateKeyError(err) {
		return store.ErrConflict
	}
	return err
}
