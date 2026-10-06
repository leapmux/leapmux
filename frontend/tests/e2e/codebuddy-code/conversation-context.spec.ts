import { codebuddyTest, expect } from '../codebuddy-fixtures'
import { exerciseConversationContext } from '../helpers/nativeConversation'
import { ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT, expectAssistantAnswer, sendMessage, waitForAgentIdle } from '../helpers/ui'

codebuddyTest('carries the earlier user prompt and assistant answer into the next native request', async ({ native }) => {
  await exerciseConversationContext(native)
})

codebuddyTest('keeps the conversation from one turn to the next', async ({ authenticatedCodebuddyWorkspace, page, modelScript }) => {
  void authenticatedCodebuddyWorkspace
  const first = await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
  await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
  await modelScript.waitForSteps(first + 1)
  await waitForAgentIdle(page)
  await expectAssistantAnswer(page)

  const second = await modelScript.queue({ text: 'The second answer.' })
  await sendMessage(page, modelScript.prompt('And the second question?'))
  await modelScript.waitForSteps(second + 1)
  await waitForAgentIdle(page)
  await expectAssistantAnswer(page)
  await expectAssistantAnswer(page, { answer: /The second answer\./ })
  const body = JSON.stringify((await modelScript.requestAt(second)).body)
  expect(body).toContain(ARITHMETIC_PROMPT)
  expect(body).toContain(ARITHMETIC_ANSWER_TEXT)
  expect(body).toContain('And the second question?')
})
