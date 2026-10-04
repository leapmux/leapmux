import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { CLINE_E2E_SKIP_REASON, clineTest } from '../cline-fixtures'
import { exerciseInterruptTurn } from '../helpers/nativeLifecycle'
import { ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT, expectAssistantAnswer } from '../helpers/ui'

clineTest.skip(!!CLINE_E2E_SKIP_REASON, CLINE_E2E_SKIP_REASON || '')

clineTest.describe('Cline interrupt', () => {
  clineTest('stops a model call and continues the session at the next prompt', async ({ page, modelScript, leapmuxServer, authenticatedClineWorkspace }) => {
    const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedClineWorkspace.workspaceId, provider: AgentProvider.CLINE }
    await exerciseInterruptTurn(context, { kind: 'model', prompt: 'Write a long essay about the history of computing.', divider: /^Turn interrupted/, continuation: { prompt: ARITHMETIC_PROMPT, answer: ARITHMETIC_ANSWER_TEXT, contextMarkers: ['history of computing'] } })
    await expectAssistantAnswer(page)
  })

  clineTest('stops a running command and continues the session at the next prompt', async ({ page, modelScript, leapmuxServer, authenticatedClineWorkspace }) => {
    const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedClineWorkspace.workspaceId, provider: AgentProvider.CLINE }
    await exerciseInterruptTurn(context, { kind: 'tool', prompt: 'Wait for ten minutes.', divider: /^Turn interrupted/, continuation: { prompt: ARITHMETIC_PROMPT, answer: ARITHMETIC_ANSWER_TEXT, contextMarkers: ['Wait for ten minutes.'] } })
    await expectAssistantAnswer(page)
  })
})
