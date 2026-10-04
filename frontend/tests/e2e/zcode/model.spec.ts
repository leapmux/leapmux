import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { MOCK_MODELS, MOCK_PROVIDER_IDS } from '../helpers/mockAgentEnvironment'
import { exerciseNativeOption } from '../helpers/nativeSettings'
import { ZCODE_E2E_SKIP_REASON, zcodeTest } from '../zcode-fixtures'

zcodeTest.skip(!!ZCODE_E2E_SKIP_REASON, ZCODE_E2E_SKIP_REASON || '')

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
