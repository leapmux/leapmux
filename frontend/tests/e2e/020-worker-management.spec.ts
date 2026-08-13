import { expect, test } from './fixtures'
import { expectClipsLongText, expectClipsToOneLine } from './helpers/ui'

test.describe('Worker Management', () => {
  test('should show Workers section with registered worker', async ({ page, authenticatedWorkspace }) => {
    // Workers section should be visible in the sidebar
    const workersSection = page.getByTestId('section-header-workers')
    await expect(workersSection).toBeVisible()

    // Expand the section if collapsed
    const isOpen = await workersSection.evaluate(el => !el.hasAttribute('data-closed'))
    if (!isOpen)
      await workersSection.locator('> [role="button"]').click()

    // Should contain the worker named "Local" (dev mode sets LEAPMUX_WORKER_NAME=Local)
    await expect(workersSection.getByTestId('worker-name').filter({ hasText: 'Local' })).toBeVisible()
  })

  test('should show green status dot for online worker', async ({ page, authenticatedWorkspace }) => {
    const workersSection = page.getByTestId('section-header-workers')
    await expect(workersSection).toBeVisible()

    const isOpen = await workersSection.evaluate(el => !el.hasAttribute('data-closed'))
    if (!isOpen)
      await workersSection.locator('> [role="button"]').click()

    // The status dot should indicate "connected"
    await expect(workersSection.locator('[data-status="connected"]')).toBeVisible()
  })

  /**
   * A worker row clips its name rather than widening the list.
   *
   * The list used to render into the workspace list's container, which sizes to
   * its widest row so a deep path can be scrolled into view. A worker row has
   * no such depth, and that width made the ellipsis unreachable: a long name
   * scrolled the whole sidebar section sideways instead of clipping.
   *
   * The dev worker is called `Local`, which fits any sidebar width, and the
   * name already declared the whole clipping quartet before the container was
   * fixed. So the declarations pass on the broken code too, and only a name
   * longer than its box tells the two containers apart --
   * `expectClipsLongText` supplies one.
   */
  test('clips the worker name and never scrolls the section sideways', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace
    const workersSection = page.getByTestId('section-header-workers')
    await expect(workersSection).toBeVisible()

    const isOpen = await workersSection.evaluate(el => !el.hasAttribute('data-closed'))
    if (!isOpen)
      await workersSection.locator('> [role="button"]').click()

    const name = workersSection.getByTestId('worker-name').filter({ hasText: 'Local' })
    await expect(name).toBeVisible()
    await expectClipsToOneLine(name)
    await expectClipsLongText(name)
  })
})
