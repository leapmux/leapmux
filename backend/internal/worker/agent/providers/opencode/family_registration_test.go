package opencode

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestFamilySpecKeepsRegistrationMetadataOut leaves the provider Registration
// as the one source for its launch and option metadata.
func TestFamilySpecKeepsRegistrationMetadataOut(t *testing.T) {
	t.Parallel()

	typ := reflect.TypeFor[FamilySpec]()
	for _, duplicate := range []string{"Provider", "Locator", "OptionGroups"} {
		_, ok := typ.FieldByName(duplicate)
		assert.Falsef(t, ok, "FamilySpec must read %s from Registration", duplicate)
	}
}
