import { DROID_TITLE_RULE, droidTest, expect } from '../droid-fixtures'
import { droidNativeSettingsUpdates } from '../helpers/droidNativeSettings'
import { assistantBubbles, expectSettingsChip, sendMessage, waitForAgentIdle, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'

droidTest.describe('Factory Droid Spec mode', () => {
  droidTest('Shift+Tab selects the native Spec mode and offers ExitSpecMode', async ({ askingDroidWorkspace, page, modelScript, leapmuxServer }) => {
    await waitForSettingsHydrated(page)
    const editor = page.locator('[data-testid="composer-editor"] .ProseMirror')
    await editor.click()
    await page.keyboard.press('Shift+Tab')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Spec')
    await expect.poll(async () => (await droidNativeSettingsUpdates(leapmuxServer, askingDroidWorkspace.workspaceId)).some(update =>
      update.requestId?.startsWith('leapmux-') && update.interactionMode === 'spec' && update.autonomyLevel === 'off')).toBe(true)

    await modelScript.rule(DROID_TITLE_RULE)
    await modelScript.queue({ text: 'Spec mode is active.' })
    await sendMessage(page, modelScript.prompt('Reply while Spec mode is active.'))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    expect(JSON.stringify(status.requests.find(request => request.stepIndex === 0)?.body)).toContain('"name":"ExitSpecMode"')
    await expect(assistantBubbles(page).filter({ hasText: 'Spec mode is active.' }).first()).toBeVisible()

    await editor.click()
    await page.keyboard.press('Shift+Tab')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Default')
  })
})
