/**
 * Retry budget for the handful of specs whose failure mode is the MODEL, not
 * the app.
 *
 * The suite runs with `retries: 0` (see playwright.config.ts) on purpose: a
 * retried test hides a race instead of reporting it, and every flake chased
 * down in this suite so far turned out to be a real defect in the app or the
 * spec. Retrying would have buried a worktree being deleted behind the user's
 * back, a disconnect notice dropped by an undrained send queue, and a
 * reconnect that reconciled nothing.
 *
 * What is left after those fixes is a genuinely different category: assertions
 * on what a language model chose to emit. "Reply with just the number" usually
 * produces `7`, and sometimes produces a sentence; a plan turn usually calls
 * ExitPlanMode, and sometimes narrates instead. No amount of waiting settles
 * that, because nothing is still in flight -- the turn finished and said
 * something else. Re-running the turn is the only remedy, and it is the same
 * remedy a human would apply.
 *
 * Apply it with `test.describe.configure({ retries: MODEL_NONDETERMINISM_RETRIES })`
 * on the NARROWEST describe that covers such a test, never at file scope where
 * it would also cover the app-behaviour tests sitting next to it. Every use
 * should be greppable from here.
 */
export const MODEL_NONDETERMINISM_RETRIES = 2
