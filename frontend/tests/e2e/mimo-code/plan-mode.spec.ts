import { expect } from '@playwright/test'
import { nativeModelInstructionText } from '../helpers/nativeScenario'
import { exerciseNativeOption } from '../helpers/nativeSettings'
import { mimoTest } from '../mimo-fixtures'

mimoTest('enables native Plan mode from its setting before and after reload', async ({ native }) => {
  await exerciseNativeOption(native, { groupId: 'permissionMode', value: 'plan', nativeProof: (request) => {
    const instruction = nativeModelInstructionText(request)
    expect(instruction).toMatch(/Plan mode is (?:still )?active/i)
    expect(instruction).toContain('read-only')
  } })
})
