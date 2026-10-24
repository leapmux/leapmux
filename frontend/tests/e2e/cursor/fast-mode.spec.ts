import { cursorTest } from '../cursor-fixtures'
import { exerciseNativeOptionSequence } from '../helpers/nativeSettings'
import { chooseSettingsOption, waitForSettingsIdle } from '../helpers/ui'
import { expectCursorNativeParameters } from './settingsScenario'

cursorTest('toggles native fast serving independently of the model and effort', async ({ native }) => {
  await chooseSettingsOption(native.page, 'model-mock-sonnet')
  await chooseSettingsOption(native.page, 'effort-high')
  await waitForSettingsIdle(native.page)
  await exerciseNativeOptionSequence(native, {
    groupId: 'fast',
    steps: [{ value: 'true', via: 'choose' }, { value: 'true', via: 'reload' }, { value: 'false', via: 'choose' }],
    nativeProof: (request, step) => expectCursorNativeParameters(request, 'mock-sonnet', { context: '200k', effort: 'high', thinking: 'false', fast: step.value }),
  })
})
