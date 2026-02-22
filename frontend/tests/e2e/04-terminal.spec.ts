import type { Page } from '@playwright/test'
import { expect, test } from './fixtures'
import { waitForLayoutSave } from './helpers'

/** Read terminal text content from the active xterm's buffer (WebGL renderer makes DOM rows empty). */
async function getTerminalText(page: Page): Promise<string> {
  return page.evaluate(() => {
    // Use xterm buffer API exposed by TerminalView component
    if (typeof (window as any).__getActiveTerminalText === 'function') {
      return (window as any).__getActiveTerminalText() as string
    }
    // Fallback: DOM-based reading
    const containers = document.querySelectorAll<HTMLElement>('[data-terminal-id]')
    for (const container of containers) {
      if (container.style.display !== 'none') {
        const rows = container.querySelector('.xterm-rows')
        if (rows)
          return rows.textContent ?? ''
      }
    }
    return document.querySelector('.xterm-rows')?.textContent ?? ''
  })
}

/** Wait until terminal text contains the expected string. */
async function waitForTerminalText(page: Page, text: string, timeout?: number) {
  await expect(async () => {
    const content = await getTerminalText(page)
    expect(content).toContain(text)
  }).toPass(timeout != null ? { timeout } : undefined)
}

/** Type a command into the active terminal and press Enter. */
async function typeInTerminal(page: Page, command: string) {
  // Focus the textarea inside the visible (active) terminal container
  await page.evaluate(() => {
    const containers = document.querySelectorAll<HTMLElement>('[data-terminal-id]')
    for (const container of containers) {
      if (container.style.display !== 'none') {
        const textarea = container.querySelector<HTMLTextAreaElement>('.xterm-helper-textarea')
        if (textarea) {
          textarea.focus()
          return
        }
      }
    }
  })
  await page.keyboard.type(command, { delay: 30 })
  await page.keyboard.press('Enter')
}

/** Open a new terminal via the dedicated terminal button in the tab bar. */
async function openTerminalViaUI(page: Page) {
  await page.locator('[data-testid="new-terminal-button"]').click()
}

test.describe('Terminal', () => {
  test('should open a terminal and render xterm', async ({ page, authenticatedWorkspace }) => {
    // Open a new terminal via the tab bar + button
    await openTerminalViaUI(page)

    // Verify terminal tab appears in the unified tab bar
    await expect(page.locator('[data-testid="tab"][data-tab-type="terminal"]')).toBeVisible()

    // Verify xterm element is rendered
    await expect(page.locator('.xterm')).toBeVisible()
  })

  test('should preserve terminal content when switching between tabs', async ({ page, authenticatedWorkspace }) => {
    // Open terminal 1 and type a marker
    await openTerminalViaUI(page)
    await expect(page.locator('[data-testid="tab"][data-tab-type="terminal"]')).toBeVisible()
    await expect(page.locator('.xterm')).toBeVisible()
    await typeInTerminal(page, 'echo TERM1MARKER')
    await waitForTerminalText(page, 'TERM1MARKER')

    // Open terminal 2 and type a marker
    await openTerminalViaUI(page)
    // Wait for the second terminal tab to appear
    await expect(page.locator('[data-testid="tab"][data-tab-type="terminal"]').nth(1)).toBeVisible()
    // Wait for the new terminal to render
    await page.waitForTimeout(500)
    await typeInTerminal(page, 'echo TERM2MARKER')
    await waitForTerminalText(page, 'TERM2MARKER')

    // Switch back to terminal 1 tab (first terminal tab) using data-testid
    await page.locator('[data-testid="tab"][data-tab-type="terminal"]').first().click()
    await page.waitForTimeout(1000)

    // Terminal 1 content should be visible -- read from the active container
    await waitForTerminalText(page, 'TERM1MARKER', 30_000)

    // Switch back to terminal 2 tab (second terminal tab)
    await page.locator('[data-testid="tab"][data-tab-type="terminal"]').nth(1).click()
    await page.waitForTimeout(1000)
    await waitForTerminalText(page, 'TERM2MARKER', 30_000)
  })

  test('should restore terminal screen content after page refresh', async ({ page, authenticatedWorkspace }) => {
    // Start listening for the layout save before opening the terminal
    const saved = waitForLayoutSave(page)

    // Open a terminal via the tab bar
    await openTerminalViaUI(page)
    await expect(page.locator('.xterm')).toBeVisible()

    // Wait for the layout save to complete so the terminal tab is persisted
    await saved

    // Type a marker and wait for it to appear
    await typeInTerminal(page, 'echo SCREENRESTORE')
    await waitForTerminalText(page, 'SCREENRESTORE')

    // Refresh the page
    await page.reload()

    // Terminal tab should be restored automatically (no mode switch needed)
    // Click on the terminal tab to activate it
    await expect(page.locator('[data-testid="tab"][data-tab-type="terminal"]')).toBeVisible()
    await page.locator('[data-testid="tab"][data-tab-type="terminal"]').click()

    // Verify xterm is visible (terminal restored from worker)
    await expect(page.locator('.xterm')).toBeVisible()

    // Verify screen content was restored
    await waitForTerminalText(page, 'SCREENRESTORE')
  })

  test('should terminate shell in worker when terminal tab is closed', async ({ page, authenticatedWorkspace }) => {
    // Open a terminal via the tab bar
    await openTerminalViaUI(page)
    await expect(page.locator('.xterm')).toBeVisible()

    // Close the terminal tab (click the x button on the terminal tab)
    await page.locator('[data-testid="tab"][data-tab-type="terminal"] [data-testid="tab-close"]').first().click()

    // Verify no terminal tabs remain
    await expect(page.locator('[data-testid="tab"][data-tab-type="terminal"]')).not.toBeVisible()

    // Refresh the page
    await page.reload()

    // Verify no terminal tabs appear (worker killed the shell)
    await expect(page.locator('[data-testid="tab"][data-tab-type="terminal"]')).not.toBeVisible()
  })

  test('should keep terminal tab but stop input after shell exits via "exit"', async ({ page, authenticatedWorkspace }) => {
    // Open a terminal via the tab bar
    await openTerminalViaUI(page)
    await expect(page.locator('[data-testid="tab"][data-tab-type="terminal"]')).toBeVisible()
    await expect(page.locator('.xterm')).toBeVisible()

    // Type "exit" to terminate the shell
    await typeInTerminal(page, 'exit')

    // Wait a moment for the exit notification to arrive
    await page.waitForTimeout(2000)

    // The terminal tab should still be visible (not removed)
    await expect(page.locator('[data-testid="tab"][data-tab-type="terminal"]')).toBeVisible()

    // The xterm should still be visible (shows final output)
    await expect(page.locator('.xterm')).toBeVisible()

    // Verify the terminal no longer accepts input: type something and
    // confirm it does NOT appear in the terminal output
    await page.evaluate(() => {
      const containers = document.querySelectorAll<HTMLElement>('[data-terminal-id]')
      for (const container of containers) {
        if (container.style.display !== 'none') {
          const textarea = container.querySelector<HTMLTextAreaElement>('.xterm-helper-textarea')
          if (textarea) {
            textarea.focus()
            return
          }
        }
      }
    })
    await page.keyboard.type('echo SHOULD_NOT_APPEAR', { delay: 100 })
    await page.keyboard.press('Enter')
    await page.waitForTimeout(1000)
    const textAfter = await getTerminalText(page)
    expect(textAfter).not.toContain('SHOULD_NOT_APPEAR')

    // Closing the tab manually should work.
    // Use dispatchEvent to avoid Playwright actionability timeout issues
    // when xterm's helper textarea holds focus after the shell exited.
    await page.locator('[data-testid="tab"][data-tab-type="terminal"] [data-testid="tab-close"]').dispatchEvent('click')
    await expect(page.locator('[data-testid="tab"][data-tab-type="terminal"]')).not.toBeVisible()
  })

  test('should resize terminal to fit panel dimensions', async ({ page, authenticatedWorkspace }) => {
    // Start with a smaller viewport to get a small initial terminal
    await page.setViewportSize({ width: 900, height: 600 })

    // Open a terminal via the tab bar
    await openTerminalViaUI(page)
    await expect(page.locator('.xterm')).toBeVisible()

    // Wait for initial fit to complete
    await page.waitForTimeout(1000)

    // Read initial terminal row count
    const initialRows = await page.evaluate(() => {
      const rows = document.querySelector('.xterm-rows')
      return rows ? rows.children.length : 0
    })
    expect(initialRows).toBeGreaterThan(0)

    // Resize viewport much larger
    await page.setViewportSize({ width: 1600, height: 1000 })

    // Poll until row count increases (ResizeObserver + fit needs time)
    await expect(async () => {
      const newRows = await page.evaluate(() => {
        const rows = document.querySelector('.xterm-rows')
        return rows ? rows.children.length : 0
      })
      expect(newRows).toBeGreaterThan(initialRows)
    }).toPass()
  })
})
