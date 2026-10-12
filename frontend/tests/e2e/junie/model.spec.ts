import { expect } from '@playwright/test'
import { JUNIE_RESPONSES_MODEL, MOCK_MODELS } from '../helpers/mockAgentEnvironment'
import { exerciseNativeOption } from '../helpers/nativeSettings'
import { junieTest } from '../junie-fixtures'

junieTest.describe('Junie settings', () => {
  junieTest('a model switch reaches the native Responses endpoint and survives a reload', async ({ native }) => {
    await exerciseNativeOption(native, {
      groupId: 'model',
      value: JUNIE_RESPONSES_MODEL,
      nativeProof: (request) => {
        expect(request.path).toBe('/v1/responses')
        expect(request.body).toMatchObject({ model: MOCK_MODELS.junie })
      },
    })
  })
})
