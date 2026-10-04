import { expect } from '@playwright/test'

import { OPTION_ID_PERMISSION_MODE } from '../../../src/components/chat/settingsGroups'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exitPlanModeToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, expectSettingsChip, openWorkspace, sendMessage, visibleControlBanner, visibleOnly, waitForAgentIdle } from '../helpers/ui'

import { openQwenAgent, QWEN_E2E_SKIP_REASON, qwenTest } from '../qwen-fixtures'

qwenTest.skip(!!QWEN_E2E_SKIP_REASON, QWEN_E2E_SKIP_REASON || '')

const PROVIDER = AgentProvider.QWEN_CODE

qwenTest.describe('Qwen Code control requests', () => {
  qwenTest('approves a plan and leaves plan mode', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    await openQwenAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, { [OPTION_ID_PERMISSION_MODE]: 'plan' })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await expectSettingsChip(page, 'Plan')

    await modelScript.queue(
      { toolCalls: [exitPlanModeToolCall(PROVIDER, 'qwen-plan', '# Qwen probe plan\n\n1. Change no files.')] },
      { text: 'Plan approved; starting.' },
    )
    await sendMessage(page, modelScript.prompt('Plan the probe, then ask for approval.'))
    await modelScript.waitForSteps(1)
    const banner = visibleControlBanner(page)
    // The request carries the plan itself, so the banner draws it.
    await expect(banner).toContainText('Proposed Plan')
    await expect(banner).toContainText('Change no files.')
    await page.getByTestId('plan-approve-btn').filter({ visible: true }).click()
    await expect(banner).toHaveCount(0)
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)
    await expect(assistantBubbles(page).filter({ hasText: 'Plan approved; starting.' })).toBeVisible()
    // Qwen reports the mode it left plan mode for, and the chip follows it.
    await expectSettingsChip(page, 'Default')
  })

  // Approval with a fresh context replaces the session in place.
  // The old turn waits for plan approval. Clear answers that request and cancels the old turn before the new session opens.
  // The approved plan runs in the new session. No old-session output reaches the reader after that replacement.
  qwenTest('approves a plan with a fresh context and runs it in the new session', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    await openQwenAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, { [OPTION_ID_PERMISSION_MODE]: 'plan' })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await expectSettingsChip(page, 'Plan')

    // The clear answers the old approval `cancelled`, which Qwen reads as "keep
    // planning", and the cancel of the old turn follows it. The old session can
    // ask the model once in between, so its turn count is not this test's to fix.
    await modelScript.fallback({ text: 'Still planning in the old session.' })
    // The new session receives the stored plan, with the marker that the plan
    // carries, so its request reaches this script.
    await modelScript.rule({
      name: 'the approved plan runs in the new session',
      when: { body: 'Execute the following plan' },
      respond: { text: 'Running the approved plan in a fresh context.' },
    })
    await modelScript.queue({
      toolCalls: [exitPlanModeToolCall(PROVIDER, 'qwen-fresh-plan', modelScript.prompt('# Qwen fresh plan\n\n1. Change no files.'))],
    })
    await sendMessage(page, modelScript.prompt('Plan the probe, then ask for approval.'))
    await modelScript.waitForSteps(1)
    const banner = visibleControlBanner(page)
    await expect(banner).toContainText('Proposed Plan')
    const clearContext = page.locator('[data-testid="plan-clear-context-checkbox"] input[type="checkbox"]')
    await clearContext.check()
    await expect(clearContext).toBeChecked()
    await page.getByTestId('plan-approve-btn').filter({ visible: true }).click()
    await expect(banner).toHaveCount(0)

    await expect(visibleOnly(page.getByText('Context cleared'))).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: 'Running the approved plan in a fresh context.' })).toBeVisible()
    await waitForAgentIdle(page, 120_000)
    await expect(banner).toHaveCount(0)
    await expect(assistantBubbles(page).filter({ hasText: 'Still planning in the old session.' })).toHaveCount(0)
  })
})
