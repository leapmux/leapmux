package nilcheck

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

type probe interface{ Do() }

type ptrImpl struct{}

func (*ptrImpl) Do() {}

type funcImpl func()

func (funcImpl) Do() {}

type mapImpl map[string]bool

func (mapImpl) Do() {}

type valueImpl struct{}

func (valueImpl) Do() {}

// nilProbe returns a nil *ptrImpl AS a probe. That conversion is the whole
// subject of TestNilPointerInInterfaceDefeatsPlainNilCheck: it is what turns a
// nil pointer into a non-nil interface value. Returning it from a function also
// keeps the caller's `!= nil` a real runtime comparison rather than one the
// compiler and the linter can fold away from the concrete type in scope.
func nilProbe() probe {
	var nilPtr *ptrImpl
	return nilPtr
}

// TestIsNilDependency_CoversEveryNilableKind is the whole point of the package.
// A Pointer-only check is the easy thing to write and it admits a nil func- or
// map-typed dependency, which then panics on first use -- the failure the
// constructor guard exists to make impossible.
func TestIsNilDependency_CoversEveryNilableKind(t *testing.T) {
	var nilPtr *ptrImpl
	var nilFunc funcImpl
	var nilMap mapImpl
	var nilIface probe

	for name, tc := range map[string]struct {
		value any
		want  bool
	}{
		"untyped nil":            {nil, true},
		"nil pointer in iface":   {probe(nilPtr), true},
		"nil func in iface":      {probe(nilFunc), true},
		"nil map in iface":       {probe(nilMap), true},
		"nil interface variable": {nilIface, true},
		"nil slice":              {[]string(nil), true},
		"nil chan":               {(chan int)(nil), true},
		"live pointer":           {probe(&ptrImpl{}), false},
		"live func":              {probe(funcImpl(func() {})), false},
		"live map":               {probe(mapImpl{}), false},
		"non-nilable value":      {probe(valueImpl{}), false},
		"empty string":           {"", false},
		"zero int":               {0, false},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, IsNilDependency(tc.value))
		})
	}
}

// TestNilPointerInInterfaceDefeatsPlainNilCheck documents WHY the package
// exists: the obvious guard a constructor would otherwise write does not work.
func TestNilPointerInInterfaceDefeatsPlainNilCheck(t *testing.T) {
	dep := nilProbe()

	// The comparison is written out rather than run through assert.NotNil,
	// which does its own reflect-based unwrapping and would hide the very trap
	// this documents. `dep != nil` is what a constructor would actually write.
	//
	// staticcheck flags this as SA4023, "this comparison is always true" -- which
	// is precisely the finding: the guard a constructor would reach for cannot
	// fail, so it cannot catch anything. The suppression keeps the demonstration.
	//nolint:staticcheck,testifylint // SA4023 is the property under test
	assert.True(t, dep != nil,
		"a nil pointer in an interface is not a nil interface -- this is the trap")
	assert.True(t, IsNilDependency(dep), "and this is what catches it")
}
