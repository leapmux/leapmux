import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import { expect } from '@playwright/test'
import { antigravityTest } from '../antigravity-fixtures'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { chooseNativeModel, exerciseNativeOption, exerciseNativeOptionSequence, waitForNativeOptionApplied } from '../helpers/nativeSettings'
import { chooseSettingsOption, expectSettingsOptionChosen, offeredSettingsOptions, waitForNativeSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'

function expectGoogleThinkingLevel(request: MockModelRequestRecord, level: string, model?: string) {
  expect(request.protocol).toBe('google-generative-language')
  if (model !== undefined) {
    expect(request.path).toContain(`/models/${model}:`)
  }
  expect(request.body).toHaveProperty('generationConfig.thinkingConfig.thinkingLevel', level)
}

antigravityTest('sends the selected native effort before and after reload', async ({ native }) => {
  await exerciseNativeOption(native, {
    groupId: 'effort',
    value: 'low',
    nativeProof: (request) => {
      expectGoogleThinkingLevel(request, 'LOW')
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
      expectGoogleThinkingLevel(
        request,
        step.value === 'auto'
          ? 'HIGH'
          : step.value.toUpperCase(),
        'gemini-4-neon',
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
  await waitForNativeOptionApplied(native, 'effort', 'auto')
  await expectSettingsOptionChosen(native.page, 'effort-auto')

  const request = await sendNativeAnswer(native, 'Reply after switching to a narrower model.', 'The narrower model ran at its default effort.')
  expectGoogleThinkingLevel(request, 'HIGH', 'gemini-3.8-flash')
})
