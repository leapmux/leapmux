import { exerciseConversationContext } from '../helpers/nativeConversation'
import { ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT, expectAssistantAnswer, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { expect, qoderTest } from '../qoder-fixtures'
import { nativeContext } from './scenarios'

qoderTest('carries the earlier user prompt and assistant answer into the next native request', async ({ qoderWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: qoderWorkspace.workspaceId })
  await exerciseConversationContext(context)
})

qoderTest('keeps the conversation from one turn to the next', async ({ qoderWorkspace, page, modelScript }) => {
  void qoderWorkspace
  await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
  await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
  await modelScript.waitForSteps()
  await waitForAgentIdle(page)
  await expectAssistantAnswer(page)

  await modelScript.queue({ text: 'The second answer.' })
  await sendMessage(page, modelScript.prompt('And the second question?'))
  const continued = await modelScript.waitForSteps(2)
  await waitForAgentIdle(page)
  await expectAssistantAnswer(page)
  await expectAssistantAnswer(page, { answer: /The second answer\./ })
  const request = continued.requests.find(record => record.stepIndex === 1)
  expect(request).toBeDefined()
  expect(JSON.stringify(request?.body)).toContain(ARITHMETIC_PROMPT)
  expect(JSON.stringify(request?.body)).toContain(ARITHMETIC_ANSWER_TEXT)
  expect(JSON.stringify(request?.body)).toContain('And the second question?')
})
