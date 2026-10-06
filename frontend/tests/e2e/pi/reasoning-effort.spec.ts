import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import { expect } from '@playwright/test'
import { MOCK_MODELS } from '../helpers/mockAgentEnvironment'
import { exerciseModelSwitchKeepsOption, exerciseNativeOption } from '../helpers/nativeSettings'
import { chooseSettingsOption, expectSettingsChip, waitForSettingsIdle } from '../helpers/ui'
import { piTest } from '../pi-fixtures'

/** Require the selected model at low effort in a native Pi Chat Completions request. */
function expectModelAtLowEffort(request: MockModelRequestRecord): void {
  expect(request.protocol).toBe('openai-chat-completions')
  expect(request.body).toMatchObject({ model: MOCK_MODELS.zai, reasoning_effort: 'low' })
}

piTest('keeps the low effort after a turn and reload', async ({ native }) => {
  await exerciseNativeOption(native, {
    groupId: 'effort',
    value: 'low',
    prepare: async () => {
      await chooseSettingsOption(native.page, `model-${MOCK_MODELS.zai}`)
      await waitForSettingsIdle(native.page)
    },
    nativeProof: expectModelAtLowEffort,
  })
  await expectSettingsChip(native.page, 'Low')
})

// Pi can move the thinking level when the model changes, and the update reads the level back after the switch.
piTest('keeps the chosen effort after a model switch and a reload', async ({ native }) => {
  await exerciseModelSwitchKeepsOption(native, {
    kept: { groupId: 'effort', value: 'low' },
    model: MOCK_MODELS.zai,
    nativeProof: expectModelAtLowEffort,
  })
})
