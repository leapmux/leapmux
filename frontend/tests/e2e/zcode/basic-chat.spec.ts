import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseBasicChat } from '../helpers/nativeConversation'
import { ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT, assistantBubbles, expectAssistantAnswer, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { ZCODE_E2E_SKIP_REASON, zcodeTest } from '../zcode-fixtures'

zcodeTest.skip(!!ZCODE_E2E_SKIP_REASON, ZCODE_E2E_SKIP_REASON || '')

zcodeTest('opens, sends a prompt, and receives a response', async ({ authenticatedZCodeWorkspace, page, modelScript }) => {
  void authenticatedZCodeWorkspace
  await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
  await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
  await modelScript.waitForSteps()
  await waitForAgentIdle(page)
  await expectAssistantAnswer(page)
})

zcodeTest('assistant response appears in a chat bubble', async ({ authenticatedZCodeWorkspace, page, modelScript }) => {
  void authenticatedZCodeWorkspace
  await modelScript.queue({ text: 'hello world' })
  await sendMessage(page, modelScript.prompt('Say hello world'))
  await modelScript.waitForSteps()
  await waitForAgentIdle(page)

  await expect(assistantBubbles(page)).not.toHaveCount(0)
  // expectAssistantAnswer, not lastAssistantBubble: a turn-end divider is an
  // agent-role bubble too, so the LAST one is the divider whenever it lands
  // after the reply.
  await expectAssistantAnswer(page, { answer: /hello/i })
})

zcodeTest('ends a native turn and keeps its answer after reload', async ({ authenticatedZCodeWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedZCodeWorkspace.workspaceId, provider: AgentProvider.ZCODE }
  await exerciseBasicChat(context)
})
