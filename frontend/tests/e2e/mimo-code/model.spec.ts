import { expect } from '@playwright/test'

import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { MOCK_MODELS, MOCK_PROVIDER_IDS } from '../helpers/mockAgentEnvironment'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { nativeLastStepBody, nativeModelInstructionText } from '../helpers/nativeScenario'

import { chooseSettingsOption, expectSettingsChip, sendMessage, waitForAgentIdle, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'
import { mimoTest } from '../mimo-fixtures'

mimoTest.describe('MiMo Code settings', () => {
  // Check each selected setting in the next native model request:
  // - The model ID.
  // - The reasoning variant.
  // - The primary agent prompt.
  mimoTest('switches the model, the effort and the mode for the next prompt', async ({ authenticatedMiMoWorkspace, page, modelScript }) => {
    void authenticatedMiMoWorkspace
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

    await modelScript.queue({ text: 'SETTINGS_APPLIED' })
    await sendMessage(page, modelScript.prompt('Describe the plan in one word.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    const body = nativeLastStepBody((await modelScript.status()).requests)
    expect(body.model).toBe(MOCK_MODELS.pi)
    expect(body.reasoning_effort).toBe('low')
    expect(JSON.stringify(body.messages)).toMatch(/plan mode/i)

    await page.reload()
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Plan')
    await expectSettingsChip(page, 'Low')
    const restored = await sendNativeAnswer({ page, modelScript, provider: AgentProvider.MIMO_CODE }, 'Reply after restoring the selected settings.', 'The restored settings answered.')
    expect(restored.body).toHaveProperty('model', MOCK_MODELS.pi)
    expect(restored.body).toHaveProperty('reasoning_effort', 'low')
    expect(nativeModelInstructionText(restored)).toMatch(/Plan mode is (?:still )?active/i)
  })
})
