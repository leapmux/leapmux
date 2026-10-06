import { expect } from '@playwright/test'
import { MOCK_MODELS } from '../helpers/mockAgentEnvironment'
import { nativeModelInstructionText } from '../helpers/nativeScenario'
import { exerciseNativeOption } from '../helpers/nativeSettings'
import { mimoTest } from '../mimo-fixtures'

mimoTest('applies the native primary agent mode before and after reload', async ({ native }) => {
  await exerciseNativeOption(native, { groupId: 'permissionMode', value: 'plan', nativeProof: (request) => {
    const instruction = nativeModelInstructionText(request)
    expect(instruction).toMatch(/Plan mode is (?:still )?active/i)
    expect(instruction).toMatch(/read-only/i)
    expect(request.body).toHaveProperty('model', MOCK_MODELS.zai)
    expect(request.body).toHaveProperty('reasoning_effort', 'high')
  } })
})
