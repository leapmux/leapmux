package service

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
)

// The app registration surface takes the elevation gate by a hand-written call
// in each handler, exactly as the user and admin surfaces do. It is a THIRD
// proto file, and the two records beside this one walk user.proto and
// admin.proto only -- so every AppService verb satisfied both of them while
// carrying no gate at all.
//
// Classifying a procedure here is NOT what protects it. The handler's own call
// is. What this catches is a procedure nobody THOUGHT about.

// appElevationClass says how one AppService procedure relates to the gate.
//
// Two values, because this surface admits one rule: every write takes
// requireElevatedActor, and the rest take nothing. There is no session-only
// class here -- `leapmux control admin app register` is a documented headless
// path, and a command-line credential proves its factor through
// /oauth/step-up.
type appElevationClass int

const (
	// appNeedsElevatedCredential calls AppService.requireElevatedOwner: any
	// credential that can carry an elevation, a command-line one included.
	appNeedsElevatedCredential appElevationClass = iota
	// appNeedsNoElevation reads, or only reduces access.
	appNeedsNoElevation
)

// appProcedureElevation records the class of every AppService procedure, with
// the reason in the same place as the classification. See
// AppService.requireElevatedOwner for the whole rule.
var appProcedureElevation = map[string]struct {
	class  appElevationClass
	reason string
}{
	leapmuxv1connect.AppServiceRegisterAppProcedure: {appNeedsElevatedCredential,
		"mints a client secret and a row that writes the next consent screen"},
	leapmuxv1connect.AppServiceUpdateAppProcedure: {appNeedsElevatedCredential,
		"rewriting a redirect list diverts an in-flight authorization code to an address the editor chose"},
	leapmuxv1connect.AppServiceSetAppElevationAllowedProcedure: {appNeedsElevatedCredential,
		"hands an app the step-up ceremony, which multiplies every scope it holds"},
	leapmuxv1connect.AppServiceVerifyAppProcedure: {appNeedsElevatedCredential,
		"removes the unverified label a person reads before granting"},

	leapmuxv1connect.AppServiceListAppsProcedure: {appNeedsNoElevation,
		"reads the caller's own registrations; no secret leaves the store layer"},
	leapmuxv1connect.AppServiceRevokeAppProcedure: {appNeedsNoElevation,
		"only REDUCES access, and it is the remedy on realizing an app is malicious; a delay is the attacker's gain"},
	leapmuxv1connect.AppServiceDeleteAppProcedure: {appNeedsNoElevation,
		"refused while any credential row exists, so the reduction already happened through RevokeApp"},
}

// TestEveryAppProcedureIsElevationClassified is the tripwire: a new AppService
// method fails this test until somebody states which side of the gate it sits
// on and why.
func TestEveryAppProcedureIsElevationClassified(t *testing.T) {
	t.Parallel()

	paths := protoProcedurePaths(t, leapmuxv1.File_leapmux_v1_app_proto)
	for _, path := range paths {
		entry, ok := appProcedureElevation[path]
		assert.Truef(t, ok,
			"app.proto method %q is not elevation-classified; the gate is a hand-written call in each handler, so an unclassified procedure is one nobody decided about -- add it to appProcedureElevation with a reason", path)
		assert.NotEmptyf(t, entry.reason, "app.proto method %q has an empty reason", path)
	}

	known := make(map[string]bool, len(paths))
	for _, path := range paths {
		known[path] = true
	}
	for procedure := range appProcedureElevation {
		assert.Truef(t, known[procedure],
			"procedure %q is elevation-classified but app.proto no longer declares it; remove the stale entry", procedure)
	}
}

// TestAppWriteProceduresAreClassifiedProtected reads the classification
// against the SHAPE of the verb, in the house form of
// TestAdminWriteProceduresAreClassifiedProtected.
//
// A verb called Register, Update, Set or Verify creates or moves authority
// that other accounts stand on, so none of those may sit in
// appNeedsNoElevation. The three that legitimately need nothing all read
// (List), only reduce access (Revoke), or remove a record the reduction
// already emptied (Delete).
func TestAppWriteProceduresAreClassifiedProtected(t *testing.T) {
	t.Parallel()

	creating := []string{"Register", "Update", "Set", "Verify"}
	for procedure, entry := range appProcedureElevation {
		method := procedure[strings.LastIndex(procedure, "/")+1:]
		for _, verb := range creating {
			if !strings.HasPrefix(method, verb) {
				continue
			}
			assert.NotEqualf(t, appNeedsNoElevation, entry.class,
				"procedure %q creates or moves authority, so it may not be classified appNeedsNoElevation -- wire it to a gate, or rename it if it only reduces access", procedure)
		}
	}
}
