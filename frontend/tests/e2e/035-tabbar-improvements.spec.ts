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
})
