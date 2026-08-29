package authscope

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The frontend keeps its own copy of impliedBy (scopeCatalogue.ts) so the
// register/edit form can lock the scopes a ticked one implies before it
// submits. Nothing else pins the two together: each side's tests check only
// its own enum, so a graph edited on one side silently disagrees with the
// other about what a grant expands to. This test is the pin, in the same
// shape as the OAuth pages' palette pin: a Go test reads the frontend file,
// and a graph that drifts fails the suite until both sides match.

// scopeCataloguePath is the frontend mirror, relative to this package.
const scopeCataloguePath = "../../../frontend/src/components/settings/account/scopeCatalogue.ts"

// impliedEntryPattern matches one `[Scope.NAME]: [Scope.NAME, ...],` row of
// the frontend IMPLIED_BY table.
var impliedEntryPattern = regexp.MustCompile(`\[Scope\.([A-Z0-9_]+)\]:\s*\[([^\]]*)\]`)

func TestFrontendImpliedByMatchesTheHubGraph(t *testing.T) {
	raw, err := os.ReadFile(scopeCataloguePath)
	if err != nil {
		// FAIL, not skip: the target is a git-controlled source file, so a
		// read failure means it moved or was renamed -- exactly the drift
		// this pin exists to catch. Skipping here would silently disable it
		// in every checkout that runs the suite.
		t.Fatalf("scope catalogue not reachable from this checkout: %v", err)
	}
	scopeByName := map[string]leapmuxv1.Scope{}
	for _, scope := range Grantable() {
		scopeByName[scope.String()] = scope
	}

	frontend := map[leapmuxv1.Scope]map[leapmuxv1.Scope]bool{}
	for _, m := range impliedEntryPattern.FindAllStringSubmatch(string(raw), -1) {
		key, ok := scopeByName["SCOPE_"+m[1]]
		require.Truef(t, ok, "IMPLIED_BY key Scope.%s is not a grantable scope", m[1])
		frontend[key] = map[leapmuxv1.Scope]bool{}
		for _, part := range strings.Split(m[2], ",") {
			name := strings.TrimPrefix(strings.TrimSpace(part), "Scope.")
			if name == "" {
				continue
			}
			target, ok := scopeByName["SCOPE_"+name]
			require.Truef(t, ok, "IMPLIED_BY maps %s to unknown scope %s", m[1], name)
			frontend[key][target] = true
		}
	}
	require.NotEmpty(t, frontend, "no IMPLIED_BY entries parsed; scopeCatalogue.ts changed shape?")

	for key, implied := range impliedBy {
		if !assert.Containsf(t, frontend, key,
			"the hub closes %s, but the frontend catalogue states no implications for it", key) {
			continue
		}
		want := frontend[key]
		for _, target := range implied {
			assert.Truef(t, want[target],
				"the hub closes %s to include %s, but the frontend form does not lock it implied", key, target)
		}
		for target := range want {
			assert.Truef(t, slices.Contains(implied, target),
				"the frontend form locks %s implied by %s, but the hub does not close it", target, key)
		}
	}
	for key := range frontend {
		assert.Containsf(t, impliedBy, key,
			"the frontend form states implications for %s, but the hub closes nothing there", key)
	}
}
