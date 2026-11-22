import { expect } from '@playwright/test'
import { antigravityTest } from '../antigravity-fixtures'
import { chooseNativeModel, exerciseNativeOption, exerciseNativeOptionSequence } from '../helpers/nativeSettings'

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

antigravityTest('sends the chosen Neon reasoning variant and restores it after reload', async ({ native }) => {
  await chooseNativeModel(native, 'gemini-4-neon')
  await exerciseNativeOptionSequence(native, {
    groupId: 'effort',
    steps: [
      { value: 'low', via: 'choose' },
      { value: 'high', via: 'choose' },
      { value: 'xhigh', via: 'choose' },
      { value: 'xhigh', via: 'reload' },
    ],
    nativeProof: (request, step) => {
      expect(request.protocol).toBe('google-generative-language')
      expect(request.path).toContain('/models/gemini-4-neon:')
      expect(request.body).toHaveProperty('generationConfig.thinkingConfig.thinkingLevel', step.value.toUpperCase())
    },
  })
})
