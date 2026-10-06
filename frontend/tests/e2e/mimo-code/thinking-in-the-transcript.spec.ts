import { expect } from '@playwright/test'
import { ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT, assistantBubbles, expectAssistantAnswer, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { mimoTest } from '../mimo-fixtures'

mimoTest.describe('MiMo Code basic chat', () => {
  // One scripted turn proves the native request path.
  // The Worker starts `mimo serve` and sends the prompt through HTTP.
  // It reads the answer from the event stream. The native idle status ends the turn.
  mimoTest('renders the reasoning and the answer, then clears the thinking indicator', async ({ authenticatedMiMoWorkspace, page, modelScript }) => {
    void authenticatedMiMoWorkspace
    await modelScript.queue({ reasoning: 'Add the two numbers.', text: ARITHMETIC_ANSWER_TEXT })
    await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    await expectAssistantAnswer(page)
    await expect(assistantBubbles(page).filter({ hasText: 'Add the two numbers.' })).toBeVisible()
    await expect(page.getByTestId('thinking-indicator')).not.toBeVisible()
  })
})
