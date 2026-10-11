import { expect } from '@playwright/test'
import { currentNativeAgent, nativeOptionGroup } from '../helpers/nativeScenario'
import { exerciseNativeOptionSequence } from '../helpers/nativeSettings'
import { chooseSettingsOption, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'
import { kiroTest } from '../kiro-fixtures'

kiroTest('applies thinking independently of a fixed native model and effort before and after reload', async ({ native, page }) => {
  await waitForSettingsHydrated(page)
  await chooseSettingsOption(page, 'model-kiro-e2e-thinking')
  await waitForSettingsIdle(page)
  await chooseSettingsOption(page, 'effortLevel-medium')
  await waitForSettingsIdle(page)
  // Kiro 2.24 offers the `thinking` option, with the values `on` and `off`, only
  // for a model whose catalog schema lists both `adaptive` and `disabled` as
  // `thinking.type`. Its request then states `adaptive` for On and `disabled`
  // for Off, and never `enabled`.
  const thinking = nativeOptionGroup(await currentNativeAgent(native), 'thinking')
  expect(thinking?.mutable).toBe(true)
  expect(thinking?.options.map(option => option.id).sort()).toEqual(['off', 'on'])
  await exerciseNativeOptionSequence(native, {
    groupId: 'thinking',
    steps: [
      { value: 'off', via: 'choose' },
      { value: 'on', via: 'choose' },
      { value: 'on', via: 'reload' },
      { value: 'off', via: 'choose' },
      { value: 'off', via: 'reload' },
    ],
    nativeProof: (request, step) => {
      expect(request.body).toHaveProperty('conversationState.currentMessage.userInputMessage.modelId', 'kiro-e2e-thinking')
      expect(request.body).toHaveProperty('additionalModelRequestFields.output_config.effort', 'medium')
      expect(request.body).toHaveProperty('additionalModelRequestFields.thinking.type', step.value === 'on' ? 'adaptive' : 'disabled')
    },
  })
})
