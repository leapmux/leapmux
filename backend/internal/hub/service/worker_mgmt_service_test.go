package service_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/mail"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// TestListWorkers_RejectsMalformedCursor pins the API-boundary contract: a
// stale (pre-composite-format) or garbled opaque cursor is bad client input
// and must surface as InvalidArgument (400), not the Internal (500) that
// genuine store failures map to. The store's cursor decode wraps
// store.ErrInvalidCursor before any query runs, and ListWorkers classifies
// the store call's error via errors.Is instead of re-parsing the cursor.
func TestListWorkers_RejectsMalformedCursor(t *testing.T) {
	st := testutil.OpenTestStore(t)
	svc := service.NewWorkerManagementService(st, nil, nil, nil, nil, mail.Renderer{}, &config.Config{}, nil)
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{ID: userid.MustNew("u1"), OrgID: "o1"})

	// Missing "_" delimiter -> store.ErrInvalidCursor -> InvalidArgument.
	_, err := svc.ListWorkers(ctx, connect.NewRequest(&leapmuxv1.ListWorkersRequest{
		Page: &leapmuxv1.PageRequest{Cursor: "no-underscore-timestamp"},
	}))
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	// An unparseable timestamp half is also bad client input, not a server fault.
	_, err = svc.ListWorkers(ctx, connect.NewRequest(&leapmuxv1.ListWorkersRequest{
		Page: &leapmuxv1.PageRequest{Cursor: "not-a-time_abc"},
	}))
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	// The empty first-page cursor stays valid: no error, an empty page, and no
	// next cursor for a user with no workers.
	resp, err := svc.ListWorkers(ctx, connect.NewRequest(&leapmuxv1.ListWorkersRequest{}))
	require.NoError(t, err)
	assert.Empty(t, resp.Msg.GetWorkers())
	assert.False(t, resp.Msg.GetPage().GetHasMore())
}
