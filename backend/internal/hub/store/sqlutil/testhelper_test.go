package sqlutil

import (
	"context"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSetEntityColumnValueUsesApprovedIdentifiersAndParameterStyle(t *testing.T) {
	var gotQuery string
	var gotArgs []any
	at := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	err := SetEntityColumnValue(
		context.Background(),
		func(_ context.Context, query string, args ...any) error {
			gotQuery = query
			gotArgs = args
			return nil
		},
		ParameterStyleDollar,
		store.EntityWorkers,
		"deleted_at",
		"worker-id",
		at,
	)

	require.NoError(t, err)
	assert.Equal(t, "UPDATE workers SET deleted_at = $1 WHERE id = $2", gotQuery)
	assert.Equal(t, []any{at, "worker-id"}, gotArgs)
}

func TestSetEntityColumnValueRejectsUnknownColumnWithoutExecuting(t *testing.T) {
	called := false
	err := SetEntityColumnValue(
		context.Background(),
		func(context.Context, string, ...any) error {
			called = true
			return nil
		},
		ParameterStyleQuestionMark,
		store.EntityWorkers,
		"auth_token",
		"id",
		time.Now(),
	)

	require.EqualError(t, err, `unknown entity column "auth_token"`)
	assert.False(t, called)
}

func TestSetEntityColumnValueRejectsUnknownParameterStyleWithoutExecuting(t *testing.T) {
	called := false
	err := SetEntityColumnValue(
		context.Background(),
		func(context.Context, string, ...any) error {
			called = true
			return nil
		},
		ParameterStyle(255),
		store.EntityWorkers,
		"deleted_at",
		"id",
		time.Now(),
	)

	require.EqualError(t, err, "unknown parameter style 255")
	assert.False(t, called)
}
