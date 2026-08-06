package service

import "context"

// ReconcileOnceForTest runs exactly one reconciliation pass and reports whether
// it converged.
//
// The external test package can only reach the reconciler through Run, whose
// pass is bounded by the caller's context -- so a test driving "one pass" had to
// start Run on a goroutine, sleep long enough for the pass to finish, and then
// cancel. That sleep is a window sized by the machine rather than by the state
// it is waiting for: under -race at full package parallelism the pass is still
// mid-flight when the cancel lands, every ctx-aware DB write in it is abandoned,
// and the test fails claiming the reconciler did not close a stale tab.
//
// Exposing the pass itself removes the window instead of widening it. Test-only,
// so the production type keeps offering exactly one way to run: the loop.
func (r *OrphanReconciler) ReconcileOnceForTest(ctx context.Context) bool {
	return r.reconcileOnce(ctx)
}
