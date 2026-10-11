import { expect } from '@playwright/test'
import { claudeTest } from '../claude-fixtures'
import { exerciseSessionReset } from '../helpers/nativeLifecycle'
import { ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT, expectAssistantAnswer, SECOND_ARITHMETIC_ANSWER, SECOND_ARITHMETIC_ANSWER_TEXT, SECOND_ARITHMETIC_PROMPT, sendMessage, visibleOnly } from '../helpers/ui'

claudeTest.describe('Clear Command', () => {
  // Search answer rows directly. A turn-end divider can be the last agent-role row.
  claudeTest('slash reset clears context (alias for /clear)', async ({ page, authenticatedWorkspace, modelScript }) => {
    void authenticatedWorkspace // fixture trigger

    // Send a message to establish a session
    const first = await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
    await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
    await modelScript.waitForSteps(first + 1)
    await expectAssistantAnswer(page)

    // Send /reset (alias for /clear)
    await sendMessage(page, '/reset')

    // Verify notification bubble appears
    await expect(visibleOnly(page.getByText('Context cleared'))).toBeVisible()

    // Verify agent is still responsive (new session)
    const second = await modelScript.queue({ text: SECOND_ARITHMETIC_ANSWER_TEXT })
    await sendMessage(page, modelScript.prompt(SECOND_ARITHMETIC_PROMPT))
    await modelScript.waitForSteps(second + 1)
    await expectAssistantAnswer(page, { answer: SECOND_ARITHMETIC_ANSWER })
  })

  claudeTest('slash clear clears context and shows notification', async ({ page, authenticatedWorkspace, modelScript }) => {
    void authenticatedWorkspace // fixture trigger

    // Send a message to establish a session
    const first = await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
    await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
    await modelScript.waitForSteps(first + 1)
    await expectAssistantAnswer(page)

    // Send /clear
    await sendMessage(page, '/clear')

    // Verify notification bubble appears
    await expect(visibleOnly(page.getByText('Context cleared'))).toBeVisible()

    // Verify agent is still responsive (new session)
    const second = await modelScript.queue({ text: SECOND_ARITHMETIC_ANSWER_TEXT })
    await sendMessage(page, modelScript.prompt(SECOND_ARITHMETIC_PROMPT))
    await modelScript.waitForSteps(second + 1)
    await expectAssistantAnswer(page, { answer: SECOND_ARITHMETIC_ANSWER })

    // The new session reports system prompt tokens after its first reply.
    // Check the context grid by test ID. ThemeSwatch also uses PipGrid, so an SVG selector would match unrelated theme controls.
    const grid = page.getByTestId('context-usage-grid')
    await expect(grid).toBeVisible()
  })
})

for (const command of ['/clear', '/reset'] as const) {
  claudeTest(`excludes unique prior native context after ${command} and keeps saved Worker rows`, async ({ native }) => {
    await exerciseSessionReset(native, { command })
  })
}
