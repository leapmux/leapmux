import process from 'node:process'

import { defineConfig, devices } from '@playwright/test'

export default defineConfig({
  testDir: './tests/e2e',
  // `.spec.ts` only. Playwright's default also collects `*.test.ts`, which
  // would take the vitest unit tests co-located with the helpers under
  // `helpers/` and run them here -- inside a browser worker, with a whole
  // `leapmux dev` instance behind them -- instead of in the unit suite.
  // `vitest.config.ts` pins the mirror half of the rule, and
  // `src/test-support/testFileNaming.test.ts` fails the suite when a file
  // lands on the wrong side.
  testMatch: '**/*.spec.ts',
  globalSetup: './tests/e2e/global-setup.ts',
  globalTeardown: './tests/e2e/global-teardown.ts',
  // Each worker owns a whole `leapmux dev` instance (hub + worker + SQLite) and
  // a browser, and every agent spec drives a real Claude CLI, so the suite is
  // dominated by waiting on network and subprocesses rather than by CPU: the
  // full run measured 105% CPU across two workers on a ten-core machine, i.e.
  // ~90% of the box idle.
  //
  // Eight, and the number was found by walking it up and comparing FAILURE SETS
  // rather than by reasoning about the machine, because what bounds this is not
  // CPU. Measured over the Claude + non-agent subset (303 tests):
  //
  //   workers  wall     passed  failed
  //   2        30m28s   280     23
  //   4        19m12s   277     26
  //   6        15m57s   280     23
  //   8        11m45s   275     28   <- before any of the fixes
  //   8        13m24s   294      9   <- after the shared-cause fixes
  //   8        12m18s   300      3   <- after the per-spec fixes
  //
  // Load never created a failure here; it widened windows that were already
  // open. Every one traced to a real defect, in the app or in the spec:
  //
  //   - ChatView keeps a hidden premeasure COPY of each unmeasured row, so any
  //     page-rooted chat locator transiently matched twice and died on strict
  //     mode. Helpers in helpers/ui.ts are `:visible`-scoped now, and
  //     src/test-support/visibleChatLocators.test.ts keeps them that way.
  //   - The sidebar rendered one row per section ITEM, so a workspace with a
  //     transient duplicate item produced two nodes with the same test id.
  //   - The git filter tabs matched a file through any ancestor in the visible
  //     set -- including the repo root -- so "Changed" listed unchanged files.
  //   - getBrowserPref/setInitialBrowserPref bypassed the storage wrapper, so
  //     preference-driven specs silently asserted against defaults.
  //   - openSettingsMenu treated a menu mid-CLOSE as open, and openAgentViaUI
  //     clicked before the tab context resolved (a one-shot bail to a dialog).
  //   - The static Claude catalog disagreed with the CLI about Sonnet's effort
  //     tiers, so the effort menu grew options after the first settings change.
  //
  // The residual three-to-five per run were chased the same way, by reproducing
  // each under `--repeat-each=8 --workers=8` until it was near-deterministic
  // (071 failed 7 of 8, 061 five of sixteen) and reading the captured worker
  // log rather than the locator that timed out. All were product defects:
  //
  //   - A CloseAgent/CloseTerminal that landed while its startup goroutine was
  //     still running ran `git worktree remove --force` + `git branch -D`
  //     unconditionally, ignoring the WorktreeAction the close carried. Picking
  //     "Close anyway" on the last-tab dialog therefore DELETED the worktree
  //     the user had just asked to keep, uncommitted work included, whenever
  //     the agent happened to still be starting -- and silently, since that
  //     path only logs on failure. The close's decision is now carried to the
  //     startup goroutine as a closeWorktreeDisposition.
  //   - Client.Send only ENQUEUES onto the worker's send queue, and graceful
  //     shutdown tore the connection down straight after broadcasting the
  //     terminal "[Worker disconnected]" notice. Under load the drain lost that
  //     race and the frames were discarded with no error anywhere.
  //     sendq.Writer.Flush now bounds the teardown behind the drain.
  //   - The orphan reconciler's pass is triggered on reconnect -- exactly when
  //     the hub RPC is most likely to hit a channel that has not settled -- and
  //     a failed pass returned without rescheduling, so nothing converged until
  //     the next hourly tick. It now retries on a capped backoff.
  //   - The sidebar prop bag read every reactive source eagerly, so the whole
  //     sidebar was re-CREATED rather than updated on each tab-context / todo /
  //     worker change, detaching whatever menu was open under the pointer.
  //   - The worker's read-only `git status` probes took `.git/index.lock` (git
  //     refreshes the index as a side effect), so a status poll landing on the
  //     same repo as a checkout killed one of them with "Another git process
  //     seems to be running". It surfaced as an agent that failed to start
  //     mid-checkout, whose rollback then put the user back on their original
  //     branch. Every git command now runs with GIT_OPTIONAL_LOCKS=0.
  //
  // A later review pass found four more of the same shape, three of them in the
  // fixes above rather than in what they fixed:
  //
  //   - The worktree fix covered only the branch that DETECTS the close via
  //     closed_at. A close cancels the startup before it writes that column, so
  //     the cancellation usually surfaces as a startup FAILURE instead -- and
  //     that path still force-removed the worktree. The disposition is consulted
  //     on both endings now.
  //   - Shutdown broadcasts the disconnect notice twice, and its idempotency
  //     check was "does the screen end with the notice". A terminal still
  //     producing output between the two passes defeats that, so a running build
  //     got the notice twice. The sweep tracks ids instead.
  //   - That same sweep's terminal upsert left closed_at unbound, so a close
  //     landing beside it was overwritten with NULL and the tab came back live.
  //   - The orphan reconciler's retry armed only on a HUB failure. A local
  //     SQLite read that errored reported "idle, nothing to do", so the pass
  //     most in need of a retry never got one.
  //
  // A failing test attaches its server's output for its own window -- the hub
  // and worker run out of process, and without it a worker-side failure arrives
  // as a timeout on an unrelated locator. Both fixture files do it, over one
  // ring buffer (helpers/serverOutput.ts): the dev instance in fixtures.ts, and
  // the separate hub+worker in process-control-fixtures.ts, which labels the
  // two processes apart and spans the restarts its own specs drive.
  //
  // fullyParallel stays OFF. Turning it on schedules per test instead of per
  // file, which stops one heavy file from pinning a worker while the others
  // drain. Measured back to back on the same box, BEFORE the product fixes
  // listed above landed:
  //
  //   fullyParallel  wall    passed  failed
  //   false          10m36s  300     3
  //   true            8m00s  295     8
  //
  // 25% is a real saving and the speed is genuinely there -- the suite sums to
  // ~50 min of test time and only reaches 4.7x of the 8 workers with per-file
  // scheduling. It was off because reliability is the constraint set for this
  // suite, and interleaving tests from different files made the sidebar-remount
  // race worse specifically: both 065 context-menu tests failed under it and
  // neither did without it.
  //
  // THAT MEASUREMENT IS NOW STALE, and deliberately left here rather than
  // quietly deleted. Its stated blocker -- the sidebar being rebuilt whenever
  // the active tab context changes -- is the prop-bag remount listed as FIXED
  // above, and the `false` row's 3 failures are the pre-fix baseline, not the
  // current one. So the table no longer describes a trade-off anyone has
  // measured: it describes one defect being weighed against another that is
  // gone.
  //
  // It stays off until someone re-runs the A/B, because "unmeasured" is a
  // reason to keep the conservative setting, not a reason to claim the old
  // number. Re-run both sides back to back before changing this:
  //
  //   bun run test:e2e "tests/e2e/(0[0-9][0-9]|1[4-6][0-9])-"
  //
  // and note that 036's popover-drag test is independently flaky right now
  // (it closed the popover mid-drag in two consecutive runs on an otherwise
  // green suite), so it has to be excluded or fixed first or it will show up
  // as scheduling noise on whichever side it lands.
  fullyParallel: false,
  // Six, and E2E_WORKERS overrides it for one run without editing this file.
  //
  // Read the table above with its dates in mind: the 2, 4 and 6 rows are all
  // PRE-fix, and only the 8 row was measured again afterwards. So the table
  // does NOT say that 6 costs failures today -- it says nobody has measured 6
  // since the defects were corrected. What it does bound is the wall-clock
  // cost, at roughly a quarter more than 8.
  //
  // What the table DOES establish is the direction to read a failure from:
  // walking 2 -> 8 did not change the failure COUNT before the fixes, and
  // every failure traced to a real defect in the app or in the spec. So a
  // lower count is a debugging tool, never a fix. A spec that passes at 2 and
  // fails at 8 has an open window, and the window is the bug.
  workers: Number(process.env.E2E_WORKERS) || 6,
  // No retries, and that is the policy rather than the default. Every failure
  // this suite has produced under load traced to a real defect -- a worktree
  // force-removed behind the user's back, a disconnect notice dropped by an
  // undrained send queue, a reconnect whose reconciliation pass was thrown
  // away, a sidebar remounting out from under an open menu. A blanket retry
  // would have turned each of those into a green run and shipped the bug.
  //
  // The one category retries genuinely fit -- an assertion on what a language
  // model chose to emit -- opts in per-describe via
  // MODEL_NONDETERMINISM_RETRIES (tests/e2e/helpers/modelRetries.ts), so the
  // exceptions are greppable from one place instead of being the default
  // everywhere.
  //
  // Note how the two timeouts below interact with the `toPass` loops in
  // helpers/ui.ts, because it is not the obvious way round: a bare `toPass()`
  // inherits NO timeout and runs to the 300s test budget, while the assertions
  // INSIDE it inherit `expect.timeout`. An unbounded loop wrapped around a 120s
  // assertion therefore retries at most twice before the test dies, reporting a
  // bare "Test timeout" with no named assertion. Those loops carry explicit
  // budgets on both levels for that reason.
  retries: 0,
  // 300s per test, 120s per assertion.
  //
  // Both were raised when the per-call `{ timeout: ... }` overrides were removed
  // from the specs (the repo forbids them). The slowest legitimate waits are
  // real ones -- agent startup and a full LLM turn -- and they had been carrying
  // 60s/120s overrides, so the global has to cover the slowest of those or those
  // specs simply flake. The test timeout keeps its ~2.5x headroom over a single
  // assertion so one slow expect cannot eat the whole test budget.
  //
  // The cost is honest and deliberate: a genuinely broken assertion now takes
  // 120s to report instead of 30s. That is the price of having one number
  // instead of 63 scattered ones.
  timeout: 300_000,
  expect: {
    timeout: 120_000,
  },
  use: {
    actionTimeout: 30_000,
    trace: 'retain-on-failure',
    permissions: ['clipboard-read', 'clipboard-write'],
  },
  projects: [
    {
      name: 'chromium',
      use: { ...devices['Desktop Chrome'] },
    },
  ],
})
