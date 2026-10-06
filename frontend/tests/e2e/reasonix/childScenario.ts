import type { MockModelRule } from '../helpers/mockModelScript'
import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import type { NativeChildProfile } from '../helpers/runningChildProof'
import { expect } from '@playwright/test'
import { spawnSubagentToolCall } from '../helpers/providerToolCalls'
import { expectNoRegistryRows, expectRowBecomesFinal, expectSectionPersists, openChildTabFromRow, requireRegistryRow } from '../helpers/subagentRegistry'
import { sendMessage, subagentReportBubble, userBubbles } from '../helpers/ui'
import { readReasonixChildTaskId, reasonixChildTaskMatcher } from './childIdentity'
import { bypassToolRequests } from './scenarios'

/**
 * The facts of a held Reasonix child.
 * The Bypass preset lets the read-only task run without a permission request. The task receipt of the parent selects
 * the child by its spawn call, so the row title does not.
 */
export const REASONIX_CHILD: NativeChildProfile = {
  childTask: reasonixChildTaskMatcher,
  rowTitleHoldsDescription: false,
  prepare: bypassToolRequests,
  resolveTaskId: (context, parentId, child) => readReasonixChildTaskId(context, parentId, child.spawn.id, child.prompt),
}

/** The task of the child that {@link exerciseReasonixSpawnTranscript} spawns. */
export const REASONIX_SPAWN_TASK = 'Reply with the single word PONG.'

/**
 * The rule that answers the turns of the child that {@link exerciseReasonixSpawnTranscript} spawns.
 * Reasonix opens a child turn with its own context pack, so the task never starts the turn. The matcher of
 * {@link REASONIX_CHILD} anchors the start of that pack and the task section inside it, so a turn that only quotes the
 * task does not match.
 */
export function reasonixSpawnChildRule(): MockModelRule {
  return {
    name: 'the child answers its one-word task',
    when: REASONIX_CHILD.childTask(REASONIX_SPAWN_TASK),
    respond: { text: 'PONG' },
  }
}

/**
 * Spawn one read-only child that answers one word, then follow its row from running to final, and its prompt and
 * report into its own tab. The transcript tab cell and the background task cell both run this scenario.
 */
export async function exerciseReasonixSpawnTranscript(context: ManagedNativeScenarioContext): Promise<void> {
  const { page, modelScript } = context
  await expectNoRegistryRows(page, context.leapmuxServer)

  // The child's prompt carries the marker, so the turns it runs on its own
  // reach this script.
  await modelScript.rule(reasonixSpawnChildRule())
  const start = await modelScript.queue(
    {
      toolCalls: [spawnSubagentToolCall(context.provider, 'spawn-reasonix', {
        description: 'Ask the subagent for one word',
        prompt: modelScript.prompt(REASONIX_SPAWN_TASK),
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
