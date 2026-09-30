import { MOCK_MODELS } from './helpers/mockAgentEnvironment'
import {
  ARITHMETIC_ANSWER_TEXT,
  ARITHMETIC_PROMPT,
  chooseSettingsOption,
  closeComposerMenus,
  expectAssistantAnswer,
  expectSettingsChip,
  expectSettingsOptionChosen,
  openPlusMenu,
  sendMessage,
  settingsGroupTrigger,
  waitForAgentIdle,
  waitForSettingsHydrated,
  waitForSettingsIdle,
} from './helpers/ui'
import { expect, LETTA_E2E_SKIP_REASON, LETTA_TITLE_RULE, lettaTest } from './letta-fixtures'

/**
 * 265 — Letta Code settings.
 *
 * The isolated provider config points at the mock model server. The model
 * menu reads native model handles, and a model write waits for its reply.
 * A permission-mode change needs a session restart.
 */
lettaTest.skip(!!LETTA_E2E_SKIP_REASON, LETTA_E2E_SKIP_REASON || '')

lettaTest.describe('Letta Code settings', () => {
  lettaTest('offers the configured model and the permission modes', async ({ authenticatedLettaWorkspace, page }) => {
    void authenticatedLettaWorkspace
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, MOCK_MODELS.letta)

    await openPlusMenu(page)
    await expect(settingsGroupTrigger(page, 'model')).toBeVisible()
    await expect(settingsGroupTrigger(page, 'permissionMode')).toBeVisible()
    await closeComposerMenus(page)
  })

  lettaTest('applies a permission-mode change to the session', async ({ authenticatedLettaWorkspace, page, modelScript }) => {
    void authenticatedLettaWorkspace
    await waitForSettingsHydrated(page)
    await modelScript.rule(LETTA_TITLE_RULE)
    await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
    await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)
    await expectAssistantAnswer(page)

    // A settings change reaches the running session. The chip follows the value.
    await waitForSettingsHydrated(page)
    await chooseSettingsOption(page, 'permissionMode-strict')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Strict')
  })

  lettaTest('sends a selected model on the next request and keeps it after reload', async ({ authenticatedLettaWorkspace, page, modelScript }) => {
    void authenticatedLettaWorkspace
    const alternate = `openai/${MOCK_MODELS.openai}`
    await waitForSettingsHydrated(page)
    await chooseSettingsOption(page, `model-${alternate}`)
    await waitForSettingsIdle(page)
    await expectSettingsOptionChosen(page, `model-${alternate}`)

    await modelScript.rule(LETTA_TITLE_RULE)
    await modelScript.queue({ text: 'The selected model answered.' })
    await sendMessage(page, modelScript.prompt('Reply once after the model switch.'))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)
    expect(status.requests.find(request => request.stepIndex === 0)?.body).toMatchObject({ model: MOCK_MODELS.openai })

    await page.reload()
    await waitForSettingsHydrated(page)
    await expectSettingsOptionChosen(page, `model-${alternate}`)
  })
})
