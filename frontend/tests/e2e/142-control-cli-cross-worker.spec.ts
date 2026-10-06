/**
 * `leapmux control` end-to-end coverage for cross-worker workspaces.
 *
 * The plan calls cross-worker "the normal case" — a workspace whose
 * tabs span more than one worker. Go integration tests use fake
 * Noise_NK responders to exercise the protocol; this spec runs the
 * real handshake against two real worker processes and asserts that
 * the live frontend renders tabs hosted on either worker.
 *
 * The DOM-observable assertions:
 *   - The CLI runs `agent open --worker-id <B>`. The hub publishes a
 *     snapshot; both browsers reconcile their `tabStore` and render
 *     the new tab. `GetTab` against the hub confirms the new tab is
 *     pinned to Worker B (not A) — proving the harness produced a
 *     real cross-worker workspace, not a fake.
 *
 * Active-tab is purely local (sessionStorage) under the CRDT model;
 * there is no `tab focus` CLI and the spec does not exercise remote
 * focus propagation.
 *
 * Negative test: `agent open` with no `--worker-id` and no
 * `LEAPMUX_CONTROL_WORKER_ID` env var fails with a clear error pointing
 * the user at the recovery action.
 */

import type { CLIConfigDir } from './helpers/cli'
import type { MultiWorkerHarness } from './helpers/multiWorker'
import { test as base, expect } from '@playwright/test'
import { callHub, openAgentViaAPI } from './helpers/api'
import { withCleanup } from './helpers/cleanup'
import { cliAgentOpen, CLIError, mintCLITokenForAdmin, runCLI } from './helpers/cli'
import { withExtraClients } from './helpers/multiClient'
import { startMultiWorkerHarness } from './helpers/multiWorker'
import { expectAgentTabCount, loginViaToken, openWorkspace, tabById } from './helpers/ui'
import { withTestWorkspace } from './helpers/workspace'

interface CrossWorkerEnv {
  harness: MultiWorkerHarness
  cli: CLIConfigDir
}

const test = base.extend<{ crossWorker: CrossWorkerEnv }, {
  crossWorkerHarness: { harness: MultiWorkerHarness, cli: CLIConfigDir }
}>({
  // eslint-disable-next-line no-empty-pattern -- Playwright requires first arg to be a destructuring pattern
  crossWorkerHarness: [async ({}, use) => {
    const harness = await startMultiWorkerHarness(2)
    // A failed mint stops the harness too.
    await withCleanup(async () => {
      // The CLI's credential file uses the hub URL as its lookup key.
      const cli = await mintCLITokenForAdmin(harness)
      await use({ harness, cli })
    }, () => harness.stop())
  }, { scope: 'worker' }],

  crossWorker: async ({ crossWorkerHarness }, use) => {
    await use(crossWorkerHarness)
  },
})

test.describe('control CLI cross-worker', () => {
  test('CLI agent-open on Worker B propagates to both browsers', async ({ browser, crossWorker }) => {
    const { harness, cli } = crossWorker
    const [workerA, workerB] = harness.workers
    if (workerA === undefined || workerB === undefined)
      throw new Error('expected the crossWorker harness to expose two workers')

    // The harness hub is private to this file, so no suite reset deletes its
    // workspaces. `withTestWorkspace` deletes this one after the test, and its
    // delete closes the tabs on each Worker.
    await withTestWorkspace(harness, 'xw', async ({ workspaceId }) => {
      // Seed one agent on Worker A so the workspace renders
      // something initially. The interesting tab — the one we'll
      // open via the CLI — lives on Worker B.
      const agentA = await openAgentViaAPI({ hubUrl: harness.hubUrl, adminToken: harness.adminToken, workerId: workerA.id }, workspaceId)

      await withExtraClients(browser, harness, 2, async ([pageA, pageB]) => {
        await loginViaToken(pageA, harness.adminToken)
        await loginViaToken(pageB, harness.adminToken)
        await Promise.all([
          openWorkspace(pageA, workspaceId),
          openWorkspace(pageB, workspaceId),
        ])
        await Promise.all([expectAgentTabCount(pageA, 1), expectAgentTabCount(pageB, 1)])
        await Promise.all([
          expect(tabById(pageA, agentA)).toBeVisible(),
          expect(tabById(pageB, agentA)).toBeVisible(),
        ])

        // 1. CLI-driven `agent open` against Worker B. The hub
        //    publishes a snapshot containing the new tab; both
        //    browsers reconcile their `tabStore` from
        //    `snapshot.tabs` and render it. This proves the entire
        //    cross-worker stack: CLI → hub bearer auth → AddTab on
        //    a tab pinned to a different worker → snapshot fan-out
        //    → frontend reconciler.
        const agentB = await cliAgentOpen(cli, { workspaceId, workerId: workerB.id })
        await Promise.all([
          expect(tabById(pageA, agentB)).toBeVisible(),
          expect(tabById(pageB, agentB)).toBeVisible(),
        ])

        // The tab the CLI created really is on Worker B (not A),
        // not just a same-worker tab in disguise. Without this, a
        // regression that pinned everything to a single worker
        // would still pass the broadcast assertion above.
        const tabBInfo = await fetchTab(harness, workspaceId, agentB)
        expect(tabBInfo.workerId).toBe(workerB.id)
        expect(tabBInfo.workerId).not.toBe(workerA.id)
      })
    })
  })

  test('tab open without a worker target produces a clear error', async ({ crossWorker }) => {
    const { harness, cli } = crossWorker
    await withTestWorkspace(harness, 'xw-err', async ({ workspaceId }) => {
      // Drive `tab open --type=agent` with neither --worker-id nor
      // the `LEAPMUX_CONTROL_WORKER_ID` env var. The resolver must
      // surface a descriptive `invalid_request` envelope listing
      // the unmet ID slot — the message text is loose-matched so
      // a small copy edit doesn't break the assertion.
      try {
        await runCLI(cli, [
          'tab',
          'open',
          '--type',
          'agent',
          '--workspace-id',
          workspaceId,
        ], { env: { LEAPMUX_CONTROL_WORKER_ID: '' } })
        throw new Error('expected tab open to fail')
      }
      catch (err) {
        if (!(err instanceof CLIError))
          throw err
        expect(err.code).toBe('invalid_request')
        expect(err.message).toMatch(/--worker-id/i)
      }
    })
  })
})

/** Fetch a tab via `WorkspaceService.GetTab` and return its worker. */
async function fetchTab(harness: MultiWorkerHarness, workspaceId: string, tabId: string): Promise<{ workerId: string }> {
  const res = await callHub<{ tab?: { workerId?: string } }>(harness.hubUrl, 'WorkspaceService/GetTab', {
    workspaceId,
    tabId,
    tabType: 'TAB_TYPE_AGENT',
  }, { cookie: harness.adminToken, operation: `fetchTab(${tabId})` })
  if (!res.tab?.workerId)
    throw new Error(`GetTab: missing tab.workerId in response`)
  return { workerId: res.tab.workerId }
}
