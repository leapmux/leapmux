import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import { expect } from '@playwright/test'
import { commandCodeTest } from '../command-code-fixtures'
import { COMMAND_CODE_ALT_MODEL_ID, COMMAND_CODE_MODEL_ID, COMMAND_CODE_REASONING_MODEL_ID } from '../helpers/mockAgentEnvironment'
import { exerciseEffortModelRoundTrip, exerciseModelSwitchKeepsOption, exerciseNativeOption, exerciseNativePlanWithEffort } from '../helpers/nativeSettings'

/** The wire ID of a Command Code model, which drops the provider of the mock catalog. */
function wireModel(modelID: string): string {
  return modelID.slice(modelID.indexOf('/') + 1)
}

/** Require a native request of `modelID` at `effort`. */
function expectModelAtEffort(request: MockModelRequestRecord, modelID: string, effort: string): void {
  expect(request.body).toMatchObject({ model: wireModel(modelID), reasoning_effort: effort })
}

commandCodeTest('uses the selected native effort before and after reload', async ({ native: context }) => {
  await exerciseNativeOption(context, { groupId: 'effort', value: 'low', nativeProof: request => expect(request.body).toMatchObject({ model: 'command-code-e2e', reasoning_effort: 'low' }) })
})

// The host keeps its effort across `set_model` when the new model offers it. Without a pinned effort the host runs a
// model at High (the round trip below shows it), so Low differs from what the new model would start at.
commandCodeTest('keeps the chosen effort after a model switch and a reload', async ({ native }) => {
  await exerciseModelSwitchKeepsOption(native, {
    kept: { groupId: 'effort', value: 'low' },
    model: COMMAND_CODE_REASONING_MODEL_ID,
    nativeProof: request => expectModelAtEffort(request, COMMAND_CODE_REASONING_MODEL_ID, 'low'),
  })
})

// The alternate model declares no reasoning, so it offers no effort. The Worker sends Auto with the switch to it and
// with the switch back (`resetEffortToAutoIfUnsupported`, Command Code manages its effort). Command Code sends no
// setter for Auto (`commandcode/settings.go`), so the host settles the default model at the level that it chooses,
// High, and reports it. The menu of the default model must be the same after the trip as before it.
commandCodeTest('hides the effort for a model without levels and settles a defined effort after the round trip', async ({ native }) => {
  await exerciseEffortModelRoundTrip(native, {
    effortGroupId: 'effort',
    model: COMMAND_CODE_MODEL_ID,
    chosen: 'low',
    via: COMMAND_CODE_ALT_MODEL_ID,
    viaEfforts: 'hidden',
    settled: 'high',
    nativeProof: request => expectModelAtEffort(request, COMMAND_CODE_MODEL_ID, 'high'),
  })
})

// A new permission mode relaunches the host (`commandcode/settings.go`), and the effort is a launch option
// (`--effort`, `commandcode/start.go`), so the relaunch must carry the chosen effort.
commandCodeTest('keeps the chosen effort over the relaunch of a mode change', async ({ native }) => {
  await exerciseNativePlanWithEffort(native, {
    mode: { groupId: 'permissionMode', value: 'plan' },
    effort: { groupId: 'effort', value: 'low' },
    restore: 'effort',
    nativeBuildProof: request => expectModelAtEffort(request, COMMAND_CODE_MODEL_ID, 'high'),
    nativePlanProof: request => expectModelAtEffort(request, COMMAND_CODE_MODEL_ID, 'low'),
  })
})
