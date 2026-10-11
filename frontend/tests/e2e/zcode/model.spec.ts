import { expect } from '@playwright/test'
import { MOCK_MODELS, MOCK_PROVIDER_IDS } from '../helpers/mockAgentEnvironment'
import { exerciseNativeOption } from '../helpers/nativeSettings'
import { zcodeTest } from '../zcode-fixtures'

zcodeTest('sends the selected model on the next native request and keeps it after reload', async ({ native }) => {
  await exerciseNativeOption(native, {
    groupId: 'model',
    value: `${MOCK_PROVIDER_IDS.zcode}/${MOCK_MODELS.pi}`,
    nativeProof(request) {
      expect(request.body).toMatchObject({ model: MOCK_MODELS.pi })
    },
  })
})
