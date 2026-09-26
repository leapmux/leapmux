import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { spawnSubagentToolCall } from './helpers/providerToolCalls'
import {
  expectRowBecomesFinal,
  openChildTabFromRow,
  requireRegistryRow,
} from './helpers/subagentRegistry'
import {
  applyPermissionPreset,
  assistantBubbles,
  expectSettingsChip,
  sendMessage,
  waitForAgentIdle,
  waitForSettingsHydrated,
} from './helpers/ui'
import { expect, KIMI_E2E_SKIP_REASON, kimiTest } from './kimi-fixtures'

kimiTest.skip(!!KIMI_E2E_SKIP_REASON, KIMI_E2E_SKIP_REASON || '')

const KIMI = AgentProvider.KIMI_CODE

/** The statement that opens a Kimi Code subagent's system prompt. */
const SUBAGENT_SYSTEM = 'You are now running as a subagent'

kimiTest.describe('sends to a Kimi Code subagent', () => {
  kimiTest.beforeEach(async ({ authenticatedKimiWorkspace, page }) => {
    void authenticatedKimiWorkspace
    await waitForSettingsHydrated(page)
    await applyPermissionPreset(page, 'bypass')
    await expectSettingsChip(page, 'Never Ask')
  })

  // Kimi Code runs a subagent as a task of its session. The child tab's
  // composer accepts a message that becomes the child's next turn, which is
  // what the matrix calls "Send to a subagent".
  kimiTest('a message sent from the subagent tab becomes the child\'s next turn', async ({ page, modelScript }) => {
    await modelScript.rule(
      {
        name: 'the child answers its first prompt',
        when: { system: SUBAGENT_SYSTEM, user: 'first probe' },
        respond: { text: 'KIMI_CHILD_FIRST' },
        once: true,
      },
      {
        name: 'the child answers the follow-up',
        when: { system: SUBAGENT_SYSTEM, user: 'follow up' },
        respond: { text: 'KIMI_CHILD_FOLLOWUP' },
      },
    )
    await modelScript.queue(
      {
        toolCalls: [spawnSubagentToolCall(KIMI, 'spawn-kimi', {
          description: 'Run the first probe',
          prompt: modelScript.prompt('Reply with exactly KIMI_CHILD_FIRST.'),
        })],
      },
      { text: 'KIMI_ROOT_DONE' },
    )
    await sendMessage(page, modelScript.prompt('Spawn one subagent to run the first probe, then report what it said.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    await expect(assistantBubbles(page).filter({ hasText: 'KIMI_ROOT_DONE' })).not.toHaveCount(0)

    const row = await requireRegistryRow(page)
    await expectRowBecomesFinal(page, row)
    await openChildTabFromRow(page, row)
    await expect(assistantBubbles(page).filter({ hasText: 'KIMI_CHILD_FIRST' })).not.toHaveCount(0)

    // The child tab is active. Its composer sends the child's next turn.
    await sendMessage(page, modelScript.prompt('Now reply with exactly KIMI_CHILD_FOLLOWUP.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    await expect(assistantBubbles(page).filter({ hasText: 'KIMI_CHILD_FOLLOWUP' })).not.toHaveCount(0)
  })
})
