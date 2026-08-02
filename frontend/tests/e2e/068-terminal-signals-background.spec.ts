import type { Page } from '@playwright/test'
import { expect, test } from './fixtures'
import { waitForTerminalText } from './helpers/terminal'
import { openTerminalViaUI, waitForWorkspaceReady } from './helpers/ui'

/** Type a command into the active terminal and press Enter. */
async function typeInTerminal(page: Page, command: string) {
  await page.evaluate(() => {
    const containers = document.querySelectorAll<HTMLElement>('[data-terminal-id]')
    for (const container of containers) {
      if (container.dataset.active === 'true') {
        const textarea = container.querySelector<HTMLTextAreaElement>('.xterm-helper-textarea')
        if (textarea) {
          textarea.focus()
          return
        }
      }
    }
  })
  await page.keyboard.type(command, { delay: 20 })
  await page.keyboard.press('Enter')
}

/** Terminal tab label text with chrome nodes stripped. */
async function terminalTabLabel(page: Page, index: number): Promise<string> {
  return page.locator('[data-testid="tab"][data-tab-type="terminal"]').nth(index).evaluate((el) => {
    const clone = el.cloneNode(true) as HTMLElement
    clone.querySelectorAll('[data-testid="tab-close"], [data-testid="tab-notification"], [data-testid="tab-progress"]').forEach(n => n.remove())
    return (clone.textContent ?? '').trim()
  })
}

test.describe('Terminal signals while backgrounded', () => {
  test('bell, title, and OSC 9 reach a hidden terminal tab', async ({ page, authenticatedWorkspace }) => {
    void authenticatedWorkspace
    await waitForWorkspaceReady(page)

    await openTerminalViaUI(page)
    const termTab = page.locator('[data-testid="tab"][data-tab-type="terminal"]').first()
    await expect(termTab).toBeVisible()
    await expect(page.locator('.xterm')).toBeVisible()

    // Seed a marker so we can prove the screen is still current after switch-back.
    await typeInTerminal(page, 'echo SIGMARKER')
    await waitForTerminalText(page, 'SIGMARKER')

    // Cover the terminal with the workspace's existing agent tab.
    const agentTab = page.locator('[data-testid="tab"][data-tab-type="agent"]').first()
    await agentTab.click()
    await expect(agentTab).toHaveAttribute('aria-selected', 'true')

    // Re-select the terminal briefly to fire the escape sequences, then hide it
    // again so the resulting NOTIFY events land while it is not workspace-active.
    await termTab.click()
    await expect(termTab).toHaveAttribute('aria-selected', 'true')

    // Queue signals that fire after a short delay, then hide the tab before they land.
    await typeInTerminal(page, 'sleep 0.4; printf \'\\a\'; sleep 0.2; printf \'\\033]0;newtitle\\a\'; sleep 0.2; printf \'\\033]9;hi\\a\'; echo SIGNALS_DONE')
    await agentTab.click()
    await expect(agentTab).toHaveAttribute('aria-selected', 'true')

    // Bell badges the backgrounded terminal.
    await expect(termTab.locator('[data-testid="tab-notification"]')).toBeVisible()

    // Title update arrives without the tab being visible.
    await expect.poll(() => terminalTabLabel(page, 0)).toBe('newtitle')

    // OSC 9 badges (already) and toasts when OS notifications are not opted in.
    await expect(page.locator('output .toast-message').filter({ hasText: 'hi' })).toBeVisible()

    // Switch back: retained buffer + catch-up keep the screen current.
    await termTab.click()
    await expect(termTab).toHaveAttribute('aria-selected', 'true')
    await waitForTerminalText(page, 'SIGNALS_DONE')
    await waitForTerminalText(page, 'SIGMARKER')
  })
})
