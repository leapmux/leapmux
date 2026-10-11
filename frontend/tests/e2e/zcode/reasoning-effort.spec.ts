import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import { expect } from '@playwright/test'
import { MOCK_MODELS, MOCK_PROVIDER_IDS, ZCODE_PLAIN_MODEL_ID } from '../helpers/mockAgentEnvironment'
import { exerciseAutomaticEffort, exerciseEffortModelRoundTrip, exerciseModelSwitchKeepsOption, exerciseNativeOption } from '../helpers/nativeSettings'
import { expectSettingsChip } from '../helpers/ui'
import { zcodeTest } from '../zcode-fixtures'

/** The default model of the ZCode fixture. */
const FLASH_MODEL_ID = `${MOCK_PROVIDER_IDS.zcode}/${MOCK_MODELS.zai}`

/** Require low effort in a native ZCode Chat Completions request. */
function expectLowEffort(request: MockModelRequestRecord): void {
  expect(request.protocol).toBe('openai-chat-completions')
  expect(request.body).toMatchObject({ reasoning_effort: 'low' })
}

/** Require the flash model at the Max thought level in a native ZCode Chat Completions request. */
function expectFlashAtMax(request: MockModelRequestRecord): void {
  expect(request.protocol).toBe('openai-chat-completions')
  expect(request.body).toMatchObject({ model: MOCK_MODELS.zai, reasoning_effort: 'max' })
}

zcodeTest('keeps the low effort after a turn and reload', async ({ native }) => {
  await exerciseNativeOption(native, { groupId: 'effort', value: 'low', nativeProof: expectLowEffort })
  await expectSettingsChip(native.page, 'Low')
})

// Both mock models offer low, medium, and high. ZCode starts the new model at its own default level.
zcodeTest('keeps the chosen effort after a model switch and a reload', async ({ native }) => {
  await exerciseModelSwitchKeepsOption(native, {
    kept: { groupId: 'effort', value: 'low' },
    model: `${MOCK_PROVIDER_IDS.zcode}/${MOCK_MODELS.pi}`,
    nativeProof(request) {
      expectLowEffort(request)
      expect(request.body).toMatchObject({ model: MOCK_MODELS.pi })
    },
  })
})

// The plain model turns its reasoning off. ZCode (app 3.14.4) still reports a thought-level axis for it, with the
// levels Enabled and Disabled, so the effort control stays and a model without levels cannot hide it here. The round trip
// passes through a model that lacks Low. ZCode's snapshot then reports the level that the default model runs, Max
// (`zcode/settings.go`, `applySettingsSnapshotLocked`), and the next request carries it.
zcodeTest('settles a defined effort after a round trip through a model without the chosen level', async ({ native }) => {
  await exerciseEffortModelRoundTrip(native, {
    effortGroupId: 'effort',
    model: FLASH_MODEL_ID,
    chosen: 'low',
    via: ZCODE_PLAIN_MODEL_ID,
    viaEfforts: ['auto', 'enabled', 'disabled'],
    settled: 'max',
    nativeProof: expectFlashAtMax,
  })
})

// Auto relaunches the session with no thought level (`zcode/settings.go`, `IsEffortAutoTransition`), and ZCode reports
// the level that it then runs. The menu shows that level in place of Auto. ZCode starts each mock model at Max, so a
// model switch from an automatic session cannot tell a kept level from the default of the new model.
zcodeTest('shows the level that ZCode runs for an automatic effort', async ({ native }) => {
  await exerciseAutomaticEffort(native, { effortGroupId: 'effort', runs: 'max', nativeProof: expectFlashAtMax })
})
