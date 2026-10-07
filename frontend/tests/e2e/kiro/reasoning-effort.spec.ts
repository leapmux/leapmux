import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import { expect } from '@playwright/test'
import { KIRO_DEFAULT_MOCK_MODEL } from '../helpers/kiroSurface'
import { exerciseEffortModelRoundTrip, exerciseModelSwitchKeepsOption, exerciseNativeOption, exerciseNativePlanWithEffort } from '../helpers/nativeSettings'
import { kiroTest } from '../kiro-fixtures'

/** The mock model that offers no effort axis. */
const KIRO_LITE_MODEL = 'kiro-e2e-lite'

/** Require a native Kiro turn of `model` at `effort`. */
function expectModelAtEffort(request: MockModelRequestRecord, model: string, effort: string): void {
  expect(request.body).toHaveProperty('conversationState.currentMessage.userInputMessage.modelId', model)
  expect(request.body).toHaveProperty('additionalModelRequestFields.output_config.effort', effort)
}

kiroTest('sends the chosen effort into native turns before and after reload', async ({ native }) => {
  await exerciseNativeOption(native, { groupId: 'effortLevel', value: 'high', nativeProof: (request) => {
    expect(request.body).toHaveProperty('additionalModelRequestFields.output_config.effort', 'high')
  } })
})

// A Kiro model write starts the effort at the default of the new model. Both models offer low, medium, and high.
kiroTest('keeps the chosen effort after a model switch and a reload', async ({ native }) => {
  await exerciseModelSwitchKeepsOption(native, {
    kept: { groupId: 'effortLevel', value: 'low' },
    model: 'kiro-e2e-thinking',
    nativeProof: request => expectModelAtEffort(request, 'kiro-e2e-thinking', 'low'),
  })
})

// Kiro offers its effort option only for a model whose schema lists effort levels, and the lite model lists none.
// A Kiro model write starts the effort at the default of the model, so the default model returns at High
// (`helpers/kiroSurface.ts`), and the ACP base shows the level that Kiro reports.
kiroTest('hides the effort for a model without levels and settles a defined effort after the round trip', async ({ native }) => {
  await exerciseEffortModelRoundTrip(native, {
    effortGroupId: 'effortLevel',
    model: KIRO_DEFAULT_MOCK_MODEL.modelId,
    chosen: 'low',
    via: KIRO_LITE_MODEL,
    viaEfforts: 'hidden',
    settled: 'high',
    nativeProof: request => expectModelAtEffort(request, KIRO_DEFAULT_MOCK_MODEL.modelId, 'high'),
  })
})

// Kiro reads the policy preset only when a session opens, so a new preset reopens the session through a relaunch
// (`kiro/settings.go`). The relaunch must carry the chosen effort into the reopened session.
kiroTest('keeps the chosen effort over the relaunch of a policy change', async ({ native }) => {
  await exerciseNativePlanWithEffort(native, {
    mode: { groupId: 'policyPreset', value: 'edit-workspace' },
    effort: { groupId: 'effortLevel', value: 'low' },
    restore: 'effort',
    nativeBuildProof: request => expectModelAtEffort(request, KIRO_DEFAULT_MOCK_MODEL.modelId, 'medium'),
    nativePlanProof: request => expectModelAtEffort(request, KIRO_DEFAULT_MOCK_MODEL.modelId, 'low'),
  })
})
