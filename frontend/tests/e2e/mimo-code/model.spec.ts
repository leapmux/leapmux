import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import { expect } from '@playwright/test'
import { MOCK_MODELS, MOCK_PROVIDER_IDS } from '../helpers/mockAgentEnvironment'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { nativeModelInstructionText } from '../helpers/nativeScenario'
import { chooseSettingsOption, expectSettingsChip, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'
import { mimoTest } from '../mimo-fixtures'

/** Require the selected model, the low reasoning variant, and the Plan prompt of the primary agent. */
function expectSelectedSettings(request: MockModelRequestRecord): void {
  expect(request.body).toHaveProperty('model', MOCK_MODELS.pi)
  expect(request.body).toHaveProperty('reasoning_effort', 'low')
  expect(nativeModelInstructionText(request)).toMatch(/Plan mode is (?:still )?active/i)
}

mimoTest.describe('MiMo Code settings', () => {
  // Check each selected setting in the next native model request:
  // - The model ID.
  // - The reasoning variant.
  // - The primary agent prompt.
  mimoTest('switches the model, the effort and the mode for the next prompt', async ({ native, page }) => {
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Build')

    await chooseSettingsOption(page, `model-${MOCK_PROVIDER_IDS.openCode}/${MOCK_MODELS.pi}`)
    await waitForSettingsIdle(page)
    await chooseSettingsOption(page, 'effort-low')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Low')
    await chooseSettingsOption(page, 'permissionMode-plan')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Plan')

    expectSelectedSettings(await sendNativeAnswer(native, 'Describe the plan in one word.', 'SETTINGS_APPLIED'))

    await page.reload()
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Plan')
    await expectSettingsChip(page, 'Low')
    expectSelectedSettings(await sendNativeAnswer(native, 'Reply after restoring the selected settings.', 'The restored settings answered.'))
  })
})
