import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import type { NativeChildProfile } from '../helpers/runningChildProof'
import { expect } from '@playwright/test'
import { spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { expectNoRegistryRows, expectRowBecomesFinal, expectSectionPersists, openChildTabFromRow, requireRegistryRow } from '../helpers/subagentRegistry'
import { applyPermissionPreset, sendMessage, subagentReportBubble, userBubbles } from '../helpers/ui'
import { readReasonixChildTaskId, reasonixChildTaskMatcher } from './childIdentity'

/**
 * The facts of a held Reasonix child.
 * The Bypass preset lets the read-only task run without a permission request. The task receipt of the parent selects
 * the child by its spawn call, so the row title does not.
 */
export const REASONIX_CHILD: NativeChildProfile = {
  childTask: reasonixChildTaskMatcher,
  rowTitleHoldsDescription: false,
  prepare: context => applyPermissionPreset(context.page, 'bypass'),
  resolveTaskId: (context, parentId, child) => readReasonixChildTaskId(context, parentId, child.spawn.id, child.prompt),
}

/**
 * Spawn one read-only child that answers one word, then follow its row from running to final, and its prompt and
 * report into its own tab. The transcript tab cell and the background task cell both run this scenario.
 */
export async function exerciseReasonixSpawnTranscript(context: ManagedNativeScenarioContext): Promise<void> {
  const { page, modelScript } = context
  await expectNoRegistryRows(page, context.leapmuxServer)

  // The child's prompt carries the marker, so the turns it runs on its own
  // reach this script. NOT anchored: Reasonix opens a child turn with a
  // host-injected `<subagent-context event="SubagentStart">` block, so `^`
  // never matches the prompt. It needs no anchor either, because Reasonix
  // keeps a tool result out of the user turn, so no parent turn carries a
  // copy of the child's prompt.
  await modelScript.rule({
    name: 'the child answers its one-word task',
    when: { user: 'Reply with the single word PONG' },
    respond: { text: 'PONG' },
  })
  const start = await modelScript.queue(
    {
      toolCalls: [spawnSubagentToolCall(context.provider, 'spawn-reasonix', {
        description: 'Ask the subagent for one word',
        prompt: modelScript.prompt('Reply with the single word PONG.'),
      })],
    },
    { text: 'The subagent reported PONG.' },
  )
  await sendMessage(page, modelScript.prompt('Delegate one word to a read-only subagent.'))
  await modelScript.waitForSteps(start + 2)

  const row = await requireRegistryRow(page)
  await expectRowBecomesFinal(page, row)
  await expectSectionPersists(page)
  await openChildTabFromRow(page, row)
  await expect(userBubbles(page).filter({ hasText: 'PONG' })).toBeVisible()
  await expect(subagentReportBubble(page, /PONG/)).toBeVisible()
}
