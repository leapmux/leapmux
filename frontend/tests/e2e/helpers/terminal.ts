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
 * Wait until the shell is actually accepting input.
 *
 * A terminal renders before its shell finishes sourcing its init files, and a
 * command typed into that window silently loses its leading characters --
 * `sleep 120 &` arrives as `eep 120 &`, which the shell then reports as a
 * command not found. Echoing a marker in a retry loop is what proves the shell
 * is past it: the attempts that get eaten simply fail the check, and the first
 * one that survives round-trips the marker back.
 *
 * The marker is split by a quote pair so the shell REASSEMBLES it. The typed
 * line therefore never contains the marker, only the output does -- otherwise a
 * mangled `ho MARKER` would still show the marker and pass while characters
 * were being dropped.
 */
export async function waitForTerminalReady(page: Page): Promise<void> {
  const marker = `RDY${Math.random().toString(36).slice(2, 8).toUpperCase()}`
  await expect(async () => {
    await typeInTerminal(page, `echo ${marker.slice(0, 3)}""${marker.slice(3)}`)
    expect(await getTerminalText(page)).toContain(marker)
  }).toPass()
}

/**
 * Focus the helper textarea of the active terminal, so keyboard input (and a
 * real input method driven over CDP) lands in xterm.
 */
export async function focusActiveTerminal(page: Page): Promise<void> {
  await page.evaluate(() => {
    const containers = document.querySelectorAll<HTMLElement>('[data-terminal-id]')
    for (const container of containers) {
      if (container.dataset.active === 'true') {
        container.querySelector<HTMLTextAreaElement>('.xterm-helper-textarea')?.focus()
        return
      }
    }
  })
}

/**
 * Type a command into the active terminal and press Enter.
 */
export async function typeInTerminal(page: Page, command: string, delay = 30): Promise<void> {
  await focusActiveTerminal(page)
  await page.keyboard.type(command, { delay })
  await page.keyboard.press('Enter')
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
