import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { COPILOT_E2E_SKIP_REASON, copilotTest } from '../copilot-fixtures'
import { MOCK_MODELS } from '../helpers/mockAgentEnvironment'
import { exerciseNativeOption } from '../helpers/nativeSettings'

copilotTest.skip(!!COPILOT_E2E_SKIP_REASON, COPILOT_E2E_SKIP_REASON || '')

copilotTest('sends the selected model on the next native request and keeps it after reload', async ({ authenticatedCopilotWorkspace, page, modelScript, leapmuxServer }) => {
  await exerciseNativeOption({
    page,
    modelScript,
    leapmuxServer,
    provider: AgentProvider.GITHUB_COPILOT,
    workspaceId: authenticatedCopilotWorkspace.workspaceId,
  }, {
    groupId: 'model',
    value: MOCK_MODELS.gooseReasoning,
    nativeProof(request) {
      expect(request.body).toMatchObject({ model: MOCK_MODELS.gooseReasoning })
    },
  })
})
