import { expect, test } from './fixtures'

test.describe('Worker Management', () => {
  test('should show Workers section with registered worker', async ({ page, authenticatedWorkspace }) => {
    // Workers section should be visible in the sidebar
    const workersSection = page.getByTestId('section-header-workers')
    await expect(workersSection).toBeVisible()

    // Expand the section if collapsed
    const isOpen = await workersSection.evaluate((el: HTMLDetailsElement) => el.open)
    if (!isOpen)
      await workersSection.locator('> summary').click()

    // Should contain the worker named "Local" (standalone sets LEAPMUX_WORKER_NAME=Local)
    await expect(workersSection.getByText('Local')).toBeVisible()
  })

  test('should show green status dot for online worker', async ({ page, authenticatedWorkspace }) => {
    const workersSection = page.getByTestId('section-header-workers')
    await expect(workersSection).toBeVisible()

    const isOpen = await workersSection.evaluate((el: HTMLDetailsElement) => el.open)
    if (!isOpen)
      await workersSection.locator('> summary').click()

    // The status dot should indicate "connected"
    await expect(workersSection.locator('[data-status="connected"]')).toBeVisible()
  })
})
