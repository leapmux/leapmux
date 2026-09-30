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
  codexTest('sends the selected effort and keeps it after a reload', async ({ authenticatedCodexWorkspace, page, modelScript }) => {
    void authenticatedCodexWorkspace
    await waitForSettingsHydrated(page)

    await chooseSettingsOption(page, 'effort-low')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, /low/i)

    await modelScript.queue({ text: 'Codex answered at low effort.' })
    await sendMessage(page, modelScript.prompt('Reply once after the effort switch.'))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    expect(status.requests.find(request => request.stepIndex === 0)?.body).toMatchObject({ reasoning: { effort: 'low' } })

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
    const option = menu.locator(
      `[data-testid^="model-"]:not([data-testid="model-default"]):not([data-testid="model-${MOCK_MODELS.openai}"])`,
    ).first()
    await expect(option).toBeVisible()
    const optionId = await option.getAttribute('data-testid')
    expect(optionId).toBeTruthy()
    const selectedModel = optionId!.slice('model-'.length)
    await chooseSettingsOption(page, optionId!)
    await waitForSettingsIdle(page)

    await modelScript.queue({ text: 'Answered on the chosen model.' })
    await sendMessage(page, modelScript.prompt('Reply once.'))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    expect(requestedModels(status)).toContain(selectedModel)
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
