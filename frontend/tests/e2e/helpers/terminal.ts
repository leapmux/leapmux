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

/**
 * Click a piece of the active terminal's text with a real mouse.
 *
 * The coordinates come from the app's own buffer-to-screen hook, because the
 * WebGL renderer paints to a canvas: there is no element for a Playwright
 * locator to find, and an OSC 8 hyperlink adds none of its own. Retried,
 * because the shell's output reaches the buffer after the command returns.
 */
export async function clickTerminalText(page: Page, text: string): Promise<void> {
  interface Point { x: number, y: number, awayY: number }
  let point: Point | null = null
  await expect(async () => {
    point = await page.evaluate(
      t => ((window as any).__activeTerminalPointAt?.(t) ?? null) as Point | null,
      text,
    )
    expect(point, `no terminal cell shows ${text}`).not.toBeNull()
  }).toPass()

  // Hover ANOTHER cell first. xterm activates a link on mouseup, and only for
  // the link its last hover resolved -- so a modal that opened over the
  // terminal, and took a `mouseleave` with it, leaves that link cleared. A
  // second click at the same coordinates then re-asks for nothing, because
  // xterm skips the lookup while the buffer cell under the mouse is unchanged.
  await page.mouse.move(point!.x, point!.awayY)
  await page.mouse.click(point!.x, point!.y)
}

/**
 * Split a string by an empty quote pair, so the SHELL reassembles it.
 *
 * The typed command line is echoed into the same buffer the assertions read,
 * so a caller that searched for the whole string would find the echo first --
 * on a row that holds no link and that a click therefore does nothing to.
 * Split, the command line never contains the string and only the output does.
 * `'a''b'` is one word to every POSIX shell, and `printf` receives `ab`.
 */
function reassembledByShell(text: string): string {
  const half = Math.ceil(text.length / 2)
  return `${text.slice(0, half)}''${text.slice(half)}`
}

/**
 * Print an OSC 8 hyperlink into the active terminal: `label` over `uri`.
 *
 * Written with `printf` and octal escapes so the sequence survives the shell
 * unchanged -- `echo -e` is not portable across the shells a worker may run,
 * and `\\e` is not portable inside `printf` either.
 */
export async function printTerminalHyperlink(page: Page, uri: string, label: string): Promise<void> {
  const target = reassembledByShell(uri)
  const shown = reassembledByShell(label)
  await typeInTerminal(page, `printf '\\033]8;;${target}\\033\\\\${shown}\\033]8;;\\033\\\\\\n'`)
}
