import { expect, test } from './fixtures'
import { createWorkspaceViaAPI, deleteWorkspaceViaAPI, getRecordedToasts, loginViaToken, openWorkspaceContextMenu, waitForWorkspaceReady } from './helpers'

test.describe('Workspace UX Enhancements', () => {
  test('should auto-activate first workspace on org root', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, workerId, adminOrgId } = leapmuxServer
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, workerId, 'Auto Activate Test', adminOrgId)
    try {
      await loginViaToken(page, adminToken)
      await page.goto(`/o/admin/workspace/${workspaceId}`)
      await waitForWorkspaceReady(page)

      // Navigate to the org root (no workspace ID)
      await page.goto('/o/admin')
      await expect(page.locator('[data-testid="section-header-workspaces_in_progress"]')).toBeVisible()

      // Should auto-redirect to the first workspace
      await expect(page).toHaveURL(/\/workspace\//)
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })

  test('should open new workspace dialog from sidebar + button', async ({ page, leapmuxServer }) => {
    await loginViaToken(page, leapmuxServer.adminToken)
    // Navigate to org root first
    await page.goto('/o/admin')
    await expect(page.locator('[data-testid="section-header-workspaces_in_progress"]')).toBeVisible()

    // Click the sidebar + button
    await page.locator('[data-testid="sidebar-new-workspace"]').click()

    // New workspace dialog should appear
    await expect(page.getByRole('heading', { name: 'New Workspace' })).toBeVisible()

    // Close dialog without creating
    await page.keyboard.press('Escape')
    await expect(page.getByRole('heading', { name: 'New Workspace' })).not.toBeVisible()
  })

  test('should show empty state when no tabs are open', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, workerId, adminOrgId } = leapmuxServer
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, workerId, 'Empty State Test', adminOrgId)
    try {
      await loginViaToken(page, adminToken)
      await page.goto(`/o/admin/workspace/${workspaceId}`)
      await waitForWorkspaceReady(page)

      // Close any auto-created tabs (agents) via the close button
      const tabs = page.locator('[data-testid="tab"]')
      const count = await tabs.count()
      for (let i = count - 1; i >= 0; i--) {
        const closeBtn = tabs.nth(i).locator('[data-testid="tab-close"]')
        if (await closeBtn.isVisible()) {
          await closeBtn.click()
          await page.waitForTimeout(500)
        }
      }

      // Verify all tabs are actually closed
      await expect(page.locator('[data-testid="tab"]')).toHaveCount(0)

      // Empty state message should be visible
      await expect(page.getByText('No tabs in this tile')).toBeVisible()
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })

  test('should open new agent dialog when clicking agent button with no tabs', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, workerId, adminOrgId } = leapmuxServer
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, workerId, 'No Tabs Agent Dialog Test', adminOrgId)
    try {
      await loginViaToken(page, adminToken)
      await page.goto(`/o/admin/workspace/${workspaceId}`)
      await waitForWorkspaceReady(page)

      // Close all tabs to reach the empty state
      const tabs = page.locator('[data-testid="tab"]')
      const count = await tabs.count()
      for (let i = count - 1; i >= 0; i--) {
        const closeBtn = tabs.nth(i).locator('[data-testid="tab-close"]')
        if (await closeBtn.isVisible()) {
          await closeBtn.click()
          await page.waitForTimeout(500)
        }
      }
      await expect(page.locator('[data-testid="tab"]')).toHaveCount(0)

      // Click the new agent button — should open dialog, not show a toast
      await page.locator('[data-testid="new-agent-button"]').click()
      await expect(page.getByRole('heading', { name: 'New Agent' })).toBeVisible()

      // Verify no error toast was shown
      const toasts = await getRecordedToasts(page)
      const errorToasts = toasts.filter(t => t.variant === 'danger')
      expect(errorToasts).toHaveLength(0)

      // Close the dialog
      await page.keyboard.press('Escape')
      await expect(page.getByRole('heading', { name: 'New Agent' })).not.toBeVisible()
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })

  test('should open new terminal dialog when clicking terminal button with no tabs', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, workerId, adminOrgId } = leapmuxServer
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, workerId, 'No Tabs Terminal Dialog Test', adminOrgId)
    try {
      await loginViaToken(page, adminToken)
      await page.goto(`/o/admin/workspace/${workspaceId}`)
      await waitForWorkspaceReady(page)

      // Close all tabs to reach the empty state
      const tabs = page.locator('[data-testid="tab"]')
      const count = await tabs.count()
      for (let i = count - 1; i >= 0; i--) {
        const closeBtn = tabs.nth(i).locator('[data-testid="tab-close"]')
        if (await closeBtn.isVisible()) {
          await closeBtn.click()
          await page.waitForTimeout(500)
        }
      }
      await expect(page.locator('[data-testid="tab"]')).toHaveCount(0)

      // Click the new terminal button — should open dialog, not show a toast
      await page.locator('[data-testid="new-terminal-button"]').click()
      await expect(page.getByRole('heading', { name: 'New Terminal' })).toBeVisible()

      // Verify no error toast was shown
      const toasts = await getRecordedToasts(page)
      const errorToasts = toasts.filter(t => t.variant === 'danger')
      expect(errorToasts).toHaveLength(0)

      // Close the dialog
      await page.keyboard.press('Escape')
      await expect(page.getByRole('heading', { name: 'New Terminal' })).not.toBeVisible()
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })

  test('should activate next workspace after deleting the active one', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, workerId, adminOrgId } = leapmuxServer
    const workspaceId1 = await createWorkspaceViaAPI(hubUrl, adminToken, workerId, 'Delete Target WS', adminOrgId)
    const workspaceId2 = await createWorkspaceViaAPI(hubUrl, adminToken, workerId, 'Next WS', adminOrgId)
    try {
      await loginViaToken(page, adminToken)
      await page.goto(`/o/admin/workspace/${workspaceId1}`)
      await waitForWorkspaceReady(page)

      // Ensure both workspaces are visible in the sidebar before deleting
      await expect(page.locator('[data-testid^="workspace-item-"]').filter({ hasText: 'Delete Target WS' })).toBeVisible()
      await expect(page.locator('[data-testid^="workspace-item-"]').filter({ hasText: 'Next WS' })).toBeVisible()

      // Set up dialog handler for the confirm prompt
      page.on('dialog', dialog => dialog.accept())

      // Delete the active workspace
      await openWorkspaceContextMenu(page, 'Delete Target WS')
      await page.getByRole('menuitem', { name: 'Delete' }).click()

      // The deleted workspace should be gone from sidebar
      await expect(page.locator('[data-testid^="workspace-item-"]').filter({ hasText: 'Delete Target WS' })).not.toBeVisible()
      // Should navigate to the next workspace
      await expect(page).toHaveURL(/\/workspace\//)
      // Verify the 'Next WS' workspace is visible in the sidebar
      await expect(page.getByText('Next WS')).toBeVisible()
    }
    finally {
      // workspaceId1 was deleted by the test, but clean up best-effort
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId1).catch(() => {})
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId2).catch(() => {})
    }
  })
})
