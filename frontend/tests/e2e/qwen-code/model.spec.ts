import { expect } from '@playwright/test'
import { QWEN_ALT_MODEL_ID, QWEN_ALT_MODEL_WIRE_ID } from '../helpers/mockAgentEnvironment'
import { exerciseNativeOption } from '../helpers/nativeSettings'
import { qwenTest } from '../qwen-fixtures'

qwenTest.describe('Qwen Code settings and goal', () => {
  qwenTest('switches the model for the next native request', async ({ native }) => {
    await exerciseNativeOption(native, {
      groupId: 'model',
      value: QWEN_ALT_MODEL_ID,
      nativeProof: request => expect(request.body).toHaveProperty('model', QWEN_ALT_MODEL_WIRE_ID),
    })
  })
})
