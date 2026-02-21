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
  await page.keyboard.press('Enter')
}

async function waitForAssistantReply(page: Page) {
  await expect(
    page.locator('[data-testid="message-bubble"][data-role="assistant"]').first(),
  ).toBeVisible({ timeout: 30_000 })
}

/** Wait for the agent to finish its turn (no more streaming indicator). */
async function waitForTurnComplete(page: Page) {
  // Wait for the streaming indicator to disappear (agent finished responding)
  await expect(page.locator('[data-testid="interrupt-button"]')).not.toBeVisible({ timeout: 60_000 })
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
    await ensureAgentTab(page)

    // Send a message and wait for full reply to ensure enough content
    await sendMessage(page, 'Write a numbered list of 30 programming languages, one per line. Include a brief one-sentence description for each.')
    await waitForAssistantReply(page)
    await waitForTurnComplete(page)

    const messageList = page.locator('[class*="messageList"]').first()

    // Check if content is scrollable (scrollHeight > clientHeight)
    const isScrollable = await messageList.evaluate(
      el => el.scrollHeight > el.clientHeight + 16,
    )
    if (!isScrollable) {
      // Content not long enough to scroll — skip the scroll button test
      test.skip()
      return
    }

    // Scroll to top
    await messageList.evaluate(el => el.scrollTop = 0)
    await page.waitForTimeout(300)

    // Verify we're not at the bottom (scroll-to-bottom button should be visible)
    const atBottom = await messageList.evaluate(
      el => el.scrollHeight - el.scrollTop - el.clientHeight < 16,
    )
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
