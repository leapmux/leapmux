import type { Page } from '@playwright/test'
import { expect, test } from './fixtures'
import { boxCenter, dragSidebarLeafTo, mouseDragOnto } from './helpers/drag'
import { selectedAgentTab } from './helpers/nativeScenario'
import { agentTabs, collapseWorkspaceRow, expandWorkspaceRow, expectAgentTabCount, loginViaToken, openWorkspace, sidebarLeaves, waitForLayoutSave, waitForWorkspaceReady, workspaceRow, workspaceRowTitle } from './helpers/ui'
import { createWorkspaceWithAgentsViaAPI } from './helpers/workspace'

/**
 * Drag the first agent tab of the tab bar onto the sidebar row of `workspaceId`.
 * The tab leaves the page with the move, so the drag checks no dragging class after the release.
 */
async function dragFirstAgentTabOnto(page: Page, workspaceId: string): Promise<void> {
  await mouseDragOnto(page, {
    from: await boxCenter(agentTabs(page).first()),
    to: await boxCenter(workspaceRow(page, workspaceId)),
  })
}

test.describe('Multi-Workspace', () => {
  test('workspace switch preserves tabs in sidebar', async ({ page, leapmuxServer }) => {
    const { workspaceId: ws1 } = await createWorkspaceWithAgentsViaAPI(leapmuxServer, 'Multi WS Alpha')
    const { workspaceId: ws2 } = await createWorkspaceWithAgentsViaAPI(leapmuxServer, 'Multi WS Beta')

    await loginViaToken(page, leapmuxServer.adminToken)
    await openWorkspace(page, ws1)

    // WS Alpha should be active with an agent tab visible
    await agentTabs(page).first().waitFor()
    await expectAgentTabCount(page, 1)

    // Switch to WS Beta
    await workspaceRowTitle(page, ws2).click()
    await waitForWorkspaceReady(page)
    await agentTabs(page).first().waitFor()
    await expectAgentTabCount(page, 1)

    // Switch back to WS Alpha — tabs should still be there
    await workspaceRowTitle(page, ws1).click()
    await waitForWorkspaceReady(page)
    await agentTabs(page).first().waitFor()
    await expectAgentTabCount(page, 1)
  })

  test('expand non-active workspace shows tab tree', async ({ page, leapmuxServer }) => {
    const { workspaceId: ws1 } = await createWorkspaceWithAgentsViaAPI(leapmuxServer, 'TreeView Active')
    const { workspaceId: ws2 } = await createWorkspaceWithAgentsViaAPI(leapmuxServer, 'TreeView Inactive')

    await loginViaToken(page, leapmuxServer.adminToken)
    await openWorkspace(page, ws1)
    await agentTabs(page).first().waitFor()

    // ws2 should be visible in the sidebar. Collapse it first: the cold start
    // of `openWorkspace` can expand it, and the test then proves nothing.
    await expect(workspaceRow(page, ws2)).toBeVisible()
    await collapseWorkspaceRow(page, ws2)

    // Expand ws2 while ws1 stays active. The projection already holds the
    // tabs of every workspace, so the expansion only shows them.
    await expandWorkspaceRow(page, ws2)

    // ws2's tab tree should appear beside the active ws1's. Counted per
    // workspace, so a workspace that another test left behind cannot change it.
    await expect(sidebarLeaves(page, ws1)).toHaveCount(1)
    await expect(sidebarLeaves(page, ws2)).toHaveCount(1)
    await expect(sidebarLeaves(page, ws2)).toBeVisible()
  })

  test('cross-workspace drag: tabbar tab to sidebar workspace', async ({ page, leapmuxServer }) => {
    // A second agent in ws1, so the source keeps a tab.
    const { workspaceId: ws1 } = await createWorkspaceWithAgentsViaAPI(leapmuxServer, 'Drag Source WS', { agentCount: 2 })
    const { workspaceId: ws2 } = await createWorkspaceWithAgentsViaAPI(leapmuxServer, 'Drag Target WS')

    await loginViaToken(page, leapmuxServer.adminToken)
    await openWorkspace(page, ws1)

    // ws1 should have 2 agent tabs
    await expectAgentTabCount(page, 2)

    // Set up layout save listener before the drag
    const saved = waitForLayoutSave(page)

    // Drag the first agent tab from the tabbar to ws2 in the sidebar
    await dragFirstAgentTabOnto(page, ws2)

    // ws1 should now have 1 agent tab
    await expectAgentTabCount(page, 1)

    // Wait for persistence
    await saved

    // Switch to ws2 and verify the moved tab is there
    await workspaceRowTitle(page, ws2).click()
    await waitForWorkspaceReady(page)
    await expectAgentTabCount(page, 2)
  })

  test('cross-workspace drag: sidebar tab to active tabbar', async ({ page, leapmuxServer }) => {
    const { workspaceId: ws1 } = await createWorkspaceWithAgentsViaAPI(leapmuxServer, 'Active WS')
    // ws2 has 2 agents so it keeps one
    const { workspaceId: ws2 } = await createWorkspaceWithAgentsViaAPI(leapmuxServer, 'Sidebar WS', { agentCount: 2 })

    await loginViaToken(page, leapmuxServer.adminToken)
    await openWorkspace(page, ws1)
    await agentTabs(page).first().waitFor()

    // Expand ws2 in the sidebar
    await expandWorkspaceRow(page, ws2)

    // Wait for ws2's tab tree leaves to appear: ws1 has 1 leaf and ws2 has 2.
    await expect(sidebarLeaves(page, ws1)).toHaveCount(1)
    await expect(sidebarLeaves(page, ws2)).toHaveCount(2)

    // ws1 starts with 1 agent tab in the tabbar
    await expectAgentTabCount(page, 1)

    // Set up layout save listener
    const saved = waitForLayoutSave(page)

    // Drag the first leaf of ws2 onto the active tab bar. See `dragSidebarLeafTo`
    // for why a sidebar leaf takes synthetic pointer events.
    await dragSidebarLeafTo(sidebarLeaves(page, ws2).first(), await boxCenter(agentTabs(page).first()))

    // ws1 should now have 2 agent tabs (the original + the moved one)
    await expectAgentTabCount(page, 2)

    // Wait for persistence
    await saved
  })

  test('expanded workspace state persists after reload', async ({ page, leapmuxServer }) => {
    const { workspaceId: ws1 } = await createWorkspaceWithAgentsViaAPI(leapmuxServer, 'Expand Persist A')
    const { workspaceId: ws2 } = await createWorkspaceWithAgentsViaAPI(leapmuxServer, 'Expand Persist B')

    await loginViaToken(page, leapmuxServer.adminToken)
    await openWorkspace(page, ws1)
    await agentTabs(page).first().waitFor()

    // Read the expanded bit off the row rather than counting leaves:
    // collapsing only sets `visibility: hidden` on the children wrapper, so
    // the leaves stay in the DOM and a count reads the same either way.
    const ws2Row = workspaceRow(page, ws2)
    const ws2Leaf = sidebarLeaves(page, ws2)

    // Drive ws2 to collapsed instead of assuming it: the cold start of
    // `openWorkspace` can expand it. The expansion is then a real chevron click.
    await collapseWorkspaceRow(page, ws2)
    await expandWorkspaceRow(page, ws2)
    await expect(ws2Leaf).toBeVisible()

    // Reload the page
    await page.reload()
    await waitForWorkspaceReady(page)
    await agentTabs(page).first().waitFor()

    // ws2 should still be expanded after reload, and its tabs re-fetched --
    // the expanded bit alone would survive with an empty subtree.
    await expect(ws2Row).toHaveAttribute('data-expanded', 'true')
    await expect(ws2Leaf).toBeVisible()
  })

  test('clicking non-active workspace tab switches workspace', async ({ page, leapmuxServer }) => {
    const { workspaceId: ws1 } = await createWorkspaceWithAgentsViaAPI(leapmuxServer, 'Click Tab Active')
    const { workspaceId: ws2 } = await createWorkspaceWithAgentsViaAPI(leapmuxServer, 'Click Tab Inactive')

    await loginViaToken(page, leapmuxServer.adminToken)
    await openWorkspace(page, ws1)
    await agentTabs(page).first().waitFor()

    // Expand ws2 in the sidebar
    await expandWorkspaceRow(page, ws2)
    await expect(sidebarLeaves(page, ws1)).toHaveCount(1)
    await expect(sidebarLeaves(page, ws2)).toHaveCount(1)

    // Click ws2's tab leaf in the sidebar — should switch to ws2. The click is
    // dispatched on the leaf, because the workspace row above it covers it.
    await sidebarLeaves(page, ws2).first().dispatchEvent('click')

    // Should switch to ws2 -- verify the sidebar row went active and its agent tab is visible
    await waitForWorkspaceReady(page)
    await agentTabs(page).first().waitFor()
    await expect(workspaceRow(page, ws2))
      .toHaveAttribute('data-active', 'true')
    await expectAgentTabCount(page, 1)
  })

  test('clicking moved tab in target workspace activates it', async ({ page, leapmuxServer }) => {
    // Two agents in ws1, so the source keeps a tab.
    const { workspaceId: ws1 } = await createWorkspaceWithAgentsViaAPI(leapmuxServer, 'Move Click Source', { agentCount: 2 })
    const { workspaceId: ws2 } = await createWorkspaceWithAgentsViaAPI(leapmuxServer, 'Move Click Target')

    await loginViaToken(page, leapmuxServer.adminToken)
    await openWorkspace(page, ws1)
    await expectAgentTabCount(page, 2)

    // Set up layout save listener
    const saved = waitForLayoutSave(page)

    // Drag the first agent tab from ws1's tabbar to ws2 in the sidebar
    await dragFirstAgentTabOnto(page, ws2)
    await expectAgentTabCount(page, 1)
    await saved

    // Expand ws2's tab tree in the sidebar
    await expandWorkspaceRow(page, ws2)

    // ws2 should now have 2 leaves (original + moved tab); ws1 has 1 leaf
    await expect(sidebarLeaves(page, ws1)).toHaveCount(1)
    await expect(sidebarLeaves(page, ws2)).toHaveCount(2)

    // Click the moved tab in ws2's sidebar tree
    await sidebarLeaves(page, ws2).first().dispatchEvent('click')

    // Should switch to ws2 and show the moved tab as active
    await waitForWorkspaceReady(page)
    await agentTabs(page).first().waitFor()
    await expect(workspaceRow(page, ws2))
      .toHaveAttribute('data-active', 'true')
    await expectAgentTabCount(page, 2)
  })

  test('clicking moved tab in sidebar shows all target workspace tabs', async ({ page, leapmuxServer }) => {
    // ws1 has 1 tab (tab 1), ws2 has 1 tab (tab 2)
    const { workspaceId: ws1 } = await createWorkspaceWithAgentsViaAPI(leapmuxServer, 'Move Click Src')
    const { workspaceId: ws2 } = await createWorkspaceWithAgentsViaAPI(leapmuxServer, 'Move Click Tgt')

    await loginViaToken(page, leapmuxServer.adminToken)
    await openWorkspace(page, ws1)
    await agentTabs(page).first().waitFor()

    // ws1 has 1 agent tab
    await expectAgentTabCount(page, 1)

    // Drag tab 1 from ws1's tabbar to ws2 in the sidebar.
    // Do NOT wait for layout save — test the race condition where the
    // user clicks the moved tab before persistence completes.
    await dragFirstAgentTabOnto(page, ws2)

    // ws1 should now have 0 agent tabs
    await expectAgentTabCount(page, 0)

    // Expand ws2's tab tree — should show 2 leaves (tab 1 moved + tab 2 existing)
    await expandWorkspaceRow(page, ws2)
    await expect(sidebarLeaves(page, ws2)).toHaveCount(2)

    // Click the first leaf in ws2's sidebar tree immediately.
    await sidebarLeaves(page, ws2).first().dispatchEvent('click')

    // Should switch to ws2 and show BOTH tabs in the tabbar
    await waitForWorkspaceReady(page)
    await agentTabs(page).first().waitFor()
    await expect(workspaceRow(page, ws2))
      .toHaveAttribute('data-active', 'true')
    await expectAgentTabCount(page, 2)
  })

  test('moved tab does not flash in source workspace after reload', async ({ page, leapmuxServer }) => {
    // Two agents in ws1, so ws1 keeps a tab.
    const { workspaceId: ws1 } = await createWorkspaceWithAgentsViaAPI(leapmuxServer, 'Flash Source', { agentCount: 2 })
    const { workspaceId: ws2 } = await createWorkspaceWithAgentsViaAPI(leapmuxServer, 'Flash Target')

    await loginViaToken(page, leapmuxServer.adminToken)
    await openWorkspace(page, ws1)
    await expectAgentTabCount(page, 2)

    // Drag first agent tab to ws2
    const saved = waitForLayoutSave(page)
    await dragFirstAgentTabOnto(page, ws2)
    await expectAgentTabCount(page, 1)
    await saved

    // Reload and verify ws1 has exactly 1 tab tree leaf (no stale flash)
    await page.reload()
    await waitForWorkspaceReady(page)
    await agentTabs(page).first().waitFor()

    // ws1 (active, auto-expanded) should have exactly 1 leaf — the remaining tab.
    // If the stale agent flash bug is present, we'd briefly see 2 leaves.
    // Wait a bit to ensure any flash would have occurred.
    await page.waitForTimeout(1000)

    // Count leaves within ws1's children wrapper specifically
    await expect(sidebarLeaves(page, ws1)).toHaveCount(1)

    // Also verify the tab bar shows exactly 1 tab
    await expectAgentTabCount(page, 1)
  })

  test('move tab back to original workspace preserves tab bar', async ({ page, leapmuxServer }) => {
    // Two agents in ws1, so ws1 keeps a tab.
    const { workspaceId: ws1 } = await createWorkspaceWithAgentsViaAPI(leapmuxServer, 'MoveBack WS1', { agentCount: 2 })
    const { workspaceId: ws2 } = await createWorkspaceWithAgentsViaAPI(leapmuxServer, 'MoveBack WS2')

    await loginViaToken(page, leapmuxServer.adminToken)
    await openWorkspace(page, ws1)
    await expectAgentTabCount(page, 2)

    // Drag ws1's first tab to ws2
    const saved1 = waitForLayoutSave(page)
    await dragFirstAgentTabOnto(page, ws2)
    await expectAgentTabCount(page, 1)
    await saved1

    // Switch to ws2 — should have 2 agent tabs (its own + the moved one)
    await workspaceRowTitle(page, ws2).click()
    await waitForWorkspaceReady(page)
    await expectAgentTabCount(page, 2)

    // Now drag one tab back to ws1
    const saved2 = waitForLayoutSave(page)
    await dragFirstAgentTabOnto(page, ws1)
    await expectAgentTabCount(page, 1)
    await saved2

    // Switch to ws1 — should have 2 agent tabs in the tab bar (not empty)
    await workspaceRowTitle(page, ws1).click()
    await waitForWorkspaceReady(page)
    await expectAgentTabCount(page, 2)
  })

  test('clicking specific tab in non-active workspace activates that tab', async ({ page, leapmuxServer }) => {
    const { workspaceId: ws1 } = await createWorkspaceWithAgentsViaAPI(leapmuxServer, 'Specific Tab WS1')
    // Create 2 agents in ws2 so we can distinguish which tab is active.
    const { workspaceId: ws2 } = await createWorkspaceWithAgentsViaAPI(leapmuxServer, 'Specific Tab WS2', { agentCount: 2 })

    await loginViaToken(page, leapmuxServer.adminToken)
    await openWorkspace(page, ws1)
    await agentTabs(page).first().waitFor()

    // Expand ws2 in the sidebar
    await expandWorkspaceRow(page, ws2)

    // Wait for ws2's tab tree leaves to appear (2 agents)
    await expect(sidebarLeaves(page, ws2)).toHaveCount(2)

    // Get the tab ID of the SECOND leaf and click it
    const secondLeaf = sidebarLeaves(page, ws2).nth(1)
    const secondLeafTabId = await secondLeaf.getAttribute('data-tab-id')
    expect(secondLeafTabId).toBeTruthy()
    await secondLeaf.dispatchEvent('click')

    // Should switch to ws2
    await waitForWorkspaceReady(page)
    await expect(workspaceRow(page, ws2))
      .toHaveAttribute('data-active', 'true')

    // The clicked tab should be the active (aria-selected) tab in the tab bar
    await expect(selectedAgentTab(page)).toHaveAttribute('data-tab-id', secondLeafTabId!)
  })

  test('cross-workspace move persists after reload', async ({ page, leapmuxServer }) => {
    // Two agents in ws1, so the source keeps a tab.
    const { workspaceId: ws1 } = await createWorkspaceWithAgentsViaAPI(leapmuxServer, 'Persist Source', { agentCount: 2 })
    const { workspaceId: ws2 } = await createWorkspaceWithAgentsViaAPI(leapmuxServer, 'Persist Target')

    await loginViaToken(page, leapmuxServer.adminToken)
    await openWorkspace(page, ws1)
    await expectAgentTabCount(page, 2)

    // Set up layout save listener
    const saved = waitForLayoutSave(page)

    // Drag first agent tab to ws2
    await dragFirstAgentTabOnto(page, ws2)
    await expectAgentTabCount(page, 1)
    await saved

    // Reload and verify ws1 still has 1 tab
    await page.reload()
    await waitForWorkspaceReady(page)
    await expectAgentTabCount(page, 1)

    // Navigate to ws2 and verify it has 2 tabs
    await workspaceRowTitle(page, ws2).click()
    await waitForWorkspaceReady(page)
    await expectAgentTabCount(page, 2)
  })
})
