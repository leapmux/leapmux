import { expect } from '@playwright/test'
import { antigravityTest } from '../antigravity-fixtures'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { nativeOptionValue } from '../helpers/nativeScenario'
import { chooseNativeModel, exerciseNativeOption, exerciseNativeOptionSequence, waitForNativeOptionApplied } from '../helpers/nativeSettings'
import { chooseSettingsOption, expectSettingsOptionChosen, offeredSettingsOptions, waitForNativeSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'

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
  expect((await offeredSettingsOptions(native.page, 'effort'))[0]).toBe('auto')
  await exerciseNativeOptionSequence(native, {
    groupId: 'effort',
    steps: [
      { value: 'low', via: 'choose' },
      { value: 'high', via: 'choose' },
      { value: 'xhigh', via: 'choose' },
      { value: 'xhigh', via: 'reload' },
      { value: 'auto', via: 'choose' },
      { value: 'auto', via: 'reload' },
    ],
    nativeProof: (request, step) => {
      expect(request.protocol).toBe('google-generative-language')
      expect(request.path).toContain('/models/gemini-4-neon:')
      expect(request.body).toHaveProperty(
        'generationConfig.thinkingConfig.thinkingLevel',
        step.value === 'auto'
          ? 'HIGH'
          : step.value.toUpperCase(),
      )
    },
  })
})

antigravityTest('resets an unsupported effort to auto when switching to a narrower model', async ({ native }) => {
  await waitForNativeSettingsHydrated(native.page)
  await chooseNativeModel(native, 'gemini-4-neon')
  await chooseSettingsOption(native.page, 'effort-xhigh')
  await waitForSettingsIdle(native.page)
  await expectSettingsOptionChosen(native.page, 'effort-xhigh')
  await waitForNativeOptionApplied(native, 'effort', 'xhigh')

  await chooseNativeModel(native, 'gemini-3.8-flash')
  const settled = await waitForNativeOptionApplied(native, 'effort', 'auto')
  expect(nativeOptionValue(settled, 'effort')).toBe('auto')
  await expectSettingsOptionChosen(native.page, 'effort-auto')

  const request = await sendNativeAnswer(native, 'Reply after switching to a narrower model.', 'The narrower model ran at its default effort.')
  expect(request.protocol).toBe('google-generative-language')
  expect(request.path).toContain('/models/gemini-3.8-flash:')
  expect(request.body).toHaveProperty('generationConfig.thinkingConfig.thinkingLevel', 'HIGH')
})
