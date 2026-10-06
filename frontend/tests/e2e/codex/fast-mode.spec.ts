import { CODEX_OPTION, CODEX_OPTION_DEFAULT } from '../../../src/generated/contracts/codex-protocol'
import { codexTest, expect } from '../codex-fixtures'
import { exerciseNativeOptionSequence } from '../helpers/nativeSettings'

codexTest('applies Fast to native turns and clears it when Default returns', async ({ native }) => {
  const fast = 'fast'
  const standard = CODEX_OPTION_DEFAULT.ServiceTier
  await exerciseNativeOptionSequence(native, {
    groupId: CODEX_OPTION.ServiceTier,
    steps: [
      // A new session starts on the default tier, so the first turn chooses nothing.
      { value: standard, via: 'default' },
      { value: fast, via: 'choose' },
      { value: fast, via: 'reload' },
      { value: standard, via: 'choose' },
      { value: standard, via: 'reload' },
    ],
    nativeProof: (request, step) => {
      expect(request.protocol).toBe('openai-responses')
      if (step.value === fast)
        expect(request.body).toMatchObject({ service_tier: 'priority' })
      else
        expect(request.body).not.toHaveProperty('service_tier')
    },
  })
})
