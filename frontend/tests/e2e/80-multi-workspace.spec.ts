import type { Page } from '@playwright/test'
import { expect, test } from './fixtures'
import { createWorkspaceViaAPI, deleteWorkspaceViaAPI, openAgentViaAPI } from './helpers/api'
import { loginViaToken, waitForLayoutSave, waitForWorkspaceReady } from './helpers/ui'

/** Wait for the workspace to be fully loaded with its initial agent tab. */
async function waitForInitialAgent(page: Page) {
  await page.locator('[data-testid="tab"][data-tab-type="agent"]').first().waitFor({ timeout: 10000 })
}

/** Simulate a drag-and-drop from one element to another using mouse events. */
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
 * Visit a workspace to populate its registry snapshot (tabs, agents, etc.),
 * then switch to another workspace. This is needed because the sidebar's
 * chevron expansion only works for workspaces whose tabs have been loaded.
 */
async function preloadWorkspace(page: Page, workspaceId: string, thenSwitchTo: string) {
  await page.locator(`[data-testid="workspace-item-${workspaceId}"]`).click()
  await waitForWorkspaceReady(page)
  await waitForInitialAgent(page)
  await page.locator(`[data-testid="workspace-item-${thenSwitchTo}"]`).click()
  await waitForWorkspaceReady(page)
  await waitForInitialAgent(page)
}

test.describe('Multi-Workspace', () => {
  test('workspace switch preserves tabs in sidebar', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, adminOrgId, workerId } = leapmuxServer
    const ws1 = await createWorkspaceViaAPI(hubUrl, adminToken, 'Multi WS Alpha', adminOrgId)
    const ws2 = await createWorkspaceViaAPI(hubUrl, adminToken, 'Multi WS Beta', adminOrgId)
    await openAgentViaAPI(hubUrl, adminToken, workerId, ws1)
    await openAgentViaAPI(hubUrl, adminToken, workerId, ws2)

    try {
      await loginViaToken(page, adminToken)
      await page.goto(`/o/admin/workspace/${ws1}`)
      await waitForWorkspaceReady(page)

      // WS Alpha should be active with an agent tab visible
      await waitForInitialAgent(page)
      await expect(page.locator('[data-testid="tab"][data-tab-type="agent"]')).toHaveCount(1)

      // Switch to WS Beta
      await page.locator(`[data-testid="workspace-item-${ws2}"]`).click()
      await waitForWorkspaceReady(page)
      await waitForInitialAgent(page)
      await expect(page.locator('[data-testid="tab"][data-tab-type="agent"]')).toHaveCount(1)

      // Switch back to WS Alpha — tabs should still be there
      await page.locator(`[data-testid="workspace-item-${ws1}"]`).click()
      await waitForWorkspaceReady(page)
      await waitForInitialAgent(page)
      await expect(page.locator('[data-testid="tab"][data-tab-type="agent"]')).toHaveCount(1)
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, ws1).catch(() => {})
      await deleteWorkspaceViaAPI(hubUrl, adminToken, ws2).catch(() => {})
    }
  })

  test('expand non-active workspace shows tab tree', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, adminOrgId, workerId } = leapmuxServer
    const ws1 = await createWorkspaceViaAPI(hubUrl, adminToken, 'TreeView Active', adminOrgId)
    const ws2 = await createWorkspaceViaAPI(hubUrl, adminToken, 'TreeView Inactive', adminOrgId)
    await openAgentViaAPI(hubUrl, adminToken, workerId, ws1)
    await openAgentViaAPI(hubUrl, adminToken, workerId, ws2)

    try {
      await loginViaToken(page, adminToken)
      await page.goto(`/o/admin/workspace/${ws1}`)
      await waitForWorkspaceReady(page)
      await waitForInitialAgent(page)

      // ws2 should be visible in the sidebar but not expanded
      const ws2Item = page.locator(`[data-testid="workspace-item-${ws2}"]`)
      await expect(ws2Item).toBeVisible()

      // Click the chevron on ws2 to expand it — this triggers lazy loading
      // without needing to preload (visit) the workspace first
      await ws2Item.locator('svg').first().click()

      // ws2's tab tree should appear — count should be 2 (ws1 active + ws2 expanded)
      await expect(page.locator('[data-testid="tab-tree-leaf"]')).toHaveCount(2, { timeout: 5000 })
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, ws1).catch(() => {})
      await deleteWorkspaceViaAPI(hubUrl, adminToken, ws2).catch(() => {})
    }
  })

  test('cross-workspace drag: tabbar tab to sidebar workspace', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, adminOrgId, workerId } = leapmuxServer
    const ws1 = await createWorkspaceViaAPI(hubUrl, adminToken, 'Drag Source WS', adminOrgId)
    const ws2 = await createWorkspaceViaAPI(hubUrl, adminToken, 'Drag Target WS', adminOrgId)
    await openAgentViaAPI(hubUrl, adminToken, workerId, ws1)
    await openAgentViaAPI(hubUrl, adminToken, workerId, ws1) // second agent so source keeps a tab
    await openAgentViaAPI(hubUrl, adminToken, workerId, ws2)

    try {
      await loginViaToken(page, adminToken)
      await page.goto(`/o/admin/workspace/${ws1}`)
      await waitForWorkspaceReady(page)

      // ws1 should have 2 agent tabs
      await expect(page.locator('[data-testid="tab"][data-tab-type="agent"]')).toHaveCount(2)

      // Set up layout save listener before the drag
      const saved = waitForLayoutSave(page)

      // Drag the first agent tab from the tabbar to ws2 in the sidebar
      const sourceTab = page.locator('[data-testid="tab"][data-tab-type="agent"]').first()
      const targetWsItem = page.locator(`[data-testid="workspace-item-${ws2}"]`)
      const sourceBox = await sourceTab.boundingBox()
      const targetBox = await targetWsItem.boundingBox()
      if (!sourceBox || !targetBox)
        throw new Error('Could not get bounding boxes')

      await dragTo(
        page,
        { x: sourceBox.x + sourceBox.width / 2, y: sourceBox.y + sourceBox.height / 2 },
        { x: targetBox.x + targetBox.width / 2, y: targetBox.y + targetBox.height / 2 },
      )
      await page.waitForTimeout(500)

      // ws1 should now have 1 agent tab
      await expect(page.locator('[data-testid="tab"][data-tab-type="agent"]')).toHaveCount(1)

      // Wait for persistence
      await saved

      // Switch to ws2 and verify the moved tab is there
      await page.locator(`[data-testid="workspace-item-${ws2}"]`).click()
      await waitForWorkspaceReady(page)
      await expect(page.locator('[data-testid="tab"][data-tab-type="agent"]')).toHaveCount(2)
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, ws1).catch(() => {})
      await deleteWorkspaceViaAPI(hubUrl, adminToken, ws2).catch(() => {})
    }
  })

  test('cross-workspace drag: sidebar tab to active tabbar', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, adminOrgId, workerId } = leapmuxServer
    const ws1 = await createWorkspaceViaAPI(hubUrl, adminToken, 'Active WS', adminOrgId)
    const ws2 = await createWorkspaceViaAPI(hubUrl, adminToken, 'Sidebar WS', adminOrgId)
    await openAgentViaAPI(hubUrl, adminToken, workerId, ws1)
    await openAgentViaAPI(hubUrl, adminToken, workerId, ws2)
    await openAgentViaAPI(hubUrl, adminToken, workerId, ws2) // ws2 has 2 agents so it keeps one

    try {
      await loginViaToken(page, adminToken)
      await page.goto(`/o/admin/workspace/${ws1}`)
      await waitForWorkspaceReady(page)
      await waitForInitialAgent(page)

      // Visit ws2 to populate its registry, then switch back to ws1
      await preloadWorkspace(page, ws2, ws1)

      // Expand ws2 in the sidebar
      const ws2Item = page.locator(`[data-testid="workspace-item-${ws2}"]`)
      await ws2Item.locator('svg').first().click()

      // Wait for ws2's tab tree leaves to appear (ws1 has 1 leaf + ws2 should have 2)
      await expect(page.locator('[data-testid="tab-tree-leaf"]')).toHaveCount(3, { timeout: 5000 })

      // ws1 starts with 1 agent tab in the tabbar
      await expect(page.locator('[data-testid="tab"][data-tab-type="agent"]')).toHaveCount(1)

      // Set up layout save listener
      const saved = waitForLayoutSave(page)

      // The sidebar tab tree leaves are covered by the workspace item header's
      // stacking context (translate3d from sortable). We must dispatch pointerdown
      // directly on the leaf element rather than relying on hit-testing.
      const targetTab = page.locator('[data-testid="tab"][data-tab-type="agent"]').first()
      const targetBox = await targetTab.boundingBox()
      if (!targetBox)
        throw new Error('Could not get target bounding box')

      // Perform the entire drag using programmatic PointerEvents in the browser.
      // We can't use page.mouse because the sidebar tab tree leaf is covered by
      // the workspace item's stacking context (translate3d from sortable).
      const tgtX = targetBox.x + targetBox.width / 2
      const tgtY = targetBox.y + targetBox.height / 2

      await page.evaluate(({ leafIndex, targetX, targetY }) => {
        return new Promise<void>((resolve) => {
          const leaf = document.querySelectorAll('[data-testid="tab-tree-leaf"]')[leafIndex] as HTMLElement
          if (!leaf)
            throw new Error('Leaf not found')
          const rect = leaf.getBoundingClientRect()
          const startX = rect.x + 10
          const startY = rect.y + rect.height / 2

          leaf.dispatchEvent(new PointerEvent('pointerdown', {
            clientX: startX,
            clientY: startY,
            pointerId: 1,
            button: 0,
            buttons: 1,
            isPrimary: true,
            bubbles: true,
            cancelable: true,
          }))

          // Wait for 300ms activation delay, then move and release
          setTimeout(() => {
            const steps = 10
            for (let i = 1; i <= steps; i++) {
              const x = startX + (targetX - startX) * (i / steps)
              const y = startY + (targetY - startY) * (i / steps)
              document.dispatchEvent(new PointerEvent('pointermove', {
                clientX: x,
                clientY: y,
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
                clientX: targetX,
                clientY: targetY,
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
      }, { leafIndex: 1, targetX: tgtX, targetY: tgtY })
      await page.waitForTimeout(200)

      // ws1 should now have 2 agent tabs (the original + the moved one)
      await expect(page.locator('[data-testid="tab"][data-tab-type="agent"]')).toHaveCount(2)

      // Wait for persistence
      await saved
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, ws1).catch(() => {})
      await deleteWorkspaceViaAPI(hubUrl, adminToken, ws2).catch(() => {})
    }
  })

  test('expanded workspace state persists after reload', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, adminOrgId, workerId } = leapmuxServer
    const ws1 = await createWorkspaceViaAPI(hubUrl, adminToken, 'Expand Persist A', adminOrgId)
    const ws2 = await createWorkspaceViaAPI(hubUrl, adminToken, 'Expand Persist B', adminOrgId)
    await openAgentViaAPI(hubUrl, adminToken, workerId, ws1)
    await openAgentViaAPI(hubUrl, adminToken, workerId, ws2)

    try {
      await loginViaToken(page, adminToken)
      await page.goto(`/o/admin/workspace/${ws1}`)
      await waitForWorkspaceReady(page)
      await waitForInitialAgent(page)

      // Expand ws2 in the sidebar
      const ws2Item = page.locator(`[data-testid="workspace-item-${ws2}"]`)
      await ws2Item.locator('svg').first().click()
      await expect(page.locator('[data-testid="tab-tree-leaf"]')).toHaveCount(2, { timeout: 5000 })

      // Reload the page
      await page.reload()
      await waitForWorkspaceReady(page)
      await waitForInitialAgent(page)

      // ws2 should still be expanded after reload
      await expect(page.locator('[data-testid="tab-tree-leaf"]')).toHaveCount(2, { timeout: 5000 })
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, ws1).catch(() => {})
      await deleteWorkspaceViaAPI(hubUrl, adminToken, ws2).catch(() => {})
    }
  })

  test('clicking non-active workspace tab switches workspace', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, adminOrgId, workerId } = leapmuxServer
    const ws1 = await createWorkspaceViaAPI(hubUrl, adminToken, 'Click Tab Active', adminOrgId)
    const ws2 = await createWorkspaceViaAPI(hubUrl, adminToken, 'Click Tab Inactive', adminOrgId)
    await openAgentViaAPI(hubUrl, adminToken, workerId, ws1)
    await openAgentViaAPI(hubUrl, adminToken, workerId, ws2)

    try {
      await loginViaToken(page, adminToken)
      await page.goto(`/o/admin/workspace/${ws1}`)
      await waitForWorkspaceReady(page)
      await waitForInitialAgent(page)

      // Expand ws2 in the sidebar
      const ws2Item = page.locator(`[data-testid="workspace-item-${ws2}"]`)
      await ws2Item.locator('svg').first().click()
      await expect(page.locator('[data-testid="tab-tree-leaf"]')).toHaveCount(2, { timeout: 5000 })

      // Click ws2's tab leaf in the sidebar — should switch to ws2.
      // Find the leaf that belongs to ws2 by locating it within ws2's
      // workspace item's adjacent children wrapper.
      await page.evaluate((wsId) => {
        const wsItem = document.querySelector(`[data-testid="workspace-item-${wsId}"]`)
        if (!wsItem)
          throw new Error('Workspace item not found')
        // The children wrapper is the next sibling of the workspace item div
        const childrenWrapper = wsItem.nextElementSibling
        if (!childrenWrapper)
          throw new Error('Children wrapper not found')
        const leaf = childrenWrapper.querySelector('[data-testid="tab-tree-leaf"]') as HTMLElement
        if (!leaf)
          throw new Error('Tab tree leaf not found in workspace children')
        leaf.click()
      }, ws2)

      // Should navigate to ws2 — verify by checking the URL and that its agent tab is visible
      await waitForWorkspaceReady(page)
      await waitForInitialAgent(page)
      await expect(page).toHaveURL(new RegExp(`/workspace/${ws2}`), { timeout: 5000 })
      await expect(page.locator('[data-testid="tab"][data-tab-type="agent"]')).toHaveCount(1)
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, ws1).catch(() => {})
      await deleteWorkspaceViaAPI(hubUrl, adminToken, ws2).catch(() => {})
    }
  })

  test('clicking moved tab in target workspace activates it', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, adminOrgId, workerId } = leapmuxServer
    const ws1 = await createWorkspaceViaAPI(hubUrl, adminToken, 'Move Click Source', adminOrgId)
    const ws2 = await createWorkspaceViaAPI(hubUrl, adminToken, 'Move Click Target', adminOrgId)
    await openAgentViaAPI(hubUrl, adminToken, workerId, ws1)
    await openAgentViaAPI(hubUrl, adminToken, workerId, ws1) // keep a tab in source
    await openAgentViaAPI(hubUrl, adminToken, workerId, ws2)

    try {
      await loginViaToken(page, adminToken)
      await page.goto(`/o/admin/workspace/${ws1}`)
      await waitForWorkspaceReady(page)
      await expect(page.locator('[data-testid="tab"][data-tab-type="agent"]')).toHaveCount(2)

      // Set up layout save listener
      const saved = waitForLayoutSave(page)

      // Drag the first agent tab from ws1's tabbar to ws2 in the sidebar
      const sourceTab = page.locator('[data-testid="tab"][data-tab-type="agent"]').first()
      const targetWsItem = page.locator(`[data-testid="workspace-item-${ws2}"]`)
      const sourceBox = await sourceTab.boundingBox()
      const targetBox = await targetWsItem.boundingBox()
      if (!sourceBox || !targetBox)
        throw new Error('Could not get bounding boxes')

      await dragTo(
        page,
        { x: sourceBox.x + sourceBox.width / 2, y: sourceBox.y + sourceBox.height / 2 },
        { x: targetBox.x + targetBox.width / 2, y: targetBox.y + targetBox.height / 2 },
      )
      await page.waitForTimeout(500)
      await expect(page.locator('[data-testid="tab"][data-tab-type="agent"]')).toHaveCount(1)
      await saved

      // Expand ws2's tab tree in the sidebar
      const ws2Item = page.locator(`[data-testid="workspace-item-${ws2}"]`)
      await ws2Item.locator('svg').first().click()

      // ws2 should now have 2 leaves (original + moved tab); ws1 has 1 leaf
      await expect(page.locator('[data-testid="tab-tree-leaf"]')).toHaveCount(3, { timeout: 5000 })

      // Click the moved tab in ws2's sidebar tree
      await page.evaluate((wsId) => {
        const wsItem = document.querySelector(`[data-testid="workspace-item-${wsId}"]`)
        if (!wsItem)
          throw new Error('Workspace item not found')
        const childrenWrapper = wsItem.nextElementSibling
        if (!childrenWrapper)
          throw new Error('Children wrapper not found')
        const leaf = childrenWrapper.querySelector('[data-testid="tab-tree-leaf"]') as HTMLElement
        if (!leaf)
          throw new Error('Tab tree leaf not found')
        leaf.click()
      }, ws2)

      // Should navigate to ws2 and show the moved tab as active
      await waitForWorkspaceReady(page)
      await waitForInitialAgent(page)
      await expect(page).toHaveURL(new RegExp(`/workspace/${ws2}`), { timeout: 5000 })
      await expect(page.locator('[data-testid="tab"][data-tab-type="agent"]')).toHaveCount(2)
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, ws1).catch(() => {})
      await deleteWorkspaceViaAPI(hubUrl, adminToken, ws2).catch(() => {})
    }
  })

  test('clicking moved tab in sidebar shows all target workspace tabs', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, adminOrgId, workerId } = leapmuxServer
    // ws1 has 1 tab (tab 1), ws2 has 1 tab (tab 2)
    const ws1 = await createWorkspaceViaAPI(hubUrl, adminToken, 'Move Click Src', adminOrgId)
    const ws2 = await createWorkspaceViaAPI(hubUrl, adminToken, 'Move Click Tgt', adminOrgId)
    await openAgentViaAPI(hubUrl, adminToken, workerId, ws1)
    await openAgentViaAPI(hubUrl, adminToken, workerId, ws2)

    try {
      await loginViaToken(page, adminToken)
      await page.goto(`/o/admin/workspace/${ws1}`)
      await waitForWorkspaceReady(page)
      await waitForInitialAgent(page)

      // ws1 has 1 agent tab
      await expect(page.locator('[data-testid="tab"][data-tab-type="agent"]')).toHaveCount(1)

      // Drag tab 1 from ws1's tabbar to ws2 in the sidebar.
      // Do NOT wait for layout save — test the race condition where the
      // user clicks the moved tab before persistence completes.
      const sourceTab = page.locator('[data-testid="tab"][data-tab-type="agent"]').first()
      const targetWsItem = page.locator(`[data-testid="workspace-item-${ws2}"]`)
      const sourceBox = await sourceTab.boundingBox()
      const targetBox = await targetWsItem.boundingBox()
      if (!sourceBox || !targetBox)
        throw new Error('Could not get bounding boxes')

      await dragTo(
        page,
        { x: sourceBox.x + sourceBox.width / 2, y: sourceBox.y + sourceBox.height / 2 },
        { x: targetBox.x + targetBox.width / 2, y: targetBox.y + targetBox.height / 2 },
      )
      await page.waitForTimeout(500)

      // ws1 should now have 0 agent tabs
      await expect(page.locator('[data-testid="tab"][data-tab-type="agent"]')).toHaveCount(0)

      // Expand ws2's tab tree — should show 2 leaves (tab 1 moved + tab 2 existing)
      const ws2Item = page.locator(`[data-testid="workspace-item-${ws2}"]`)
      await ws2Item.locator('svg').first().click()
      await expect(page.locator('[data-testid="tab-tree-leaf"]')).toHaveCount(2, { timeout: 5000 })

      // Click the first leaf in ws2's sidebar tree immediately.
      await page.evaluate((wsId) => {
        const wsItem = document.querySelector(`[data-testid="workspace-item-${wsId}"]`)
        if (!wsItem)
          throw new Error('Workspace item not found')
        const childrenWrapper = wsItem.nextElementSibling
        if (!childrenWrapper)
          throw new Error('Children wrapper not found')
        const leaf = childrenWrapper.querySelector('[data-testid="tab-tree-leaf"]') as HTMLElement
        if (!leaf)
          throw new Error('Tab tree leaf not found')
        leaf.click()
      }, ws2)

      // Should navigate to ws2 and show BOTH tabs in the tabbar
      await waitForWorkspaceReady(page)
      await waitForInitialAgent(page)
      await expect(page).toHaveURL(new RegExp(`/workspace/${ws2}`), { timeout: 5000 })
      await expect(page.locator('[data-testid="tab"][data-tab-type="agent"]')).toHaveCount(2, { timeout: 5000 })
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, ws1).catch(() => {})
      await deleteWorkspaceViaAPI(hubUrl, adminToken, ws2).catch(() => {})
    }
  })

  test('moved tab does not flash in source workspace after reload', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, adminOrgId, workerId } = leapmuxServer
    const ws1 = await createWorkspaceViaAPI(hubUrl, adminToken, 'Flash Source', adminOrgId)
    const ws2 = await createWorkspaceViaAPI(hubUrl, adminToken, 'Flash Target', adminOrgId)
    await openAgentViaAPI(hubUrl, adminToken, workerId, ws1)
    await openAgentViaAPI(hubUrl, adminToken, workerId, ws1) // keep a tab in ws1
    await openAgentViaAPI(hubUrl, adminToken, workerId, ws2)

    try {
      await loginViaToken(page, adminToken)
      await page.goto(`/o/admin/workspace/${ws1}`)
      await waitForWorkspaceReady(page)
      await expect(page.locator('[data-testid="tab"][data-tab-type="agent"]')).toHaveCount(2)

      // Drag first agent tab to ws2
      const saved = waitForLayoutSave(page)
      const sourceTab = page.locator('[data-testid="tab"][data-tab-type="agent"]').first()
      const targetWsItem = page.locator(`[data-testid="workspace-item-${ws2}"]`)
      const sourceBox = await sourceTab.boundingBox()
      const targetBox = await targetWsItem.boundingBox()
      if (!sourceBox || !targetBox)
        throw new Error('Could not get bounding boxes')

      await dragTo(
        page,
        { x: sourceBox.x + sourceBox.width / 2, y: sourceBox.y + sourceBox.height / 2 },
        { x: targetBox.x + targetBox.width / 2, y: targetBox.y + targetBox.height / 2 },
      )
      await page.waitForTimeout(500)
      await expect(page.locator('[data-testid="tab"][data-tab-type="agent"]')).toHaveCount(1)
      await saved

      // Reload and verify ws1 has exactly 1 tab tree leaf (no stale flash)
      await page.reload()
      await waitForWorkspaceReady(page)
      await waitForInitialAgent(page)

      // ws1 (active, auto-expanded) should have exactly 1 leaf — the remaining tab.
      // If the stale agent flash bug is present, we'd briefly see 2 leaves.
      // Wait a bit to ensure any flash would have occurred.
      await page.waitForTimeout(1000)

      // Count leaves within ws1's children wrapper specifically
      const ws1LeafCount = await page.evaluate((wsId) => {
        const wsItem = document.querySelector(`[data-testid="workspace-item-${wsId}"]`)
        if (!wsItem)
          return -1
        const childrenWrapper = wsItem.nextElementSibling
        if (!childrenWrapper)
          return 0
        return childrenWrapper.querySelectorAll('[data-testid="tab-tree-leaf"]').length
      }, ws1)
      expect(ws1LeafCount).toBe(1)

      // Also verify the tab bar shows exactly 1 tab
      await expect(page.locator('[data-testid="tab"][data-tab-type="agent"]')).toHaveCount(1)
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, ws1).catch(() => {})
      await deleteWorkspaceViaAPI(hubUrl, adminToken, ws2).catch(() => {})
    }
  })

  test('move tab back to original workspace preserves tab bar', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, adminOrgId, workerId } = leapmuxServer
    const ws1 = await createWorkspaceViaAPI(hubUrl, adminToken, 'MoveBack WS1', adminOrgId)
    const ws2 = await createWorkspaceViaAPI(hubUrl, adminToken, 'MoveBack WS2', adminOrgId)
    await openAgentViaAPI(hubUrl, adminToken, workerId, ws1)
    await openAgentViaAPI(hubUrl, adminToken, workerId, ws1) // keep a tab in ws1
    await openAgentViaAPI(hubUrl, adminToken, workerId, ws2)

    try {
      await loginViaToken(page, adminToken)
      await page.goto(`/o/admin/workspace/${ws1}`)
      await waitForWorkspaceReady(page)
      await expect(page.locator('[data-testid="tab"][data-tab-type="agent"]')).toHaveCount(2)

      // Drag ws1's first tab to ws2
      const saved1 = waitForLayoutSave(page)
      const sourceTab = page.locator('[data-testid="tab"][data-tab-type="agent"]').first()
      const ws2Item = page.locator(`[data-testid="workspace-item-${ws2}"]`)
      const sourceBox = await sourceTab.boundingBox()
      const ws2Box = await ws2Item.boundingBox()
      if (!sourceBox || !ws2Box)
        throw new Error('Could not get bounding boxes')

      await dragTo(
        page,
        { x: sourceBox.x + sourceBox.width / 2, y: sourceBox.y + sourceBox.height / 2 },
        { x: ws2Box.x + ws2Box.width / 2, y: ws2Box.y + ws2Box.height / 2 },
      )
      await page.waitForTimeout(500)
      await expect(page.locator('[data-testid="tab"][data-tab-type="agent"]')).toHaveCount(1)
      await saved1

      // Switch to ws2 — should have 2 agent tabs (its own + the moved one)
      await ws2Item.click()
      await waitForWorkspaceReady(page)
      await expect(page.locator('[data-testid="tab"][data-tab-type="agent"]')).toHaveCount(2)

      // Now drag one tab back to ws1
      const saved2 = waitForLayoutSave(page)
      const tabToDragBack = page.locator('[data-testid="tab"][data-tab-type="agent"]').first()
      const ws1Item = page.locator(`[data-testid="workspace-item-${ws1}"]`)
      const tabBox = await tabToDragBack.boundingBox()
      const ws1Box = await ws1Item.boundingBox()
      if (!tabBox || !ws1Box)
        throw new Error('Could not get bounding boxes')

      await dragTo(
        page,
        { x: tabBox.x + tabBox.width / 2, y: tabBox.y + tabBox.height / 2 },
        { x: ws1Box.x + ws1Box.width / 2, y: ws1Box.y + ws1Box.height / 2 },
      )
      await page.waitForTimeout(500)
      await expect(page.locator('[data-testid="tab"][data-tab-type="agent"]')).toHaveCount(1)
      await saved2

      // Switch to ws1 — should have 2 agent tabs in the tab bar (not empty)
      await ws1Item.click()
      await waitForWorkspaceReady(page)
      await expect(page.locator('[data-testid="tab"][data-tab-type="agent"]')).toHaveCount(2)
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, ws1).catch(() => {})
      await deleteWorkspaceViaAPI(hubUrl, adminToken, ws2).catch(() => {})
    }
  })

  test('cross-workspace move persists after reload', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, adminOrgId, workerId } = leapmuxServer
    const ws1 = await createWorkspaceViaAPI(hubUrl, adminToken, 'Persist Source', adminOrgId)
    const ws2 = await createWorkspaceViaAPI(hubUrl, adminToken, 'Persist Target', adminOrgId)
    await openAgentViaAPI(hubUrl, adminToken, workerId, ws1)
    await openAgentViaAPI(hubUrl, adminToken, workerId, ws1) // keep a tab in source
    await openAgentViaAPI(hubUrl, adminToken, workerId, ws2)

    try {
      await loginViaToken(page, adminToken)
      await page.goto(`/o/admin/workspace/${ws1}`)
      await waitForWorkspaceReady(page)
      await expect(page.locator('[data-testid="tab"][data-tab-type="agent"]')).toHaveCount(2)

      // Set up layout save listener
      const saved = waitForLayoutSave(page)

      // Drag first agent tab to ws2
      const sourceTab = page.locator('[data-testid="tab"][data-tab-type="agent"]').first()
      const targetWsItem = page.locator(`[data-testid="workspace-item-${ws2}"]`)
      const sourceBox = await sourceTab.boundingBox()
      const targetBox = await targetWsItem.boundingBox()
      if (!sourceBox || !targetBox)
        throw new Error('Could not get bounding boxes')

      await dragTo(
        page,
        { x: sourceBox.x + sourceBox.width / 2, y: sourceBox.y + sourceBox.height / 2 },
        { x: targetBox.x + targetBox.width / 2, y: targetBox.y + targetBox.height / 2 },
      )
      await page.waitForTimeout(500)
      await expect(page.locator('[data-testid="tab"][data-tab-type="agent"]')).toHaveCount(1)
      await saved

      // Reload and verify ws1 still has 1 tab
      await page.reload()
      await waitForWorkspaceReady(page)
      await expect(page.locator('[data-testid="tab"][data-tab-type="agent"]')).toHaveCount(1)

      // Navigate to ws2 and verify it has 2 tabs
      await page.locator(`[data-testid="workspace-item-${ws2}"]`).click()
      await waitForWorkspaceReady(page)
      await expect(page.locator('[data-testid="tab"][data-tab-type="agent"]')).toHaveCount(2)
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, ws1).catch(() => {})
      await deleteWorkspaceViaAPI(hubUrl, adminToken, ws2).catch(() => {})
    }
  })
})
