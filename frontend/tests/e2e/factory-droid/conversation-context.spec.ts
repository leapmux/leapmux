import { droidTest, expect } from '../droid-fixtures'
import { exerciseConversationContext } from '../helpers/nativeConversation'
import { ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT, expectAssistantAnswer, sendMessage, waitForAgentIdle } from '../helpers/ui'

droidTest('carries the earlier user prompt and assistant answer into the next native request', async ({ native }) => {
  await exerciseConversationContext(native)
})

droidTest('carries the conversation into the next request', async ({ authenticatedDroidWorkspace, page, modelScript }) => {
  void authenticatedDroidWorkspace
  const first = await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
  await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
  await modelScript.waitForSteps(first + 1)
  await waitForAgentIdle(page)
  await expectAssistantAnswer(page)
  await expect(page.locator('[data-testid="result-divider"]:visible')).toHaveCount(1)

  // The request the turn made carries the prompt the user wrote.
  expect(JSON.stringify((await modelScript.requestAt(first)).body)).toContain('1234 + 5678')

  const second = await modelScript.queue({ text: 'The second native answer.' })
  await sendMessage(page, modelScript.prompt('Continue the earlier arithmetic conversation.'))
  await modelScript.waitForSteps(second + 1)
  await waitForAgentIdle(page)
  await expectAssistantAnswer(page, { answer: /The second native answer\./ })
  const next = JSON.stringify((await modelScript.requestAt(second)).body)
  expect(next).toContain(ARITHMETIC_PROMPT)
  expect(next).toContain(ARITHMETIC_ANSWER_TEXT)
  expect(next).toContain('Continue the earlier arithmetic conversation.')
  await expect(page.locator('[data-testid="result-divider"]:visible')).toHaveCount(2)
})
