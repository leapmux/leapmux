import type { Page } from '@playwright/test'
import { expect, test } from './fixtures'
import { createWorkspaceViaAPI, deleteWorkspaceViaAPI, openAgentViaAPI } from './helpers/api'
import { loginViaToken, openWorkspace, waitForWorkspaceReady, workspaceChevron, workspaceRow } from './helpers/ui'

/** Wait for the workspace to be fully loaded with its initial agent tab. */
async function waitForInitialAgent(page: Page) {
  await page.locator('[data-testid="tab"][data-tab-type="agent"]').first().waitFor()
}

test.describe('Multi-Workspace Events', () => {
  test('non-active workspace agent status reflected in sidebar', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, workerId } = leapmuxServer
    const ws1 = await createWorkspaceViaAPI(hubUrl, adminToken, 'Events Active')
    const ws2 = await createWorkspaceViaAPI(hubUrl, adminToken, 'Events Inactive')
    await openAgentViaAPI(hubUrl, adminToken, workerId, ws1)
    await openAgentViaAPI(hubUrl, adminToken, workerId, ws2)

    try {
      await loginViaToken(page, adminToken)
      await openWorkspace(page, ws1)
      await waitForInitialAgent(page)

      // No preload needed: every workspace is projected at all times.

      // Expand ws2 in the sidebar
      await workspaceChevron(page, ws2).click()

      // ws1 active has 1 leaf (auto-expanded) + ws2 expanded has 1 leaf = 2
      await expect(page.locator('[data-testid="tab-tree-leaf"]')).toHaveCount(2)
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, ws1).catch(() => {})
      await deleteWorkspaceViaAPI(hubUrl, adminToken, ws2).catch(() => {})
    }
  })

  test('switching to previously expanded workspace shows correct tabs', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, workerId } = leapmuxServer
    const ws1 = await createWorkspaceViaAPI(hubUrl, adminToken, 'Switch From')
    const ws2 = await createWorkspaceViaAPI(hubUrl, adminToken, 'Switch To')
    await openAgentViaAPI(hubUrl, adminToken, workerId, ws1)
    await openAgentViaAPI(hubUrl, adminToken, workerId, ws2)
    await openAgentViaAPI(hubUrl, adminToken, workerId, ws2) // ws2 has 2 agents

    try {
      await loginViaToken(page, adminToken)
      await openWorkspace(page, ws1)
      await waitForInitialAgent(page)

      // Visit ws2 to populate its registry, then switch back
      const ws2Item = workspaceRow(page, ws2)
      await workspaceChevron(page, ws2).click()

      // ws1 active (1 leaf) + ws2 expanded (2 leaves) = 3
      await expect(page.locator('[data-testid="tab-tree-leaf"]')).toHaveCount(3)

      // Switch to ws2 — should load with its 2 agent tabs
      await ws2Item.click()
      await waitForWorkspaceReady(page)

      await expect(page.locator('[data-testid="tab"][data-tab-type="agent"]')).toHaveCount(2)

      // Switch back to ws1 — should have 1 agent tab
      await workspaceRow(page, ws1).click()
      await waitForWorkspaceReady(page)
      await expect(page.locator('[data-testid="tab"][data-tab-type="agent"]')).toHaveCount(1)
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, ws1).catch(() => {})
      await deleteWorkspaceViaAPI(hubUrl, adminToken, ws2).catch(() => {})
    }
  })

  test('multiple workspaces with agents all appear in sidebar', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, workerId } = leapmuxServer
    const ws1 = await createWorkspaceViaAPI(hubUrl, adminToken, 'Multi Events A')
    const ws2 = await createWorkspaceViaAPI(hubUrl, adminToken, 'Multi Events B')
    const ws3 = await createWorkspaceViaAPI(hubUrl, adminToken, 'Multi Events C')
    await openAgentViaAPI(hubUrl, adminToken, workerId, ws1)
    await openAgentViaAPI(hubUrl, adminToken, workerId, ws2)
    await openAgentViaAPI(hubUrl, adminToken, workerId, ws3)

    try {
      await loginViaToken(page, adminToken)
      await openWorkspace(page, ws1)
      await waitForInitialAgent(page)

      // All three workspaces should appear in the sidebar
      await expect(workspaceRow(page, ws1)).toBeVisible()
      await expect(workspaceRow(page, ws2)).toBeVisible()
      await expect(workspaceRow(page, ws3)).toBeVisible()

      // No preload needed: every workspace is projected at all times.
      // Preloading auto-expands each workspace (since it becomes active),
      // and the expansion persists after switching back.

      // After preloading, all 3 workspaces are expanded:
      // ws1 active (1 leaf) + ws2 (1 leaf) + ws3 (1 leaf) = 3
      await expect(page.locator('[data-testid="tab-tree-leaf"]')).toHaveCount(3)
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, ws1).catch(() => {})
      await deleteWorkspaceViaAPI(hubUrl, adminToken, ws2).catch(() => {})
      await deleteWorkspaceViaAPI(hubUrl, adminToken, ws3).catch(() => {})
    }
  })
})
