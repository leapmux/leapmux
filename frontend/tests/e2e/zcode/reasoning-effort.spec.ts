import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import { expect } from '@playwright/test'
import { MOCK_MODELS, MOCK_PROVIDER_IDS } from '../helpers/mockAgentEnvironment'
import { exerciseModelSwitchKeepsOption, exerciseNativeOption } from '../helpers/nativeSettings'
import { expectSettingsChip } from '../helpers/ui'
import { zcodeTest } from '../zcode-fixtures'

/** Require low effort in a native ZCode Chat Completions request. */
function expectLowEffort(request: MockModelRequestRecord): void {
  expect(request.protocol).toBe('openai-chat-completions')
  expect(request.body).toMatchObject({ reasoning_effort: 'low' })
}

zcodeTest('keeps the low effort after a turn and reload', async ({ native }) => {
  await exerciseNativeOption(native, { groupId: 'effort', value: 'low', nativeProof: expectLowEffort })
  await expectSettingsChip(native.page, 'Low')
})

// Both mock models offer low, medium, and high. ZCode starts the new model at its own default level.
zcodeTest('keeps the chosen effort after a model switch and a reload', async ({ native }) => {
  await exerciseModelSwitchKeepsOption(native, {
    kept: { groupId: 'effort', value: 'low' },
    model: `${MOCK_PROVIDER_IDS.zcode}/${MOCK_MODELS.pi}`,
    nativeProof(request) {
      expectLowEffort(request)
      expect(request.body).toMatchObject({ model: MOCK_MODELS.pi })
    },
  })
})
