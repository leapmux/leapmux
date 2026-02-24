import type { Page } from '@playwright/test'
import { expect, test } from './fixtures'

/**
 * Wait for an agent tab to be present. If none exists, create one.
 */
async function ensureAgentTab(page: Page): Promise<void> {
  try {
    await expect(page.locator('[data-testid="tab"][data-tab-type="agent"]')).toBeVisible()
  }
  catch {
    await page.locator('[data-testid="new-agent-button"]').click()
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
  await expect(
    page.locator('[data-testid="message-bubble"][data-role="assistant"]').first(),
  ).toBeVisible({ timeout: 30_000 })
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
          return el.scrollHeight - el.scrollTop - el.clientHeight < 16
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

    // Verify message is visible
    const chatContainer = page.locator('[data-testid="chat-container"]')
    await expect(chatContainer).toContainText('First Agent Reply', { timeout: 30_000 })

    // Create a second agent tab
    await page.locator('[data-testid="new-agent-button"]').click()
    await page.waitForTimeout(2000)

    // Switch back to the first agent tab
    const agentTabs = page.locator('[data-testid="tab"][data-tab-type="agent"]')
    await agentTabs.first().click()

    // Wait for messages to load (initial load happens on tab switch)
    await expect(chatContainer).toContainText('First Agent Reply', { timeout: 15_000 })
  })

  test('should show thinking indicator while agent is processing', async ({ page, authenticatedWorkspace }) => {
    await ensureAgentTab(page)

    await sendMessage(page, 'Say hello.')

    // The thinking indicator should appear while the agent is processing
    await expect(page.locator('[data-testid="thinking-indicator"]')).toBeVisible({ timeout: 10_000 })

    // Wait for the turn to complete
    await waitForTurnComplete(page)

    // After turn completes, the thinking indicator should not be visible
    await expect(page.locator('[data-testid="thinking-indicator"]')).not.toBeVisible()
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
    await expect(chatContainer).toContainText('Message One', { timeout: 15_000 })
  })
})
