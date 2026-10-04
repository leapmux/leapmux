import { expect } from '@playwright/test'
import { kiroSwitchToExecutionToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, expectSettingsChip, messageBubbles, openWorkspace, sendMessage, waitForAgentIdle, waitForSettingsHydrated } from '../helpers/ui'
import { KIRO_E2E_SKIP_REASON, kiroTest, openKiroAgent } from '../kiro-fixtures'

kiroTest.skip(!!KIRO_E2E_SKIP_REASON, KIRO_E2E_SKIP_REASON || '')

kiroTest.describe('Kiro settings', () => {
  // Kiro's Plan mode ends through `switch_to_execution`, which raises no approval.
  // The native tool result acknowledges the switch. Kiro then sends the plan to Default mode in the same turn.
  // No mode update separates those answers. The mode chip follows, and each answer appears in a separate message.
  kiroTest('leaves plan mode through the plan switch', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    await openKiroAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, { permissionMode: 'plan' })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Plan')

    await modelScript.queue(
      { toolCalls: [kiroSwitchToExecutionToolCall('kiro-plan', '1. Write the parser.\n2. Test it.')] },
      { text: 'PLAN_HANDED_OFF' },
      { text: 'EXECUTING_THE_PLAN' },
    )
    await sendMessage(page, modelScript.prompt('Finish planning and start.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)

    await expectSettingsChip(page, 'Default')
    const requests = (await modelScript.status()).requests
    expect(JSON.stringify(requests.find(request => request.stepIndex === 1)?.body), 'the plan mode answers the switch').toContain('"agentMode":"plan"')
    expect(JSON.stringify(requests.find(request => request.stepIndex === 2)?.body), 'the default mode runs the plan').toContain('"agentMode":"vibe"')
    // The call's row states the plan. A result row beside its call row draws no
    // tool-message wrapper of its own, so the row is found by its bubble.
    await expect(messageBubbles(page).filter({ hasText: 'Switch to execution' }).first()).toBeVisible()
    await expect(messageBubbles(page).filter({ hasText: 'Write the parser.' }).first()).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: 'PLAN_HANDED_OFF' })).toHaveCount(1)
    await expect(assistantBubbles(page).filter({ hasText: 'EXECUTING_THE_PLAN' })).toHaveCount(1)
    const joined = assistantBubbles(page).filter({ hasText: 'PLAN_HANDED_OFF' }).filter({ hasText: 'EXECUTING_THE_PLAN' })
    await expect(joined, 'the two answers are two messages').toHaveCount(0)
  })
})
