import type { Page } from '@playwright/test'
import { expect, test } from './fixtures'

const EDITOR_HEIGHT_KEY_PREFIX = 'leapmux-editor-min-height-'

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

/**
 * Get the stored per-agent editor height value.
 */
async function getStoredEditorHeight(page: Page): Promise<string | null> {
  return page.evaluate((prefix) => {
    for (let i = 0; i < localStorage.length; i++) {
      const key = localStorage.key(i)
      if (key?.startsWith(prefix))
        return localStorage.getItem(key)
    }
    return null
  }, EDITOR_HEIGHT_KEY_PREFIX)
}

/**
 * Remove all per-agent editor height localStorage entries.
 */
async function clearEditorHeightKeys(page: Page): Promise<void> {
  await page.evaluate((prefix) => {
    const keysToRemove: string[] = []
    for (let i = 0; i < localStorage.length; i++) {
      const key = localStorage.key(i)
      if (key?.startsWith(prefix))
        keysToRemove.push(key)
    }
    keysToRemove.forEach(k => localStorage.removeItem(k))
  }, EDITOR_HEIGHT_KEY_PREFIX)
}

test.describe('Editor Resize Handle', () => {
  test.afterEach(async ({ page }) => {
    // Clear the stored editor min height so tests don't leak state
    await clearEditorHeightKeys(page)
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
    // Should be at most 75% of viewport (the max editor height is 75% of the container)
    expect(height).toBeLessThanOrEqual(viewportHeight * 0.75)
  })

  test('should persist resize height across page reload', async ({ page, authenticatedWorkspace }) => {
    const wrapper = page.locator('[data-testid="chat-editor"]')
    await expect(wrapper).toBeVisible()
    await expect(wrapper.locator('.ProseMirror')).toBeVisible()
    await page.waitForTimeout(500)

    // Drag handle upward to set a custom height
    await dragResizeHandle(page, -60)

    // Wait for localStorage to be written after drag
    let storedVal = 0
    await expect(async () => {
      const stored = await getStoredEditorHeight(page)
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

    // Editor should use the stored height (acts as min-height when content is small)
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
    const stored = await getStoredEditorHeight(page)
    expect(stored).toBeNull()
  })

  test('should constrain height and scroll when content exceeds requested height', async ({ page, authenticatedWorkspace }) => {
    const wrapper = page.locator('[data-testid="chat-editor"]')
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await page.waitForTimeout(500)

    // Drag handle up to set requestedHeight to ~60px
    await dragResizeHandle(page, -25)

    const heightAfterResize = await wrapper.evaluate(el => el.getBoundingClientRect().height)

    // Default mode is Cmd+Enter-to-send, so Enter creates newlines

    // Type many lines to exceed the requested height
    await editor.click()
    for (let i = 0; i < 10; i++) {
      await page.keyboard.type(`Line ${i + 1}`, { delay: 100 })
      await page.keyboard.press('Enter')
    }

    // Height should stay approximately the same (constrained mode)
    await expect(async () => {
      const heightAfterTyping = await wrapper.evaluate(el => el.getBoundingClientRect().height)
      // Allow a small tolerance (±5px) for the constrained height
      expect(heightAfterTyping).toBeLessThanOrEqual(heightAfterResize + 5)
    }).toPass({ timeout: 5000 })

    // The editor should be scrollable (content overflows)
    const isScrollable = await wrapper.evaluate(el => el.scrollHeight > el.clientHeight)
    expect(isScrollable).toBe(true)
  })

  test('should reset min-height override when editor becomes empty after dragging to minimum', async ({ page, authenticatedWorkspace }) => {
    const wrapper = page.locator('[data-testid="chat-editor"]')
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await page.waitForTimeout(500)

    // Record the natural default height before any drag.
    const defaultHeight = await wrapper.evaluate(el => el.getBoundingClientRect().height)

    // Drag the resize handle upward to enlarge the editor first,
    // then drag it back down to the minimum — this creates a stored
    // min-height override at or below the EDITOR_MIN_HEIGHT (38px).
    await dragResizeHandle(page, -60)
    await dragResizeHandle(page, 200) // well past minimum → clamped to 38px

    // A per-agent localStorage key should NOT exist because the value
    // is at the minimum (the onMouseUp handler skips storage for values
    // that are <= EDITOR_MIN_HEIGHT).
    const storedAfterDrag = await getStoredEditorHeight(page)
    expect(storedAfterDrag).toBeNull()

    // The in-memory signal still holds the clamped value (38).
    // Verify the editor has a min-height override applied (the wrapper
    // should be approximately 38px — the clamped value).
    await expect(async () => {
      const h = await wrapper.evaluate(el => el.getBoundingClientRect().height)
      expect(h).toBeGreaterThanOrEqual(35) // ~38px with tolerance
      expect(h).toBeLessThanOrEqual(50)
    }).toPass({ timeout: 5000 })

    // Type some text into the editor.
    await editor.click()
    await page.keyboard.type('hello')
    await page.waitForTimeout(200)

    // Now clear the editor completely (select all + delete).
    await page.keyboard.press('Meta+A')
    await page.keyboard.press('Backspace')
    await page.waitForTimeout(300)

    // The min-height override should be cleared because the content is
    // empty and the override was at the minimum. The editor should be
    // back to its natural default height.
    await expect(async () => {
      const h = await wrapper.evaluate(el => el.getBoundingClientRect().height)
      // Should be at or near the natural default height (no override).
      expect(h).toBeLessThanOrEqual(defaultHeight + 5)
    }).toPass({ timeout: 5000 })

    // localStorage key should still be absent.
    const storedAfterClear = await getStoredEditorHeight(page)
    expect(storedAfterClear).toBeNull()
  })
})
