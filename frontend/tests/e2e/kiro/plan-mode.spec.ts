import { expect } from '@playwright/test'
import { kiroSwitchToExecutionToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, expectSettingsChip, messageBubbles, openWorkspace, sendMessage, waitForAgentIdle, waitForSettingsHydrated } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'
import { KIRO_AGENT, kiroTest } from '../kiro-fixtures'

kiroTest.describe('Kiro settings', () => {
  // Kiro's Plan mode ends through `switch_to_execution`, which raises no approval.
  // The native tool result acknowledges the switch. Kiro then sends the plan to Default mode in the same turn.
  // No mode update separates those answers. The mode chip follows, and each answer appears in a separate message.
  kiroTest('leaves plan mode through the plan switch', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, KIRO_AGENT, { optionValues: { permissionMode: 'plan' } })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Plan')

    const start = await modelScript.queue(
      { toolCalls: [kiroSwitchToExecutionToolCall('kiro-plan', '1. Write the parser.\n2. Test it.')] },
      { text: 'PLAN_HANDED_OFF' },
      { text: 'EXECUTING_THE_PLAN' },
    )
    await sendMessage(page, modelScript.prompt('Finish planning and start.'))
    await modelScript.waitForSteps(start + 3)
    await waitForAgentIdle(page)

    await expectSettingsChip(page, 'Default')
    expect(JSON.stringify((await modelScript.requestAt(start + 1)).body), 'the plan mode answers the switch').toContain('"agentMode":"plan"')
    expect(JSON.stringify((await modelScript.requestAt(start + 2)).body), 'the default mode runs the plan').toContain('"agentMode":"vibe"')
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
