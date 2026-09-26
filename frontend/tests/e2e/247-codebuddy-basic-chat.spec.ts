import { CODEBUDDY_E2E_SKIP_REASON, codebuddyTest, expect } from './codebuddy-fixtures'
import {
  ARITHMETIC_ANSWER_TEXT,
  ARITHMETIC_PROMPT,
  expectAssistantAnswer,
  sendMessage,
  waitForAgentIdle,
} from './helpers/ui'

/**
 * 247 — CodeBuddy Code basic chat.
 *
 * The worker runs `codebuddy -p --input-format stream-json --output-format
 * stream-json` for the agent and sends each prompt as one stdin `user` frame.
 * CodeBuddy asks the mock through its custom-local `models.json` entry. One
 * scripted turn proves that a prompt reaches the model, that the answer reaches
 * the chat, and that the `result` frame closes the turn. A second turn proves
 * that the process keeps the conversation.
 */
codebuddyTest.skip(!!CODEBUDDY_E2E_SKIP_REASON, CODEBUDDY_E2E_SKIP_REASON || '')

codebuddyTest.describe('CodeBuddy Code basic chat', () => {
  codebuddyTest('draws the answer and ends the turn', async ({ codebuddyWorkspace, page, modelScript }) => {
    void codebuddyWorkspace
    await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
    await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)

    await expectAssistantAnswer(page)
    await expect(page.getByTestId('thinking-indicator')).not.toBeVisible()

    // The worker writes the rows it streamed, so a reload draws the same turn.
    await page.reload()
    await expectAssistantAnswer(page)
  })

  codebuddyTest('keeps the conversation from one turn to the next', async ({ codebuddyWorkspace, page, modelScript }) => {
    void codebuddyWorkspace
    await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
    await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)
    await expectAssistantAnswer(page)

    await modelScript.queue({ text: 'The second answer.' })
    await sendMessage(page, modelScript.prompt('And the second question?'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)
    await expectAssistantAnswer(page)
  })
})
