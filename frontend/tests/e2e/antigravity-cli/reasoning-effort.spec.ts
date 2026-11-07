import { expect } from '@playwright/test'
import { antigravityTest } from '../antigravity-fixtures'
import { exerciseNativeOption } from '../helpers/nativeSettings'

antigravityTest('sends the selected native effort before and after reload', async ({ native }) => {
  await exerciseNativeOption(native, {
    groupId: 'effort',
    value: 'low',
    nativeProof: (request) => {
      expect(request.protocol).toBe('google-generative-language')
      expect(request.body).toHaveProperty('generationConfig.thinkingConfig.thinkingLevel', 'LOW')
    },
  })
})
