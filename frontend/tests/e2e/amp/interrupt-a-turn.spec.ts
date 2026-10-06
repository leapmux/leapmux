import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { ampTest } from '../amp-fixtures'
import { exerciseInterruptTurn } from '../helpers/nativeLifecycle'
import { ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT, expectAssistantAnswer } from '../helpers/ui'

ampTest.describe('Amp interrupt', () => {
  ampTest('stops a model call and continues the thread at the next prompt', async ({ page, modelScript, leapmuxServer, authenticatedAmpWorkspace }) => {
    const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedAmpWorkspace.workspaceId, provider: AgentProvider.AMP }
    await exerciseInterruptTurn(context, { kind: 'model', prompt: 'Write a long essay about the history of computing.', divider: /^Turn interrupted/, continuation: { prompt: ARITHMETIC_PROMPT, answer: ARITHMETIC_ANSWER_TEXT, contextMarkers: ['history of computing'] } })
    await expectAssistantAnswer(page)
  })

  ampTest('stops a running command and continues the thread at the next prompt', async ({ page, modelScript, leapmuxServer, authenticatedAmpWorkspace }) => {
    const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedAmpWorkspace.workspaceId, provider: AgentProvider.AMP }
    await exerciseInterruptTurn(context, { kind: 'tool', prompt: 'Wait for ten minutes.', divider: /^Turn interrupted/, continuation: { prompt: ARITHMETIC_PROMPT, answer: ARITHMETIC_ANSWER_TEXT, contextMarkers: ['Wait for ten minutes.'] } })
    await expectAssistantAnswer(page)
  })
})
