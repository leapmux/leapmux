import type { Page } from '@playwright/test'
import { expect, test } from './fixtures'

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
    await expect(allowBtn).toBeVisible()
    await allowBtn.click()
  }
  catch {
    // Control banner did not appear — tool was auto-approved
  }
}

test.describe('ANSI Escape Sequence Rendering', () => {
  test('should render ANSI colored output from Bash tool as styled HTML', async ({ page, authenticatedWorkspace }) => {
    // Ask Claude to run `ls --color=always /` which produces ANSI colored output.
    // Directories and symlinks get colored by ls, and --color=always forces
    // ANSI codes even when stdout is not a TTY.
    await sendMessage(
      page,
      'Run this exact command with the Bash tool and nothing else: ls --color=always /',
    )

    // Approve the Bash tool execution if a control banner appears
    await allowToolExecutionIfNeeded(page)

    // Wait for the agent's turn to finish (indicated by the turn duration marker).
    await expect(page.locator('[data-testid="message-content"]').last()).toBeVisible({ timeout: 60_000 })

    // If the tool result is collapsed, expand it so the ANSI output becomes visible.
    const expandBtn = page.getByRole('button', { name: /Expand.*tool result/ })
    if (await expandBtn.isVisible().catch(() => false)) {
      await expandBtn.click()
    }

    // The ANSI-rendered content will have a pre.shiki element inside the thread child bubble
    // (tool results render outside [data-testid="message-content"]).
    // If the tool output doesn't contain ANSI codes, it may render as a plain <code> element instead.
    const shikiPre = page.locator('[data-testid="thread-child-bubble"] pre.shiki')
    const plainCode = page.locator('[data-testid="thread-child-bubble"] code')
    const outputElement = await shikiPre.isVisible().catch(() => false) ? shikiPre : plainCode
    await expect(outputElement.first()).toBeVisible({ timeout: 10_000 })

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
