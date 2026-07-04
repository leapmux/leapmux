import type { Page } from '@playwright/test'
import { expect, test } from './fixtures'

/** Open a new terminal via the dedicated terminal button in the tab bar. */
async function openTerminalViaUI(page: Page) {
  await page.locator('[data-testid="new-terminal-button"]').click()
}

/** Number of terminals currently holding a live WebGL context. */
function webglTerminalCount(page: Page): Promise<number> {
  return page.evaluate(() => (window as any).__webglTerminalCount?.() ?? -1)
}

/** Which renderer ('webgl' | 'dom') the given terminal id is currently using. */
function rendererFor(page: Page, terminalId: string): Promise<string> {
  return page.evaluate(id => (window as any).__terminalRendererFor?.(id) ?? 'unknown', terminalId)
}

/** The terminal ids currently mounted, split by whether the tab is active. */
function terminalIds(page: Page): Promise<{ active: string[], hidden: string[] }> {
  return page.evaluate(() => {
    const active: string[] = []
    const hidden: string[] = []
    for (const el of document.querySelectorAll<HTMLElement>('[data-terminal-id]')) {
      const id = el.dataset.terminalId!
      ;(el.dataset.active === 'true' ? active : hidden).push(id)
    }
    return { active, hidden }
  })
}

/**
 * The xterm surface of the currently-visible terminal. Scoped to the active
 * container because hidden terminal tabs stay mounted (each with its own
 * `.xterm` element on the DOM renderer), so a bare `.xterm` locator matches
 * every open terminal.
 */
function activeXterm(page: Page) {
  return page.locator('[data-terminal-id][data-active="true"] .xterm')
}

test.describe('Terminal WebGL context pool', () => {
  // Only the visible terminal in a tile should hold a WebGL context. Hidden
  // terminal tabs -- which stay mounted -- must NOT each keep their own
  // context, or a workspace with many terminals would blow past the browser's
  // simultaneous-WebGL-context cap and corrupt the evicted terminals' glyphs.
  test('keeps only the visible terminal on WebGL as tabs are opened and switched', async ({ page, authenticatedWorkspace }) => {
    // A dropped GPU context logs this marker; assert it never fires.
    const contextLostLogs: string[] = []
    page.on('console', (msg) => {
      if (msg.text().includes('terminal_renderer_webgl_context_lost'))
        contextLostLogs.push(msg.text())
    })

    // Open three terminals in the same tile. Each new terminal becomes the
    // active tab, hiding the previous one.
    await openTerminalViaUI(page)
    await expect(activeXterm(page)).toBeVisible()
    await openTerminalViaUI(page)
    await openTerminalViaUI(page)

    const terminalTabs = page.locator('[data-testid="tab"][data-tab-type="terminal"]')
    await expect(terminalTabs).toHaveCount(3)

    // Exactly one context: only the active terminal. The two hidden tabs are
    // mounted but render via the DOM renderer.
    await expect.poll(() => webglTerminalCount(page)).toBe(1)

    // Concretely: the visible terminal is on WebGL, each hidden one on DOM.
    const { active, hidden } = await terminalIds(page)
    expect(active).toHaveLength(1)
    expect(hidden).toHaveLength(2)
    await expect.poll(() => rendererFor(page, active[0])).toBe('webgl')
    for (const id of hidden)
      expect(await rendererFor(page, id)).toBe('dom')

    // Switching tabs must move the single context to whichever terminal is
    // now visible -- never accumulate one per tab.
    await terminalTabs.nth(0).click()
    await expect(activeXterm(page)).toBeVisible()
    await expect.poll(() => webglTerminalCount(page)).toBe(1)

    await terminalTabs.nth(1).click()
    await expect(activeXterm(page)).toBeVisible()
    await expect.poll(() => webglTerminalCount(page)).toBe(1)

    // No terminal ever lost its GPU context (which would corrupt its glyphs).
    expect(contextLostLogs).toEqual([])
  })
})
