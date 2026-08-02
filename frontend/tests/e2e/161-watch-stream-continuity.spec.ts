import type { Page } from '@playwright/test'
import { expect, test } from './fixtures'
import { createWorkspaceViaAPI, deleteWorkspaceViaAPI, openAgentViaAPI } from './helpers/api'
import { waitForTerminalText } from './helpers/terminal'
import { armTurnEndSound, expectDoorbellCount } from './helpers/turnEndSound'
import {
  loginViaToken,
  openAgentViaUI,
  openTerminalViaUI,
  openWorkspace,
  sendMessage,
  waitForWorkspaceReady,
  workspaceRow,
} from './helpers/ui'

/** Count of WatchEvents channel opens observed via the LEAPMUX_DEV hook. */
async function watchOpenCount(page: Page): Promise<number> {
  return page.evaluate(() => (window as unknown as { __watchOpens?: number }).__watchOpens ?? 0)
}

async function installWatchOpenCounter(page: Page) {
  await page.addInitScript(() => {
    ;(window as unknown as { __watchOpens?: number }).__watchOpens = 0
    window.addEventListener('leapmux:watch-events-open', () => {
      const w = window as unknown as { __watchOpens?: number }
      w.__watchOpens = (w.__watchOpens ?? 0) + 1
    })
  })
}

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

const TOOL_USING_PROMPT = 'Run the command `pwd` and tell me the result.'

test.describe('WatchEvents stream continuity', () => {
  test('tab and workspace switches revise interest without reopening the stream', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, workerId } = leapmuxServer
    const ws1 = await createWorkspaceViaAPI(hubUrl, adminToken, 'Watch Continuity A')
    const ws2 = await createWorkspaceViaAPI(hubUrl, adminToken, 'Watch Continuity B')
    await openAgentViaAPI(hubUrl, adminToken, workerId, ws1)
    await openAgentViaAPI(hubUrl, adminToken, workerId, ws2)

    try {
      await installWatchOpenCounter(page)
      await loginViaToken(page, adminToken)
      await openWorkspace(page, ws1)
      await waitForWorkspaceReady(page)

      // Arm after first load so the init script + sound pref both stick across reload.
      await armTurnEndSound(page, 'ding-dong')
      await waitForWorkspaceReady(page)

      // First open after reload — baseline for "no further opens".
      await expect.poll(() => watchOpenCount(page)).toBeGreaterThanOrEqual(1)
      const opensAtStart = await watchOpenCount(page)

      // Second agent tab in ws1 + a terminal.
      await openAgentViaUI(page)
      await expect(page.locator('[data-testid="tab"][data-tab-type="agent"]')).toHaveCount(2)
      await openTerminalViaUI(page)
      await expect(page.locator('[data-testid="tab"][data-tab-type="terminal"]')).toBeVisible()
      await expect(page.locator('.xterm')).toBeVisible()

      await typeInTerminal(page, 'echo CONT_TERM')
      await waitForTerminalText(page, 'CONT_TERM')

      // Flick between agent tabs and the terminal.
      const agentTabs = page.locator('[data-testid="tab"][data-tab-type="agent"]')
      const termTab = page.locator('[data-testid="tab"][data-tab-type="terminal"]').first()
      await agentTabs.first().click()
      await agentTabs.nth(1).click()
      await termTab.click()
      await agentTabs.first().click()

      // Hidden agent still receives turn-end notify (sound).
      await sendMessage(page, TOOL_USING_PROMPT)
      await agentTabs.nth(1).click()
      await expectDoorbellCount(page, 1)

      // Cross-workspace switches.
      await workspaceRow(page, ws2).click()
      await waitForWorkspaceReady(page)
      await expect(page.locator('[data-testid="tab"][data-tab-type="agent"]').first()).toBeVisible()

      await workspaceRow(page, ws1).click()
      await waitForWorkspaceReady(page)
      await agentTabs.first().click()
      await termTab.click()
      await waitForTerminalText(page, 'CONT_TERM')

      // Interest revisions must not tear down / re-open WatchEvents.
      expect(await watchOpenCount(page)).toBe(opensAtStart)
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, ws1).catch(() => {})
      await deleteWorkspaceViaAPI(hubUrl, adminToken, ws2).catch(() => {})
    }
  })
})
