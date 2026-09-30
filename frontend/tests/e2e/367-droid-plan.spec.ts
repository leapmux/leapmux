import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { DROID_E2E_SKIP_REASON, DROID_TITLE_RULE, droidTest, expect } from './droid-fixtures'
import { droidNativeSettingsUpdates } from './helpers/droidNativeSettings'
import { exitPlanModeToolCall } from './helpers/providerToolCalls'
import {
  assistantBubbles,
  chooseSettingsOption,
  expectSettingsChip,
  expectSettingsOptionChosen,
  sendMessage,
  waitForAgentIdle,
  waitForControlBanner,
  waitForSettingsHydrated,
  waitForSettingsIdle,
} from './helpers/ui'

droidTest.skip(!!DROID_E2E_SKIP_REASON, DROID_E2E_SKIP_REASON || '')

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

  droidTest('shows the native plan review and returns to Default after approval', async ({ askingDroidWorkspace, page, modelScript, leapmuxServer }) => {
    await waitForSettingsHydrated(page)
    await chooseSettingsOption(page, 'permissionMode-spec')
    await waitForSettingsIdle(page)
    await expectSettingsOptionChosen(page, 'permissionMode-spec')
    await modelScript.rule(DROID_TITLE_RULE)
    await modelScript.queue(
      { toolCalls: [exitPlanModeToolCall(AgentProvider.DROID, 'exit-spec-1', '# Native plan\n\n- Apply the change.')] },
      { text: 'The plan was approved.' },
    )
    await sendMessage(page, modelScript.prompt('Present the native plan for approval.'))
    await modelScript.waitForSteps(1)

    const banner = await waitForControlBanner(page)
    await expect(banner).toContainText('Plan Ready for Review')
    await page.getByTestId('plan-approve-btn').click()
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    await expect(banner).toHaveCount(0)
    await expect(assistantBubbles(page).filter({ hasText: 'The plan was approved.' }).first()).toBeVisible()
    await expectSettingsOptionChosen(page, 'permissionMode-default')
    await expect.poll(async () => {
      const updates = await droidNativeSettingsUpdates(leapmuxServer, askingDroidWorkspace.workspaceId)
      return updates.at(-1)?.interactionMode === 'auto' && updates.at(-1)?.autonomyLevel === 'off'
    }).toBe(true)
  })
})
