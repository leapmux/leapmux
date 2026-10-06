import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseInterruptTurn } from '../helpers/nativeLifecycle'
import { ARITHMETIC_ANSWER_TEXT, ARITHMETIC_PROMPT, expectAssistantAnswer } from '../helpers/ui'
import { ohMyPiTest } from '../ohmypi-fixtures'

ohMyPiTest.describe('Oh My Pi interrupt', () => {
  ohMyPiTest('stops a model call and takes the next prompt', async ({ page, modelScript, leapmuxServer, authenticatedOhMyPiWorkspace }) => {
    const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedOhMyPiWorkspace.workspaceId, provider: AgentProvider.OH_MY_PI }
    await exerciseInterruptTurn(context, { kind: 'model', prompt: 'Write a long essay about the history of computing.', divider: /^Turn interrupted \(.+\)$/, continuation: { prompt: ARITHMETIC_PROMPT, answer: ARITHMETIC_ANSWER_TEXT } })
    await expectAssistantAnswer(page)
  })

  ohMyPiTest('stops a running command and takes the next prompt', async ({ page, modelScript, leapmuxServer, authenticatedOhMyPiWorkspace }) => {
    const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedOhMyPiWorkspace.workspaceId, provider: AgentProvider.OH_MY_PI }
    await exerciseInterruptTurn(context, { kind: 'tool', prompt: 'Wait for ten minutes.', divider: /^Turn interrupted \(.+\)1 tool$/, continuation: { prompt: ARITHMETIC_PROMPT, answer: ARITHMETIC_ANSWER_TEXT } })
    await expectAssistantAnswer(page)
  })
})
