import type { Page } from '@playwright/test'
import { expect, test } from './fixtures'

/**
 * Simulate a drag gesture on the resize handle by dispatching native MouseEvents
 * directly on the handle element. This bypasses potential coordinate-targeting issues
 * with `page.mouse.*` and ensures SolidJS event handlers receive the events.
 */
async function dragResizeHandle(page: Page, deltaY: number) {
  const handle = page.locator('[data-testid="editor-resize-handle"]')
  await handle.dispatchEvent('mousedown', { clientX: 100, clientY: 300, bubbles: true })
  // Dispatch mousemove on document (the handler attaches to document)
  for (let i = 1; i <= 10; i++) {
    const y = 300 + (deltaY * i) / 10
    await page.evaluate(
      ([cy]) => document.dispatchEvent(new MouseEvent('mousemove', { clientX: 100, clientY: cy, bubbles: true })),
      [y] as const,
    )
  }
  await page.evaluate(() =>
    document.dispatchEvent(new MouseEvent('mouseup', { bubbles: true })),
  )
  // Allow SolidJS reactivity to update
  await page.waitForTimeout(200)
}

test.describe('Editor Resize Handle', () => {
  test.afterEach(async ({ page }) => {
    // Clear the stored editor min height so tests don't leak state
    await page.evaluate(() => localStorage.removeItem('leapmux-editor-min-height'))
  })

  test('should resize editor by dragging handle up', async ({ page, authenticatedWorkspace }) => {
    const wrapper = page.locator('[data-testid="chat-editor"]')
    await expect(wrapper).toBeVisible()
    // Wait for ProseMirror to initialize inside the editor wrapper
    await expect(wrapper.locator('.ProseMirror')).toBeVisible()
    await page.waitForTimeout(500)

    const heightBefore = await wrapper.evaluate(el => el.getBoundingClientRect().height)

    // Drag the resize handle upward by 50px (negative deltaY = upward)
    await dragResizeHandle(page, -50)

    await expect(async () => {
      const heightAfter = await wrapper.evaluate(el => el.getBoundingClientRect().height)
      expect(heightAfter).toBeGreaterThan(heightBefore)
    }).toPass({ timeout: 5000 })
  })

  test('should resize editor by dragging handle down after enlarging', async ({ page, authenticatedWorkspace }) => {
    const wrapper = page.locator('[data-testid="chat-editor"]')
    await expect(wrapper).toBeVisible()
    await expect(wrapper.locator('.ProseMirror')).toBeVisible()
    await page.waitForTimeout(500)

    // First drag up to enlarge
    await dragResizeHandle(page, -80)

    await expect(async () => {
      const heightAfterEnlarge = await wrapper.evaluate(el => el.getBoundingClientRect().height)
      expect(heightAfterEnlarge).toBeGreaterThan(50)
    }).toPass({ timeout: 5000 })

    const heightAfterEnlarge = await wrapper.evaluate(el => el.getBoundingClientRect().height)

    // Now drag down to shrink
    await dragResizeHandle(page, 40)

    await expect(async () => {
      const heightAfterShrink = await wrapper.evaluate(el => el.getBoundingClientRect().height)
      expect(heightAfterShrink).toBeLessThan(heightAfterEnlarge)
    }).toPass({ timeout: 5000 })
  })

  test('should not shrink below default minimum height', async ({ page, authenticatedWorkspace }) => {
    const wrapper = page.locator('[data-testid="chat-editor"]')
    await expect(wrapper).toBeVisible()
    await page.waitForTimeout(500)

    // Drag handle downward by a large amount
    await dragResizeHandle(page, 200)

    // Editor should not be smaller than the default minimum (38px)
    const height = await wrapper.evaluate(el => el.getBoundingClientRect().height)
    expect(height).toBeGreaterThanOrEqual(38)
  })

  test('should not grow beyond maximum height', async ({ page, authenticatedWorkspace }) => {
    const wrapper = page.locator('[data-testid="chat-editor"]')
    await expect(wrapper).toBeVisible()
    await page.waitForTimeout(500)

    // Drag handle upward by a very large amount
    await dragResizeHandle(page, -500)

    // The editor's min-height is clamped to 75% of the center panel height.
    // Verify the editor stays well within the viewport height after a large drag.
    const height = await wrapper.evaluate(el => el.getBoundingClientRect().height)
    const viewportHeight = page.viewportSize()!.height
    // Should be less than 75% of viewport (the panel is smaller than the viewport,
    // so the actual cap is even lower, but this is a safe upper bound)
    expect(height).toBeLessThan(viewportHeight * 0.75)
  })

  test('should persist resize height across page reload', async ({ page, authenticatedWorkspace }) => {
    const wrapper = page.locator('[data-testid="chat-editor"]')
    await expect(wrapper).toBeVisible()
    await expect(wrapper.locator('.ProseMirror')).toBeVisible()
    await page.waitForTimeout(500)

    // Drag handle upward to set a custom min height
    await dragResizeHandle(page, -60)

    // Wait for localStorage to be written after drag
    let storedVal = 0
    await expect(async () => {
      const stored = await page.evaluate(() => localStorage.getItem('leapmux-editor-min-height'))
      expect(stored).not.toBeNull()
      storedVal = Number.parseInt(stored!, 10)
      expect(storedVal).toBeGreaterThan(38)
    }).toPass({ timeout: 5000 })

    // Reload the page and wait for the workspace to re-render
    await page.waitForTimeout(500)
    await page.reload()
    await expect(wrapper).toBeVisible()
    await expect(wrapper.locator('.ProseMirror')).toBeVisible()
    await page.waitForTimeout(500)

    // Editor should use the stored min height
    await expect(async () => {
      const heightAfterReload = await wrapper.evaluate(el => el.getBoundingClientRect().height)
      expect(heightAfterReload).toBeGreaterThanOrEqual(storedVal)
    }).toPass({ timeout: 5000 })
  })

  test('should reset height on double-click', async ({ page, authenticatedWorkspace }) => {
    const wrapper = page.locator('[data-testid="chat-editor"]')
    await expect(wrapper).toBeVisible()
    await expect(wrapper.locator('.ProseMirror')).toBeVisible()
    await page.waitForTimeout(500)

    // Drag handle upward to set a custom min height
    await dragResizeHandle(page, -60)

    // Verify it was enlarged
    await expect(async () => {
      const heightEnlarged = await wrapper.evaluate(el => el.getBoundingClientRect().height)
      expect(heightEnlarged).toBeGreaterThan(50)
    }).toPass({ timeout: 5000 })

    // Double-click the resize handle to reset
    const handle = page.locator('[data-testid="editor-resize-handle"]')
    await handle.dblclick()

    // Height should be back to default (~38px CSS min-height)
    await expect(async () => {
      const heightReset = await wrapper.evaluate(el => el.getBoundingClientRect().height)
      expect(heightReset).toBeLessThanOrEqual(50)
    }).toPass({ timeout: 5000 })

    // localStorage key should be removed
    const stored = await page.evaluate(() => localStorage.getItem('leapmux-editor-min-height'))
    expect(stored).toBeNull()
  })

  test('should still auto-grow when content exceeds manual minimum', async ({ page, authenticatedWorkspace }) => {
    const wrapper = page.locator('[data-testid="chat-editor"]')
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await page.waitForTimeout(500)

    // Drag handle up to set minHeight to ~60px
    await dragResizeHandle(page, -25)

    const heightAfterResize = await wrapper.evaluate(el => el.getBoundingClientRect().height)

    // Switch to Alt+Enter mode so Enter creates newlines
    await page.locator('button:has-text("Enter sends")').click()

    // Type many lines to exceed the manual minimum
    await editor.click()
    for (let i = 0; i < 10; i++) {
      await page.keyboard.type(`Line ${i + 1}`, { delay: 100 })
      await page.keyboard.press('Enter')
    }

    await expect(async () => {
      const heightAfterTyping = await wrapper.evaluate(el => el.getBoundingClientRect().height)
      expect(heightAfterTyping).toBeGreaterThan(heightAfterResize)
    }).toPass({ timeout: 5000 })
  })
})
