import { expect } from '@playwright/test'
import { MOCK_MODELS, REASONIX_ALT_MODEL_ID } from '../helpers/mockAgentEnvironment'
import { exerciseNativeOption } from '../helpers/nativeSettings'
import { reasonixTest } from '../reasonix-fixtures'

reasonixTest('sends the selected model on the next native request and keeps it after reload', async ({ native }) => {
  await exerciseNativeOption(native, {
    groupId: 'model',
    value: REASONIX_ALT_MODEL_ID,
    nativeProof(request) {
      expect(request.body).toMatchObject({ model: MOCK_MODELS.pi })
    },
  })
})
