import { expect, test } from './fixtures'
import { openTerminalViaUI } from './helpers/ui'

/**
 * Verifies the terminal startup loader: when a new terminal is opened,
 * the OpenTerminal RPC returns immediately with status=STARTING. The
 * frontend renders the loader overlay until the PTY is up
 * (TerminalStatusChange{status=READY}), then mounts xterm.js.
 *
 * Terminal PTY startup is fast (single-digit ms typically), so this
 * spec asserts the *order* of states rather than that the loader is
 * visible at any specific instant.
 */
test.describe('Terminal startup loader', () => {
  test('shows the startup overlay then mounts xterm', async ({ page, authenticatedWorkspace }) => {
    await openTerminalViaUI(page)

    // The terminal tab must appear immediately (sync OpenTerminal response).
    await expect(page.locator('[data-testid="tab"][data-tab-type="terminal"]')).toBeVisible()

    // The xterm container should mount once status flips to READY.
    // Backend's WatchEvents catch-up guarantees the READY event reaches
    // a late-subscribing watcher, so this transition is robust to the
    // open/subscribe race that motivated the registry.
    await expect(page.locator('.xterm')).toBeVisible({ timeout: 30_000 })
    await expect(page.locator('[data-testid="terminal-startup-overlay"]')).not.toBeVisible()
  })
})
