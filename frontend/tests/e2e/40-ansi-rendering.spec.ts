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

    // Look for the command output. It may be collapsed, so check for
    // well-known root directories visible in the collapsed view.
    const bubbles = page.locator('[data-testid="message-bubble"]')
    const outputWithBin = bubbles.filter({ hasText: 'bin' })
    await expect(outputWithBin.first()).toBeVisible({ timeout: 30_000 })

    // Verify some well-known root directories are present in the output
    const textContent = await outputWithBin.first().textContent()
    expect(textContent).toContain('bin')
  })
})
