import { expect, test } from './fixtures'
import { messageContents, openAgentViaUI } from './helpers/ui'

/**
 * Smoke test for the tab bar's new-agent flow. The TabBar component's
 * dropdown structure, double-click rename behavior, and click handlers are
 * exercised by direct component tests in `Tile.test.tsx` and the various
 * `tab.store.test.ts` cases. This e2e exercises the part that only a real
 * browser + real worker can verify: a new agent reaching ACTIVE status,
 * the editor becoming focused, and the session-ID footer populating after
 * the first turn completes.
 */

test.describe('TabBar Improvements', () => {
  test('new agent tab focuses editor and surfaces session ID after first turn', async ({ page, authenticatedWorkspace }) => {
    await openAgentViaUI(page)

    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await expect(editor).toBeFocused()

    await editor.click()
    await page.keyboard.type('Say "hello". Reply with just the word, nothing else.')
    await page.keyboard.press('Meta+Enter')

    // Wait for the assistant turn to land — the init message that carries
    // the session ID arrives alongside the first response.
    await expect(messageContents(page).filter({ hasText: 'hello' })).toBeVisible()

    await expect(page.locator('[data-testid="agent-info-trigger"]')).toBeVisible()
    await page.locator('[data-testid="agent-info-trigger"]').click()

    const popover = page.locator('[data-testid="agent-info-popover"]')
    await expect(popover).toBeVisible()
    // Scoped to THIS popover. The same card also hangs off the `[+]` menu's
    // "Agent info" item, and a popover keeps its children mounted while closed,
    // so both copies of every row are in the DOM at all times -- an unscoped row
    // locator matches two elements and fails strict mode.
    const sessionIdValue = popover.locator('[data-testid="session-id-value"]')
    await expect(sessionIdValue).toBeVisible()
    const text = await sessionIdValue.textContent()
    expect(text?.length ?? 0).toBeGreaterThan(0)
  })

  /**
   * The tab used to swallow `contextmenu` and offer nothing in its place, which is
   * strictly worse than leaving the browser's own menu alone. It now carries the
   * actions the tab already had by other means.
   */
  test('right-click on a tab opens its menu, and Rename starts the inline edit', async ({ page, authenticatedWorkspace }) => {
    await openAgentViaUI(page)

    const tab = page.locator('[data-testid="tab"]:visible').first()
    await expect(tab).toBeVisible()

    await tab.click({ button: 'right' })

    // Scoped to the OPEN popover: every tab mounts its own menu, and a popover
    // keeps its children in the DOM while closed.
    const menu = page.locator('[data-testid="tab-bar-tab-menu"]:popover-open')
    await expect(menu).toBeVisible()
    await expect(menu.locator('[data-testid="tab-menu-rename"]')).toBeVisible()
    await expect(menu.locator('[data-testid="tab-menu-close"]')).toBeVisible()
    // Proves the whole `tabPop` chain -- TileRenderer builds it per tab from the
    // same function the tile-level affordance uses, TabBar forwards it, the menu
    // renders it. The tile is in the main window here, so it reads "Pop out".
    await expect(menu.locator('[data-testid="tab-menu-pop"]')).toHaveText('Pop out to floating window')
    // Tile-scoped actions stay on the tile's own surfaces, not on a tab's menu.
    await expect(menu.locator('[data-testid="split-vertical"]')).toHaveCount(0)
    await expect(menu.locator('[data-testid="make-grid-menu-item"]')).toHaveCount(0)

    await menu.locator('[data-testid="tab-menu-rename"]').click()
    await expect(page.locator('[data-testid="tab-rename-input"]:visible')).toBeFocused()
  })
})
