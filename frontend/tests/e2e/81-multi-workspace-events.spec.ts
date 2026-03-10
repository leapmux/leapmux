import type { Page } from '@playwright/test'
import { expect, test } from './fixtures'
import { createWorkspaceViaAPI, deleteWorkspaceViaAPI, openAgentViaAPI } from './helpers/api'
import { loginViaToken, waitForWorkspaceReady } from './helpers/ui'

/** Wait for the workspace to be fully loaded with its initial agent tab. */
async function waitForInitialAgent(page: Page) {
  await page.locator('[data-testid="tab"][data-tab-type="agent"]').first().waitFor({ timeout: 10000 })
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

test.describe('Multi-Workspace Events', () => {
  test('non-active workspace agent status reflected in sidebar', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, adminOrgId, workerId } = leapmuxServer
    const ws1 = await createWorkspaceViaAPI(hubUrl, adminToken, 'Events Active', adminOrgId)
    const ws2 = await createWorkspaceViaAPI(hubUrl, adminToken, 'Events Inactive', adminOrgId)
    await openAgentViaAPI(hubUrl, adminToken, workerId, ws1)
    await openAgentViaAPI(hubUrl, adminToken, workerId, ws2)

    try {
      await loginViaToken(page, adminToken)
      await page.goto(`/o/admin/workspace/${ws1}`)
      await waitForWorkspaceReady(page)
      await waitForInitialAgent(page)

      // Visit ws2 to populate its registry snapshot, then switch back
      await preloadWorkspace(page, ws2, ws1)

      // Expand ws2 in the sidebar
      const ws2Item = page.locator(`[data-testid="workspace-item-${ws2}"]`)
      await ws2Item.locator('svg').first().click()

      // ws1 active has 1 leaf (auto-expanded) + ws2 expanded has 1 leaf = 2
      await expect(page.locator('[data-testid="tab-tree-leaf"]')).toHaveCount(2, { timeout: 5000 })
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, ws1).catch(() => {})
      await deleteWorkspaceViaAPI(hubUrl, adminToken, ws2).catch(() => {})
    }
  })

  test('switching to previously expanded workspace shows correct tabs', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, adminOrgId, workerId } = leapmuxServer
    const ws1 = await createWorkspaceViaAPI(hubUrl, adminToken, 'Switch From', adminOrgId)
    const ws2 = await createWorkspaceViaAPI(hubUrl, adminToken, 'Switch To', adminOrgId)
    await openAgentViaAPI(hubUrl, adminToken, workerId, ws1)
    await openAgentViaAPI(hubUrl, adminToken, workerId, ws2)
    await openAgentViaAPI(hubUrl, adminToken, workerId, ws2) // ws2 has 2 agents

    try {
      await loginViaToken(page, adminToken)
      await page.goto(`/o/admin/workspace/${ws1}`)
      await waitForWorkspaceReady(page)
      await waitForInitialAgent(page)

      // Visit ws2 to populate its registry, then switch back
      await preloadWorkspace(page, ws2, ws1)

      // Expand ws2 in the sidebar
      const ws2Item = page.locator(`[data-testid="workspace-item-${ws2}"]`)
      await ws2Item.locator('svg').first().click()

      // ws1 active (1 leaf) + ws2 expanded (2 leaves) = 3
      await expect(page.locator('[data-testid="tab-tree-leaf"]')).toHaveCount(3, { timeout: 5000 })

      // Switch to ws2 — should load with its 2 agent tabs
      await ws2Item.click()
      await waitForWorkspaceReady(page)

      await expect(page.locator('[data-testid="tab"][data-tab-type="agent"]')).toHaveCount(2)

      // Switch back to ws1 — should have 1 agent tab
      await page.locator(`[data-testid="workspace-item-${ws1}"]`).click()
      await waitForWorkspaceReady(page)
      await expect(page.locator('[data-testid="tab"][data-tab-type="agent"]')).toHaveCount(1)
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, ws1).catch(() => {})
      await deleteWorkspaceViaAPI(hubUrl, adminToken, ws2).catch(() => {})
    }
  })

  test('multiple workspaces with agents all appear in sidebar', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, adminOrgId, workerId } = leapmuxServer
    const ws1 = await createWorkspaceViaAPI(hubUrl, adminToken, 'Multi Events A', adminOrgId)
    const ws2 = await createWorkspaceViaAPI(hubUrl, adminToken, 'Multi Events B', adminOrgId)
    const ws3 = await createWorkspaceViaAPI(hubUrl, adminToken, 'Multi Events C', adminOrgId)
    await openAgentViaAPI(hubUrl, adminToken, workerId, ws1)
    await openAgentViaAPI(hubUrl, adminToken, workerId, ws2)
    await openAgentViaAPI(hubUrl, adminToken, workerId, ws3)

    try {
      await loginViaToken(page, adminToken)
      await page.goto(`/o/admin/workspace/${ws1}`)
      await waitForWorkspaceReady(page)
      await waitForInitialAgent(page)

      // All three workspaces should appear in the sidebar
      await expect(page.locator(`[data-testid="workspace-item-${ws1}"]`)).toBeVisible()
      await expect(page.locator(`[data-testid="workspace-item-${ws2}"]`)).toBeVisible()
      await expect(page.locator(`[data-testid="workspace-item-${ws3}"]`)).toBeVisible()

      // Preload ws2 and ws3 so their registry snapshots are populated.
      // Preloading auto-expands each workspace (since it becomes active),
      // and the expansion persists after switching back.
      await preloadWorkspace(page, ws2, ws1)
      await preloadWorkspace(page, ws3, ws1)

      // After preloading, all 3 workspaces are expanded:
      // ws1 active (1 leaf) + ws2 (1 leaf) + ws3 (1 leaf) = 3
      await expect(page.locator('[data-testid="tab-tree-leaf"]')).toHaveCount(3, { timeout: 5000 })
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, ws1).catch(() => {})
      await deleteWorkspaceViaAPI(hubUrl, adminToken, ws2).catch(() => {})
      await deleteWorkspaceViaAPI(hubUrl, adminToken, ws3).catch(() => {})
    }
  })
})
