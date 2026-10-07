import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import { expect } from '@playwright/test'
import { grokTest } from '../grok-fixtures'
import { GROK_ALT_MODEL_ID, GROK_REASONING_MODEL_ID, MOCK_MODELS } from '../helpers/mockAgentEnvironment'
import { exerciseEffortModelRoundTrip, exerciseModelSwitchKeepsOption, exerciseNativeOption } from '../helpers/nativeSettings'

/** Require a native request of `model` at `effort`. */
function expectModelAtEffort(request: MockModelRequestRecord, model: string, effort: string): void {
  expect(request.body).toMatchObject({ model, reasoning_effort: effort })
}

grokTest('sends the chosen effort into native turns before and after reload', async ({ native }) => {
  await exerciseNativeOption(native, { groupId: 'reasoning_effort', value: 'high', nativeProof: (request) => {
    expect(request.body).toHaveProperty('reasoning_effort', 'high')
  } })
})

// Grok keeps the effort across a model switch when the new model offers it (`grok/settings_test.go`). The reasoning
// model starts at High, so Low differs from its default.
grokTest('keeps the chosen effort after a model switch and a reload', async ({ native }) => {
  await exerciseModelSwitchKeepsOption(native, {
    kept: { groupId: 'reasoning_effort', value: 'low' },
    model: GROK_REASONING_MODEL_ID,
    nativeProof: request => expectModelAtEffort(request, GROK_REASONING_MODEL_ID, 'low'),
  })
})

// The alternate model states no reasoning effort, so Grok offers no `reasoning_effort` option for it. When the model
// returns, Grok starts it at its configured default, Medium, and the ACP base shows the level that Grok reports.
grokTest('hides the effort for a model without levels and settles a defined effort after the round trip', async ({ native }) => {
  await exerciseEffortModelRoundTrip(native, {
    effortGroupId: 'reasoning_effort',
    model: MOCK_MODELS.grok,
    chosen: 'high',
    via: GROK_ALT_MODEL_ID,
    viaEfforts: 'hidden',
    settled: 'medium',
    nativeProof: request => expectModelAtEffort(request, MOCK_MODELS.grok, 'medium'),
  })
})
