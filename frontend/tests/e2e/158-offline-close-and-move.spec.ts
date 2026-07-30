import type { Page } from '@playwright/test'
import { ListAgentsRequestSchema, ListAgentsResponseSchema } from '../../src/generated/leapmux/v1/agent_pb'
import { cleanupWorkspaceViaAPI, createWorkspaceViaAPI, deleteWorkspaceViaAPI, getTestChannel, openAgentViaAPI } from './helpers/api'
import { boxOf, loginViaToken, openWorkspace, tabbarAgentLabels, waitForWorkspaceReady } from './helpers/ui'
import { ensureWorkerOnline, expect, restartWorker, stopWorker, processTest as test, waitForWorkerOffline } from './process-control-fixtures'

/**
 * Layout is the CRDT's business, not the Worker's.
 *
 * Since the Worker stopped tracking `workspace_id`, a tab's placement lives
 * only in the hub-side CRDT, so both of the operations that used to need a
 * live Worker RPC are now pure CRDT edits:
 *
 *   - Close: the tombstone is a CRDT op. The Worker RPC that stops the
 *     process is fire-and-forget; when it cannot be delivered the tab still
 *     goes, and the Worker's orphan reconciler reaps the process and the row
 *     on its next pass (triggered on reconnect).
 *   - Cross-workspace move: `SetTabRegister(tile_id in the new workspace)`.
 *     There is no `MoveTabWorkspace` RPC any more -- there is nothing on the
 *     Worker left to update.
 *
 * This spec drives both gestures with the Worker's process killed, then
 * restarts it and asserts the reconciler converged: the closed agent's row is
 * gone from the Worker and the moved agent's row is untouched (over-reaping
 * would be just as wrong as not reaping).
 *
 * Uses the `separateHubWorker` fixture because it is the only one that can
 * stop and restart the Worker independently of the Hub.
 */

/** Wait for the workspace to be fully loaded with its initial agent tabs. */
async function waitForAgentTabs(page: Page, count: number) {
  await expect(page.locator('[data-testid="tab"][data-tab-type="agent"]')).toHaveCount(count)
}

/** Simulate a drag-and-drop from one point to another using mouse events. */
async function dragTo(page: Page, source: { x: number, y: number }, target: { x: number, y: number }) {
  await page.mouse.move(source.x, source.y)
  await page.mouse.down()
  const steps = 10
  for (let i = 1; i <= steps; i++) {
    await page.mouse.move(
      source.x + (target.x - source.x) * (i / steps),
      source.y + (target.y - source.y) * (i / steps),
      { steps: 1 },
    )
    await page.waitForTimeout(30)
  }
  await page.mouse.up()
}

/**
 * Ask the Worker directly which of `tabIds` it still holds an OPEN agent row
 * for. `ListAgents` filters `closed_at IS NULL`, so a reaped agent drops out
 * of the response -- which makes this the observable form of "the reconciler
 * stopped the process and tombstoned the row" (both happen in the same branch
 * of `reconcileAgents`; only the row is visible from out here).
 *
 * Returns `null` while the channel to a just-restarted Worker is still being
 * re-established, so callers can poll instead of racing the reconnect.
 */
async function liveAgentIdsViaAPI(hubUrl: string, token: string, workerId: string, tabIds: string[]): Promise<string[] | null> {
  const channel = await getTestChannel(hubUrl, token)
  try {
    const resp = await channel.callWorker(
      workerId,
      'ListAgents',
      ListAgentsRequestSchema,
      ListAgentsResponseSchema,
      { tabIds },
    )
    return (resp.agents ?? []).map(a => a.id).sort()
  }
  catch {
    return null
  }
}

/**
 * Start accumulating every toast body the page renders into
 * `window.__toastTexts`, so a later assertion is not racing the 3s auto-dismiss.
 *
 * Deliberately not `helpers/toast`'s recorder: that one patches `window.ot.toast`
 * through a `window.ot` setter, and oat installs `toast` onto an already-assigned
 * `window.ot` object, so the patch never lands for `renderToast`'s
 * `window.ot.toast.el(...)` path. Reading the rendered DOM has no such coupling.
 */
async function recordToastTexts(page: Page): Promise<void> {
  await page.evaluate(() => {
    const w = window as unknown as { __toastTexts?: string[], __toastObserver?: MutationObserver }
    w.__toastTexts = []
    const collect = () => {
      for (const node of document.querySelectorAll('.toast-message')) {
        const text = (node.textContent ?? '').trim()
        if (text && !w.__toastTexts!.includes(text))
          w.__toastTexts!.push(text)
      }
    }
    w.__toastObserver?.disconnect()
    w.__toastObserver = new MutationObserver(collect)
    w.__toastObserver.observe(document.body, { childList: true, subtree: true })
    collect()
  })
}

/** Toast bodies seen since the last `recordToastTexts` call. */
async function recordedToastTexts(page: Page): Promise<string[]> {
  return page.evaluate(() => (window as unknown as { __toastTexts?: string[] }).__toastTexts ?? [])
}

/**
 * Tab ids of the sidebar rows nested under `workspaceId`.
 *
 * Ids, not rendered titles: a title is Worker-sourced metadata, so after a
 * reload with the Worker still offline every row falls back to the generic
 * "Agent" label. That fallback is correct behaviour and says nothing about
 * where the tab lives -- which is the only thing this spec is asserting.
 */
async function sidebarLeafIdsForWorkspace(page: Page, workspaceId: string): Promise<string[]> {
  return page.evaluate((wsId) => {
    const wsItem = document.querySelector(`[data-testid="workspace-item-${wsId}"]`)
    if (!wsItem)
      return []
    const wrapper = wsItem.nextElementSibling
    if (!wrapper)
      return []
    const leaves = Array.from(wrapper.querySelectorAll('[data-testid="tab-tree-leaf"]')) as HTMLElement[]
    return leaves.map(leaf => leaf.getAttribute('data-tab-id') ?? '')
  }, workspaceId)
}

test.describe('Offline close and cross-workspace move', () => {
  test('close and move commit with the worker offline; the worker reaps on reconnect', async ({ separateHubWorker, page }) => {
    await ensureWorkerOnline(separateHubWorker)
    const { hubUrl, adminToken, workerId } = separateHubWorker

    const wsA = await createWorkspaceViaAPI(hubUrl, adminToken, 'Offline Source')
    const wsB = await createWorkspaceViaAPI(hubUrl, adminToken, 'Offline Target')
    const closeTitle = 'Close Offline'
    const moveTitle = 'Move Offline'
    // Titles are opt-in on the API path (it defaults to ""). Non-empty ones
    // let the pre-offline check prove the Worker had really hydrated both tabs
    // before we killed it.
    const closedAgentId = await openAgentViaAPI(hubUrl, adminToken, workerId, wsA, undefined, { title: closeTitle })
    const movedAgentId = await openAgentViaAPI(hubUrl, adminToken, workerId, wsA, undefined, { title: moveTitle })

    try {
      await loginViaToken(page, adminToken)
      await openWorkspace(page, wsA)
      await waitForAgentTabs(page, 2)
      // Poll: titles are Worker-side metadata fetched after the tab itself
      // renders from the CRDT projection, so a one-shot read races the fetch.
      await expect.poll(async () => (await tabbarAgentLabels(page)).sort())
        .toEqual([closeTitle, moveTitle].sort())

      // Both agents are live on the Worker before we kill it -- otherwise the
      // post-restart assertion could pass for the trivial reason that nothing
      // was ever there.
      expect(await liveAgentIdsViaAPI(hubUrl, adminToken, workerId, [closedAgentId, movedAgentId]))
        .toEqual([closedAgentId, movedAgentId].sort())

      // ─── Take the Worker offline ────────────────────────────────────────
      await stopWorker()
      await waitForWorkerOffline(hubUrl, adminToken)

      // ─── 1. Close a tab with the Worker offline ─────────────────────────
      //
      // The inspect RPC that normally decides whether to prompt cannot be
      // answered, so `handleTabClose` takes its unreachable-worker branch:
      // no dialog, an info toast, and the CRDT tombstone still commits.
      const closingTab = page.locator(`[data-testid="tab"][data-tab-type="agent"][data-tab-id="${closedAgentId}"]`)
      await expect(closingTab).toBeVisible()
      await recordToastTexts(page)
      await closingTab.locator('[data-testid="tab-close"]').dispatchEvent('click')

      await waitForAgentTabs(page, 1)
      // The toast is what distinguishes "took the unreachable branch" from
      // "the close somehow reached the worker" -- both end with one tab left.
      await expect.poll(async () => (await recordedToastTexts(page)).join('\n'))
        .toContain('Worker is unreachable')

      // ─── 2. Move the surviving tab to another workspace, still offline ──
      const movingTab = page.locator(`[data-testid="tab"][data-tab-type="agent"][data-tab-id="${movedAgentId}"]`)
      const sourceBox = await boxOf(movingTab)
      const targetBox = await boxOf(page.locator(`[data-testid="workspace-item-${wsB}"]`))
      await dragTo(
        page,
        { x: sourceBox.x + sourceBox.width / 2, y: sourceBox.y + sourceBox.height / 2 },
        { x: targetBox.x + targetBox.width / 2, y: targetBox.y + targetBox.height / 2 },
      )

      // wsA is left empty and wsB gained the tab, without a Worker round-trip.
      await waitForAgentTabs(page, 0)
      await page.locator(`[data-testid="workspace-item-${wsB}"]`).click()
      await waitForWorkspaceReady(page)
      await waitForAgentTabs(page, 1)

      // ─── 3. Both edits are durable, not just optimistic UI ──────────────
      //
      // Reloading re-reads the layout from the hub's CRDT. If either gesture
      // had needed the Worker to commit, the tab would come back to wsA (move)
      // or reappear entirely (close).
      await page.reload()
      await waitForWorkspaceReady(page)
      await waitForAgentTabs(page, 1)
      await expect.poll(() => sidebarLeafIdsForWorkspace(page, wsB)).toEqual([movedAgentId])
      // Expand wsA so its (now empty) section mounts.
      await page.locator(`[data-testid="workspace-item-${wsA}"]`).locator('svg').first().click()
      await expect.poll(() => sidebarLeafIdsForWorkspace(page, wsA)).toEqual([])

      // ─── 4. The Worker converges on reconnect ───────────────────────────
      //
      // `bootstrap.Wire` triggers the orphan reconciler as soon as the Worker
      // reconnects, so the closed agent is stopped and tombstoned without
      // waiting out the hourly interval. The moved agent must survive: its
      // hub-side ownership row never changed, only the tile it hangs off.
      await restartWorker(separateHubWorker)
      await expect.poll(() => liveAgentIdsViaAPI(hubUrl, adminToken, workerId, [closedAgentId, movedAgentId]))
        .toEqual([movedAgentId])
    }
    finally {
      // `separateHubWorker` is worker-SCOPED: later specs in the same worker
      // reuse it. This spec deliberately kills it mid-test, so a failure between
      // stopWorker() and the restart above would otherwise leave it dead and
      // every later spec would fail for an unrelated reason, masking the real
      // one. Restarting here is idempotent -- restartWorker is a no-op when the
      // process is already up.
      await restartWorker(separateHubWorker).catch(() => {})
      // Tear the workers' local state down before dropping the hub rows, in the
      // pairing every other separateHubWorker consumer uses: each workspace here
      // holds a live agent, and deleting only the hub row leaves the subprocess
      // running on the shared machine for the rest of the suite.
      for (const ws of [wsA, wsB]) {
        await cleanupWorkspaceViaAPI(hubUrl, adminToken, workerId, ws).catch(() => {})
        await deleteWorkspaceViaAPI(hubUrl, adminToken, ws).catch(() => {})
      }
    }
  })
})
