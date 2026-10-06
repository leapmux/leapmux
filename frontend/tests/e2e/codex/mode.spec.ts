import { expect } from '@playwright/test'
import { CODEX_OPTION } from '../../../src/generated/contracts/codex-protocol'
import { codexTest } from '../codex-fixtures'
import { nativeModelInstructionText } from '../helpers/nativeScenario'
import { exerciseNativeOption } from '../helpers/nativeSettings'

codexTest('applies and restores the selected native collaboration mode', async ({ native }) => {
  await exerciseNativeOption(native, {
    groupId: CODEX_OPTION.CollaborationMode,
    value: 'plan',
    nativeProof: (request) => {
      expect(request.protocol).toBe('openai-responses')
      const instructions = nativeModelInstructionText(request)
      expect(instructions).toContain('<collaboration_mode>')
      expect(instructions).toMatch(/plan mode/i)
    },
  })
})
