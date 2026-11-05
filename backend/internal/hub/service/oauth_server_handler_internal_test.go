package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDetachedContext(t *testing.T) {
	t.Parallel()

	type requestKey struct{}
	newRequest := func() (*http.Request, context.CancelFunc) {
		ctx, cancel := context.WithCancel(context.WithValue(context.Background(), requestKey{}, "value"))
		return httptest.NewRequest(http.MethodPost, "/oauth/token", nil).WithContext(ctx), cancel
	}

	t.Run("a hub lifetime outlives the request and ends with the hub", func(t *testing.T) {
		t.Parallel()
		servingCtx, endHub := context.WithCancel(context.Background())
		defer endHub()
		h := &OAuthServerHandler{servingCtx: servingCtx}
		r, disconnect := newRequest()

		ctx := h.detachedContext(r)
		disconnect()
		assert.NoError(t, ctx.Err(), "a client disconnect must not end the work")

		endHub()
		assert.ErrorIs(t, ctx.Err(), context.Canceled, "the end of the hub must end the work")
	})

	t.Run("no hub lifetime outlives the request and keeps its values", func(t *testing.T) {
		t.Parallel()
		h := &OAuthServerHandler{}
		r, disconnect := newRequest()

		ctx := h.detachedContext(r)
		disconnect()

		assert.NoError(t, ctx.Err(), "a client disconnect must not end the work")
		assert.Equal(t, "value", ctx.Value(requestKey{}))
	})
}
