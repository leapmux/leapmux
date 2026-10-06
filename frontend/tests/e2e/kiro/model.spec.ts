import { expect } from '@playwright/test'
import { exerciseNativeOption } from '../helpers/nativeSettings'
import { kiroTest } from '../kiro-fixtures'

kiroTest('sends the selected model into native requests before and after reload', async ({ native }) => {
  await exerciseNativeOption(native, { groupId: 'model', value: 'kiro-e2e-lite', nativeProof: (request) => {
    expect(request.body).toHaveProperty('conversationState.currentMessage.userInputMessage.modelId', 'kiro-e2e-lite')
  } })
})
