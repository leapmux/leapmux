package service

import (
	"os"
	"regexp"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The grantable vocabulary is grouped into families twice: the consent
// screen's scopeCategories table (this package) and the Preferences
// dialog's SCOPE_CATEGORIES catalogue (frontend
// src/components/settings/account/scopeCatalogue.ts). Each side's own tests
// pin only totality against the enum, so a scope moved between families --
// or a renamed family -- passed both suites while the two surfaces
// disagreed. This test is the pin between them, in the same shape as the
// impliedBy mirror (backend/internal/authscope): family order, labels, and
// membership must match exactly.

const scopeCataloguePath = "../../../../frontend/src/components/settings/account/scopeCatalogue.ts"

// catalogueTokenPattern matches one `label: 'Family'` header or one
// `scope: Scope.NAME` entry of SCOPE_CATEGORIES, in file order. The
// ScopeEntry interface's own `scope: Scope` field states no enum name, so
// it cannot match.
var catalogueTokenPattern = regexp.MustCompile(`(?:label: '([^']+)'|scope: Scope\.([A-Z0-9_]+))`)

func TestScopeCategoriesMatchTheFrontendCatalogue(t *testing.T) {
	raw, err := os.ReadFile(scopeCataloguePath)
	if err != nil {
		// FAIL, not skip: the target is a git-controlled source file, so a
		// read failure means it moved or was renamed -- exactly the drift
		// this pin exists to catch.
		t.Fatalf("frontend scope catalogue not reachable from this checkout: %v", err)
	}
	type family struct {
		label  string
		scopes []leapmuxv1.Scope
	}
	var got []family
	for _, m := range catalogueTokenPattern.FindAllStringSubmatch(string(raw), -1) {
		switch {
		case m[1] != "":
			got = append(got, family{label: m[1]})
		case m[2] != "":
			value, ok := leapmuxv1.Scope_value["SCOPE_"+m[2]]
			require.Truef(t, ok,
				"scopeCatalogue.ts states Scope.%s, which the proto enum does not define", m[2])
			require.NotEmpty(t, got, "a scope entry appears before any family label")
			got[len(got)-1].scopes = append(got[len(got)-1].scopes, leapmuxv1.Scope(value))
		}
	}
	require.NotEmpty(t, got,
		"SCOPE_CATEGORIES no longer parses; update this test's pattern with its new shape")

	require.Lenf(t, got, len(scopeCategories),
		"the consent screen and the Preferences catalogue state different family counts")
	for i, want := range scopeCategories {
		assert.Equalf(t, want.label, got[i].label,
			"family %d: the Preferences catalogue labels it %q where the consent screen says %q",
			i, got[i].label, want.label)
		assert.Equal(t, want.scopes, got[i].scopes,
			"family "+want.label+": the Preferences catalogue and the consent screen disagree on membership or order")
	}
}
