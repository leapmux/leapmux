import { expect } from '@playwright/test'
import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { DEEPSEEK_HARNESS_ALT_MODEL_ID, DEEPSEEK_HARNESS_MODEL_ID, DEEPSEEK_HARNESS_PLAIN_MODEL_ID } from '../helpers/deepseekHarnessEnvironment'
import { exerciseEffortModelRoundTrip, exerciseModelSwitchKeepsOption, exerciseNativeOption } from '../helpers/nativeSettings'

deepseekHarnessTest('uses the selected native reasoning effort before and after reload', async ({ native }) => {
  await exerciseNativeOption(native, { groupId: 'effort', value: 'low', nativeProof: request => expect(request.body).toMatchObject({ thinking: { type: 'enabled' }, output_config: { effort: 'low' } }) })
})

// Both models offer low. The second model starts at another default, so a reset shows.
deepseekHarnessTest('keeps the chosen effort after a model switch and a reload', async ({ native }) => {
  await exerciseModelSwitchKeepsOption(native, {
    kept: { groupId: 'effort', value: 'low' },
    model: DEEPSEEK_HARNESS_ALT_MODEL_ID,
    nativeProof: request => expect(request.body).toMatchObject({ model: 'deepseek-v4-pro', thinking: { type: 'enabled' }, output_config: { effort: 'low' } }),
  })
})

// The plain route declares a model that does not reason (`reasoningEfforts: false`), so the native catalog gives it no
// reasoning and LeapMux offers it no effort (`deepseekharness/catalog.go`). The Worker sends Auto with the switch to it
// and with the switch back (`resetEffortToAutoIfUnsupported`, DeepSeek Harness manages its effort), and the provider
// resolves Auto to the default effort of the model, High (`modelCatalog.resolve`).
deepseekHarnessTest('hides the effort for a model without levels and settles a defined effort after the round trip', async ({ native }) => {
  await exerciseEffortModelRoundTrip(native, {
    effortGroupId: 'effort',
    model: DEEPSEEK_HARNESS_MODEL_ID,
    chosen: 'low',
    via: DEEPSEEK_HARNESS_PLAIN_MODEL_ID,
    viaEfforts: 'hidden',
    settled: 'high',
    nativeProof: request => expect(request.body).toMatchObject({ model: 'deepseek-flash', thinking: { type: 'enabled' }, output_config: { effort: 'high' } }),
  })
})
