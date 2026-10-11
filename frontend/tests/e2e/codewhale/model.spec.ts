import { expect } from '@playwright/test'
import { codewhaleTest } from '../codewhale-fixtures'
import { CODEWHALE_VISION_MODEL_ID } from '../helpers/mockAgentEnvironment'
import { exerciseNativeOption } from '../helpers/nativeSettings'

codewhaleTest('sends the selected model into native requests before and after reload', async ({ native }) => {
  await exerciseNativeOption(native, { groupId: 'model', value: CODEWHALE_VISION_MODEL_ID, nativeProof: (request) => {
    expect(request.body).toHaveProperty('model', CODEWHALE_VISION_MODEL_ID)
  } })
})
