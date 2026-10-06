import { expect } from '@playwright/test'
import { exerciseNativeOption } from '../helpers/nativeSettings'
import { kimiTest } from '../kimi-fixtures'

kimiTest('sends the chosen effort into native turns before and after reload', async ({ native }) => {
  await exerciseNativeOption(native, { groupId: 'effort', value: 'low', nativeProof: (request) => {
    expect(request.body).toHaveProperty('reasoning_effort', 'low')
  } })
})
