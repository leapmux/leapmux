import { expect } from '@playwright/test'
import { MOCK_MODELS } from '../helpers/mockAgentEnvironment'
import { exerciseNativeOption } from '../helpers/nativeSettings'
import { piTest } from '../pi-fixtures'

piTest('sends the selected model on the next native request and keeps it after reload', async ({ native }) => {
  await exerciseNativeOption(native, {
    groupId: 'model',
    value: MOCK_MODELS.zai,
    nativeProof(request) {
      expect(request.body).toMatchObject({ model: MOCK_MODELS.zai })
    },
  })
})
