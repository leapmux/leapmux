// Package nilcheck answers one question that `x != nil` cannot: is this
// interface value holding a nil concrete value?
//
// It exists because narrowing a concrete dependency to a consumer-side
// interface -- good practice, and used throughout the hub -- silently destroys
// the caller's ability to reject a nil one. A nil *workermgr.Manager converted
// to a one-method interface is a NON-nil interface holding a nil pointer, so
// the obvious guard passes and the first method call panics on a nil receiver,
// somewhere far from the miswiring. Constructors that want to fail at
// construction instead need this.
package nilcheck

import "reflect"

// IsNilDependency reports whether value is nil, or is an interface holding a
// nil value of a nilable kind.
//
// All six nilable kinds are covered, not just Pointer. A dependency satisfied
// by a func type or a map type is an ordinary shape for a policy hook or a test
// double, and a Pointer-only check would admit a nil one and then panic on
// first use -- exactly the failure the constructor guard exists to prevent.
func IsNilDependency(value any) bool {
	if value == nil {
		return true
	}
	rv := reflect.ValueOf(value)
	switch rv.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return rv.IsNil()
	default:
		return false
	}
}
