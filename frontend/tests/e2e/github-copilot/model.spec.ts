import { expect } from '@playwright/test'
import { copilotTest } from '../copilot-fixtures'
import { MOCK_MODELS } from '../helpers/mockAgentEnvironment'
import { exerciseNativeOption } from '../helpers/nativeSettings'

copilotTest('sends the selected model on the next native request and keeps it after reload', async ({ native }) => {
  await exerciseNativeOption(native, {
    groupId: 'model',
    value: MOCK_MODELS.gooseReasoning,
    nativeProof(request) {
      expect(request.body).toMatchObject({ model: MOCK_MODELS.gooseReasoning })
    },
  })
})
