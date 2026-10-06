import { expect } from '@playwright/test'
import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { nativeModelInstructionText } from '../helpers/nativeScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { exitPlanModeToolCall } from '../helpers/providerToolCalls'
import { answerPlanReview, assistantBubbles, chooseSettingsOption, controlBanner, expectSettingsChip, sendMessage, waitForAgentIdle, waitForSettingsIdle } from '../helpers/ui'

for (const approval of [true, false]) {
  deepseekHarnessTest(`routes native plan ${approval ? 'approval' : 'rejection'} and retains the selected native mode`, async ({ native, page, modelScript }) => {
    await chooseSettingsOption(page, 'permissionMode-plan')
    await waitForSettingsIdle(page)
    const plan = '# Exact native plan\n\n1. Inspect the scratch file.\n2. Implement only after approval.'
    const callId = `native-plan-${approval ? 'approve' : 'reject'}`
    const start = await modelScript.queue(
      { toolCalls: [exitPlanModeToolCall(native.provider, callId, plan)] },
      { text: 'The native plan review completed.' },
    )
    await sendMessage(page, modelScript.prompt('Present the exact native plan for review.'))
    await modelScript.waitForSteps(start + 1)
    const banner = controlBanner(page)
    // The native request carries its plan, so the banner draws the plan under its own title, as for every provider that sends the plan.
    await expect(banner).toContainText('Proposed Plan')
    await expect(banner).toContainText('Inspect the scratch file.')
    await answerPlanReview(page, approval ? 'approve' : 'reject')
    await modelScript.waitForSteps(start + 2)
    await waitForAgentIdle(page)
    await expect(banner).toHaveCount(0)
    // The read fails when the plan decision reached no following model request.
    const next = await modelScript.requestAt(start + 1)
    expect(nativeToolResult(next, callId)).toContain(approval ? 'Plan approved' : 'The user chose to keep planning')
    if (approval)
      expect(nativeModelInstructionText(next)).not.toContain('You are in plan mode.')
    else
      expect(nativeModelInstructionText(next)).toContain('You are in plan mode.')
    await expectSettingsChip(page, approval ? 'Act' : 'Plan')
    await expect(assistantBubbles(page).filter({ hasText: 'The native plan review completed.' }).first()).toBeVisible()
    await page.reload()
    await expectSettingsChip(page, approval ? 'Act' : 'Plan')
    await expect(banner).toHaveCount(0)
  })
}
