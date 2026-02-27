import type { Page } from '@playwright/test'
import { expect, test } from './fixtures'
import { PLAN_MODE_PROMPT } from './helpers'

/** Send a message via the ProseMirror editor. */
async function sendMessage(page: Page, text: string) {
  const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
  await expect(editor).toBeVisible()
  await editor.click()
  await page.keyboard.type(text, { delay: 100 })
  await page.keyboard.press('Meta+Enter')
}

/** Wait for the control request banner to appear and return a scoped locator. */
async function waitForControlBanner(page: Page) {
  const banner = page.locator('[data-testid="control-banner"]')
  await expect(banner).toBeVisible({ timeout: 60_000 })
  return banner
}

test.describe('Control Request Draft Persistence', () => {
  test('ExitPlanMode draft survives page reload', async ({ page, authenticatedWorkspace }) => {
    // Trigger EnterPlanMode then ExitPlanMode (ExitPlanMode produces a control banner).
    await sendMessage(page, PLAN_MODE_PROMPT)

    // Wait for ExitPlanMode control banner.
    const banner = await waitForControlBanner(page)
    await expect(banner.getByText('Plan Ready for Review')).toBeVisible()

    // Type a rejection reason in the editor.
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await editor.click()
    await page.keyboard.type('draft rejection reason', { delay: 100 })

    // Wait for debounced save (500ms debounce + margin).
    await page.waitForTimeout(700)

    // Reload the page.
    await page.reload()

    // Wait for the control banner to reappear (control requests are persisted server-side).
    const bannerAfterReload = page.locator('[data-testid="control-banner"]')
    await expect(bannerAfterReload).toBeVisible()

    // Verify the editor still contains the rejection reason.
    const restoredEditor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(restoredEditor).toContainText('draft rejection reason')
  })

  test('AskUserQuestion custom text draft survives page reload', async ({ page, authenticatedWorkspace }) => {
    // Trigger AskUserQuestion.
    await sendMessage(
      page,
      'Use AskUserQuestion and tell me what I answered: {"questions":[{"question":"Pick a color","header":"Color","multiSelect":false,"options":[{"label":"Red"},{"label":"Blue"},{"label":"Green"}]}]}',
    )

    // Wait for the control banner.
    await waitForControlBanner(page)

    // Type custom text in the editor.
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await editor.click()
    await page.keyboard.type('my custom color answer', { delay: 100 })

    // Wait for debounced save.
    await page.waitForTimeout(700)

    // Reload the page.
    await page.reload()

    // Wait for the control banner to reappear.
    const bannerAfterReload = page.locator('[data-testid="control-banner"]')
    await expect(bannerAfterReload).toBeVisible()

    // Verify the editor still contains the custom text.
    const restoredEditor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(restoredEditor).toContainText('my custom color answer')
  })

  test('control request draft is isolated from conversation draft', async ({ page, authenticatedWorkspace }) => {
    // Type a conversation draft first.
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()
    await page.keyboard.type('conversation draft text', { delay: 100 })

    // Wait for debounced save.
    await page.waitForTimeout(700)

    // Clear the editor and send a message to trigger AskUserQuestion.
    await page.keyboard.press('Meta+a')
    await page.keyboard.press('Backspace')
    await sendMessage(
      page,
      'Use AskUserQuestion and tell me what I answered: {"questions":[{"question":"Pick a color","header":"Color","multiSelect":false,"options":[{"label":"Red"},{"label":"Blue"},{"label":"Green"}]}]}',
    )

    // Wait for the control banner.
    await waitForControlBanner(page)

    // Editor should be empty (control request has its own draft key).
    await page.waitForTimeout(300)
    const editorText = await page.locator('[data-testid="chat-editor"] .ProseMirror').textContent()
    expect(editorText?.trim()).toBe('')

    // Type control request draft text.
    const editorForCtrl = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await editorForCtrl.click()
    await page.keyboard.type('control request draft text', { delay: 100 })

    // Wait for debounced save.
    await page.waitForTimeout(700)

    // Reload the page.
    await page.reload()

    // Wait for the control banner to reappear.
    const bannerAfterReload = page.locator('[data-testid="control-banner"]')
    await expect(bannerAfterReload).toBeVisible()

    // Verify editor contains the control request draft (not the conversation draft).
    const restoredEditor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(restoredEditor).toContainText('control request draft text')
    await expect(restoredEditor).not.toContainText('conversation draft text')
  })
})
