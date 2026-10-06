import { expect } from '@playwright/test'
import { grokTest } from '../grok-fixtures'
import { GROK_ALT_MODEL_ID } from '../helpers/mockAgentEnvironment'
import { exerciseNativeOption } from '../helpers/nativeSettings'

grokTest.describe('Grok Build settings, folder trust and MCP forms', () => {
  grokTest('switches the model for the next native request', async ({ native }) => {
    await exerciseNativeOption(native, {
      groupId: 'model',
      value: GROK_ALT_MODEL_ID,
      nativeProof: request => expect(request.body).toHaveProperty('model', GROK_ALT_MODEL_ID),
    })
  })
})
