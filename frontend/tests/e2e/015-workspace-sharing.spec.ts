import { expect, test } from './fixtures'
import { createWorkspaceViaAPI, deleteWorkspaceViaAPI, getUserId, inviteToOrgViaAPI, shareWorkspaceViaAPI } from './helpers/api'
import { loginViaToken, loginViaUI, waitForWorkspaceReady } from './helpers/ui'

const ORG_ADMIN_URL_RE = /\/o\/admin/

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
    const { hubUrl, adminToken, adminOrgId } = leapmuxServer
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Share Test Workspace', adminOrgId)
    try {
      await loginViaToken(page, adminToken)
      await page.goto(`/o/admin/workspace/${workspaceId}`)
      await waitForWorkspaceReady(page)

      // Open context menu on the workspace item
      const wsItem = page.locator(`[data-testid="workspace-item-${workspaceId}"]`)
      await wsItem.hover()
      await wsItem.locator('button').first().click()

      // Share option should be visible in the context menu
      await expect(page.getByRole('menuitem', { name: 'Share' })).toBeVisible()
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })

  test('should open sharing dialog via context menu', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, adminOrgId } = leapmuxServer
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Share Dialog Test', adminOrgId)
    try {
      await loginViaToken(page, adminToken)
      await page.goto(`/o/admin/workspace/${workspaceId}`)
      await waitForWorkspaceReady(page)

      // Open context menu and click Share
      const wsItem = page.locator(`[data-testid="workspace-item-${workspaceId}"]`)
      await wsItem.hover()
      await wsItem.locator('button').first().click()
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
    const { hubUrl, adminToken, newuserToken, adminOrgId } = leapmuxServer

    // Use API calls for setup to stay within timeout
    const workspaceId = await createWorkspaceViaAPI(
      hubUrl,
      adminToken,
      'Shared Workspace',
      adminOrgId,
    )
    try {
      // Use SHARE_MODE_MEMBERS since the Worker doesn't support SHARE_MODE_ORG.
      const newuserUserId = await getUserId(hubUrl, newuserToken)
      await shareWorkspaceViaAPI(hubUrl, adminToken, workspaceId, 'SHARE_MODE_MEMBERS', [newuserUserId])

      // Login as newuser and verify the shared workspace is visible
      await loginViaUI(page, 'newuser', 'password123')

      // Navigate to admin's org
      await page.goto('/o/admin')
      await expect(page).toHaveURL(ORG_ADMIN_URL_RE)

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
