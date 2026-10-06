import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { bashToolCall, spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { expectNoRegistryRows, expectRowBecomesFinal, openChildTabFromRow, requireRegistryRow } from '../helpers/subagentRegistry'
import { applyPermissionPreset, assistantBubbles, expectSettingsChip, sendMessage, userBubbles, waitForSettingsHydrated } from '../helpers/ui'
import { kimiTest } from '../kimi-fixtures'

const KIMI = AgentProvider.KIMI_CODE

/**
 * The statement that opens a Kimi Code subagent's system prompt, and that the
 * main agent's system prompt never holds. A rule on it answers the child alone,
 * although the root's own requests quote the child's prompt in the spawn call.
 */
const SUBAGENT_SYSTEM = 'You are now running as a subagent'

kimiTest.describe('runs Kimi Code subagents and background tasks', () => {
  // The child's command would stop at a banner under Always Ask. The routing,
  // not the approval, is the subject here.
  kimiTest.beforeEach(async ({ authenticatedKimiWorkspace, page }) => {
    void authenticatedKimiWorkspace
    await waitForSettingsHydrated(page)
    await applyPermissionPreset(page, 'bypass')
    await expectSettingsChip(page, 'Never Ask')
  })

  kimiTest('routes the prompt, tools, and final report into the child tab', async ({ page, modelScript, leapmuxServer }) => {
    await expectNoRegistryRows(page, leapmuxServer)

    await modelScript.rule(
      {
        name: 'the child runs its shell probe',
        when: { system: SUBAGENT_SYSTEM, body: 'printf kimi-child-tool-ok' },
        respond: { toolCalls: [bashToolCall(KIMI, 'child-shell', 'printf kimi-child-tool-ok')] },
        once: true,
      },
      {
        name: 'the child answers after its shell probe',
        when: { system: SUBAGENT_SYSTEM, body: 'printf kimi-child-tool-ok' },
        respond: { text: 'KIMI_CHILD_PONG' },
      },
    )
    await modelScript.queue(
      {
        toolCalls: [spawnSubagentToolCall(KIMI, 'spawn-kimi', {
          description: 'Run the shell probe',
          prompt: modelScript.prompt('Use Bash to run printf kimi-child-tool-ok, then reply with exactly KIMI_CHILD_PONG.'),
        })],
      },
      { text: 'KIMI_ROOT_DONE' },
    )
    await sendMessage(page, modelScript.prompt('Spawn one subagent to run the shell probe, then report what it said.'))
    await modelScript.waitForSteps()
    await expect(assistantBubbles(page).filter({ hasText: 'KIMI_ROOT_DONE' })).not.toHaveCount(0)

    const row = await requireRegistryRow(page)
    await expectRowBecomesFinal(page, row)
    await expect(row).toContainText('Run the shell probe')
    await expect.poll(async () => await row.getAttribute('data-child-agent-id')).not.toBe('')
    await openChildTabFromRow(page, row)

    await expect(userBubbles(page).filter({ hasText: 'printf kimi-child-tool-ok' })).not.toHaveCount(0)
    await expect(page.locator('[data-tool-message]:visible').filter({ hasText: 'kimi-child-tool-ok' }).first()).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: 'KIMI_CHILD_PONG' })).not.toHaveCount(0)
  })
})
