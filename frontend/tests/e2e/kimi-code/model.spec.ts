import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { KIMI_MOCK_MODELS, MOCK_MODELS } from '../helpers/mockAgentEnvironment'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { chooseSettingsOption, closeComposerMenus, expectNoSettingsChip, expectSettingsChip, openPlusMenu, sendMessage, settingsGroupTrigger, waitForAgentIdle, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'
import { kimiTest } from '../kimi-fixtures'

/** The model identifier each answered request asked for. */
function requestedModels(status: { requests: { body: unknown }[] }): string[] {
  return status.requests.map(request => String((request.body as { model?: unknown }).model ?? ''))
}

kimiTest.describe('applies Kimi Code session settings', () => {
  // The second model thinks at no level, so the effort axis leaves with it.
  kimiTest('a model switch reaches the next request and drops the effort axis', async ({ authenticatedKimiWorkspace, page, modelScript }) => {
    void authenticatedKimiWorkspace
    await waitForSettingsHydrated(page)

    await chooseSettingsOption(page, `model-${KIMI_MOCK_MODELS.plain}`)
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'GLM-5.3')
    await expectNoSettingsChip(page, 'GLM-5.3 Flash')
    await openPlusMenu(page)
    await expect(settingsGroupTrigger(page, 'model')).toBeVisible()
    await expect(settingsGroupTrigger(page, 'effort')).toHaveCount(0)
    await closeComposerMenus(page)

    await modelScript.queue({ text: 'Answered on the plain model.' })
    await sendMessage(page, modelScript.prompt('Reply on the plain model.'))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    expect(requestedModels(status)).toEqual([MOCK_MODELS.pi])
    expect(status.requests.find(request => request.stepIndex === 0)?.body).not.toHaveProperty('reasoning_effort')

    await page.reload()
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'GLM-5.3')
    await openPlusMenu(page)
    await expect(settingsGroupTrigger(page, 'effort')).toHaveCount(0)
    await closeComposerMenus(page)
    const restored = await sendNativeAnswer({ page, modelScript, provider: AgentProvider.KIMI_CODE }, 'Reply after restoring the plain model.', 'The plain model still answered.')
    expect(restored.body).toHaveProperty('model', MOCK_MODELS.pi)
    expect(restored.body).not.toHaveProperty('reasoning_effort')
  })
})
