import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { MOCK_MODELS, REASONIX_ALT_MODEL_ID } from '../helpers/mockAgentEnvironment'
import { exerciseNativeOption } from '../helpers/nativeSettings'
import { REASONIX_E2E_SKIP_REASON, reasonixTest } from '../reasonix-fixtures'

reasonixTest.skip(!!REASONIX_E2E_SKIP_REASON, REASONIX_E2E_SKIP_REASON || '')

reasonixTest('sends the selected model on the next native request and keeps it after reload', async ({ authenticatedReasonixWorkspace, page, modelScript, leapmuxServer }) => {
  await exerciseNativeOption({
    page,
    modelScript,
    leapmuxServer,
    provider: AgentProvider.REASONIX,
    workspaceId: authenticatedReasonixWorkspace.workspaceId,
  }, {
    groupId: 'model',
    value: REASONIX_ALT_MODEL_ID,
    nativeProof(request) {
      expect(request.body).toMatchObject({ model: MOCK_MODELS.pi })
    },
  })
})
