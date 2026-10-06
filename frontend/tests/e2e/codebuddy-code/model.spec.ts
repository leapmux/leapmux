import { expect } from '@playwright/test'
import { codebuddyTest } from '../codebuddy-fixtures'
import { CODEBUDDY_ALT_MODEL_ID, CODEBUDDY_ALT_MODEL_WIRE_ID } from '../helpers/mockAgentEnvironment'
import { exerciseNativeOption } from '../helpers/nativeSettings'

codebuddyTest.describe('CodeBuddy Code settings', () => {
  codebuddyTest('switches the model for the next native request', async ({ native }) => {
    await exerciseNativeOption(native, {
      groupId: 'model',
      value: CODEBUDDY_ALT_MODEL_ID,
      nativeProof: request => expect(request.body).toMatchObject({ model: CODEBUDDY_ALT_MODEL_WIRE_ID }),
    })
  })
})
