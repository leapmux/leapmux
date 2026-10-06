import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { MOCK_MODELS, MOCK_PROVIDER_IDS } from '../helpers/mockAgentEnvironment'
import { exerciseNativeOption } from '../helpers/nativeSettings'
import { zcodeTest } from '../zcode-fixtures'

zcodeTest('sends the selected model on the next native request and keeps it after reload', async ({ authenticatedZCodeWorkspace, page, modelScript, leapmuxServer }) => {
  await exerciseNativeOption({
    page,
    modelScript,
    leapmuxServer,
    provider: AgentProvider.ZCODE,
    workspaceId: authenticatedZCodeWorkspace.workspaceId,
  }, {
    groupId: 'model',
    value: `${MOCK_PROVIDER_IDS.zcode}/${MOCK_MODELS.pi}`,
    nativeProof(request) {
      expect(request.body).toMatchObject({ model: MOCK_MODELS.pi })
    },
  })
})
