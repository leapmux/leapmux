import { expect, test } from './fixtures'
import { createWorkspaceViaAPI, deleteWorkspaceViaAPI } from './helpers/api'
import { getRecordedToasts } from './helpers/toast'
import { loginViaToken, openWorkspace, workspaceRow } from './helpers/ui'

test.describe('Workspace UX Enhancements', () => {
  test('should auto-activate first workspace on app home', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken } = leapmuxServer
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Auto Activate Test')
    try {
      await loginViaToken(page, adminToken)
      await openWorkspace(page, workspaceId)

      // Load the app home cold
      await page.goto('/')
      await expect(page.locator('[data-testid="section-header-workspaces_in_progress"]')).toBeVisible()

      // Should auto-activate a workspace rather than sit on an empty shell.
      // There is no URL to check any more -- the sidebar row is where the
      // active selection is observable.
      await expect(workspaceRow(page, workspaceId))
        .toHaveAttribute('data-active', 'true')
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })

  test('should open new workspace dialog from sidebar + button', async ({ page, leapmuxServer }) => {
    await loginViaToken(page, leapmuxServer.adminToken)
    // Navigate to app home first
    await page.goto('/')
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
    const { hubUrl, adminToken } = leapmuxServer
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Empty State Test')
    try {
      await loginViaToken(page, adminToken)
      await openWorkspace(page, workspaceId)

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

      // Empty state actions should be visible
      await expect(page.locator('[data-testid="empty-tile-actions"]')).toBeVisible()
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })

  test('should open new agent dialog when clicking agent button with no tabs', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken } = leapmuxServer
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'No Tabs Agent Dialog Test')
    try {
      await loginViaToken(page, adminToken)
      await openWorkspace(page, workspaceId)

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

      // Click the empty-state agent button — should open dialog, not show a toast
      await page.locator('[data-testid="empty-tile-open-agent"]').click()
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
    const { hubUrl, adminToken } = leapmuxServer
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'No Tabs Terminal Dialog Test')
    try {
      await loginViaToken(page, adminToken)
      await openWorkspace(page, workspaceId)

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

      // Click the button DIRECTLY, not through `openTerminalViaUI`: that helper
      // waits for the active tab's working directory, and this test's whole
      // subject is the no-tab state where there is no such context and the
      // dialog is the correct answer. Waiting for it here would hang forever.
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
    const { hubUrl, adminToken } = leapmuxServer
    const workspaceId1 = await createWorkspaceViaAPI(hubUrl, adminToken, 'Delete Target WS')
    const workspaceId2 = await createWorkspaceViaAPI(hubUrl, adminToken, 'Next WS')
    try {
      await loginViaToken(page, adminToken)
      await openWorkspace(page, workspaceId1)

      // Ensure both workspaces are visible in the sidebar before deleting
      await expect(page.locator('[data-testid^="workspace-item-"]').filter({ hasText: 'Delete Target WS' })).toBeVisible()
      await expect(page.locator('[data-testid^="workspace-item-"]').filter({ hasText: 'Next WS' })).toBeVisible()

      // Delete the active workspace
      const deleteTarget = workspaceRow(page, workspaceId1)
      await deleteTarget.hover()
      await deleteTarget.locator('button').first().click()
      await page.getByRole('menuitem', { name: 'Delete' }).click()

      // Confirm the delete via ConfirmDialog (danger mode: arm then confirm)
      const dialog = page.locator('dialog')
      await expect(dialog).toBeVisible()
      await dialog.getByRole('button', { name: 'Delete' }).click()
      await dialog.getByRole('button', { name: 'Confirm?' }).click()

      // The deleted workspace should be gone from sidebar
      await expect(page.locator('[data-testid^="workspace-item-"]').filter({ hasText: 'Delete Target WS' })).not.toBeVisible()
      // Should switch to the surviving workspace rather than leave the shell
      // pointed at the one that was just deleted.
      await expect(workspaceRow(page, workspaceId2))
        .toHaveAttribute('data-active', 'true')
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
