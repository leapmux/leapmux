import { expect } from '@playwright/test'
import { droidTest } from '../droid-fixtures'
import { DROID_MOCK_MODEL_IDS } from '../helpers/mockAgentEnvironment'
import { exerciseEffortModelRoundTrip, exerciseModelSwitchKeepsOption, exerciseNativeOption } from '../helpers/nativeSettings'
import { chooseSettingsOption, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'
import { expectDroidNativeSettings } from './settingsUpdates'

/** The built-in model whose effort ladder both effort specs use. */
const BUILT_IN_MODEL = 'claude-fable-5.1'

/** Droid sends a built-in model to the mock through its own Anthropic route. */
const BUILT_IN_ROUTE = '/v1/api/llm/a/v1/messages'

droidTest.describe('Factory Droid settings', () => {
  droidTest('sends a built-in model effort to the isolated mock', async ({ native, page }) => {
    await exerciseNativeOption(native, {
      groupId: 'effort',
      value: 'high',
      prepare: async () => {
        await waitForSettingsHydrated(page)
        await chooseSettingsOption(page, `model-${BUILT_IN_MODEL}`)
        await waitForSettingsIdle(page)
      },
      nativeProof: async (request) => {
        expect(request.path).toBe(BUILT_IN_ROUTE)
        expect(request.body).toMatchObject({ model: BUILT_IN_MODEL, output_config: { effort: 'high' } })
        await expectDroidNativeSettings(native, { modelId: BUILT_IN_MODEL, reasoningEffort: 'high' })
      },
    })

    await chooseSettingsOption(page, `model-${DROID_MOCK_MODEL_IDS.primary}`)
    await waitForSettingsIdle(page)
  })
})

droidTest.describe('Factory Droid model switch', () => {
  // The native session keeps the effort when the new model supports it. The two built-in models share the ladder.
  droidTest('keeps the chosen effort after a model switch and a reload', async ({ native, page }) => {
    await exerciseModelSwitchKeepsOption(native, {
      prepare: async () => {
        await waitForSettingsHydrated(page)
        await chooseSettingsOption(page, `model-${BUILT_IN_MODEL}`)
        await waitForSettingsIdle(page)
      },
      kept: { groupId: 'effort', value: 'low' },
      model: 'claude-opus-5',
      nativeProof: (request) => {
        expect(request.path).toBe(BUILT_IN_ROUTE)
        expect(request.body).toMatchObject({ model: 'claude-opus-5', output_config: { effort: 'low' } })
      },
    })
  })

  // The static effort group lists Droid's four levels until the running agent reports the live ladder of each model
  // (`droid/catalog.go`). A built-in model adds Xhigh and Max to the ladder of a custom model. The menu of the built-in
  // model must be the same before and after a trip through a custom model, which lacks Max.
  droidTest('keeps one effort menu over a round trip through a model without the chosen level', async ({ native }) => {
    await exerciseEffortModelRoundTrip(native, {
      effortGroupId: 'effort',
      model: BUILT_IN_MODEL,
      chosen: 'max',
      via: DROID_MOCK_MODEL_IDS.alternate,
      viaEfforts: ['off', 'low', 'medium', 'high'],
      settled: 'high',
      nativeProof: (request) => {
        expect(request.path).toBe(BUILT_IN_ROUTE)
        expect(request.body).toMatchObject({ model: BUILT_IN_MODEL, output_config: { effort: 'high' } })
      },
    })
  })
})
