import { expect } from '@playwright/test'
import { isObject } from '../../../src/lib/jsonPick'
import { claudeTest } from '../claude-fixtures'
import { exerciseNativeOptionSequence } from '../helpers/nativeSettings'
import { chooseSettingsOption, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'

claudeTest('applies native fast speed and clears it across reloads with a fixed Opus model', async ({ native, page }) => {
  await waitForSettingsHydrated(page)
  // LeapMux offers Opus as one option, opus[1m]: the Worker collapses every Opus
  // spelling of the native catalog, bare "opus" included, onto that ID.
  await chooseSettingsOption(page, 'model-opus[1m]')
  await waitForSettingsIdle(page)
  let model: unknown
  await exerciseNativeOptionSequence(native, {
    groupId: 'fastMode',
    steps: [
      { value: 'off', via: 'choose' },
      { value: 'on', via: 'choose' },
      { value: 'on', via: 'reload' },
      { value: 'off', via: 'choose' },
      { value: 'off', via: 'reload' },
    ],
    nativeProof: (request, step, index) => {
      expect(request.protocol).toBe('anthropic-messages')
      if (!isObject(request.body))
        throw new Error('The native fast speed request has no object body.')
      if (index === 0) {
        expect(request.body.model).toEqual(expect.stringMatching(/^claude-opus-/))
        model = request.body.model
      }
      expect(request.body.model).toBe(model)
      if (step.value === 'on')
        expect(request.body.speed).toBe('fast')
      else
        expect(request.body).not.toHaveProperty('speed')
    },
  })
})
