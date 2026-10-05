import { exerciseConversationContext } from '../helpers/nativeConversation'
import { ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT, expectAssistantAnswer, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { expect, LETTA_TITLE_RULE, lettaTest } from '../letta-fixtures'
import { nativeContext } from './scenarios'

lettaTest('carries the earlier user prompt and assistant answer into the next native request', async ({ authenticatedLettaWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedLettaWorkspace.workspaceId })
  await exerciseConversationContext(context)
})

lettaTest('carries the conversation into the request', async ({ authenticatedLettaWorkspace, page, modelScript }) => {
  void authenticatedLettaWorkspace
  await modelScript.rule(LETTA_TITLE_RULE)
  await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
  await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
  const status = await modelScript.waitForSteps()
  await waitForAgentIdle(page)
  await expectAssistantAnswer(page)
  await expect(page.locator('[data-testid="result-divider"]:visible')).toHaveCount(1)

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
