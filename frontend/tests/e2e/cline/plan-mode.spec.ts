import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { CLINE_E2E_SKIP_REASON, clineTest, offeredTools } from '../cline-fixtures'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { expectSettingsChip, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'

/**
 * The composer changes the actual native Plan setting. The next native operation must follow the selected policy.
 *
 * The Worker starts one private Cline hub for this agent. Cline's DeepSeek provider sends requests to the isolated mock.
 *
 * Cline fixes tools and system instructions when it creates a session. A Plan or Act change recreates the same session with its earlier messages.
 */
clineTest.skip(!!CLINE_E2E_SKIP_REASON, CLINE_E2E_SKIP_REASON || '')

clineTest.describe('Cline settings', () => {
  clineTest('Shift+Tab toggles Plan mode from the composer', async ({ askingClineWorkspace, page, modelScript, leapmuxServer }) => {
    const context = { page, modelScript, leapmuxServer, workspaceId: askingClineWorkspace.workspaceId, provider: AgentProvider.CLINE }
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Act')

    const editor = page.locator('[data-testid="composer-editor"] .ProseMirror')
    await editor.click()
    await page.keyboard.press('Shift+Tab')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Plan')
    for (const reload of [false, true]) {
      if (reload) {
        await page.reload()
        await waitForSettingsHydrated(page)
        await expectSettingsChip(page, 'Plan')
      }
      const plan = await sendNativeAnswer(context, 'Reply under the selected native mode.', reload ? 'The restored Plan mode answered.' : 'The selected Plan mode answered.')
      expect(offeredTools(plan.body)).toContain('switch_to_act_mode')
      expect(offeredTools(plan.body)).not.toContain('editor')
    }

    await editor.click()
    await page.keyboard.press('Shift+Tab')
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Act')
    const act = await sendNativeAnswer(context, 'Reply under the restored native execution mode.', 'The restored Act mode answered.')
    expect(offeredTools(act.body)).toContain('editor')
    expect(offeredTools(act.body)).not.toContain('switch_to_act_mode')
  })
})
