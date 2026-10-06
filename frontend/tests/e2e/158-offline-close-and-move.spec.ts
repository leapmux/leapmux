import { openAgentViaAPI } from './helpers/api'
import { withCleanup } from './helpers/cleanup'
import { boxCenter, mouseDragOnto } from './helpers/drag'
import { nativeAgentsByIds } from './helpers/nativeScenario'
import { tabbarLabels } from './helpers/tabLabels'
import { clearRecordedToasts, expectToastRecorded } from './helpers/toast'
import { expectAgentTabCount, loginViaToken, openWorkspace, sidebarLeafIds, tabById, waitForWorkspaceReady, workspaceChevron, workspaceRow, workspaceRowTitle } from './helpers/ui'
import { withTestWorkspace } from './helpers/workspace'
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

test.describe('Offline close and cross-workspace move', () => {
  test('close and move commit with the worker offline; the worker reaps on reconnect', async ({ separateHubWorker, page }) => {
    await ensureWorkerOnline(separateHubWorker)
    const { hubUrl, adminToken, workerId } = separateHubWorker
    // The open agents of `agentIds`, as the Worker lists them. ListAgents excludes a row with `closed_at` set, and
    // reconcileAgents stops the process and marks the row closed in one step, so this read shows the reap.
    // A failed read throws, and `expect.poll` reads again while a restarted Worker reconnects.
    const openAgentIds = async (agentIds: string[]) =>
      (await nativeAgentsByIds({ leapmuxServer: separateHubWorker }, agentIds)).map(agent => agent.id).sort()

    // The separate hub has no per-test reset, so each workspace is deleted after the test.
    await withTestWorkspace(separateHubWorker, 'offline-source', async ({ workspaceId: wsA }) => {
      await withTestWorkspace(separateHubWorker, 'offline-target', async ({ workspaceId: wsB }) => {
        const closeTitle = 'Close Offline'
        const moveTitle = 'Move Offline'
        // Titles are opt-in on the API path (it defaults to ""). Non-empty ones
        // let the pre-offline check prove the Worker had really hydrated both tabs
        // before we killed it.
        const closedAgentId = await openAgentViaAPI(hubUrl, adminToken, workerId, wsA, undefined, { title: closeTitle })
        const movedAgentId = await openAgentViaAPI(hubUrl, adminToken, workerId, wsA, undefined, { title: moveTitle })

        // `separateHubWorker` is worker-SCOPED: later specs in the same worker
        // reuse it. This spec deliberately kills it mid-test, so the cleanup
        // brings it back after a failure between the stop and the restart below.
        // Otherwise every later spec would fail for an unrelated reason, masking
        // the real one. `ensureWorkerOnline` restarts the Worker only when the
        // hub does not list it as online, and the cleanup runs before the
        // workspace deletes, which close the Worker's tabs.
        await withCleanup(async () => {
          await loginViaToken(page, adminToken)
          await openWorkspace(page, wsA)
          await expectAgentTabCount(page, 2)
          // Poll: titles are Worker-side metadata fetched after the tab itself
          // renders from the CRDT projection, so a one-shot read races the fetch.
          await expect.poll(async () => (await tabbarLabels(page, 'agent')).sort())
            .toEqual([closeTitle, moveTitle].sort())

          // Both agents are live on the Worker before we kill it -- otherwise the
          // post-restart assertion could pass for the trivial reason that nothing
          // was ever there.
          expect(await openAgentIds([closedAgentId, movedAgentId]))
            .toEqual([closedAgentId, movedAgentId].sort())

          // ─── Take the Worker offline ────────────────────────────────────────
          await stopWorker(separateHubWorker)
          await waitForWorkerOffline(separateHubWorker)

          // ─── 1. Close a tab with the Worker offline ─────────────────────────
          //
          // The inspect RPC that normally decides whether to prompt cannot be
          // answered, so `handleTabClose` takes its unreachable-worker branch:
          // no dialog, an info toast, and the CRDT tombstone still commits.
          const closingTab = tabById(page, closedAgentId)
          await expect(closingTab).toBeVisible()
          // The recorder of the processTest page fixture keeps each toast after its
          // 3s display. Clear it, so only a toast of this close can match below.
          await clearRecordedToasts(page)
          await closingTab.locator('[data-testid="tab-close"]').dispatchEvent('click')

          await expectAgentTabCount(page, 1)
          // The toast is what distinguishes "took the unreachable branch" from
          // "the close somehow reached the worker" -- both end with one tab left.
          await expectToastRecorded(page, 'Worker is unreachable')

          // ─── 2. Move the surviving tab to another workspace, still offline ──
          // The tab leaves the page with the move, so the drag checks no dragging
          // class after the release.
          await mouseDragOnto(page, {
            from: await boxCenter(tabById(page, movedAgentId)),
            to: await boxCenter(workspaceRow(page, wsB)),
          })

          // wsA is left empty and wsB gained the tab, without a Worker round-trip.
          await expectAgentTabCount(page, 0)
          await workspaceRowTitle(page, wsB).click()
          await waitForWorkspaceReady(page)
          await expectAgentTabCount(page, 1)

          // ─── 3. Both edits are durable, not just optimistic UI ──────────────
          //
          // Reloading re-reads the layout from the hub's CRDT. If either gesture
          // had needed the Worker to commit, the tab would come back to wsA (move)
          // or reappear entirely (close).
          await page.reload()
          await waitForWorkspaceReady(page)
          await expectAgentTabCount(page, 1)
          await expect.poll(() => sidebarLeafIds(page, wsB)).toEqual([movedAgentId])
          // Expand wsA so its (now empty) section mounts.
          await workspaceChevron(page, wsA).click()
          await expect.poll(() => sidebarLeafIds(page, wsA)).toEqual([])

          // ─── 4. The Worker converges on reconnect ───────────────────────────
          //
          // `bootstrap.Wire` triggers the orphan reconciler as soon as the Worker
          // reconnects, so the closed agent is stopped and tombstoned without
          // waiting out the hourly interval. The moved agent must survive: its
          // hub-side ownership row never changed, only the tile it hangs off.
          await restartWorker(separateHubWorker)
          await expect.poll(() => openAgentIds([closedAgentId, movedAgentId]))
            .toEqual([movedAgentId])
        }, () => ensureWorkerOnline(separateHubWorker))
      })
    })
  })
})
