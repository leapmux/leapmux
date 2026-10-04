import { expect } from '@playwright/test'
import { codexTest } from '../codex-fixtures'
import { MOCK_MODELS } from '../helpers/mockAgentEnvironment'
import { chooseSettingsOption, closeComposerMenus, expectAssistantAnswer, expectSettingsOptionChosen, openPlusMenu, openSettingsMenu, sendMessage, settingsGroupTrigger, waitForAgentIdle, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'

codexTest.describe('applies Codex session settings', () => {
  codexTest('a model switch reaches the next request', async ({ authenticatedCodexWorkspace, page, modelScript }) => {
    void authenticatedCodexWorkspace
    await waitForSettingsHydrated(page)
    await openPlusMenu(page)
    await expect(settingsGroupTrigger(page, 'model')).toBeVisible()
    await closeComposerMenus(page)

    const menu = await openSettingsMenu(page, 'model')
    // Select a mock catalog entry that differs from the pinned default.
    const option = menu.locator(
      `[data-testid^="model-"]:not([data-testid="model-default"]):not([data-testid="model-${MOCK_MODELS.openai}"])`,
    ).first()
    await expect(option).toBeVisible()
    const optionId = await option.getAttribute('data-testid')
    if (!optionId?.startsWith('model-'))
      throw new Error('The selected model option has no model identifier.')
    const selectedModel = optionId.slice('model-'.length)
    await chooseSettingsOption(page, optionId)
    await waitForSettingsIdle(page)

    await modelScript.queue({ text: 'Answered on the chosen model.' })
    await sendMessage(page, modelScript.prompt('Reply once.'))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    const first = status.requests.find(request => request.stepIndex === 0)
    expect(first?.protocol).toBe('openai-responses')
    expect(first?.body).toMatchObject({ model: selectedModel })
    await expectAssistantAnswer(page, { answer: /Answered on the chosen model\./ })
    await page.reload()
    await waitForSettingsHydrated(page)
    await expectSettingsOptionChosen(page, optionId)
    await modelScript.queue({ text: 'The restored model answered the next turn.' })
    await sendMessage(page, modelScript.prompt('Reply once after restoring the model.'))
    const restored = await modelScript.waitForSteps(2)
    await waitForAgentIdle(page)
    const next = restored.requests.find(request => request.stepIndex === 1)
    expect(next?.protocol).toBe('openai-responses')
    expect(next?.body).toMatchObject({ model: selectedModel })
    await expectAssistantAnswer(page, { answer: /The restored model answered the next turn\./ })
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
