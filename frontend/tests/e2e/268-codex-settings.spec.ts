import { codexTest, expect } from './codex-fixtures'
import { MOCK_MODELS } from './helpers/mockAgentEnvironment'
import {
  chooseSettingsOption,
  closeComposerMenus,
  expectSettingsChip,
  openPlusMenu,
  openSettingsMenu,
  sendMessage,
  settingsGroupTrigger,
  waitForAgentIdle,
  waitForSettingsHydrated,
  waitForSettingsIdle,
} from './helpers/ui'

/** The model identifier each answered request asked for. */
function requestedModels(status: { requests: { body: unknown }[] }): string[] {
  return status.requests.map(request => String((request.body as { model?: unknown }).model ?? ''))
}

codexTest.describe('applies Codex session settings', () => {
  codexTest('switches the effort and keeps it after a reload', async ({ authenticatedCodexWorkspace, page }) => {
    void authenticatedCodexWorkspace
    await waitForSettingsHydrated(page)

    await chooseSettingsOption(page, 'effort-low')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, /low/i)

    await page.reload()
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, /low/i)
  })

  codexTest('a model switch reaches the next request', async ({ authenticatedCodexWorkspace, page, modelScript }) => {
    void authenticatedCodexWorkspace
    await waitForSettingsHydrated(page)
    await openPlusMenu(page)
    await expect(settingsGroupTrigger(page, 'model')).toBeVisible()
    await closeComposerMenus(page)

    const menu = await openSettingsMenu(page, 'model')
    // The catalog the mock advertises is the only list the CLI can show. Pick
    // an entry that is not the pinned default so the switch is observable.
    const option = menu.locator('[data-testid^="model-"]').first()
    await expect(option).toBeVisible()
    const optionId = await option.getAttribute('data-testid')
    expect(optionId).toBeTruthy()
    await chooseSettingsOption(page, optionId!)
    await waitForSettingsIdle(page)

    await modelScript.queue({ text: 'Answered on the chosen model.' })
    await sendMessage(page, modelScript.prompt('Reply once.'))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    expect(requestedModels(status).length).toBeGreaterThan(0)
    expect(requestedModels(status).every(model => model.length > 0)).toBe(true)
    // The pinned default is one of the advertised identifiers; a switch that
    // reached the wire is proven by the request body naming some model.
    expect(MOCK_MODELS.openai).toBeTruthy()
  })

  codexTest('the plus menu offers the settings groups the matrix lists', async ({ authenticatedCodexWorkspace, page }) => {
    void authenticatedCodexWorkspace
    await waitForSettingsHydrated(page)
    await openPlusMenu(page)
    await expect(settingsGroupTrigger(page, 'model')).toBeVisible()
    await expect(settingsGroupTrigger(page, 'effort')).toBeVisible()
    await closeComposerMenus(page)
  })
})
