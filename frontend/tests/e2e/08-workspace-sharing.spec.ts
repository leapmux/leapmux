import { expect, test } from './fixtures'
import { createWorkspaceViaAPI, deleteWorkspaceViaAPI, inviteToOrgViaAPI, shareWorkspaceViaAPI } from './helpers/api'
import { loginViaToken, loginViaUI, openWorkspaceContextMenu, waitForWorkspaceReady } from './helpers/ui'

test.describe('Workspace Sharing', () => {
  // Ensure newuser is invited to admin's org before any test in this file.
  test.beforeAll(async ({ leapmuxServer }) => {
    const { hubUrl, adminToken, adminOrgId } = leapmuxServer
    try {
      await inviteToOrgViaAPI(hubUrl, adminToken, adminOrgId, 'newuser')
    }
    catch {
      // Ignore if already invited (idempotent)
    }
  })

  test('should show context menu with share option on owned workspace', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, workerId, adminOrgId } = leapmuxServer
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, workerId, 'Share Test Workspace', adminOrgId)
    try {
      await loginViaToken(page, adminToken)
      await page.goto(`/o/admin/workspace/${workspaceId}`)
      await waitForWorkspaceReady(page)

      // Open context menu on the workspace item
      await openWorkspaceContextMenu(page, 'Share Test Workspace')

      // Share option should be visible in the context menu
      await expect(page.getByRole('menuitem', { name: 'Share' })).toBeVisible()
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })

  test('should open sharing dialog via context menu', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, workerId, adminOrgId } = leapmuxServer
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, workerId, 'Share Dialog Test', adminOrgId)
    try {
      await loginViaToken(page, adminToken)
      await page.goto(`/o/admin/workspace/${workspaceId}`)
      await waitForWorkspaceReady(page)

      // Open context menu and click Share
      await openWorkspaceContextMenu(page, 'Share Dialog Test')
      await page.getByRole('menuitem', { name: 'Share' }).click()

      // Dialog should appear
      await expect(page.getByText('Workspace Sharing')).toBeVisible()
      await expect(page.getByText('Private')).toBeVisible()
      await expect(page.getByText('All org members')).toBeVisible()
      await expect(page.getByText('Specific members')).toBeVisible()
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })

  test('should share workspace to org and show to non-owner', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, workerId, adminOrgId } = leapmuxServer

    // Use API calls for setup to stay within timeout
    const workspaceId = await createWorkspaceViaAPI(
      hubUrl,
      adminToken,
      workerId,
      'Shared Workspace',
      adminOrgId,
    )
    try {
      await shareWorkspaceViaAPI(hubUrl, adminToken, workspaceId, 'SHARE_MODE_ORG')

      // Login as newuser and verify the shared workspace is visible
      await loginViaUI(page, 'newuser', 'password123')

      // Navigate to admin's org
      await page.goto('/o/admin')
      await expect(page).toHaveURL(/\/o\/admin/)

      // Shared workspace should be visible (in the "Shared" section)
      await expect(page.getByText('Shared Workspace')).toBeVisible()

      // The workspace item in the Shared section should NOT have a context menu trigger
      const sharedItem = page.locator('[data-testid^="workspace-item-"]').filter({ hasText: 'Shared Workspace' })
      await expect(sharedItem).toBeVisible()
      await expect(sharedItem.locator('button')).not.toBeVisible()
    }
    finally {
      // Clean up - delete the workspace via API
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })
})
