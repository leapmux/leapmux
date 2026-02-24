import type { Page } from '@playwright/test'
import { expect, test } from './fixtures'

async function sendAndWaitForReply(page: Page, message: string) {
  const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
  await expect(editor).toBeVisible()
  await editor.click()
  await page.keyboard.type(message)
  await page.keyboard.press('Meta+Enter')

  // Wait for at least one assistant bubble to appear
  await expect(
    page.locator('[data-testid="message-bubble"][data-role="assistant"]').first(),
  ).toBeVisible()
}

test.describe('Chat Message Rendering', () => {
  test('should render user message as human-friendly text, not raw JSON', async ({ page, authenticatedWorkspace }) => {
    await sendAndWaitForReply(page, 'Hello world')

    const userBubble = page.locator('[data-testid="message-bubble"][data-role="user"]').first()
    await expect(userBubble).toBeVisible()

    const content = userBubble.locator('[data-testid="message-content"]')
    await expect(content).toContainText('Hello world')
    await expect(content).not.toContainText('{"content":')
  })

  test('should render assistant message as markdown', async ({ page, authenticatedWorkspace }) => {
    await sendAndWaitForReply(page, 'What is 2+2? Reply with just the number.')

    // Wait for turn to complete so the text bubble is present
    await expect(page.locator('[data-testid="interrupt-button"]')).not.toBeVisible({ timeout: 30_000 })

    // Find an assistant bubble whose message-content contains <p> tags
    // (skip thinking bubbles and turn-end indicators which have no <p>)
    const assistantBubble = page.locator(
      '[data-testid="message-bubble"][data-role="assistant"]',
    ).filter({
      has: page.locator('[data-testid="message-content"] p'),
    }).first()

    const content = assistantBubble.locator('[data-testid="message-content"]')

    // Content should be rendered as HTML elements (e.g. <p> tags), not raw text
    const hasParagraph = await content.locator('p').count()
    expect(hasParagraph).toBeGreaterThan(0)
  })

  test('should show floating toolbar on hover', async ({ page, authenticatedWorkspace }) => {
    await sendAndWaitForReply(page, 'Hi')

    const bubble = page.locator('[data-testid="message-bubble"]').first()
    const row = bubble.locator('..') // parent messageRow div
    // Check the copy-JSON button (which has opacity:0 by default via toolHeaderButtonHidden).
    // Playwright's toBeVisible() does not consider opacity:0 as hidden, so we check CSS directly.
    const copyButton = row.locator('[data-testid="message-copy-json"]')

    // Move mouse away first to ensure no hover state
    await page.mouse.move(0, 0)

    // Hidden toolbar button should have opacity 0 initially
    await expect(copyButton).toHaveCSS('opacity', '0')

    // Hover over the bubble — row hover rule reveals hidden buttons
    await bubble.hover()
    await expect(copyButton).toHaveCSS('opacity', '1')

    // Move mouse away
    await page.mouse.move(0, 0)
    await expect(copyButton).toHaveCSS('opacity', '0')
  })

  test('should copy Raw JSON to clipboard on button click', async ({ page, context, authenticatedWorkspace }) => {
    await context.grantPermissions(['clipboard-read', 'clipboard-write'])
    await sendAndWaitForReply(page, 'Hello')

    const userBubble = page.locator('[data-testid="message-bubble"][data-role="user"]').first()
    const userRow = userBubble.locator('..') // parent messageRow div

    // Hover and click the copy JSON button
    await userBubble.hover()
    const copyButton = userRow.locator('[data-testid="message-copy-json"]')
    await expect(copyButton).toBeVisible()
    await copyButton.click()

    // Clipboard should contain JSON with "content"
    const clipboardText = await page.evaluate(() => navigator.clipboard.readText())
    expect(clipboardText).toContain('"content"')
  })
})
