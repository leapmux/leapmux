import { expect } from '@playwright/test'
import { QWEN_ALT_MODEL_ID, QWEN_ALT_MODEL_WIRE_ID } from '../helpers/mockAgentEnvironment'
import { exerciseModelSwitchKeepsOption, exerciseNativeOption } from '../helpers/nativeSettings'
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
