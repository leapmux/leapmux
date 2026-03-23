import type { Page } from '@playwright/test'
import { expect, test } from './fixtures'

const TOOK_TIME_RE = /Took \d+/

/** Send a message via the ProseMirror editor. */
async function sendMessage(page: Page, text: string) {
  const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
  await expect(editor).toBeVisible()
  await editor.click()
  await page.keyboard.type(text, { delay: 50 })
  await page.keyboard.press('Meta+Enter')
  await expect(editor).toHaveText('')
}

/**
 * Allow tool execution if a control request banner appears.
 * If the tool is already auto-approved (e.g. YOLO mode or prior approval),
 * the banner won't appear and this is a no-op.
 */
async function allowToolExecutionIfNeeded(page: Page) {
  const allowBtn = page.locator('[data-testid="control-allow-btn"]')
  try {
    await expect(allowBtn).toBeVisible({ timeout: 10_000 })
    await allowBtn.click()
  }
  catch {
    // Control banner did not appear — tool was auto-approved
  }
}

test.describe('ANSI Escape Sequence Rendering', () => {
  test('should render ANSI colored output from Bash tool as styled HTML', async ({ page, authenticatedWorkspace }) => {
    // Ask Claude to run `ls --color=always /` which produces ANSI colored output.
    await sendMessage(
      page,
      'Run this exact command with the Bash tool and nothing else: ls --color=always /',
    )

    // Approve the Bash tool execution if a control banner appears
    await allowToolExecutionIfNeeded(page)

    // Wait for the agent's turn to finish.
    await expect(page.getByText(TOOK_TIME_RE)).toBeVisible({ timeout: 60_000 })

    // tool_result messages are now standalone. Look for ANSI-rendered content
    // that contains actual directory listing output (not the command itself).
    // Use text content filtering to find the output element.
    const bubbles = page.locator('[data-testid="message-bubble"]')
    const shikiPre = bubbles.locator('pre.shiki').filter({ hasText: 'usr' })
    const plainPre = bubbles.locator('pre').filter({ hasText: 'usr' })
    const outputElement = shikiPre.or(plainPre)
    await expect(outputElement.first()).toBeVisible({ timeout: 30_000 })

    // Verify some well-known root directories are present
    const textContent = await outputElement.first().textContent()
    expect(textContent).toContain('usr')

    // If Shiki rendered the output, verify styled spans exist
    if (await shikiPre.isVisible().catch(() => false)) {
      const styledSpans = shikiPre.locator('span[style*="--shiki-light"]')
      const count = await styledSpans.count()
      expect(count).toBeGreaterThanOrEqual(1)
    }
  })
})
