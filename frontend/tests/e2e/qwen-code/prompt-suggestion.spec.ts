import { expect } from '@playwright/test'
import { escapeRegExp } from '../../../src/lib/regexp'
import { sendMessage, waitForAgentIdle } from '../helpers/ui'
import { qwenTest } from '../qwen-fixtures'

/**
 * The words that open Qwen Code's follow-up suggestion request. Qwen builds that
 * request as a user turn of its own, after the answer, and only when the
 * `ui.enableFollowupSuggestions` setting of the workspace allows it.
 */
const QWEN_SUGGESTION_REQUEST = '[SUGGESTION MODE'

qwenTest.describe('Qwen Code follow-up suggestions', () => {
  qwenTest('shows the native suggestion and fills the editor on Use', async ({ page, suggestingQwenWorkspace, modelScript }) => {
    expect(suggestingQwenWorkspace.workingDir, 'the suggesting workspace opened its agent').toBeDefined()
    // The suggestion request is a native turn of its own with no known place
    // in the order, so a rule answers it.
    await modelScript.rule({
      name: 'the follow-up suggestion',
      when: { user: escapeRegExp(QWEN_SUGGESTION_REQUEST) },
      // The suggestion must hold 2 to 12 words with no sentence boundary or
      // formatting: Qwen filters one-word and meta-shaped replies instead of
      // offering them (see generatePromptSuggestion's filter).
      respond: { text: 'Try the CEDAR followup now' },
    })
    // Qwen states no suggestion before the conversation holds two model turns,
    // so a first answer warms the conversation up.
    const warmup = await modelScript.queue({ text: 'CEDAR_SUGGESTION_WARMUP' })
    await sendMessage(page, modelScript.prompt('Answer once so the conversation leaves its first turn.'))
    await modelScript.waitForSteps(warmup + 1)
    await waitForAgentIdle(page)
    const start = await modelScript.queue({ text: 'CEDAR_SUGGESTION_ANSWER' })
    await sendMessage(page, modelScript.prompt('Answer so Qwen suggests a follow-up.'))
    await modelScript.waitForSteps(start + 1)
    await waitForAgentIdle(page)

    const chip = page.locator('[data-testid="prompt-suggestion-chip"]:visible')
    await expect(chip).toBeVisible()
    await expect(chip).toContainText('Try the CEDAR followup now')
    await page.locator('[data-testid="prompt-suggestion-use"]:visible').click()
    await expect(page.locator('[data-testid="composer-editor"]:visible .ProseMirror')).toContainText('Try the CEDAR followup now')
  })
})
