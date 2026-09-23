package testutil

import "strings"

// IsTestSupportPackage reports whether the package whose last path element is
// name exists only for tests: testutil, or a name that ends in "test"
// (storetest, agenttest, and the rest).
//
// Two checks read this one rule. TestShippedBinaryLinksNoTestSupport in
// cmd/leapmux keeps these packages out of the shipped binary, and the audit
// rule on test hooks lets them call a hook. A second copy of the rule would let
// the two checks disagree about one package.
//
// The rule reads the name alone, so a production package must not take a name
// that ends in "test". The binary check then fails for it, and the audit rule
// stops checking it.
func IsTestSupportPackage(name string) bool {
	return name == "testutil" || strings.HasSuffix(name, "test")
}
