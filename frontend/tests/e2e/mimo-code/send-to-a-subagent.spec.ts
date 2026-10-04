/**
 * A message from the child tab must reach that actual native child. The parent must remain operational.
 *
 * The Worker drives MiMo Code's native HTTP server. MiMo identifies each child actor in its events.
 *
 * MiMo tags each child message with its actor ID. The Worker routes those messages into that child's transcript.
 */
import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { bashToolCall, spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { steerQueuedInput } from '../helpers/steer'
import { openChildTabFromRow, requireRegistryRow } from '../helpers/subagentRegistry'
import { applyPermissionPreset, assistantBubbles, sendMessage, userBubbles, waitForAgentIdle } from '../helpers/ui'
import { MIMO_E2E_SKIP_REASON, mimoTest } from '../mimo-fixtures'

mimoTest.skip(!!MIMO_E2E_SKIP_REASON, MIMO_E2E_SKIP_REASON || '')

mimoTest.describe('MiMo Code subagent registry', () => {
  mimoTest('sends a queued message into a running subagent', async ({ authenticatedMiMoWorkspace, page, modelScript }) => {
    void authenticatedMiMoWorkspace
    await applyPermissionPreset(page, 'bypass')
    const gate = 'mimo-child-send'
    try {
      await modelScript.rule(
        {
          name: 'the child runs its first tool',
          when: { user: '^Reply with CHILD_STEERED' },
          respond: { gate, toolCalls: [bashToolCall(AgentProvider.MIMO_CODE, 'child-shell', 'printf mimo-child-ready')] },
          once: true,
        },
        {
          name: 'the child reads the queued message',
          // MiMo's continuation reuses the main system prompt. The child prompt,
          // shell result, and steered message still share this request body.
          when: { body: ['mimo-child-ready', 'Also say'] },
          respond: { text: '**Status**: success\n**Summary**: replied\n\nCHILD_STEERED' },
          once: true,
        },
      )
      await modelScript.queue(
        {
          toolCalls: [spawnSubagentToolCall(AgentProvider.MIMO_CODE, 'spawn-mimo-send', {
            description: 'Answer the queued message',
            prompt: modelScript.prompt('Reply with CHILD_STEERED after the shell command.'),
          })],
        },
        { text: 'The child received the queued message.' },
      )
      await sendMessage(page, modelScript.prompt('Spawn one subagent for the queued-message task.'))
      await modelScript.waitForGate(gate)

      const row = await requireRegistryRow(page)
      await expect(row).toHaveAttribute('data-status', 'running')
      await expect.poll(async () => await row.getAttribute('data-child-agent-id') ?? '').not.toBe('')
      await openChildTabFromRow(page, row)
      await steerQueuedInput(page, { message: 'Also say CHILD_STEERED.', match: 'Also say' })
      await modelScript.releaseGate(gate)
    }
    finally {
      await modelScript.releaseGateIfHeld(gate)
    }

    await modelScript.waitForSteps()
    await expect.poll(async () => (await modelScript.status()).ruleMatches['the child reads the queued message'] ?? 0).toBe(1)
    await waitForAgentIdle(page)
    const status = await modelScript.status()
    expect(status.ruleMatches['the child reads the queued message']).toBe(1)
    await expect(userBubbles(page).filter({ hasText: /Also say CHILD.*STEERED/ }).first()).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: 'CHILD_STEERED' }).first()).toBeVisible()
  })
})
