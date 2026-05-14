/**
 * `leapmux remote` end-to-end coverage for cross-worker workspaces.
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
 * `LEAPMUX_REMOTE_WORKER_ID` env var fails with a clear error pointing
 * the user at the recovery action.
 */

import type { Browser, Page } from '@playwright/test'
import type { CLIConfigDir } from './helpers/cli'
import type { MultiWorkerHarness } from './helpers/multiWorker'
import { test as base, expect } from '@playwright/test'
import { authedHeaders } from './helpers/api'
import { cliAgentOpen, CLIError, mintCLITokenForAdmin, runCLI, waitForAgentTabs } from './helpers/cli'
import { startMultiWorkerHarness } from './helpers/multiWorker'
import { installToastRecorder } from './helpers/toast'
import { loginViaToken, tabById } from './helpers/ui'

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
    // The CLI's credential file uses the hub URL as its lookup key,
    // and the harness's hub data dir is what `admin api-token issue`
    // opens. Pass both via the focused token-source interface.
    const cli = await mintCLITokenForAdmin({
      hubUrl: harness.hubUrl,
      adminToken: harness.adminToken,
      // Standalone `leapmux hub` doesn't split the data dir the way
      // `dev` mode does (`<root>/hub` + `<root>/worker`); the hub
      // opens hubDataDir verbatim.
      dataDir: harness.hubDataDir,
    })
    try {
      await use({ harness, cli })
    }
    finally {
      await harness.stop()
    }
  }, { scope: 'worker' }],

  crossWorker: async ({ crossWorkerHarness }, use) => {
    await use(crossWorkerHarness)
  },
})

/** POST a JSON-bodied RPC against the harness hub. */
async function hubPost<T>(harness: MultiWorkerHarness, path: string, body: unknown): Promise<T> {
  const res = await fetch(`${harness.hubUrl}${path}`, {
    method: 'POST',
    headers: authedHeaders(harness.adminToken),
    body: JSON.stringify(body),
  })
  if (!res.ok)
    throw new Error(`${path}: ${res.status} ${await res.text()}`)
  return res.json() as Promise<T>
}

/** Authed helpers for the harness hub. */
async function createWorkspace(harness: MultiWorkerHarness, title: string): Promise<string> {
  // Warm the org-events subscription BEFORE issuing CreateWorkspace
  // so the lifecycle-broadcast of the seed `SetWorkspaceRootNode` op
  // lands on it. `seedTabIntoWorkspace` will read `rootNodeId` from
  // this subscription's state later. Mirrors what
  // `createWorkspaceViaAPI` does for the single-worker spec.
  const { getOrgEventsSubscription } = await import('./helpers/crdt')
  await getOrgEventsSubscription(harness.hubUrl, harness.adminToken, harness.adminOrgId)
  const data = await hubPost<{ workspaceId?: string, workspace?: { id?: string } }>(harness, '/leapmux.v1.WorkspaceService/CreateWorkspace', {
    title,
    orgId: harness.adminOrgId,
  })
  const id = data.workspaceId ?? data.workspace?.id
  if (!id)
    throw new Error('createWorkspace: missing id')
  return id
}

async function deleteWorkspace(harness: MultiWorkerHarness, workspaceId: string): Promise<void> {
  await hubPost(harness, '/leapmux.v1.WorkspaceService/DeleteWorkspace', { workspaceId })
}

/**
 * `openAgentViaAPI` from helpers/api.ts caches a ChannelManager per
 * (hubUrl, cookie) — perfect for a single-process dev hub but
 * unhelpful here because the spec's hub url is the harness's, not
 * the cached one. We mint per-call channels via the same primitives
 * the helper uses, just without the global cache.
 */
async function openAgent(harness: MultiWorkerHarness, workerId: string, workspaceId: string): Promise<string> {
  const { OpenAgentRequestSchema, OpenAgentResponseSchema } = await import('../../src/generated/leapmux/v1/agent_pb')
  const { createTestChannelManager } = await import('./helpers/e2e-channel')
  const { TabType } = await import('../../src/generated/leapmux/v1/workspace_pb')
  const { getOrgEventsSubscription, seedTabIntoWorkspace } = await import('./helpers/crdt')
  const channel = await createTestChannelManager(harness.hubUrl, harness.adminToken)

  // Every cross-worker channel needs the workspace marked
  // accessible on the worker side before the workspace-scoped RPC
  // lands; this matches what `helpers/api.ts:openAgentViaAPI` does.
  await hubPost(harness, '/leapmux.v1.ChannelService/PrepareWorkspaceAccess', { workerId, workspaceId })

  const resp = await channel.callWorker(workerId, 'OpenAgent', OpenAgentRequestSchema, OpenAgentResponseSchema, {
    workspaceId,
    workerId,
    workingDir: '',
  })
  if (!resp.agent)
    throw new Error('openAgent: no agent in response')

  // Seed the tab into the CRDT so the live frontend renders it.
  // Mirrors `openAgentViaAPI` from helpers/api.ts: SetTabRegister
  // tile_id + position + worker_id in one batch.
  const orgEvents = await getOrgEventsSubscription(harness.hubUrl, harness.adminToken, harness.adminOrgId)
  await seedTabIntoWorkspace({
    hubUrl: harness.hubUrl,
    cookie: harness.adminToken,
    orgId: harness.adminOrgId,
    workspaceId,
    tabType: TabType.AGENT,
    tabId: resp.agent.id,
    workerId,
    orgEvents,
  })
  return resp.agent.id
}

/** Open two browser pages logged in as admin against the harness hub. */
async function openTwoBrowsers(browser: Browser, harness: MultiWorkerHarness): Promise<{ pageA: Page, pageB: Page, close: () => Promise<void> }> {
  const ctxA = await browser.newContext({ baseURL: harness.hubUrl })
  const ctxB = await browser.newContext({ baseURL: harness.hubUrl })
  const pageA = await ctxA.newPage()
  const pageB = await ctxB.newPage()
  await installToastRecorder(pageA)
  await installToastRecorder(pageB)
  await loginViaToken(pageA, harness.adminToken)
  await loginViaToken(pageB, harness.adminToken)
  const close = async () => {
    await Promise.all([ctxA.close(), ctxB.close()])
  }
  return { pageA, pageB, close }
}

test.describe('remote CLI cross-worker', () => {
  test('CLI agent-open on Worker B propagates to both browsers', async ({ browser, crossWorker }) => {
    const { harness, cli } = crossWorker
    const [workerA, workerB] = harness.workers

    const workspaceId = await createWorkspace(harness, `xw-${Date.now()}`)
    let pages: Awaited<ReturnType<typeof openTwoBrowsers>> | null = null
    try {
      // Seed one agent on Worker A so the workspace renders
      // something initially. The interesting tab — the one we'll
      // open via the CLI — lives on Worker B.
      const agentA = await openAgent(harness, workerA.id, workspaceId)

      pages = await openTwoBrowsers(browser, harness)
      await Promise.all([
        pages.pageA.goto(`/o/admin/workspace/${workspaceId}`),
        pages.pageB.goto(`/o/admin/workspace/${workspaceId}`),
      ])
      await Promise.all([waitForAgentTabs(pages.pageA, 1), waitForAgentTabs(pages.pageB, 1)])
      await Promise.all([
        expect(tabById(pages.pageA, agentA)).toBeVisible(),
        expect(tabById(pages.pageB, agentA)).toBeVisible(),
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
        expect(tabById(pages.pageA, agentB)).toBeVisible(),
        expect(tabById(pages.pageB, agentB)).toBeVisible(),
      ])

      // The tab the CLI created really is on Worker B (not A),
      // not just a same-worker tab in disguise. Without this, a
      // regression that pinned everything to a single worker
      // would still pass the broadcast assertion above.
      const tabBInfo = await fetchTab(harness, workspaceId, agentB)
      expect(tabBInfo.workerId).toBe(workerB.id)
      expect(tabBInfo.workerId).not.toBe(workerA.id)
    }
    finally {
      if (pages)
        await pages.close()
      await deleteWorkspace(harness, workspaceId)
    }
  })

  test('tab open without a worker target produces a clear error', async ({ crossWorker }) => {
    const { harness, cli } = crossWorker
    const workspaceId = await createWorkspace(harness, `xw-err-${Date.now()}`)
    try {
      // Drive `tab open --type=agent` with neither --worker-id nor
      // the `LEAPMUX_REMOTE_WORKER_ID` env var. The resolver must
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
        ], { env: { LEAPMUX_REMOTE_WORKER_ID: '' } })
        throw new Error('expected tab open to fail')
      }
      catch (err) {
        if (!(err instanceof CLIError))
          throw err
        expect(err.code).toBe('invalid_request')
        expect(err.message).toMatch(/--worker-id/i)
      }
    }
    finally {
      await deleteWorkspace(harness, workspaceId)
    }
  })
})

/** Fetch a tab via `WorkspaceService.GetTab` and return its worker. */
async function fetchTab(harness: MultiWorkerHarness, workspaceId: string, tabId: string): Promise<{ workerId: string }> {
  const res = await hubPost<{ tab?: { workerId?: string } }>(harness, '/leapmux.v1.WorkspaceService/GetTab', {
    workspaceId,
    tabId,
    tabType: 'TAB_TYPE_AGENT',
  })
  if (!res.tab?.workerId)
    throw new Error(`GetTab: missing tab.workerId in response`)
  return { workerId: res.tab.workerId }
}
