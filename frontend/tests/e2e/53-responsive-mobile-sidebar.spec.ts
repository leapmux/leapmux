import type { Page } from '@playwright/test'
import { expect, test } from './fixtures'
import { waitForWorkspaceReady } from './helpers/ui'

/** Switch to a smartphone-sized viewport, reload, and wait for the mobile layout. */
async function switchToMobile(page: Page) {
  await page.setViewportSize({ width: 375, height: 667 })
  await page.reload()
  await waitForWorkspaceReady(page)
  // Wait for mobile toggle buttons to appear (indicates mobile layout is active)
  await expect(page.getByRole('button', { name: 'Toggle workspaces' })).toBeVisible()
}

test.describe('Responsive Mobile Sidebar', () => {
  test('should hide desktop sidebars and show toggle buttons on mobile', async ({ page, authenticatedWorkspace }) => {
    await switchToMobile(page)

    // Desktop sidebar sections should not be visible (they're off-screen)
    const leftSidebar = page.locator('[data-testid="sidebar-left"]')
    await expect(leftSidebar).not.toBeInViewport()

    const rightSidebar = page.locator('[data-testid="sidebar-right"]')
    await expect(rightSidebar).not.toBeInViewport()

    // Mobile toggle buttons should be visible in the tab bar
    await expect(page.getByRole('button', { name: 'Toggle workspaces' })).toBeVisible()
    await expect(page.getByRole('button', { name: 'Toggle files' })).toBeVisible()

    // Desktop resize handles should not exist
    await expect(page.locator('[data-testid="resize-handle"]')).toHaveCount(0)
  })

  test('should open and close left sidebar', async ({ page, authenticatedWorkspace }) => {
    await switchToMobile(page)

    const leftToggle = page.getByRole('button', { name: 'Toggle workspaces' })
    const leftSidebar = page.locator('[data-testid="sidebar-left"]')

    // Initially the left sidebar should be off-screen
    await expect(leftSidebar).not.toBeInViewport()

    // Click the toggle to open the left sidebar
    await leftToggle.click()

    // Left sidebar should now be visible (in viewport)
    await expect(leftSidebar).toBeInViewport()

    // The "In Progress" section header should be visible
    await expect(page.locator('[data-testid="section-header-workspaces_in_progress"]')).toBeVisible()

    // Overlay should be present
    const overlay = page.locator('[class*="mobileOverlay"]')
    await expect(overlay).toBeVisible()

    // Click the uncovered strip of the overlay (right edge, outside the 80%-wide sidebar)
    await overlay.click({ position: { x: 350, y: 333 } })

    // Left sidebar should be off-screen again
    await expect(leftSidebar).not.toBeInViewport()

    // Overlay should be gone
    await expect(overlay).not.toBeVisible()
  })

  test('should open and close right sidebar', async ({ page, authenticatedWorkspace }) => {
    await switchToMobile(page)

    const rightToggle = page.getByRole('button', { name: 'Toggle files' })
    const rightSidebar = page.locator('[data-testid="sidebar-right"]')

    // Initially the right sidebar should be off-screen
    await expect(rightSidebar).not.toBeInViewport()

    // Click the toggle to open the right sidebar
    await rightToggle.click()

    // Right sidebar should now be visible
    await expect(rightSidebar).toBeInViewport()

    // The "Files" section header should be visible
    await expect(page.locator('[data-testid="section-header-files-summary"]')).toBeVisible()

    // Overlay should be present
    const overlay = page.locator('[class*="mobileOverlay"]')
    await expect(overlay).toBeVisible()

    // Click the uncovered strip of the overlay (left edge, outside the 80%-wide sidebar)
    await overlay.click({ position: { x: 25, y: 333 } })

    // Right sidebar should be off-screen again
    await expect(rightSidebar).not.toBeInViewport()
  })

  test('should close left sidebar when toggle is clicked again', async ({ page, authenticatedWorkspace }) => {
    await switchToMobile(page)

    const leftToggle = page.getByRole('button', { name: 'Toggle workspaces' })
    const leftSidebar = page.locator('[data-testid="sidebar-left"]')

    // Open
    await leftToggle.click()
    await expect(leftSidebar).toBeInViewport()

    // Close via the toggle button (not the overlay)
    await leftToggle.click()
    await expect(leftSidebar).not.toBeInViewport()
  })

  test('should close right sidebar when toggle is clicked again', async ({ page, authenticatedWorkspace }) => {
    await switchToMobile(page)

    const rightToggle = page.getByRole('button', { name: 'Toggle files' })
    const rightSidebar = page.locator('[data-testid="sidebar-right"]')

    // Open
    await rightToggle.click()
    await expect(rightSidebar).toBeInViewport()

    // Close via the toggle button (not the overlay)
    await rightToggle.click()
    await expect(rightSidebar).not.toBeInViewport()
  })

  test('should close open sidebar when the other sidebar is opened', async ({ page, authenticatedWorkspace }) => {
    await switchToMobile(page)

    const leftToggle = page.getByRole('button', { name: 'Toggle workspaces' })
    const rightToggle = page.getByRole('button', { name: 'Toggle files' })
    const leftSidebar = page.locator('[data-testid="sidebar-left"]')
    const rightSidebar = page.locator('[data-testid="sidebar-right"]')

    // Open left sidebar
    await leftToggle.click()
    await expect(leftSidebar).toBeInViewport()
    await expect(rightSidebar).not.toBeInViewport()

    // Open right sidebar — left should close
    await rightToggle.click()
    await expect(rightSidebar).toBeInViewport()
    await expect(leftSidebar).not.toBeInViewport()
  })
})
