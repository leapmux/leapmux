import type { Page } from '@playwright/test'
import { expect, test } from './fixtures'
import { activeXterm } from './helpers/terminal'
import { openTerminalViaUI, terminalTabs } from './helpers/ui'

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
    await openTerminalViaUI(page)
    await openTerminalViaUI(page)

    const terminals = terminalTabs(page)
    await expect(terminals).toHaveCount(3)

    // Exactly one context: only the active terminal. The two hidden tabs are
    // mounted but render via the DOM renderer.
    await expect.poll(() => webglTerminalCount(page)).toBe(1)

    // Concretely: the visible terminal is on WebGL, each hidden one on DOM.
    const { active, hidden } = await terminalIds(page)
    expect(active).toHaveLength(1)
    expect(hidden).toHaveLength(2)
    const activeId = active[0]
    if (activeId === undefined)
      throw new Error('expected exactly one active terminal')
    await expect.poll(() => rendererFor(page, activeId)).toBe('webgl')
    for (const id of hidden)
      expect(await rendererFor(page, id)).toBe('dom')

    // Switching tabs must move the single context to whichever terminal is
    // now visible -- never accumulate one per tab.
    await terminals.nth(0).click()
    await expect(activeXterm(page)).toBeVisible()
    await expect.poll(() => webglTerminalCount(page)).toBe(1)

    await terminals.nth(1).click()
    await expect(activeXterm(page)).toBeVisible()
    await expect.poll(() => webglTerminalCount(page)).toBe(1)

    // No terminal ever lost its GPU context (which would corrupt its glyphs).
    expect(contextLostLogs).toEqual([])
  })
})
