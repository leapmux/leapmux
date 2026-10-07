import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import { expect } from '@playwright/test'
import { KIMI_MOCK_MODELS, MOCK_MODELS } from '../helpers/mockAgentEnvironment'
import { exerciseEffortModelRoundTrip, exerciseModelSwitchKeepsOption, exerciseNativeOption } from '../helpers/nativeSettings'
import { kimiTest } from '../kimi-fixtures'

/** Require a native request of `model` at `effort`. Kimi sends the `model` of an alias, not the alias. */
function expectModelAtEffort(request: MockModelRequestRecord, model: string, effort: string): void {
  expect(request.body).toMatchObject({ model, reasoning_effort: effort })
}

kimiTest('sends the chosen effort into native turns before and after reload', async ({ native }) => {
  await exerciseNativeOption(native, { groupId: 'effort', value: 'low', nativeProof: (request) => {
    expect(request.body).toHaveProperty('reasoning_effort', 'low')
  } })
})

// The alternate thinking model takes the ladder of the default model and starts at Medium, so Low differs from its default
// (`kimi/settings.go`, `kimiModelTakesEffort`).
kimiTest('keeps the chosen effort after a model switch and a reload', async ({ native }) => {
  await exerciseModelSwitchKeepsOption(native, {
    kept: { groupId: 'effort', value: 'low' },
    model: KIMI_MOCK_MODELS.alternateThinking,
    nativeProof: request => expectModelAtEffort(request, MOCK_MODELS.deepseek, 'low'),
  })
})

// The plain model cannot think, so it offers no level (`kimi/catalog.go`, `kimiModelEfforts`). The Worker sends Auto
// with the switch to it and with the switch back (`resetEffortToAutoIfUnsupported`, Kimi Code manages its effort).
// Auto sends no level, but the server keeps the level of a session across a model switch, so Kimi Code states the
// configured thinking of the model for Auto (`restateAutoThinking`): High, its `default_effort`.
kimiTest('hides the effort for a model without levels and settles a defined effort after the round trip', async ({ native }) => {
  await exerciseEffortModelRoundTrip(native, {
    effortGroupId: 'effort',
    model: KIMI_MOCK_MODELS.thinking,
    chosen: 'low',
    via: KIMI_MOCK_MODELS.plain,
    viaEfforts: 'hidden',
    settled: 'auto',
    nativeProof: request => expectModelAtEffort(request, MOCK_MODELS.zai, 'high'),
  })
})
