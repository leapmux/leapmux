import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseBasicChat } from '../helpers/nativeConversation'
import { ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT, expectAssistantAnswer, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { REASONIX_E2E_SKIP_REASON, reasonixTest } from '../reasonix-fixtures'

reasonixTest.skip(!!REASONIX_E2E_SKIP_REASON, REASONIX_E2E_SKIP_REASON || '')

reasonixTest('send message and receive response', async ({ authenticatedReasonixWorkspace, page, modelScript }) => {
  void authenticatedReasonixWorkspace
  await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
  await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
  await waitForAgentIdle(page)
  await expectAssistantAnswer(page)
})

reasonixTest('ends a native turn and keeps its answer after reload', async ({ authenticatedReasonixWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedReasonixWorkspace.workspaceId, provider: AgentProvider.REASONIX }
  await exerciseBasicChat(context)
})
