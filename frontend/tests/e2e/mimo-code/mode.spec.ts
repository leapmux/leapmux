import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { MOCK_MODELS } from '../helpers/mockAgentEnvironment'
import { nativeModelInstructionText } from '../helpers/nativeScenario'
import { exerciseNativeOption } from '../helpers/nativeSettings'
import { mimoTest } from '../mimo-fixtures'

mimoTest('applies the native primary agent mode before and after reload', async ({ authenticatedMiMoWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedMiMoWorkspace.workspaceId, provider: AgentProvider.MIMO_CODE }
  await exerciseNativeOption(context, { groupId: 'permissionMode', value: 'plan', nativeProof: (request) => {
    const instruction = nativeModelInstructionText(request)
    expect(instruction).toMatch(/Plan mode is (?:still )?active/i)
    expect(instruction).toMatch(/read-only/i)
    expect(request.body).toHaveProperty('model', MOCK_MODELS.zai)
    expect(request.body).toHaveProperty('reasoning_effort', 'high')
  } })
})
