import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import type { NativeChildProfile } from '../helpers/runningChildProof'
import { expect } from '@playwright/test'
import { withCleanup } from '../helpers/cleanup'
import { bashToolCall, spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { printfMarkerCommand } from '../helpers/shellArguments'
import { steerQueuedInput } from '../helpers/steer'
import { openChildTabFromRow, requireRegistryRow } from '../helpers/subagentRegistry'
import { applyPermissionPreset, assistantBubbles, sendMessage, userBubbles, waitForAgentIdle } from '../helpers/ui'

/** What one member of the OpenCode family does with a message that reaches a running subagent. */
export interface OpenCodeFamilyChildSend {
  /** The matcher of the child's own turns. */
  profile: NativeChildProfile
  /**
   * Whether the child reads the message only after its running turn ends, as a turn of its own.
   * OpenCode adds the message to the running turn: the child's next request after its tool holds it.
   * Kilo holds a prompt that arrives during a turn until that turn ends, so the child first answers its tool.
   */
  readsAfterTurn: boolean
}

/** The words of the message that the reader sends to the child, and of the child's answer to it. */
const STEER_MESSAGE = 'Also say CHILD_STEERED.'
const STEERED_ANSWER = 'CHILD_STEERED'

/**
 * Send a message from the tab of a running subagent, and require that the subagent's own model reads it and that its
 * answer shows in the subagent's tab. The child's first request holds at a gate, so the message arrives while the child
 * runs. Bypass keeps the child's shell command from asking.
 */
export async function exerciseOpenCodeFamilyChildSend(native: ManagedNativeScenarioContext, member: OpenCodeFamilyChildSend): Promise<void> {
  const { page, modelScript } = native
  await applyPermissionPreset(page, 'bypass')
  const gate = 'opencode-family-child-send'
  const childTask = 'Run the child marker command, then wait for the reader.'
  const command = printfMarkerCommand('CHILD_READY_', 42)
  const steeredRule = 'the child reads the message from its tab'
  const start = await withCleanup(async () => {
    await modelScript.rule(
      {
        name: 'the child runs its first tool',
        when: member.profile.childTask(childTask),
        respond: { gate, toolCalls: [bashToolCall(native.provider, 'child-shell', command)] },
        once: true,
      },
      ...(member.readsAfterTurn
        ? [{
            name: 'the child answers its tool before it reads the message',
            when: { ...member.profile.childTask(childTask), lastMessage: { role: 'tool' as const } },
            respond: { text: 'The child marker command ran.' },
            once: true,
          }]
        : []),
      {
        name: steeredRule,
        when: { user: '^Also say CHILD_STEERED' },
        respond: { text: STEERED_ANSWER },
        once: true,
      },
    )
    const queued = await modelScript.queue(
      {
        toolCalls: [spawnSubagentToolCall(native.provider, 'spawn-child-send', {
          description: 'Answer the message from the child tab',
          prompt: modelScript.prompt(childTask),
        })],
      },
      { text: 'The child read the message from its tab.' },
    )
    await sendMessage(page, modelScript.prompt('Spawn one subagent for the message from its tab.'))
    await modelScript.waitForGate(gate)

    const row = await requireRegistryRow(page)
    await expect(row).toHaveAttribute('data-status', 'running')
    // `openChildTabFromRow` waits until the row links a child agent.
    await openChildTabFromRow(page, row)
    await steerQueuedInput(page, { message: STEER_MESSAGE, match: 'Also say' })
    await modelScript.releaseGate(gate)
    return queued
  }, async () => {
    await modelScript.releaseGateIfHeld(gate)
  })

  await modelScript.waitForSteps(start + 2)
  await expect.poll(async () => (await modelScript.status()).ruleMatches[steeredRule] ?? 0).toBe(1)
  await waitForAgentIdle(page)
  await expect(userBubbles(page).filter({ hasText: /Also say CHILD.*STEERED/ }).first()).toBeVisible()
  await expect(assistantBubbles(page).filter({ hasText: STEERED_ANSWER }).first()).toBeVisible()
}
