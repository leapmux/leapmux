import { expect } from '@playwright/test'
import { exerciseNativeOption } from '../helpers/nativeSettings'
import { museTest } from '../muse-fixtures'

museTest('changes the native model and keeps each selection after reload', async ({ native }) => {
  for (const model of ['muse-spark-1.3', 'muse-spark-1.2']) {
    await exerciseNativeOption(native, {
      groupId: 'model',
      value: model,
      nativeProof: (request) => {
        expect(request.protocol).toBe('openai-responses')
        expect(request.path).toBe('/v1/responses')
        expect(request.body).toHaveProperty('model', model)
        expect(request.mockCredential?.accepted).toBe(true)
      },
    })
  }
})
