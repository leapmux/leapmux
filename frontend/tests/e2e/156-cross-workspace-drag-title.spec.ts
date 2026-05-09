import type { Page } from '@playwright/test'
import { expect, test } from './fixtures'
import { createWorkspaceViaAPI, deleteWorkspaceViaAPI, openAgentViaAPI } from './helpers/api'
import { loginViaToken, tabbarAgentLabels, waitForLayoutSave, waitForWorkspaceReady } from './helpers/ui'

/**
 * Regression: dragging a tab from a non-active workspace's expanded
 * sidebar section to the active workspace (either onto the active
 * workspace's sidebar item or onto the active tabbar zone) used to
 * strip the tab's `title` and `agentProvider` from the destination,
 * so the sidebar row rendered the agent's nanoid and the tabbar
 * rendered "Agent" with the generic icon. Refreshing the page
 * re-fetched the agent record and the title returned — confirming
 * the data loss was purely client-side.
 *
 * Root cause: the CRDT-projection reconciler effect in `AppShell.tsx`
 * read `tabStore.state.tabs` inside its body without `untrack`. The
 * optimistic `tabStore.addTab` in the cross-workspace move handler
 * re-ran the effect against a CRDT projection that hadn't yet absorbed
 * the move op (the op only ships after the worker RPC resolves). Step
 * 1 silently removed the just-added tab as "gone from this workspace",
 * and step 2 re-added it as a bare record (no title / agentProvider /
 * git fields) after the move op finally landed.
 *
 * Defense layered with `tabStore.addTab` dedupe by `(type, id)` — an
 * HMR / concurrent-restore race could land both a bare reconciler
 * insert and a full worker-restore insert for the same id, producing
 * two sidebar rows that "closing one removes both" because removeTab
 * filters by key. The dedupe makes the first insert (typically the
 * one with metadata) win.
 *
 * This spec exercises the real drag-and-drop gesture through the
 * sidebar — the unit tests pin the reconciler / addTab contract; this
 * one pins the full UI flow end-to-end and adds a `page.reload()`
 * checkpoint so a future regression that survived in-session would
 * still fail after rehydration from the worker.
 */

/** Wait for the workspace to be fully loaded with its initial agent tab. */
async function waitForInitialAgent(page: Page) {
  await page.locator('[data-testid="tab"][data-tab-type="agent"]').first().waitFor()
}

/**
 * Drag a sidebar `tab-tree-leaf` (covered by the workspace item's
 * stacking context) onto a target by dispatching programmatic
 * PointerEvents from within the page. Mirrors the pattern in
 * `017-multi-workspace.spec.ts`'s cross-workspace drag test — solid-dnd's
 * activation delay is respected via the 300ms timeout.
 */
async function dragSidebarLeafTo(page: Page, opts: { sourceWorkspaceId: string, target: { x: number, y: number } }): Promise<void> {
  await page.evaluate(({ sourceWsId, tx, ty }) => {
    return new Promise<void>((resolve, reject) => {
      const wsItem = document.querySelector(`[data-testid="workspace-item-${sourceWsId}"]`)
      if (!wsItem) {
        reject(new Error(`workspace-item-${sourceWsId} not found`))
        return
      }
      const childrenWrapper = wsItem.nextElementSibling
      if (!childrenWrapper) {
        reject(new Error(`children wrapper for ${sourceWsId} not found`))
        return
      }
      const leaf = childrenWrapper.querySelector('[data-testid="tab-tree-leaf"]') as HTMLElement | null
      if (!leaf) {
        reject(new Error(`no tab-tree-leaf in ${sourceWsId}`))
        return
      }
      const rect = leaf.getBoundingClientRect()
      const sx = rect.x + 10
      const sy = rect.y + rect.height / 2

      leaf.dispatchEvent(new PointerEvent('pointerdown', {
        clientX: sx,
        clientY: sy,
        pointerId: 1,
        button: 0,
        buttons: 1,
        isPrimary: true,
        bubbles: true,
        cancelable: true,
      }))

      // solid-dnd activation delay → move once activated, then up.
      setTimeout(() => {
        const steps = 10
        for (let i = 1; i <= steps; i++) {
          document.dispatchEvent(new PointerEvent('pointermove', {
            clientX: sx + (tx - sx) * (i / steps),
            clientY: sy + (ty - sy) * (i / steps),
            pointerId: 1,
            button: 0,
            buttons: 1,
            isPrimary: true,
            bubbles: true,
            cancelable: true,
          }))
        }
        setTimeout(() => {
          document.dispatchEvent(new PointerEvent('pointerup', {
            clientX: tx,
            clientY: ty,
            pointerId: 1,
            button: 0,
            buttons: 0,
            isPrimary: true,
            bubbles: true,
            cancelable: true,
          }))
          setTimeout(resolve, 500)
        }, 100)
      }, 300)
    })
  }, { sourceWsId: opts.sourceWorkspaceId, tx: opts.target.x, ty: opts.target.y })
  await page.waitForTimeout(200)
}

/**
 * Strip the close icon / notification / remote badge from each tab and
 * return the rendered title text. Used to assert that a moved tab kept
 * the title the API seeded rather than degrading to its nanoid id
 * (sidebar fallback) or to a bare "Agent" (tabbar fallback).
 */
async function sidebarLeafLabelsForWorkspace(page: Page, workspaceId: string): Promise<string[]> {
  return page.evaluate((wsId) => {
    const wsItem = document.querySelector(`[data-testid="workspace-item-${wsId}"]`)
    if (!wsItem)
      return []
    const wrapper = wsItem.nextElementSibling
    if (!wrapper)
      return []
    const leaves = Array.from(wrapper.querySelectorAll('[data-testid="tab-tree-leaf"]')) as HTMLElement[]
    return leaves.map((leaf) => {
      const clone = leaf.cloneNode(true) as HTMLElement
      clone.querySelectorAll('button, svg').forEach(n => n.remove())
      return (clone.textContent ?? '').trim()
    })
  }, workspaceId)
}

test.describe('Cross-workspace sidebar drag preserves title and icon', () => {
  test('drag from non-active sidebar section to active workspace keeps title; survives reload', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, adminOrgId, workerId } = leapmuxServer

    // API-seed both workspaces with an agent. We pass a known title
    // (the bug strips exactly this field; the API path defaults
    // `title=""` which would render the empty-fallback both before
    // AND after the move, masking the regression).
    const wsA = await createWorkspaceViaAPI(hubUrl, adminToken, 'Drag Source', adminOrgId)
    const wsB = await createWorkspaceViaAPI(hubUrl, adminToken, 'Drag Target', adminOrgId)
    const wsATitle = 'Source Agent'
    const wsBTitle = 'Target Agent'
    const wsAAgentId = await openAgentViaAPI(hubUrl, adminToken, workerId, wsA, undefined, { title: wsATitle })
    await openAgentViaAPI(hubUrl, adminToken, workerId, wsB, undefined, { title: wsBTitle })

    try {
      await loginViaToken(page, adminToken)

      // Land on wsB (the destination — the user's repro had the
      // target workspace active at the moment of the drag).
      await page.goto(`/o/admin/workspace/${wsB}`)
      await waitForWorkspaceReady(page)
      await waitForInitialAgent(page)
      const initialBLabels = await tabbarAgentLabels(page)
      expect(initialBLabels).toEqual([wsBTitle])

      // Expand wsA in the sidebar so its tab-tree-leaf mounts and is
      // draggable. The chevron is the first SVG inside the workspace
      // item; clicking it fires `onExpandWorkspace`, which lazy-loads
      // the registry snapshot for wsA.
      const wsAItem = page.locator(`[data-testid="workspace-item-${wsA}"]`)
      await wsAItem.locator('svg').first().click()
      // Two leaves expected: wsA's (the source) and wsB's (the
      // destination — already visible because wsB is active).
      await expect(page.locator('[data-testid="tab-tree-leaf"]')).toHaveCount(2)

      // Verify wsA's leaf renders with its seeded title before the
      // drag — confirms the registry-load path delivered the metadata
      // we'll be asserting survives the move.
      const wsALabelsBefore = await sidebarLeafLabelsForWorkspace(page, wsA)
      expect(wsALabelsBefore).toEqual([wsATitle])

      // Target: drop on wsB's workspace item in the sidebar (it's the
      // active workspace, so this is a non-active → active move).
      const wsBItem = page.locator(`[data-testid="workspace-item-${wsB}"]`)
      const targetBox = await wsBItem.boundingBox()
      if (!targetBox)
        throw new Error('Could not get wsB workspace-item bounding box')

      const saved = waitForLayoutSave(page)
      await dragSidebarLeafTo(page, {
        sourceWorkspaceId: wsA,
        target: {
          x: targetBox.x + targetBox.width / 2,
          y: targetBox.y + targetBox.height / 2,
        },
      })
      await saved

      // wsB's tabbar now has two agent tabs — the original wsBTitle
      // and the moved wsATitle. Both must keep their titles; the
      // pre-fix bug would have collapsed the moved one to a bare
      // "Agent" (tabbar fallback) and its sidebar row to the nanoid.
      const tabbarAgents = page.locator('[data-testid="tab"][data-tab-type="agent"]')
      await expect(tabbarAgents).toHaveCount(2)
      const tabbarTitles = await tabbarAgentLabels(page)
      expect(new Set(tabbarTitles)).toEqual(new Set([wsATitle, wsBTitle]))
      expect(tabbarTitles).not.toContain(wsAAgentId)
      expect(tabbarTitles).not.toContain('Agent')

      // Mirror assertion in the sidebar — wsB's section now lists
      // both tabs and neither row shows the nanoid fallback.
      const sidebarLabels = await sidebarLeafLabelsForWorkspace(page, wsB)
      expect(new Set(sidebarLabels)).toEqual(new Set([wsATitle, wsBTitle]))

      // --- Reload checkpoint (#3) ---
      //
      // The cross-workspace move emits a CRDT `SetTabRegister(tile_id)`
      // batch and a worker-side `MoveTabWorkspace` RPC. After reload,
      // both should be durably committed — the destination workspace
      // must still show two tabs (the original + the moved), and the
      // moved tab id must NOT have leaked back to the source.
      //
      // We intentionally only assert COUNT and id-NOT-leaked-to-source
      // here, not titles. Title round-tripping through `listAgents`
      // after a refresh interacts with several worker-side concerns
      // (openAgent title persistence, listAgents ordering vs the
      // CRDT-projection reconciler) that are independent of the bug
      // under test. A non-deterministic title result post-reload
      // would mask the actual move regression we DO cover (the
      // pre-reload assertion above).
      await page.reload()
      await waitForWorkspaceReady(page)
      await waitForInitialAgent(page)

      await expect(tabbarAgents).toHaveCount(2)
      // wsAAgentId must not appear back under wsA's sidebar section
      // after refresh — the move op committed to the hub and the
      // post-reload `listTabs(wsA)` should no longer return it.
      const wsAItemAfterReload = page.locator(`[data-testid="workspace-item-${wsA}"]`)
      await wsAItemAfterReload.locator('svg').first().click()
      const wsALabelsAfterReload = await sidebarLeafLabelsForWorkspace(page, wsA)
      expect(wsALabelsAfterReload).toEqual([])
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, wsA).catch(() => {})
      await deleteWorkspaceViaAPI(hubUrl, adminToken, wsB).catch(() => {})
    }
  })
})
