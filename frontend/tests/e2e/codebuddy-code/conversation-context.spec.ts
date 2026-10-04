import { codebuddyTest, expect } from '../codebuddy-fixtures'
import { exerciseConversationContext } from '../helpers/nativeConversation'
import { ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT, expectAssistantAnswer, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { nativeContext } from './scenarios'

codebuddyTest('carries the earlier user prompt and assistant answer into the next native request', async ({ codebuddyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: codebuddyWorkspace.workspaceId })
  await exerciseConversationContext(context)
})

codebuddyTest('keeps the conversation from one turn to the next', async ({ codebuddyWorkspace, page, modelScript }) => {
  void codebuddyWorkspace
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
