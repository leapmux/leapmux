import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { GROK_E2E_SKIP_REASON, grokTest } from '../grok-fixtures'
import { exerciseBasicChat } from '../helpers/nativeConversation'
import { nativeModelLastUserText } from '../helpers/nativeScenario'
import { ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT, expectAssistantAnswer, sendMessage, waitForAgentIdle } from '../helpers/ui'

grokTest.skip(!!GROK_E2E_SKIP_REASON, GROK_E2E_SKIP_REASON || '')

grokTest.describe('Grok Build Basic Chat', () => {
  grokTest('send message and receive response', async ({ authenticatedGrokWorkspace, page, modelScript }) => {
    void authenticatedGrokWorkspace
    await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
    await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
    const status = await modelScript.waitForSteps(1)
    const request = status.requests.find(record => record.stepIndex === 0)
    if (!request)
      throw new Error('The basic chat prompt reached no native model request.')
    expect(nativeModelLastUserText(request)).toContain(ARITHMETIC_PROMPT)
    await waitForAgentIdle(page)
    await expectAssistantAnswer(page)
    await exerciseBasicChat({ page, modelScript, provider: AgentProvider.GROK_BUILD })
  })
})
