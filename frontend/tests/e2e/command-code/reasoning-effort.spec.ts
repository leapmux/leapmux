import { expect } from '@playwright/test'
import { commandCodeTest } from '../command-code-fixtures'
import { exerciseNativeOption } from '../helpers/nativeSettings'

commandCodeTest('uses the selected native effort before and after reload', async ({ native: context }) => {
  await exerciseNativeOption(context, { groupId: 'effort', value: 'low', nativeProof: request => expect(request.body).toMatchObject({ model: 'command-code-e2e', reasoning_effort: 'low' }) })
})
