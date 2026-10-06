import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseModelSwitchKeepsOption, exerciseNativeOption } from '../helpers/nativeSettings'
import { kiroTest } from '../kiro-fixtures'

kiroTest('sends the chosen effort into native turns before and after reload', async ({ page, modelScript, leapmuxServer, authenticatedKiroWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedKiroWorkspace.workspaceId, provider: AgentProvider.KIRO }
  await exerciseNativeOption(context, { groupId: 'effortLevel', value: 'high', nativeProof: (request) => {
    expect(request.body).toHaveProperty('additionalModelRequestFields.output_config.effort', 'high')
  } })
})

// A Kiro model write starts the effort at the default of the new model. Both models offer low, medium, and high.
kiroTest('keeps the chosen effort after a model switch and a reload', async ({ page, modelScript, leapmuxServer, authenticatedKiroWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedKiroWorkspace.workspaceId, provider: AgentProvider.KIRO }
  await exerciseModelSwitchKeepsOption(context, {
    kept: { groupId: 'effortLevel', value: 'low' },
    model: 'kiro-e2e-thinking',
    nativeProof: (request) => {
      expect(request.body).toHaveProperty('conversationState.currentMessage.userInputMessage.modelId', 'kiro-e2e-thinking')
      expect(request.body).toHaveProperty('additionalModelRequestFields.output_config.effort', 'low')
    },
  })
})
