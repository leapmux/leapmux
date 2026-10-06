import { expect, test } from './fixtures'
import { agentTabs, expectAgentTabCount, loginViaToken, openWorkspace, sidebarLeaves, waitForWorkspaceReady, workspaceChevron, workspaceRow } from './helpers/ui'
import { createWorkspaceWithAgentsViaAPI } from './helpers/workspace'

test.describe('Multi-Workspace Events', () => {
  test('non-active workspace agent status reflected in sidebar', async ({ page, leapmuxServer }) => {
    const { workspaceId: ws1 } = await createWorkspaceWithAgentsViaAPI(leapmuxServer, 'Events Active')
    const { workspaceId: ws2 } = await createWorkspaceWithAgentsViaAPI(leapmuxServer, 'Events Inactive')

    await loginViaToken(page, leapmuxServer.adminToken)
    await openWorkspace(page, ws1)
    await agentTabs(page).first().waitFor()

    // No preload needed: every workspace is projected at all times.

    // Expand ws2 in the sidebar
    await workspaceChevron(page, ws2).click()

    // ws1 active has 1 leaf (auto-expanded) and ws2 expanded has 1 leaf.
    await expect(sidebarLeaves(page, ws1)).toHaveCount(1)
    await expect(sidebarLeaves(page, ws2)).toHaveCount(1)
  })

  test('switching to previously expanded workspace shows correct tabs', async ({ page, leapmuxServer }) => {
    const { workspaceId: ws1 } = await createWorkspaceWithAgentsViaAPI(leapmuxServer, 'Switch From')
    // ws2 has 2 agents
    const { workspaceId: ws2 } = await createWorkspaceWithAgentsViaAPI(leapmuxServer, 'Switch To', { agentCount: 2 })

    await loginViaToken(page, leapmuxServer.adminToken)
    await openWorkspace(page, ws1)
    await agentTabs(page).first().waitFor()

    // Visit ws2 to populate its registry, then switch back
    const ws2Item = workspaceRow(page, ws2)
    await workspaceChevron(page, ws2).click()

    // ws1 active (1 leaf) and ws2 expanded (2 leaves)
    await expect(sidebarLeaves(page, ws1)).toHaveCount(1)
    await expect(sidebarLeaves(page, ws2)).toHaveCount(2)

    // Switch to ws2 — should load with its 2 agent tabs
    await ws2Item.click()
    await waitForWorkspaceReady(page)

    await expectAgentTabCount(page, 2)

    // Switch back to ws1 — should have 1 agent tab
    await workspaceRow(page, ws1).click()
    await waitForWorkspaceReady(page)
    await expectAgentTabCount(page, 1)
  })

  test('multiple workspaces with agents all appear in sidebar', async ({ page, leapmuxServer }) => {
    const { workspaceId: ws1 } = await createWorkspaceWithAgentsViaAPI(leapmuxServer, 'Multi Events A')
    const { workspaceId: ws2 } = await createWorkspaceWithAgentsViaAPI(leapmuxServer, 'Multi Events B')
    const { workspaceId: ws3 } = await createWorkspaceWithAgentsViaAPI(leapmuxServer, 'Multi Events C')

    await loginViaToken(page, leapmuxServer.adminToken)
    await openWorkspace(page, ws1)
    await agentTabs(page).first().waitFor()

    // All three workspaces should appear in the sidebar
    await expect(workspaceRow(page, ws1)).toBeVisible()
    await expect(workspaceRow(page, ws2)).toBeVisible()
    await expect(workspaceRow(page, ws3)).toBeVisible()

    // No preload needed: every workspace is projected at all times.
    // Preloading auto-expands each workspace (since it becomes active),
    // and the expansion persists after switching back.

    // After preloading, all 3 workspaces are expanded, one leaf each.
    for (const workspaceId of [ws1, ws2, ws3])
      await expect(sidebarLeaves(page, workspaceId)).toHaveCount(1)
  })
})
