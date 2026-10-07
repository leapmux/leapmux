import { expect } from '@playwright/test'
import { MOCK_MODELS, QWEN_ALT_MODEL_ID, QWEN_ALT_MODEL_WIRE_ID, QWEN_MODEL_ID, QWEN_PLAIN_MODEL_ID } from '../helpers/mockAgentEnvironment'
import { exerciseEffortModelRoundTrip, exerciseModelSwitchKeepsOption, exerciseNativeOption } from '../helpers/nativeSettings'
import { qwenTest } from '../qwen-fixtures'

qwenTest('sends the chosen effort into native turns before and after reload', async ({ native }) => {
  await exerciseNativeOption(native, { groupId: 'reasoning_effort', value: 'low', nativeProof: (request) => {
    expect(request.body).toHaveProperty('reasoning_effort', 'low')
  } })
})

// Both models offer low, medium, and high. The server keeps the tier across the switch, so the kept
// value must reach the next native request of the new model.
qwenTest('keeps the chosen effort after a model switch and a reload', async ({ native }) => {
  await exerciseModelSwitchKeepsOption(native, {
    kept: { groupId: 'reasoning_effort', value: 'low' },
    model: QWEN_ALT_MODEL_ID,
    nativeProof: (request) => {
      expect(request.body).toMatchObject({ model: QWEN_ALT_MODEL_WIRE_ID, reasoning_effort: 'low' })
    },
  })
})

// Qwen Code 0.24.7 states no `reasoning_effort` option for a model without a reasoning capability, so the control
// hides. When the reasoning model returns, Qwen starts it at its `defaultEffort`, High, and the ACP base shows it.
qwenTest('hides the effort for a model without reasoning and settles a defined effort after the round trip', async ({ native }) => {
  await exerciseEffortModelRoundTrip(native, {
    effortGroupId: 'reasoning_effort',
    model: QWEN_MODEL_ID,
    chosen: 'low',
    via: QWEN_PLAIN_MODEL_ID,
    viaEfforts: 'hidden',
    settled: 'high',
    nativeProof: request => expect(request.body).toMatchObject({ model: MOCK_MODELS.qwen, reasoning_effort: 'high' }),
  })
})
