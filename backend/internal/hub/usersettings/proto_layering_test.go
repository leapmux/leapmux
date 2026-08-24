package usersettings_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/reflect/protoreflect"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// The three proto paths this walk reasons about.
const (
	userProtoPath      = "leapmux/v1/user.proto"
	adminProtoPath     = "leapmux/v1/admin.proto"
	settingsProtoPath  = "leapmux/v1/settings.proto"
	timestampProtoPath = "google/protobuf/timestamp.proto"
)

// importChain returns the shortest chain of proto paths from root to
// target, root first and target last, or nil when target is not
// reachable. The search is breadth-first so the reported chain is the
// shortest one, which is the one a reader must break.
func importChain(root protoreflect.FileDescriptor, target string) []string {
	type step struct {
		file  protoreflect.FileDescriptor
		chain []string
	}
	seen := map[string]bool{root.Path(): true}
	queue := []step{{file: root, chain: []string{root.Path()}}}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		imports := cur.file.Imports()
		for i := range imports.Len() {
			imp := imports.Get(i)
			chain := append(append([]string(nil), cur.chain...), imp.Path())
			if imp.Path() == target {
				return chain
			}
			if seen[imp.Path()] {
				continue
			}
			seen[imp.Path()] = true
			queue = append(queue, step{file: imp.FileDescriptor, chain: chain})
		}
	}
	return nil
}

// TestUserProtoDoesNotReachAdminProto pins the layering that splitting
// settings.proto out of admin.proto exists to create.
//
// leapmux/v1/user.proto carries the account-settings RPCs
// (ListUserSettings, UpdateUserSetting, ResetUserSetting). It reaches the
// shared SettingDescriptor shape through settings.proto, and it must
// never reach it through admin.proto, which declares the instance-scope
// administration surface. Re-adding one admin type to a user message
// restores the whole dependency, and nothing else refuses it.
//
// WHY THIS TEST LIVES HERE. The natural home is internal/hub/service,
// which owns the mapper between these descriptors and the proto messages.
// This package is the second choice, for what the split protects: the
// account keys declared in keys.go are the ENTIRE payload of the
// user-facing settings RPCs, and this directory already pins that
// surface's other cross-boundary contract — schema_golden_test.go pins
// what the hub and the frontend registry must agree on. Whoever changes
// the account settings surface reads this directory.
//
// WHAT IT PROVES, AND WHAT IT DOES NOT. It asserts the DESCRIPTOR graph.
// Every leapmux/v1 proto generates into one Go package, so a Go binary
// that links user.pb.go links the admin descriptors whatever this test
// says. The payoff is on the client: the TypeScript codegen emits one
// module per proto file, so this import graph is what decides whether a
// user-facing bundle carries the admin surface.
func TestUserProtoDoesNotReachAdminProto(t *testing.T) {
	userProto := leapmuxv1.File_leapmux_v1_user_proto
	require.NotNil(t, userProto, "the generated user.proto descriptor is missing")
	require.Equal(t, userProtoPath, userProto.Path(), "the walk starts at the wrong file")

	// The target must still exist under the asserted path. A renamed or
	// deleted admin.proto would make the assertion below vacuous, and it
	// would pass for the wrong reason for as long as nobody noticed.
	adminProto := leapmuxv1.File_leapmux_v1_admin_proto
	require.NotNil(t, adminProto, "the generated admin.proto descriptor is missing")
	require.Equal(t, adminProtoPath, adminProto.Path(),
		"admin.proto moved; update adminProtoPath or this test asserts nothing")

	// Liveness, both depths. The direct hop proves the walk reads the
	// import list at all; the transitive hop (user -> settings ->
	// timestamp) proves it descends, which is the whole property the
	// refusal below depends on. Without these, a placeholder descriptor
	// that reported no imports would make this test pass on an empty walk.
	require.Equal(t, []string{userProtoPath, settingsProtoPath},
		importChain(userProto, settingsProtoPath),
		"user.proto no longer imports settings.proto directly")
	// user.proto also imports timestamp.proto directly (passkey RPCs), so the
	// shortest chain to timestamp skips settings.
	require.Equal(t, []string{userProtoPath, timestampProtoPath},
		importChain(userProto, timestampProtoPath),
		"user.proto must still reach timestamp.proto (directly or transitively)")

	chain := importChain(userProto, adminProtoPath)
	assert.Nilf(t, chain,
		"%s reaches %s through %s.\n"+
			"The user-facing settings surface must reach SettingDescriptor through %s only.\n"+
			"Move the shared message into %s, or keep the admin type out of the user message.",
		userProtoPath, adminProtoPath, strings.Join(chain, " -> "), settingsProtoPath, settingsProtoPath)
}
