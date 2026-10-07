import { expect } from '@playwright/test'
import { MOCK_MODELS, MOCK_PROVIDER_IDS, OPENCODE_FAMILY_PLAIN_MODEL_ID } from '../helpers/mockAgentEnvironment'
import { exerciseEffortModelRoundTrip, exerciseModelSwitchKeepsOption, exerciseNativeOption } from '../helpers/nativeSettings'
import { chooseSettingsOption, waitForSettingsIdle } from '../helpers/ui'
import { mimoTest } from '../mimo-fixtures'

mimoTest('sends the chosen effort into native turns before and after reload', async ({ native, page }) => {
  await exerciseNativeOption(native, { groupId: 'effort', value: 'low', prepare: async () => {
    await chooseSettingsOption(page, `model-${MOCK_PROVIDER_IDS.openCode}/${MOCK_MODELS.pi}`)
    await waitForSettingsIdle(page)
  }, nativeProof: (request) => {
    expect(request.body).toHaveProperty('reasoning_effort', 'low')
  } })
})

// MiMo keeps the model, the effort, and the mode as LeapMux state, so the next prompt must state both after a switch.
mimoTest('keeps the chosen effort after a model switch and a reload', async ({ native }) => {
  await exerciseModelSwitchKeepsOption(native, {
    kept: { groupId: 'effort', value: 'low' },
    model: `${MOCK_PROVIDER_IDS.openCode}/${MOCK_MODELS.pi}`,
    nativeProof: (request) => {
      expect(request.body).toMatchObject({ model: MOCK_MODELS.pi, reasoning_effort: 'low' })
    },
  })
})

// The plain model declares no variant, so MiMo offers it no effort (`mimo/catalog.go`, `mimoEfforts`). The Worker
// sends Auto with the switch to it and with the switch back (`resetEffortToAutoIfUnsupported`, MiMo manages its
// effort). Auto applies no variant (`mimoAutoEffort`), so the next request states no effort.
mimoTest('hides the effort for a model without levels and settles Auto after the round trip', async ({ native }) => {
  await exerciseEffortModelRoundTrip(native, {
    effortGroupId: 'effort',
    model: `${MOCK_PROVIDER_IDS.openCode}/${MOCK_MODELS.zai}`,
    chosen: 'low',
    via: OPENCODE_FAMILY_PLAIN_MODEL_ID,
    viaEfforts: 'hidden',
    settled: 'auto',
    nativeProof: (request) => {
      expect(request.body).toMatchObject({ model: MOCK_MODELS.zai })
      expect(request.body).not.toHaveProperty('reasoning_effort')
    },
  })
})
