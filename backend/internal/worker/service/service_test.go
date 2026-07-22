package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/channel"
)

func TestExpandTilde(t *testing.T) {
	home, err := os.UserHomeDir()
	require.NoError(t, err, "failed to get home dir")

	tests := []struct {
		name string
		in   string
		want string
	}{
		{"bare tilde", "~", home},
		{"tilde slash", "~/Documents", filepath.Join(home, "Documents")},
		{"tilde nested", "~/a/b/c", filepath.Join(home, "a/b/c")},
		{"absolute path unchanged", "/usr/local/bin", "/usr/local/bin"},
		{"relative path unchanged", "some/path", "some/path"},
		{"empty string", "", ""},
		{"double tilde unchanged", "~~", "~~"},
		{"tilde in middle unchanged", "/foo/~/bar", "/foo/~/bar"},
		{"tilde user unchanged", "~user/foo", "~user/foo"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := expandTilde(tt.in)
			assert.Equal(t, tt.want, got, "expandTilde(%q)", tt.in)
		})
	}
}

// dispatchOwnerOnlyProbe runs one request through a handler registered the way the
// machine-scoped families are (ownerOnlyRegistrar), and reports whether the gate
// admitted the caller. It exercises the REAL gate rather than calling
// requireWorkerOwner directly, so an owner the Hub clobbered is observed exactly as
// a file/git/tunnel RPC would observe it.
func dispatchOwnerOnlyProbe(svc *Context, userID string) bool {
	d := channel.NewDispatcher()
	admitted := false
	ownerOnlyRegistrar{r: newRegistrar(d, svc)}.Register("Probe",
		func(context.Context, string, *leapmuxv1.InnerRpcRequest, *channel.Sender) {
			admitted = true
		})
	d.DispatchWith(context.Background(), userID, &leapmuxv1.InnerRpcRequest{Method: "Probe"}, newTestWriter())
	return admitted
}

// An empty owner from the Hub must NOT clobber a good one.
//
// requireWorkerOwner refuses an empty owner, so storing "" would make the worker
// deny every machine-scoped RPC to its own legitimate user until the next
// connect -- indistinguishably from a real cross-tenant refusal, which is the
// exact failure the Hub-pushed owner exists to prevent. Keeping the previous
// owner is the only safe direction: the Hub cannot legitimately un-own a live
// worker (that is what deregistration is for).
func TestUpdateRegisteredByIgnoresEmptyOwner(t *testing.T) {
	svc := &Context{}
	svc.SetRegisteredBy("user-1")

	svc.UpdateRegisteredBy("")

	assert.Equal(t, "user-1", svc.RegisteredBy(), "an empty push must not clobber the owner")
	assert.True(t, dispatchOwnerOnlyProbe(svc, "user-1"),
		"the worker must still serve its own owner after an empty push")
}

// ...and the guard must not be so broad that it pins the first owner forever: a
// genuine re-registration under a different user is the Hub's call, and the worker
// converges on it.
func TestUpdateRegisteredByAppliesOwnerChange(t *testing.T) {
	svc := &Context{}
	svc.SetRegisteredBy("user-1")

	// The drift path (prev != "" && prev != new) warns and STILL stores: the Hub is
	// the authority, so the warning is a breadcrumb, never a veto.
	svc.UpdateRegisteredBy("user-2")

	assert.Equal(t, "user-2", svc.RegisteredBy())
	assert.True(t, dispatchOwnerOnlyProbe(svc, "user-2"), "the new owner is served")
	assert.False(t, dispatchOwnerOnlyProbe(svc, "user-1"), "the previous owner is not")
}

// The first delivery on a worker with no seed populates the owner.
func TestUpdateRegisteredByAppliesFirstOwner(t *testing.T) {
	svc := &Context{}
	require.Empty(t, svc.RegisteredBy(), "no owner before the Hub delivers one")

	svc.UpdateRegisteredBy("user-1")

	assert.Equal(t, "user-1", svc.RegisteredBy())
	assert.True(t, dispatchOwnerOnlyProbe(svc, "user-1"))
}
