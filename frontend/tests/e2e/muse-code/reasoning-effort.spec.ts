import { expect } from '@playwright/test'
import { exerciseNativeOption } from '../helpers/nativeSettings'
import { museTest } from '../muse-fixtures'

museTest('applies both native reasoning tiers to model requests before and after reload', async ({ native }) => {
  for (const effort of ['high', 'low']) {
    await exerciseNativeOption(native, {
      groupId: 'effort',
      value: effort,
      nativeProof: (request) => {
        expect(request.protocol).toBe('openai-responses')
        expect(request.path).toBe('/v1/responses')
        expect(request.body).toHaveProperty('reasoning.effort', effort)
        expect(request.mockCredential?.accepted).toBe(true)
      },
    })
  }
})
