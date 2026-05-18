import type { Page } from '@playwright/test'
import { expect } from '@playwright/test'

/**
 * Read terminal text content from the active xterm's buffer. The WebGL
 * renderer leaves DOM rows empty, so prefer the window hook exposed by
 * TerminalView; fall back to the DOM for the rare case where xterm has
 * mounted before the hook registers.
 */
export async function getTerminalText(page: Page): Promise<string> {
  return page.evaluate(() => {
    if (typeof (window as any).__getActiveTerminalText === 'function') {
      return (window as any).__getActiveTerminalText() as string
    }
    const containers = document.querySelectorAll<HTMLElement>('[data-terminal-id]')
    for (const container of containers) {
      if (container.dataset.active === 'true') {
        const rows = container.querySelector('.xterm-rows')
        if (rows)
          return rows.textContent ?? ''
      }
    }
    return document.querySelector('.xterm-rows')?.textContent ?? ''
  })
}

/** Wait until terminal text contains the expected string. */
export async function waitForTerminalText(page: Page, text: string, timeout?: number) {
  await expect(async () => {
    const content = await getTerminalText(page)
    expect(content).toContain(text)
  }).toPass(timeout != null ? { timeout } : undefined)
}

/**
 * Send input to the active terminal via the same callback xterm's
 * onData fires. Returns false when no terminal is registered with the
 * window hook (e.g. xterm not mounted yet).
 */
export async function sendActiveTerminalInput(page: Page, text: string): Promise<boolean> {
  return page.evaluate((s) => {
    const fn = (window as any).__sendActiveTerminalInput
    return typeof fn === 'function' ? fn(s) === true : false
  }, text)
}
