import { expect, test } from './fixtures'
import { inviteToOrgViaAPI, loginViaUI, shareWorkerViaAPI } from './helpers'

test.describe('Worker Management', () => {
  // Ensure newuser is invited to admin's org and worker sharing is reset
  // to private before tests run.
  test.beforeAll(async ({ leapmuxServer }) => {
    const { hubUrl, adminToken, adminOrgId, workerId } = leapmuxServer
    try {
      await inviteToOrgViaAPI(hubUrl, adminToken, adminOrgId, 'newuser')
    }
    catch {
      // Ignore if already invited
    }
    // Ensure worker starts as private
    await shareWorkerViaAPI(hubUrl, adminToken, workerId, 'SHARE_MODE_PRIVATE')
  })

  // Revert worker sharing to private after all tests to avoid affecting other tests
  test.afterAll(async ({ leapmuxServer }) => {
    const { hubUrl, adminToken, workerId } = leapmuxServer
    try {
      await shareWorkerViaAPI(hubUrl, adminToken, workerId, 'SHARE_MODE_PRIVATE')
    }
    catch {
      // Best effort cleanup
    }
  })

  test('should show Workers link in user menu and navigate to page', async ({ page }) => {
    await loginViaUI(page)
    // Click the user menu trigger
    await page.getByTestId('user-menu-trigger').first().click()
    // Workers menu item should be visible
    const workersItem = page.getByRole('menuitem', { name: 'Workers' })
    await expect(workersItem).toBeVisible()
    await workersItem.click()
    // Should navigate to workers page
    await expect(page).toHaveURL(/\/o\/admin\/workers/)
    await expect(page.getByRole('heading', { name: 'Workers' })).toBeVisible()
  })

  test('should show My Workers section with registered worker', async ({ page }) => {
    await loginViaUI(page)
    await page.goto('/o/admin/workers')
    await expect(page.getByRole('heading', { name: 'Workers' })).toBeVisible()

    // My Workers section should exist
    const ownedSection = page.getByTestId('worker-section-owned')
    await expect(ownedSection).toBeVisible()
    await expect(ownedSection.getByText('My Workers')).toBeVisible()

    // Should contain the Local worker card (standalone auto-registers with name "Local")
    await expect(ownedSection.getByTestId('worker-name').filter({ hasText: 'Local' })).toBeVisible()
  })

  test('should show worker properties (hostname, status, share mode)', async ({ page }) => {
    await loginViaUI(page)
    await page.goto('/o/admin/workers')
    await expect(page.getByRole('heading', { name: 'Workers' })).toBeVisible()

    const card = page.getByTestId('worker-card').first()
    // Hostname should be visible
    await expect(card.getByTestId('worker-hostname')).toBeVisible()
    // Status badge should show Online or Offline
    await expect(card.getByTestId('worker-status')).toBeVisible()
    // Share mode badge should be visible (for owned workers)
    await expect(card.getByTestId('worker-share-mode')).toBeVisible()
  })

  test('should rename a worker', async ({ page }) => {
    await loginViaUI(page)
    await page.goto('/o/admin/workers')
    await expect(page.getByRole('heading', { name: 'Workers' })).toBeVisible()

    // Open context menu
    await page.getByTestId('worker-menu-trigger').first().click()
    await page.getByRole('menuitem', { name: 'Rename' }).click()

    // Rename dialog should appear
    await expect(page.getByRole('heading', { name: 'Worker Settings' })).toBeVisible()
    const input = page.getByTestId('rename-input')
    await expect(input).toBeVisible()

    // Clear and type new name
    await input.fill('renamed-worker')
    await page.getByRole('button', { name: 'Save' }).click()

    // Dialog should close and new name should appear
    await expect(page.getByRole('heading', { name: 'Worker Settings' })).not.toBeVisible()
    await expect(page.getByTestId('worker-name').filter({ hasText: 'renamed-worker' })).toBeVisible()

    // Rename back to original name
    await page.getByTestId('worker-menu-trigger').first().click()
    await page.getByRole('menuitem', { name: 'Rename' }).click()
    await expect(page.getByTestId('rename-input')).toBeVisible()
    await page.getByTestId('rename-input').fill('Local')
    await page.getByRole('button', { name: 'Save' }).click()
    await expect(page.getByRole('heading', { name: 'Worker Settings' })).not.toBeVisible()
    await expect(page.getByTestId('worker-name').filter({ hasText: 'Local' })).toBeVisible()
  })

  test('should open sharing dialog and change to org sharing', async ({ page }) => {
    await loginViaUI(page)
    await page.goto('/o/admin/workers')
    await expect(page.getByRole('heading', { name: 'Workers' })).toBeVisible()

    // Open context menu and click Sharing
    await page.getByTestId('worker-menu-trigger').first().click()
    await page.getByRole('menuitem', { name: 'Sharing' }).click()

    // Sharing dialog should appear
    const dialog = page.getByRole('dialog')
    await expect(dialog.getByRole('heading', { name: 'Worker Settings' })).toBeVisible()
    await expect(dialog.getByText('Private')).toBeVisible()
    await expect(dialog.getByText('All org members')).toBeVisible()
    await expect(dialog.getByText('Specific members')).toBeVisible()

    // Select "All org members"
    await dialog.getByText('All org members').click()
    await dialog.getByRole('button', { name: 'Save' }).click()

    // Dialog should close
    await expect(dialog).not.toBeVisible()

    // Share mode badge should show "Org"
    await expect(page.getByTestId('worker-share-mode').filter({ hasText: 'Org' })).toBeVisible()
  })

  test('should show shared worker to non-owner in Shared section', async ({ page }) => {
    // Login as newuser (created in fixture setup, invited to admin org in beforeAll)
    await loginViaUI(page, 'newuser', 'password123')
    await page.goto('/o/admin/workers')
    await expect(page.getByRole('heading', { name: 'Workers' })).toBeVisible()

    // Shared section should exist with the shared worker
    const sharedSection = page.getByTestId('worker-section-shared')
    await expect(sharedSection).toBeVisible()
    await expect(sharedSection.getByText('Shared with me')).toBeVisible()
    await expect(sharedSection.getByTestId('worker-name').filter({ hasText: 'Local' })).toBeVisible()
  })

  test('should not show context menu for non-owner', async ({ page }) => {
    await loginViaUI(page, 'newuser', 'password123')
    await page.goto('/o/admin/workers')
    await expect(page.getByRole('heading', { name: 'Workers' })).toBeVisible()

    // Shared section should have no menu trigger
    const sharedSection = page.getByTestId('worker-section-shared')
    await expect(sharedSection).toBeVisible()
    await expect(sharedSection.getByTestId('worker-menu-trigger')).not.toBeVisible()
  })

  test('should revert sharing to private', async ({ page }) => {
    await loginViaUI(page)
    await page.goto('/o/admin/workers')
    await expect(page.getByRole('heading', { name: 'Workers' })).toBeVisible()

    // Open sharing dialog and revert to private
    await page.getByTestId('worker-menu-trigger').first().click()
    await page.getByRole('menuitem', { name: 'Sharing' }).click()
    const dialog = page.getByRole('dialog')
    await expect(dialog.getByRole('heading', { name: 'Worker Settings' })).toBeVisible()
    await dialog.getByText('Private').click()
    await dialog.getByRole('button', { name: 'Save' }).click()
    await expect(dialog).not.toBeVisible()

    // Share mode badge should show "Private"
    await expect(page.getByTestId('worker-share-mode').filter({ hasText: 'Private' })).toBeVisible()
  })
})
