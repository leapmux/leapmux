import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseNativeOption } from '../helpers/nativeSettings'
import { kiroTest } from '../kiro-fixtures'

kiroTest('sends the selected model into native requests before and after reload', async ({ page, modelScript, leapmuxServer, authenticatedKiroWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedKiroWorkspace.workspaceId, provider: AgentProvider.KIRO }
  await exerciseNativeOption(context, { groupId: 'model', value: 'kiro-e2e-lite', nativeProof: (request) => {
    expect(request.body).toHaveProperty('conversationState.currentMessage.userInputMessage.modelId', 'kiro-e2e-lite')
  } })
})
