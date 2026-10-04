import { expect } from '@playwright/test'

import { OPTION_ID_PERMISSION_MODE } from '../../../src/components/chat/settingsGroups'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { GROK_E2E_SKIP_REASON, grokTest, openGrokAgent } from '../grok-fixtures'
import { exitPlanModeToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, expectSettingsChip, openWorkspace, sendMessage, visibleControlBanner, waitForAgentIdle } from '../helpers/ui'

grokTest.skip(!!GROK_E2E_SKIP_REASON, GROK_E2E_SKIP_REASON || '')

const PROVIDER = AgentProvider.GROK_BUILD

grokTest.describe('Grok Build control requests', () => {
  // Grok's plan approval is a request of its own. Approve takes the shared plan
  // surface, and Grok leaves plan mode itself, which the chip then follows.
  grokTest('approves a plan and leaves plan mode', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    await openGrokAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, { [OPTION_ID_PERMISSION_MODE]: 'plan' })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await expectSettingsChip(page, 'Plan')

    await modelScript.queue(
      { toolCalls: [exitPlanModeToolCall(PROVIDER, 'grok-plan', '')] },
      { text: 'Plan approved; starting.' },
    )
    await sendMessage(page, modelScript.prompt('Finish planning and ask for approval.'))
    await modelScript.waitForSteps(1)
    const banner = visibleControlBanner(page)
    await expect(banner).toContainText('Plan Ready for Review')
    await page.getByTestId('plan-approve-btn').filter({ visible: true }).click()
    await expect(banner).toHaveCount(0)
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)
    await expect(assistantBubbles(page).filter({ hasText: 'Plan approved; starting.' })).toBeVisible()
    await expectSettingsChip(page, 'Default')
  })
})
