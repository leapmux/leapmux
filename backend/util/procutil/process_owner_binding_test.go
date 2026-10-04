package procutil

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBindDescendantsRejectsAbsentUnstartedAndClosedOwners(t *testing.T) {
	ctx := t.Context()
	require.ErrorContains(t, (*ProcessOwner)(nil).BindDescendants(ctx), "owner is absent")
	owner := PrepareProcess(nil)
	require.ErrorContains(t, owner.BindDescendants(ctx), "before its descendants could be bound")
	var absentContext context.Context
	require.ErrorContains(t, owner.BindDescendants(absentContext), "context is absent")
	require.NoError(t, owner.Close())
	require.ErrorContains(t, owner.BindDescendants(ctx), "before its descendants could be bound")
}

func TestBindDescendantsRetainsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	owner := PrepareProcess(nil)
	require.ErrorIs(t, owner.BindDescendants(ctx), context.Canceled)
}
