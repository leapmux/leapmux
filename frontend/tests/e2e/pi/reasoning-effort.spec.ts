import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import { expect } from '@playwright/test'
import { MOCK_MODELS, PI_PLAIN_MODEL_ID } from '../helpers/mockAgentEnvironment'
import { exerciseAutomaticEffort, exerciseEffortModelRoundTrip, exerciseModelSwitchKeepsOption, exerciseNativeOption } from '../helpers/nativeSettings'
import { chooseSettingsOption, expectSettingsChip, waitForNativeSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'
import { piTest } from '../pi-fixtures'

/** Require the selected model at low effort in a native Pi Chat Completions request. */
function expectModelAtLowEffort(request: MockModelRequestRecord): void {
  expect(request.protocol).toBe('openai-chat-completions')
  expect(request.body).toMatchObject({ model: MOCK_MODELS.zai, reasoning_effort: 'low' })
}

piTest('keeps the low effort after a turn and reload', async ({ native }) => {
  await exerciseNativeOption(native, {
    groupId: 'effort',
    value: 'low',
    prepare: async () => {
      await chooseSettingsOption(native.page, `model-${MOCK_MODELS.zai}`)
      await waitForSettingsIdle(native.page)
    },
    nativeProof: expectModelAtLowEffort,
  })
  await expectSettingsChip(native.page, 'Low')
})

// Pi can move the thinking level when the model changes, and the update reads the level back after the switch.
piTest('keeps the chosen effort after a model switch and a reload', async ({ native }) => {
  await exerciseModelSwitchKeepsOption(native, {
    kept: { groupId: 'effort', value: 'low' },
    model: MOCK_MODELS.zai,
    nativeProof: expectModelAtLowEffort,
  })
})

// Pi offers a model that does not reason only Auto and Off (`pi/catalog.go`), and the static fallback lists one
// ladder for every model until Pi reports its live catalog. The menu of the default model must be the same before
// and after the trip. The Worker sends Auto with the switch to the plain model (`resetEffortToAutoIfUnsupported`),
// which relaunches Pi (`pi/settings.go`). Pi runs the plain model at Off and reports it, and the default model offers
// Off too, so the switch back keeps it, and the next request turns thinking off.
piTest('keeps one effort menu over a round trip through a model without the chosen level', async ({ native }) => {
  await exerciseEffortModelRoundTrip(native, {
    effortGroupId: 'effort',
    model: MOCK_MODELS.pi,
    chosen: 'low',
    via: PI_PLAIN_MODEL_ID,
    viaEfforts: ['auto', 'off'],
    settled: 'off',
    nativeProof: (request) => {
      expect(request.body).toMatchObject({ model: MOCK_MODELS.pi, thinking: { type: 'disabled' } })
      expect(request.body).not.toHaveProperty('reasoning_effort')
    },
  })
})

// Auto relaunches Pi without a thinking level (`pi/settings.go`, `IsEffortAutoTransition`), and Pi then reports the
// level of its configuration, Medium. The menu shows that level in place of Auto. Pi takes the level of a new model
// from the same configuration (`set_model`), so a model switch from an automatic session cannot tell a kept level from
// the default of the new model. Only the flash model states its level in the request (`supportsReasoningEffort`).
piTest('shows the level that Pi runs for an automatic effort', async ({ native }) => {
  await waitForNativeSettingsHydrated(native.page)
  await chooseSettingsOption(native.page, `model-${MOCK_MODELS.zai}`)
  await waitForSettingsIdle(native.page)
  await exerciseAutomaticEffort(native, {
    effortGroupId: 'effort',
    runs: 'medium',
    nativeProof: request => expect(request.body).toMatchObject({ model: MOCK_MODELS.zai, reasoning_effort: 'medium' }),
  })
})
