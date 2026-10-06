import { droidTest, expect } from '../droid-fixtures'
import { exerciseConversationContext } from '../helpers/nativeConversation'
import { ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT, expectAssistantAnswer, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { nativeContext } from './scenarios'

droidTest('carries the earlier user prompt and assistant answer into the next native request', async ({ authenticatedDroidWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedDroidWorkspace.workspaceId })
  await exerciseConversationContext(context)
})

droidTest('carries the conversation into the next request', async ({ authenticatedDroidWorkspace, page, modelScript }) => {
  void authenticatedDroidWorkspace
  await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
  await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
  const status = await modelScript.waitForSteps()
  await waitForAgentIdle(page)
  await expectAssistantAnswer(page)
  await expect(page.locator('[data-testid="result-divider"]:visible')).toHaveCount(1)

  // The request the turn made carries the prompt the user wrote.
  const request = status.requests.find(record => record.stepIndex === 0)
  expect(JSON.stringify(request?.body)).toContain('1234 + 5678')

  await modelScript.queue({ text: 'The second native answer.' })
  await sendMessage(page, modelScript.prompt('Continue the earlier arithmetic conversation.'))
  const continued = await modelScript.waitForSteps(2)
  await waitForAgentIdle(page)
  await expectAssistantAnswer(page, { answer: /The second native answer\./ })
  const next = continued.requests.find(record => record.stepIndex === 1)
  expect(next).toBeDefined()
  expect(JSON.stringify(next?.body)).toContain(ARITHMETIC_PROMPT)
  expect(JSON.stringify(next?.body)).toContain(ARITHMETIC_ANSWER_TEXT)
  expect(JSON.stringify(next?.body)).toContain('Continue the earlier arithmetic conversation.')
  await expect(page.locator('[data-testid="result-divider"]:visible')).toHaveCount(2)
})
