import { KIMI_MOCK_MODELS, MOCK_MODELS } from './helpers/mockAgentEnvironment'
import { chooseSettingsOption, closeComposerMenus, expectNoSettingsChip, expectSettingsChip, openPlusMenu, openSettingsMenu, sendMessage, settingsGroupTrigger, waitForAgentIdle, waitForSettingsHydrated, waitForSettingsIdle } from './helpers/ui'
import { expect, KIMI_E2E_SKIP_REASON, kimiTest } from './kimi-fixtures'

kimiTest.skip(!!KIMI_E2E_SKIP_REASON, KIMI_E2E_SKIP_REASON || '')

/** The model identifier each answered request asked for. */
function requestedModels(status: { requests: { body: unknown }[] }): string[] {
  return status.requests.map(request => String((request.body as { model?: unknown }).model ?? ''))
}

kimiTest.describe('applies Kimi Code session settings', () => {
  kimiTest('switches the model, the effort, the mode, and swarm, and keeps them after a reload', async ({ authenticatedKimiWorkspace, page }) => {
    void authenticatedKimiWorkspace
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'GLM-5.3 Flash')
    await expectSettingsChip(page, 'High')

    await chooseSettingsOption(page, 'effort-low')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Low')

    await chooseSettingsOption(page, 'permissionMode-yolo')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Ask When Needed')

    await chooseSettingsOption(page, 'swarmMode-on')
    await waitForSettingsIdle(page)
    const swarm = await openSettingsMenu(page, 'swarmMode')
    await expect(swarm.locator('[data-testid="swarmMode-on"]')).toHaveAttribute('aria-checked', 'true')
    await closeComposerMenus(page)

    await page.reload()
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Low')
    await expectSettingsChip(page, 'Ask When Needed')
    const swarmAfter = await openSettingsMenu(page, 'swarmMode')
    await expect(swarmAfter.locator('[data-testid="swarmMode-on"]')).toHaveAttribute('aria-checked', 'true')
    await closeComposerMenus(page)
  })

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
  })
})
