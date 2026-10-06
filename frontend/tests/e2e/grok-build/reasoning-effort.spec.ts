import { expect } from '@playwright/test'
import { grokTest } from '../grok-fixtures'
import { exerciseNativeOption } from '../helpers/nativeSettings'

grokTest('sends the chosen effort into native turns before and after reload', async ({ native }) => {
  await exerciseNativeOption(native, { groupId: 'reasoning_effort', value: 'high', nativeProof: (request) => {
    expect(request.body).toHaveProperty('reasoning_effort', 'high')
  } })
})
