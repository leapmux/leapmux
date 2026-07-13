package sqlutil

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSetTimestampColumnUsesApprovedIdentifiersAndParameterStyle(t *testing.T) {
	var gotQuery string
	var gotArgs []any
	at := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	err := SetTimestampColumn(
		context.Background(),
		func(_ context.Context, query string, args ...any) error {
			gotQuery = query
			gotArgs = args
			return nil
		},
		ParameterStyleDollar,
		TimestampColumnRevocationEventRevokedAt,
		"event-id",
		at,
	)

	require.NoError(t, err)
	assert.Equal(t, "UPDATE revocation_events SET revoked_at = $1 WHERE id = $2", gotQuery)
	assert.Equal(t, []any{at, "event-id"}, gotArgs)
}

func TestSetTimestampColumnRejectsUnknownColumnWithoutExecuting(t *testing.T) {
	called := false
	err := SetTimestampColumn(
		context.Background(),
		func(context.Context, string, ...any) error {
			called = true
			return nil
		},
		ParameterStyleQuestionMark,
		TimestampColumn(255),
		"id",
		time.Now(),
	)

	require.EqualError(t, err, "unknown timestamp column 255")
	assert.False(t, called)
}

func TestSetTimestampColumnRejectsUnknownParameterStyleWithoutExecuting(t *testing.T) {
	called := false
	err := SetTimestampColumn(
		context.Background(),
		func(context.Context, string, ...any) error {
			called = true
			return nil
		},
		ParameterStyle(255),
		TimestampColumnRevocationEventRevokedAt,
		"id",
		time.Now(),
	)

	require.EqualError(t, err, "unknown parameter style 255")
	assert.False(t, called)
}
