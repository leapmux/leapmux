import type { ModelScript } from './helpers/modelScriptFixture'
import type { QuestionRequest } from './helpers/providerToolCalls'
import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { expect, test } from './fixtures'
import { enterAndExitPlanMode } from './helpers/plan-mode'
import { askUserQuestionToolCall } from './helpers/providerToolCalls'
import { sendMessage, waitForControlBanner, waitForEditorDraft } from './helpers/ui'

const COLOR_QUESTION: QuestionRequest = {
  question: 'Pick a color',
  header: 'Color',
  options: [
    { label: 'Red', description: 'Red color' },
    { label: 'Blue', description: 'Blue color' },
    { label: 'Green', description: 'Green color' },
  ],
}

/** Script one `AskUserQuestion` call and send the turn that makes it. */
async function askColor(page: Parameters<typeof sendMessage>[0], script: ModelScript): Promise<void> {
  // What the test does with the banner decides how many turns follow.
  await script.fallback({ text: 'You answered the question.' })
  await script.queue({ toolCalls: [askUserQuestionToolCall(AgentProvider.CLAUDE_CODE, 'ask-color', [COLOR_QUESTION])] })
  await sendMessage(page, script.prompt('Use AskUserQuestion and tell me what I answered.'))
  await script.waitForSteps()
}

test.describe('Control Request Draft Persistence', () => {
  test('ExitPlanMode draft survives page reload', async ({ page, authenticatedWorkspace, leapmuxServer, modelScript }) => {
    // Enter plan mode, write a dummy plan, and exit.
    const banner = await enterAndExitPlanMode(page, modelScript)
    await expect(banner.getByText('Plan Ready for Review')).toBeVisible()

    // Type a rejection reason in the editor.
    const editor = page.locator('[data-testid="composer-editor"] .ProseMirror')
    await editor.click()
    await page.keyboard.type('draft rejection reason', { delay: 100 })

    // Wait for the debounced save to actually land, not for a fixed margin.
    await waitForEditorDraft(page, leapmuxServer.adminUserId, 'draft rejection reason')

    // Reload the page.
    await page.reload()

    // Wait for the control banner to reappear (control requests are persisted server-side).
    const bannerAfterReload = page.locator('[data-testid="control-banner"]')
    await expect(bannerAfterReload).toBeVisible()

    // Verify the editor still contains the rejection reason.
    const restoredEditor = page.locator('[data-testid="composer-editor"] .ProseMirror')
    await expect(restoredEditor).toContainText('draft rejection reason')
  })

  test('AskUserQuestion custom text draft survives page reload', async ({ page, authenticatedWorkspace, leapmuxServer, modelScript }) => {
    // Trigger AskUserQuestion.
    await askColor(page, modelScript)

    // Wait for the control banner.
    await waitForControlBanner(page)

    // Type custom text in the editor.
    const editor = page.locator('[data-testid="composer-editor"] .ProseMirror')
    await editor.click()
    await page.keyboard.type('my custom color answer', { delay: 100 })

    // Wait for the debounced save to actually land, not for a fixed margin.
    await waitForEditorDraft(page, leapmuxServer.adminUserId, 'my custom color answer')

    // Reload the page.
    await page.reload()

    // Wait for the control banner to reappear.
    const bannerAfterReload = page.locator('[data-testid="control-banner"]')
    await expect(bannerAfterReload).toBeVisible()

    // Verify the editor still contains the custom text.
    const restoredEditor = page.locator('[data-testid="composer-editor"] .ProseMirror')
    await expect(restoredEditor).toContainText('my custom color answer')
  })

  test('control request draft is isolated from conversation draft', async ({ page, authenticatedWorkspace, leapmuxServer, modelScript }) => {
    // Type a conversation draft first.
    const editor = page.locator('[data-testid="composer-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()
    await page.keyboard.type('conversation draft text', { delay: 100 })

    // Wait for the debounced save to actually land, not for a fixed margin.
    await waitForEditorDraft(page, leapmuxServer.adminUserId, 'conversation draft text')

    // Clear the editor and send a message to trigger AskUserQuestion.
    await page.keyboard.press('Meta+a')
    await page.keyboard.press('Backspace')
    await askColor(page, modelScript)

    // Wait for the control banner.
    await waitForControlBanner(page)

    // Editor should be empty (control request has its own draft key). The
    // web-first assertion retries, so it needs no settling sleep.
    await expect(page.locator('[data-testid="composer-editor"] .ProseMirror')).toHaveText('')

    // Type control request draft text.
    const editorForCtrl = page.locator('[data-testid="composer-editor"] .ProseMirror')
    await editorForCtrl.click()
    await page.keyboard.type('control request draft text', { delay: 100 })

    // Wait for the debounced save to actually land, not for a fixed margin.
    await waitForEditorDraft(page, leapmuxServer.adminUserId, 'control request draft text')

    // Reload the page.
    await page.reload()

    // Wait for the control banner to reappear.
    const bannerAfterReload = page.locator('[data-testid="control-banner"]')
    await expect(bannerAfterReload).toBeVisible()

    // Verify editor contains the control request draft (not the conversation draft).
    const restoredEditor = page.locator('[data-testid="composer-editor"] .ProseMirror')
    await expect(restoredEditor).toContainText('control request draft text')
    await expect(restoredEditor).not.toContainText('conversation draft text')
  })
})
