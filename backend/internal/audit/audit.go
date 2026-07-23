// Package audit is the single home for the repo-wide invariants that are
// enforced mechanically rather than by review.
//
// Each invariant here has the same shape: an authorization rule that a reader
// cannot verify locally, paired with a table of the sites allowed to look like
// they break it. TestRepoInvariants walks the whole repository's source and
// fails on anything unregistered, so forgetting is a build failure rather than
// a silent hole. The tables are the documentation; the test is the enforcement.
//
// Why one package rather than one per rule: every one of these rules is about
// what an author may write ANYWHERE, and a per-package test can only see its own
// directory. Two of these rules previously lived in internal/hub/service and
// were blind to internal/hub/notifier and internal/hub/store -- both of which
// turn out to contain sites the rules care about. Walking once, centrally, is
// what makes "repo-wide" true instead of aspirational.
//
// One sibling invariant deliberately lives elsewhere:
// internal/hub/auth's TestZeroUserIDDenies / TestEveryUserIDCarryingFuncIsClassified.
// Its table does not merely list sites -- it drives a table-driven test whose
// cases seed real orgs, blank-owner rows, and control assertions against a live
// store. Moving the list here would separate it from the fixtures that prove
// each entry actually denies, which is the only thing that makes that net worth
// having. A registry of names belongs here; a registry of names plus the
// evidence for each belongs with the evidence.
package audit
