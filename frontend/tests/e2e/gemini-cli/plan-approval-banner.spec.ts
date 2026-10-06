import { join } from 'node:path'
import { expect } from '@playwright/test'
import { geminiTest } from '../gemini-fixtures'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { geminiPlanApprovalToolCall, writeToolCall } from '../helpers/providerToolCalls'
import { answerPlanReview, assistantBubbles, chooseSettingsOption, controlBanner, expectSettingsOptionChosen, sendMessage, waitForAgentIdle, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'
import { geminiNativeProject } from './nativeStore'

for (const decision of ['approve', 'reject'] as const) {
  geminiTest(`reads the complete native plan file before ${decision}`, async ({ native, page, modelScript }) => {
    await chooseSettingsOption(page, 'permissionMode-plan')
    await waitForSettingsIdle(page)
    const agent = await currentNativeAgent(native)
    const filename = `native-${decision}-plan.md`
    const path = join(geminiNativeProject(native, agent), agent.agentSessionId, 'plans', filename)
    const plan = '# Native complete plan\n\n1. Read the actual source.\n2. Apply only the approved change.\n'
    const start = await modelScript.queue(
      { toolCalls: [writeToolCall(native.provider, 'gemini-plan-write', { path, content: plan })] },
      { toolCalls: [geminiPlanApprovalToolCall('gemini-plan-exit', filename)] },
      { text: `The native plan ${decision} operation completed.` },
    )
    await sendMessage(page, modelScript.prompt('Write the native plan, then request its approval.'))
    await modelScript.waitForSteps(start + 2)
    const banner = controlBanner(page)
    await expect(banner).toContainText('Proposed Plan')
    await expect(banner).toContainText('Read the actual source.')
    await expect(banner).toContainText('Apply only the approved change.')
    await answerPlanReview(page, decision)
    await expect(banner).toHaveCount(0)
    await modelScript.waitForSteps(start + 3)
    await waitForAgentIdle(page)
    const request = await modelScript.requestAt(start + 2)
    expect(nativeToolResult(request, 'gemini-plan-exit')).toMatch(decision === 'approve' ? /Plan approved/ : /User cancelled|Plan rejected|Tool \\"exit_plan_mode\\" was canceled by the user\./i)
    await expect(assistantBubbles(page).filter({ hasText: `The native plan ${decision} operation completed.` })).toBeVisible()
    await page.reload()
    await waitForSettingsHydrated(page)
    await expectSettingsOptionChosen(page, `permissionMode-${decision === 'approve' ? 'default' : 'plan'}`)
  })
}
