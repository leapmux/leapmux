import type { Page } from '@playwright/test'
import { expect, test } from './fixtures'
import { firstAssistantBubble } from './helpers/ui'

/**
 * Wait for an agent tab to be present. If none exists, create one.
 */
async function ensureAgentTab(page: Page): Promise<void> {
  try {
    await expect(page.locator('[data-testid="tab"][data-tab-type="agent"]')).toBeVisible()
  }
  catch {
    await page.locator('[data-testid^="new-agent-button"]').first().click()
    await expect(page.locator('[data-testid="tab"][data-tab-type="agent"]')).toBeVisible()
  }
  // Wait for auto-created agents from the worker to settle.
  await page.waitForTimeout(2000)
}

async function sendMessage(page: Page, message: string) {
  const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
  await expect(editor).toBeVisible()
  await editor.click()
  await page.keyboard.type(message)
  await page.keyboard.press('Meta+Enter')
}

async function waitForAssistantReply(page: Page) {
  await expect(firstAssistantBubble(page)).toBeVisible({ timeout: 30_000 })
}

/** Wait for the agent to finish its turn (no more streaming/thinking indicators). */
async function waitForTurnComplete(page: Page) {
  // Wait for the interrupt button and thinking indicator to disappear (agent finished responding)
  await expect(page.locator('[data-testid="interrupt-button"]')).not.toBeVisible({ timeout: 60_000 })
  await expect(page.locator('[data-testid="thinking-indicator"]')).not.toBeVisible()
}

test.describe('Chat Pagination & Scroll', () => {
  test('should render messages with data-seq attributes', async ({ page, authenticatedWorkspace }) => {
    await ensureAgentTab(page)

    await sendMessage(page, 'Say hello.')
    await waitForAssistantReply(page)

    // Verify that message wrappers have data-seq attributes
    const seqElements = page.locator('[data-seq]')
    const count = await seqElements.count()
    expect(count).toBeGreaterThan(0)

    // Each data-seq should be a valid number string
    for (let i = 0; i < count; i++) {
      const seqValue = await seqElements.nth(i).getAttribute('data-seq')
      expect(seqValue).toBeTruthy()
      expect(Number(seqValue)).toBeGreaterThan(0)
    }
  })

  test('should show scroll-to-bottom button when scrolled up', async ({ page, authenticatedWorkspace }) => {
    // Use a small viewport to make content overflow more likely
    await page.setViewportSize({ width: 1280, height: 400 })

    await ensureAgentTab(page)

    // Send a message that generates a long response
    await sendMessage(page, 'Write a numbered list of 30 programming languages, one per line. Include a brief one-sentence description for each.')
    await waitForAssistantReply(page)
    await waitForTurnComplete(page)

    // Find the actual scrollable message container (overflow-y: auto),
    // not the wrapper (overflow: hidden) which also matches [class*="messageList"].
    const isScrollable = await page.evaluate(() => {
      const els = document.querySelectorAll<HTMLElement>('[class*="messageList"]')
      for (const el of els) {
        if (getComputedStyle(el).overflowY === 'auto') {
          return el.scrollHeight > el.clientHeight + 16
        }
      }
      return false
    })
    expect(isScrollable).toBe(true)

    // Scroll to top on the actual scrollable element
    await page.evaluate(() => {
      const els = document.querySelectorAll<HTMLElement>('[class*="messageList"]')
      for (const el of els) {
        if (getComputedStyle(el).overflowY === 'auto') {
          el.scrollTop = 0
          return
        }
      }
    })
    await page.waitForTimeout(300)

    // Verify we're not at the bottom
    const atBottom = await page.evaluate(() => {
      const els = document.querySelectorAll<HTMLElement>('[class*="messageList"]')
      for (const el of els) {
        if (getComputedStyle(el).overflowY === 'auto') {
          return el.scrollHeight - el.scrollTop - el.clientHeight < 32
        }
      }
      return true
    })
    expect(atBottom).toBe(false)
  })

  test('should not auto-scroll when user is scrolled up', async ({ page, authenticatedWorkspace }) => {
    await ensureAgentTab(page)

    // Send a message that generates a long response
    await sendMessage(page, 'Count from 1 to 50, each number on a new line.')

    // Wait for some content to appear
    await page.waitForTimeout(2000)

    const messageList = page.locator('[class*="messageList"]').first()

    // Scroll to top while content is still being generated
    await messageList.evaluate(el => el.scrollTop = 0)
    await page.waitForTimeout(200)

    // Record scroll position
    const scrollTopBefore = await messageList.evaluate(el => el.scrollTop)

    // Wait for more content to arrive
    await page.waitForTimeout(2000)

    // Scroll position should NOT have jumped to the bottom
    const scrollTopAfter = await messageList.evaluate(el => el.scrollTop)
    // Allow some small tolerance for scroll anchoring adjustments
    expect(Math.abs(scrollTopAfter - scrollTopBefore)).toBeLessThan(50)
  })

  test('should maintain chat when switching between agent tabs', async ({ page, authenticatedWorkspace }) => {
    await ensureAgentTab(page)

    // Send a message in the first agent tab and wait for full response
    await sendMessage(page, 'Say "First Agent Reply" and nothing else.')
    await waitForAssistantReply(page)
    await waitForTurnComplete(page)

    // Verify message is visible (use .first() because all agent tabs stay
    // mounted and the locator may resolve to multiple elements)
    const chatContainer = page.locator('[data-testid="chat-container"]').first()
    await expect(chatContainer).toContainText('First Agent Reply', { timeout: 30_000 })

    // Create a second agent tab
    await page.locator('[data-testid^="new-agent-button"]').first().click()
    await page.waitForTimeout(2000)

    // Switch back to the first agent tab
    const agentTabs = page.locator('[data-testid="tab"][data-tab-type="agent"]')
    await agentTabs.first().click()

    // Wait for messages to load (tab content stays mounted across switches)
    await expect(chatContainer).toContainText('First Agent Reply')
  })

  test('should show thinking indicator while agent is processing', async ({ page, authenticatedWorkspace }) => {
    await ensureAgentTab(page)

    await sendMessage(page, 'Say hello.')

    // The thinking indicator should appear while the agent is processing
    await expect(page.locator('[data-testid="thinking-indicator"]')).toBeVisible()

    // Wait for the turn to complete
    await waitForTurnComplete(page)

    // After turn completes, the thinking indicator should not be visible
    await expect(page.locator('[data-testid="thinking-indicator"]')).not.toBeVisible()
  })

  test('should scroll to bottom when switching back to a tab that was at bottom', async ({ page, authenticatedWorkspace }) => {
    // Use a small viewport so the response overflows.
    await page.setViewportSize({ width: 1280, height: 400 })
    await ensureAgentTab(page)

    // Generate a response long enough to overflow the viewport.
    await sendMessage(page, 'Write a numbered list of 30 programming languages, one per line. Include a brief one-sentence description for each.')
    await waitForAssistantReply(page)
    await waitForTurnComplete(page)

    // Verify the chat content is scrollable (overflows).
    const isScrollable = await page.evaluate(() => {
      const els = document.querySelectorAll<HTMLElement>('[class*="messageList"]')
      for (const el of els) {
        if (getComputedStyle(el).overflowY === 'auto')
          return el.scrollHeight > el.clientHeight + 16
      }
      return false
    })
    expect(isScrollable).toBe(true)

    // Record "at bottom" state before switching.
    const wasAtBottom = await page.evaluate(() => {
      const els = document.querySelectorAll<HTMLElement>('[class*="messageList"]')
      for (const el of els) {
        if (getComputedStyle(el).overflowY === 'auto')
          return el.scrollHeight - el.scrollTop - el.clientHeight < 32
      }
      return false
    })
    expect(wasAtBottom).toBe(true)

    // Create a second agent tab (switches to it automatically).
    await page.locator('[data-testid^="new-agent-button"]').first().click()
    await page.waitForTimeout(2000)

    // Switch back to the first agent tab.
    const agentTabs = page.locator('[data-testid="tab"][data-tab-type="agent"]')
    await agentTabs.first().click()
    await page.waitForTimeout(500)

    // The chat should still be scrolled to the bottom.
    const isAtBottom = await page.evaluate(() => {
      const els = document.querySelectorAll<HTMLElement>('[class*="messageList"]')
      for (const el of els) {
        if (getComputedStyle(el).overflowY === 'auto')
          return el.scrollHeight - el.scrollTop - el.clientHeight < 32
      }
      return false
    })
    expect(isAtBottom).toBe(true)
  })

  test('should scroll to bottom when turn completes while tab is hidden', async ({ page, authenticatedWorkspace }) => {
    // Use a small viewport so the response overflows.
    await page.setViewportSize({ width: 1280, height: 400 })
    await ensureAgentTab(page)

    // Send a message that generates a long response.
    await sendMessage(page, 'Write a numbered list of 30 programming languages, one per line. Include a brief one-sentence description for each.')

    // Wait for the agent to start responding (thinking indicator or streaming).
    await expect(
      page.locator('[data-testid="thinking-indicator"], [data-testid="interrupt-button"]').first(),
    ).toBeVisible()

    // Switch to a new agent tab while the turn is still in progress.
    await page.locator('[data-testid^="new-agent-button"]').first().click()
    await page.waitForTimeout(1000)

    // Wait for the first agent's turn to complete while its tab is hidden.
    // We can't use the UI indicators directly since they're on the hidden tab,
    // so poll until no agent is in ACTIVE+working state on the first tab.
    const firstAgentTab = page.locator('[data-testid="tab"][data-tab-type="agent"]').first()

    // Switch back briefly to check, then away again — or just wait long enough.
    // The simplest approach: wait for the turn to complete (the turn-complete
    // indicators are still tracked even when the tab is hidden).
    await page.waitForTimeout(30_000)

    // Switch back to the first agent tab.
    await firstAgentTab.click()
    await page.waitForTimeout(500)

    // Verify the turn actually completed and content overflows.
    await waitForTurnComplete(page)
    const isScrollable = await page.evaluate(() => {
      const els = document.querySelectorAll<HTMLElement>('[class*="messageList"]')
      for (const el of els) {
        if (getComputedStyle(el).overflowY === 'auto')
          return el.scrollHeight > el.clientHeight + 16
      }
      return false
    })
    expect(isScrollable).toBe(true)

    // The chat should be scrolled to the bottom despite the turn having
    // completed while the tab was hidden.
    const isAtBottom = await page.evaluate(() => {
      const els = document.querySelectorAll<HTMLElement>('[class*="messageList"]')
      for (const el of els) {
        if (getComputedStyle(el).overflowY === 'auto')
          return el.scrollHeight - el.scrollTop - el.clientHeight < 32
      }
      return false
    })
    expect(isAtBottom).toBe(true)
  })

  test('should load initial messages when opening existing agent', async ({ page, authenticatedWorkspace }) => {
    await ensureAgentTab(page)

    // Send messages to build up some history
    await sendMessage(page, 'Say "Message One" and nothing else.')
    await waitForAssistantReply(page)
    await waitForTurnComplete(page)

    // Reload the page to test initial message loading
    await page.reload()

    // The chat should show the previous messages
    const chatContainer = page.locator('[data-testid="chat-container"]')
    await expect(chatContainer).toContainText('Message One')
  })
})
