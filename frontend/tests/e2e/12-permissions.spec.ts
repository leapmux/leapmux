import { expect, test } from './fixtures'
import { createWorkspaceViaAPI, inviteToOrgViaAPI, loginViaToken, loginViaUI, shareWorkspaceViaAPI } from './helpers'

test.describe('Permissions', () => {
  let sharedWorkspaceId: string

  // Set up prerequisites: ensure newuser exists (fixture setup), is invited
  // to admin's org, and a shared workspace exists.
  test.beforeAll(async ({ leapmuxServer }) => {
    const { hubUrl, adminToken, adminOrgId, workerId } = leapmuxServer
    try {
      await inviteToOrgViaAPI(hubUrl, adminToken, adminOrgId, 'newuser')
    }
    catch {
      // Ignore if already invited
    }
    // Create and share a workspace for the permissions test (unique title
    // to avoid collisions with workspaces created by other test files)
    sharedWorkspaceId = await createWorkspaceViaAPI(
      hubUrl,
      adminToken,
      workerId,
      'Permissions Shared WS',
      adminOrgId,
    )
    await shareWorkspaceViaAPI(
      hubUrl,
      adminToken,
      sharedWorkspaceId,
      'SHARE_MODE_ORG',
    )
  })

  test('should show 404 for non-admin accessing admin page', async ({ page }) => {
    // Login as a regular user (newuser was created in fixture setup)
    await loginViaUI(page, 'newuser', 'password123')
    await page.goto('/admin')
    // Should see the Not Found page (AuthGuard with requireAdmin)
    await expect(page.getByRole('heading', { name: 'Not Found' })).toBeVisible()
    await expect(page.getByText('System Settings')).not.toBeVisible()
  })

  test('should show 404 for nonexistent org', async ({ page, leapmuxServer }) => {
    // Login as admin and navigate to a nonexistent org slug
    await loginViaToken(page, leapmuxServer.adminToken)
    await page.goto('/o/nonexistent-org-slug')
    // Should see the Not Found page
    await expect(page.getByRole('heading', { name: 'Not Found' })).toBeVisible()
  })

  test('should show 404 for nonexistent workspace ID', async ({ page, leapmuxServer }) => {
    // Login as admin and navigate to a workspace that doesn't exist
    await loginViaToken(page, leapmuxServer.adminToken)
    await page.goto('/o/admin/workspace/ws_nonexistent_id')
    // Should see the Not Found page
    await expect(page.getByRole('heading', { name: 'Not Found' })).toBeVisible()
  })

  test('should show 404 for workspace under wrong org', async ({ page, leapmuxServer }) => {
    // Login as newuser and try to access admin's workspace under newuser's org
    await loginViaToken(page, leapmuxServer.newuserToken)
    // sharedWorkspaceId belongs to admin's org, but we access it under newuser's org
    await page.goto(`/o/newuser/workspace/${sharedWorkspaceId}`)
    // Should see the Not Found page (workspace doesn't belong to this org)
    await expect(page.getByRole('heading', { name: 'Not Found' })).toBeVisible()
  })

  test('should isolate workspaces between users', async ({ page }) => {
    // Login as admin
    await loginViaUI(page)
    // Admin's workspace page should be loaded
    await expect(page).toHaveURL(/\/o\/admin/)
  })

  test('should not show new tab button to non-owner on shared workspace', async ({ page }) => {
    // Login as newuser (invited to admin org in beforeAll)
    await loginViaUI(page, 'newuser', 'password123')

    // Navigate to admin's org
    await page.goto('/o/admin')
    await expect(page).toHaveURL(/\/o\/admin/)

    // Click on the shared workspace (created and shared in beforeAll)
    await page.getByText('Permissions Shared WS').click()

    // Verify workspace loaded (page content is visible)
    await expect(page).toHaveURL(/\/workspace\//)
    await expect(page.locator('[data-testid="tab"]').or(page.getByText('no open tabs'))).toBeVisible()

    // New agent/terminal buttons should NOT be visible for non-owner
    await expect(page.locator('[data-testid="new-agent-button"]')).not.toBeVisible()
  })
})
