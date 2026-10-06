import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { MOCK_MODELS } from '../helpers/mockAgentEnvironment'
import { exerciseNativeOption } from '../helpers/nativeSettings'
import { piTest } from '../pi-fixtures'

piTest('sends the selected model on the next native request and keeps it after reload', async ({ authenticatedPiWorkspace, page, modelScript, leapmuxServer }) => {
  await exerciseNativeOption({
    page,
    modelScript,
    leapmuxServer,
    provider: AgentProvider.PI,
    workspaceId: authenticatedPiWorkspace.workspaceId,
  }, {
    groupId: 'model',
    value: MOCK_MODELS.zai,
    nativeProof(request) {
      expect(request.body).toMatchObject({ model: MOCK_MODELS.zai })
    },
  })
})
