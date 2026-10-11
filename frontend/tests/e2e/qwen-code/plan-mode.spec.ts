import { expect } from '@playwright/test'
import { nativeModelInstructionText } from '../helpers/nativeScenario'
import { exerciseNativeOption } from '../helpers/nativeSettings'
import { qwenTest } from '../qwen-fixtures'

qwenTest('enables native Plan mode from its setting before and after reload', async ({ native }) => {
  await exerciseNativeOption(native, { groupId: 'permissionMode', value: 'plan', nativeProof: (request) => {
    expect(nativeModelInstructionText(request)).toContain('Plan mode is active.')
  } })
})
