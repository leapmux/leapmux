import { DROID_E2E_SKIP_REASON, DROID_TITLE_RULE, droidTest, expect } from './droid-fixtures'
import {
  ARITHMETIC_ANSWER_TEXT,
  ARITHMETIC_PROMPT,
  chooseSettingsOption,
  closeComposerMenus,
  expectAssistantAnswer,
  expectSettingsOptionChosen,
  openPlusMenu,
  sendMessage,
  settingsGroupTrigger,
  waitForAgentIdle,
  waitForSettingsHydrated,
  waitForSettingsIdle,
} from './helpers/ui'

/**
 * 261 — Factory Droid settings.
 *
 * The isolated BYOK settings pin `custom:Droid-0`, and the session reports the
 * models of that configuration. The permission-mode axis maps onto Droid's own
 * autonomy axis, so a change there reaches the running session as
 * `droid.update_session_settings`.
 */
droidTest.skip(!!DROID_E2E_SKIP_REASON, DROID_E2E_SKIP_REASON || '')

droidTest.describe('Factory Droid settings', () => {
  droidTest('offers the model, effort and permission-mode groups', async ({ authenticatedDroidWorkspace, page }) => {
    void authenticatedDroidWorkspace
    await waitForSettingsHydrated(page)
    await openPlusMenu(page)
    await expect(settingsGroupTrigger(page, 'model')).toBeVisible()
    await expect(settingsGroupTrigger(page, 'effort')).toBeVisible()
    await expect(settingsGroupTrigger(page, 'permissionMode')).toBeVisible()
    await closeComposerMenus(page)
  })

  droidTest('applies a permission-mode change to the running session', async ({ authenticatedDroidWorkspace, page, modelScript }) => {
    void authenticatedDroidWorkspace
    await waitForSettingsHydrated(page)
    await modelScript.rule(DROID_TITLE_RULE)
    await modelScript.queue({ text: ARITHMETIC_ANSWER_TEXT })
    await sendMessage(page, modelScript.prompt(ARITHMETIC_PROMPT))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 180_000)
    await expectAssistantAnswer(page)

    // A settings change reaches the running session without a restart.
    await waitForSettingsHydrated(page)
    await chooseSettingsOption(page, 'permissionMode-auto-high')
    await waitForSettingsIdle(page)
    // The change reached the session. The menu states the choice.
    await expectSettingsOptionChosen(page, 'permissionMode-auto-high')
  })
})
