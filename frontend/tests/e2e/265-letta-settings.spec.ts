import { MOCK_MODELS } from './helpers/mockAgentEnvironment'
import {
  ARITHMETIC_ANSWER_TEXT,
  ARITHMETIC_PROMPT,
  chooseSettingsOption,
  closeComposerMenus,
  expectAssistantAnswer,
  expectSettingsChip,
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
 * The isolated `providers/auth.json` pins `openai-compatible`, and the model
 * handle is `provider/model`, so the model group offers the configured one. The
 * permission-mode axis maps onto `runtime_start.mode`; a change there reaches the
 * running session as an `update_model` command where the axis supports it, and
 * needs a restart for the mode.
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
})
