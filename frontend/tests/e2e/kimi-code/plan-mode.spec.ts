import { expect } from '@playwright/test'
import { nativeModelInstructionText } from '../helpers/nativeScenario'
import { exerciseNativeOption } from '../helpers/nativeSettings'
import { kimiTest } from '../kimi-fixtures'
import { kimiPlanModeFromInstructions } from './planScenario'

kimiTest('enables native Plan mode from its setting before and after reload', async ({ native }) => {
  await exerciseNativeOption(native, {
    groupId: 'permissionMode',
    value: 'plan',
    nativeProof: (request) => {
      expect(kimiPlanModeFromInstructions(nativeModelInstructionText(request))).toBe(true)
    },
  })
})
